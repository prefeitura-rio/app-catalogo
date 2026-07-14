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

func TestPublicServiceIntelligenceUsesOneEligibleCatalogSnapshot(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	catalogRepository := NewCatalogItemRepository(databasePool)
	testContext, cancelTest := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelTest()
	fixturePrefix := "public-intelligence-" + uuid.NewString()
	cleanupCatalogRepositoryFixtures(t, databasePool, fixturePrefix)
	t.Cleanup(func() {
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		if _, cleanupError := databasePool.Exec(cleanupContext, `
			DELETE FROM catalog_item_journeys
			WHERE from_external_id LIKE $1 OR to_external_id LIKE $1
		`, fixturePrefix+"%"); cleanupError != nil {
			t.Errorf("clean up public intelligence journey fixture: %v", cleanupError)
		}
	})

	origin := &models.CatalogItem{
		ExternalID: fixturePrefix + "-origin", Source: models.SourceTypesense, Type: models.TypeService,
		Title: "Emissão de IPTU", ShortDesc: "Emita a segunda via do imposto predial.",
		Status: models.StatusActive, SourceData: json.RawMessage(`{
			"slug":"` + fixturePrefix + `-iptu","tema_geral":"Tributos","sub_categoria":"IPTU"
		}`),
	}
	target := &models.CatalogItem{
		ExternalID: fixturePrefix + "-target", Source: models.SourceTypesense, Type: models.TypeService,
		Title: "Certidão tributária", ShortDesc: "Solicite uma certidão negativa.",
		Status: models.StatusActive, SourceData: json.RawMessage(`{
			"slug":"` + fixturePrefix + `-certidao","tema_geral":"Tributos","sub_categoria":"Certidões"
		}`),
	}
	if _, upsertError := catalogRepository.UpsertBatch(testContext, []*models.CatalogItem{origin, target}); upsertError != nil {
		t.Fatalf("upsert public intelligence fixtures: %v", upsertError)
	}
	persistedOrigin, originError := catalogRepository.GetBySourceAndExternalID(testContext, origin.Source, origin.ExternalID)
	if originError != nil {
		t.Fatalf("read public intelligence origin fixture: %v", originError)
	}
	persistedTarget, targetError := catalogRepository.GetBySourceAndExternalID(testContext, target.Source, target.ExternalID)
	if targetError != nil {
		t.Fatalf("read public intelligence target fixture: %v", targetError)
	}
	if _, insertionError := databasePool.Exec(testContext, `
		INSERT INTO catalog_item_journeys (
			from_external_id, from_source, to_external_id, to_source,
			journey_type, weight, reason, theme
		) VALUES ($1, 'typesense', $2, 'typesense', 'sequence', 1.0, 'obter certidão negativa', 'jornada tributária')
	`, origin.ExternalID, target.ExternalID); insertionError != nil {
		t.Fatalf("insert public intelligence journey fixture: %v", insertionError)
	}

	suggestions, suggestionError := catalogRepository.SuggestPublicServices(testContext, "emissao", models.MaximumPublicSuggestions)
	if suggestionError != nil || !hasPublicServiceSuggestion(suggestions, fixturePrefix+"-iptu") {
		t.Fatalf("public suggestions = %#v error=%v", suggestions, suggestionError)
	}
	wildcardSuggestions, wildcardSuggestionError := catalogRepository.SuggestPublicServices(
		testContext, "_missa_", models.MaximumPublicSuggestions,
	)
	if wildcardSuggestionError != nil || hasPublicServiceSuggestion(wildcardSuggestions, fixturePrefix+"-iptu") {
		t.Fatalf("literal wildcard suggestions = %#v error=%v", wildcardSuggestions, wildcardSuggestionError)
	}
	relations, relationError := catalogRepository.GetPublicServiceRelations(
		testContext, fixturePrefix+"-iptu", models.MaximumPublicServiceRelations,
	)
	if relationError != nil || relations.Theme != "jornada tributária" ||
		!hasPublicServiceRelation(relations.Journey, fixturePrefix+"-certidao") ||
		!hasPublicServiceRelation(relations.Recommendations, fixturePrefix+"-certidao") {
		t.Fatalf("public relations = %#v error=%v", relations, relationError)
	}

	catalogSnapshot, snapshotError := catalogRepository.CatalogSnapshot(testContext)
	if snapshotError != nil {
		t.Fatalf("read catalog snapshot: %v", snapshotError)
	}
	summaryCandidates, candidateError := catalogRepository.GetSearchSummaryCandidates(
		testContext, catalogSnapshot.Revision, []uuid.UUID{persistedTarget.ID, persistedOrigin.ID},
	)
	if candidateError != nil || len(summaryCandidates.Items) != 2 ||
		summaryCandidates.Items[0].ID != persistedTarget.ID || summaryCandidates.Items[1].ID != persistedOrigin.ID {
		t.Fatalf("summary candidates = %#v error=%v", summaryCandidates, candidateError)
	}
	if _, staleError := catalogRepository.GetSearchSummaryCandidates(
		testContext, "catalog-v2:stale", []uuid.UUID{persistedOrigin.ID},
	); !errors.Is(staleError, models.ErrCatalogRevisionMismatch) {
		t.Fatalf("stale summary candidate error = %v", staleError)
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

func hasPublicServiceSuggestion(suggestions []models.PublicServiceSuggestion, expectedSlug string) bool {
	for _, suggestion := range suggestions {
		if suggestion.Slug == expectedSlug {
			return true
		}
	}
	return false
}

func hasPublicServiceRelation(relations []models.PublicServiceRelation, expectedSlug string) bool {
	for _, relation := range relations {
		if relation.Slug == expectedSlug {
			return true
		}
	}
	return false
}
