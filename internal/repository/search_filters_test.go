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

func boolPtr(b bool) *bool { return &b }

// DEV-575: os filtros de curso e de vaga entram no WHERE de toda busca (FTS,
// híbrida e HyDE usam buildFilterClauses), cada um restrito ao tipo dele.
func TestBuildFilterClauses_FiltrosDeCursoEVaga(t *testing.T) {
	cases := []struct {
		name     string
		filters  models.SearchFilters
		contains []string
		args     []interface{}
	}{
		{"turno", models.SearchFilters{Turno: "noturno"},
			[]string{"(ci.type <> 'course' OR (upper(ci.source_data->>'turno') = $1))"},
			[]interface{}{"NOITE"}},
		{"regime", models.SearchFilters{RegimeContratacao: " temporario "},
			[]string{"ci.type <> 'job' OR", "ci.source_data->'regime_contratacao'->>'descricao'", "lower(unaccent($1))"},
			[]interface{}{"temporario"}},
		{"modelo", models.SearchFilters{ModeloTrabalho: "hibrido"},
			[]string{"ci.type <> 'job' OR", "ci.source_data->'modelo_trabalho'->>'descricao'", "lower(unaccent($1))"},
			[]interface{}{"hibrido"}},
		{"pcd true", models.SearchFilters{PCD: boolPtr(true)},
			[]string{"(ci.type <> 'job' OR (ci.source_data->>'acessibilidade_pcd' = 'exclusivo_pcd'))"}, nil},
		{"pcd false", models.SearchFilters{PCD: boolPtr(false)},
			[]string{"(ci.type <> 'job' OR (ci.source_data->>'acessibilidade_pcd' IS DISTINCT FROM 'exclusivo_pcd'))"}, nil},
		{"faixa com teto", models.SearchFilters{FaixaSalarial: models.FaixaSalarial2a4SM},
			[]string{"ci.type <> 'job' OR", "> $1::float8", "<= $2::float8"},
			[]interface{}{2 * models.SalarioMinimo, 4 * models.SalarioMinimo}},
		{"faixa sem teto", models.SearchFilters{FaixaSalarial: models.FaixaSalarialAcima4SM},
			[]string{"ci.type <> 'job' OR", "> $1::float8"},
			[]interface{}{4 * models.SalarioMinimo}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, args, next := buildFilterClauses(&models.SearchRequest{Filters: tc.filters}, 1)
			for _, want := range tc.contains {
				if !strings.Contains(sql, want) {
					t.Errorf("cláusula sem %q:\n%s", want, sql)
				}
			}
			if fmt.Sprint(args) != fmt.Sprint(tc.args) {
				t.Errorf("args = %v, want %v", args, tc.args)
			}
			if next != 1+len(tc.args) {
				t.Errorf("next = %d, want %d", next, 1+len(tc.args))
			}
		})
	}

	// Vazio e faixa desconhecida não acrescentam cláusula.
	sql, args, _ := buildFilterClauses(&models.SearchRequest{Filters: models.SearchFilters{Turno: "  ", FaixaSalarial: "xyz"}}, 1)
	if strings.Contains(sql, "ci.type <>") || len(args) != 0 {
		t.Errorf("filtro vazio virou cláusula: %q %v", sql, args)
	}
	if strings.Contains(sql, "faixa") {
		t.Errorf("sql inesperado: %q", sql)
	}
}

