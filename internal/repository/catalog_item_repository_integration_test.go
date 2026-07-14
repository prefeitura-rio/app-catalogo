package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

const catalogRepositoryTestDatabaseURLVariable = "APP_CATALOGO_SEARCH_TEST_DATABASE_URL"

func TestReconcileSourceSnapshotRejectsIncompleteOrMixedScopeBeforeDatabaseAccess(t *testing.T) {
	repositoryWithoutDatabase := &CatalogItemRepository{}
	snapshotUpperBound := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	typesenseItem := sourceSnapshotItem(
		models.SourceTypesense,
		"service-1",
		"Service",
		snapshotUpperBound,
	)

	upserted, deactivated, reconciliationError := repositoryWithoutDatabase.ReconcileSourceSnapshot(
		context.Background(),
		models.SourceTypesense,
		[]*models.CatalogItem{typesenseItem},
		2,
		&snapshotUpperBound,
		snapshotUpperBound,
	)
	if !errors.Is(reconciliationError, ErrIncompleteSourceSnapshot) {
		t.Fatalf("incomplete snapshot error = %v, want ErrIncompleteSourceSnapshot", reconciliationError)
	}
	if upserted != 0 || deactivated != 0 {
		t.Fatalf("incomplete snapshot reported upserted=%d deactivated=%d", upserted, deactivated)
	}

	mixedSourceItem := sourceSnapshotItem(models.SourceCourses, "course-1", "Course", snapshotUpperBound)
	upserted, deactivated, reconciliationError = repositoryWithoutDatabase.ReconcileSourceSnapshot(
		context.Background(),
		models.SourceTypesense,
		[]*models.CatalogItem{mixedSourceItem},
		1,
		&snapshotUpperBound,
		snapshotUpperBound,
	)
	if reconciliationError == nil || !strings.Contains(reconciliationError.Error(), "belongs to") {
		t.Fatalf("mixed-source snapshot error = %v", reconciliationError)
	}
	if upserted != 0 || deactivated != 0 {
		t.Fatalf("mixed-source snapshot reported upserted=%d deactivated=%d", upserted, deactivated)
	}
}

func TestCatalogItemReadsNullableFieldsAndRejectsFutureCandidates(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()

	fixturePrefix := "catalog-nullable-" + uuid.NewString()
	currentExternalID := fixturePrefix + "-current"
	futureExternalID := fixturePrefix + "-future"
	cleanupCatalogRepositoryFixtures(t, databasePool, fixturePrefix)

	futureValidityStart := time.Now().Add(time.Hour)
	var currentItemID uuid.UUID
	if err := databasePool.QueryRow(testContext, `
		INSERT INTO catalog_items (
			external_id, source, type, title,
			description, short_desc, organization, url, image_url,
			modalidade, status, valid_from
		) VALUES (
			$1, 'typesense', 'service', 'Nullable catalog item',
			NULL, NULL, NULL, NULL, NULL,
			NULL, 'active', NULL
		)
		RETURNING id
	`, currentExternalID).Scan(&currentItemID); err != nil {
		t.Fatalf("insert nullable catalog fixture: %v", err)
	}
	if _, err := databasePool.Exec(testContext, `
		INSERT INTO catalog_items (
			external_id, source, type, title, status, valid_from
		) VALUES ($1, 'typesense', 'service', 'Future catalog item', 'active', $2)
	`, futureExternalID, futureValidityStart); err != nil {
		t.Fatalf("insert future catalog fixture: %v", err)
	}

	catalogRepository := NewCatalogItemRepository(databasePool)
	bySource, err := catalogRepository.GetBySourceAndExternalID(testContext, models.SourceTypesense, currentExternalID)
	if err != nil {
		t.Fatalf("GetBySourceAndExternalID nullable item: %v", err)
	}
	assertNullableCatalogFieldsAreEmpty(t, bySource)

	byID, err := catalogRepository.GetByID(testContext, currentItemID)
	if err != nil {
		t.Fatalf("GetByID nullable item: %v", err)
	}
	assertNullableCatalogFieldsAreEmpty(t, byID)

	candidates, err := catalogRepository.GetCandidates(testContext, []models.ItemType{models.TypeService}, 100)
	if err != nil {
		t.Fatalf("GetCandidates nullable item: %v", err)
	}
	if !containsCatalogExternalID(candidates, currentExternalID) {
		t.Fatalf("current nullable item %q was omitted from candidates", currentExternalID)
	}
	if containsCatalogExternalID(candidates, futureExternalID) {
		t.Fatalf("future-dated item %q entered recommendation candidates", futureExternalID)
	}
}

