package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

const (
	validFromStarted = "valid_from IS NULL OR valid_from <= NOW()"
	validUntilOpen   = "valid_until IS NULL OR valid_until > NOW()"
)

// Toda leitura de descoberta exige a vigência inteira: início (valid_from) e
// fim (valid_until). Antes do DEV-609 só valid_until era comparado.
func TestDiscoveryQueries_RequireValidityWindow(t *testing.T) {
	cases := map[string]struct {
		sql    string
		prefix string
	}{
		"detalhe público (GetPublicByID)":  {getPublicByIDSQL, ""},
		"candidatos (GetCandidates)":       {getCandidatesSQL, ""},
		"busca (searchEligibleWhere)":      {searchEligibleWhere(""), "ci."},
		"busca híbrida (hybridSearchCTEs)": {hybridSearchCTEs(searchEligibleWhere("")), "ci."},
		"busca HyDE (hydeSearchCTEs)":      {hydeSearchCTEs(searchEligibleWhere("")), "ci."},
		"filtros mantidos depois do WHERE": {searchEligibleWhere("AND ci.type = ANY($2)"), "ci."},
	}
	for name, tc := range cases {
		started := strings.ReplaceAll(validFromStarted, "valid_from", tc.prefix+"valid_from")
		open := strings.ReplaceAll(validUntilOpen, "valid_until", tc.prefix+"valid_until")
		if !strings.Contains(tc.sql, started) {
			t.Errorf("%s: sem o início da vigência %q:\n%s", name, started, tc.sql)
		}
		if !strings.Contains(tc.sql, open) {
			t.Errorf("%s: sem o fim da vigência %q:\n%s", name, open, tc.sql)
		}
	}
	if where := searchEligibleWhere("AND ci.type = ANY($2)"); !strings.HasSuffix(strings.TrimSpace(where), "AND ci.type = ANY($2)") {
		t.Errorf("searchEligibleWhere deve terminar nos filtros dinâmicos: %q", where)
	}
}

// Item cuja vigência ainda não começou não aparece no detalhe público, na
// busca nem nos candidatos de recomendação; item vigente (com valid_from no
// passado ou sem valid_from) continua aparecendo, e item vencido segue fora.
func TestDiscovery_HidesItemBeforeValidFrom(t *testing.T) {
	pool := testCatalogPool(t)
	itemRepo := NewCatalogItemRepository(pool)
	searchRepo := NewSearchRepository(pool)
	ctx := context.Background()

	token := fmt.Sprintf("vigencia%d", time.Now().UnixNano())
	now := time.Now().UTC()
	past := now.Add(-48 * time.Hour)
	future := now.Add(48 * time.Hour)
	expired := now.Add(-time.Hour)

	newItem := func(suffix string, from, until *time.Time) *models.CatalogItem {
		return &models.CatalogItem{
			ExternalID:     token + "-" + suffix,
			Source:         models.SourceSalesForce,
			Type:           models.TypeService,
			Title:          "Serviço " + token + " " + suffix,
			Status:         models.StatusActive,
			Tags:           []string{},
			Bairros:        []string{},
			TargetAudience: json.RawMessage(`{}`),
			SourceData:     json.RawMessage(`{}`),
			ValidFrom:      from,
			ValidUntil:     until,
		}
	}
	items := map[string]*models.CatalogItem{
		"futuro":  newItem("futuro", &future, nil),
		"vigente": newItem("vigente", &past, &future),
		"semfrom": newItem("semfrom", nil, nil),
		"vencido": newItem("vencido", &past, &expired),
	}
	externalIDs := make([]string, 0, len(items))
	batch := make([]*models.CatalogItem, 0, len(items))
	for _, it := range items {
		externalIDs = append(externalIDs, it.ExternalID)
		batch = append(batch, it)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM catalog_items WHERE external_id = ANY($1::text[])`, externalIDs)
	})
	if _, err := itemRepo.UpsertBatch(ctx, batch); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	visible := map[string]bool{"futuro": false, "vigente": true, "semfrom": true, "vencido": false}

	byExternalID := map[string]string{}
	for name, it := range items {
		stored, err := itemRepo.GetBySourceAndExternalID(ctx, it.Source, it.ExternalID)
		if err != nil {
			t.Fatalf("%s: leitura do item gravado: %v", name, err)
		}
		byExternalID[it.ExternalID] = name

		got, err := itemRepo.GetPublicByID(ctx, stored.ID)
		switch {
		case visible[name] && err != nil:
			t.Errorf("detalhe público de %s: erro %v, esperado o item", name, err)
		case !visible[name] && !errors.Is(err, pgx.ErrNoRows):
			t.Errorf("detalhe público de %s: (%v, %v), esperado pgx.ErrNoRows", name, got, err)
		}
	}

	seen := func(label string, found []*models.CatalogItem) {
		t.Helper()
		got := map[string]bool{}
		for _, it := range found {
			if name, ok := byExternalID[it.ExternalID]; ok {
				got[name] = true
			}
		}
		for name, want := range visible {
			if got[name] != want {
				t.Errorf("%s: %s presente=%v, esperado %v", label, name, got[name], want)
			}
		}
	}

	for _, q := range []string{token, ""} {
		results, _, err := searchRepo.Search(ctx, &models.SearchRequest{
			Q:       q,
			Types:   []models.ItemType{models.TypeService},
			Page:    1,
			PerPage: 100,
		})
		if err != nil {
			t.Fatalf("search q=%q: %v", q, err)
		}
		found := make([]*models.CatalogItem, 0, len(results))
		for _, r := range results {
			found = append(found, r.Item)
		}
		seen(fmt.Sprintf("busca q=%q", q), found)
	}

	candidates, err := itemRepo.GetCandidates(ctx, []models.ItemType{models.TypeService}, 500)
	if err != nil {
		t.Fatalf("candidatos: %v", err)
	}
	seen("candidatos de recomendação", candidates)
}
