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

func TestSearchOrderBy_HasUniqueTieBreaker(t *testing.T) {
	cases := map[string]string{
		"sem texto (recência)": searchOrderBy(false),
		"com texto (rank)":     searchOrderBy(true),
		"híbrida (RRF)":        hybridOrderByRRF,
	}
	for name, orderBy := range cases {
		if !strings.HasSuffix(orderBy, ", ci.id") {
			t.Errorf("%s: ORDER BY sem desempate único por ci.id: %q", name, orderBy)
		}
	}
	if !strings.HasPrefix(searchOrderBy(false), "ORDER BY ci.created_at DESC") {
		t.Errorf("sem texto deve manter a ordem por recência: %q", searchOrderBy(false))
	}
	if !strings.HasPrefix(searchOrderBy(true), "ORDER BY rank DESC") {
		t.Errorf("com texto deve manter a ordem por relevância: %q", searchOrderBy(true))
	}
}

// Itens gravados no mesmo lote do UpsertBatch têm created_at idêntico, e itens
// de mesmo título empatam no rank. Paginando com per_page fixo, cada item
// precisa aparecer exatamente uma vez e a união das páginas precisa cobrir o
// total declarado (DEV-142).
func TestSearch_PaginationIsStableWithTies(t *testing.T) {
	pool := testCatalogPool(t)
	itemRepo := NewCatalogItemRepository(pool)
	searchRepo := NewSearchRepository(pool)
	ctx := context.Background()

	token := fmt.Sprintf("orgpaginacao%d", time.Now().UnixNano())
	const n = 60
	items := make([]*models.CatalogItem, 0, n)
	exts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ext := fmt.Sprintf("%s-%02d", token, i)
		exts = append(exts, ext)
		items = append(items, &models.CatalogItem{
			ExternalID:     ext,
			Source:         models.SourceSalesForce,
			Type:           models.TypeService,
			Title:          "Serviço paginação " + token,
			Organization:   token,
			Status:         models.StatusActive,
			Tags:           []string{},
			Bairros:        []string{},
			TargetAudience: json.RawMessage(`{}`),
			SourceData:     json.RawMessage(`{}`),
		})
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM catalog_items WHERE external_id = ANY($1::text[])`, exts)
	})
	if _, err := itemRepo.UpsertBatch(ctx, items); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Garante o empate total em created_at, como no full sync em staging.
	if _, err := pool.Exec(ctx,
		`UPDATE catalog_items SET created_at = '2026-10-14T15:52:58.846518Z' WHERE external_id = ANY($1::text[])`,
		exts); err != nil {
		t.Fatalf("empata created_at: %v", err)
	}

	for name, q := range map[string]string{"sem texto": "", "com texto": token} {
		t.Run(name, func(t *testing.T) {
			const perPage = 7
			seen := make(map[string]int, n)
			for page := 1; page <= (n+perPage-1)/perPage; page++ {
				results, total, err := searchRepo.Search(ctx, &models.SearchRequest{
					Q:       q,
					Types:   []models.ItemType{models.TypeService},
					Filters: models.SearchFilters{Orgao: token},
					Page:    page,
					PerPage: perPage,
				})
				if err != nil {
					t.Fatalf("search page %d: %v", page, err)
				}
				if total != n {
					t.Fatalf("page %d: total=%d want %d", page, total, n)
				}
				for _, r := range results {
					seen[r.Item.ID.String()]++
				}
			}
			for id, c := range seen {
				if c != 1 {
					t.Errorf("item %s apareceu %d vezes entre as páginas", id, c)
				}
			}
			if len(seen) != n {
				t.Fatalf("união das páginas cobre %d itens distintos, want %d", len(seen), n)
			}
		})
	}
}
