package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

// A CTE fts precisa escolher as 50 correspondências de maior rank, em ordem
// total: o LIMIT 50 sem ORDER BY pega 50 linhas quaisquer, e o ROW_NUMBER() sem
// desempate dá posições (que pesam no RRF) diferentes entre páginas (DEV-143).
func TestHybridFTSCTE_OrdersByRankWithTieBreakerBeforeLimit(t *testing.T) {
	cte := hybridFTSCTE("ci.status = 'active'")

	window := cte[strings.Index(cte, "ROW_NUMBER() OVER ("):]
	window = window[:strings.Index(window, ") AS rn")]
	if !strings.Contains(window, "DESC, ci.id") {
		t.Errorf("ROW_NUMBER() da fts sem desempate por ci.id depois do rank: %q", window)
	}

	orderIdx := strings.Index(cte, "ORDER BY rn")
	limitIdx := strings.Index(cte, "LIMIT 50")
	if orderIdx < 0 || limitIdx < 0 || orderIdx > limitIdx {
		t.Fatalf("fts precisa de ORDER BY rn antes do LIMIT 50:\n%s", cte)
	}
	if strings.Count(cte, "LIMIT") != 1 {
		t.Fatalf("fts deve ter um único LIMIT:\n%s", cte)
	}

	for name, ctes := range map[string]string{
		"híbrida": hybridSearchCTEs("TRUE"),
		"hyde":    hydeSearchCTEs("TRUE"),
	} {
		if !strings.Contains(ctes, hybridFTSCTE("TRUE")) {
			t.Errorf("%s: CTEs não usam a fts ordenada", name)
		}
	}
}

// O total da busca híbrida é o tamanho da lista RRF, calculado antes do
// LIMIT/OFFSET, e a ordem final segue desempatada por ci.id (DEV-142).
func TestHybridPageSQL_CountsWholeRRFList(t *testing.T) {
	sql := hybridPageSQL(hybridSearchCTEs("TRUE"), "LIMIT $4 OFFSET $5")
	if !strings.Contains(sql, "COUNT(*) OVER () AS rrf_total") {
		t.Errorf("página híbrida sem o total da lista RRF:\n%s", sql)
	}
	orderIdx := strings.Index(sql, hybridOrderByRRF)
	limitIdx := strings.Index(sql, "LIMIT $4 OFFSET $5")
	if orderIdx < 0 || limitIdx < orderIdx {
		t.Errorf("página híbrida precisa de %q antes do LIMIT/OFFSET:\n%s", hybridOrderByRRF, sql)
	}
	if count := hybridCountSQL(hybridSearchCTEs("TRUE")); !strings.Contains(count, "SELECT COUNT(*) FROM rrf") {
		t.Errorf("contagem da lista RRF ausente:\n%s", count)
	}
}

// testVectorLiteral monta um vetor de 1536 dimensões (a da coluna embedding)
// com x na primeira posição, y na segunda e zero no resto.
func testVectorLiteral(x, y float64) string {
	parts := make([]string, 1536)
	for i := range parts {
		parts[i] = "0"
	}
	parts[0] = fmt.Sprintf("%g", x)
	parts[1] = fmt.Sprintf("%g", y)
	return "[" + strings.Join(parts, ",") + "]"
}