func TestReconcileSalesForceSnapshotScopesAndCommitsAtomically(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()

	fixturePrefix := "salesforce-reconcile-" + uuid.NewString()
	cleanupCatalogRepositoryFixtures(t, databasePool, fixturePrefix)
	catalogRepository := NewCatalogItemRepository(databasePool)
	objectType := salesForceTestObjectType("Service")
	otherObjectType := salesForceTestObjectType("Other")
	cleanupSalesForceTestCursors(t, databasePool, objectType, otherObjectType)

	retainedExternalID := fixturePrefix + "-retained"
	staleExternalID := fixturePrefix + "-stale"
	legacyStaleExternalID := fixturePrefix + "-legacy-stale"
	otherScopeExternalID := fixturePrefix + "-other-scope"
	unscopedExternalID := fixturePrefix + "-unscoped"
	concurrentExternalID := fixturePrefix + "-concurrent"
	newExternalID := fixturePrefix + "-new"
	snapshotCursor := time.Now().UTC().Truncate(time.Microsecond)
	initialSourceUpdatedAt := snapshotCursor.Add(-time.Minute)

	initialItems := []*models.CatalogItem{
		salesForceSnapshotItemAt(retainedExternalID, objectType, "Retained service", initialSourceUpdatedAt),
		salesForceSnapshotItemAt(staleExternalID, objectType, "Stale service", initialSourceUpdatedAt),
		salesForceSnapshotItemAt(concurrentExternalID, objectType, "Concurrently updated service", initialSourceUpdatedAt),
		salesForceSnapshotItemAt(otherScopeExternalID, otherObjectType, "Other scoped service", initialSourceUpdatedAt),
	}
	if changed, err := catalogRepository.UpsertBatch(testContext, initialItems); err != nil || changed != len(initialItems) {
		t.Fatalf("insert initial scoped fixtures: changed=%d error=%v", changed, err)
	}
	if _, err := databasePool.Exec(testContext, `
		INSERT INTO catalog_items (external_id, source, type, title, status, source_data)
		VALUES
			($1, 'salesforce', 'service', 'Legacy stale service', 'active', jsonb_build_object('attributes', jsonb_build_object('type', $3::text))),
			($2, 'salesforce', 'service', 'Unscoped service', 'active', '{}')
	`, legacyStaleExternalID, unscopedExternalID, objectType); err != nil {
		t.Fatalf("insert legacy scope fixtures: %v", err)
	}
	if err := catalogRepository.SoftDelete(testContext, models.SourceSalesForce, retainedExternalID); err != nil {
		t.Fatalf("soft-delete retained fixture: %v", err)
	}

	if _, err := databasePool.Exec(testContext, `
		UPDATE catalog_items SET source_updated_at = $2
		WHERE source = 'salesforce' AND external_id = $1
	`, concurrentExternalID, snapshotCursor.Add(time.Minute)); err != nil {
		t.Fatalf("mark concurrent update fixture: %v", err)
	}
	upserted, deactivated, err := catalogRepository.ReconcileSalesForceSnapshot(
		testContext,
		objectType,
		[]*models.CatalogItem{
			salesForceSnapshotItemAt(retainedExternalID, objectType, "Retained service", snapshotCursor),
			salesForceSnapshotItemAt(newExternalID, objectType, "New service", snapshotCursor),
		},
		snapshotCursor,
	)
	if err != nil {
		t.Fatalf("ReconcileSalesForceSnapshot returned error: %v", err)
	}
	if upserted != 2 || deactivated != 2 {
		t.Fatalf("reconciliation counts = upserted %d, deactivated %d; want 2, 2", upserted, deactivated)
	}

	assertCatalogItemState(t, testContext, databasePool, retainedExternalID, models.StatusActive, false)
	assertCatalogItemState(t, testContext, databasePool, newExternalID, models.StatusActive, false)
	assertCatalogItemState(t, testContext, databasePool, staleExternalID, models.StatusInactive, true)
	assertCatalogItemState(t, testContext, databasePool, legacyStaleExternalID, models.StatusInactive, true)
	assertCatalogItemState(t, testContext, databasePool, concurrentExternalID, models.StatusActive, false)
	assertCatalogItemState(t, testContext, databasePool, otherScopeExternalID, models.StatusActive, false)
	assertCatalogItemState(t, testContext, databasePool, unscopedExternalID, models.StatusActive, false)

	if _, err := databasePool.Exec(testContext, `
		UPDATE salesforce_sync_cursor SET last_delta_token = NULL WHERE object_type = $1
	`, objectType); err != nil {
		t.Fatalf("set nullable delta token fixture: %v", err)
	}
	cursor, err := catalogRepository.GetSalesForceCursor(testContext, objectType)
	if err != nil {
		t.Fatalf("read reconciled cursor: %v", err)
	}
	if cursor.LastSyncAt == nil || !cursor.LastSyncAt.Equal(snapshotCursor) {
		t.Fatalf("reconciled cursor = %v, want %s", cursor.LastSyncAt, snapshotCursor)
	}
	if cursor.LastDeltaToken != "" {
		t.Fatalf("nullable delta token = %q, want normalized empty string", cursor.LastDeltaToken)
	}
}

