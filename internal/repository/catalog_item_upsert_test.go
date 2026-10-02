package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func testCatalogPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	host := envOr("DB_HOST", "localhost")
	port := envOr("DB_PORT", "5432")
	user := envOr("DB_USER", "catalogo")
	pass := envOr("DB_PASSWORD", "catalogo")
	name := envOr("DB_NAME", "catalogo_test")
	ssl := envOr("DB_SSL_MODE", "disable")

	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=%s",
		user, pass, host, port, name, ssl,
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

	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'catalog_items'
		)
	`).Scan(&exists); err != nil || !exists {
		pool.Close()
		t.Skip("catalog_items ausente; rode migrations antes dos testes de integração")
	}

	t.Cleanup(pool.Close)
	return pool
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestUpsertBatch_ClearsDeletedAtOnReactivation(t *testing.T) {
	pool := testCatalogPool(t)
	repo := NewCatalogItemRepository(pool)
	ctx := context.Background()

	extID := fmt.Sprintf("test-reactivate-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM catalog_items WHERE external_id = $1`, extID)
	})

	item := &models.CatalogItem{
		ExternalID:     extID,
		Source:         models.SourceJobs,
		Type:           models.TypeJob,
		Title:          "before soft-delete",
		Status:         models.StatusActive,
		Tags:           []string{},
		Bairros:        []string{},
		TargetAudience: json.RawMessage(`{}`),
		SourceData:     json.RawMessage(`{}`),
	}
	if _, err := repo.UpsertBatch(ctx, []*models.CatalogItem{item}); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}
	if err := repo.SoftDelete(ctx, models.SourceJobs, extID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	var deletedBefore bool
	if err := pool.QueryRow(ctx, `
		SELECT deleted_at IS NOT NULL FROM catalog_items
		WHERE source = 'jobs' AND external_id = $1
	`, extID).Scan(&deletedBefore); err != nil {
		t.Fatalf("select before: %v", err)
	}
	if !deletedBefore {
		t.Fatal("expected deleted_at set after SoftDelete")
	}

	item.Title = "after reactivation"
	item.Status = models.StatusActive
	if _, err := repo.UpsertBatch(ctx, []*models.CatalogItem{item}); err != nil {
		t.Fatalf("reactivation upsert: %v", err)
	}

	var title string
	var softDeleted bool
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT title, status, deleted_at IS NOT NULL
		FROM catalog_items WHERE source = 'jobs' AND external_id = $1
	`, extID).Scan(&title, &status, &softDeleted); err != nil {
		t.Fatalf("select after: %v", err)
	}
	if softDeleted {
		t.Fatal("UpsertBatch must clear deleted_at on conflict")
	}
	if status != string(models.StatusActive) || title != "after reactivation" {
		t.Fatalf("got title=%q status=%q", title, status)
	}
}

func TestSoftDeleteActiveNotIn_RemovesOrphans(t *testing.T) {
	pool := testCatalogPool(t)
	repo := NewCatalogItemRepository(pool)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	keepID := "test-keep-" + suffix
	orphanID := "test-orphan-" + suffix
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM catalog_items WHERE external_id = ANY($1::text[])`,
			[]string{keepID, orphanID})
	})

	mk := func(id, title string) *models.CatalogItem {
		return &models.CatalogItem{
			ExternalID:     id,
			Source:         models.SourceCourses,
			Type:           models.TypeCourse,
			Title:          title,
			Status:         models.StatusActive,
			Tags:           []string{},
			Bairros:        []string{},
			TargetAudience: json.RawMessage(`{}`),
			SourceData:     json.RawMessage(`{}`),
		}
	}
	if _, err := repo.UpsertBatch(ctx, []*models.CatalogItem{mk(keepID, "keep"), mk(orphanID, "orphan")}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}

	n, err := repo.SoftDeleteActiveNotIn(ctx, models.SourceCourses, []string{keepID})
	if err != nil {
		t.Fatalf("SoftDeleteActiveNotIn: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected >=1 orphan deactivated, got %d", n)
	}

	assertVisible := func(id string, want bool) {
		t.Helper()
		var visible bool
		err := pool.QueryRow(ctx, `
			SELECT deleted_at IS NULL AND status = 'active'
			FROM catalog_items WHERE source = 'courses' AND external_id = $1
		`, id).Scan(&visible)
		if err != nil {
			t.Fatalf("select %s: %v", id, err)
		}
		if visible != want {
			t.Fatalf("%s visible=%v want=%v", id, visible, want)
		}
	}
	assertVisible(keepID, true)
	assertVisible(orphanID, false)
}