// Com mais de 50 correspondências no FTS, a busca híbrida pagina sobre um
// conjunto fixo: as 50 de maior rank (empates resolvidos por ci.id) somadas aos
// vizinhos semânticos. Cada item aparece uma vez, a união das páginas é a lista
// RRF inteira e o total é o tamanho dela, também numa página além do fim
// (DEV-143).
func TestSearchHybrid_PaginationIsStableOverFTSLimit(t *testing.T) {
	pool := testCatalogPool(t)
	itemRepo := NewCatalogItemRepository(pool)
	searchRepo := NewSearchRepository(pool)
	ctx := context.Background()

	token := fmt.Sprintf("orghibrida%d", time.Now().UnixNano())
	const (
		nTop      = 3  // título igual ao termo: maior rank no FTS
		nTied     = 60 // mesmo título: empatados no rank
		nSemantic = 5  // sem o termo no texto, só com embedding
		ftsLimit  = 50
		wantTotal = ftsLimit + nSemantic
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
	var exts, topExts, tiedExts, semExts []string
	for i := 0; i < nTop; i++ {
		ext := fmt.Sprintf("%s-top-%02d", token, i)
		topExts = append(topExts, ext)
		items = append(items, newItem(ext, token))
	}
	for i := 0; i < nTied; i++ {
		ext := fmt.Sprintf("%s-tied-%02d", token, i)
		tiedExts = append(tiedExts, ext)
		items = append(items, newItem(ext, "Serviço comum "+token))
	}
	for i := 0; i < nSemantic; i++ {
		ext := fmt.Sprintf("%s-sem-%02d", token, i)
		semExts = append(semExts, ext)
		items = append(items, newItem(ext, fmt.Sprintf("Atendimento vetorial %d", i)))
	}
	exts = append(append(append(exts, topExts...), tiedExts...), semExts...)

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM catalog_items WHERE external_id = ANY($1::text[])`, exts)
	})
	if _, err := itemRepo.UpsertBatch(ctx, items); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for i, ext := range semExts {
		if _, err := pool.Exec(ctx,
			`UPDATE catalog_items SET embedding = $1::vector WHERE external_id = $2`,
			testVectorLiteral(1, float64(i+1)*0.1), ext); err != nil {
			t.Fatalf("embedding %s: %v", ext, err)
		}
	}

	idsOf := func(group []string) []string {
		rows, err := pool.Query(ctx,
			`SELECT id::text FROM catalog_items WHERE external_id = ANY($1::text[])`, group)
		if err != nil {
			t.Fatalf("ids: %v", err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan id: %v", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("ids: %v", err)
		}
		sort.Strings(ids) // ordem textual do UUID = ordem do tipo uuid no Postgres
		return ids
	}
	topIDs, tiedIDs, semIDs := idsOf(topExts), idsOf(tiedExts), idsOf(semExts)
	// Entre os empatados, entram no LIMIT 50 os de menor ci.id.
	tiedIn := tiedIDs[:ftsLimit-nTop]
	tiedOut := tiedIDs[ftsLimit-nTop:]

	queryVec := testVectorLiteral(1, 0)
	searchers := map[string]func(page, perPage int) ([]*SearchResult, int, error){
		"híbrida": func(page, perPage int) ([]*SearchResult, int, error) {
			return searchRepo.SearchHybrid(ctx, &models.SearchRequest{
				Q: token, Types: []models.ItemType{models.TypeService},
				Filters: models.SearchFilters{Orgao: token}, Page: page, PerPage: perPage,
			}, queryVec)
		},
		"hyde": func(page, perPage int) ([]*SearchResult, int, error) {
			return searchRepo.SearchHybridWithHyDE(ctx, &models.SearchRequest{
				Q: token, Types: []models.ItemType{models.TypeService},
				Filters: models.SearchFilters{Orgao: token}, Page: page, PerPage: perPage,
			}, queryVec, queryVec)
		},
	}

	for name, search := range searchers {
		t.Run(name, func(t *testing.T) {
			const perPage = 7
			pages := (wantTotal + perPage - 1) / perPage
			seen := make(map[string]int, wantTotal)
			var firstPage []string
			for page := 1; page <= pages; page++ {
				results, total, err := search(page, perPage)
				if err != nil {
					t.Fatalf("page %d: %v", page, err)
				}
				if total != wantTotal {
					t.Fatalf("page %d: total=%d want %d (tamanho da lista RRF)", page, total, wantTotal)
				}
				if page < pages && len(results) != perPage {
					t.Fatalf("page %d: %d itens, want %d", page, len(results), perPage)
				}
				for _, r := range results {
					seen[r.Item.ID.String()]++
					if page == 1 {
						firstPage = append(firstPage, r.Item.ID.String())
					}
				}
			}
			for id, c := range seen {
				if c != 1 {
					t.Errorf("item %s apareceu %d vezes entre as páginas", id, c)
				}
			}
			if len(seen) != wantTotal {
				t.Fatalf("união das páginas cobre %d itens distintos, want %d", len(seen), wantTotal)
			}
			for _, group := range []struct {
				name string
				ids  []string
				in   bool
			}{
				{"maior rank no FTS", topIDs, true},
				{"empatados de menor id", tiedIn, true},
				{"semânticos", semIDs, true},
				{"empatados além do LIMIT 50", tiedOut, false},
			} {
				for _, id := range group.ids {
					if (seen[id] == 1) != group.in {
						t.Errorf("%s: item %s presente=%v, want %v", group.name, id, seen[id] == 1, group.in)
					}
				}
			}

			again, _, err := search(1, perPage)
			if err != nil {
				t.Fatalf("page 1 de novo: %v", err)
			}
			for i, r := range again {
				if i >= len(firstPage) || r.Item.ID.String() != firstPage[i] {
					t.Fatalf("page 1 mudou entre execuções na posição %d", i)
				}
			}

			beyond, total, err := search(pages+1, perPage)
			if err != nil {
				t.Fatalf("página além do fim: %v", err)
			}
			if len(beyond) != 0 || total != wantTotal {
				t.Fatalf("página além do fim: %d itens, total=%d; want 0 e %d", len(beyond), total, wantTotal)
			}
		})
	}
}