func TestReconcileSalesForceSnapshotRollsBackOnUpsertFailure(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()

	fixturePrefix := "salesforce-atomic-" + uuid.NewString()
	cleanupCatalogRepositoryFixtures(t, databasePool, fixturePrefix)
	catalogRepository := NewCatalogItemRepository(databasePool)
	objectType := salesForceTestObjectType("Atomic")
	cleanupSalesForceTestCursors(t, databasePool, objectType)

	retainedExternalID := fixturePrefix + "-retained"
	staleExternalID := fixturePrefix + "-stale"
	if changed, err := catalogRepository.UpsertBatch(testContext, []*models.CatalogItem{
		salesForceSnapshotItem(retainedExternalID, objectType, "Original title"),
		salesForceSnapshotItem(staleExternalID, objectType, "Stale title"),
	}); err != nil || changed != 2 {
		t.Fatalf("insert atomic fixtures: changed=%d error=%v", changed, err)
	}

	newerSourceUpdatedAt := time.Now().UTC().Add(time.Minute)
	changedRetained := salesForceSnapshotItemAt(retainedExternalID, objectType, "Changed title", newerSourceUpdatedAt)
	invalidItem := salesForceSnapshotItemAt(fixturePrefix+"-invalid", objectType, "Invalid item", newerSourceUpdatedAt)
	invalidItem.Type = models.ItemType("invalid_type")
	upserted, deactivated, err := catalogRepository.ReconcileSalesForceSnapshot(
		testContext,
		objectType,
		[]*models.CatalogItem{changedRetained, invalidItem},
		newerSourceUpdatedAt,
	)
	if err == nil {
		t.Fatal("ReconcileSalesForceSnapshot accepted an invalid database item type")
	}
	if upserted != 0 || deactivated != 0 {
		t.Fatalf("failed reconciliation reported committed changes: upserted=%d deactivated=%d", upserted, deactivated)
	}

	var retainedTitle string
	if err := databasePool.QueryRow(testContext, `
		SELECT title FROM catalog_items WHERE source = 'salesforce' AND external_id = $1
	`, retainedExternalID).Scan(&retainedTitle); err != nil {
		t.Fatalf("read retained item after rollback: %v", err)
	}
	if retainedTitle != "Original title" {
		t.Fatalf("retained title after rollback = %q, want original", retainedTitle)
	}
	assertCatalogItemState(t, testContext, databasePool, staleExternalID, models.StatusActive, false)
}

func TestReconcileSalesForceSnapshotRejectsEmptySnapshot(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	catalogRepository := NewCatalogItemRepository(databasePool)

	upserted, deactivated, err := catalogRepository.ReconcileSalesForceSnapshot(
		context.Background(),
		salesForceTestObjectType("Empty"),
		nil,
		time.Now(),
	)
	if !errors.Is(err, ErrEmptySalesForceSnapshot) {
		t.Fatalf("empty reconciliation error = %v, want ErrEmptySalesForceSnapshot", err)
	}
	if upserted != 0 || deactivated != 0 {
		t.Fatalf("empty reconciliation reported changes: upserted=%d deactivated=%d", upserted, deactivated)
	}
}

