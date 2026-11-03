package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

// countingSearcher conta as chamadas e guarda os tipos recebidos, para provar
// que um `types` inválido nunca chega ao serviço (e, portanto, ao banco).
type countingSearcher struct {
	calls atomic.Int32
	types []models.ItemType
}

func (s *countingSearcher) Search(_ context.Context, req *models.SearchRequest) (*models.SearchResponse, error) {
	s.calls.Add(1)
	s.types = req.Types
	return &models.SearchResponse{Page: req.Page, PerPage: req.PerPage}, nil
}

// countingRecommender conta as chamadas e guarda os tipos recebidos.
type countingRecommender struct {
	calls atomic.Int32
	types []models.ItemType
}

func (r *countingRecommender) Recommend(_ context.Context, _ *models.CitizenProfile, req *models.RecommendationRequest) (*models.RecommendationResponse, error) {
	r.calls.Add(1)
	r.types = req.Types
	return &models.RecommendationResponse{Context: req.Context, Personalized: true}, nil
}

func (r *countingRecommender) RecommendAnonymous(_ context.Context, req *models.RecommendationRequest) (*models.RecommendationResponse, error) {
	r.calls.Add(1)
	r.types = req.Types
	return &models.RecommendationResponse{Context: req.Context}, nil
}

// counter é o que os testes leem dos fakes acima.
type counter interface{ count() int32 }

func (s *countingSearcher) count() int32    { return s.calls.Load() }
func (r *countingRecommender) count() int32 { return r.calls.Load() }

type typesRoute struct {
	name    string
	path    string
	newFake func() (counter, gin.HandlerFunc)
}

func typesRoutes() []typesRoute {
	withSearch := func() (counter, gin.HandlerFunc) {
		s := &countingSearcher{}
		return s, (&SearchHandler{searchSvc: s}).Search
	}
	anonymous := func() (counter, gin.HandlerFunc) {
		r := &countingRecommender{}
		return r, (&RecommendationHandler{recomSvc: r, citizenSvc: fixedProfiles{}}).Anonymous
	}
	authenticated := func() (counter, gin.HandlerFunc) {
		r := &countingRecommender{}
		return r, (&RecommendationHandler{recomSvc: r, citizenSvc: fixedProfiles{}}).Authenticated
	}
	return []typesRoute{
		{"busca autenticada", "/api/v1/search", withSearch},
		{"busca pública", "/api/public/search", withSearch},
		{"recomendações autenticadas", "/api/v1/recommendations", authenticated},
		{"recomendações públicas", "/api/public/recommendations", anonymous},
	}
}

func serveTypes(t *testing.T, route typesRoute, rawQuery string) (int, string, counter) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	fake, handler := route.newFake()
	engine := gin.New()
	engine.GET(route.path, func(c *gin.Context) {
		c.Set(middleware.UserCPFKey, syntheticCPF)
		c.Next()
	}, handler)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, route.path+"?"+rawQuery, nil))
	return w.Code, w.Body.String(), fake
}

// TestTypesInvalido_Responde400SemChamarServico cobre DEV-165: valor de `types`
// fora do enum item_type virava erro de cast no Postgres e 500. Agora a API
// valida antes e responde 400 sem chamar o serviço.
func TestTypesInvalido_Responde400SemChamarServico(t *testing.T) {
	invalid := []string{
		"types=invalid_type",
		"types=SERVICE",
		"types=Course",
		"types=course,invalid_type",
		"types=job&types=nope",
		"types=" + url.QueryEscape("service'; DROP TABLE catalog_items;--"),
	}

	for _, route := range typesRoutes() {
		for _, q := range invalid {
			t.Run(route.name+"/"+q, func(t *testing.T) {
				code, body, fake := serveTypes(t, route, q)
				if code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s, want 400", code, body)
				}
				var resp map[string]string
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Fatalf("corpo não é JSON: %v (%s)", err, body)
				}
				msg := resp["error"]
				if !strings.Contains(msg, "types") || !strings.Contains(msg, "service, course, job, mei_opportunity") {
					t.Fatalf("mensagem pouco objetiva: %q", msg)
				}
				if strings.Contains(msg, "DROP") || strings.Contains(msg, "invalid_type") {
					t.Fatalf("mensagem ecoa a entrada do cliente: %q", msg)
				}
				if n := fake.count(); n != 0 {
					t.Fatalf("serviço chamado %d vez(es) com types inválido", n)
				}
			})
		}
	}
}

// TestTypesValido_ChegaAoServico garante que os valores documentados, a lista
// por vírgula, o parâmetro repetido e `types` vazio ou omitido seguem com 200.
func TestTypesValido_ChegaAoServico(t *testing.T) {
	valid := []struct {
		query string
		want  []models.ItemType
	}{
		{"", nil},
		{"types=", nil},
		{"types=service", []models.ItemType{models.TypeService}},
		{"types=course,job", []models.ItemType{models.TypeCourse, models.TypeJob}},
		{"types=job&types=mei_opportunity", []models.ItemType{models.TypeJob, models.TypeMEIOpportunity}},
		{"types=" + url.QueryEscape(" course , ,service "), []models.ItemType{models.TypeCourse, models.TypeService}},
	}

	for _, route := range typesRoutes() {
		for _, tc := range valid {
			t.Run(route.name+"/"+tc.query, func(t *testing.T) {
				code, body, fake := serveTypes(t, route, tc.query)
				if code != http.StatusOK {
					t.Fatalf("status=%d body=%s, want 200", code, body)
				}
				if n := fake.count(); n != 1 {
					t.Fatalf("serviço chamado %d vez(es), want 1", n)
				}
				var got []models.ItemType
				switch f := fake.(type) {
				case *countingSearcher:
					got = f.types
				case *countingRecommender:
					got = f.types
				}
				if len(got) != len(tc.want) {
					t.Fatalf("types=%v, want %v", got, tc.want)
				}
				for i := range got {
					if got[i] != tc.want[i] {
						t.Fatalf("types=%v, want %v", got, tc.want)
					}
				}
			})
		}
	}
}
