package clients

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeCartaServicosBaseURL(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"https://example.com", "https://example.com/api"},
		{"https://example.com/", "https://example.com/api"},
		{"https://example.com/api", "https://example.com/api"},
		{"https://example.com/api/", "https://example.com/api"},
		{"https://example.com/api/v2", "https://example.com/api/v2"},
		{"https://example.com/api/v2/", "https://example.com/api/v2"},
		{"https://example.com/carta", "https://example.com/carta/api"},
		{"", ""},
	}
	for _, tt := range tests {
		got, err := normalizeCartaServicosBaseURL(tt.in)
		if err != nil {
			t.Errorf("normalize(%q) unexpected err: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("normalize(%q)=%q want %q", tt.in, got, tt.want)
		}
	}

	invalid := []string{"not-a-url", "javascript:alert(1)", "ftp://example.com", "://missing-scheme"}
	for _, in := range invalid {
		if _, err := normalizeCartaServicosBaseURL(in); err == nil {
			t.Errorf("normalize(%q) expected error", in)
		}
	}
}

func mustCartaClient(t *testing.T, baseURL string) *CartaServicosClient {
	t.Helper()
	c, err := NewCartaServicosClient(baseURL)
	if err != nil {
		t.Fatalf("NewCartaServicosClient: %v", err)
	}
	return c
}

func TestCartaServicosClient_ListThemes_Happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/themes" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.URL.Query().Get("page") != "1" {
			t.Errorf("page=%s", r.URL.Query().Get("page"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta": map[string]int{"page": 1, "per_page": 10, "total": 1, "total_pages": 1},
			"data": []map[string]any{
				{"slug": "tributos", "name": "Tributos", "subthemesCount": 4, "publishedServices": 8},
			},
		})
	}))
	defer srv.Close()

	c := mustCartaClient(t, srv.URL)
	themes, meta, err := c.ListThemes(context.Background(), 1, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Total != 1 || len(themes) != 1 || themes[0].Slug != "tributos" {
		t.Fatalf("unexpected: meta=%+v themes=%+v", meta, themes)
	}
}

func TestCartaServicosClient_ListAllThemes_Pagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		switch page {
		case "1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta": map[string]int{"page": 1, "per_page": 100, "total": 2, "total_pages": 2},
				"data": []map[string]any{{"slug": "a", "name": "A"}},
			})
		case "2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta": map[string]int{"page": 2, "per_page": 100, "total": 2, "total_pages": 2},
				"data": []map[string]any{{"slug": "b", "name": "B"}},
			})
		default:
			http.Error(w, "bad page", 500)
		}
	}))
	defer srv.Close()

	c := mustCartaClient(t, srv.URL)
	themes, err := c.ListAllThemes(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(themes) != 2 {
		t.Fatalf("want 2 themes, got %d", len(themes))
	}
}

func TestCartaServicosClient_ListServicesBySubtheme_Happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/subthemes/iptu/services") {
			t.Errorf("path=%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta": map[string]int{"page": 1, "per_page": 100, "total": 1, "total_pages": 1},
			"data": []map[string]any{{
				"slug":             "iptu-cobranca",
				"name":             "IPTU",
				"articleStatus":    "Online",
				"lastModifiedDate": "2026-08-20T04:59:39.000Z",
			}},
		})
	}))
	defer srv.Close()

	c := mustCartaClient(t, srv.URL)
	items, meta, err := c.ListServicesBySubtheme(context.Background(), "iptu", 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Total != 1 || items[0].Slug != "iptu-cobranca" {
		t.Fatalf("unexpected: %+v meta=%+v", items, meta)
	}
	mod := items[0].EffectiveModifiedAt()
	if mod.IsZero() || mod.Year() != 2026 {
		t.Fatalf("EffectiveModifiedAt=%v", mod)
	}
}

