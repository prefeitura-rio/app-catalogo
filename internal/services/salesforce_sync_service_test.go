package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

type salesForceSyncClientStub struct {
	allRecords      []map[string]interface{}
	modifiedRecords []map[string]interface{}
	queryRecords    []map[string]interface{}
	queryAllError   error
	queryCalls      int
	modifiedSince   time.Time
}

func (client *salesForceSyncClientStub) Query(context.Context, string) ([]map[string]interface{}, error) {
	client.queryCalls++
	return client.queryRecords, nil
}

func (client *salesForceSyncClientStub) QueryAll(context.Context, string) ([]map[string]interface{}, error) {
	client.queryCalls++
	return client.allRecords, client.queryAllError
}

func (client *salesForceSyncClientStub) QueryModifiedSince(_ context.Context, _ string, since time.Time) ([]map[string]interface{}, error) {
	client.queryCalls++
	client.modifiedSince = since
	return client.modifiedRecords, nil
}

type syncEventUpdate struct {
	status    models.SyncEventStatus
	processed int
	failed    int
}

type salesForceSyncRepositoryStub struct {
	cursor               *models.SalesForceSyncCursor
	cursorError          error
	reconcileUpserted    int
	reconcileDeactivated int
	reconcileCalls       int
	deltaCalls           int
	reconciledItems      []*models.CatalogItem
	deltaItems           []*models.CatalogItem
	reconcileCursor      time.Time
	deltaCursor          time.Time
	eventUpdates         []syncEventUpdate
	softDeleteCalls      int
	upsertCalls          int
}

func (repositoryStub *salesForceSyncRepositoryStub) GetSalesForceCursor(context.Context, string) (*models.SalesForceSyncCursor, error) {
	return repositoryStub.cursor, repositoryStub.cursorError
}

func (repositoryStub *salesForceSyncRepositoryStub) RecordSyncEvent(context.Context, *models.SyncEvent) (int64, error) {
	return 1, nil
}

func (repositoryStub *salesForceSyncRepositoryStub) UpdateSyncEvent(
	_ context.Context,
	_ int64,
	status models.SyncEventStatus,
	processed int,
	failed int,
	_ string,
	_ int,
) error {
	repositoryStub.eventUpdates = append(repositoryStub.eventUpdates, syncEventUpdate{
		status:    status,
		processed: processed,
		failed:    failed,
	})
	return nil
}

func (repositoryStub *salesForceSyncRepositoryStub) ReconcileSalesForceSnapshot(
	_ context.Context,
	_ string,
	items []*models.CatalogItem,
	lastSyncAt time.Time,
) (int, int, error) {
	repositoryStub.reconcileCalls++
	repositoryStub.reconciledItems = items
	repositoryStub.reconcileCursor = lastSyncAt
	return repositoryStub.reconcileUpserted, repositoryStub.reconcileDeactivated, nil
}

func (repositoryStub *salesForceSyncRepositoryStub) UpsertSalesForceDelta(
	_ context.Context,
	_ string,
	items []*models.CatalogItem,
	lastSyncAt time.Time,
) (int, error) {
	repositoryStub.deltaCalls++
	repositoryStub.deltaItems = items
	repositoryStub.deltaCursor = lastSyncAt
	return len(items), nil
}

func (repositoryStub *salesForceSyncRepositoryStub) SoftDelete(context.Context, models.ItemSource, string) error {
	repositoryStub.softDeleteCalls++
	return nil
}

func (repositoryStub *salesForceSyncRepositoryStub) Upsert(context.Context, *models.CatalogItem) error {
	repositoryStub.upsertCalls++
	return nil
}

func TestSalesForceFullSyncRejectsEmptySnapshotBeforeReconciliation(t *testing.T) {
	client := &salesForceSyncClientStub{}
	repositoryStub := &salesForceSyncRepositoryStub{}
	service := NewSalesForceSyncService(client, repositoryStub, "Service__c")

	changed, err := service.FullSync(context.Background())

	if !errors.Is(err, repository.ErrEmptySalesForceSnapshot) {
		t.Fatalf("FullSync error = %v, want empty snapshot error", err)
	}
	if changed != 0 || repositoryStub.reconcileCalls != 0 {
		t.Fatalf("empty snapshot mutated repository: changed=%d reconciliations=%d", changed, repositoryStub.reconcileCalls)
	}
	assertLastSalesForceSyncEvent(t, repositoryStub, models.SyncStatusFailed, 0, 0)
}

