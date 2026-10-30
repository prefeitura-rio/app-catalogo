package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prefeitura-rio/app-catalogo/internal/cache"
	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// integrationPool conecta no Postgres do CI (services.postgres) com
// enable_indexscan desligado: a busca semântica vira varredura exata, sem a
// aproximação do HNSW, e o teste compara listas exatas. Pula sem o banco.
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		envOr("DB_USER", "catalogo"), envOr("DB_PASSWORD", "catalogo"),
		envOr("DB_HOST", "localhost"), envOr("DB_PORT", "5432"),
		envOr("DB_NAME", "catalogo_test"), envOr("DB_SSL_MODE", "disable"))
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["enable_indexscan"] = "off"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("postgres indisponível (%v); pulando teste de integração", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres ping falhou (%v); pulando teste de integração", err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'catalog_items' AND column_name = 'embedding'
		)`).Scan(&exists); err != nil || !exists {
		pool.Close()
		t.Skip("catalog_items.embedding ausente; rode migrations antes dos testes de integração")
	}
	t.Cleanup(pool.Close)
	return pool
}

// integrationRedis conecta no Redis do CI (services.redis); pula sem ele.
func integrationRedis(t *testing.T) *cache.RedisCache {
	t.Helper()
	port, err := strconv.Atoi(envOr("REDIS_PORT", "6379"))
	if err != nil {
		t.Fatalf("REDIS_PORT inválida: %v", err)
	}
	c := cache.NewRedisCache(envOr("REDIS_HOST", "localhost"), port, os.Getenv("REDIS_PASSWORD"), 0, 4, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Skipf("redis indisponível (%v); pulando teste de integração", err)
	}
	return c
}

// angleVector é um vetor de 1536 dimensões (a da coluna embedding) no ângulo
// deg do plano das duas primeiras dimensões.
func angleVector(deg float64) []float32 {
	v := make([]float32, 1536)
	rad := deg * math.Pi / 180
	v[0] = float32(math.Cos(rad))
	v[1] = float32(math.Sin(rad))
	return v
}

// Reprodução da reprovação na 6.2 do DEV-143: o HyDE muda a cada requisição, e
// cada página é uma requisição. Sem os vetores guardados por busca, a lista RRF
// e o total mudam entre as páginas (controle); com eles, as páginas cobrem a
// lista inteira uma única vez e o total é o mesmo em todas, também além do fim.
func TestSearchService_HybridPaginationStableWhenHyDEVaries(t *testing.T) {
	pool := integrationPool(t)
	redis := integrationRedis(t)
	ctx := context.Background()

	token := fmt.Sprintf("orgvetores%d", time.Now().UnixNano())
	const (
		nFTS      = 5  // título com o termo, sem embedding
		nSemantic = 80 // embeddings espalhados de 0° a 177,75°
		wantTotal = nFTS + nSemantic
	)

	newItem := func(ext, title string) *models.CatalogItem {
		return &models.CatalogItem{
			ExternalID:     ext,
			Source:         models.SourceSalesForce,
			Type:           models.TypeService,
			Title:          title,
			Organization:   token,
			Status:         models.StatusActive,
			Tags:           []string{},
			Bairros:        []string{},
			TargetAudience: json.RawMessage(`{}`),
			SourceData:     json.RawMessage(`{}`),
		}
	}
	var items []*models.CatalogItem
	var exts, semExts []string
	for i := 0; i < nFTS; i++ {
		ext := fmt.Sprintf("%s-fts-%02d", token, i)
		exts = append(exts, ext)
		items = append(items, newItem(ext, "Serviço "+token))
	}
	for i := 0; i < nSemantic; i++ {
		ext := fmt.Sprintf("%s-sem-%02d", token, i)
		exts = append(exts, ext)
		semExts = append(semExts, ext)
		items = append(items, newItem(ext, fmt.Sprintf("Item vetorial %d", i)))
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM catalog_items WHERE external_id = ANY($1::text[])`, exts)
	})
	if _, err := repository.NewCatalogItemRepository(pool).UpsertBatch(ctx, items); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for i, ext := range semExts {
		if _, err := pool.Exec(ctx,
			`UPDATE catalog_items SET embedding = $1::vector WHERE external_id = $2`,
			clients.VectorLiteral(angleVector(float64(i)*2.25)), ext); err != nil {
			t.Fatalf("embedding %s: %v", ext, err)
		}
	}

	// A query aponta para 0° (vizinhos: itens 0..49). O HyDE alterna entre
	// 180° (itens 30..79, lista RRF com 85) e ~0° (itens 0..49, lista com 55).
	newEncoder := func() *varyingEncoder {
		return &varyingEncoder{
			queryVec: func(string) []float32 { return angleVector(0) },
			hydeVec: func(call int) []float32 {
				if call%2 == 1 {
					return angleVector(180)
				}
				return angleVector(0.5)
			},
		}
	}
	searchRepo := repository.NewSearchRepository(pool)
	newService := func(store vectorStore) *SearchService {
		svc := NewSearchService(searchRepo, redis, time.Minute, time.Minute, nil, nil)
		svc.encoder = newEncoder()
		svc.vectorStore = store
		return svc
	}

	var cacheKeys []string
	t.Cleanup(func() {
		if len(cacheKeys) > 0 {
			_ = redis.Del(context.Background(), cacheKeys...)
		}
	})
	search := func(t *testing.T, svc *SearchService, page, perPage int) *models.SearchResponse {
		t.Helper()
		req := &models.SearchRequest{
			Q: token, Types: []models.ItemType{models.TypeService},
			Filters: models.SearchFilters{Orgao: token}, Page: page, PerPage: perPage,
		}
		cacheKeys = append(cacheKeys, svc.cacheKey(req))
		resp, err := svc.Search(ctx, req)
		if err != nil {
			t.Fatalf("página %d: %v", page, err)
		}
		return resp
	}

	t.Run("controle sem vetores guardados", func(t *testing.T) {
		unavailable := newMemoryStore()
		unavailable.err = errors.New("sem cache de vetores")
		svc := newService(unavailable)
		first := search(t, svc, 1, 9)
		second := search(t, svc, 2, 9)
		if first.Total == second.Total {
			t.Fatalf("controle: com o HyDE variando, o total deveria mudar entre páginas (obtido %d e %d)", first.Total, second.Total)
		}
	})

	t.Run("vetores guardados no Redis", func(t *testing.T) {
		cacheKeys = append(cacheKeys, searchVectorsCacheKey(token))
		svc := newService(redis)
		const perPage = 7
		pages := (wantTotal + perPage - 1) / perPage
		seen := make(map[string]int, wantTotal)
		for page := 1; page <= pages; page++ {
			resp := search(t, svc, page, perPage)
			if resp.Total != wantTotal {
				t.Fatalf("página %d: total=%d, esperado %d em todas as páginas", page, resp.Total, wantTotal)
			}
			if page < pages && len(resp.Items) != perPage {
				t.Fatalf("página %d: %d itens, esperado %d", page, len(resp.Items), perPage)
			}
			for _, it := range resp.Items {
				seen[it.ID]++
			}
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("item %s apareceu %d vezes entre as páginas", id, n)
			}
		}
		if len(seen) != wantTotal {
			t.Fatalf("união das páginas cobre %d itens, esperado %d", len(seen), wantTotal)
		}

		beyond := search(t, svc, pages+1, perPage)
		if beyond.Total != wantTotal || len(beyond.Items) != 0 {
			t.Fatalf("além do fim: total=%d itens=%d, esperado total %d e nenhum item", beyond.Total, len(beyond.Items), wantTotal)
		}
		if calls := svc.encoder.(*varyingEncoder).calls(); calls != 1 {
			t.Fatalf("HyDE gerado %d vezes; esperado uma vez por busca", calls)
		}
	})
}
