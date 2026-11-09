package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

// syncEventUpdate é uma chamada a UpdateSyncEvent capturada pelo stub.
type syncEventUpdate struct {
	id         int64
	status     models.SyncEventStatus
	processed  int
	failed     int
	errMsg     string
	durationMs int
	ctxErr     error
}

// syncEventRecordingRepo grava em memória os eventos de sync de app-go-api,
// delegando upsert/soft delete ao stub comum.
type syncEventRecordingRepo struct {
	*stubAppGoAPIRepo

	recordErr error
	updateErr error
	recorded  []models.SyncEvent
	updates   []syncEventUpdate
}

func (r *syncEventRecordingRepo) RecordSyncEvent(_ context.Context, event *models.SyncEvent) (int64, error) {
	if r.recordErr != nil {
		return 0, r.recordErr
	}
	r.recorded = append(r.recorded, *event)
	return int64(41 + len(r.recorded)), nil
}

func (r *syncEventRecordingRepo) UpdateSyncEvent(ctx context.Context, id int64, status models.SyncEventStatus, processed, failed int, errMsg string, durationMs int) error {
	r.updates = append(r.updates, syncEventUpdate{id, status, processed, failed, errMsg, durationMs, ctx.Err()})
	return r.updateErr
}

func newSyncEventRepo() *syncEventRecordingRepo {
	return &syncEventRecordingRepo{stubAppGoAPIRepo: &stubAppGoAPIRepo{}}
}

func TestSync_RecordsCompletedSyncEventForAppGoAPI(t *testing.T) {
	t.Parallel()

	fetcher := &stubAppGoAPIFetcher{
		courses: []clients.Course{
			{ID: "c1", Title: "Curso", Status: "published", IsVisible: true},
			{ID: "c2", Title: "Encerrado", Status: "finished", IsVisible: true},
		},
		jobs: []clients.Job{
			{ID: "j1", Title: "Vaga 1", Status: "publicado_ativo"},
			{ID: "j2", Title: "Vaga 2", Status: "publicado_ativo"},
		},
		mei: []clients.MEIOpportunity{{ID: "m1", Title: "MEI", Status: "active"}},
	}
	repo := newSyncEventRepo()
	ds := NewAppGoAPIDataSource(fetcher, repo, time.Hour)

	before := time.Now()
	if err := ds.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if len(repo.recorded) != 1 {
		t.Fatalf("RecordSyncEvent calls=%d want 1", len(repo.recorded))
	}
	ev := repo.recorded[0]
	if ev.Source != models.SourceAppGoAPI {
		t.Fatalf("event source=%q want %q", ev.Source, models.SourceAppGoAPI)
	}
	if ev.EventType != models.SyncTypeFullSync {
		t.Fatalf("event type=%q want %q", ev.EventType, models.SyncTypeFullSync)
	}
	if ev.Status != models.SyncStatusStarted {
		t.Fatalf("event status=%q want %q", ev.Status, models.SyncStatusStarted)
	}
	if ev.StartedAt.Before(before) {
		t.Fatalf("event started_at=%v before sync start %v", ev.StartedAt, before)
	}
	// sync_events.metadata é JSONB NOT NULL: metadata nil viraria NULL no pgx e o INSERT falharia.
	var meta map[string]any
	if len(ev.Metadata) == 0 || json.Unmarshal(ev.Metadata, &meta) != nil {
		t.Fatalf("event metadata=%q must be a JSON object", ev.Metadata)
	}

	if len(repo.updates) != 1 {
		t.Fatalf("UpdateSyncEvent calls=%d want 1", len(repo.updates))
	}
	up := repo.updates[0]
	if up.id != 42 {
		t.Fatalf("updated event id=%d want 42 (the recorded one)", up.id)
	}
	if up.status != models.SyncStatusCompleted {
		t.Fatalf("final status=%q want %q", up.status, models.SyncStatusCompleted)
	}
	// 1 curso indexável + 2 vagas + 1 MEI
	if up.processed != 4 || up.failed != 0 {
		t.Fatalf("processed=%d failed=%d want 4/0", up.processed, up.failed)
	}
	if up.errMsg != "" {
		t.Fatalf("errMsg=%q want empty", up.errMsg)
	}
	if up.durationMs < 0 {
		t.Fatalf("durationMs=%d", up.durationMs)
	}
}

func TestSync_RecordsFailedSyncEventOnPartialFailure(t *testing.T) {
	t.Parallel()

	fetcher := &stubAppGoAPIFetcher{
		coursesErr: errors.New("courses down"),
		jobs:       []clients.Job{{ID: "j1", Title: "Vaga", Status: "publicado_ativo"}},
		meiErr:     errors.New("mei down"),
	}
	repo := newSyncEventRepo()
	ds := NewAppGoAPIDataSource(fetcher, repo, time.Hour)

	err := ds.Sync(context.Background())
	if err == nil {
		t.Fatal("expected joined error")
	}

	if len(repo.recorded) != 1 || len(repo.updates) != 1 {
		t.Fatalf("record=%d update=%d want 1/1", len(repo.recorded), len(repo.updates))
	}
	up := repo.updates[0]
	if up.status != models.SyncStatusFailed {
		t.Fatalf("final status=%q want %q", up.status, models.SyncStatusFailed)
	}
	if up.processed != 1 {
		t.Fatalf("processed=%d want 1 (vagas ok)", up.processed)
	}
	if up.errMsg != err.Error() {
		t.Fatalf("errMsg=%q want the returned error %q", up.errMsg, err.Error())
	}
	if !strings.Contains(up.errMsg, "cursos") || !strings.Contains(up.errMsg, "mei") {
		t.Fatalf("errMsg must name the failed sub-syncs: %q", up.errMsg)
	}
}