func TestSalesForceFullSyncRejectsPartiallyInvalidSnapshotBeforeReconciliation(t *testing.T) {
	client := &salesForceSyncClientStub{allRecords: []map[string]interface{}{
		{"Id": "valid-id", "Name": "Valid service"},
		{"Id": "invalid-id"},
	}}
	repositoryStub := &salesForceSyncRepositoryStub{}
	service := NewSalesForceSyncService(client, repositoryStub, "Service__c")

	changed, err := service.FullSync(context.Background())

	if err == nil {
		t.Fatal("FullSync accepted a partially invalid snapshot")
	}
	if changed != 0 || repositoryStub.reconcileCalls != 0 {
		t.Fatalf("partial snapshot mutated repository: changed=%d reconciliations=%d", changed, repositoryStub.reconcileCalls)
	}
	assertLastSalesForceSyncEvent(t, repositoryStub, models.SyncStatusFailed, 0, 2)
}

func TestSalesForceFullSyncAddsScopeAndCountsDeactivations(t *testing.T) {
	client := &salesForceSyncClientStub{allRecords: []map[string]interface{}{
		{
			"Id":               "service-id",
			"Name":             "Scoped service",
			"Tags__c":          "health, citizen",
			"Theme__c":         "rights",
			"LastModifiedDate": "2001-02-03T04:05:06.789Z",
			"attributes":       map[string]interface{}{"type": "Service__c"},
		},
	}}
	repositoryStub := &salesForceSyncRepositoryStub{
		reconcileUpserted:    1,
		reconcileDeactivated: 2,
	}
	service := NewSalesForceSyncService(client, repositoryStub, "Service__c")

	changed, err := service.FullSync(context.Background())

	if err != nil {
		t.Fatalf("FullSync returned error: %v", err)
	}
	if changed != 3 {
		t.Fatalf("FullSync changed = %d, want 3", changed)
	}
	if len(repositoryStub.reconciledItems) != 1 {
		t.Fatalf("reconciled item count = %d, want 1", len(repositoryStub.reconciledItems))
	}
	var sourceData map[string]interface{}
	if err := json.Unmarshal(repositoryStub.reconciledItems[0].SourceData, &sourceData); err != nil {
		t.Fatalf("decode source data: %v", err)
	}
	if sourceData[models.SalesForceObjectTypeSourceDataKey] != "Service__c" {
		t.Fatalf("source data scope = %#v, want Service__c", sourceData[models.SalesForceObjectTypeSourceDataKey])
	}
	if len(repositoryStub.reconciledItems[0].Tags) != 3 {
		t.Fatalf("mapped tags = %#v, want source tags plus theme", repositoryStub.reconciledItems[0].Tags)
	}
	wantCursor := time.Date(2001, time.February, 3, 4, 5, 6, 789000000, time.UTC)
	if !repositoryStub.reconcileCursor.Equal(wantCursor) {
		t.Fatalf("full-sync cursor = %s, want upstream timestamp %s", repositoryStub.reconcileCursor, wantCursor)
	}
	assertLastSalesForceSyncEvent(t, repositoryStub, models.SyncStatusCompleted, 3, 0)
}

func TestSalesForceDeltaKeepsUpstreamCursorForEmptyResult(t *testing.T) {
	lastSyncAt := time.Date(2000, time.January, 2, 3, 4, 5, 0, time.UTC)
	client := &salesForceSyncClientStub{}
	repositoryStub := &salesForceSyncRepositoryStub{
		cursor: &models.SalesForceSyncCursor{LastSyncAt: &lastSyncAt},
	}
	service := NewSalesForceSyncService(client, repositoryStub, "Service__c")

	changed, err := service.DeltaSync(context.Background())

	if err != nil {
		t.Fatalf("DeltaSync returned error: %v", err)
	}
	if changed != 0 || repositoryStub.deltaCalls != 1 || len(repositoryStub.deltaItems) != 0 {
		t.Fatalf("empty delta was not committed: changed=%d delta_calls=%d items=%d", changed, repositoryStub.deltaCalls, len(repositoryStub.deltaItems))
	}
	if !client.modifiedSince.Equal(lastSyncAt.Add(-salesForceDeltaOverlap)) {
		t.Fatalf("empty delta queried since %s, want overlap start %s", client.modifiedSince, lastSyncAt.Add(-salesForceDeltaOverlap))
	}
	if !repositoryStub.deltaCursor.Equal(lastSyncAt) {
		t.Fatalf("empty delta cursor = %s, want unchanged upstream cursor %s", repositoryStub.deltaCursor, lastSyncAt)
	}
	assertLastSalesForceSyncEvent(t, repositoryStub, models.SyncStatusCompleted, 0, 0)
}

