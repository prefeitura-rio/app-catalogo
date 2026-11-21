package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
// parâmetro, sobre os vizinhos já ordenados: o piso vale para o RRF e para o
// total (DEV-601). A lista _knn, sem piso, fica para a observação de calibração.
func TestSemanticCTEs_FilterByOwnMaxDistance(t *testing.T) {
	for name, tc := range map[string]struct {
		ctes  string
		lists map[string][2]string // lista -> (vetor, distância máxima)
	}{
		"híbrida": {hybridSearchCTEs("TRUE"), map[string][2]string{"sem": {"$3", "$4"}}},
		"hyde":    {hydeSearchCTEs("TRUE"), map[string][2]string{"sem": {"$3", "$5"}, "hyde": {"$4", "$6"}}},
	} {
		for list, p := range tc.lists {
			knn := hybridSemanticCTE(list, p[0], p[1], "TRUE")
			if !strings.Contains(tc.ctes, knn) {
				t.Errorf("%s: sem a CTE %s com vetor %s e piso %s:\n%s", name, list, p[0], p[1], tc.ctes)
			}
			knnStart := strings.Index(knn, list+"_knn AS (")
			distCol := strings.Index(knn, "ci.embedding <=> "+p[0]+"::vector AS dist")
			limit := strings.Index(knn, "LIMIT 50")
			floorStart := strings.Index(knn, "\n\t\t"+list+" AS (")
			floor := strings.Index(knn, "FROM "+list+"_knn k\n\t\t\tWHERE k.dist <= "+p[1]+"::float8")
			if knnStart < 0 || distCol < knnStart || limit < distCol || floorStart < limit || floor < floorStart {
				t.Errorf("%s: CTE %s fora da forma knn (LIMIT 50) -> piso:\n%s", name, list, knn)
			}
		}
	}
}

// semanticFixture grava três itens de um órgão próprio com vetores em relação a
// (1,0): perto = similaridade 1, meio ≈ 0,707, longe = 0 (e similaridade 1
// com (0,1)). Devolve o órgão, que isola os itens nos filtros da busca.
func semanticFixture(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	pool := testCatalogPool(t)
	itemRepo := NewCatalogItemRepository(pool)
	ctx := context.Background()

	token := fmt.Sprintf("orgpiso%d", time.Now().UnixNano())
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
	return pool, token
}

// semanticFixtureRequest é uma busca sem termo real (nada casa o FTS) restrita
// ao órgão da fixture: só as listas semânticas trazem itens.
func semanticFixtureRequest(token string, page int) *models.SearchRequest {
	return &models.SearchRequest{
		Q: "zzqsemtermo" + token, Types: []models.ItemType{models.TypeService},
		Filters: models.SearchFilters{Orgao: token}, Page: page, PerPage: 10,
	}
}

// Item semântico abaixo do piso da lista não entra no RRF nem no total; com o
// piso desligado a busca segue trazendo os vizinhos como antes (DEV-601).
func TestSearchHybrid_SemanticFloorsCutListAndTotal(t *testing.T) {
	pool, token := semanticFixture(t)
	ctx := context.Background()
	req := func() *models.SearchRequest { return semanticFixtureRequest(token, 1) }
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

// A melhor similaridade de cada lista é observada antes do piso, uma vez por
// busca (só na primeira página), inclusive quando o piso esvazia a resposta
// (DEV-601).
func TestSearchHybrid_ObservesBestSimilarityBeforeFloor(t *testing.T) {
	pool, token := semanticFixture(t)
	ctx := context.Background()
	// (1,-1) tem similaridade ≈ 0,707 com perto, 0 com meio e ≈ -0,707 com longe;
	// (0,1) tem similaridade 1 com longe.
	queryVec, hydeVec := testVectorLiteral(1, -1), testVectorLiteral(0, 1)

	type run struct {
		name   string
		floors SemanticFloors
		hyde   bool
		page   int
		want   map[string]float64 // lista -> melhor similaridade
		total  int
		fts    bool // busca com termo que casa o FTS (os títulos da fixture)
	}
	runs := []run{
		{"híbrida sem piso", SemanticFloors{}, false, 1, map[string]float64{SemanticListQuery: math.Sqrt2 / 2}, 3, false},
		{"híbrida com piso que esvazia", SemanticFloors{Query: 0.9}, false, 1, map[string]float64{SemanticListQuery: math.Sqrt2 / 2}, 0, false},
		{"hyde com pisos", SemanticFloors{Query: 0.9, HyDE: 0.9}, true, 1,
			map[string]float64{SemanticListQuery: math.Sqrt2 / 2, SemanticListHyDE: 1}, 1, false},
		{"hyde página 2", SemanticFloors{}, true, 2, map[string]float64{}, 3, false},
		{"híbrida com termo real", SemanticFloors{Query: 0.9}, false, 1, map[string]float64{SemanticListQuery: math.Sqrt2 / 2}, 3, true},
	}
	for _, tc := range runs {
		t.Run(tc.name, func(t *testing.T) {
			var got []SemanticObservation
			repo := NewSearchRepository(pool).WithSemanticFloors(tc.floors).
				WithSemanticObserver(func(o SemanticObservation) { got = append(got, o) })
			var (
				total int
				err   error
			)
			req := semanticFixtureRequest(token, tc.page)
			if tc.fts {
				req.Q = "vetorial"
			}
			if tc.hyde {
				_, total, err = repo.SearchHybridWithHyDE(ctx, req, queryVec, hydeVec)
			} else {
				_, total, err = repo.SearchHybrid(ctx, req, queryVec)
			}
			if err != nil {
				t.Fatalf("busca: %v", err)
			}
			if total != tc.total {
				t.Errorf("total = %d, want %d", total, tc.total)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("observações = %+v, want %v", got, tc.want)
			}
			for _, o := range got {
				want, ok := tc.want[o.List]
				if !ok || math.Abs(o.BestSimilarity-want) > 1e-6 {
					t.Errorf("observação %+v, want similaridade %v", o, want)
				}
				if o.FTSMatch != tc.fts {
					t.Errorf("observação %+v: FTSMatch want %v", o, tc.fts)
				}
			}
		})
	}
}
