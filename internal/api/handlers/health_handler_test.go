package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newHealthRouter(pool *pgxpool.Pool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewHealthHandler(pool)
	r := gin.New()
	r.GET("/health", h.Health)
	r.GET("/ready", h.Ready)
	return r
}

func get(r *gin.Engine, path string) (int, map[string]string) {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

func newPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestHealth_AlwaysOK(t *testing.T) {
	// Liveness não depende do banco.
	r := newHealthRouter(newPool(t, "postgres://catalogo:catalogo@127.0.0.1:1/catalogo_test?sslmode=disable&connect_timeout=1"))
	code, body := get(r, "/health")
	if code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("esperado 200 ok, obtido %d %v", code, body)
	}
}

func TestReady_DatabaseUnavailable(t *testing.T) {
	r := newHealthRouter(newPool(t, "postgres://catalogo:segredo@127.0.0.1:1/catalogo_test?sslmode=disable&connect_timeout=1"))
	code, body := get(r, "/ready")
	if code != http.StatusServiceUnavailable || body["status"] != "not ready" {
		t.Fatalf("esperado 503 not ready, obtido %d %v", code, body)
	}
	for _, v := range body {
		if v == "segredo" || len(v) > 100 {
			t.Fatalf("a resposta não deve vazar detalhes da conexão: %v", body)
		}
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Integração com o Postgres do CI (services.postgres); pula quando ele não está no ar.
func TestReady_DatabaseAvailable(t *testing.T) {
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s&connect_timeout=2",
		envOr("DB_USER", "catalogo"), envOr("DB_PASSWORD", "catalogo"),
		envOr("DB_HOST", "localhost"), envOr("DB_PORT", "5432"),
		envOr("DB_NAME", "catalogo_test"), envOr("DB_SSL_MODE", "disable"))
	pool := newPool(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres indisponível (%v); pulando teste de integração", err)
	}

	code, body := get(newHealthRouter(pool), "/ready")
	if code != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("esperado 200 ready, obtido %d %v", code, body)
	}
}
