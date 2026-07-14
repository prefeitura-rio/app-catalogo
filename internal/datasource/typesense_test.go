package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

type typesenseExporterStub struct {
	services       []clients.TypesenseService
	exportError    error
	receivedCursor time.Time
	exportCalls    int
}

func (exporter *typesenseExporterStub) ExportSince(
	_ context.Context,
	since time.Time,
	process func(clients.TypesenseService) error,
) error {
	exporter.exportCalls++
	exporter.receivedCursor = since
	for _, service := range exporter.services {
		if processError := process(service); processError != nil {
			return processError
		}
	}
	return exporter.exportError
}

type typesenseSyncRepositoryStub struct {
	completedMetadata      json.RawMessage
	completedMetadataFound bool
	completedMetadataError error
	recordEventError       error
	updateEventError       error
	upsertError            error
	reconcileError         error
	leaseError             error
	changed                int
	deactivated            int
	upsertedItems          []*models.CatalogItem
	reconciledItems        []*models.CatalogItem
	reconciledSource       models.ItemSource
	reconciledExpected     int
	reconciledUpperBound   *time.Time
	reconciledStartedAt    time.Time
	leasedSources          []models.ItemSource
	finalStatus            models.SyncEventStatus
	finalProcessed         int
	finalFailed            int
	finalMetadata          json.RawMessage
}

func (repository *typesenseSyncRepositoryStub) WithSourceSyncLease(
	ctx context.Context,
	source models.ItemSource,
	operation func(context.Context) (int, error),
) (int, error) {
	repository.leasedSources = append(repository.leasedSources, source)
	if repository.leaseError != nil {
		return 0, repository.leaseError
	}
	return operation(ctx)
}

func (repository *typesenseSyncRepositoryStub) RecordSyncEvent(
	_ context.Context,
	_ *models.SyncEvent,
) (int64, error) {
	return 42, repository.recordEventError
}

func (repository *typesenseSyncRepositoryStub) UpdateSyncEvent(
	_ context.Context,
	_ int64,
	status models.SyncEventStatus,
	processed int,
	failed int,
	_ string,
	_ int,
) error {
	repository.finalStatus = status
	repository.finalProcessed = processed
	repository.finalFailed = failed
	return repository.updateEventError
}

func (repository *typesenseSyncRepositoryStub) UpdateSyncEventWithMetadata(
	_ context.Context,
	_ int64,
	status models.SyncEventStatus,
	processed int,
	failed int,
	_ string,
	_ int,
	metadata json.RawMessage,
) error {
	repository.finalStatus = status
	repository.finalProcessed = processed
	repository.finalFailed = failed
	repository.finalMetadata = append(json.RawMessage(nil), metadata...)
	return repository.updateEventError
}

func (repository *typesenseSyncRepositoryStub) GetLastCompletedSyncMetadata(
	_ context.Context,
	_ models.ItemSource,
) (json.RawMessage, bool, error) {
	return repository.completedMetadata, repository.completedMetadataFound, repository.completedMetadataError
}

func (repository *typesenseSyncRepositoryStub) UpsertBatch(
	_ context.Context,
	items []*models.CatalogItem,
) (int, error) {
	repository.upsertedItems = append([]*models.CatalogItem(nil), items...)
	return repository.changed, repository.upsertError
}

func (repository *typesenseSyncRepositoryStub) ReconcileSourceSnapshot(
	_ context.Context,
	source models.ItemSource,
	items []*models.CatalogItem,
	expectedItemCount int,
	sourceUpdatedUpperBound *time.Time,
	snapshotStartedAt time.Time,
) (int, int, error) {
	repository.reconciledSource = source
	repository.reconciledItems = append([]*models.CatalogItem(nil), items...)
	repository.reconciledExpected = expectedItemCount
	if sourceUpdatedUpperBound != nil {
		upperBoundCopy := *sourceUpdatedUpperBound
		repository.reconciledUpperBound = &upperBoundCopy
	}
	repository.reconciledStartedAt = snapshotStartedAt
	return repository.changed, repository.deactivated, repository.reconcileError
}