func TestReconcileSourceSnapshotIsSourceScopedAndIdempotent(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()

	fixturePrefix := "source-reconcile-" + uuid.NewString()
	cleanupCatalogRepositoryFixtures(t, databasePool, fixturePrefix)
	catalogRepository := NewCatalogItemRepository(databasePool)
	initialTimestamp := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	retainedExternalID := fixturePrefix + "-retained"
	staleExternalID := fixturePrefix + "-stale"
	newExternalID := fixturePrefix + "-new"
	otherSourceExternalID := fixturePrefix + "-other-source"

	initialItems := []*models.CatalogItem{
		sourceSnapshotItem(models.SourceTypesense, retainedExternalID, "Original retained service", initialTimestamp),
		sourceSnapshotItem(models.SourceTypesense, staleExternalID, "Stale service", initialTimestamp),
		sourceSnapshotItem(models.SourceCourses, otherSourceExternalID, "Unrelated course", initialTimestamp),
	}
	if changed, insertError := catalogRepository.UpsertBatch(testContext, initialItems); insertError != nil || changed != 3 {
		t.Fatalf("insert source reconciliation fixtures: changed=%d error=%v", changed, insertError)
	}
	snapshotStartedAt := time.Now().UTC().Truncate(time.Microsecond)
	snapshotUpperBound := snapshotStartedAt

	snapshotItems := []*models.CatalogItem{
		sourceSnapshotItem(models.SourceTypesense, retainedExternalID, "Updated retained service", snapshotUpperBound),
		sourceSnapshotItem(models.SourceTypesense, newExternalID, "New service", snapshotUpperBound),
	}
	upserted, deactivated, reconciliationError := catalogRepository.ReconcileSourceSnapshot(
		testContext,
		models.SourceTypesense,
		snapshotItems,
		len(snapshotItems),
		&snapshotUpperBound,
		snapshotStartedAt,
	)
	if reconciliationError != nil {
		t.Fatalf("ReconcileSourceSnapshot returned error: %v", reconciliationError)
	}
	if upserted != 2 || deactivated != 1 {
		t.Fatalf("reconciliation counts = upserted %d, deactivated %d; want 2, 1", upserted, deactivated)
	}
	assertSourceCatalogItemState(t, testContext, databasePool, models.SourceTypesense, retainedExternalID, models.StatusActive, false)
	assertSourceCatalogItemState(t, testContext, databasePool, models.SourceTypesense, newExternalID, models.StatusActive, false)
	assertSourceCatalogItemState(t, testContext, databasePool, models.SourceTypesense, staleExternalID, models.StatusInactive, true)
	assertSourceCatalogItemState(t, testContext, databasePool, models.SourceCourses, otherSourceExternalID, models.StatusActive, false)
	staleVersion := sourceSnapshotItem(
		models.SourceTypesense,
		retainedExternalID,
		"Stale overwrite",
		initialTimestamp,
	)
	if changed, staleWriteError := catalogRepository.UpsertBatch(
		testContext,
		[]*models.CatalogItem{staleVersion},
	); staleWriteError != nil || changed != 0 {
		t.Fatalf("stale source write = changed %d, error %v; want no-op", changed, staleWriteError)
	}
	var retainedTitle string
	if readError := databasePool.QueryRow(testContext, `
		SELECT title FROM catalog_items WHERE source = 'typesense' AND external_id = $1
	`, retainedExternalID).Scan(&retainedTitle); readError != nil {
		t.Fatalf("read retained title after stale write: %v", readError)
	}
	if retainedTitle != "Updated retained service" {
		t.Fatalf("retained title after stale write = %q, want latest title", retainedTitle)
	}

	upserted, deactivated, reconciliationError = catalogRepository.ReconcileSourceSnapshot(
		testContext,
		models.SourceTypesense,
		snapshotItems,
		len(snapshotItems),
		&snapshotUpperBound,
		snapshotStartedAt,
	)
	if reconciliationError != nil {
		t.Fatalf("idempotent ReconcileSourceSnapshot returned error: %v", reconciliationError)
	}
	if upserted != 0 || deactivated != 0 {
		t.Fatalf("idempotent reconciliation changed upserted=%d deactivated=%d; want 0, 0", upserted, deactivated)
	}
}

