package repository

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/query"
)

func TestSearchRankedFusesIndependentPoolsAndRejectsIncompatibleVectors(t *testing.T) {
	databaseURL := os.Getenv("APP_CATALOGO_SEARCH_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("APP_CATALOGO_SEARCH_TEST_DATABASE_URL is not configured")
	}

	searchContext, cancelSearch := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSearch()
	databaseConfiguration, parseError := pgxpool.ParseConfig(databaseURL)
	if parseError != nil {
		t.Fatalf("invalid search test database configuration")
	}
	if !strings.Contains(strings.ToLower(databaseConfiguration.ConnConfig.Database), "test") {
		t.Fatalf("search integration tests require a database whose name contains test")
	}
	databasePool, connectionError := pgxpool.NewWithConfig(searchContext, databaseConfiguration)
	if connectionError != nil {
		t.Fatalf("create database pool: %v", connectionError)
	}
	defer databasePool.Close()

	fixturePrefix := "search-integration-" + uuid.NewString()
	defer func() {
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		if _, cleanupError := databasePool.Exec(cleanupContext, "DELETE FROM catalog_items WHERE external_id LIKE $1", fixturePrefix+"%"); cleanupError != nil {
			t.Errorf("cleanup search fixtures: %v", cleanupError)
		}
	}()

	embedding := make([]float32, 1536)
	embedding[0] = 1
	embeddingLiteral := clients.VectorLiteral(embedding)
	futureValidityStart := time.Now().Add(24 * time.Hour)
	fixtureRows := []struct {
		suffix          string
		title           string
		description     string
		embedding       string
		documentVersion string
		validFrom       *time.Time
	}{
		{suffix: "-exact", title: "Cartão SUS", description: "Solicitação municipal de saúde"},
		{suffix: "-future", title: "Cartão SUS", description: "Serviço ainda indisponível", validFrom: &futureValidityStart},
		{suffix: "-full-text", title: "Emissão de documento de saúde", description: "Cadastre o cartão do Sistema Único de Saúde"},
		{suffix: "-typo", title: "Cartao do SUZ", description: "Atendimento ao cidadão"},
		{suffix: "-semantic", title: "Orientação tributária", description: "Conteúdo semanticamente recuperado", embedding: embeddingLiteral, documentVersion: "catalog-item-v1"},
		{suffix: "-incompatible", title: "Licenciamento ambiental", description: "Vetor de documento obsoleto", embedding: embeddingLiteral, documentVersion: "catalog-item-obsolete"},
	}
	for _, fixtureRow := range fixtureRows {
		_, insertionError := databasePool.Exec(searchContext, `
			INSERT INTO catalog_items (
				external_id, source, type, title, description, short_desc,
				organization, status, tags, source_data,
				embedding, embedding_model, embedding_model_version,
				embedding_dimensions, embedding_task_type,
				embedding_document_version, embedding_source_hash,
				embedding_generated_at, valid_from
			) VALUES (
				$1, 'typesense', 'service', $2, $3, $3,
				'Prefeitura do Rio', 'active', '{}', '{}',
				NULLIF($4, '')::vector,
				CASE WHEN $4 = '' THEN NULL ELSE 'gemini-embedding-001' END,
				CASE WHEN $4 = '' THEN NULL ELSE '001' END,
				CASE WHEN $4 = '' THEN NULL ELSE 1536 END,
				CASE WHEN $4 = '' THEN NULL ELSE 'RETRIEVAL_DOCUMENT' END,
				NULLIF($5, ''),
				CASE WHEN $4 = '' THEN NULL ELSE repeat('a', 64) END,
				CASE WHEN $4 = '' THEN NULL ELSE NOW() END,
				$6
			)
		`, fixturePrefix+fixtureRow.suffix, fixtureRow.title, fixtureRow.description, fixtureRow.embedding, fixtureRow.documentVersion, fixtureRow.validFrom)
		if insertionError != nil {
			t.Fatalf("insert %s fixture: %v", fixtureRow.suffix, insertionError)
		}
	}
	canonicalAliasID := fixturePrefix + "-split-signal-entity"
	aliasCreatedAt := time.Now().UTC().Add(-2 * time.Hour)
	if _, insertionError := databasePool.Exec(searchContext, `
		INSERT INTO catalog_items (
			external_id, source, type, title, description, short_desc,
			organization, status, tags, source_data, created_at, updated_at
		) VALUES
			($1, 'typesense', 'service', 'Alias legado', 'cartao sus atendimento legado',
				'cartao sus atendimento legado', 'Prefeitura do Rio', 'active', '{}',
				jsonb_build_object('canonical_id', $3::text), $5, $5),
			($2, 'salesforce', 'service', 'Representante atual', 'Conteudo atualizado sem o termo consultado',
				'Conteudo atualizado', 'Prefeitura do Rio', 'active', '{}',
				jsonb_build_object('canonical_id', $3::text), $4, $4)
	`,
		fixturePrefix+"-split-signal-old",
		fixturePrefix+"-split-signal-new",
		canonicalAliasID,
		aliasCreatedAt.Add(time.Hour),
		aliasCreatedAt,
	); insertionError != nil {
		t.Fatalf("insert split-signal canonical aliases: %v", insertionError)
	}
	var insertedFixtureCount int
	if countError := databasePool.QueryRow(
		searchContext,
		"SELECT COUNT(*) FROM catalog_items WHERE external_id LIKE $1",
		fixturePrefix+"%",
	).Scan(&insertedFixtureCount); countError != nil {
		t.Fatalf("count inserted fixtures: %v", countError)
	}
	if insertedFixtureCount != len(fixtureRows)+2 {
		t.Fatalf("inserted fixture count = %d, want %d", insertedFixtureCount, len(fixtureRows)+2)
	}
	var exactFixtureCount int
	if countError := databasePool.QueryRow(searchContext, `
		SELECT COUNT(*)
		FROM catalog_items
		WHERE external_id LIKE $1
		  AND status = 'active'
		  AND deleted_at IS NULL
		  AND (valid_from IS NULL OR valid_from <= NOW())
		  AND immutable_unaccent(lower(title)) = immutable_unaccent(lower('cartao sus'))
	`, fixturePrefix+"%").Scan(&exactFixtureCount); countError != nil {
		t.Fatalf("count exact fixtures: %v", countError)
	}
	if exactFixtureCount != 1 {
		t.Fatalf("exact fixture count = %d, want 1", exactFixtureCount)
	}
	var typedFixtureCount int
	if countError := databasePool.QueryRow(searchContext, `
		SELECT COUNT(*)
		FROM catalog_items
		WHERE external_id LIKE $1
		  AND type = ANY($2::item_type[])
	`, fixturePrefix+"%", []string{"service"}).Scan(&typedFixtureCount); countError != nil {
		t.Fatalf("count typed fixtures: %v", countError)
	}
	if typedFixtureCount != len(fixtureRows)+2 {
		t.Fatalf("typed fixture count = %d, want %d", typedFixtureCount, len(fixtureRows)+2)
	}

	searchRequest := &models.SearchRequest{
		Q:         "cartao sus",
		ExpandedQ: query.Expand("cartao sus"),
		Types:     []models.ItemType{models.TypeService},
		Page:      1,
		PerPage:   10,
	}
	searchOptions := RankedSearchOptions{
		QueryEmbedding:           embeddingLiteral,
		EmbeddingModel:           "gemini-embedding-001",
		EmbeddingModelVersion:    "001",
		EmbeddingDimensions:      1536,
		EmbeddingTaskType:        "RETRIEVAL_DOCUMENT",
		EmbeddingDocumentVersion: "catalog-item-v1",
		CandidatePoolSize:        DefaultCandidatePoolSize,
		Weights:                  DefaultRetrievalWeights(),
	}
	queryStatement, queryArguments := buildRankedSearchQuery(searchRequest, searchOptions)
	finalSelectMarker := "\n\t\tSELECT\n\t\t\tci.id, ci.external_id"
	finalSelectIndex := strings.Index(queryStatement, finalSelectMarker)
	if finalSelectIndex < 0 {
		t.Fatalf("ranked query final SELECT marker was not found")
	}
	diagnosticStatement := queryStatement[:finalSelectIndex] + `
		SELECT
			(SELECT COUNT(*) FROM exact_candidates),
			(SELECT COUNT(*) FROM full_text_candidates),
			(SELECT COUNT(*) FROM trigram_candidates),
			(SELECT COUNT(*) FROM semantic_candidates),
			(SELECT COUNT(*) FROM retrieval_signals),
			(SELECT COUNT(*) FROM retrieval_signals WHERE contribution > 0),
			(SELECT COUNT(*) FROM fused_candidates)
	`
	var exactCandidateCount int
	var fullTextCandidateCount int
	var trigramCandidateCount int
	var semanticCandidateCount int
	var retrievalSignalCount int
	var positiveRetrievalSignalCount int
	var fusedCandidateCount int
	if diagnosticError := databasePool.QueryRow(searchContext, diagnosticStatement, queryArguments...).Scan(
		&exactCandidateCount,
		&fullTextCandidateCount,
		&trigramCandidateCount,
		&semanticCandidateCount,
		&retrievalSignalCount,
		&positiveRetrievalSignalCount,
		&fusedCandidateCount,
	); diagnosticError != nil {
		t.Fatalf("diagnose ranked query: %v", diagnosticError)
	}
	if exactCandidateCount < 1 || fullTextCandidateCount < 1 || trigramCandidateCount < 1 || semanticCandidateCount < 1 {
		t.Fatalf(
			"independent pools were not populated: exact=%d full_text=%d trigram=%d semantic=%d",
			exactCandidateCount,
			fullTextCandidateCount,
			trigramCandidateCount,
			semanticCandidateCount,
		)
	}
	if positiveRetrievalSignalCount != retrievalSignalCount || fusedCandidateCount < 1 {
		t.Fatalf(
			"RRF contributions were not positive and fused: signals=%d positive=%d fused=%d",
			retrievalSignalCount,
			positiveRetrievalSignalCount,
			fusedCandidateCount,
		)
	}
	searchRepository := NewSearchRepository(databasePool)
	searchResults, totalCandidates, searchError := searchRepository.SearchRanked(
		searchContext,
		searchRequest,
		searchOptions,
	)
	if searchError != nil {
		t.Fatalf("SearchRanked returned an error: %v", searchError)
	}
	if totalCandidates != len(searchResults) {
		t.Fatalf("total candidates = %d, result count = %d", totalCandidates, len(searchResults))
	}
	if len(searchResults) < 3 {
		t.Fatalf("candidate union returned %d results, want independent lexical and semantic pools", len(searchResults))
	}
	if searchResults[0].Item.ExternalID != fixturePrefix+"-exact" {
		t.Fatalf("first result = %q, want exact title match", searchResults[0].Item.ExternalID)
	}

	foundCompatibleSemanticItem := false
	foundCurrentSplitSignalRepresentative := false
	for _, searchResult := range searchResults {
		if searchResult.Item.ExternalID == fixturePrefix+"-semantic" {
			foundCompatibleSemanticItem = true
		}
		if strings.HasSuffix(searchResult.Item.ExternalID, "-incompatible") {
			t.Fatalf("obsolete document-version vector entered semantic candidates")
		}
		if strings.HasSuffix(searchResult.Item.ExternalID, "-future") {
			t.Fatalf("future-dated catalog item entered search candidates")
		}
		if strings.HasSuffix(searchResult.Item.ExternalID, "-split-signal-old") {
			t.Fatalf("retrieval alias was returned instead of the newest canonical representative")
		}
		if strings.HasSuffix(searchResult.Item.ExternalID, "-split-signal-new") {
			foundCurrentSplitSignalRepresentative = true
		}
	}
	if !foundCompatibleSemanticItem {
		t.Fatalf("compatible semantic vector was not retrieved")
	}
	if !foundCurrentSplitSignalRepresentative {
		t.Fatalf("entity matched by an older alias did not return its newest canonical representative")
	}

	itemRepository := NewCatalogItemRepository(databasePool)
	sourceUpdatedAt := time.Now().UTC().Truncate(time.Microsecond)
	upsertItem := &models.CatalogItem{
		ExternalID:      fixturePrefix + "-upsert-visible-fields",
		Source:          models.SourceTypesense,
		Type:            models.TypeService,
		Title:           "Atualização de campos de busca",
		URL:             "https://example.test/old",
		ImageURL:        "https://example.test/old.png",
		TargetAudience:  []byte(`{"pcd":false}`),
		Bairros:         []string{"Centro"},
		Modalidade:      "presencial",
		Status:          models.StatusActive,
		Tags:            []string{"serviço"},
		SourceData:      []byte(`{"slug":"upsert-visible-fields"}`),
		SourceUpdatedAt: &sourceUpdatedAt,
	}
	insertedCount, insertionError := itemRepository.UpsertBatch(searchContext, []*models.CatalogItem{upsertItem})
	if insertionError != nil || insertedCount != 1 {
		t.Fatalf("insert visible-field fixture: count=%d error=%v", insertedCount, insertionError)
	}

	updatedSourceTime := sourceUpdatedAt.Add(time.Minute)
	upsertItem.URL = "https://example.test/new"
	upsertItem.ImageURL = "https://example.test/new.png"
	upsertItem.TargetAudience = []byte(`{"pcd":true}`)
	upsertItem.Bairros = []string{"Tijuca"}
	upsertItem.Modalidade = "digital"
	upsertItem.SourceUpdatedAt = &updatedSourceTime
	updatedCount, updateError := itemRepository.UpsertBatch(searchContext, []*models.CatalogItem{upsertItem})
	if updateError != nil || updatedCount != 1 {
		t.Fatalf("update search-visible fields: count=%d error=%v", updatedCount, updateError)
	}

	var storedURL string
	var storedImageURL string
	var storedPCD string
	var storedBairros []string
	var storedModality string
	var storedSourceUpdatedAt time.Time
	if queryError := databasePool.QueryRow(searchContext, `
		SELECT url, image_url, target_audience->>'pcd', bairros, modalidade, source_updated_at
		FROM catalog_items
		WHERE source = $1 AND external_id = $2
	`, models.SourceTypesense, upsertItem.ExternalID).Scan(
		&storedURL,
		&storedImageURL,
		&storedPCD,
		&storedBairros,
		&storedModality,
		&storedSourceUpdatedAt,
	); queryError != nil {
		t.Fatalf("read updated search-visible fields: %v", queryError)
	}
	if storedURL != upsertItem.URL || storedImageURL != upsertItem.ImageURL || storedPCD != "true" ||
		len(storedBairros) != 1 || storedBairros[0] != "Tijuca" || storedModality != "digital" ||
		!storedSourceUpdatedAt.Equal(updatedSourceTime) {
		t.Fatalf(
			"stored search-visible fields are stale: url=%q image=%q pcd=%q bairros=%v modalidade=%q source_updated_at=%s",
			storedURL,
			storedImageURL,
			storedPCD,
			storedBairros,
			storedModality,
			storedSourceUpdatedAt,
		)
	}
	unchangedCount, unchangedError := itemRepository.UpsertBatch(searchContext, []*models.CatalogItem{upsertItem})
	if unchangedError != nil || unchangedCount != 0 {
		t.Fatalf("identical upsert should be a no-op: count=%d error=%v", unchangedCount, unchangedError)
	}
}

