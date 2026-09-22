package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

type stubCartaStore struct {
	themes         []models.CartaTheme
	themeMeta      models.CartaMeta
	subthemes      map[string][]models.CartaSubtheme
	services       map[string][]models.CartaServiceSummary
	details        map[string]json.RawMessage
	err            error
	lastPage       int
	lastPerPage    int
	lastIncludeEmp bool
	lastThemeSlug  string
	lastSubSlug    string
}

func (s *stubCartaStore) ListThemes(ctx context.Context, page, perPage int, includeEmpty bool) ([]models.CartaTheme, models.CartaMeta, error) {
	s.lastPage = page
	s.lastPerPage = perPage
	s.lastIncludeEmp = includeEmpty
	if s.err != nil {
		return nil, models.CartaMeta{}, s.err
	}
	return s.themes, s.themeMeta, nil
}

func (s *stubCartaStore) ListSubthemes(ctx context.Context, themeSlug string, page, perPage int) ([]models.CartaSubtheme, models.CartaMeta, error) {
	s.lastThemeSlug = themeSlug
	s.lastPage = page
	s.lastPerPage = perPage
	if s.err != nil {
		return nil, models.CartaMeta{}, s.err
	}
	items := s.subthemes[themeSlug]
	return items, models.CartaMeta{Page: page, PerPage: perPage, Total: len(items), TotalPages: 1}, nil
}

func (s *stubCartaStore) ListServicesBySubtheme(ctx context.Context, subthemeSlug string, page, perPage int) ([]models.CartaServiceSummary, models.CartaMeta, error) {
	s.lastSubSlug = subthemeSlug
	s.lastPage = page
	s.lastPerPage = perPage
	if s.err != nil {
		return nil, models.CartaMeta{}, s.err
	}
	items := s.services[subthemeSlug]
	return items, models.CartaMeta{Page: page, PerPage: perPage, Total: len(items), TotalPages: 1}, nil
}

func (s *stubCartaStore) GetServiceDetail(ctx context.Context, slug string) (json.RawMessage, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.details[slug], nil
}

func setupCartaRouter(h *CartaHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/public/themes", h.ListThemes)
	r.GET("/api/public/themes/:slug/subthemes", h.ListSubthemes)
	r.GET("/api/public/subthemes/:slug/services", h.ListServicesBySubtheme)
	r.GET("/api/public/services/:slug", h.GetService)
	return r
}

func callWithSlug(t *testing.T, h *CartaHandler, method func(*CartaHandler, *gin.Context), slug string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Params = gin.Params{{Key: "slug", Value: slug}}
	method(h, c)
	return w
}

func TestCartaHandler_ListThemesLocal(t *testing.T) {
	store := &stubCartaStore{
		themes:    []models.CartaTheme{{Slug: "tributos", Name: "Tributos", PublishedServices: 3}},
		themeMeta: models.CartaMeta{Page: 1, PerPage: 100, Total: 1, TotalPages: 1},
	}
	h := newCartaHandler(store)
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/themes?page=2&per_page=10&include_empty=true", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if store.lastPage != 2 || store.lastPerPage != 10 || !store.lastIncludeEmp {
		t.Fatalf("pagination/include_empty: page=%d per=%d empty=%v", store.lastPage, store.lastPerPage, store.lastIncludeEmp)
	}
	var body cartaPagedJSON
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	data, ok := body.Data.([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("data=%v", body.Data)
	}
}

func TestCartaHandler_ListThemesEmpty(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{
		themes:    nil,
		themeMeta: models.CartaMeta{Page: 1, PerPage: 100, Total: 0, TotalPages: 0},
	})
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/themes", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["data"]) != "[]" {
		t.Fatalf("expected empty array, got %s", body["data"])
	}
}

