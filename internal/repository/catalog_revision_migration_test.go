package repository

import (
	"context"
	"database/sql"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

const catalogRevisionMigrationPath = "../../db/migrations/000005_catalog_revision.sql"

const catalogMigrationTestDatabaseURLVariable = "APP_CATALOGO_MIGRATION_TEST_DATABASE_URL"

func TestCatalogRevisionMigrationContract(t *testing.T) {
	migrations, collectionError := goose.CollectMigrations("../../db/migrations", 0, math.MaxInt64)
	if collectionError != nil {
		t.Fatalf("collect Goose migrations: %v", collectionError)
	}
	if len(migrations) == 0 || migrations[len(migrations)-1].Version != 5 {
		t.Fatalf("latest Goose migration = %#v, want version 5", migrations)
	}

	migrationBytes, readError := os.ReadFile(catalogRevisionMigrationPath)
	if readError != nil {
		t.Fatalf("read catalog revision migration: %v", readError)
	}
	migrationSQL := string(migrationBytes)
	for _, requiredFragment := range []string{
		"-- +goose Up",
		"-- +goose Down",
		"-- +goose StatementBegin",
		"-- +goose StatementEnd",
		"CREATE TABLE catalog_state",
		"CHECK (singleton)",
		"CHECK (revision > 0)",
		"CREATE FUNCTION bump_catalog_revision()",
		"CREATE CONSTRAINT TRIGGER trg_catalog_items_revision",
		"AFTER INSERT OR UPDATE OR DELETE ON catalog_items",
		"DEFERRABLE INITIALLY DEFERRED",
		"embedding_claim_token",
		"embedding_claimed_at",
		"current_setting('app_catalogo.catalog_revision_bumped', TRUE)",
		"set_config('app_catalogo.catalog_revision_bumped', '1', TRUE)",
		"DROP TRIGGER IF EXISTS trg_catalog_items_revision ON catalog_items",
		"DROP FUNCTION IF EXISTS bump_catalog_revision()",
		"DROP TABLE IF EXISTS catalog_state",
	} {
		if !strings.Contains(migrationSQL, requiredFragment) {
			t.Errorf("catalog revision migration is missing %q", requiredFragment)
		}
	}
	if strings.Contains(migrationSQL, "-- +goose NO TRANSACTION") {
		t.Error("catalog revision migration must remain atomic")
	}
}

func TestCatalogRevisionMigrationRoundTrip(t *testing.T) {
	databaseURL := os.Getenv(catalogMigrationTestDatabaseURLVariable)
	if databaseURL == "" {
		t.Skip(catalogMigrationTestDatabaseURLVariable + " is not configured")
	}
	databaseConfiguration, parseError := pgxpool.ParseConfig(databaseURL)
	if parseError != nil {
		t.Fatalf("parse migration test database configuration: %v", parseError)
	}
	if !strings.Contains(strings.ToLower(databaseConfiguration.ConnConfig.Database), "test") {
		t.Fatalf("migration round-trip requires a database whose name contains test")
	}

	testContext, cancelTest := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelTest()
	database, openError := sql.Open("pgx", databaseURL)
	if openError != nil {
		t.Fatalf("open migration test database: %v", openError)
	}
	t.Cleanup(func() {
		if closeError := database.Close(); closeError != nil {
			t.Errorf("close migration test database: %v", closeError)
		}
	})
	if pingError := database.PingContext(testContext); pingError != nil {
		t.Fatalf("ping migration test database: %v", pingError)
	}
	if dialectError := goose.SetDialect("postgres"); dialectError != nil {
		t.Fatalf("configure Goose PostgreSQL dialect: %v", dialectError)
	}

	startingVersion, versionError := goose.GetDBVersionContext(testContext, database)
	if versionError != nil {
		t.Fatalf("read starting migration version: %v", versionError)
	}
	if startingVersion != 4 && startingVersion != 5 {
		t.Fatalf("migration round-trip requires schema version 4 or 5, got %d", startingVersion)
	}
	t.Cleanup(func() {
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelCleanup()
		currentVersion, currentVersionError := goose.GetDBVersionContext(cleanupContext, database)
		if currentVersionError == nil && currentVersion == 4 {
			if restoreError := goose.UpToContext(cleanupContext, database, "../../db/migrations", 5); restoreError != nil {
				t.Errorf("restore catalog revision migration after test: %v", restoreError)
			}
		}
	})

	if startingVersion == 4 {
		if upError := goose.UpToContext(testContext, database, "../../db/migrations", 5); upError != nil {
			t.Fatalf("apply catalog revision migration: %v", upError)
		}
	}
	assertCatalogRevisionDatabaseObjects(t, testContext, database, true)

	fixtureExternalID := "catalog-revision-migration-" + uuid.NewString()
	t.Cleanup(func() {
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		if _, cleanupError := database.ExecContext(
			cleanupContext,
			"DELETE FROM catalog_items WHERE external_id = $1",
			fixtureExternalID,
		); cleanupError != nil {
			t.Errorf("cleanup migration round-trip fixture: %v", cleanupError)
		}
	})
	initialRevision := readDatabaseCatalogRevision(t, testContext, database)
	if _, insertionError := database.ExecContext(testContext, `
		INSERT INTO catalog_items (external_id, source, type, title, status)
		VALUES ($1, 'typesense', 'service', 'Migration round-trip fixture', 'active')
	`, fixtureExternalID); insertionError != nil {
		t.Fatalf("insert migration round-trip fixture: %v", insertionError)
	}
	if afterInsert := readDatabaseCatalogRevision(t, testContext, database); afterInsert != initialRevision+1 {
		t.Fatalf("revision after first migration apply and insert = %d, want %d", afterInsert, initialRevision+1)
	}

	if downError := goose.DownToContext(testContext, database, "../../db/migrations", 4); downError != nil {
		t.Fatalf("roll back catalog revision migration: %v", downError)
	}
	assertCatalogRevisionDatabaseObjects(t, testContext, database, false)
	if upError := goose.UpToContext(testContext, database, "../../db/migrations", 5); upError != nil {
		t.Fatalf("reapply catalog revision migration: %v", upError)
	}
	assertCatalogRevisionDatabaseObjects(t, testContext, database, true)
	if revisionAfterReapply := readDatabaseCatalogRevision(t, testContext, database); revisionAfterReapply != 1 {
		t.Fatalf("revision after migration reapply = %d, want 1", revisionAfterReapply)
	}
	if _, updateError := database.ExecContext(testContext, `
		UPDATE catalog_items SET title = 'Migration reapplied' WHERE external_id = $1
	`, fixtureExternalID); updateError != nil {
		t.Fatalf("update fixture after migration reapply: %v", updateError)
	}
	if revisionAfterUpdate := readDatabaseCatalogRevision(t, testContext, database); revisionAfterUpdate != 2 {
		t.Fatalf("revision after migration reapply and update = %d, want 2", revisionAfterUpdate)
	}
}

func TestCatalogRevisionTracksCommittedSearchChangesOnly(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()

	fixturePrefix := "catalog-revision-" + uuid.NewString()
	cleanupCatalogRepositoryFixtures(t, databasePool, fixturePrefix)
	catalogRepository := NewCatalogItemRepository(databasePool)

	initialSnapshot, initialSnapshotError := catalogRepository.CatalogSnapshot(testContext)
	if initialSnapshotError != nil {
		t.Fatalf("read initial catalog revision: %v", initialSnapshotError)
	}

	fixture := &models.CatalogItem{
		ExternalID: fixturePrefix,
		Source:     models.SourceTypesense,
		Type:       models.TypeService,
		Title:      "Revision fixture",
		Status:     models.StatusActive,
		SourceData: []byte(`{"fixture":true}`),
	}
	changed, insertionError := catalogRepository.UpsertBatch(testContext, []*models.CatalogItem{fixture})
	if insertionError != nil || changed != 1 {
		t.Fatalf("insert catalog revision fixture: changed=%d error=%v", changed, insertionError)
	}
	afterInsert := readCatalogContentRevision(t, testContext, catalogRepository)
	if afterInsert != initialSnapshot.ContentRevision+1 {
		t.Fatalf("revision after insert = %d, want %d", afterInsert, initialSnapshot.ContentRevision+1)
	}

	claimToken := uuid.New()
	if _, claimError := databasePool.Exec(testContext, `
		UPDATE catalog_items
		SET embedding_claim_token = $2,
			embedding_claimed_at = NOW()
		WHERE external_id = $1
	`, fixturePrefix, claimToken); claimError != nil {
		t.Fatalf("set transient embedding claim: %v", claimError)
	}
	if afterClaim := readCatalogContentRevision(t, testContext, catalogRepository); afterClaim != afterInsert {
		t.Fatalf("revision after transient claim = %d, want %d", afterClaim, afterInsert)
	}
	if releaseError := catalogRepository.ReleaseEmbeddingClaim(testContext, claimToken); releaseError != nil {
		t.Fatalf("release transient embedding claim: %v", releaseError)
	}
	if afterRelease := readCatalogContentRevision(t, testContext, catalogRepository); afterRelease != afterInsert {
		t.Fatalf("revision after transient claim release = %d, want %d", afterRelease, afterInsert)
	}
	if upsertError := catalogRepository.Upsert(testContext, fixture); upsertError != nil {
		t.Fatalf("repeat identical unit upsert: %v", upsertError)
	}
	if afterNoOpUpsert := readCatalogContentRevision(t, testContext, catalogRepository); afterNoOpUpsert != afterInsert {
		t.Fatalf("revision after identical unit upsert = %d, want %d", afterNoOpUpsert, afterInsert)
	}

	rollbackTransaction, beginError := databasePool.Begin(testContext)
	if beginError != nil {
		t.Fatalf("begin revision rollback transaction: %v", beginError)
	}
	if _, updateError := rollbackTransaction.Exec(testContext, `
		UPDATE catalog_items SET title = 'Rolled back title' WHERE external_id = $1
	`, fixturePrefix); updateError != nil {
		rollbackTransaction.Rollback(testContext)
		t.Fatalf("update catalog fixture before rollback: %v", updateError)
	}
	if _, updateError := rollbackTransaction.Exec(testContext, `
		UPDATE catalog_items SET description = 'Rolled back description' WHERE external_id = $1
	`, fixturePrefix); updateError != nil {
		rollbackTransaction.Rollback(testContext)
		t.Fatalf("update catalog fixture again before rollback: %v", updateError)
	}
	if rollbackError := rollbackTransaction.Rollback(testContext); rollbackError != nil {
		t.Fatalf("roll back catalog revision transaction: %v", rollbackError)
	}
	if afterRollback := readCatalogContentRevision(t, testContext, catalogRepository); afterRollback != afterInsert {
		t.Fatalf("revision after rollback = %d, want %d", afterRollback, afterInsert)
	}

	commitTransaction, beginError := databasePool.Begin(testContext)
	if beginError != nil {
		t.Fatalf("begin multi-write catalog transaction: %v", beginError)
	}
	if _, updateError := commitTransaction.Exec(testContext, `
		UPDATE catalog_items SET title = 'Committed title' WHERE external_id = $1
	`, fixturePrefix); updateError != nil {
		commitTransaction.Rollback(testContext)
		t.Fatalf("update title in multi-write transaction: %v", updateError)
	}
	if _, updateError := commitTransaction.Exec(testContext, `
		UPDATE catalog_items SET description = 'Committed description' WHERE external_id = $1
	`, fixturePrefix); updateError != nil {
		commitTransaction.Rollback(testContext)
		t.Fatalf("update description in multi-write transaction: %v", updateError)
	}
	if commitError := commitTransaction.Commit(testContext); commitError != nil {
		t.Fatalf("commit multi-write catalog transaction: %v", commitError)
	}
	afterUpdate := readCatalogContentRevision(t, testContext, catalogRepository)
	if afterUpdate != afterInsert+1 {
		t.Fatalf("revision after committed multi-write transaction = %d, want %d", afterUpdate, afterInsert+1)
	}

	savepointTransaction, beginError := databasePool.Begin(testContext)
	if beginError != nil {
		t.Fatalf("begin catalog savepoint transaction: %v", beginError)
	}
	if _, savepointError := savepointTransaction.Exec(testContext, "SAVEPOINT catalog_revision_test"); savepointError != nil {
		savepointTransaction.Rollback(testContext)
		t.Fatalf("create catalog revision savepoint: %v", savepointError)
	}
	if _, updateError := savepointTransaction.Exec(testContext, `
		UPDATE catalog_items SET title = 'Savepoint title' WHERE external_id = $1
	`, fixturePrefix); updateError != nil {
		savepointTransaction.Rollback(testContext)
		t.Fatalf("update catalog fixture inside savepoint: %v", updateError)
	}
	if _, rollbackError := savepointTransaction.Exec(testContext, "ROLLBACK TO SAVEPOINT catalog_revision_test"); rollbackError != nil {
		savepointTransaction.Rollback(testContext)
		t.Fatalf("roll back catalog revision savepoint: %v", rollbackError)
	}
	if _, updateError := savepointTransaction.Exec(testContext, `
		UPDATE catalog_items SET description = 'Committed after savepoint' WHERE external_id = $1
	`, fixturePrefix); updateError != nil {
		savepointTransaction.Rollback(testContext)
		t.Fatalf("update catalog fixture after savepoint rollback: %v", updateError)
	}
	if commitError := savepointTransaction.Commit(testContext); commitError != nil {
		t.Fatalf("commit catalog savepoint transaction: %v", commitError)
	}
	afterSavepoint := readCatalogContentRevision(t, testContext, catalogRepository)
	if afterSavepoint != afterUpdate+1 {
		t.Fatalf("revision after savepoint transaction = %d, want %d", afterSavepoint, afterUpdate+1)
	}

	var fixtureID uuid.UUID
	if queryError := databasePool.QueryRow(
		testContext,
		"SELECT id FROM catalog_items WHERE external_id = $1",
		fixturePrefix,
	).Scan(&fixtureID); queryError != nil {
		t.Fatalf("read catalog fixture id: %v", queryError)
	}
	embeddingClaimToken := uuid.New()
	if _, claimError := databasePool.Exec(testContext, `
		UPDATE catalog_items
		SET embedding_claim_token = $2,
			embedding_claimed_at = NOW()
		WHERE id = $1
	`, fixtureID, embeddingClaimToken); claimError != nil {
		t.Fatalf("claim fixture before embedding completion: %v", claimError)
	}
	embedding := make([]float32, 1536)
	embedding[0] = 1
	embeddingCompleted, completionError := catalogRepository.CompleteEmbedding(testContext, EmbeddingCompletion{
		ItemID:        fixtureID,
		ClaimToken:    embeddingClaimToken,
		VectorLiteral: clients.VectorLiteral(embedding),
		SourceHash:    strings.Repeat("a", 64),
		Metadata: models.EmbeddingMetadata{
			Model:            "gemini-embedding-001",
			Version:          "001",
			Dimensions:       len(embedding),
			DocumentTaskType: "RETRIEVAL_DOCUMENT",
			DocumentVersion:  "catalog-item-v1",
		},
	})
	if completionError != nil || !embeddingCompleted {
		t.Fatalf("complete catalog fixture embedding: completed=%t error=%v", embeddingCompleted, completionError)
	}
	afterEmbedding := readCatalogContentRevision(t, testContext, catalogRepository)
	if afterEmbedding != afterSavepoint+1 {
		t.Fatalf("revision after embedding completion = %d, want %d", afterEmbedding, afterSavepoint+1)
	}

	overflowTransaction, beginError := databasePool.Begin(testContext)
	if beginError != nil {
		t.Fatalf("begin revision overflow transaction: %v", beginError)
	}
	if _, stateUpdateError := overflowTransaction.Exec(
		testContext,
		"UPDATE catalog_state SET revision = $1 WHERE singleton = TRUE",
		int64(math.MaxInt64),
	); stateUpdateError != nil {
		overflowTransaction.Rollback(testContext)
		t.Fatalf("prepare revision overflow: %v", stateUpdateError)
	}
	if _, updateError := overflowTransaction.Exec(testContext, `
		UPDATE catalog_items SET title = 'Overflow must roll back' WHERE external_id = $1
	`, fixturePrefix); updateError != nil {
		overflowTransaction.Rollback(testContext)
		t.Fatalf("update catalog fixture before revision overflow: %v", updateError)
	}
	if commitError := overflowTransaction.Commit(testContext); commitError == nil {
		t.Fatal("catalog revision overflow transaction unexpectedly committed")
	}
	if afterOverflow := readCatalogContentRevision(t, testContext, catalogRepository); afterOverflow != afterEmbedding {
		t.Fatalf("revision after overflow rollback = %d, want %d", afterOverflow, afterEmbedding)
	}

	if _, deletionError := databasePool.Exec(
		testContext,
		"DELETE FROM catalog_items WHERE external_id = $1",
		fixturePrefix,
	); deletionError != nil {
		t.Fatalf("delete catalog revision fixture: %v", deletionError)
	}
	if afterDelete := readCatalogContentRevision(t, testContext, catalogRepository); afterDelete != afterEmbedding+1 {
		t.Fatalf("revision after delete = %d, want %d", afterDelete, afterEmbedding+1)
	}
}

func TestCatalogRevisionDefersGlobalLockUntilCatalogWritesFinish(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()

	fixturePrefix := "catalog-revision-concurrency-" + uuid.NewString()
	cleanupCatalogRepositoryFixtures(t, databasePool, fixturePrefix)
	catalogRepository := NewCatalogItemRepository(databasePool)
	initialRevision := readCatalogContentRevision(t, testContext, catalogRepository)
	fixtures := []*models.CatalogItem{
		{
			ExternalID: fixturePrefix + "-first",
			Source:     models.SourceTypesense,
			Type:       models.TypeService,
			Title:      "First concurrency fixture",
			Status:     models.StatusActive,
			SourceData: []byte(`{"fixture":true}`),
		},
		{
			ExternalID: fixturePrefix + "-second",
			Source:     models.SourceTypesense,
			Type:       models.TypeService,
			Title:      "Second concurrency fixture",
			Status:     models.StatusActive,
			SourceData: []byte(`{"fixture":true}`),
		},
	}
	changed, insertionError := catalogRepository.UpsertBatch(testContext, fixtures)
	if insertionError != nil || changed != len(fixtures) {
		t.Fatalf("insert concurrency fixtures: changed=%d error=%v", changed, insertionError)
	}

	secondTransaction, beginError := databasePool.Begin(testContext)
	if beginError != nil {
		t.Fatalf("begin second catalog transaction: %v", beginError)
	}
	defer secondTransaction.Rollback(testContext)
	if _, updateError := secondTransaction.Exec(testContext, `
		UPDATE catalog_items SET title = 'Second transaction title' WHERE external_id = $1
	`, fixturePrefix+"-second"); updateError != nil {
		t.Fatalf("lock second catalog fixture: %v", updateError)
	}

	firstTransaction, beginError := databasePool.Begin(testContext)
	if beginError != nil {
		t.Fatalf("begin first catalog transaction: %v", beginError)
	}
	defer firstTransaction.Rollback(testContext)
	if _, updateError := firstTransaction.Exec(testContext, `
		UPDATE catalog_items SET title = 'First transaction title' WHERE external_id = $1
	`, fixturePrefix+"-first"); updateError != nil {
		t.Fatalf("lock first catalog fixture: %v", updateError)
	}

	secondUpdateCompleted := make(chan error, 1)
	go func() {
		_, updateError := secondTransaction.Exec(testContext, `
			UPDATE catalog_items SET description = 'Second transaction description' WHERE external_id = $1
		`, fixturePrefix+"-first")
		secondUpdateCompleted <- updateError
	}()
	select {
	case updateError := <-secondUpdateCompleted:
		t.Fatalf("second transaction did not wait for the first row lock: %v", updateError)
	case <-time.After(100 * time.Millisecond):
	}

	if commitError := firstTransaction.Commit(testContext); commitError != nil {
		t.Fatalf("commit first catalog transaction: %v", commitError)
	}
	select {
	case updateError := <-secondUpdateCompleted:
		if updateError != nil {
			t.Fatalf("complete second catalog update after row release: %v", updateError)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second catalog update remained blocked after the first commit")
	}
	if commitError := secondTransaction.Commit(testContext); commitError != nil {
		t.Fatalf("commit second catalog transaction: %v", commitError)
	}

	finalRevision := readCatalogContentRevision(t, testContext, catalogRepository)
	if finalRevision != initialRevision+3 {
		t.Fatalf("revision after insert and two concurrent commits = %d, want %d", finalRevision, initialRevision+3)
	}
}

func assertCatalogRevisionDatabaseObjects(
	t *testing.T,
	testContext context.Context,
	database *sql.DB,
	wantObjects bool,
) {
	t.Helper()
	var tableName sql.NullString
	var functionName sql.NullString
	var deferredConstraintTriggerExists bool
	queryError := database.QueryRowContext(testContext, `
		SELECT
			to_regclass('catalog_state')::text,
			to_regprocedure('bump_catalog_revision()')::text,
			EXISTS (
				SELECT 1
				FROM pg_trigger
				WHERE tgname = 'trg_catalog_items_revision'
				  AND tgrelid = to_regclass('catalog_items')
				  AND tgdeferrable = TRUE
				  AND tginitdeferred = TRUE
			)
	`).Scan(&tableName, &functionName, &deferredConstraintTriggerExists)
	if queryError != nil {
		t.Fatalf("inspect catalog revision database objects: %v", queryError)
	}
	objectsExist := tableName.Valid && functionName.Valid && deferredConstraintTriggerExists
	anyObjectExists := tableName.Valid || functionName.Valid || deferredConstraintTriggerExists
	objectsMatchExpectation := objectsExist
	if !wantObjects {
		objectsMatchExpectation = !anyObjectExists
	}
	if !objectsMatchExpectation {
		t.Fatalf(
			"catalog revision objects: table=%q function=%q deferred_trigger=%t, want all present=%t",
			tableName.String,
			functionName.String,
			deferredConstraintTriggerExists,
			wantObjects,
		)
	}
}

func readDatabaseCatalogRevision(t *testing.T, testContext context.Context, database *sql.DB) int64 {
	t.Helper()
	var revision int64
	if queryError := database.QueryRowContext(
		testContext,
		"SELECT revision FROM catalog_state WHERE singleton = TRUE",
	).Scan(&revision); queryError != nil {
		t.Fatalf("read database catalog revision: %v", queryError)
	}
	return revision
}

func readCatalogContentRevision(
	t *testing.T,
	testContext context.Context,
	catalogRepository *CatalogItemRepository,
) int64 {
	t.Helper()
	catalogSnapshot, snapshotError := catalogRepository.CatalogSnapshot(testContext)
	if snapshotError != nil {
		t.Fatalf("read catalog revision: %v", snapshotError)
	}
	return catalogSnapshot.ContentRevision
}
