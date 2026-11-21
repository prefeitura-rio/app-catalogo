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

func TestMaxCosineDistance(t *testing.T) {
	cases := []struct {
		floor, want float64
	}{
		{0, 2},     // piso desligado: toda distância de cosseno (0 a 2) passa
		{-0.1, 2},  // negativo também desliga
		{0.7, 0.3}, // similaridade >= 0,7 = distância <= 0,3
		{0.55, 0.45},
	}
	for _, tc := range cases {
		if got := maxCosineDistance(tc.floor); fmt.Sprintf("%.6f", got) != fmt.Sprintf("%.6f", tc.want) {
			t.Errorf("maxCosineDistance(%v) = %v, want %v", tc.floor, got, tc.want)
		}
	}
}

// Cada lista semântica corta pela distância do próprio vetor, com o próprio
// parâmetro, antes do ORDER BY/LIMIT: o piso vale para o RRF e para o total
// (DEV-601).
func TestSemanticCTEs_FilterByOwnMaxDistance(t *testing.T) {
	cte := hybridSemanticCTE("sem", "$3", "$4", "TRUE")
	cond := "(ci.embedding <=> $3::vector) <= $4::float8"
	condIdx := strings.Index(cte, cond)
	orderIdx := strings.LastIndex(cte, "ORDER BY ci.embedding")
	if condIdx < 0 || orderIdx < 0 || condIdx > orderIdx {
		t.Fatalf("CTE semântica sem o piso %q no WHERE:\n%s", cond, cte)
	}

	for name, tc := range map[string]struct {
		ctes  string
		conds []string
	}{
		"híbrida": {hybridSearchCTEs("TRUE"), []string{
			"(ci.embedding <=> $3::vector) <= $4::float8",
		}},
		"hyde": {hydeSearchCTEs("TRUE"), []string{
			"(ci.embedding <=> $3::vector) <= $5::float8",
			"(ci.embedding <=> $4::vector) <= $6::float8",
		}},
	} {
		for _, c := range tc.conds {
			if !strings.Contains(tc.ctes, c) {
				t.Errorf("%s: sem o piso %q:\n%s", name, c, tc.ctes)
			}
		}
	}
}

// Item semântico abaixo do piso da lista não entra no RRF nem no total; com o
// piso desligado a busca segue trazendo os vizinhos como antes (DEV-601).
func TestSearchHybrid_SemanticFloorsCutListAndTotal(t *testing.T) {
	pool := testCatalogPool(t)
	itemRepo := NewCatalogItemRepository(pool)
	ctx := context.Background()

	token := fmt.Sprintf("orgpiso%d", time.Now().UnixNano())
	// Vetores em relação à query (1,0): perto = similaridade 1, meio ≈ 0,707,
	// longe = 0 (e similaridade 1 com o vetor HyDE (0,1)).
	vectors := map[string][2]float64{
		"perto": {1, 0},
		"meio":  {1, 1},
		"longe": {0, 1},
	}
	var items []*models.CatalogItem
	var exts []string
	for name := range vectors {
		ext := token + "-" + name
		exts = append(exts, ext)
		items = append(items, &models.CatalogItem{
			ExternalID:     ext,
			Source:         models.SourceSalesForce,
			Type:           models.TypeService,
			Title:          "Atendimento vetorial " + name,
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
	for name, v := range vectors {
		if _, err := pool.Exec(ctx,
			`UPDATE catalog_items SET embedding = $1::vector WHERE external_id = $2`,
			testVectorLiteral(v[0], v[1]), token+"-"+name); err != nil {
			t.Fatalf("embedding %s: %v", name, err)
		}
	}

	// Query sem termo real: nada casa o FTS, só as listas semânticas trazem itens.
	req := func() *models.SearchRequest {
		return &models.SearchRequest{
			Q: "zzqsemtermo" + token, Types: []models.ItemType{models.TypeService},
			Filters: models.SearchFilters{Orgao: token}, Page: 1, PerPage: 10,
		}
	}
	queryVec, hydeVec := testVectorLiteral(1, 0), testVectorLiteral(0, 1)
	titles := func(results []*SearchResult) []string {
		var out []string
		for _, r := range results {
			out = append(out, strings.TrimPrefix(r.Item.Title, "Atendimento vetorial "))
		}
		sort.Strings(out)
		return out
	}

	cases := []struct {
		name   string
		floors SemanticFloors
		hyde   bool
		want   []string
	}{
		{"híbrida sem piso", SemanticFloors{}, false, []string{"longe", "meio", "perto"}},
		{"híbrida com piso 0,5", SemanticFloors{Query: 0.5}, false, []string{"meio", "perto"}},
		{"híbrida com piso 0,9", SemanticFloors{Query: 0.9}, false, []string{"perto"}},
		{"hyde sem piso", SemanticFloors{}, true, []string{"longe", "meio", "perto"}},
		// Cada lista com o seu piso: a da query só aceita "perto", a do HyDE só "longe".
		{"hyde com pisos 0,9", SemanticFloors{Query: 0.9, HyDE: 0.9}, true, []string{"longe", "perto"}},
		{"hyde só com piso da query", SemanticFloors{Query: 0.9}, true, []string{"longe", "meio", "perto"}},
		{"hyde com pisos 0,99", SemanticFloors{Query: 0.99, HyDE: 0.99}, true, []string{"longe", "perto"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := NewSearchRepository(pool).WithSemanticFloors(tc.floors)
			var (
				results []*SearchResult
				total   int
				err     error
			)
			if tc.hyde {
				results, total, err = repo.SearchHybridWithHyDE(ctx, req(), queryVec, hydeVec)
			} else {
				results, total, err = repo.SearchHybrid(ctx, req(), queryVec)
			}
			if err != nil {
				t.Fatalf("busca: %v", err)
			}
			got := titles(results)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("itens = %v, want %v", got, tc.want)
			}
			if total != len(tc.want) {
				t.Errorf("total = %d, want %d (o piso também vale para o total)", total, len(tc.want))
			}
		})
	}
}
