package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestBuildFilterClauses_ExcludesTypesense(t *testing.T) {
	sql, args, next := buildFilterClauses(&models.SearchRequest{}, 1)
	if !strings.Contains(sql, "ci.source <> 'typesense'") {
		t.Fatalf("expected typesense exclusion in %q", sql)
	}
	if len(args) != 0 {
		t.Fatalf("typesense exclusion must not consume args, got %v", args)
	}
	if next != 1 {
		t.Fatalf("next idx=%d want 1", next)
	}

	sql, args, next = buildFilterClauses(&models.SearchRequest{
		Types: []models.ItemType{models.TypeService},
	}, 2)
	if !strings.Contains(sql, "ci.source <> 'typesense'") {
		t.Fatalf("expected typesense exclusion alongside other filters: %q", sql)
	}
	if !strings.Contains(sql, "ci.type = ANY") {
		t.Fatalf("expected type filter: %q", sql)
	}
	if len(args) != 1 || next != 3 {
		t.Fatalf("args=%v next=%d", args, next)
	}
}

func TestSearch_ExcludesTypesenseSource(t *testing.T) {
	pool := testCatalogPool(t)
	itemRepo := NewCatalogItemRepository(pool)
	searchRepo := NewSearchRepository(pool)
	ctx := context.Background()

	token := fmt.Sprintf("tsfilter%d", time.Now().UnixNano())
	sfExt := "sf-" + token
	tsExt := "ts-" + token

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM catalog_items WHERE external_id = ANY($1::text[])`,
			[]string{sfExt, tsExt})
	})

	sf := &models.CatalogItem{
		ExternalID:     sfExt,
		Source:         models.SourceSalesForce,
		Type:           models.TypeService,
		Title:          "Serviço SF " + token,
		Status:         models.StatusActive,
		Tags:           []string{},
		Bairros:        []string{},
		TargetAudience: json.RawMessage(`{}`),
		SourceData:     json.RawMessage(`{}`),
	}
	ts := &models.CatalogItem{
		ExternalID:     tsExt,
		Source:         models.SourceTypesense,
		Type:           models.TypeService,
		Title:          "Serviço Typesense " + token,
		Status:         models.StatusActive,
		Tags:           []string{},
		Bairros:        []string{},
		TargetAudience: json.RawMessage(`{}`),
		SourceData:     json.RawMessage(`{}`),
	}
	if _, err := itemRepo.UpsertBatch(ctx, []*models.CatalogItem{sf, ts}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	results, total, err := searchRepo.Search(ctx, &models.SearchRequest{
		Q:       token,
		Types:   []models.ItemType{models.TypeService},
		Page:    1,
		PerPage: 20,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	var sawSF, sawTS bool
	for _, r := range results {
		switch r.Item.Source {
		case models.SourceSalesForce:
			if r.Item.ExternalID == sfExt {
				sawSF = true
			}
		case models.SourceTypesense:
			sawTS = true
			t.Errorf("typesense item leaked into search: %s", r.Item.ExternalID)
		}
	}
	if !sawSF {
		t.Fatalf("expected salesforce item in results (total=%d results=%d)", total, len(results))
	}
	if sawTS {
		t.Fatal("search must exclude source=typesense")
	}
}
