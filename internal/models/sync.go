package models

import (
	"encoding/json"
	"time"
)

type SyncEventStatus string
type SyncEventType string

const (
	SyncStatusStarted   SyncEventStatus = "started"
	SyncStatusCompleted SyncEventStatus = "completed"
	SyncStatusFailed    SyncEventStatus = "failed"

	SyncTypeFullSync   SyncEventType = "full_sync"
	SyncTypeDeltaSync  SyncEventType = "delta_sync"
	SyncTypeWebhook    SyncEventType = "webhook"
)

type SyncEvent struct {
	ID             int64           `json:"id"`
	Source         ItemSource      `json:"source"`
	EventType      SyncEventType   `json:"event_type"`
	Status         SyncEventStatus `json:"status"`
	ItemsProcessed int             `json:"items_processed"`
	ItemsFailed    int             `json:"items_failed"`
	ErrorMessage   string          `json:"error_message,omitempty"`
	DurationMs     int             `json:"duration_ms,omitempty"`
	StartedAt      time.Time       `json:"started_at"`
	CompletedAt    *time.Time      `json:"completed_at,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty" swaggertype:"object"`
}

type SalesForceSyncCursor struct {
	ID             int        `json:"id"`
	ObjectType     string     `json:"object_type"`
	LastSyncAt     *time.Time `json:"last_sync_at,omitempty"`
	LastDeltaToken string     `json:"last_delta_token,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type SyncStatus struct {
	Source         ItemSource      `json:"source"`
	LastEventType  SyncEventType   `json:"last_event_type"`
	LastStatus     SyncEventStatus `json:"last_status"`
	LastStartedAt  time.Time       `json:"last_started_at"`
	LastCompletedAt *time.Time     `json:"last_completed_at,omitempty"`
	ItemsProcessed int             `json:"items_processed"`
	ItemsFailed    int             `json:"items_failed"`
	ErrorMessage   string          `json:"error_message,omitempty"`
}