func TestTypesenseSyncPersistsMaximumUpstreamCursorAfterAtomicBatch(t *testing.T) {
	olderTimestamp := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	newerTimestamp := olderTimestamp.Add(45 * time.Minute)
	exporter := &typesenseExporterStub{services: []clients.TypesenseService{
		{ID: "service-newer", NomeServico: "Novo", LastUpdate: newerTimestamp.Unix()},
		{ID: "service-older", NomeServico: "Antigo", LastUpdate: olderTimestamp.Unix()},
	}}
	repository := &typesenseSyncRepositoryStub{changed: 2}
	source := NewTypesenseDataSource(exporter, repository, "https://example.test", time.Hour, 24*time.Hour)

	changed, syncError := source.Sync(context.Background())

	if syncError != nil {
		t.Fatalf("Sync returned error: %v", syncError)
	}
	if changed != 2 {
		t.Fatalf("Sync changed = %d, want 2", changed)
	}
	if !exporter.receivedCursor.IsZero() {
		t.Fatalf("initial cursor = %s, want zero", exporter.receivedCursor)
	}
	if len(repository.leasedSources) != 1 || repository.leasedSources[0] != models.SourceTypesense {
		t.Fatalf("leased sources = %v, want [typesense]", repository.leasedSources)
	}
	if len(repository.reconciledItems) != 2 {
		t.Fatalf("reconciled items = %d, want 2", len(repository.reconciledItems))
	}
	if repository.reconciledSource != models.SourceTypesense || repository.reconciledExpected != 2 ||
		repository.reconciledUpperBound == nil || repository.reconciledUpperBound.Before(newerTimestamp) {
		t.Fatalf(
			"reconciliation scope = source %q, expected %d, upper bound %v",
			repository.reconciledSource,
			repository.reconciledExpected,
			repository.reconciledUpperBound,
		)
	}
	if repository.finalStatus != models.SyncStatusCompleted || repository.finalProcessed != 2 || repository.finalFailed != 0 {
		t.Fatalf(
			"final event = (%q, %d, %d), want completed, 2, 0",
			repository.finalStatus,
			repository.finalProcessed,
			repository.finalFailed,
		)
	}

	var metadata typesenseSyncMetadata
	if unmarshalError := json.Unmarshal(repository.finalMetadata, &metadata); unmarshalError != nil {
		t.Fatalf("unmarshal cursor metadata: %v", unmarshalError)
	}
	if metadata.CursorVersion != typesenseCursorVersion || metadata.CursorUnix != newerTimestamp.Unix() {
		t.Fatalf("cursor metadata = %+v, want version %d and unix %d", metadata, typesenseCursorVersion, newerTimestamp.Unix())
	}
	if metadata.LastFullSnapshotUnix <= 0 {
		t.Fatal("full snapshot completion timestamp was not persisted")
	}
}

func TestTypesenseSyncDoesNotFetchWithoutGlobalSourceLease(t *testing.T) {
	leaseError := errors.New("lease unavailable")
	exporter := &typesenseExporterStub{services: []clients.TypesenseService{{
		ID:          "service-1",
		NomeServico: "Service",
		LastUpdate:  10,
	}}}
	repository := &typesenseSyncRepositoryStub{leaseError: leaseError}
	source := NewTypesenseDataSource(exporter, repository, "", time.Hour, 24*time.Hour)

	changed, syncError := source.Sync(context.Background())

	if changed != 0 || !errors.Is(syncError, leaseError) {
		t.Fatalf("Sync = %d, %v; want lease failure", changed, syncError)
	}
	if exporter.exportCalls != 0 {
		t.Fatalf("Typesense exported %d times without acquiring the source lease", exporter.exportCalls)
	}
}

func TestTypesenseSyncUsesPersistedUpstreamCursorForEmptyDelta(t *testing.T) {
	cursor := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	metadata, marshalError := json.Marshal(typesenseSyncMetadata{
		CursorVersion:        typesenseCursorVersion,
		CursorUnix:           cursor.Unix(),
		LastFullSnapshotUnix: cursor.Unix(),
	})
	if marshalError != nil {
		t.Fatalf("marshal cursor: %v", marshalError)
	}
	exporter := &typesenseExporterStub{}
	repository := &typesenseSyncRepositoryStub{
		completedMetadata:      metadata,
		completedMetadataFound: true,
	}
	source := newTypesenseDataSource(
		exporter,
		repository,
		"",
		time.Hour,
		24*time.Hour,
		func() time.Time { return cursor.Add(time.Hour) },
	)

	changed, syncError := source.Sync(context.Background())

	if syncError != nil {
		t.Fatalf("Sync returned error: %v", syncError)
	}
	if changed != 0 {
		t.Fatalf("Sync changed = %d, want 0", changed)
	}
	if !exporter.receivedCursor.Equal(cursor) {
		t.Fatalf("export cursor = %s, want %s", exporter.receivedCursor, cursor)
	}
	var finalMetadata typesenseSyncMetadata
	if unmarshalError := json.Unmarshal(repository.finalMetadata, &finalMetadata); unmarshalError != nil {
		t.Fatalf("unmarshal final cursor: %v", unmarshalError)
	}
	if finalMetadata.CursorUnix != cursor.Unix() {
		t.Fatalf("final cursor = %d, want %d", finalMetadata.CursorUnix, cursor.Unix())
	}
	if repository.reconciledItems != nil {
		t.Fatalf("delta sync attempted snapshot reconciliation: %v", repository.reconciledItems)
	}
}