func TestReconcileSourceSnapshotRollsBackUpsertsAndDeactivationsOnFailure(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()

	fixturePrefix := "source-atomic-" + uuid.NewString()
	cleanupCatalogRepositoryFixtures(t, databasePool, fixturePrefix)
	catalogRepository := NewCatalogItemRepository(databasePool)
	initialTimestamp := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	retainedExternalID := fixturePrefix + "-retained"
	staleExternalID := fixturePrefix + "-stale"
	if changed, insertError := catalogRepository.UpsertBatch(testContext, []*models.CatalogItem{
		sourceSnapshotItem(models.SourceTypesense, retainedExternalID, "Original retained service", initialTimestamp),
		sourceSnapshotItem(models.SourceTypesense, staleExternalID, "Stale service", initialTimestamp),
	}); insertError != nil || changed != 2 {
		t.Fatalf("insert atomic source fixtures: changed=%d error=%v", changed, insertError)
	}
	snapshotStartedAt := time.Now().UTC().Truncate(time.Microsecond)
	snapshotUpperBound := snapshotStartedAt

	changedRetained := sourceSnapshotItem(
		models.SourceTypesense,
		retainedExternalID,
		"Changed title that must roll back",
		snapshotUpperBound,
	)
	invalidItem := sourceSnapshotItem(
		models.SourceTypesense,
		fixturePrefix+"-invalid",
		"Invalid source data",
		snapshotUpperBound,
	)
	invalidItem.SourceData = json.RawMessage(`{invalid-json`)
	upserted, deactivated, reconciliationError := catalogRepository.ReconcileSourceSnapshot(
		testContext,
		models.SourceTypesense,
		[]*models.CatalogItem{changedRetained, invalidItem},
		2,
		&snapshotUpperBound,
		snapshotStartedAt,
	)
	if reconciliationError == nil {
		t.Fatal("ReconcileSourceSnapshot accepted invalid JSON source data")
	}
	if upserted != 0 || deactivated != 0 {
		t.Fatalf("failed reconciliation reported upserted=%d deactivated=%d", upserted, deactivated)
	}

	var retainedTitle string
	if readError := databasePool.QueryRow(testContext, `
		SELECT title
		FROM catalog_items
		WHERE source = 'typesense' AND external_id = $1
	`, retainedExternalID).Scan(&retainedTitle); readError != nil {
		t.Fatalf("read retained source item after rollback: %v", readError)
	}
	if retainedTitle != "Original retained service" {
		t.Fatalf("retained title after rollback = %q, want original", retainedTitle)
	}
	assertSourceCatalogItemState(t, testContext, databasePool, models.SourceTypesense, staleExternalID, models.StatusActive, false)
}

func TestReconcileStaleSourceSnapshotPreservesRowsWrittenAfterSnapshotStarted(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()

	fixturePrefix := "source-empty-boundary-" + uuid.NewString()
	cleanupCatalogRepositoryFixtures(t, databasePool, fixturePrefix)
	catalogRepository := NewCatalogItemRepository(databasePool)
	snapshotStartedAt := time.Now().UTC().Truncate(time.Microsecond)
	oldExternalID := fixturePrefix + "-old"
	newerExternalID := fixturePrefix + "-newer"
	if _, insertError := databasePool.Exec(testContext, `
		INSERT INTO catalog_items (
			external_id, source, type, title, status, source_updated_at, updated_at
		) VALUES
			($1, 'courses', 'course', 'Old course', 'active', $3, $4),
			($2, 'courses', 'course', 'Newer concurrent course', 'active', $3, $5)
	`,
		oldExternalID,
		newerExternalID,
		snapshotStartedAt.Add(-time.Hour),
		snapshotStartedAt.Add(-time.Minute),
		snapshotStartedAt.Add(time.Minute),
	); insertError != nil {
		t.Fatalf("insert empty-snapshot boundary fixtures: %v", insertError)
	}

	staleConcurrentItem := sourceSnapshotItem(
		models.SourceCourses,
		newerExternalID,
		"Stale overwrite",
		snapshotStartedAt.Add(-time.Hour),
	)
	upserted, deactivated, reconciliationError := catalogRepository.ReconcileSourceSnapshot(
		testContext,
		models.SourceCourses,
		[]*models.CatalogItem{staleConcurrentItem},
		1,
		&snapshotStartedAt,
		snapshotStartedAt,
	)
	if reconciliationError != nil {
		t.Fatalf("stale ReconcileSourceSnapshot returned error: %v", reconciliationError)
	}
	if upserted != 0 || deactivated != 1 {
		t.Fatalf("stale reconciliation = upserted %d, deactivated %d; want 0, 1", upserted, deactivated)
	}
	assertSourceCatalogItemState(t, testContext, databasePool, models.SourceCourses, oldExternalID, models.StatusInactive, true)
	assertSourceCatalogItemState(t, testContext, databasePool, models.SourceCourses, newerExternalID, models.StatusActive, false)
	var newerTitle string
	if readError := databasePool.QueryRow(testContext, `
		SELECT title FROM catalog_items WHERE source = 'courses' AND external_id = $1
	`, newerExternalID).Scan(&newerTitle); readError != nil {
		t.Fatalf("read concurrently written course title: %v", readError)
	}
	if newerTitle != "Newer concurrent course" {
		t.Fatalf("concurrently written course title = %q, want newer title", newerTitle)
	}
}

