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
	themes          []models.CartaTheme
	themeMeta       models.CartaMeta
	homeFeatured    []models.CartaHomeFeatured
	homeFeaturedErr error
	subthemes       map[string][]models.CartaSubtheme
	services        map[string][]models.CartaServiceSummary
	details         map[string]json.RawMessage
	redirects       map[string]string
	orgUnits        []models.CartaOrgUnit
	orgUnitMeta     models.CartaMeta
	bySici          map[string][]json.RawMessage
	allServices     []json.RawMessage
	allMeta         models.CartaMeta
	err             error
	lastPage        int
	lastPerPage     int
	lastIncludeEmp  bool
	lastThemeSlug   string
	lastSubSlug     string
	lastSiciCode    string
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

func (s *stubCartaStore) ListHomeFeatured(ctx context.Context) ([]models.CartaHomeFeatured, error) {
	if s.homeFeaturedErr != nil {
		return nil, s.homeFeaturedErr
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.homeFeatured, nil
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

func (s *stubCartaStore) GetServiceDetail(ctx context.Context, slug string) (json.RawMessage, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	if to, ok := s.redirects[slug]; ok {
		return nil, to, nil
	}
	return s.details[slug], "", nil
}

func (s *stubCartaStore) ListOrgUnits(ctx context.Context, page, perPage int) ([]models.CartaOrgUnit, models.CartaMeta, error) {
	s.lastPage = page
	s.lastPerPage = perPage
	if s.err != nil {
		return nil, models.CartaMeta{}, s.err
	}
	return s.orgUnits, s.orgUnitMeta, nil
}

func (s *stubCartaStore) ListServicesBySiciCode(ctx context.Context, siciCode string, page, perPage int) ([]json.RawMessage, models.CartaMeta, error) {
	s.lastSiciCode = siciCode
	s.lastPage = page
	s.lastPerPage = perPage
	if s.err != nil {
		return nil, models.CartaMeta{}, s.err
	}
	items := s.bySici[siciCode]
	if items == nil {
		items = []json.RawMessage{}
	}
	return items, models.CartaMeta{Page: page, PerPage: perPage, Total: len(items), TotalPages: 1}, nil
}

func (s *stubCartaStore) ListAllServices(ctx context.Context, page, perPage int) ([]json.RawMessage, models.CartaMeta, error) {
	s.lastPage = page
	s.lastPerPage = perPage
	if s.err != nil {
		return nil, models.CartaMeta{}, s.err
	}
	return s.allServices, s.allMeta, nil
}

func setupCartaRouter(h *CartaHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/public/themes", h.ListThemes)
	r.GET("/api/public/themes/:slug/subthemes", h.ListSubthemes)
	r.GET("/api/public/subthemes/:slug/services", h.ListServicesBySubtheme)
	r.GET("/api/public/org-units", h.ListOrgUnits)
	r.GET("/api/public/org-units/:siciCode/services", h.ListServicesByOrgUnit)
	r.GET("/api/public/services", h.ListAllServices)
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
	var body cartaThemesPagedJSON
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(body.Data) != 1 {
		t.Fatalf("data=%v", body.Data)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["home_featured"]; ok {
		t.Fatalf("home_featured must be omitted without include_featured, got %s", raw["home_featured"])
	}
}

func TestCartaHandler_ListThemesWithFeatured(t *testing.T) {
	store := &stubCartaStore{
		themes:    []models.CartaTheme{{Slug: "tributos", Name: "Tributos", PublishedServices: 3}},
		themeMeta: models.CartaMeta{Page: 1, PerPage: 100, Total: 1, TotalPages: 1},
		homeFeatured: []models.CartaHomeFeatured{
			{Slug: "iptu-2026", Name: "IPTU 2026", Position: 1, ThemeSlug: "tributos"},
		},
	}
	h := newCartaHandler(store)
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/themes?include_featured=true", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body cartaThemesPagedJSON
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body.HomeFeatured == nil || len(*body.HomeFeatured) != 1 || (*body.HomeFeatured)[0].Slug != "iptu-2026" {
		t.Fatalf("home_featured=%v", body.HomeFeatured)
	}
}

func TestCartaHandler_ListThemesWithFeaturedEmpty(t *testing.T) {
	store := &stubCartaStore{
		themes:       []models.CartaTheme{},
		themeMeta:    models.CartaMeta{Page: 1, PerPage: 100, Total: 0, TotalPages: 0},
		homeFeatured: nil,
	}
	h := newCartaHandler(store)
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/themes?include_featured=true", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["home_featured"]) != "[]" {
		t.Fatalf("expected explicit empty home_featured, got %s", raw["home_featured"])
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
	if body.Data.Slug != "iptu" || body.Data.Name != "IPTU" {
		t.Fatalf("data=%+v", body.Data)
	}

	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/public/services/missing", nil))
	if w2.Code != http.StatusNotFound {
		t.Fatalf("status=%d", w2.Code)
	}
}

func TestCartaHandler_GetServiceRedirect(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{
		redirects: map[string]string{"iptu-antigo": "iptu-2026"},
	})
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/services/iptu-antigo", nil))
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Fatalf("Location header must be empty, got %q", loc)
	}
	var body cartaServiceRedirectJSON
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Redirect != "iptu-2026" {
		t.Fatalf("redirect=%q", body.Redirect)
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
		query       string
		wantPage    int
		wantPerPage int
	}{
		{"", 1, 100},
		{"page=0&per_page=-1", 1, 100},
		{"page=abc&per_page=xyz", 1, 100},
		{"page=4&per_page=25", 4, 25},
		{"page=1000&per_page=50", 1000, 50},
		{"page=1001&per_page=200", 1000, 100},
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

func TestCartaHandler_ListOrgUnits(t *testing.T) {
	store := &stubCartaStore{
		orgUnits: []models.CartaOrgUnit{{
			ResponsibleOrgUnitSiciCode:  "1552",
			ResponsibleOrgUnit:          "Secretaria Municipal de Conservação",
			ResponsibleOrgUnitShortName: "SECONSERVA",
			PublishedServices:           2,
		}},
		orgUnitMeta: models.CartaMeta{Page: 1, PerPage: 100, Total: 1, TotalPages: 1},
	}
	h := newCartaHandler(store)
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/org-units?page=1&per_page=50", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if store.lastPage != 1 || store.lastPerPage != 50 {
		t.Fatalf("page=%d per=%d", store.lastPage, store.lastPerPage)
	}
	var body cartaOrgUnitsPagedJSON
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].ResponsibleOrgUnitSiciCode != "1552" {
		t.Fatalf("data=%+v", body.Data)
	}
}

func TestCartaHandler_ListServicesByOrgUnit(t *testing.T) {
	store := &stubCartaStore{
		bySici: map[string][]json.RawMessage{
			"1552": {json.RawMessage(`{"slug":"reparo-de-luminaria","name":"Reparo","responsibleOrgUnitSiciCode":"1552"}`)},
		},
	}
	h := newCartaHandler(store)
	r := setupCartaRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/org-units/1552/services?page=2&per_page=10", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if store.lastSiciCode != "1552" || store.lastPage != 2 || store.lastPerPage != 10 {
		t.Fatalf("args: sici=%s page=%d per=%d", store.lastSiciCode, store.lastPage, store.lastPerPage)
	}
	var body cartaServiceDetailsPagedJSON
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].Slug != "reparo-de-luminaria" {
		t.Fatalf("data=%+v", body.Data)
	}

	// Código sem serviços: 200 com data: []
	wEmpty := httptest.NewRecorder()
	r.ServeHTTP(wEmpty, httptest.NewRequest(http.MethodGet, "/api/public/org-units/9999/services", nil))
	if wEmpty.Code != http.StatusOK {
		t.Fatalf("empty status=%d", wEmpty.Code)
	}
	var emptyBody map[string]json.RawMessage
	if err := json.Unmarshal(wEmpty.Body.Bytes(), &emptyBody); err != nil {
		t.Fatal(err)
	}
	if string(emptyBody["data"]) != "[]" {
		t.Fatalf("data=%s", emptyBody["data"])
	}
}

