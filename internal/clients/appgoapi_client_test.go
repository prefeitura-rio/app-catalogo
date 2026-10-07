package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestAppGoAPIClient(t *testing.T, handler http.Handler) *AppGoAPIClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	tm := NewKeycloakTokenManager(srv.URL, "test-realm", "client", "secret")
	return NewAppGoAPIClient(srv.URL, tm)
}

func withKeycloakToken(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/protocol/openid-connect/token") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "test-token",
				"expires_in":   300,
			})
			return
		}
		next(w, r)
	})
}

func TestAppGoAPIClient_GetCourses_UsesLimitQuery(t *testing.T) {
	t.Parallel()

	var gotPath string
	client := newTestAppGoAPIClient(t, withKeycloakToken(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"courses": []map[string]any{
					{"id": "1", "title": "Curso", "status": "published", "is_visible": true},
				},
				"pagination": map[string]any{"total": 1, "page": 1},
			},
		})
	}))

	courses, total, err := client.GetCourses(context.Background(), 1, time.Time{})
	if err != nil {
		t.Fatalf("GetCourses: %v", err)
	}
	if total != 1 || len(courses) != 1 {
		t.Fatalf("total=%d len=%d", total, len(courses))
	}
	if !strings.Contains(gotPath, "limit=100") {
		t.Fatalf("expected limit=100 in %q", gotPath)
	}
	if strings.Contains(gotPath, "per_page=") {
		t.Fatalf("must not use per_page: %q", gotPath)
	}
}

func TestAppGoAPIClient_GetJobs_UsesPageSizeAndActiveStatus(t *testing.T) {
	t.Parallel()

	var gotPath string
	client := newTestAppGoAPIClient(t, withKeycloakToken(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "j1", "titulo": "Vaga", "status": "publicado_ativo"},
			},
			"meta": map[string]any{"total": 1, "page": 1, "page_size": 100},
		})
	}))

	jobs, total, err := client.GetJobs(context.Background(), 2, time.Time{})
	if err != nil {
		t.Fatalf("GetJobs: %v", err)
	}
	if total != 1 || len(jobs) != 1 || jobs[0].Status != "publicado_ativo" {
		t.Fatalf("jobs=%+v total=%d", jobs, total)
	}
	if !strings.Contains(gotPath, "page=2") || !strings.Contains(gotPath, "pageSize=100") {
		t.Fatalf("expected page/pageSize in %q", gotPath)
	}
	if !strings.Contains(gotPath, "status=publicado_ativo") {
		t.Fatalf("expected status filter in %q", gotPath)
	}
	if strings.Contains(gotPath, "per_page=") {
		t.Fatalf("must not use per_page: %q", gotPath)
	}
}

func TestAppGoAPIClient_GetMEI_UsesPageSizeAndActiveStatus(t *testing.T) {
	t.Parallel()

	var gotPath string
	client := newTestAppGoAPIClient(t, withKeycloakToken(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "m1", "titulo": "MEI", "status": "active"},
			},
			"meta": map[string]any{"total": 1, "page": 1, "page_size": 100},
		})
	}))

	items, total, err := client.GetMEIOpportunities(context.Background(), 1, time.Time{})
	if err != nil {
		t.Fatalf("GetMEI: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d len=%d", total, len(items))
	}
	if !strings.Contains(gotPath, "pageSize=100") || !strings.Contains(gotPath, "status=active") {
		t.Fatalf("expected pageSize/status in %q", gotPath)
	}
}

func TestAppGoAPIClient_GetCourses_RequiresAuthHeader(t *testing.T) {
	t.Parallel()

	var gotAuth string
	client := newTestAppGoAPIClient(t, withKeycloakToken(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"courses":    []any{},
				"pagination": map[string]any{"total": 0, "page": 1},
			},
		})
	}))

	if _, _, err := client.GetCourses(context.Background(), 1, time.Time{}); err != nil {
		t.Fatalf("GetCourses: %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization=%q", gotAuth)
	}
}