func TestSourceSyncLeaseSerializesSameSourceAndReleasesOnCancellation(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()
	catalogRepository := NewCatalogItemRepository(databasePool)

	firstLeaseAcquired := make(chan struct{})
	releaseFirstLease := make(chan struct{})
	firstLeaseCompleted := make(chan error, 1)
	go func() {
		changed, leaseError := catalogRepository.WithSourceSyncLease(
			testContext,
			models.SourceCourses,
			func(context.Context) (int, error) {
				close(firstLeaseAcquired)
				<-releaseFirstLease
				return 1, nil
			},
		)
		if leaseError == nil && changed != 1 {
			leaseError = fmt.Errorf("first lease changed = %d, want 1", changed)
		}
		firstLeaseCompleted <- leaseError
	}()
	select {
	case <-firstLeaseAcquired:
	case <-time.After(2 * time.Second):
		t.Fatal("first source lease was not acquired")
	}

	differentSourceCompleted := make(chan error, 1)
	go func() {
		_, leaseError := catalogRepository.WithSourceSyncLease(
			testContext,
			models.SourceJobs,
			func(context.Context) (int, error) { return 0, nil },
		)
		differentSourceCompleted <- leaseError
	}()
	select {
	case differentSourceError := <-differentSourceCompleted:
		if differentSourceError != nil {
			t.Fatalf("different-source lease failed: %v", differentSourceError)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("different source was blocked by an unrelated lease")
	}

	var canceledOperationCalled atomic.Bool
	waitingContext, cancelWaiting := context.WithTimeout(testContext, 100*time.Millisecond)
	_, waitingError := catalogRepository.WithSourceSyncLease(
		waitingContext,
		models.SourceCourses,
		func(context.Context) (int, error) {
			canceledOperationCalled.Store(true)
			return 0, nil
		},
	)
	cancelWaiting()
	if !errors.Is(waitingError, context.DeadlineExceeded) && !errors.Is(waitingError, context.Canceled) {
		t.Fatalf("canceled same-source lease error = %v, want context cancellation", waitingError)
	}
	if canceledOperationCalled.Load() {
		t.Fatal("operation ran without acquiring its same-source lease")
	}

	close(releaseFirstLease)
	select {
	case firstLeaseError := <-firstLeaseCompleted:
		if firstLeaseError != nil {
			t.Fatalf("first source lease failed: %v", firstLeaseError)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first source lease was not released")
	}

	changed, reacquireError := catalogRepository.WithSourceSyncLease(
		testContext,
		models.SourceCourses,
		func(context.Context) (int, error) { return 2, nil },
	)
	if reacquireError != nil || changed != 2 {
		t.Fatalf("reacquire after release = changed %d, error %v", changed, reacquireError)
	}
	operationError := errors.New("sync operation failed")
	if _, leaseError := catalogRepository.WithSourceSyncLease(
		testContext,
		models.SourceMEI,
		func(context.Context) (int, error) { return 0, operationError },
	); !errors.Is(leaseError, operationError) {
		t.Fatalf("operation failure through lease = %v, want sentinel", leaseError)
	}
	if _, leaseError := catalogRepository.WithSourceSyncLease(
		testContext,
		models.SourceMEI,
		func(context.Context) (int, error) { return 0, nil },
	); leaseError != nil {
		t.Fatalf("lease was not released after operation failure: %v", leaseError)
	}
}

func TestSalesForceCursorNeverRegresses(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()

	fixturePrefix := "salesforce-cursor-" + uuid.NewString()
	cleanupCatalogRepositoryFixtures(t, databasePool, fixturePrefix)
	catalogRepository := NewCatalogItemRepository(databasePool)
	objectType := salesForceTestObjectType("Cursor")
	cleanupSalesForceTestCursors(t, databasePool, objectType)
	newerCursor := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	olderCursor := newerCursor.Add(-time.Hour)

	if _, err := catalogRepository.UpsertSalesForceDelta(
		testContext,
		objectType,
		[]*models.CatalogItem{
			salesForceSnapshotItemAt(fixturePrefix+"-item", objectType, "Newest title", newerCursor),
		},
		newerCursor,
	); err != nil {
		t.Fatalf("write newer Salesforce cursor: %v", err)
	}
	if _, err := catalogRepository.UpsertSalesForceDelta(testContext, objectType, nil, olderCursor); err != nil {
		t.Fatalf("apply stale empty delta: %v", err)
	}

	cursor, err := catalogRepository.GetSalesForceCursor(testContext, objectType)
	if err != nil {
		t.Fatalf("read Salesforce cursor: %v", err)
	}
	if cursor.LastSyncAt == nil || !cursor.LastSyncAt.Equal(newerCursor) {
		t.Fatalf("cursor regressed to %v, want %s", cursor.LastSyncAt, newerCursor)
	}
}

func TestSalesForceUpsertRejectsOlderAndEqualVersions(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()

	fixturePrefix := "salesforce-version-" + uuid.NewString()
	cleanupCatalogRepositoryFixtures(t, databasePool, fixturePrefix)
	catalogRepository := NewCatalogItemRepository(databasePool)
	objectType := salesForceTestObjectType("Version")
	cleanupSalesForceTestCursors(t, databasePool, objectType)
	externalID := fixturePrefix + "-item"
	newerSourceUpdatedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

	if err := catalogRepository.Upsert(
		testContext,
		salesForceSnapshotItemAt(externalID, objectType, "Newest title", newerSourceUpdatedAt),
	); err != nil {
		t.Fatalf("insert newest Salesforce version: %v", err)
	}
	if err := catalogRepository.Upsert(
		testContext,
		salesForceSnapshotItemAt(externalID, objectType, "Older title", newerSourceUpdatedAt.Add(-time.Second)),
	); err != nil {
		t.Fatalf("apply older webhook version: %v", err)
	}
	equalVersion := salesForceSnapshotItemAt(externalID, objectType, "Equal-version title", newerSourceUpdatedAt)
	changed, err := catalogRepository.UpsertSalesForceDelta(
		testContext,
		objectType,
		[]*models.CatalogItem{equalVersion},
		newerSourceUpdatedAt,
	)
	if err != nil {
		t.Fatalf("apply equal delta version: %v", err)
	}
	if changed != 0 {
		t.Fatalf("equal Salesforce version reported %d changes, want 0", changed)
	}

	var title string
	var storedSourceUpdatedAt time.Time
	if err := databasePool.QueryRow(testContext, `
		SELECT title, source_updated_at
		FROM catalog_items
		WHERE source = 'salesforce' AND external_id = $1
	`, externalID).Scan(&title, &storedSourceUpdatedAt); err != nil {
		t.Fatalf("read protected Salesforce item: %v", err)
	}
	if title != "Newest title" || !storedSourceUpdatedAt.Equal(newerSourceUpdatedAt) {
		t.Fatalf("stored version = %q at %s, want newest at %s", title, storedSourceUpdatedAt, newerSourceUpdatedAt)
	}
}

func TestSalesForceTransactionsShareObjectScopedAdvisoryLock(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()

	catalogRepository := NewCatalogItemRepository(databasePool)
	objectType := salesForceTestObjectType("Lock")
	cleanupSalesForceTestCursors(t, databasePool, objectType)
	lockingTransaction, err := databasePool.Begin(testContext)
	if err != nil {
		t.Fatalf("begin locking transaction: %v", err)
	}
	defer lockingTransaction.Rollback(testContext)
	if err := lockSalesForceSync(testContext, lockingTransaction, objectType); err != nil {
		t.Fatalf("acquire Salesforce advisory lock: %v", err)
	}

	completed := make(chan error, 1)
	go func() {
		_, deltaError := catalogRepository.UpsertSalesForceDelta(
			testContext,
			objectType,
			nil,
			time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
		)
		completed <- deltaError
	}()
	select {
	case deltaError := <-completed:
		t.Fatalf("delta transaction bypassed object advisory lock: %v", deltaError)
	case <-time.After(100 * time.Millisecond):
	}
	if err := lockingTransaction.Commit(testContext); err != nil {
		t.Fatalf("release Salesforce advisory lock: %v", err)
	}
	select {
	case deltaError := <-completed:
		if deltaError != nil {
			t.Fatalf("delta transaction failed after lock release: %v", deltaError)
		}
	case <-time.After(time.Second):
		t.Fatal("delta transaction did not continue after advisory lock release")
	}
}

func openCatalogRepositoryTestDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv(catalogRepositoryTestDatabaseURLVariable)
	if databaseURL == "" {
		t.Skip(catalogRepositoryTestDatabaseURLVariable + " is not configured")
	}
	poolConfiguration, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse catalog repository test database configuration: %v", err)
	}
	if !strings.Contains(strings.ToLower(poolConfiguration.ConnConfig.Database), "test") {
		t.Fatalf("catalog repository integration tests require a database whose name contains test")
	}
	databasePool, err := pgxpool.NewWithConfig(context.Background(), poolConfiguration)
	if err != nil {
		t.Fatalf("open catalog repository test database: %v", err)
	}
	t.Cleanup(databasePool.Close)
	if err := databasePool.Ping(context.Background()); err != nil {
		t.Fatalf("ping catalog repository test database: %v", err)
	}
	return databasePool
}