func TestBrowseSnapshotDeduplicatesBeforePaginationAndFacets(t *testing.T) {
	databaseURL := os.Getenv("APP_CATALOGO_SEARCH_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("APP_CATALOGO_SEARCH_TEST_DATABASE_URL is not configured")
	}

	searchContext, cancelSearch := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSearch()
	databaseConfiguration, parseError := pgxpool.ParseConfig(databaseURL)
	if parseError != nil {
		t.Fatalf("invalid search test database configuration")
	}
	if !strings.Contains(strings.ToLower(databaseConfiguration.ConnConfig.Database), "test") {
		t.Fatalf("search integration tests require a database whose name contains test")
	}
	databasePool, connectionError := pgxpool.NewWithConfig(searchContext, databaseConfiguration)
	if connectionError != nil {
		t.Fatalf("create database pool: %v", connectionError)
	}
	defer databasePool.Close()

	fixturePrefix := "browse-snapshot-" + uuid.NewString()
	organization := fixturePrefix + " organization"
	defer func() {
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		if _, cleanupError := databasePool.Exec(
			cleanupContext,
			"DELETE FROM catalog_items WHERE external_id LIKE $1",
			fixturePrefix+"%",
		); cleanupError != nil {
			t.Errorf("cleanup browse fixtures: %v", cleanupError)
		}
	}()

	now := time.Now().UTC().Truncate(time.Microsecond)
	fixtureRows := []struct {
		suffix        string
		source        models.ItemSource
		title         string
		url           string
		modality      string
		neighborhoods []string
		sourceData    string
		createdAt     time.Time
	}{
		{
			suffix:        "-slug-winner",
			source:        models.SourceTypesense,
			title:         "Shared slug winner",
			modality:      "ONLINE",
			neighborhoods: []string{"Méier", "méier"},
			sourceData:    `{"slug":"shared-service"}`,
			createdAt:     now,
		},
		{
			suffix:        "-explicit-winner",
			source:        models.SourceSalesForce,
			title:         "Explicit winner",
			modality:      "Hybrid",
			neighborhoods: []string{"Tijuca"},
			sourceData:    `{"canonical_id":"explicit-service"}`,
			createdAt:     now.Add(-time.Minute),
		},
		{
			suffix:        "-independent",
			source:        models.SourceSalesForce,
			title:         "Independent service",
			modality:      "teletransporte",
			neighborhoods: []string{"MEIER"},
			sourceData:    `{}`,
			createdAt:     now.Add(-2 * time.Minute),
		},
		{
			suffix:        "-slug-loser",
			source:        models.SourceSalesForce,
			title:         "Shared URL loser",
			url:           "https://pref.rio/servicos/shared-service?campaign=test",
			modality:      "presencial",
			neighborhoods: []string{"Centro"},
			sourceData:    `{}`,
			createdAt:     now.Add(-3 * time.Minute),
		},
		{
			suffix:        "-explicit-loser",
			source:        models.SourceTypesense,
			title:         "Explicit loser",
			modality:      "presencial",
			neighborhoods: []string{"Centro"},
			sourceData:    `{"canonical_id":"EXPLICIT-SERVICE"}`,
			createdAt:     now.Add(-4 * time.Minute),
		},
	}
	for _, fixtureRow := range fixtureRows {
		_, insertionError := databasePool.Exec(searchContext, `
			INSERT INTO catalog_items (
				external_id, source, type, title, organization, url,
				modalidade, bairros, status, source_data, created_at, updated_at
			) VALUES ($1, $2, 'service', $3, $4, $5, $6, $7, 'active', $8, $9, $9)
		`,
			fixturePrefix+fixtureRow.suffix,
			fixtureRow.source,
			fixtureRow.title,
			organization,
			fixtureRow.url,
			fixtureRow.modality,
			fixtureRow.neighborhoods,
			fixtureRow.sourceData,
			fixtureRow.createdAt,
		)
		if insertionError != nil {
			t.Fatalf("insert %s fixture: %v", fixtureRow.suffix, insertionError)
		}
	}

	searchRepository := NewSearchRepository(databasePool)
	firstPage, firstPageError := searchRepository.BrowseSnapshot(searchContext, &models.SearchRequest{
		Filters: models.SearchFilters{Orgao: organization},
		Page:    1,
		PerPage: 2,
	})
	if firstPageError != nil {
		t.Fatalf("first BrowseSnapshot returned an error: %v", firstPageError)
	}
	secondPage, secondPageError := searchRepository.BrowseSnapshot(searchContext, &models.SearchRequest{
		Filters: models.SearchFilters{Orgao: organization},
		Page:    2,
		PerPage: 2,
	})
	if secondPageError != nil {
		t.Fatalf("second BrowseSnapshot returned an error: %v", secondPageError)
	}

	if firstPage.Total != 3 || secondPage.Total != 3 {
		t.Fatalf("canonical totals = first %d second %d, want 3", firstPage.Total, secondPage.Total)
	}
	if len(firstPage.Results) != 2 || len(secondPage.Results) != 1 {
		t.Fatalf("page sizes = first %d second %d, want 2 and 1", len(firstPage.Results), len(secondPage.Results))
	}
	returnedExternalIDs := []string{
		firstPage.Results[0].Item.ExternalID,
		firstPage.Results[1].Item.ExternalID,
		secondPage.Results[0].Item.ExternalID,
	}
	expectedExternalIDs := []string{
		fixturePrefix + "-slug-winner",
		fixturePrefix + "-explicit-winner",
		fixturePrefix + "-independent",
	}
	for resultIndex, expectedExternalID := range expectedExternalIDs {
		if returnedExternalIDs[resultIndex] != expectedExternalID {
			t.Fatalf("result %d = %q, want %q", resultIndex, returnedExternalIDs[resultIndex], expectedExternalID)
		}
	}
	if firstPage.CatalogRevision == "" || firstPage.CatalogRevision != secondPage.CatalogRevision {
		t.Fatalf("snapshot revisions = first %q second %q", firstPage.CatalogRevision, secondPage.CatalogRevision)
	}
	if len(firstPage.Facets.Types) != 1 || firstPage.Facets.Types[0].Count != 3 {
		t.Fatalf("type facets = %#v", firstPage.Facets.Types)
	}
	if len(firstPage.Facets.Organizations) != 1 || firstPage.Facets.Organizations[0].Count != 3 {
		t.Fatalf("organization facets = %#v", firstPage.Facets.Organizations)
	}
	if len(firstPage.Facets.Bairros) != 2 ||
		firstPage.Facets.Bairros[0].Value != "meier" ||
		firstPage.Facets.Bairros[0].Count != 2 {
		t.Fatalf("neighborhood facets = %#v", firstPage.Facets.Bairros)
	}
	if len(firstPage.Facets.Modalidades) != 2 ||
		firstPage.Facets.Modalidades[0].Value != "digital" ||
		firstPage.Facets.Modalidades[1].Value != "hibrido" {
		t.Fatalf("modality facets = %#v", firstPage.Facets.Modalidades)
	}

	digitalSnapshot, digitalSnapshotError := searchRepository.BrowseSnapshot(searchContext, &models.SearchRequest{
		Filters: models.SearchFilters{Orgao: organization, Modalidade: "digital"},
		Page:    1,
		PerPage: 10,
	})
	if digitalSnapshotError != nil {
		t.Fatalf("digital BrowseSnapshot returned an error: %v", digitalSnapshotError)
	}
	if digitalSnapshot.Total != 1 ||
		len(digitalSnapshot.Results) != 1 ||
		digitalSnapshot.Results[0].Item.ExternalID != fixturePrefix+"-slug-winner" {
		t.Fatalf("digital round-trip snapshot = %#v", digitalSnapshot)
	}

	neighborhoodSnapshot, neighborhoodSnapshotError := searchRepository.BrowseSnapshot(searchContext, &models.SearchRequest{
		Filters: models.SearchFilters{Orgao: organization, Bairro: "meier"},
		Page:    1,
		PerPage: 10,
	})
	if neighborhoodSnapshotError != nil {
		t.Fatalf("neighborhood BrowseSnapshot returned an error: %v", neighborhoodSnapshotError)
	}
	if neighborhoodSnapshot.Total != 2 || len(neighborhoodSnapshot.Results) != 2 {
		t.Fatalf("neighborhood round-trip snapshot = %#v", neighborhoodSnapshot)
	}

	urlIsolationOrganization := fixturePrefix + " URL isolation"
	if _, insertionError := databasePool.Exec(searchContext, `
		INSERT INTO catalog_items (
			external_id, source, type, title, organization, url,
			status, source_data, created_at, updated_at
		) VALUES
			($1, 'typesense', 'service', 'Real service path', $3,
				'https://pref.rio/servicos/shared-service', 'active', '{}', NOW(), NOW()),
			($2, 'salesforce', 'service', 'Query text only', $3,
				'https://pref.rio/other?next=/servicos/shared-service', 'active', '{}', NOW(), NOW())
	`, fixturePrefix+"-real-service-path", fixturePrefix+"-query-text-only", urlIsolationOrganization); insertionError != nil {
		t.Fatalf("insert URL isolation fixtures: %v", insertionError)
	}
	urlIsolationSnapshot, urlIsolationError := searchRepository.BrowseSnapshot(searchContext, &models.SearchRequest{
		Filters: models.SearchFilters{Orgao: urlIsolationOrganization},
		Page:    1,
		PerPage: 10,
	})
	if urlIsolationError != nil {
		t.Fatalf("URL isolation BrowseSnapshot returned an error: %v", urlIsolationError)
	}
	if urlIsolationSnapshot.Total != 2 || len(urlIsolationSnapshot.Results) != 2 {
		t.Fatalf("query text was mistaken for URL path canonical evidence: %#v", urlIsolationSnapshot)
	}
}