func TestTypesenseSyncRejectsEmptyInitialSnapshot(t *testing.T) {
	exporter := &typesenseExporterStub{}
	repository := &typesenseSyncRepositoryStub{}
	source := NewTypesenseDataSource(exporter, repository, "", time.Hour, 24*time.Hour)

	changed, syncError := source.Sync(context.Background())

	if changed != 0 || !errors.Is(syncError, errEmptyTypesenseSnapshot) {
		t.Fatalf("Sync = %d, %v, want empty snapshot failure", changed, syncError)
	}
	if repository.upsertedItems != nil {
		t.Fatalf("empty snapshot attempted upsert: %v", repository.upsertedItems)
	}
	if repository.reconciledItems != nil {
		t.Fatalf("empty snapshot attempted reconciliation: %v", repository.reconciledItems)
	}
	if repository.finalStatus != models.SyncStatusFailed || repository.finalFailed != 1 {
		t.Fatalf("final event = (%q, %d), want failed, 1", repository.finalStatus, repository.finalFailed)
	}
}

func TestTypesenseSyncDoesNotPersistPartialExport(t *testing.T) {
	exporter := &typesenseExporterStub{
		services:    []clients.TypesenseService{{ID: "service-1", NomeServico: "Serviço", LastUpdate: 10}},
		exportError: errors.New("stream interrupted"),
	}
	repository := &typesenseSyncRepositoryStub{}
	source := NewTypesenseDataSource(exporter, repository, "", time.Hour, 24*time.Hour)

	changed, syncError := source.Sync(context.Background())

	if changed != 0 || syncError == nil || !errors.Is(syncError, exporter.exportError) {
		t.Fatalf("Sync = %d, %v, want export failure", changed, syncError)
	}
	if repository.upsertedItems != nil {
		t.Fatalf("partial export attempted upsert: %v", repository.upsertedItems)
	}
	if repository.reconciledItems != nil {
		t.Fatalf("partial export attempted reconciliation: %v", repository.reconciledItems)
	}
	if repository.finalStatus != models.SyncStatusFailed || repository.finalProcessed != 1 {
		t.Fatalf("final event = (%q, %d), want failed, 1 processed", repository.finalStatus, repository.finalProcessed)
	}
}

func TestTypesenseSyncRejectsDocumentWithoutUpstreamTimestamp(t *testing.T) {
	exporter := &typesenseExporterStub{services: []clients.TypesenseService{{ID: "service-1", NomeServico: "Serviço"}}}
	repository := &typesenseSyncRepositoryStub{}
	source := NewTypesenseDataSource(exporter, repository, "", time.Hour, 24*time.Hour)

	changed, syncError := source.Sync(context.Background())

	if changed != 0 || syncError == nil {
		t.Fatalf("Sync = %d, %v, want timestamp validation failure", changed, syncError)
	}
	if repository.upsertedItems != nil {
		t.Fatalf("invalid document attempted upsert: %v", repository.upsertedItems)
	}
	if repository.reconciledItems != nil {
		t.Fatalf("invalid document attempted reconciliation: %v", repository.reconciledItems)
	}
}

