package cache

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestItoa(t *testing.T) {
	for _, n := range []int{0, 1, 9, 10, 6379, -1, -42, math.MaxInt32, math.MinInt32 + 1} {
		if got, want := itoa(n), strconv.Itoa(n); got != want {
			t.Errorf("itoa(%d) = %q, esperado %q", n, got, want)
		}
	}
}

func TestIsMiss(t *testing.T) {
	if !IsMiss(ErrCacheMiss) {
		t.Fatal("ErrCacheMiss deve ser miss")
	}
	if !IsMiss(fmt.Errorf("busca: %w", ErrCacheMiss)) {
		t.Fatal("ErrCacheMiss embrulhado deve ser miss")
	}
	if IsMiss(nil) || IsMiss(errors.New("conexão recusada")) {
		t.Fatal("outros erros não são miss")
	}
}

func TestNewRedisCache_Addr(t *testing.T) {
	c := NewRedisCache("redis.example.test", 6380, "", 2, 5, 1)
	opts := c.client.Options()
	if opts.Addr != "redis.example.test:6380" || opts.DB != 2 || opts.PoolSize != 5 || opts.MinIdleConns != 1 {
		t.Fatalf("opções inesperadas: addr=%s db=%d pool=%d idle=%d", opts.Addr, opts.DB, opts.PoolSize, opts.MinIdleConns)
	}
	_ = c.client.Close()
}

// Redis inalcançável: os erros sobem e nunca viram cache miss.
func TestRedisCache_Unavailable(t *testing.T) {
	c := NewRedisCache("127.0.0.1", 1, "", 0, 1, 0)
	t.Cleanup(func() { _ = c.client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := c.Ping(ctx); err == nil {
		t.Fatal("Ping deveria falhar")
	}
	var dest string
	if err := c.Get(ctx, "k", &dest); err == nil || IsMiss(err) {
		t.Fatalf("Get deveria falhar sem ser miss, obtido %v", err)
	}
	if err := c.Set(ctx, "k", "v", time.Minute); err == nil {
		t.Fatal("Set deveria falhar")
	}
	if err := c.Del(ctx, "k"); err == nil {
		t.Fatal("Del deveria falhar")
	}
}

func TestRedisCache_SetRejectsUnserializableValue(t *testing.T) {
	c := NewRedisCache("127.0.0.1", 1, "", 0, 1, 0)
	t.Cleanup(func() { _ = c.client.Close() })
	if err := c.Set(context.Background(), "k", make(chan int), time.Minute); err == nil {
		t.Fatal("valor não serializável deveria falhar antes de chegar ao Redis")
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Integração com o Redis do CI (services.redis); pula quando ele não está no ar.
func newIntegrationCache(t *testing.T) *RedisCache {
	t.Helper()
	port, err := strconv.Atoi(envOr("REDIS_PORT", "6379"))
	if err != nil {
		t.Fatalf("REDIS_PORT inválida: %v", err)
	}
	c := NewRedisCache(envOr("REDIS_HOST", "localhost"), port, os.Getenv("REDIS_PASSWORD"), 0, 2, 0)
	t.Cleanup(func() { _ = c.client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Skipf("redis indisponível (%v); pulando teste de integração", err)
	}
	return c
}

func TestRedisCache_Integration(t *testing.T) {
	c := newIntegrationCache(t)
	ctx := context.Background()
	key := fmt.Sprintf("catalogo:test:%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = c.Del(context.Background(), key) })

	type payload struct {
		Title string   `json:"title"`
		Tags  []string `json:"tags"`
	}

	var got payload
	if err := c.Get(ctx, key, &got); !IsMiss(err) {
		t.Fatalf("chave ausente: esperado miss, obtido %v", err)
	}

	want := payload{Title: "Serviço de teste", Tags: []string{"a", "b"}}
	if err := c.Set(ctx, key, want, time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := c.Get(ctx, key, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != want.Title || len(got.Tags) != 2 {
		t.Fatalf("valor lido diferente do gravado: %+v", got)
	}
	if ttl := c.client.TTL(ctx, key).Val(); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("TTL esperado em (0, 1m], obtido %v", ttl)
	}

	var wrongType int
	if err := c.Get(ctx, key, &wrongType); err == nil || IsMiss(err) {
		t.Fatalf("tipo incompatível deveria falhar na desserialização, obtido %v", err)
	}

	if err := c.Del(ctx, key); err != nil {
		t.Fatalf("Del: %v", err)
	}
	if err := c.Get(ctx, key, &got); !IsMiss(err) {
		t.Fatalf("após Del: esperado miss, obtido %v", err)
	}
}

func TestRedisCache_IntegrationExpires(t *testing.T) {
	c := newIntegrationCache(t)
	ctx := context.Background()
	key := fmt.Sprintf("catalogo:test:ttl:%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = c.Del(context.Background(), key) })

	if err := c.Set(ctx, key, "efêmero", 50*time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var v string
		err := c.Get(ctx, key, &v)
		if IsMiss(err) {
			return
		}
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("chave deveria expirar pelo TTL")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
