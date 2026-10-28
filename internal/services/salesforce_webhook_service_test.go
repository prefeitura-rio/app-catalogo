package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

// memWebhookStore reproduz em memória as regras da fila do repositório
// (lease, claim de vencidos, conclusão que cobre pendentes anteriores do slug e
// falha válida só para a tentativa dona), com relógio controlado pelo teste.
type memWebhookStore struct {
	mu     sync.Mutex
	now    time.Time
	nextID int64
	events map[int64]*memEvent
	byKey  map[string]int64
}

type memEvent struct {
	ev        models.SalesForceWebhookEvent
	lastError string
}

func newMemWebhookStore() *memWebhookStore {
	return &memWebhookStore{
		now:    time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
		events: map[int64]*memEvent{},
		byKey:  map[string]int64{},
	}
}

func (m *memWebhookStore) advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
}

func (m *memWebhookStore) get(id int64) memEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *m.events[id]
}

func (m *memWebhookStore) Enqueue(_ context.Context, key, slug, eventType string, lease time.Duration) (*models.SalesForceWebhookEvent, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byKey[key]; ok {
		return nil, false, nil
	}
	m.nextID++
	e := &memEvent{ev: models.SalesForceWebhookEvent{
		ID: m.nextID, EventKey: key, Slug: slug, EventType: eventType,
		Status: models.WebhookEventPending, Attempts: 1,
		NextAttemptAt: m.now.Add(lease), ReceivedAt: m.now, ClaimedAt: m.now,
	}}
	m.events[e.ev.ID] = e
	m.byKey[key] = e.ev.ID
	out := e.ev
	return &out, true, nil
}

func (m *memWebhookStore) ClaimDue(_ context.Context, limit int, lease time.Duration) ([]*models.SalesForceWebhookEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*models.SalesForceWebhookEvent
	for id := int64(1); id <= m.nextID && len(out) < limit; id++ {
		e := m.events[id]
		if e.ev.Status != models.WebhookEventPending || e.ev.NextAttemptAt.After(m.now) {
			continue
		}
		e.ev.Attempts++
		e.ev.NextAttemptAt = m.now.Add(lease)
		e.ev.ClaimedAt = m.now
		claimed := e.ev
		out = append(out, &claimed)
	}
	return out, nil
}

func (m *memWebhookStore) Complete(_ context.Context, ev *models.SalesForceWebhookEvent) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, e := range m.events {
		if e.ev.Status != models.WebhookEventPending {
			continue
		}
		if e.ev.ID == ev.ID || (e.ev.Slug == ev.Slug && !e.ev.ReceivedAt.After(ev.ClaimedAt)) {
			e.ev.Status = models.WebhookEventCompleted
			e.lastError = ""
			n++
		}
	}
	return n, nil
}

func (m *memWebhookStore) Fail(_ context.Context, ev *models.SalesForceWebhookEvent, errMsg string, retryIn time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.events[ev.ID]
	if e == nil || e.ev.Status != models.WebhookEventPending || e.ev.Attempts != ev.Attempts {
		return nil
	}
	e.lastError = errMsg
	e.ev.NextAttemptAt = m.now.Add(retryIn)
	return nil
}

func (m *memWebhookStore) DeleteCompletedBefore(context.Context, time.Time) (int64, error) {
	return 0, nil
}

// scriptedSyncer falha nas primeiras failures chamadas e conta as chamadas por slug.
type scriptedSyncer struct {
	mu       sync.Mutex
	failures int
	calls    []string
}

func (s *scriptedSyncer) SyncRecord(_ context.Context, slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, slug)
	if len(s.calls) <= s.failures {
		return errors.New("carta indisponível")
	}
	return nil
}

