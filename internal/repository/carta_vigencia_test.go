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

// Toda leitura pública da Carta e as contagens da hierarquia exigem a vigência
// inteira (início e fim), como o detalhe do catálogo e a busca (DEV-622).
func TestCartaQueries_RequireValidityWindow(t *testing.T) {
	cases := map[string]struct {
		sql    string
		prefix string
	}{
		"contagem por subtema (RefreshHierarchyCounts)":       {refreshSubthemeCountsSQL, "ci."},
		"contagem por tema (RefreshHierarchyCounts)":          {refreshThemeCountsSQL, "ci."},
		"total de ListServicesBySubtheme":                     {countServicesBySubthemeSQL, ""},
		"lista de ListServicesBySubtheme":                     {listServicesBySubthemeSQL, ""},
		"lista de ListOrgUnits":                               {listOrgUnitsSQL, ""},
		"total de ListOrgUnits":                               {countOrgUnitsSQL, ""},
		"total de ListServicesBySiciCode":                     {countServicesBySiciCodeSQL, ""},
		"lista de ListServicesBySiciCode":                     {listServicesBySiciCodeSQL, ""},
		"total de ListAllServices":                            {countAllServicesSQL, ""},
		"lista de ListAllServices":                            {listAllServicesSQL, ""},
		"detalhe (GetServiceDetail)":                          {getServiceDetailSQL, ""},
		"destaques da home (ListHomeFeatured)":                {listHomeFeaturedSQL, "ci."},
		"condição publicada sem alias (cartaPublishedSQL)":    {cartaPublishedSQL(""), ""},
		"condição publicada com alias (cartaPublishedSQL ci)": {cartaPublishedSQL("ci."), "ci."},
	}
	for name, tc := range cases {
		started := strings.ReplaceAll(validFromStarted, "valid_from", tc.prefix+"valid_from")
		open := strings.ReplaceAll(validUntilOpen, "valid_until", tc.prefix+"valid_until")
		for _, want := range []string{
			started,
			open,
			tc.prefix + "source = 'salesforce'",
			tc.prefix + "deleted_at IS NULL",
			tc.prefix + "status = 'active'",
		} {
			if !strings.Contains(tc.sql, want) {
				t.Errorf("%s: sem %q:\n%s", name, want, tc.sql)
			}
		}
	}
}

