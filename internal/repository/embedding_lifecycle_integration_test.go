package repository_test

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
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

const embeddingTestDatabaseURLVariable = "APP_CATALOGO_EMBEDDING_TEST_DATABASE_URL"

func TestEmbeddingLifecycleIntegration(t *testing.T) {
	testDatabasePool := openEmbeddingTestDatabase(t)
	catalogItemRepository := repository.NewCatalogItemRepository(testDatabasePool)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()

	firstExternalID := "embedding-lifecycle-first-" + uuid.NewString()
	secondExternalID := "embedding-lifecycle-second-" + uuid.NewString()
	partialMetadataExternalID := "embedding-lifecycle-partial-" + uuid.NewString()
	firstSourceUpdatedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	secondSourceUpdatedAt := time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() {
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		_, _ = testDatabasePool.Exec(
			cleanupContext,
			"DELETE FROM catalog_items WHERE source = 'typesense' AND external_id = ANY($1::text[])",
			[]string{firstExternalID, secondExternalID, partialMetadataExternalID},
		)
	})

	firstItemID := insertEmbeddingTestCatalogItem(
		t,
		testContext,
		testDatabasePool,
		firstExternalID,
		"First catalog item",
		firstSourceUpdatedAt,
	)
	secondItemID := insertEmbeddingTestCatalogItem(
		t,
		testContext,
		testDatabasePool,
		secondExternalID,
		"Second catalog item",
		secondSourceUpdatedAt,
	)

	lockedTransaction, err := testDatabasePool.Begin(testContext)
	if err != nil {
		t.Fatalf("failed to begin row-lock transaction: %v", err)
	}
	defer lockedTransaction.Rollback(context.Background())
	var lockedItemID uuid.UUID
	if err := lockedTransaction.QueryRow(
		testContext,
		"SELECT id FROM catalog_items WHERE id = $1 FOR UPDATE",
		firstItemID,
	).Scan(&lockedItemID); err != nil {
		t.Fatalf("failed to lock first catalog item: %v", err)
	}

	embeddingMetadata := integrationEmbeddingMetadata("001")
	secondClaim, err := catalogItemRepository.ClaimItemsForEmbedding(
		testContext,
		embeddingMetadata,
		1,
		15*time.Minute,
	)
	if err != nil {
		t.Fatalf("failed to claim while first item was locked: %v", err)
	}
	if len(secondClaim.Items) != 1 || secondClaim.Items[0].ID != secondItemID {
		t.Fatalf("SKIP LOCKED claim IDs = %v, expected only %s", embeddingClaimItemIDs(secondClaim), secondItemID)
	}
	if err := catalogItemRepository.ReleaseEmbeddingClaim(testContext, secondClaim.Token); err != nil {
		t.Fatalf("failed to release second claim: %v", err)
	}
	if err := lockedTransaction.Rollback(testContext); err != nil {
		t.Fatalf("failed to release first row lock: %v", err)
	}

	firstClaim, err := catalogItemRepository.ClaimItemsForEmbedding(
		testContext,
		embeddingMetadata,
		1,
		15*time.Minute,
	)
	if err != nil {
		t.Fatalf("failed to claim first catalog item: %v", err)
	}
	if len(firstClaim.Items) != 1 || firstClaim.Items[0].ID != firstItemID {
		t.Fatalf("first claim IDs = %v, expected only %s", embeddingClaimItemIDs(firstClaim), firstItemID)
	}

	vectorLiteral := clients.VectorLiteral(make([]float32, embeddingMetadata.Dimensions))
	firstSourceHash := strings.Repeat("a", 64)
	completed, err := catalogItemRepository.CompleteEmbedding(testContext, repository.EmbeddingCompletion{
		ItemID:        firstItemID,
		ClaimToken:    firstClaim.Token,
		VectorLiteral: vectorLiteral,
		SourceHash:    firstSourceHash,
		Metadata:      embeddingMetadata,
	})
	if err != nil {
		t.Fatalf("failed to complete first embedding: %v", err)
	}
	if !completed {
		t.Fatalf("first embedding completion was unexpectedly rejected")
	}
	assertStoredEmbeddingMetadata(
		t,
		testContext,
		testDatabasePool,
		firstItemID,
		embeddingMetadata,
		firstSourceHash,
		firstSourceUpdatedAt,
	)

	nextMetadata := integrationEmbeddingMetadata("002")
	staleClaim, err := catalogItemRepository.ClaimItemsForEmbedding(
		testContext,
		nextMetadata,
		1,
		15*time.Minute,
	)
	if err != nil {
		t.Fatalf("failed to claim stale embedding: %v", err)
	}
	if len(staleClaim.Items) != 1 || staleClaim.Items[0].ID != firstItemID {
		t.Fatalf("stale claim IDs = %v, expected only %s", embeddingClaimItemIDs(staleClaim), firstItemID)
	}

	if err := catalogItemRepository.Upsert(testContext, &models.CatalogItem{
		ExternalID: firstExternalID,
		Source:     models.SourceTypesense,
		Type:       models.TypeService,
		Title:      "First catalog item updated",
		Status:     models.StatusActive,
	}); err != nil {
		t.Fatalf("failed to update claimed source item: %v", err)
	}

	staleCompletionAccepted, err := catalogItemRepository.CompleteEmbedding(
		testContext,
		repository.EmbeddingCompletion{
			ItemID:        firstItemID,
			ClaimToken:    staleClaim.Token,
			VectorLiteral: vectorLiteral,
			SourceHash:    strings.Repeat("b", 64),
			Metadata:      nextMetadata,
		},
	)
	if err != nil {
		t.Fatalf("stale embedding completion returned an error: %v", err)
	}
	if staleCompletionAccepted {
		t.Fatalf("stale embedding completion was accepted after source update")
	}
	assertEmbeddingInvalidated(t, testContext, testDatabasePool, firstItemID)

	_, err = testDatabasePool.Exec(testContext, `
		INSERT INTO catalog_items (
			external_id,
			source,
			type,
			title,
			status,
			embedding,
			embedding_model_version,
			embedding_dimensions,
			embedding_task_type,
			embedding_document_version,
			embedding_source_hash,
			embedding_generated_at
		) VALUES (
			$1,
			'typesense',
			'service',
			'Invalid partial metadata',
			'active',
			$2::vector,
			$3,
			$4,
			$5,
			$6,
			$7,
			NOW()
		)
	`,
		partialMetadataExternalID,
		vectorLiteral,
		embeddingMetadata.Version,
		embeddingMetadata.Dimensions,
		embeddingMetadata.DocumentTaskType,
		embeddingMetadata.DocumentVersion,
		strings.Repeat("c", 64),
	)
	if err == nil || !strings.Contains(err.Error(), "catalog_items_embedding_metadata_consistent") {
		t.Fatalf("partial metadata error = %v, expected metadata consistency constraint violation", err)
	}
}

func openEmbeddingTestDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	connectionString := os.Getenv(embeddingTestDatabaseURLVariable)
	if connectionString == "" {
		t.Skipf("set %s to run PostgreSQL embedding lifecycle integration tests", embeddingTestDatabaseURLVariable)
	}

	poolConfiguration, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		t.Fatalf("invalid embedding test database configuration")
	}
	if !strings.Contains(strings.ToLower(poolConfiguration.ConnConfig.Database), "test") {
		t.Fatalf("embedding integration tests require a database whose name contains test")
	}
	poolConfiguration.MaxConns = 4

	testDatabasePool, err := pgxpool.NewWithConfig(context.Background(), poolConfiguration)
	if err != nil {
		t.Fatalf("failed to create embedding test database pool")
	}
	t.Cleanup(testDatabasePool.Close)
	if err := testDatabasePool.Ping(context.Background()); err != nil {
		t.Fatalf("failed to connect to embedding test database")
	}
	return testDatabasePool
}

func insertEmbeddingTestCatalogItem(
	t *testing.T,
	testContext context.Context,
	testDatabasePool *pgxpool.Pool,
	externalID string,
	title string,
	updatedAt time.Time,
) uuid.UUID {
	t.Helper()
	var catalogItemID uuid.UUID
	err := testDatabasePool.QueryRow(testContext, `
		INSERT INTO catalog_items (
			external_id,
			source,
			type,
			title,
			status,
			updated_at
		) VALUES ($1, 'typesense', 'service', $2, 'active', $3)
		RETURNING id
	`, externalID, title, updatedAt).Scan(&catalogItemID)
	if err != nil {
		t.Fatalf("failed to insert embedding test catalog item: %v", err)
	}
	return catalogItemID
}