// DEV-575: o SQL dos filtros roda de verdade contra o Postgres e devolve só os
// itens que casam; os outros tipos não são afetados pelos filtros de curso/vaga.
func TestSearch_FiltrosDeCursoEVaga(t *testing.T) {
	pool := testCatalogPool(t)
	itemRepo := NewCatalogItemRepository(pool)
	searchRepo := NewSearchRepository(pool)
	ctx := context.Background()

	token := fmt.Sprintf("orgfiltros%d", time.Now().UnixNano())
	mk := func(ext string, typ models.ItemType, src models.ItemSource, sourceData string) *models.CatalogItem {
		return &models.CatalogItem{
			ExternalID:     token + "-" + ext,
			Source:         src,
			Type:           typ,
			Title:          "Item " + ext + " " + token,
			Organization:   token,
			Status:         models.StatusActive,
			Tags:           []string{},
			Bairros:        []string{},
			TargetAudience: json.RawMessage(`{}`),
			SourceData:     json.RawMessage(sourceData),
		}
	}
	items := []*models.CatalogItem{
		mk("curso-noite", models.TypeCourse, models.SourceCourses, `{"turno":"NOITE"}`),
		mk("curso-manha", models.TypeCourse, models.SourceCourses, `{"turno":"MANHA"}`),
		mk("vaga-clt", models.TypeJob, models.SourceJobs,
			`{"regime_contratacao":{"descricao":"CLT"},"modelo_trabalho":{"descricao":"Híbrido"},"acessibilidade_pcd":"exclusivo_pcd","valor_vaga":3000}`),
		mk("vaga-temp", models.TypeJob, models.SourceJobs,
			`{"regime_contratacao":{"descricao":"Temporário"},"modelo_trabalho":{"descricao":"Presencial"},"acessibilidade_pcd":"preferencial_pcd","valor_vaga":5000}`),
		mk("vaga-pj", models.TypeJob, models.SourceJobs,
			`{"regime_contratacao":{"descricao":"PJ"},"modelo_trabalho":{"descricao":"Remoto"},"valor_vaga":10000}`),
		mk("vaga-sem-valor", models.TypeJob, models.SourceJobs, `{"valor_vaga":0}`),
		mk("vaga-valor-texto", models.TypeJob, models.SourceJobs, `{"valor_vaga":"a combinar"}`),
		mk("servico", models.TypeService, models.SourceSalesForce, `{}`),
	}
	exts := make([]string, len(items))
	for i, it := range items {
		exts[i] = it.ExternalID
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM catalog_items WHERE external_id = ANY($1::text[])`, exts)
	})
	if _, err := itemRepo.UpsertBatch(ctx, items); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	course := []models.ItemType{models.TypeCourse}
	job := []models.ItemType{models.TypeJob}
	cases := []struct {
		name    string
		types   []models.ItemType
		filters models.SearchFilters
		want    []string
	}{
		{"turno do contrato", course, models.SearchFilters{Turno: "noturno"}, []string{"curso-noite"}},
		{"turno gravado", course, models.SearchFilters{Turno: "manha"}, []string{"curso-manha"}},
		{"turno inventado", course, models.SearchFilters{Turno: "VALOR-INEXISTENTE-XYZ"}, nil},
		{"regime sem acento", job, models.SearchFilters{RegimeContratacao: "temporario"}, []string{"vaga-temp"}},
		{"regime caixa baixa", job, models.SearchFilters{RegimeContratacao: "clt"}, []string{"vaga-clt"}},
		{"regime inventado", job, models.SearchFilters{RegimeContratacao: "VALOR-INEXISTENTE-XYZ"}, nil},
		{"modelo sem acento", job, models.SearchFilters{ModeloTrabalho: "hibrido"}, []string{"vaga-clt"}},
		{"modelo remoto", job, models.SearchFilters{ModeloTrabalho: "Remoto"}, []string{"vaga-pj"}},
		{"modelo inventado", job, models.SearchFilters{ModeloTrabalho: "VALOR-INEXISTENTE-XYZ"}, nil},
		{"pcd exclusivo", job, models.SearchFilters{PCD: boolPtr(true)}, []string{"vaga-clt"}},
		{"pcd não exclusivo", job, models.SearchFilters{PCD: boolPtr(false)},
			[]string{"vaga-pj", "vaga-sem-valor", "vaga-temp", "vaga-valor-texto"}},
		{"até 2 SM", job, models.SearchFilters{FaixaSalarial: models.FaixaSalarialAte2SM}, []string{"vaga-clt"}},
		{"2 a 4 SM", job, models.SearchFilters{FaixaSalarial: models.FaixaSalarial2a4SM}, []string{"vaga-temp"}},
		{"acima de 4 SM", job, models.SearchFilters{FaixaSalarial: models.FaixaSalarialAcima4SM}, []string{"vaga-pj"}},
		{"combinados", job, models.SearchFilters{RegimeContratacao: "CLT", ModeloTrabalho: "híbrido", PCD: boolPtr(true), FaixaSalarial: models.FaixaSalarialAte2SM},
			[]string{"vaga-clt"}},
		{"sem types, filtro de curso não restringe outros tipos", nil, models.SearchFilters{Turno: "noturno"},
			[]string{"curso-noite", "servico", "vaga-clt", "vaga-pj", "vaga-sem-valor", "vaga-temp", "vaga-valor-texto"}},
		{"sem types, filtro de vaga não restringe outros tipos", nil, models.SearchFilters{RegimeContratacao: "PJ"},
			[]string{"curso-manha", "curso-noite", "servico", "vaga-pj"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.filters
			f.Orgao = token
			for _, q := range []string{"", token} {
				results, total, err := searchRepo.Search(ctx, &models.SearchRequest{
					Q: q, Types: tc.types, Filters: f, Page: 1, PerPage: 50,
				})
				if err != nil {
					t.Fatalf("search (q=%q): %v", q, err)
				}
				var got []string
				for _, r := range results {
					got = append(got, strings.TrimPrefix(r.Item.ExternalID, token+"-"))
				}
				sort.Strings(got)
				if fmt.Sprint(got) != fmt.Sprint(tc.want) || total != len(tc.want) {
					t.Fatalf("q=%q: got %v (total %d), want %v", q, got, total, tc.want)
				}
			}
		})
	}
}
