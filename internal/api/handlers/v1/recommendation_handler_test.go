package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
	"github.com/prefeitura-rio/app-catalogo/internal/cache"
	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
	"github.com/prefeitura-rio/app-catalogo/internal/services"
)

// CPF sintético (não pertence a ninguém): só precisa ter 11 dígitos.
const syntheticCPF = "00000000191"

type circuitOpenProfiles struct{}

func (circuitOpenProfiles) GetOrSync(context.Context, string) (*models.CitizenProfile, error) {
	return nil, fmt.Errorf("rmi: %w", clients.ErrCircuitOpen)
}

// slowProfiles simula o RMI lento: só devolve quando o contexto acaba.
type slowProfiles struct{}

func (slowProfiles) GetOrSync(ctx context.Context, _ string) (*models.CitizenProfile, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type recordingRecommender struct {
	personalizedCalls atomic.Int32
	anonymousCalls    atomic.Int32
}

func (r *recordingRecommender) Recommend(context.Context, *models.CitizenProfile, *models.RecommendationRequest) (*models.RecommendationResponse, error) {
	r.personalizedCalls.Add(1)
	return nil, fmt.Errorf("recomendação personalizada não deveria ser chamada sem perfil")
}

func (r *recordingRecommender) RecommendAnonymous(ctx context.Context, req *models.RecommendationRequest) (*models.RecommendationResponse, error) {
	r.anonymousCalls.Add(1)
	// Como o Redis e o Postgres reais: com o contexto vencido, a consulta falha.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &models.RecommendationResponse{
		Items:        []*models.RankedItem{{ID: "item-generico", Title: "Item genérico"}},
		Context:      req.Context,
		Personalized: false,
	}, nil
}

func authenticatedRecommendationsEngine(handler *RecommendationHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// Autenticação já coberta por middleware/user_context_test.go: aqui só o CPF importa.
	engine.GET("/api/v1/recommendations", func(c *gin.Context) {
		c.Set(middleware.UserCPFKey, syntheticCPF)
		c.Next()
	}, handler.Authenticated)
	return engine
}

func TestRecommendationsAuthenticated_CircuitoAbertoRespondeGenerica(t *testing.T) {
	t.Parallel()

	recommender := &recordingRecommender{}
	engine := authenticatedRecommendationsEngine(&RecommendationHandler{
		recomSvc:   recommender,
		citizenSvc: circuitOpenProfiles{},
	})

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/recommendations?limit=5", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var resp models.RecommendationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	if resp.Personalized || len(resp.Items) != 1 {
		t.Fatalf("esperava recomendação genérica, got %+v", resp)
	}
	if recommender.anonymousCalls.Load() != 1 || recommender.personalizedCalls.Load() != 0 {
		t.Fatalf("anônima=%d personalizada=%d", recommender.anonymousCalls.Load(), recommender.personalizedCalls.Load())
	}
}

// TestRecommendationsAuthenticated_RMILentoRespondeGenericaNoPrazo cobre o RMI
// lento antes de o circuito abrir: a busca do perfil não pode consumir o prazo
// inteiro da requisição, senão a recomendação genérica roda com o contexto
// vencido e vira 500.
func TestRecommendationsAuthenticated_RMILentoRespondeGenericaNoPrazo(t *testing.T) {
	t.Parallel()

	recommender := &recordingRecommender{}
	engine := gin.New()
	engine.Use(middleware.Timeout(300 * time.Millisecond))
	engine.GET("/api/v1/recommendations", func(c *gin.Context) {
		c.Set(middleware.UserCPFKey, syntheticCPF)
		c.Next()
	}, (&RecommendationHandler{recomSvc: recommender, citizenSvc: slowProfiles{}}).Authenticated)

	started := time.Now()
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/recommendations", nil))
	elapsed := time.Since(started)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var resp models.RecommendationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	if resp.Personalized {
		t.Fatalf("esperava recomendação genérica, got %+v", resp)
	}
	if elapsed >= 300*time.Millisecond {
		t.Fatalf("a resposta deveria sair antes do prazo da requisição, levou %v", elapsed)
	}
}

// TestRecommendationsAuthenticated_RMIForaComCircuitoAberto monta a pilha real
// (cliente RMI com circuit breaker, serviços e repositórios no Postgres) com o
// RMI fora (status 503). Precisa do Postgres migrado do CI; sem ele, é pulado.
func TestRecommendationsAuthenticated_RMIForaComCircuitoAberto(t *testing.T) {
	pool := testRecommendationPool(t)

	var rmiCalls atomic.Int32
	rmi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/protocol/openid-connect/token") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test-token", "expires_in": 300})
			return
		}
		rmiCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(rmi.Close)

	rmiClient := clients.NewRMIClient(rmi.URL, clients.NewKeycloakTokenManager(rmi.URL, "test-realm", "client", "secret"))
	for i := 0; i < 5 && rmiClient.CircuitState() != clients.CircuitOpen; i++ {
		_, _ = rmiClient.GetCitizen(context.Background(), syntheticCPF)
	}
	if rmiClient.CircuitState() != clients.CircuitOpen {
		t.Fatalf("circuito do RMI deveria estar aberto, got %s", rmiClient.CircuitState())
	}
	callsWhenOpened := rmiCalls.Load()

	redisPort, _ := strconv.Atoi(envOrDefault("REDIS_PORT", "6379"))
	redisCache := cache.NewRedisCache(envOrDefault("REDIS_HOST", "localhost"), redisPort, os.Getenv("REDIS_PASSWORD"), 0, 2, 0)
	itemRepo := repository.NewCatalogItemRepository(pool)
	handler := NewRecommendationHandler(
		services.NewRecommendationService(itemRepo, redisCache, models.DefaultWeights, time.Minute, time.Minute),
		services.NewCitizenProfileService(rmiClient, repository.NewCitizenProfileRepository(pool), "test-cpf-hmac-key-with-at-least-32-bytes", time.Hour),
	)

	w := httptest.NewRecorder()
	authenticatedRecommendationsEngine(handler).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/recommendations", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var resp models.RecommendationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resposta inválida: %v", err)
	}
	if resp.Personalized {
		t.Fatalf("esperava recomendação genérica (personalized=false), got %+v", resp)
	}
	if rmiCalls.Load() != callsWhenOpened {
		t.Fatalf("com o circuito aberto o RMI não pode ser chamado: %d → %d", callsWhenOpened, rmiCalls.Load())
	}
}

func testRecommendationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=%s",
		envOrDefault("DB_USER", "catalogo"),
		envOrDefault("DB_PASSWORD", "catalogo"),
		envOrDefault("DB_HOST", "localhost"),
		envOrDefault("DB_PORT", "5432"),
		envOrDefault("DB_NAME", "catalogo_test"),
		envOrDefault("DB_SSL_MODE", "disable"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("postgres indisponível (%v); pulando teste de integração", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres ping falhou (%v); pulando teste de integração", err)
	}

	var ready bool
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) = 2 FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name IN ('catalog_items', 'citizen_profiles')
	`).Scan(&ready); err != nil || !ready {
		pool.Close()
		t.Skip("catalog_items/citizen_profiles ausentes; rode migrations antes dos testes de integração")
	}

	t.Cleanup(pool.Close)
	return pool
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