func TestCartaServicosClient_GetService_Happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"slug": "iptu-cobranca",
				"name": "IPTU cobrança",
				"info": map[string]any{"summary": "<p>resumo</p>", "fullDescription": "<p>desc</p>"},
			},
		})
	}))
	defer srv.Close()

	c := mustCartaClient(t, srv.URL)
	detail, err := c.GetService(context.Background(), "iptu-cobranca")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Slug != "iptu-cobranca" || detail.Name == "" {
		t.Fatalf("unexpected detail: %+v", detail)
	}
}

func TestCartaServicosClient_GetService_Redirect301(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/services/slug-antigo") {
			w.Header().Set("Location", "/api/services/slug-novo")
			w.WriteHeader(http.StatusMovedPermanently)
			_ = json.NewEncoder(w).Encode(map[string]string{"redirect": "slug-novo"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := mustCartaClient(t, srv.URL)
	_, err := c.GetService(context.Background(), "slug-antigo")
	var redir *ServiceRedirectError
	if !errors.As(err, &redir) {
		t.Fatalf("want ServiceRedirectError, got %v", err)
	}
	if redir.ToSlug != "slug-novo" {
		t.Fatalf("ToSlug=%q", redir.ToSlug)
	}
}

func TestCartaServicosClient_GetServiceCanonical_FollowsRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/services/old"):
			w.Header().Set("Location", "/api/services/new")
			w.WriteHeader(http.StatusMovedPermanently)
			_, _ = w.Write([]byte(`{"redirect":"new"}`))
		case strings.HasSuffix(r.URL.Path, "/services/new"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"slug": "new", "name": "Novo"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := mustCartaClient(t, srv.URL)
	detail, canonical, err := c.GetServiceCanonical(context.Background(), "old")
	if err != nil {
		t.Fatal(err)
	}
	if canonical != "new" || detail.Slug != "new" {
		t.Fatalf("canonical=%q detail.Slug=%q", canonical, detail.Slug)
	}
}

func TestCartaServicosClient_GetService_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND"}}`))
	}))
	defer srv.Close()

	c := mustCartaClient(t, srv.URL)
	_, err := c.GetService(context.Background(), "missing")
	if !errors.Is(err, ErrServiceNotFound) {
		t.Fatalf("want ErrServiceNotFound, got %v", err)
	}
}

func TestCartaServicosClient_GetServiceCanonical_RedirectCycle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/services/a"):
			w.Header().Set("Location", "/api/services/b")
			w.WriteHeader(http.StatusMovedPermanently)
		case strings.HasSuffix(r.URL.Path, "/services/b"):
			w.Header().Set("Location", "/api/services/a")
			w.WriteHeader(http.StatusMovedPermanently)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := mustCartaClient(t, srv.URL)
	_, _, err := c.GetServiceCanonical(context.Background(), "a")
	if err == nil || !strings.Contains(err.Error(), "ciclo") {
		t.Fatalf("want cycle error, got %v", err)
	}
}

func TestCartaServicosClient_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`oops`))
	}))
	defer srv.Close()

	c := mustCartaClient(t, srv.URL)
	_, _, err := c.ListThemes(context.Background(), 1, 10, false)
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("want status 500 error, got %v", err)
	}
}

func TestCartaServicosClient_EmptySlug(t *testing.T) {
	c := mustCartaClient(t, "https://example.com")
	if _, err := c.GetService(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty slug")
	}
	if _, _, err := c.ListSubthemes(context.Background(), "", 1, 10); err == nil {
		t.Fatal("expected error for empty theme slug")
	}
}

func TestCartaServiceListItem_EffectiveModifiedAt_PrefersLater(t *testing.T) {
	item := CartaServiceListItem{
		LastModifiedDate:  "2026-01-01T00:00:00.000Z",
		LastPublishedDate: "2026-08-20T04:59:39.000Z",
	}
	got := item.EffectiveModifiedAt()
	want := time.Date(2026, 8, 20, 4, 59, 39, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCartaServicosClient_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not-json`))
	}))
	defer srv.Close()

	c := mustCartaClient(t, srv.URL)
	_, _, err := c.ListThemes(context.Background(), 1, 10, false)
	if err == nil {
		t.Fatal("expected decode error")
	}
}