func TestSalesForceDeltaUsesOverlapAndGreatestUpstreamTimestamp(t *testing.T) {
	lastSyncAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	newerTimestamp := lastSyncAt.Add(12 * time.Second)
	client := &salesForceSyncClientStub{modifiedRecords: []map[string]interface{}{
		{
			"Id":               "overlap-record",
			"Name":             "Overlap record",
			"LastModifiedDate": lastSyncAt.Add(-30 * time.Second).Format(time.RFC3339Nano),
		},
		{
			"Id":               "new-record",
			"Name":             "New record",
			"LastModifiedDate": newerTimestamp.Format(time.RFC3339Nano),
		},
	}}
	repositoryStub := &salesForceSyncRepositoryStub{
		cursor: &models.SalesForceSyncCursor{LastSyncAt: &lastSyncAt},
	}
	service := NewSalesForceSyncService(client, repositoryStub, "Service__c")

	changed, err := service.DeltaSync(context.Background())

	if err != nil {
		t.Fatalf("DeltaSync returned error: %v", err)
	}
	if changed != 2 {
		t.Fatalf("DeltaSync changed = %d, want 2", changed)
	}
	if !client.modifiedSince.Equal(lastSyncAt.Add(-salesForceDeltaOverlap)) {
		t.Fatalf("delta queried since %s, want %s", client.modifiedSince, lastSyncAt.Add(-salesForceDeltaOverlap))
	}
	if !repositoryStub.deltaCursor.Equal(newerTimestamp) {
		t.Fatalf("delta cursor = %s, want greatest upstream timestamp %s", repositoryStub.deltaCursor, newerTimestamp)
	}
}

func TestSalesForceSyncRejectsMissingOrInvalidLastModifiedDateBeforeMutation(t *testing.T) {
	lastSyncAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, testCase := range []struct {
		name             string
		lastModifiedDate interface{}
	}{
		{name: "missing"},
		{name: "invalid", lastModifiedDate: "not-a-timestamp"},
		{name: "wrong type", lastModifiedDate: 12345},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			record := map[string]interface{}{"Id": "service-id", "Name": "Service"}
			if testCase.lastModifiedDate != nil {
				record["LastModifiedDate"] = testCase.lastModifiedDate
			}
			client := &salesForceSyncClientStub{modifiedRecords: []map[string]interface{}{record}}
			repositoryStub := &salesForceSyncRepositoryStub{
				cursor: &models.SalesForceSyncCursor{LastSyncAt: &lastSyncAt},
			}
			service := NewSalesForceSyncService(client, repositoryStub, "Service__c")

			if _, err := service.DeltaSync(context.Background()); err == nil {
				t.Fatal("DeltaSync accepted invalid LastModifiedDate")
			}
			if repositoryStub.deltaCalls != 0 || repositoryStub.reconcileCalls != 0 {
				t.Fatalf("invalid LastModifiedDate mutated repository: delta=%d full=%d", repositoryStub.deltaCalls, repositoryStub.reconcileCalls)
			}
		})
	}
}

func TestSalesForceDeltaDoesNotTreatCursorFailureAsMissingCursor(t *testing.T) {
	client := &salesForceSyncClientStub{}
	repositoryStub := &salesForceSyncRepositoryStub{cursorError: errors.New("database unavailable")}
	service := NewSalesForceSyncService(client, repositoryStub, "Service__c")

	_, err := service.DeltaSync(context.Background())

	if err == nil {
		t.Fatal("DeltaSync treated cursor failure as a missing cursor")
	}
	if client.queryCalls != 0 || repositoryStub.reconcileCalls != 0 {
		t.Fatalf("cursor failure triggered external query or reconciliation: queries=%d reconciliations=%d", client.queryCalls, repositoryStub.reconcileCalls)
	}
}

