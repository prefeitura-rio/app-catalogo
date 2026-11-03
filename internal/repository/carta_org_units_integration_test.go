package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func requireSiciColumn(t *testing.T) *CatalogItemRepository {
	t.Helper()
	pool := testCatalogPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'catalog_items'
			  AND column_name = 'sici_code'
		)`).Scan(&exists); err != nil || !exists {
		t.Skip("catalog_items.sici_code ausente; rode migrations (000014) antes dos testes de integração")
	}
	return NewCatalogItemRepository(pool)
}

func seedCartaSiciFixtures(t *testing.T, catalog *CatalogItemRepository) (prefix string, carta *CartaRepository) {
	t.Helper()
	pool := catalog.db
	prefix = fmt.Sprintf("test-sici-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM catalog_items WHERE source = 'salesforce' AND external_id LIKE $1`, prefix+"%")
	})

	mk := func(slug, title, org, short, sici string) *models.CatalogItem {
		detail := map[string]any{
			"slug":                        slug,
			"name":                        title,
			"responsibleOrgUnit":          org,
			"responsibleOrgUnitShortName": short,
		}
		if sici == "" {
			detail["responsibleOrgUnitSiciCode"] = nil
		} else {
			detail["responsibleOrgUnitSiciCode"] = sici
		}
		raw, err := json.Marshal(detail)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return &models.CatalogItem{
			ExternalID:     slug,
			Source:         models.SourceSalesForce,
			Type:           models.TypeService,
			Title:          title,
			Organization:   org,
			Status:         models.StatusActive,
			Tags:           []string{},
			Bairros:        []string{},
			TargetAudience: json.RawMessage(`{}`),
			SourceData:     raw,
			SiciCode:       sici,
		}
	}

	items := []*models.CatalogItem{
		mk(prefix+"-reparo", "Reparo de luminária", "RIOLUZ Completo", "RIOLUZ", "1552"),
		mk(prefix+"-outro-rioluz", "Outro RIOLUZ", "RIOLUZ Completo", "RIOLUZ", "1552"),
		mk(prefix+"-fazenda", "IPTU", "SMFP Completo", "SMFP", "0001"),
		mk(prefix+"-sem-codigo", "Sem código", "Órgão Sem SICI", "SEM", ""),
	}
	ctx := context.Background()
	if _, err := catalog.UpsertBatch(ctx, items); err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}
	return prefix, NewCartaRepository(pool)
}

func TestCartaRepo_ListOrgUnitsAndFilterBySici(t *testing.T) {
	catalog := requireSiciColumn(t)
	prefix, carta := seedCartaSiciFixtures(t, catalog)
	ctx := context.Background()

	orgs, meta, err := carta.ListOrgUnits(ctx, 1, 100)
	if err != nil {
		t.Fatalf("ListOrgUnits: %v", err)
	}
	byCode := map[string]models.CartaOrgUnit{}
	for _, o := range orgs {
		if o.ResponsibleOrgUnitSiciCode == "1552" || o.ResponsibleOrgUnitSiciCode == "0001" {
			byCode[o.ResponsibleOrgUnitSiciCode] = o
		}
	}
	if meta.Total < 2 {
		t.Fatalf("expected at least 2 org units with fixtures, meta=%+v orgs=%d", meta, len(orgs))
	}
	rioluz, ok := byCode["1552"]
	if !ok {
		t.Fatalf("missing org 1552 in %+v", orgs)
	}
	if rioluz.PublishedServices < 2 {
		t.Fatalf("1552 publishedServices=%d want >=2", rioluz.PublishedServices)
	}
	if rioluz.ResponsibleOrgUnitShortName != "RIOLUZ" {
		t.Fatalf("short=%q", rioluz.ResponsibleOrgUnitShortName)
	}
	if _, ok := byCode["0001"]; !ok {
		t.Fatalf("missing org 0001")
	}
	for _, o := range orgs {
		if o.ResponsibleOrgUnitSiciCode == "" {
			t.Fatal("org-units must not include empty sici_code")
		}
	}

	raws, meta1552, err := carta.ListServicesBySiciCode(ctx, "1552", 1, 100)
	if err != nil {
		t.Fatalf("ListServicesBySiciCode: %v", err)
	}
	if meta1552.Total < 2 || len(raws) < 2 {
		t.Fatalf("1552 services total=%d len=%d", meta1552.Total, len(raws))
	}
	for _, raw := range raws {
		var detail map[string]any
		if err := json.Unmarshal(raw, &detail); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		slug, _ := detail["slug"].(string)
		if slug != "" && !(len(slug) >= len(prefix) && slug[:len(prefix)] == prefix) {
			// may include other env data; only assert our fixtures when present
			continue
		}
		code, _ := detail["responsibleOrgUnitSiciCode"].(string)
		if code != "1552" {
			t.Fatalf("slug=%s sici=%v want 1552", slug, detail["responsibleOrgUnitSiciCode"])
		}
	}

	// igualdade estrita: código parecido não entra
	rawsOther, metaOther, err := carta.ListServicesBySiciCode(ctx, "1553", 1, 100)
	if err != nil {
		t.Fatalf("ListServicesBySiciCode 1553: %v", err)
	}
	if metaOther.Total != 0 || len(rawsOther) != 0 {
		t.Fatalf("expected empty for unknown code, total=%d len=%d", metaOther.Total, len(rawsOther))
	}

	_, _, err = carta.ListServicesBySiciCode(ctx, "  ", 1, 100)
	if err == nil {
		t.Fatal("blank sici code must error")
	}

	foundNull := false
	found1552 := false
	for page := 1; page <= CartaMaxPage && (!foundNull || !found1552); page++ {
		all, allMeta, err := carta.ListAllServices(ctx, page, 100)
		if err != nil {
			t.Fatalf("ListAllServices page=%d: %v", page, err)
		}
		if page == 1 && allMeta.Total < 4 {
			t.Fatalf("ListAllServices total=%d", allMeta.Total)
		}
		if len(all) == 0 {
			break
		}
		for _, raw := range all {
			var detail map[string]any
			if err := json.Unmarshal(raw, &detail); err != nil {
				t.Fatalf("unmarshal all: %v", err)
			}
			slug, _ := detail["slug"].(string)
			switch slug {
			case prefix + "-sem-codigo":
				if detail["responsibleOrgUnitSiciCode"] != nil {
					t.Fatalf("null fixture got %#v", detail["responsibleOrgUnitSiciCode"])
				}
				foundNull = true
			case prefix + "-reparo":
				if detail["responsibleOrgUnitSiciCode"] != "1552" {
					t.Fatalf("reparo sici=%#v", detail["responsibleOrgUnitSiciCode"])
				}
				found1552 = true
			}
		}
		if page >= allMeta.TotalPages {
			break
		}
	}
	if !foundNull || !found1552 {
		t.Fatalf("ListAllServices missing fixtures null=%v 1552=%v", foundNull, found1552)
	}
}