// Serviço da Carta com início futuro ou vencido não aparece nas listas, no
// detalhe nem nas contagens de temas, subtemas e órgãos; serviço vigente (com
// valid_from no passado ou sem valid_from) continua aparecendo.
func TestCartaRepo_HidesServicesOutOfValidity(t *testing.T) {
	catalog := requireSiciColumn(t)
	pool := catalog.db
	carta := NewCartaRepository(pool)
	ctx := context.Background()

	token := fmt.Sprintf("test-vig-%d", time.Now().UnixNano())
	themeSlug := token + "-tema"
	subthemeSlug := token + "-subtema"
	sici := fmt.Sprintf("V%d", time.Now().UnixNano()%1_000_000_000)

	now := time.Now().UTC()
	past := now.Add(-48 * time.Hour)
	future := now.Add(48 * time.Hour)
	expired := now.Add(-time.Hour)

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM catalog_items WHERE source = 'salesforce' AND external_id LIKE $1`, token+"%")
		_, _ = pool.Exec(bg, `DELETE FROM carta_subthemes WHERE slug = $1`, subthemeSlug)
		_, _ = pool.Exec(bg, `DELETE FROM carta_themes WHERE slug = $1`, themeSlug)
	})

	if err := carta.UpsertTheme(ctx, &models.CartaTheme{Slug: themeSlug, Name: "Tema " + token}); err != nil {
		t.Fatalf("UpsertTheme: %v", err)
	}
	if err := carta.UpsertSubtheme(ctx, &models.CartaSubtheme{Slug: subthemeSlug, ThemeSlug: themeSlug, Name: "Subtema " + token}); err != nil {
		t.Fatalf("UpsertSubtheme: %v", err)
	}

	newItem := func(suffix string, from, until *time.Time) *models.CatalogItem {
		slug := token + "-" + suffix
		raw, err := json.Marshal(map[string]any{
			"slug":                       slug,
			"name":                       "Serviço " + suffix,
			"responsibleOrgUnitSiciCode": sici,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return &models.CatalogItem{
			ExternalID:     slug,
			Source:         models.SourceSalesForce,
			Type:           models.TypeService,
			Title:          "Serviço " + token + " " + suffix,
			Organization:   "Órgão " + token,
			Status:         models.StatusActive,
			Tags:           []string{},
			Bairros:        []string{},
			TargetAudience: json.RawMessage(`{}`),
			SourceData:     raw,
			ThemeSlug:      themeSlug,
			SubthemeSlug:   subthemeSlug,
			SiciCode:       sici,
			ValidFrom:      from,
			ValidUntil:     until,
		}
	}
	items := []*models.CatalogItem{
		newItem("futuro", &future, nil),
		newItem("vigente", &past, &future),
		newItem("semfrom", nil, nil),
		newItem("vencido", &past, &expired),
	}
	if _, err := catalog.UpsertBatch(ctx, items); err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}

	visible := map[string]bool{
		token + "-futuro":  false,
		token + "-vigente": true,
		token + "-semfrom": true,
		token + "-vencido": false,
	}
	const wantCount = 2

	check := func(label string, got map[string]bool) {
		t.Helper()
		for slug, want := range visible {
			if got[slug] != want {
				t.Errorf("%s: %s presente=%v, esperado %v", label, slug, got[slug], want)
			}
		}
	}
	slugsOf := func(label string, raws []json.RawMessage) map[string]bool {
		t.Helper()
		got := map[string]bool{}
		for _, raw := range raws {
			var detail map[string]any
			if err := json.Unmarshal(raw, &detail); err != nil {
				t.Fatalf("%s: unmarshal: %v", label, err)
			}
			if slug, _ := detail["slug"].(string); strings.HasPrefix(slug, token) {
				got[slug] = true
			}
		}
		return got
	}

	// /subthemes/:slug/services
	summaries, meta, err := carta.ListServicesBySubtheme(ctx, subthemeSlug, 1, 100)
	if err != nil {
		t.Fatalf("ListServicesBySubtheme: %v", err)
	}
	gotSub := map[string]bool{}
	for _, s := range summaries {
		gotSub[s.Slug] = true
	}
	check("ListServicesBySubtheme", gotSub)
	if meta.Total != wantCount || len(summaries) != wantCount {
		t.Errorf("ListServicesBySubtheme: total=%d len=%d, esperado %d", meta.Total, len(summaries), wantCount)
	}

	// /org-units/:siciCode/services
	raws, meta, err := carta.ListServicesBySiciCode(ctx, sici, 1, 100)
	if err != nil {
		t.Fatalf("ListServicesBySiciCode: %v", err)
	}
	check("ListServicesBySiciCode", slugsOf("ListServicesBySiciCode", raws))
	if meta.Total != wantCount || len(raws) != wantCount {
		t.Errorf("ListServicesBySiciCode: total=%d len=%d, esperado %d", meta.Total, len(raws), wantCount)
	}

	// /org-units: o órgão conta só os serviços vigentes.
	foundOrg := false
	for page := 1; page <= CartaMaxPage && !foundOrg; page++ {
		orgs, orgMeta, err := carta.ListOrgUnits(ctx, page, 100)
		if err != nil {
			t.Fatalf("ListOrgUnits page=%d: %v", page, err)
		}
		for _, o := range orgs {
			if o.ResponsibleOrgUnitSiciCode == sici {
				foundOrg = true
				if o.PublishedServices != wantCount {
					t.Errorf("ListOrgUnits: publishedServices=%d, esperado %d", o.PublishedServices, wantCount)
				}
			}
		}
		if len(orgs) == 0 || page >= orgMeta.TotalPages {
			break
		}
	}
	if !foundOrg {
		t.Errorf("ListOrgUnits: órgão %s ausente", sici)
	}

	// /services
	gotAll := map[string]bool{}
	for page := 1; page <= CartaMaxPage; page++ {
		all, allMeta, err := carta.ListAllServices(ctx, page, 100)
		if err != nil {
			t.Fatalf("ListAllServices page=%d: %v", page, err)
		}
		for slug := range slugsOf("ListAllServices", all) {
			gotAll[slug] = true
		}
		if len(all) == 0 || page >= allMeta.TotalPages {
			break
		}
	}
	check("ListAllServices", gotAll)

	// /services/:slug
	for slug, want := range visible {
		data, redirect, err := carta.GetServiceDetail(ctx, slug)
		if err != nil {
			t.Fatalf("GetServiceDetail %s: %v", slug, err)
		}
		if redirect != "" {
			t.Errorf("GetServiceDetail %s: redirect inesperado para %q", slug, redirect)
		}
		if got := data != nil; got != want {
			t.Errorf("GetServiceDetail %s: encontrado=%v, esperado %v", slug, got, want)
		}
	}

	// Contagens de tema e subtema coerentes com as listas.
	if err := carta.RefreshHierarchyCounts(ctx); err != nil {
		t.Fatalf("RefreshHierarchyCounts: %v", err)
	}
	subthemes, _, err := carta.ListSubthemes(ctx, themeSlug, 1, 100)
	if err != nil {
		t.Fatalf("ListSubthemes: %v", err)
	}
	if len(subthemes) != 1 || subthemes[0].PublishedServices != wantCount {
		t.Errorf("ListSubthemes: %+v, esperado um subtema com publishedServices=%d", subthemes, wantCount)
	}
	var themeCount, themeSubthemes int
	if err := pool.QueryRow(ctx,
		`SELECT published_services, subthemes_count FROM carta_themes WHERE slug = $1`, themeSlug,
	).Scan(&themeCount, &themeSubthemes); err != nil {
		t.Fatalf("leitura do tema: %v", err)
	}
	if themeCount != wantCount || themeSubthemes != 1 {
		t.Errorf("tema: publishedServices=%d subthemesCount=%d, esperado %d e 1", themeCount, themeSubthemes, wantCount)
	}
}

// Os destaques da home seguem a mesma condição das listas: aparecem só os
// destaques cujo serviço está publicado e vigente, pelo slug ou por um alias
// que resolve para ele; destaque de serviço com início futuro, vencido ou
// ausente do catálogo some da home (DEV-628).
func TestCartaRepo_HomeFeaturedHidesServicesOutOfValidity(t *testing.T) {
	pool := testCatalogPool(t)
	catalog := NewCatalogItemRepository(pool)
	carta := NewCartaRepository(pool)
	ctx := context.Background()

	token := fmt.Sprintf("test-home-vig-%d", time.Now().UnixNano())
	now := time.Now().UTC()
	past := now.Add(-48 * time.Hour)
	future := now.Add(48 * time.Hour)
	expired := now.Add(-time.Hour)

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM carta_home_featured WHERE slug LIKE $1`, token+"%")
		_, _ = pool.Exec(bg, `DELETE FROM carta_service_slug_aliases WHERE old_slug LIKE $1`, token+"%")
		_, _ = pool.Exec(bg, `DELETE FROM catalog_items WHERE source = 'salesforce' AND external_id LIKE $1`, token+"%")
	})

	newItem := func(suffix string, from, until *time.Time) *models.CatalogItem {
		slug := token + "-" + suffix
		raw, err := json.Marshal(map[string]any{"slug": slug, "name": "Serviço " + suffix})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return &models.CatalogItem{
			ExternalID:     slug,
			Source:         models.SourceSalesForce,
			Type:           models.TypeService,
			Title:          "Serviço " + token + " " + suffix,
			Status:         models.StatusActive,
			Tags:           []string{},
			Bairros:        []string{},
			TargetAudience: json.RawMessage(`{}`),
			SourceData:     raw,
			ValidFrom:      from,
			ValidUntil:     until,
		}
	}
	if _, err := catalog.UpsertBatch(ctx, []*models.CatalogItem{
		newItem("vigente", &past, &future),
		newItem("semfrom", nil, nil),
		newItem("futuro", &future, nil),
		newItem("vencido", &past, &expired),
	}); err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}
	if err := carta.UpsertSlugAlias(ctx, token+"-antigo", token+"-vigente"); err != nil {
		t.Fatalf("UpsertSlugAlias vigente: %v", err)
	}
	if err := carta.UpsertSlugAlias(ctx, token+"-antigo-vencido", token+"-vencido"); err != nil {
		t.Fatalf("UpsertSlugAlias vencido: %v", err)
	}

	suffixes := []string{"futuro", "vigente", "vencido", "ausente", "antigo", "semfrom", "antigo-vencido"}
	featured := make([]models.CartaHomeFeatured, 0, len(suffixes))
	for i, suffix := range suffixes {
		featured = append(featured, models.CartaHomeFeatured{
			Slug:     token + "-" + suffix,
			Position: i + 1,
			Name:     "Destaque " + suffix,
		})
	}
	if err := carta.ReplaceHomeFeatured(ctx, featured); err != nil {
		t.Fatalf("ReplaceHomeFeatured: %v", err)
	}

	got, err := carta.ListHomeFeatured(ctx)
	if err != nil {
		t.Fatalf("ListHomeFeatured: %v", err)
	}
	slugs := make([]string, 0, len(got))
	for _, f := range got {
		if strings.HasPrefix(f.Slug, token) {
			slugs = append(slugs, strings.TrimPrefix(f.Slug, token+"-"))
		}
	}
	want := []string{"vigente", "antigo", "semfrom"}
	if strings.Join(slugs, ",") != strings.Join(want, ",") {
		t.Fatalf("ListHomeFeatured: destaques=%v, esperado %v (em ordem de position)", slugs, want)
	}
}