func TestCartaHandler_ListThemesStoreError(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{err: errors.New("db down")})
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/themes", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestCartaHandler_UnavailableLocal(t *testing.T) {
	h := NewCartaHandler(nil)
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/themes", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestCartaHandler_GetServiceLocal(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{
		details: map[string]json.RawMessage{
			"iptu": json.RawMessage(`{"slug":"iptu","name":"IPTU"}`),
		},
	})
	r := setupCartaRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/services/iptu", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var body cartaDetailJSON
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body.Data) != `{"slug":"iptu","name":"IPTU"}` {
		t.Fatalf("data=%s", body.Data)
	}

	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/public/services/missing", nil))
	if w2.Code != http.StatusNotFound {
		t.Fatalf("status=%d", w2.Code)
	}
}

func TestCartaHandler_GetServiceEmptySlug(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{})
	w := callWithSlug(t, h, (*CartaHandler).GetService, "  ")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestCartaHandler_GetServiceStoreError(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{err: errors.New("db down")})
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/services/iptu", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestCartaHandler_ListSubthemesAndServicesLocal(t *testing.T) {
	store := &stubCartaStore{
		subthemes: map[string][]models.CartaSubtheme{"tributos": {{Slug: "iptu", Name: "IPTU"}}},
		services:  map[string][]models.CartaServiceSummary{"iptu": {{Slug: "iptu-cobranca", Name: "Cobrança"}}},
	}
	h := newCartaHandler(store)
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/themes/tributos/subthemes?page=3&per_page=5", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("subthemes=%d", w.Code)
	}
	if store.lastThemeSlug != "tributos" || store.lastPage != 3 || store.lastPerPage != 5 {
		t.Fatalf("subthemes args: slug=%s page=%d per=%d", store.lastThemeSlug, store.lastPage, store.lastPerPage)
	}
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/public/subthemes/iptu/services", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("services=%d", w2.Code)
	}
	if store.lastSubSlug != "iptu" {
		t.Fatalf("subslug=%s", store.lastSubSlug)
	}
}

func TestCartaHandler_ListSubthemesEmptySlug(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{})
	w := callWithSlug(t, h, (*CartaHandler).ListSubthemes, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestCartaHandler_ListServicesEmptySlug(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{})
	w := callWithSlug(t, h, (*CartaHandler).ListServicesBySubtheme, " ")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestCartaHandler_ListSubthemesEmptyAndError(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{subthemes: map[string][]models.CartaSubtheme{}})
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/themes/inexistente/subthemes", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["data"]) != "[]" {
		t.Fatalf("data=%s", body["data"])
	}

	hErr := newCartaHandler(&stubCartaStore{err: errors.New("boom")})
	rErr := setupCartaRouter(hErr)
	w2 := httptest.NewRecorder()
	rErr.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/public/themes/tributos/subthemes", nil))
	if w2.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", w2.Code)
	}
}

func TestCartaHandler_ListServicesEmptyAndError(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{services: map[string][]models.CartaServiceSummary{}})
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/subthemes/vazio/services", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["data"]) != "[]" {
		t.Fatalf("data=%s", body["data"])
	}

	hErr := newCartaHandler(&stubCartaStore{err: errors.New("boom")})
	rErr := setupCartaRouter(hErr)
	w2 := httptest.NewRecorder()
	rErr.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/public/subthemes/iptu/services", nil))
	if w2.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", w2.Code)
	}
}

func TestParseCartaPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		query        string
		wantPage     int
		wantPerPage  int
	}{
		{"", 1, 100},
		{"page=0&per_page=-1", 1, 100},
		{"page=abc&per_page=xyz", 1, 100},
		{"page=4&per_page=25", 4, 25},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/?"+tc.query, nil)
		page, perPage := parseCartaPage(c)
		if page != tc.wantPage || perPage != tc.wantPerPage {
			t.Fatalf("query=%q got page=%d per=%d want %d/%d", tc.query, page, perPage, tc.wantPage, tc.wantPerPage)
		}
	}
}