func cleanupCatalogRepositoryFixtures(t *testing.T, databasePool *pgxpool.Pool, fixturePrefix string) {
	t.Helper()
	t.Cleanup(func() {
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		if _, err := databasePool.Exec(cleanupContext, `
			DELETE FROM catalog_items WHERE external_id LIKE $1
		`, fixturePrefix+"%"); err != nil {
			t.Errorf("cleanup catalog repository fixtures: %v", err)
		}
	})
}

func cleanupSalesForceTestCursors(t *testing.T, databasePool *pgxpool.Pool, objectTypes ...string) {
	t.Helper()
	t.Cleanup(func() {
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		if _, err := databasePool.Exec(cleanupContext, `
			DELETE FROM salesforce_sync_cursor WHERE object_type = ANY($1::text[])
		`, objectTypes); err != nil {
			t.Errorf("cleanup salesforce test cursors: %v", err)
		}
	})
}

func salesForceTestObjectType(prefix string) string {
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "") + "__c"
}

func salesForceSnapshotItem(externalID string, objectType string, title string) *models.CatalogItem {
	return salesForceSnapshotItemAt(
		externalID,
		objectType,
		title,
		time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC),
	)
}

func salesForceSnapshotItemAt(
	externalID string,
	objectType string,
	title string,
	sourceUpdatedAt time.Time,
) *models.CatalogItem {
	return &models.CatalogItem{
		ExternalID:      externalID,
		Source:          models.SourceSalesForce,
		Type:            models.TypeService,
		Title:           title,
		Status:          models.StatusActive,
		SourceData:      []byte(`{"` + models.SalesForceObjectTypeSourceDataKey + `":"` + objectType + `"}`),
		SourceUpdatedAt: &sourceUpdatedAt,
	}
}