func (s *scriptedSyncer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

var testWebhookConfig = WebhookConfig{
	Lease:          2 * time.Minute,
	ProcessTimeout: time.Minute,
	RetryBaseDelay: 30 * time.Second,
	RetryMaxDelay:  time.Hour,
	BatchSize:      10,
	Retention:      time.Hour,
}

func TestWebhookService_FailedEventStaysPendingAndIsReprocessed(t *testing.T) {
	ctx := context.Background()
	store := newMemWebhookStore()
	syncer := &scriptedSyncer{failures: 2}
	svc := NewSalesForceWebhookService(store, syncer, testWebhookConfig)

	ev, created, err := svc.Enqueue(ctx, []byte(`{"sobject":{"Slug":"servico-teste"}}`), "servico-teste", "updated", "2026-10-28T12:00:00Z")
	if err != nil || !created {
		t.Fatalf("enqueue: created=%v err=%v", created, err)
	}

	// 1ª tentativa (a do handler) falha: evento continua pendente, próxima em 30s.
	if err := svc.Process(ctx, ev); err == nil {
		t.Fatal("esperava erro da 1ª tentativa")
	}
	got := store.get(ev.ID)
	if got.ev.Status != models.WebhookEventPending {
		t.Fatalf("evento deveria seguir pendente, status=%s", got.ev.Status)
	}
	if got.lastError == "" {
		t.Error("esperava last_error registrado")
	}
	if want := store.now.Add(30 * time.Second); !got.ev.NextAttemptAt.Equal(want) {
		t.Errorf("próxima tentativa: esperava %v, got %v", want, got.ev.NextAttemptAt)
	}

	// Antes de vencer, o reprocessador não pega o evento.
	if n, _ := svc.ProcessDue(ctx); n != 0 {
		t.Fatalf("não deveria reprocessar antes de vencer, assumiu %d", n)
	}

	// 2ª tentativa falha de novo: espera dobra (60s).
	store.advance(30 * time.Second)
	if n, _ := svc.ProcessDue(ctx); n != 1 {
		t.Fatalf("esperava 1 evento reprocessado, got %d", n)
	}
	got = store.get(ev.ID)
	if want := store.now.Add(60 * time.Second); got.ev.Status != models.WebhookEventPending || !got.ev.NextAttemptAt.Equal(want) {
		t.Fatalf("após 2ª falha: status=%s next=%v (esperava %v)", got.ev.Status, got.ev.NextAttemptAt, want)
	}

	// 3ª tentativa funciona: evento concluído.
	store.advance(60 * time.Second)
	if n, _ := svc.ProcessDue(ctx); n != 1 {
		t.Fatalf("esperava 1 evento reprocessado, got %d", n)
	}
	got = store.get(ev.ID)
	if got.ev.Status != models.WebhookEventCompleted || got.ev.Attempts != 3 {
		t.Fatalf("esperava concluído em 3 tentativas, status=%s attempts=%d", got.ev.Status, got.ev.Attempts)
	}

	// Nada mais a fazer: o evento concluído não é reprocessado.
	store.advance(2 * time.Hour)
	if n, _ := svc.ProcessDue(ctx); n != 0 {
		t.Fatalf("evento concluído não deveria voltar, assumiu %d", n)
	}
	if syncer.count() != 3 {
		t.Errorf("esperava 3 chamadas a SyncRecord, got %d", syncer.count())
	}
}

func TestWebhookService_EventOwnedByDeadPodIsResumedAfterLease(t *testing.T) {
	ctx := context.Background()
	store := newMemWebhookStore()
	syncer := &scriptedSyncer{}
	svc := NewSalesForceWebhookService(store, syncer, testWebhookConfig)

	ev, _, err := svc.Enqueue(ctx, []byte(`{"id":1}`), "servico-orfao", "updated", "2026-10-28T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	// O pod morreu antes de Process: o lease segura o evento por 2 min...
	store.advance(time.Minute)
	if n, _ := svc.ProcessDue(ctx); n != 0 {
		t.Fatalf("lease ainda vigente: não deveria reprocessar, assumiu %d", n)
	}
	// ...e depois o reprocessador o retoma.
	store.advance(time.Minute)
	if n, _ := svc.ProcessDue(ctx); n != 1 {
		t.Fatalf("esperava retomar o evento órfão, assumiu %d", n)
	}
	if got := store.get(ev.ID); got.ev.Status != models.WebhookEventCompleted {
		t.Fatalf("evento órfão deveria concluir, status=%s", got.ev.Status)
	}
}

func TestWebhookService_DuplicateDeliveryIsNotProcessedAgain(t *testing.T) {
	ctx := context.Background()
	store := newMemWebhookStore()
	syncer := &scriptedSyncer{}
	svc := NewSalesForceWebhookService(store, syncer, testWebhookConfig)

	body := []byte(`{"event":{"type":"updated","created":"2026-10-28T12:00:00Z"},"sobject":{"Slug":"servico-dup"}}`)
	ev, created, err := svc.Enqueue(ctx, body, "servico-dup", "updated", "2026-10-28T12:00:00Z")
	if err != nil || !created {
		t.Fatalf("enqueue: created=%v err=%v", created, err)
	}
	if err := svc.Process(ctx, ev); err != nil {
		t.Fatal(err)
	}

	again, created, err := svc.Enqueue(ctx, body, "servico-dup", "updated", "2026-10-28T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if created || again != nil {
		t.Fatalf("reentrega do mesmo body não deveria criar evento: created=%v ev=%v", created, again)
	}

	// Reprocessar o evento já concluído não reabre nem conta de novo.
	if err := svc.Process(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.Complete(ctx, ev); n != 0 {
		t.Errorf("concluir de novo não deveria afetar eventos, afetou %d", n)
	}
	if got := store.get(ev.ID); got.ev.Status != models.WebhookEventCompleted {
		t.Fatalf("status: %s", got.ev.Status)
	}
	if len(store.events) != 1 {
		t.Errorf("esperava 1 evento na fila, got %d", len(store.events))
	}
}

func TestWebhookService_SameBodyWithoutEventCreatedIsNotTreatedAsRedelivery(t *testing.T) {
	ctx := context.Background()
	store := newMemWebhookStore()
	syncer := &scriptedSyncer{}
	svc := NewSalesForceWebhookService(store, syncer, testWebhookConfig)

	// Sem event.created, duas mudanças do mesmo serviço chegam com o mesmo body.
	body := []byte(`{"event":{"type":"updated"},"sobject":{"Slug":"servico-sem-data"}}`)
	first, created, err := svc.Enqueue(ctx, body, "servico-sem-data", "updated", "")
	if err != nil || !created {
		t.Fatalf("1ª entrega: created=%v err=%v", created, err)
	}
	second, created, err := svc.Enqueue(ctx, body, "servico-sem-data", "updated", "  ")
	if err != nil || !created {
		t.Fatalf("2ª mudança não pode ser descartada como reentrega: created=%v err=%v", created, err)
	}
	if first.EventKey == second.EventKey || first.EventKey == WebhookEventKey(body) {
		t.Fatalf("chaves deveriam ser únicas e diferentes do hash do body: %s %s", first.EventKey, second.EventKey)
	}
}

func TestWebhookService_SuccessCoversEarlierPendingEventsOfSameSlug(t *testing.T) {
	ctx := context.Background()
	store := newMemWebhookStore()
	syncer := &scriptedSyncer{failures: 1}
	svc := NewSalesForceWebhookService(store, syncer, testWebhookConfig)

	first, _, _ := svc.Enqueue(ctx, []byte(`{"n":1}`), "servico-x", "updated", "2026-10-28T12:00:01Z")
	_ = svc.Process(ctx, first) // falha: fica pendente

	other, _, _ := svc.Enqueue(ctx, []byte(`{"n":2}`), "outro-servico", "updated", "2026-10-28T12:00:02Z")

	store.advance(5 * time.Second)
	second, _, _ := svc.Enqueue(ctx, []byte(`{"n":3}`), "servico-x", "updated", "2026-10-28T12:00:03Z")
	if err := svc.Process(ctx, second); err != nil {
		t.Fatal(err)
	}

	if got := store.get(first.ID); got.ev.Status != models.WebhookEventCompleted {
		t.Errorf("evento anterior do mesmo slug deveria ser coberto, status=%s", got.ev.Status)
	}
	if got := store.get(other.ID); got.ev.Status != models.WebhookEventPending {
		t.Errorf("evento de outro slug não deveria ser tocado, status=%s", got.ev.Status)
	}

	// O primeiro não é sincronizado de novo quando venceria.
	store.advance(time.Minute)
	before := syncer.count()
	if _, err := svc.ProcessDue(ctx); err != nil {
		t.Fatal(err)
	}
	for _, slug := range syncer.calls[before:] {
		if slug == "servico-x" {
			t.Error("servico-x não deveria ser sincronizado de novo")
		}
	}
}

func TestWebhookService_RetryDelayIsExponentialAndCapped(t *testing.T) {
	svc := NewSalesForceWebhookService(newMemWebhookStore(), &scriptedSyncer{}, testWebhookConfig)
	cases := map[int]time.Duration{
		1:  30 * time.Second,
		2:  time.Minute,
		3:  2 * time.Minute,
		7:  32 * time.Minute,
		8:  time.Hour,
		50: time.Hour,
	}
	for attempt, want := range cases {
		if got := svc.retryDelay(attempt); got != want {
			t.Errorf("attempt %d: esperava %v, got %v", attempt, want, got)
		}
	}
}

// lastEventType devolve o event_type gravado no evento id.
func (m *memWebhookStore) lastEventType(id int64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.events[id].ev.EventType
}

func TestWebhookService_LongEventTypeIsTruncatedToColumnLimit(t *testing.T) {
	store := newMemWebhookStore()
	svc := NewSalesForceWebhookService(store, &scriptedSyncer{}, testWebhookConfig)

	long := strings.Repeat("é", maxWebhookEventTypeChars+10)
	ev, created, err := svc.Enqueue(context.Background(), []byte(`{"x":1}`), "servico-tipo", long, "2026-10-28T12:00:00Z")
	if err != nil || !created {
		t.Fatalf("enqueue: created=%v err=%v", created, err)
	}
	got := store.lastEventType(ev.ID)
	if n := utf8.RuneCountInString(got); n != maxWebhookEventTypeChars || !utf8.ValidString(got) {
		t.Fatalf("event_type deveria ter %d caracteres válidos, got %d (válido=%v)", maxWebhookEventTypeChars, n, utf8.ValidString(got))
	}
	if short := truncateRunes("updated", maxWebhookEventTypeChars); short != "updated" {
		t.Fatalf("tipo curto não deveria mudar: %q", short)
	}
}

func TestWebhookEventKey_IsStableSHA256(t *testing.T) {
	a := WebhookEventKey([]byte("abc"))
	if a != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("hash inesperado: %s", a)
	}
	if WebhookEventKey([]byte("abd")) == a {
		t.Fatal("bodies diferentes devem ter chaves diferentes")
	}
}