func TestTypesenseSyncRunsPeriodicFullReconciliationAndCountsDeactivations(t *testing.T) {
	currentTime := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	cursor := currentTime.Add(-time.Hour)
	metadata, marshalError := json.Marshal(typesenseSyncMetadata{
		CursorVersion:        typesenseCursorVersion,
		CursorUnix:           cursor.Unix(),
		LastFullSnapshotUnix: currentTime.Add(-24 * time.Hour).Unix(),
	})
	if marshalError != nil {
		t.Fatalf("marshal cursor metadata: %v", marshalError)
	}
	exporter := &typesenseExporterStub{services: []clients.TypesenseService{{
		ID:          "service-retained",
		NomeServico: "Retained service",
		LastUpdate:  cursor.Unix(),
	}}}
	repository := &typesenseSyncRepositoryStub{
		completedMetadata:      metadata,
		completedMetadataFound: true,
		changed:                1,
		deactivated:            2,
	}
	source := newTypesenseDataSource(
		exporter,
		repository,
		"",
		time.Hour,
		24*time.Hour,
		func() time.Time { return currentTime },
	)

	changed, syncError := source.Sync(context.Background())

	if syncError != nil {
		t.Fatalf("Sync returned error: %v", syncError)
	}
	if changed != 3 {
		t.Fatalf("Sync changed = %d, want one upsert plus two soft-deactivations", changed)
	}
	if !exporter.receivedCursor.IsZero() {
		t.Fatalf("periodic full export used delta cursor %s", exporter.receivedCursor)
	}
	if len(repository.reconciledItems) != 1 {
		t.Fatalf("periodic full reconciliation received %d items, want 1", len(repository.reconciledItems))
	}
}

func TestTypesenseSyncUpgradesLegacyCursorThroughFullSnapshot(t *testing.T) {
	currentTime := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	metadata, marshalError := json.Marshal(typesenseSyncMetadata{
		CursorVersion: legacyTypesenseCursorVersion,
		CursorUnix:    currentTime.Add(-time.Hour).Unix(),
	})
	if marshalError != nil {
		t.Fatalf("marshal legacy cursor metadata: %v", marshalError)
	}
	exporter := &typesenseExporterStub{services: []clients.TypesenseService{{
		ID:          "service-1",
		NomeServico: "Service",
		LastUpdate:  currentTime.Unix(),
	}}}
	repository := &typesenseSyncRepositoryStub{
		completedMetadata:      metadata,
		completedMetadataFound: true,
		changed:                1,
	}
	source := newTypesenseDataSource(
		exporter,
		repository,
		"",
		time.Hour,
		24*time.Hour,
		func() time.Time { return currentTime },
	)

	if _, syncError := source.Sync(context.Background()); syncError != nil {
		t.Fatalf("Sync returned error: %v", syncError)
	}
	if !exporter.receivedCursor.IsZero() || len(repository.reconciledItems) != 1 {
		t.Fatalf(
			"legacy cursor used %s and reconciled %d items; want zero cursor and one item",
			exporter.receivedCursor,
			len(repository.reconciledItems),
		)
	}
}

func TestTypesenseSyncFailsEventWhenAtomicReconciliationFails(t *testing.T) {
	reconciliationError := errors.New("database transaction failed")
	exporter := &typesenseExporterStub{services: []clients.TypesenseService{{
		ID:          "service-1",
		NomeServico: "Service",
		LastUpdate:  10,
	}}}
	repository := &typesenseSyncRepositoryStub{reconcileError: reconciliationError}
	source := NewTypesenseDataSource(exporter, repository, "", time.Hour, 24*time.Hour)

	changed, syncError := source.Sync(context.Background())

	if changed != 0 || !errors.Is(syncError, reconciliationError) {
		t.Fatalf("Sync = %d, %v; want reconciliation failure", changed, syncError)
	}
	if repository.finalStatus != models.SyncStatusFailed || repository.finalFailed != 1 {
		t.Fatalf("final event = (%q, %d), want failed, 1", repository.finalStatus, repository.finalFailed)
	}
}

func TestTypesenseAudienceDoesNotMisclassifyUnknownAudienceAsEthnicity(t *testing.T) {
	rawAudience := mapTypesenseTargetAudience(clients.TypesenseService{
		PublicoEspecifico: []string{"Empreendedores", "Pessoas com deficiência"},
	})
	var audience models.TargetAudienceData
	if unmarshalError := json.Unmarshal(rawAudience, &audience); unmarshalError != nil {
		t.Fatalf("unmarshal target audience: %v", unmarshalError)
	}
	if len(audience.Etnia) != 0 {
		t.Fatalf("unknown audience was stored as ethnicity: %v", audience.Etnia)
	}
	if len(audience.Deficiencia) != 1 || audience.Deficiencia[0] != "Pessoas com deficiência" {
		t.Fatalf("disability audience = %v, want mapped value", audience.Deficiencia)
	}
}
