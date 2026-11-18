package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestPropagateHierarchyLabels_RenamesThemeAndSubtheme(t *testing.T) {
	pool := testCatalogPool(t)
	carta := NewCartaRepository(pool)
	items := NewCatalogItemRepository(pool)
	ctx := context.Background()

	extID := fmt.Sprintf("test-propagate-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM catalog_items WHERE external_id = $1`, extID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM carta_home_featured WHERE slug = $1`, extID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM carta_subthemes WHERE slug = 'sub-propagate-test'`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM carta_themes WHERE slug = 'theme-propagate-test'`)
	})

	sourceData, _ := json.Marshal(map[string]any{
		"slug":                        extID,
		"name":                        "Serviço teste",
		"themeSlug":                   "theme-propagate-test",
		"themeName":                   "Nome Antigo",
		"subthemeSlug":                "sub-propagate-test",
		"subthemeName":                "Sub Antigo",
		"responsibleOrgUnitShortName": "ORG",
		"info": map[string]any{
			"isFree":  true,
			"summary": "s",
		},
	})

	item := &models.CatalogItem{
		ExternalID:   extID,
		Source:       models.SourceSalesForce,
		Type:         models.TypeService,
		Title:        "Serviço teste",
		Status:       models.StatusActive,
		Tags:         []string{"Nome Antigo", "Sub Antigo", "Gratuito", "ORG"},
		SourceData:   sourceData,
		ThemeSlug:    "theme-propagate-test",
		SubthemeSlug: "sub-propagate-test",
	}
	if err := items.Upsert(ctx, item); err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	if err := carta.UpsertTheme(ctx, &models.CartaTheme{Slug: "theme-propagate-test", Name: "Nome Novo"}); err != nil {
		t.Fatalf("upsert theme: %v", err)
	}
	if err := carta.UpsertSubtheme(ctx, &models.CartaSubtheme{
		Slug: "sub-propagate-test", ThemeSlug: "theme-propagate-test", Name: "Sub Novo",
	}); err != nil {
		t.Fatalf("upsert subtheme: %v", err)
	}
	if err := carta.ReplaceHomeFeatured(ctx, []models.CartaHomeFeatured{{
		Slug: extID, Position: 1, Name: "Serviço teste",
		ThemeSlug: "theme-propagate-test", ThemeName: "Nome Antigo",
		SubthemeSlug: "sub-propagate-test", SubthemeName: "Sub Antigo",
	}}); err != nil {
		t.Fatalf("home featured: %v", err)
	}

	n, err := carta.PropagateHierarchyLabels(ctx, []models.CartaHierarchyLabel{{
		ThemeSlug:    "theme-propagate-test",
		ThemeName:    "Nome Novo",
		SubthemeSlug: "sub-propagate-test",
		SubthemeName: "Sub Novo",
		ServiceSlugs: []string{extID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("updated=%d want 1", n)
	}

	var themeName, subName string
	var tags []string
	var themeSlug, subSlug string
	err = pool.QueryRow(ctx, `
		SELECT theme_slug, subtheme_slug, source_data->>'themeName', source_data->>'subthemeName', tags
		FROM catalog_items WHERE external_id = $1`, extID,
	).Scan(&themeSlug, &subSlug, &themeName, &subName, &tags)
	if err != nil {
		t.Fatal(err)
	}
	if themeSlug != "theme-propagate-test" || subSlug != "sub-propagate-test" {
		t.Fatalf("slugs theme=%q sub=%q", themeSlug, subSlug)
	}
	if themeName != "Nome Novo" || subName != "Sub Novo" {
		t.Fatalf("names theme=%q sub=%q", themeName, subName)
	}
	wantTags := []string{"Nome Novo", "Sub Novo", "Gratuito", "ORG"}
	if fmt.Sprint(tags) != fmt.Sprint(wantTags) {
		t.Fatalf("tags=%v want %v", tags, wantTags)
	}

	var featTheme, featSub string
	err = pool.QueryRow(ctx, `
		SELECT theme_name, subtheme_name FROM carta_home_featured WHERE slug = $1`, extID,
	).Scan(&featTheme, &featSub)
	if err != nil {
		t.Fatal(err)
	}
	if featTheme != "Nome Novo" || featSub != "Sub Novo" {
		t.Fatalf("featured names theme=%q sub=%q", featTheme, featSub)
	}

	// Idempotente: segunda chamada não altera linhas.
	n2, err := carta.PropagateHierarchyLabels(ctx, []models.CartaHierarchyLabel{{
		ThemeSlug: "theme-propagate-test", ThemeName: "Nome Novo",
		SubthemeSlug: "sub-propagate-test", SubthemeName: "Sub Novo",
		ServiceSlugs: []string{extID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("segunda propagação updated=%d want 0", n2)
	}
}

func TestPropagateHierarchyLabels_MovesServiceToNewThemeSlug(t *testing.T) {
	pool := testCatalogPool(t)
	carta := NewCartaRepository(pool)
	items := NewCatalogItemRepository(pool)
	ctx := context.Background()

	extID := fmt.Sprintf("test-move-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM catalog_items WHERE external_id = $1`, extID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM carta_subthemes WHERE slug IN ('old-sub','new-sub')`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM carta_themes WHERE slug IN ('old-theme','new-theme')`)
	})

	sourceData, _ := json.Marshal(map[string]any{
		"slug": extID, "name": "X",
		"themeSlug": "old-theme", "themeName": "Old",
		"subthemeSlug": "old-sub", "subthemeName": "Old Sub",
		"info": map[string]any{"isFree": false},
	})
	if err := items.Upsert(ctx, &models.CatalogItem{
		ExternalID: extID, Source: models.SourceSalesForce, Type: models.TypeService,
		Title: "X", Status: models.StatusActive, Tags: []string{"Old", "Old Sub"},
		SourceData: sourceData, ThemeSlug: "old-theme", SubthemeSlug: "old-sub",
	}); err != nil {
		t.Fatal(err)
	}
	_ = carta.UpsertTheme(ctx, &models.CartaTheme{Slug: "new-theme", Name: "New"})
	_ = carta.UpsertSubtheme(ctx, &models.CartaSubtheme{Slug: "new-sub", ThemeSlug: "new-theme", Name: "New Sub"})

	n, err := carta.PropagateHierarchyLabels(ctx, []models.CartaHierarchyLabel{{
		ThemeSlug: "new-theme", ThemeName: "New",
		SubthemeSlug: "new-sub", SubthemeName: "New Sub",
		ServiceSlugs: []string{extID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("updated=%d", n)
	}

	var themeSlug, subSlug, themeName, subName string
	if err := pool.QueryRow(ctx, `
		SELECT theme_slug, subtheme_slug, source_data->>'themeName', source_data->>'subthemeName'
		FROM catalog_items WHERE external_id = $1`, extID,
	).Scan(&themeSlug, &subSlug, &themeName, &subName); err != nil {
		t.Fatal(err)
	}
	if themeSlug != "new-theme" || subSlug != "new-sub" || themeName != "New" || subName != "New Sub" {
		t.Fatalf("got theme=%s/%s sub=%s/%s", themeSlug, themeName, subSlug, subName)
	}
}
