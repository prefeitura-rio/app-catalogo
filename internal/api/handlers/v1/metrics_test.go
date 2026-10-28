package v1

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/observability"
)

// Os testes deste arquivo não usam t.Parallel: os histogramas são globais e os
// testes paralelos do pacote também chamam os handlers de recomendação, o que
// mudaria as contagens no meio da medição.

type fakeSearcher struct{ err error }

func (f fakeSearcher) Search(_ context.Context, req *models.SearchRequest) (*models.SearchResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &models.SearchResponse{Page: req.Page, PerPage: req.PerPage}, nil
}

type fixedProfiles struct{}

func (fixedProfiles) GetOrSync(context.Context, string) (*models.CitizenProfile, error) {
	return &models.CitizenProfile{}, nil
}

type okRecommender struct{}

func (okRecommender) Recommend(_ context.Context, _ *models.CitizenProfile, req *models.RecommendationRequest) (*models.RecommendationResponse, error) {
	return &models.RecommendationResponse{Context: req.Context, Personalized: true}, nil
}

func (okRecommender) RecommendAnonymous(_ context.Context, req *models.RecommendationRequest) (*models.RecommendationResponse, error) {
	return &models.RecommendationResponse{Context: req.Context}, nil
}

// metricsCount lê de GET /metrics o valor da série `<name>_count{<labels>}`;
// série ausente conta como 0.
func metricsCount(t *testing.T, name, labels string) int {
	t.Helper()
	engine := gin.New()
	engine.GET("/metrics", observability.MetricsHandler())
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/metrics: status %d", w.Code)
	}
	prefix := name + "_count{" + labels + "} "
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if v, ok := strings.CutPrefix(line, prefix); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				t.Fatalf("valor inválido em %q: %v", line, err)
			}
			return n
		}
	}
	return 0
}

func TestSearch_ObservaDuracaoNoMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const metric = "catalogo_search_duration_seconds"

	cases := []struct {
		name     string
		query    string
		searcher fakeSearcher
		wantCode int
		labels   string
	}{
		{"com texto e tipo", "q=curso&types=course", fakeSearcher{}, http.StatusOK, `has_query="true",type_filter="true"`},
		{"só texto", "q=iptu", fakeSearcher{}, http.StatusOK, `has_query="true",type_filter="false"`},
		{"sem texto nem tipo", "", fakeSearcher{}, http.StatusOK, `has_query="false",type_filter="false"`},
		{"só tipo", "types=job,course", fakeSearcher{}, http.StatusOK, `has_query="false",type_filter="true"`},
		{"falha da busca também conta", "q=curso", fakeSearcher{err: errors.New("banco fora")}, http.StatusInternalServerError, `has_query="true",type_filter="false"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := gin.New()
			engine.GET("/api/public/search", (&SearchHandler{searchSvc: tc.searcher}).Search)

			before := metricsCount(t, metric, tc.labels)
			for i := 0; i < 3; i++ {
				w := httptest.NewRecorder()
				engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/search?"+tc.query, nil))
				if w.Code != tc.wantCode {
					t.Fatalf("status=%d body=%s, want %d", w.Code, w.Body.String(), tc.wantCode)
				}
			}
			if got := metricsCount(t, metric, tc.labels) - before; got != 3 {
				t.Fatalf("%s_count{%s} aumentou %d, want 3", metric, tc.labels, got)
			}
		})
	}
}

func TestSearch_RotulosNaoUsamEntradaDoCliente(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/api/public/search", (&SearchHandler{searchSvc: fakeSearcher{}}).Search)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/public/search?q=rotulo-hostil-xyz&types=tipo-hostil-xyz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}

	mw := httptest.NewRecorder()
	metrics := gin.New()
	metrics.GET("/metrics", observability.MetricsHandler())
	metrics.ServeHTTP(mw, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if body := mw.Body.String(); strings.Contains(body, "hostil-xyz") {
		t.Fatalf("/metrics expõe valor enviado pelo cliente")
	}
}

func TestRecommendations_ObservaDuracaoNoMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const metric = "catalogo_recommendations_duration_seconds"

	withCPF := func(c *gin.Context) {
		c.Set(middleware.UserCPFKey, syntheticCPF)
		c.Next()
	}

	cases := []struct {
		name     string
		path     string
		handler  func(h *RecommendationHandler) gin.HandlerFunc
		recom    recommender
		profiles citizenProfileProvider
		wantCode int
		labels   string
	}{
		{
			name:     "anônima",
			path:     "/api/public/recommendations?limit=5",
			handler:  func(h *RecommendationHandler) gin.HandlerFunc { return h.Anonymous },
			recom:    okRecommender{},
			profiles: fixedProfiles{},
			wantCode: http.StatusOK,
			labels:   `personalized="false"`,
		},
		{
			name:     "autenticada com perfil",
			path:     "/api/v1/recommendations?limit=5",
			handler:  func(h *RecommendationHandler) gin.HandlerFunc { return h.Authenticated },
			recom:    okRecommender{},
			profiles: fixedProfiles{},
			wantCode: http.StatusOK,
			labels:   `personalized="true"`,
		},
		{
			name:     "autenticada sem perfil cai na genérica",
			path:     "/api/v1/recommendations?limit=5",
			handler:  func(h *RecommendationHandler) gin.HandlerFunc { return h.Authenticated },
			recom:    okRecommender{},
			profiles: circuitOpenProfiles{},
			wantCode: http.StatusOK,
			labels:   `personalized="false"`,
		},
		{
			name:     "falha da recomendação também conta",
			path:     "/api/v1/recommendations?limit=5",
			handler:  func(h *RecommendationHandler) gin.HandlerFunc { return h.Authenticated },
			recom:    &recordingRecommender{},
			profiles: fixedProfiles{},
			wantCode: http.StatusInternalServerError,
			labels:   `personalized="true"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &RecommendationHandler{recomSvc: tc.recom, citizenSvc: tc.profiles}
			engine := gin.New()
			route := strings.SplitN(tc.path, "?", 2)[0]
			engine.GET(route, withCPF, tc.handler(h))

			before := metricsCount(t, metric, tc.labels)
			for i := 0; i < 3; i++ {
				w := httptest.NewRecorder()
				engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
				if w.Code != tc.wantCode {
					t.Fatalf("status=%d body=%s, want %d", w.Code, w.Body.String(), tc.wantCode)
				}
			}
			if got := metricsCount(t, metric, tc.labels) - before; got != 3 {
				t.Fatalf("%s_count{%s} aumentou %d, want 3", metric, tc.labels, got)
			}
		})
	}
}

func TestRecommendationsAuthenticated_SemCPFNaoObserva(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const metric = "catalogo_recommendations_duration_seconds"
	h := &RecommendationHandler{recomSvc: okRecommender{}, citizenSvc: fixedProfiles{}}
	engine := gin.New()
	engine.GET("/api/v1/recommendations", h.Authenticated)

	beforeTrue := metricsCount(t, metric, `personalized="true"`)
	beforeFalse := metricsCount(t, metric, `personalized="false"`)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/recommendations", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", w.Code)
	}
	if metricsCount(t, metric, `personalized="true"`) != beforeTrue || metricsCount(t, metric, `personalized="false"`) != beforeFalse {
		t.Fatalf("requisição sem autenticação não deveria contar como recomendação")
	}
}
