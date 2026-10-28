package db

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"
)

func unreachableConfig() PoolConfig {
	return PoolConfig{
		Host:         "127.0.0.1",
		Port:         1,
		User:         "catalogo",
		Password:     "catalogo",
		Name:         "catalogo_test",
		SSLMode:      "disable",
		Timezone:     "UTC",
		MaxOpenConns: 2,
		MinConns:     0,
	}
}

func TestConnect_InvalidConfig(t *testing.T) {
	cfg := unreachableConfig()
	cfg.SSLMode = "modo-invalido"

	Pool = nil
	err := Connect(context.Background(), cfg)
	if err == nil {
		t.Fatal("sslmode inválido deveria falhar ao parsear a configuração")
	}
	if Pool != nil {
		t.Fatal("Pool não deve ser definido quando a conexão falha")
	}
}

// Banco inalcançável: o contexto cancelado interrompe a espera entre tentativas
// em vez de esperar todo o backoff.
func TestConnect_StopsRetryingWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	Pool = nil
	start := time.Now()
	err := Connect(ctx, unreachableConfig())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("esperado context.DeadlineExceeded, obtido %v", err)
	}
	if elapsed := time.Since(start); elapsed >= retryBaseWait {
		t.Fatalf("Connect deveria parar com o contexto, levou %v", elapsed)
	}
	if Pool != nil {
		t.Fatal("Pool não deve ser definido quando a conexão falha")
	}
}

func TestClose_WithoutPool(t *testing.T) {
	Pool = nil
	Close() // não deve entrar em pânico
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Integração com o Postgres do CI (services.postgres); pula quando ele não está no ar.
func TestConnect_Integration(t *testing.T) {
	port, err := strconv.Atoi(envOr("DB_PORT", "5432"))
	if err != nil {
		t.Fatalf("DB_PORT inválida: %v", err)
	}
	cfg := PoolConfig{
		Host:         envOr("DB_HOST", "localhost"),
		Port:         port,
		User:         envOr("DB_USER", "catalogo"),
		Password:     envOr("DB_PASSWORD", "catalogo"),
		Name:         envOr("DB_NAME", "catalogo_test"),
		SSLMode:      envOr("DB_SSL_MODE", "disable"),
		Timezone:     "America/Sao_Paulo",
		MaxOpenConns: 3,
		MinConns:     1,
	}

	// Um único intervalo de espera: com o banco fora, a segunda tentativa não chega a rodar.
	ctx, cancel := context.WithTimeout(context.Background(), retryBaseWait/2)
	defer cancel()
	Pool = nil
	if err := Connect(ctx, cfg); err != nil {
		t.Skipf("postgres indisponível (%v); pulando teste de integração", err)
	}
	t.Cleanup(func() {
		Close()
		Pool = nil
	})

	if Pool == nil {
		t.Fatal("Pool deveria ser definido após conectar")
	}
	if got := Pool.Config().MaxConns; got != 3 {
		t.Fatalf("MaxConns esperado 3, obtido %d", got)
	}
	if got := Pool.Config().MinConns; got != 1 {
		t.Fatalf("MinConns esperado 1, obtido %d", got)
	}

	var tz string
	if err := Pool.QueryRow(context.Background(), "SHOW TimeZone").Scan(&tz); err != nil {
		t.Fatalf("SHOW TimeZone: %v", err)
	}
	if tz != "America/Sao_Paulo" {
		t.Fatalf("timezone da sessão esperado America/Sao_Paulo, obtido %q", tz)
	}
}
