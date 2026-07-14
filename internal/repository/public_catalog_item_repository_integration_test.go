package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestGetPublicByIDRequiresActiveCurrentItem(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	repository := NewCatalogItemRepository(databasePool)
	testContext, cancelTest := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelTest()
	fixturePrefix := "public-detail-" + uuid.NewString()
	cleanupCatalogRepositoryFixtures(t, databasePool, fixturePrefix)

	activeID := uuid.New()
	futureID := uuid.New()
	_, insertionError := databasePool.Exec(testContext, `
		INSERT INTO catalog_items (id, external_id, source, type, title, status, valid_from)
		VALUES
			($1, $2, 'typesense', 'service', 'Active item', 'active', NULL),
			($3, $4, 'typesense', 'service', 'Future item', 'active', NOW() + INTERVAL '1 day')
	`, activeID, fixturePrefix+"-active", futureID, fixturePrefix+"-future")
	if insertionError != nil {
		t.Fatalf("insert public detail fixtures: %v", insertionError)
	}

	if _, getError := repository.GetPublicByID(testContext, activeID); getError != nil {
		t.Fatalf("GetPublicByID(active) error = %v", getError)
	}
	if _, getError := repository.GetPublicByID(testContext, futureID); !errors.Is(getError, pgx.ErrNoRows) {
		t.Fatalf("GetPublicByID(future) error = %v, want pgx.ErrNoRows", getError)
	}
}

func TestPublicServiceBrowseUsesCanonicalAndHistoricalSlugs(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	catalogRepository := NewCatalogItemRepository(databasePool)
	testContext, cancelTest := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelTest()
	fixturePrefix := "public-service-" + uuid.NewString()
	cleanupCatalogRepositoryFixtures(t, databasePool, fixturePrefix)

	service := &models.CatalogItem{
		ExternalID:   fixturePrefix,
		Source:       models.SourceTypesense,
		Type:         models.TypeService,
		Title:        "Reparo de iluminação",
		ShortDesc:    "Solicite o reparo de uma luminária.",
		Organization: "RIO-LUZ",
		Status:       models.StatusActive,
		SourceData: json.RawMessage(`{
			"slug":"` + fixturePrefix + `-canonical",
			"slug_history":["` + fixturePrefix + `-historical"],
			"tema_geral":"Conservação",
			"sub_categoria":"Iluminação"
		}`),
	}
	if upsertError := catalogRepository.Upsert(testContext, service); upsertError != nil {
		t.Fatalf("upsert public service fixture: %v", upsertError)
	}

	canonicalResolution, canonicalError := catalogRepository.GetPublicServiceBySlug(testContext, fixturePrefix+"-canonical")
	if canonicalError != nil || canonicalResolution.CanonicalSlug != fixturePrefix+"-canonical" {
		t.Fatalf("canonical resolution = %#v error=%v", canonicalResolution, canonicalError)
	}
	historicalResolution, historicalError := catalogRepository.GetPublicServiceBySlug(testContext, fixturePrefix+"-historical")
	if historicalError != nil || historicalResolution.CanonicalSlug != fixturePrefix+"-canonical" {
		t.Fatalf("historical resolution = %#v error=%v", historicalResolution, historicalError)
	}

	categories, categoriesError := catalogRepository.ListPublicServiceCategories(testContext)
	if categoriesError != nil || !hasPublicServiceCategory(categories.Categories, "Conservação") {
		t.Fatalf("public service categories = %#v error=%v", categories, categoriesError)
	}
	subcategories, subcategoriesError := catalogRepository.ListPublicServiceSubcategories(testContext, "Conservação")
	if subcategoriesError != nil || !hasPublicServiceSubcategory(subcategories.Subcategories, "Iluminação") {
		t.Fatalf("public service subcategories = %#v error=%v", subcategories, subcategoriesError)
	}
	services, servicesError := catalogRepository.ListPublicServices(testContext, "Conservação", "Iluminação", 1, 20)
	if servicesError != nil || services.Total < 1 || len(services.Items) < 1 {
		t.Fatalf("public service list = %#v error=%v", services, servicesError)
	}
}

func hasPublicServiceCategory(categories []models.PublicServiceCategory, expectedName string) bool {
	for _, category := range categories {
		if category.Name == expectedName {
			return true
		}
	}
	return false
}

func hasPublicServiceSubcategory(subcategories []models.PublicServiceSubcategory, expectedName string) bool {
	for _, subcategory := range subcategories {
		if subcategory.Name == expectedName {
			return true
		}
	}
	return false
}