func TestSync_CountsItemsOfFailedUpsertAsFailed(t *testing.T) {
	t.Parallel()

	fetcher := &stubAppGoAPIFetcher{
		courses: []clients.Course{
			{ID: "c1", Title: "A", Status: "published", IsVisible: true},
			{ID: "c2", Title: "B", Status: "published", IsVisible: true},
		},
		jobs: []clients.Job{{ID: "j1", Title: "Vaga", Status: "publicado_ativo"}},
	}
	repo := newSyncEventRepo()
	repo.upsertErr = errors.New("db down")
	ds := NewAppGoAPIDataSource(fetcher, repo, time.Hour)

	if err := ds.Sync(context.Background()); err == nil {
		t.Fatal("expected upsert error")
	}

	if len(repo.updates) != 1 {
		t.Fatalf("UpdateSyncEvent calls=%d want 1", len(repo.updates))
	}
	up := repo.updates[0]
	if up.status != models.SyncStatusFailed {
		t.Fatalf("final status=%q want %q", up.status, models.SyncStatusFailed)
	}
	// 2 cursos + 1 vaga não gravados; MEI vazio não chama UpsertBatch com itens
	if up.processed != 0 || up.failed != 3 {
		t.Fatalf("processed=%d failed=%d want 0/3", up.processed, up.failed)
	}
	if !strings.Contains(up.errMsg, "db down") {
		t.Fatalf("errMsg=%q must carry the upsert error", up.errMsg)
	}
}

func TestSync_EventRecordingFailureDoesNotBreakSync(t *testing.T) {
	t.Parallel()

	fetcher := &stubAppGoAPIFetcher{
		jobs: []clients.Job{{ID: "j1", Title: "Vaga", Status: "publicado_ativo"}},
	}
	repo := newSyncEventRepo()
	repo.recordErr = errors.New("insert sync_events failed")
	ds := NewAppGoAPIDataSource(fetcher, repo, time.Hour)

	if err := ds.Sync(context.Background()); err != nil {
		t.Fatalf("Sync must not fail because the event could not be recorded: %v", err)
	}
	if len(repo.upserted) != 1 {
		t.Fatalf("upserted=%d want 1", len(repo.upserted))
	}
	if len(repo.updates) != 0 {
		t.Fatalf("UpdateSyncEvent must not run without a recorded event, calls=%d", len(repo.updates))
	}
}

func TestSync_EventUpdateFailureDoesNotBreakSync(t *testing.T) {
	t.Parallel()

	fetcher := &stubAppGoAPIFetcher{
		jobs: []clients.Job{{ID: "j1", Title: "Vaga", Status: "publicado_ativo"}},
	}
	repo := newSyncEventRepo()
	repo.updateErr = errors.New("update sync_events failed")
	ds := NewAppGoAPIDataSource(fetcher, repo, time.Hour)

	if err := ds.Sync(context.Background()); err != nil {
		t.Fatalf("Sync must not fail because the event could not be updated: %v", err)
	}
	if len(repo.updates) != 1 {
		t.Fatalf("UpdateSyncEvent calls=%d want 1", len(repo.updates))
	}
}

func TestSync_UpdatesEventEvenWhenContextIsCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	fetcher := &cancelingFetcher{stubAppGoAPIFetcher: &stubAppGoAPIFetcher{}, cancel: cancel}
	repo := newSyncEventRepo()
	ds := NewAppGoAPIDataSource(fetcher, repo, time.Hour)

	_ = ds.Sync(ctx)

	if len(repo.updates) != 1 {
		t.Fatalf("UpdateSyncEvent calls=%d want 1", len(repo.updates))
	}
	up := repo.updates[0]
	if up.ctxErr != nil {
		t.Fatalf("UpdateSyncEvent got a canceled context (%v): the event would stay 'started'", up.ctxErr)
	}
	if up.status != models.SyncStatusFailed {
		t.Fatalf("final status=%q want %q", up.status, models.SyncStatusFailed)
	}
}

// cancelingFetcher cancela o contexto da sync durante a listagem de MEI (a última
// sub-sync) e devolve o erro de contexto, como o cliente HTTP faria.
type cancelingFetcher struct {
	*stubAppGoAPIFetcher
	cancel context.CancelFunc
}

func (f *cancelingFetcher) GetMEIOpportunities(ctx context.Context, _ int, _ time.Time) ([]clients.MEIOpportunity, int, error) {
	f.cancel()
	return nil, 0, ctx.Err()
}