func TestSalesForceDeltaFallsBackToFullSyncOnlyWhenCursorIsMissing(t *testing.T) {
	client := &salesForceSyncClientStub{allRecords: []map[string]interface{}{{
		"Id":               "service-id",
		"Name":             "Service",
		"LastModifiedDate": "2026-01-02T03:04:05Z",
	}}}
	repositoryStub := &salesForceSyncRepositoryStub{cursorError: pgx.ErrNoRows}
	service := NewSalesForceSyncService(client, repositoryStub, "Service__c")

	_, err := service.DeltaSync(context.Background())

	if err != nil {
		t.Fatalf("DeltaSync missing-cursor fallback returned error: %v", err)
	}
	if repositoryStub.reconcileCalls != 1 {
		t.Fatalf("missing cursor reconciliations = %d, want 1", repositoryStub.reconcileCalls)
	}
}

func TestValidatedSalesForceObjectType(t *testing.T) {
	testCases := []struct {
		name       string
		objectType string
		wantError  bool
	}{
		{name: "custom object", objectType: "Service__c"},
		{name: "namespaced object", objectType: "rio__Service__c"},
		{name: "trimmed", objectType: " Service__c "},
		{name: "empty", objectType: "", wantError: true},
		{name: "query injection", objectType: "Service__c WHERE Name != ''", wantError: true},
		{name: "invalid prefix", objectType: "1Service", wantError: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := validatedSalesForceObjectType(testCase.objectType)
			if (err != nil) != testCase.wantError {
				t.Fatalf("validatedSalesForceObjectType(%q) error = %v, wantError=%t", testCase.objectType, err, testCase.wantError)
			}
		})
	}
}

func TestSalesForceSyncRecordRejectsInvalidIDBeforeAnySideEffect(t *testing.T) {
	testCases := []struct {
		name       string
		externalID string
	}{
		{name: "empty", externalID: ""},
		{name: "short", externalID: strings.Repeat("A", shortSalesForceRecordIDLength-1)},
		{name: "unsupported length", externalID: strings.Repeat("A", shortSalesForceRecordIDLength+1)},
		{name: "long", externalID: strings.Repeat("A", longSalesForceRecordIDLength+1)},
		{name: "injection", externalID: "001A0000009zGVP' OR Name != ''"},
		{name: "unicode with accepted byte length", externalID: "001" + strings.Repeat("A", 10) + "é"},
		{name: "whitespace", externalID: "001A0000009zGV "},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			client := &salesForceSyncClientStub{}
			repositoryStub := &salesForceSyncRepositoryStub{}
			service := NewSalesForceSyncService(client, repositoryStub, "Service__c")

			err := service.SyncRecord(context.Background(), testCase.externalID)

			if err == nil {
				t.Fatalf("SyncRecord accepted invalid id %q", testCase.externalID)
			}
			if client.queryCalls != 0 || repositoryStub.softDeleteCalls != 0 || repositoryStub.upsertCalls != 0 {
				t.Fatalf(
					"invalid id caused side effects: queries=%d soft_deletes=%d upserts=%d",
					client.queryCalls,
					repositoryStub.softDeleteCalls,
					repositoryStub.upsertCalls,
				)
			}
		})
	}
}

func TestValidatedSalesForceRecordIDAcceptsCanonicalLengths(t *testing.T) {
	validIDs := []string{
		"001" + strings.Repeat("A", shortSalesForceRecordIDLength-3),
		"001" + strings.Repeat("a", longSalesForceRecordIDLength-3),
	}
	for _, externalID := range validIDs {
		validatedID, err := validatedSalesForceRecordID(externalID)
		if err != nil {
			t.Fatalf("validatedSalesForceRecordID(%q) returned error: %v", externalID, err)
		}
		if validatedID != externalID {
			t.Fatalf("validatedSalesForceRecordID(%q) = %q", externalID, validatedID)
		}
	}
}

func assertLastSalesForceSyncEvent(
	t *testing.T,
	repositoryStub *salesForceSyncRepositoryStub,
	wantStatus models.SyncEventStatus,
	wantProcessed int,
	wantFailed int,
) {
	t.Helper()
	if len(repositoryStub.eventUpdates) == 0 {
		t.Fatal("sync event was not updated")
	}
	lastUpdate := repositoryStub.eventUpdates[len(repositoryStub.eventUpdates)-1]
	if lastUpdate.status != wantStatus || lastUpdate.processed != wantProcessed || lastUpdate.failed != wantFailed {
		t.Fatalf(
			"sync event = status %q, processed %d, failed %d; want %q, %d, %d",
			lastUpdate.status,
			lastUpdate.processed,
			lastUpdate.failed,
			wantStatus,
			wantProcessed,
			wantFailed,
		)
	}
}
