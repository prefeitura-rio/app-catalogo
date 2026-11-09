package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestSyncEventMetadata_DefaultsToEmptyObject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   json.RawMessage
		want string
	}{
		{"nil", nil, "{}"},
		{"empty", json.RawMessage{}, "{}"},
		{"kept", json.RawMessage(`{"courses":3}`), `{"courses":3}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := string(syncEventMetadata(tt.in)); got != tt.want {
				t.Fatalf("syncEventMetadata(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Integração: um evento sem metadata (como o das fontes app-go-api, typesense e
// salesforce) precisa ser gravado em sync_events (metadata JSONB NOT NULL) e
// aparecer em GetLastSyncEvents, que alimenta GET /api/v1/admin/sync/status.
func TestRecordSyncEvent_WithoutMetadataShowsInLastSyncEvents(t *testing.T) {
	pool := testCatalogPool(t)
	repo := NewCatalogItemRepository(pool)
	ctx := context.Background()

	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	id, err := repo.RecordSyncEvent(ctx, &models.SyncEvent{
		Source:    models.SourceAppGoAPI,
		EventType: models.SyncTypeFullSync,
		Status:    models.SyncStatusStarted,
		StartedAt: startedAt,
	})
	if err != nil {
		t.Fatalf("RecordSyncEvent sem metadata: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM sync_events WHERE id = $1`, id)
	})

	if err := repo.UpdateSyncEvent(ctx, id, models.SyncStatusCompleted, 7, 2, "", 1234); err != nil {
		t.Fatalf("UpdateSyncEvent: %v", err)
	}

	var metadata string
	if err := pool.QueryRow(ctx, `SELECT metadata::text FROM sync_events WHERE id = $1`, id).Scan(&metadata); err != nil {
		t.Fatalf("select metadata: %v", err)
	}
	if metadata != "{}" {
		t.Fatalf("metadata=%q want {}", metadata)
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
