package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
	"github.com/prefeitura-rio/app-catalogo/internal/config"
	"github.com/prefeitura-rio/app-catalogo/internal/datasource"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

const (
	testIssuer = "https://auth.example.test/realms/teste"
	testKid    = "rsa-router"
	testCPF    = "33333333333"
	itemID     = "00000000-0000-4000-8000-000000000001"
)

type routerFixture struct {
	router *gin.Engine
	key    *rsa.PrivateKey
}

// unreachablePool cria um pool real apontando para uma porta fechada. pgxpool
// conecta sob demanda, então as rotas que tocam o banco respondem com erro.
func unreachablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://catalogo:catalogo@127.0.0.1:1/catalogo_test?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newRouterFixture(t *testing.T) routerFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	jwks, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kid": testKid, "kty": "RSA", "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatalf("jwks: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(jwks)
	}))
	t.Cleanup(srv.Close)

	verifier, err := middleware.NewJWKSVerifier(testIssuer, srv.URL, []string{"superapp"})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}

	cfg := &config.AppConfig{
		App:     config.AppSettings{Environment: "test"},
		Server:  config.ServerSettings{RequestTimeout: 5},
		Tracing: config.TracingSettings{ServiceName: "app-catalogo-test"},
	}
	pool := unreachablePool(t)
	r := SetupRouter(cfg, pool, RouterDeps{
		DSManager:     datasource.NewManager(),
		ItemRepo:      repository.NewCatalogItemRepository(pool),
		CartaRepo:     repository.NewCartaRepository(pool),
		TokenVerifier: verifier,
	})
	return routerFixture{router: r, key: key}
}

func (f routerFixture) token(t *testing.T, roles ...string) string {
	t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":                testIssuer,
		"azp":                "superapp",
		"sub":                "00000000-0000-0000-0000-000000000002",
		"preferred_username": testCPF,
		"iat":                now.Unix(),
		"exp":                now.Add(5 * time.Minute).Unix(),
		"resource_access":    map[string]any{"superapp": map[string]any{"roles": roles}},
	})
	tok.Header["kid"] = testKid
	s, err := tok.SignedString(f.key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func (f routerFixture) do(method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("X-Auth-Request-Token", token)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func forgedAdminToken() string {
	enc := base64.RawURLEncoding.EncodeToString
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT", "kid": testKid})
	payload, _ := json.Marshal(map[string]any{
		"iss": testIssuer, "azp": "superapp", "preferred_username": testCPF,
		"exp":             time.Now().Add(time.Hour).Unix(),
		"resource_access": map[string]any{"superapp": map[string]any{"roles": []string{"go:admin"}}},
	})
	return enc(header) + "." + enc(payload) + ".x"
}

func TestSetupRouter_Infra(t *testing.T) {
	f := newRouterFixture(t)

	w := f.do(http.MethodGet, "/health", "")
	if w.Code != http.StatusOK {
		t.Fatalf("/health: esperado 200, obtido %d", w.Code)
	}
	if w.Header().Get(middleware.RequestIDHeader) == "" {
		t.Fatal("toda resposta deve trazer X-Request-ID")
	}
	if w := f.do(http.MethodGet, "/ready", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ready com banco fora: esperado 503, obtido %d", w.Code)
	}
	if w := f.do(http.MethodGet, "/metrics", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "go_goroutines") {
		t.Fatalf("/metrics: esperado 200 com métricas, obtido %d", w.Code)
	}
}

// Autorização das rotas como o SetupRouter as monta.
func TestSetupRouter_Authorization(t *testing.T) {
	f := newRouterFixture(t)
	user := f.token(t, "user")
	admin := f.token(t, "go:admin")

	protected := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/search?q=iptu"},
		{http.MethodGet, "/api/v1/recommendations"},
		{http.MethodGet, "/api/v1/catalog/" + itemID},
		{http.MethodGet, "/api/v1/admin/sync/status"},
		{http.MethodPost, "/api/v1/admin/sync/trigger"},
	}
	for _, rt := range protected {
		for name, token := range map[string]string{"sem token": "", "token forjado": forgedAdminToken(), "token malformado": "nao-e-um-jwt"} {
			if w := f.do(rt.method, rt.path, token); w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s (%s): esperado 401, obtido %d", rt.method, rt.path, name, w.Code)
			}
		}
	}

	for _, rt := range protected[3:] {
		if w := f.do(rt.method, rt.path, user); w.Code != http.StatusForbidden {
			t.Errorf("%s %s usuário comum: esperado 403, obtido %d", rt.method, rt.path, w.Code)
		}
	}

	// Admin passa da autorização e chega ao handler.
	if w := f.do(http.MethodPost, "/api/v1/admin/sync/trigger?source=inexistente", admin); w.Code != http.StatusAccepted {
		t.Errorf("trigger admin: esperado 202, obtido %d", w.Code)
	}
	if w := f.do(http.MethodGet, "/api/v1/admin/sync/status", admin); w.Code != http.StatusInternalServerError {
		t.Errorf("status admin com banco fora: esperado 500 do handler, obtido %d", w.Code)
	}

	// Usuário autenticado passa do RequireAuth: o handler valida o ID antes do banco.
	if w := f.do(http.MethodGet, "/api/v1/catalog/nao-uuid", user); w.Code != http.StatusBadRequest {
		t.Errorf("catálogo autenticado com ID inválido: esperado 400, obtido %d", w.Code)
	}
}

func TestSetupRouter_PublicRoutes(t *testing.T) {
	f := newRouterFixture(t)

	if w := f.do(http.MethodGet, "/api/public/catalog/nao-uuid", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("catálogo público com ID inválido: esperado 400, obtido %d", w.Code)
	}
	if w := f.do(http.MethodGet, "/api/public/catalog/"+itemID, ""); w.Code != http.StatusNotFound {
		t.Fatalf("catálogo público com banco fora: esperado 404, obtido %d", w.Code)
	}
	// Sem segredo configurado o webhook recusa tudo (falha fechada).
	if w := f.do(http.MethodPost, "/api/webhooks/salesforce", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("webhook sem segredo: esperado 401, obtido %d", w.Code)
	}
	if w := f.do(http.MethodGet, "/rota-inexistente", ""); w.Code != http.StatusNotFound {
		t.Fatalf("rota inexistente: esperado 404, obtido %d", w.Code)
	}
}
