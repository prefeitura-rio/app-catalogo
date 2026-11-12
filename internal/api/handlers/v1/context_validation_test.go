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

// contextCapturingRecommender conta as chamadas e guarda o contexto recebido,
// para provar que um `context` inválido nunca chega ao serviço.
type contextCapturingRecommender struct {
	calls   atomic.Int32
	context models.RecommendationContext
}

func (r *contextCapturingRecommender) Recommend(_ context.Context, _ *models.CitizenProfile, req *models.RecommendationRequest) (*models.RecommendationResponse, error) {
	r.calls.Add(1)
	r.context = req.Context
	return &models.RecommendationResponse{Items: []*models.RankedItem{}, Context: req.Context, Personalized: true}, nil
}

func (r *contextCapturingRecommender) RecommendAnonymous(_ context.Context, req *models.RecommendationRequest) (*models.RecommendationResponse, error) {
	r.calls.Add(1)
	r.context = req.Context
	return &models.RecommendationResponse{Items: []*models.RankedItem{}, Context: req.Context}, nil
}

type contextRoute struct {
	name    string
	path    string
	handler func(*RecommendationHandler) gin.HandlerFunc
}

func contextRoutes() []contextRoute {
	return []contextRoute{
		{"recomendações autenticadas", "/api/v1/recommendations", func(h *RecommendationHandler) gin.HandlerFunc { return h.Authenticated }},
		{"recomendações públicas", "/api/public/recommendations", func(h *RecommendationHandler) gin.HandlerFunc { return h.Anonymous }},
	}
}

func serveContext(t *testing.T, route contextRoute, rawQuery string) (int, string, *contextCapturingRecommender) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	fake := &contextCapturingRecommender{}
	handler := &RecommendationHandler{recomSvc: fake, citizenSvc: fixedProfiles{}}
	engine := gin.New()
	engine.GET(route.path, func(c *gin.Context) {
		c.Set(middleware.UserCPFKey, syntheticCPF)
		c.Next()
	}, route.handler(handler))

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, route.path+"?"+rawQuery, nil))
	return w.Code, w.Body.String(), fake
}

// TestContextInvalido_Responde400SemChamarServico cobre DEV-565: `context`
// fora do enum RecommendationContext era aceito, ecoado na resposta e
// ranqueado silenciosamente com os pesos de homepage. Agora a API responde 400
// sem chamar o serviço, como já faz com `types`.
func TestContextInvalido_Responde400SemChamarServico(t *testing.T) {
	invalid := []string{
		"context=invalido",
		"context=invalido&limit=3",
		"context=HOMEPAGE",
		"context=Profile",
		"context=" + url.QueryEscape(" homepage "),
		"context=" + url.QueryEscape("homepage,profile"),
		"context=" + url.QueryEscape("<script>alert(1)</script>"),
		"context=" + url.QueryEscape("homepage'; DROP TABLE catalog_items;--"),
	}

	for _, route := range contextRoutes() {
		for _, q := range invalid {
			t.Run(route.name+"/"+q, func(t *testing.T) {
				code, body, fake := serveContext(t, route, q)
				if code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s, want 400", code, body)
				}
				var resp map[string]string
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Fatalf("corpo não é JSON: %v (%s)", err, body)
				}
				msg := resp["error"]
				if !strings.Contains(msg, "context") || !strings.Contains(msg, "homepage, after_search, profile") {
					t.Fatalf("mensagem pouco objetiva: %q", msg)
				}
				for _, echoed := range []string{"invalido", "HOMEPAGE", "Profile", "script", "DROP"} {
					if strings.Contains(msg, echoed) {
						t.Fatalf("mensagem ecoa a entrada do cliente: %q", msg)
					}
				}
				if n := fake.calls.Load(); n != 0 {
					t.Fatalf("serviço chamado %d vez(es) com context inválido", n)
				}
			})
		}
	}
}

// TestContextValido_ChegaAoServico garante que os valores do enum seguem com
// 200 e que `context` ausente ou vazio continua virando homepage.
func TestContextValido_ChegaAoServico(t *testing.T) {
	valid := []struct {
		query string
		want  models.RecommendationContext
	}{
		{"", models.ContextHomepage},
		{"context=", models.ContextHomepage},
		{"context=homepage", models.ContextHomepage},
		{"context=after_search", models.ContextAfterSearch},
		{"context=profile&limit=3", models.ContextProfile},
	}

	for _, route := range contextRoutes() {
		for _, tc := range valid {
			t.Run(route.name+"/"+tc.query, func(t *testing.T) {
				code, body, fake := serveContext(t, route, tc.query)
				if code != http.StatusOK {
					t.Fatalf("status=%d body=%s, want 200", code, body)
				}
				if n := fake.calls.Load(); n != 1 {
					t.Fatalf("serviço chamado %d vez(es), want 1", n)
				}
				if fake.context != tc.want {
					t.Fatalf("context no serviço = %q, want %q", fake.context, tc.want)
				}
				var resp models.RecommendationResponse
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Fatalf("corpo não é JSON: %v (%s)", err, body)
				}
				if resp.Context != tc.want {
					t.Fatalf("context na resposta = %q, want %q", resp.Context, tc.want)
				}
			})
		}
	}
}