func embeddingClaimItemIDs(claim *repository.EmbeddingClaim) []uuid.UUID {
	itemIDs := make([]uuid.UUID, len(claim.Items))
	for itemIndex, catalogItem := range claim.Items {
		itemIDs[itemIndex] = catalogItem.ID
	}
	return itemIDs
}

func integrationEmbeddingMetadata(version string) models.EmbeddingMetadata {
	return models.EmbeddingMetadata{
		Model:            "gemini-embedding-001",
		Version:          version,
		Dimensions:       1536,
		DocumentTaskType: "RETRIEVAL_DOCUMENT",
		QueryTaskType:    "RETRIEVAL_QUERY",
		DocumentVersion:  "catalog-item-v1",
	}
}

func assertStoredEmbeddingMetadata(
	t *testing.T,
	testContext context.Context,
	testDatabasePool *pgxpool.Pool,
	catalogItemID uuid.UUID,
	metadata models.EmbeddingMetadata,
	expectedSourceHash string,
	expectedUpdatedAt time.Time,
) {
	t.Helper()
	var (
		storedModel            string
		storedVersion          string
		storedDimensions       int
		storedTaskType         string
		storedDocumentVersion  string
		storedSourceHash       string
		storedVectorDimensions int
		generatedAtPresent     bool
		claimCleared           bool
		storedUpdatedAt        time.Time
	)
	err := testDatabasePool.QueryRow(testContext, `
		SELECT
			embedding_model,
			embedding_model_version,
			embedding_dimensions,
			embedding_task_type,
			embedding_document_version,
			embedding_source_hash,
			vector_dims(embedding),
			embedding_generated_at IS NOT NULL,
			embedding_claim_token IS NULL AND embedding_claimed_at IS NULL,
			updated_at
		FROM catalog_items
		WHERE id = $1
	`, catalogItemID).Scan(
		&storedModel,
		&storedVersion,
		&storedDimensions,
		&storedTaskType,
		&storedDocumentVersion,
		&storedSourceHash,
		&storedVectorDimensions,
		&generatedAtPresent,
		&claimCleared,
		&storedUpdatedAt,
	)
	if err != nil {
		t.Fatalf("failed to read completed embedding: %v", err)
	}
	if storedModel != metadata.Model ||
		storedVersion != metadata.Version ||
		storedDimensions != metadata.Dimensions ||
		storedTaskType != metadata.DocumentTaskType ||
		storedDocumentVersion != metadata.DocumentVersion ||
		storedSourceHash != expectedSourceHash ||
		storedVectorDimensions != metadata.Dimensions ||
		!generatedAtPresent ||
		!claimCleared ||
		!storedUpdatedAt.Equal(expectedUpdatedAt) {
		t.Fatalf(
			"stored embedding metadata = (%q, %q, %d, %q, %q, %q, %d, generated=%t, claim_cleared=%t, updated_at=%s)",
			storedModel,
			storedVersion,
			storedDimensions,
			storedTaskType,
			storedDocumentVersion,
			storedSourceHash,
			storedVectorDimensions,
			generatedAtPresent,
			claimCleared,
			storedUpdatedAt,
		)
	}
}

func assertEmbeddingInvalidated(
	t *testing.T,
	testContext context.Context,
	testDatabasePool *pgxpool.Pool,
	catalogItemID uuid.UUID,
) {
	t.Helper()
	var embeddingStateCleared bool
	err := testDatabasePool.QueryRow(testContext, `
		SELECT
			embedding IS NULL
			AND embedding_model IS NULL
			AND embedding_model_version IS NULL
			AND embedding_dimensions IS NULL
			AND embedding_task_type IS NULL
			AND embedding_document_version IS NULL
			AND embedding_source_hash IS NULL
			AND embedding_generated_at IS NULL
			AND embedding_claim_token IS NULL
			AND embedding_claimed_at IS NULL
		FROM catalog_items
		WHERE id = $1
	`, catalogItemID).Scan(&embeddingStateCleared)
	if err != nil {
		t.Fatalf("failed to read invalidated embedding state: %v", err)
	}
	if !embeddingStateCleared {
		t.Fatalf("source update did not clear embedding metadata and claim")
	}
}