func TestCartaHandler_ListServicesByOrgUnitEmptyCode(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{})
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Params = gin.Params{{Key: "siciCode", Value: "  "}}
	h.ListServicesByOrgUnit(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestCartaHandler_ListAllServices(t *testing.T) {
	store := &stubCartaStore{
		allServices: []json.RawMessage{
			json.RawMessage(`{"slug":"sem-codigo","name":"Sem código","responsibleOrgUnitSiciCode":null}`),
			json.RawMessage(`{"slug":"com-codigo","name":"Com código","responsibleOrgUnitSiciCode":"1552"}`),
		},
		allMeta: models.CartaMeta{Page: 1, PerPage: 100, Total: 2, TotalPages: 1},
	}
	h := newCartaHandler(store)
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/services", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body cartaServiceDetailsPagedJSON
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 2 {
		t.Fatalf("data len=%d", len(body.Data))
	}
	if body.Data[0].ResponsibleOrgUnitSiciCode != nil {
		t.Fatalf("first should have null sici, got %v", body.Data[0].ResponsibleOrgUnitSiciCode)
	}
	if body.Data[1].ResponsibleOrgUnitSiciCode == nil || *body.Data[1].ResponsibleOrgUnitSiciCode != "1552" {
		t.Fatalf("second sici=%v", body.Data[1].ResponsibleOrgUnitSiciCode)
	}
}

func TestCartaHandler_ListOrgUnitsStoreError(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{err: errors.New("db down")})
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/org-units", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestCartaHandler_ListAllServicesStoreError(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{err: errors.New("db down")})
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/services", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestCartaHandler_ListServicesByOrgUnitInvalidJSON(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{
		bySici: map[string][]json.RawMessage{
			"1552": {json.RawMessage(`{`)},
		},
	})
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/org-units/1552/services", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestCartaHandler_ListAllServicesInvalidJSON(t *testing.T) {
	h := newCartaHandler(&stubCartaStore{
		allServices: []json.RawMessage{json.RawMessage(`not-json`)},
		allMeta:     models.CartaMeta{Page: 1, PerPage: 100, Total: 1, TotalPages: 1},
	})
	r := setupCartaRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/services", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
