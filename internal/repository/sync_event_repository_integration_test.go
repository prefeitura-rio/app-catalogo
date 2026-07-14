package repository

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestCompletedSyncMetadataIgnoresNewerFailedEvent(t *testing.T) {
	databasePool := openCatalogRepositoryTestDatabase(t)
	testContext, cancelTest := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelTest()

	catalogRepository := NewCatalogItemRepository(databasePool)
	completedEventID, recordCompletedError := catalogRepository.RecordSyncEvent(testContext, &models.SyncEvent{
		Source:    models.SourceTypesense,
		EventType: models.SyncTypeDeltaSync,
		Status:    models.SyncStatusStarted,
		StartedAt: time.Now().Add(-time.Minute),
	})
	if recordCompletedError != nil {
		t.Fatalf("record completed fixture: %v", recordCompletedError)
	}
	failedEventID, recordFailedError := catalogRepository.RecordSyncEvent(testContext, &models.SyncEvent{
		Source:    models.SourceTypesense,
		EventType: models.SyncTypeDeltaSync,
		Status:    models.SyncStatusStarted,
		StartedAt: time.Now(),
	})
	if recordFailedError != nil {
		t.Fatalf("record failed fixture: %v", recordFailedError)
	}
	t.Cleanup(func() {
		_, _ = databasePool.Exec(context.Background(), `DELETE FROM sync_events WHERE id = ANY($1::bigint[])`, []int64{completedEventID, failedEventID})
	})

	wantMetadata := json.RawMessage(`{"cursor_version":1,"cursor_unix":1783773000}`)
	if updateError := catalogRepository.UpdateSyncEventWithMetadata(
		testContext,
		completedEventID,
		models.SyncStatusCompleted,
		3,
		0,
		"",
		25,
		wantMetadata,
	); updateError != nil {
		t.Fatalf("complete fixture event: %v", updateError)
	}
	if updateError := catalogRepository.UpdateSyncEvent(
		testContext,
		failedEventID,
		models.SyncStatusFailed,
		1,
		1,
		"stream interrupted",
		10,
	); updateError != nil {
		t.Fatalf("fail fixture event: %v", updateError)
	}

	metadata, found, lookupError := catalogRepository.GetLastCompletedSyncMetadata(testContext, models.SourceTypesense)
	if lookupError != nil {
		t.Fatalf("GetLastCompletedSyncMetadata returned error: %v", lookupError)
	}
	if !found {
		t.Fatal("GetLastCompletedSyncMetadata did not find completed event")
	}
	var metadataValue any
	if unmarshalError := json.Unmarshal(metadata, &metadataValue); unmarshalError != nil {
		t.Fatalf("metadata is invalid JSON: %v", unmarshalError)
	}
	var wantMetadataValue any
	if unmarshalError := json.Unmarshal(wantMetadata, &wantMetadataValue); unmarshalError != nil {
		t.Fatalf("wantMetadata fixture is invalid JSON: %v", unmarshalError)
	}
	if !reflect.DeepEqual(metadataValue, wantMetadataValue) {
		t.Fatalf("metadata = %s, want %s", metadata, wantMetadata)
	}
}