func sourceSnapshotItem(
	source models.ItemSource,
	externalID string,
	title string,
	sourceUpdatedAt time.Time,
) *models.CatalogItem {
	itemType, supportedSource := snapshotItemType(source)
	if !supportedSource {
		panic("unsupported source snapshot test fixture: " + string(source))
	}
	return &models.CatalogItem{
		ExternalID:      externalID,
		Source:          source,
		Type:            itemType,
		Title:           title,
		Status:          models.StatusActive,
		SourceData:      json.RawMessage(`{"fixture":true}`),
		SourceUpdatedAt: &sourceUpdatedAt,
	}
}

func assertNullableCatalogFieldsAreEmpty(t *testing.T, item *models.CatalogItem) {
	t.Helper()
	if item.Description != "" || item.ShortDesc != "" || item.Organization != "" || item.URL != "" || item.ImageURL != "" || item.Modalidade != "" {
		t.Fatalf("nullable fields were not normalized: %#v", item)
	}
}

func containsCatalogExternalID(items []*models.CatalogItem, externalID string) bool {
	for _, item := range items {
		if item.ExternalID == externalID {
			return true
		}
	}
	return false
}

func assertCatalogItemState(
	t *testing.T,
	testContext context.Context,
	databasePool *pgxpool.Pool,
	externalID string,
	wantStatus models.ItemStatus,
	wantDeleted bool,
) {
	t.Helper()
	var status string
	var deleted bool
	if err := databasePool.QueryRow(testContext, `
		SELECT status, deleted_at IS NOT NULL
		FROM catalog_items
		WHERE source = 'salesforce' AND external_id = $1
	`, externalID).Scan(&status, &deleted); err != nil {
		t.Fatalf("read state for %q: %v", externalID, err)
	}
	if models.ItemStatus(status) != wantStatus || deleted != wantDeleted {
		t.Fatalf("state for %q = status %q, deleted %t; want %q, %t", externalID, status, deleted, wantStatus, wantDeleted)
	}
}

func assertSourceCatalogItemState(
	t *testing.T,
	testContext context.Context,
	databasePool *pgxpool.Pool,
	source models.ItemSource,
	externalID string,
	wantStatus models.ItemStatus,
	wantDeleted bool,
) {
	t.Helper()
	var status string
	var deleted bool
	if readError := databasePool.QueryRow(testContext, `
		SELECT status, deleted_at IS NOT NULL
		FROM catalog_items
		WHERE source = $1 AND external_id = $2
	`, string(source), externalID).Scan(&status, &deleted); readError != nil {
		t.Fatalf("read %q state for %q: %v", source, externalID, readError)
	}
	if models.ItemStatus(status) != wantStatus || deleted != wantDeleted {
		t.Fatalf(
			"%q state for %q = status %q, deleted %t; want %q, %t",
			source,
			externalID,
			status,
			deleted,
			wantStatus,
			wantDeleted,
		)
	}
}
