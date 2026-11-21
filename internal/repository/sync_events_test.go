package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

// Integração: o evento de sync da fonte app-go-api, como AppGoAPIDataSource.Sync o
// grava, precisa entrar em sync_events e aparecer em GetLastSyncEvents, que
// alimenta GET /api/v1/admin/sync/status.
func TestRecordSyncEvent_AppGoAPIEventShowsInLastSyncEvents(t *testing.T) {
	pool := testCatalogPool(t)
	repo := NewCatalogItemRepository(pool)
	ctx := context.Background()

	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	id, err := repo.RecordSyncEvent(ctx, &models.SyncEvent{
		Source:    models.SourceAppGoAPI,
		EventType: models.SyncTypeFullSync,
		Status:    models.SyncStatusStarted,
		StartedAt: startedAt,
		Metadata:  json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("RecordSyncEvent(app-go-api): %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM sync_events WHERE id = $1`, id)
	})

	if err := repo.UpdateSyncEvent(ctx, id, models.SyncStatusCompleted, 7, 2, "", 1234); err != nil {
		t.Fatalf("UpdateSyncEvent: %v", err)
	}

	statuses, err := repo.GetLastSyncEvents(ctx)
	if err != nil {
		t.Fatalf("GetLastSyncEvents: %v", err)
	}
	var got *models.SyncStatus
	for _, s := range statuses {
		if s.Source == models.SourceAppGoAPI {
			got = s
		}
	}
	if got == nil {
		t.Fatalf("GetLastSyncEvents sem a fonte app-go-api: %+v", statuses)
	}
	if got.LastStatus != models.SyncStatusCompleted || got.LastEventType != models.SyncTypeFullSync {
		t.Fatalf("status=%q type=%q want completed/full_sync", got.LastStatus, got.LastEventType)
	}
	if got.ItemsProcessed != 7 || got.ItemsFailed != 2 {
		t.Fatalf("processed=%d failed=%d want 7/2", got.ItemsProcessed, got.ItemsFailed)
	}
	if !got.LastStartedAt.Equal(startedAt) {
		t.Fatalf("started_at=%v want %v", got.LastStartedAt, startedAt)
	}
	if got.LastCompletedAt == nil {
		t.Fatal("completed_at must be set after UpdateSyncEvent")
	}
}

// Integração: o evento do SalesForce sem metadata (nil, como json.RawMessage zero)
// precisa ser gravado — a coluna é JSONB NOT NULL e o repositório grava {} — e
// aparecer em GetLastSyncEvents, que alimenta GET /api/v1/admin/sync/status.
func TestRecordSyncEvent_NilMetadataSalesForceEventShowsInLastSyncEvents(t *testing.T) {
	pool := testCatalogPool(t)
	repo := NewCatalogItemRepository(pool)
	ctx := context.Background()

	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	id, err := repo.RecordSyncEvent(ctx, &models.SyncEvent{
		Source:    models.SourceSalesForce,
		EventType: models.SyncTypeDeltaSync,
		Status:    models.SyncStatusStarted,
		StartedAt: startedAt,
	})
	if err != nil {
		t.Fatalf("RecordSyncEvent(salesforce, metadata nil): %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM sync_events WHERE id = $1`, id)
	})

	var metadata string
	if err := pool.QueryRow(ctx, `SELECT metadata::text FROM sync_events WHERE id = $1`, id).Scan(&metadata); err != nil {
		t.Fatalf("ler metadata: %v", err)
	}
	if metadata != "{}" {
		t.Fatalf("metadata=%q want {}", metadata)
	}

	if err := repo.UpdateSyncEvent(ctx, id, models.SyncStatusCompleted, 3, 0, "", 50); err != nil {
		t.Fatalf("UpdateSyncEvent: %v", err)
	}

	statuses, err := repo.GetLastSyncEvents(ctx)
	if err != nil {
		t.Fatalf("GetLastSyncEvents: %v", err)
	}
	var got *models.SyncStatus
	for _, s := range statuses {
		if s.Source == models.SourceSalesForce {
			got = s
		}
	}
	if got == nil {
		t.Fatalf("GetLastSyncEvents sem a fonte salesforce: %+v", statuses)
	}
	if !got.LastStartedAt.Equal(startedAt) {
		t.Fatalf("started_at=%v want %v (outro evento salesforce mais novo no banco?)", got.LastStartedAt, startedAt)
	}
	if got.LastStatus != models.SyncStatusCompleted || got.LastEventType != models.SyncTypeDeltaSync {
		t.Fatalf("status=%q type=%q want completed/delta_sync", got.LastStatus, got.LastEventType)
	}
	if got.ItemsProcessed != 3 || got.ItemsFailed != 0 {
		t.Fatalf("processed=%d failed=%d want 3/0", got.ItemsProcessed, got.ItemsFailed)
	}
}
