package datasource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/observability"
)

type stubSource struct {
	name      string
	callCount atomic.Int32
}

func (s *stubSource) Name() string                { return s.name }
func (s *stubSource) Source() models.ItemSource   { return models.ItemSource(s.name) }
func (s *stubSource) SyncInterval() time.Duration { return time.Hour } // ticker longo: não dispara em teste
func (s *stubSource) Sync(_ context.Context) error {
	s.callCount.Add(1)
	return nil
}

func TestManager_Register_And_TriggerAll(t *testing.T) {
	m := NewManager()
	s1 := &stubSource{name: "source-a"}
	s2 := &stubSource{name: "source-b"}

	m.Register(s1)
	m.Register(s2)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	m.TriggerAll(ctx)

	// Aguardar as goroutines concluírem
	time.Sleep(100 * time.Millisecond)

	if s1.callCount.Load() != 1 {
		t.Errorf("source-a: esperava 1 chamada, got %d", s1.callCount.Load())
	}
	if s2.callCount.Load() != 1 {
		t.Errorf("source-b: esperava 1 chamada, got %d", s2.callCount.Load())
	}
}

func TestManager_TriggerSync_ByName(t *testing.T) {
	m := NewManager()
	s1 := &stubSource{name: "salesforce"}
	s2 := &stubSource{name: "app-go-api"}

	m.Register(s1)
	m.Register(s2)

	ctx := context.Background()

	found := m.TriggerSync(ctx, "salesforce")
	if !found {
		t.Fatal("TriggerSync deveria retornar true para 'salesforce'")
	}

	notFound := m.TriggerSync(ctx, "inexistente")
	if notFound {
		t.Fatal("TriggerSync deveria retornar false para fonte inexistente")
	}

	time.Sleep(50 * time.Millisecond)
	if s1.callCount.Load() != 1 {
		t.Errorf("salesforce: esperava 1 chamada, got %d", s1.callCount.Load())
	}
	if s2.callCount.Load() != 0 {
		t.Errorf("app-go-api: não deveria ter sido chamado, got %d", s2.callCount.Load())
	}
}

// fullStubSource tem sync completa distinta da incremental (como a Carta).
type fullStubSource struct {
	stubSource
	fullCount atomic.Int32
}

func (s *fullStubSource) FullSync(_ context.Context) error {
	s.fullCount.Add(1)
	return nil
}

// waitCalled espera a primeira chamada registrada em c (ou 2 s).
func waitCalled(c *atomic.Int32) {
	deadline := time.Now().Add(2 * time.Second)
	for c.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

func TestParseSyncMode(t *testing.T) {
	cases := map[string]SyncMode{"": SyncModeDelta, "delta": SyncModeDelta, "full": SyncModeFull}
	for in, want := range cases {
		got, err := ParseSyncMode(in)
		if err != nil || got != want {
			t.Errorf("ParseSyncMode(%q) = %q, %v; esperado %q", in, got, err, want)
		}
	}
	for _, in := range []string{"FULL", "full ", "all", "1", "'; drop table x"} {
		if _, err := ParseSyncMode(in); err == nil {
			t.Errorf("ParseSyncMode(%q) deveria falhar", in)
		}
	}
}

func TestManager_TriggerSyncMode_FullCallsFullSync(t *testing.T) {
	m := NewManager()
	carta := &fullStubSource{stubSource: stubSource{name: "salesforce"}}
	other := &stubSource{name: "app-go-api"}
	m.Register(carta)
	m.Register(other)

	if !m.TriggerSyncMode(context.Background(), "salesforce", SyncModeFull) {
		t.Fatal("TriggerSyncMode deveria encontrar 'salesforce'")
	}
	waitCalled(&carta.fullCount)
	time.Sleep(50 * time.Millisecond)

	if carta.fullCount.Load() != 1 {
		t.Errorf("FullSync: esperava 1 chamada, got %d", carta.fullCount.Load())
	}
	if carta.callCount.Load() != 0 {
		t.Errorf("Sync incremental não deveria rodar no modo full, got %d", carta.callCount.Load())
	}
	if other.callCount.Load() != 0 {
		t.Errorf("app-go-api não deveria ter sido chamada, got %d", other.callCount.Load())
	}
}

func TestManager_TriggerAllMode_FullFallsBackToSync(t *testing.T) {
	m := NewManager()
	carta := &fullStubSource{stubSource: stubSource{name: "salesforce"}}
	other := &stubSource{name: "app-go-api"}
	m.Register(carta)
	m.Register(other)

	m.TriggerAllMode(context.Background(), SyncModeFull)
	waitCalled(&carta.fullCount)
	waitCalled(&other.callCount)

	if carta.fullCount.Load() != 1 || carta.callCount.Load() != 0 {
		t.Errorf("salesforce: full=%d sync=%d; esperado full=1 sync=0", carta.fullCount.Load(), carta.callCount.Load())
	}
	// Fonte sem FullSyncer roda a sync normal.
	if other.callCount.Load() != 1 {
		t.Errorf("app-go-api: esperava 1 sync normal, got %d", other.callCount.Load())
	}
}

func TestManager_TriggerSync_DeltaKeepsSync(t *testing.T) {
	m := NewManager()
	carta := &fullStubSource{stubSource: stubSource{name: "salesforce"}}
	m.Register(carta)

	m.TriggerSync(context.Background(), "salesforce")
	waitCalled(&carta.callCount)
	time.Sleep(50 * time.Millisecond)

	if carta.callCount.Load() != 1 || carta.fullCount.Load() != 0 {
		t.Errorf("modo delta: sync=%d full=%d; esperado sync=1 full=0", carta.callCount.Load(), carta.fullCount.Load())
	}
}

func TestManager_Start_InitialSync(t *testing.T) {
	m := NewManager()
	s := &stubSource{name: "test"}
	m.Register(s)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Start bloqueia até ctx cancelar
	m.Start(ctx)

	// Deve ter feito pelo menos 1 sync (a inicial)
	if s.callCount.Load() < 1 {
		t.Errorf("esperava pelo menos 1 sync inicial, got %d", s.callCount.Load())
	}
}

// flakySource falha nas primeiras failures chamadas e depois funciona.
type flakySource struct {
	name      string
	failures  int32
	callCount atomic.Int32
}

func (s *flakySource) Name() string                { return s.name }
func (s *flakySource) Source() models.ItemSource   { return models.ItemSource(s.name) }
func (s *flakySource) SyncInterval() time.Duration { return time.Hour }
func (s *flakySource) Sync(_ context.Context) error {
	if n := s.callCount.Add(1); n <= s.failures {
		return fmt.Errorf("falha transitória %d", n)
	}
	return nil
}

var fastRetry = RetryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 4 * time.Millisecond}

func TestManager_SyncWithRetry_RecoversAfterTransientFailures(t *testing.T) {
	src := &flakySource{name: "flaky-recovers", failures: 2}
	m := NewManager(WithRetryPolicy(fastRetry))

	before := time.Now().Unix()
	if err := m.syncWithRetry(context.Background(), src, triggerPeriodic); err != nil {
		t.Fatalf("esperava sucesso após novas tentativas, got %v", err)
	}

	if got := src.callCount.Load(); got != 3 {
		t.Errorf("esperava 3 chamadas (2 falhas + 1 sucesso), got %d", got)
	}
	if got := metricValue(t, observability.SyncRetriesTotal.WithLabelValues(src.name)); got != 2 {
		t.Errorf("sync_retries_total: esperava 2, got %v", got)
	}
	if got := metricValue(t, observability.SyncRunsTotal.WithLabelValues(src.name, triggerPeriodic, "success")); got != 1 {
		t.Errorf("sync_runs_total success: esperava 1, got %v", got)
	}
	if got := metricValue(t, observability.SyncRunsTotal.WithLabelValues(src.name, triggerPeriodic, "failure")); got != 0 {
		t.Errorf("sync_runs_total failure: esperava 0, got %v", got)
	}
	if got := metricValue(t, observability.SyncLastSuccessTimestamp.WithLabelValues(src.name)); int64(got) < before {
		t.Errorf("sync_last_success_timestamp_seconds: esperava >= %d, got %v", before, got)
	}
}

func TestManager_SyncWithRetry_GivesUpAfterMaxAttempts(t *testing.T) {
	src := &flakySource{name: "flaky-gives-up", failures: 100}
	m := NewManager(WithRetryPolicy(fastRetry))

	if err := m.syncWithRetry(context.Background(), src, triggerManual); err == nil {
		t.Fatal("esperava erro depois de esgotar as tentativas")
	}
	if got := src.callCount.Load(); got != int32(fastRetry.MaxAttempts) {
		t.Errorf("esperava %d chamadas, got %d", fastRetry.MaxAttempts, got)
	}
	if got := metricValue(t, observability.SyncRunsTotal.WithLabelValues(src.name, triggerManual, "failure")); got != 1 {
		t.Errorf("sync_runs_total failure: esperava 1, got %v", got)
	}
	if got := metricValue(t, observability.SyncLastSuccessTimestamp.WithLabelValues(src.name)); got != 0 {
		t.Errorf("sync_last_success_timestamp_seconds não deveria ser gravado, got %v", got)
	}
}

func TestManager_SyncWithRetry_StopsWhenContextCanceled(t *testing.T) {
	src := &flakySource{name: "flaky-canceled", failures: 100}
	m := NewManager(WithRetryPolicy(RetryPolicy{MaxAttempts: 10, BaseDelay: time.Hour, MaxDelay: time.Hour}))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	err := m.syncWithRetry(ctx, src, triggerPeriodic)
	if err == nil {
		t.Fatal("esperava erro com context cancelado")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("esperava context.DeadlineExceeded no erro, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("a espera do backoff deveria parar com o context, levou %v", elapsed)
	}
	if got := src.callCount.Load(); got != 1 {
		t.Errorf("esperava 1 chamada antes do cancelamento, got %d", got)
	}
}

func TestManager_Start_InitialSyncRetriesWithoutWaitingNextCycle(t *testing.T) {
	src := &flakySource{name: "flaky-initial", failures: 3}
	m := NewManager(WithRetryPolicy(fastRetry))
	m.Register(src)

	// O ticker da fonte é de 1h: só o retry pode completar a sync dentro do prazo.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	m.Start(ctx)

	if got := src.callCount.Load(); got != 4 {
		t.Errorf("esperava 4 chamadas (3 falhas + 1 sucesso), got %d", got)
	}
	if got := metricValue(t, observability.SyncRunsTotal.WithLabelValues(src.name, triggerInitial, "success")); got != 1 {
		t.Errorf("sync_runs_total success: esperava 1, got %v", got)
	}
}

func TestRetryPolicy_BackoffIsExponentialAndCapped(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 10, BaseDelay: 100 * time.Millisecond, MaxDelay: 1 * time.Second}.normalized()

	cases := []struct {
		attempt int
		full    time.Duration
	}{
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		{4, 800 * time.Millisecond},
		{5, 1 * time.Second},
		{9, 1 * time.Second},
	}
	for _, tc := range cases {
		for i := 0; i < 50; i++ {
			got := p.backoff(tc.attempt)
			if got < tc.full/2 || got > tc.full {
				t.Fatalf("attempt %d: espera %v fora de [%v, %v]", tc.attempt, got, tc.full/2, tc.full)
			}
		}
	}
}

func TestMetricsEndpoint_ExposesSyncMetrics(t *testing.T) {
	src := &flakySource{name: "flaky-metrics", failures: 1}
	m := NewManager(WithRetryPolicy(fastRetry))
	if err := m.syncWithRetry(context.Background(), src, triggerPeriodic); err != nil {
		t.Fatalf("sync: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/metrics", observability.MetricsHandler())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/metrics: status %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`catalogo_sync_runs_total{result="success",source="flaky-metrics",trigger="periodic"} 1`,
		`catalogo_sync_retries_total{source="flaky-metrics"} 1`,
		`catalogo_sync_last_success_timestamp_seconds{source="flaky-metrics"}`,
		`catalogo_sync_duration_seconds_count{source="flaky-metrics",type="periodic"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics não contém %q", want)
		}
	}
}

// metricValue lê o valor atual de um contador ou gauge.
func metricValue(t *testing.T, m prometheus.Metric) float64 {
	t.Helper()
	var out dto.Metric
	if err := m.Write(&out); err != nil {
		t.Fatalf("ler métrica: %v", err)
	}
	switch {
	case out.Counter != nil:
		return out.Counter.GetValue()
	case out.Gauge != nil:
		return out.Gauge.GetValue()
	default:
		t.Fatalf("métrica sem contador nem gauge: %v", m.Desc())
		return 0
	}
}

// TestWorkerMetricsServer_ExposesScheduledSyncMetrics reproduz o worker: as syncs
// agendadas rodam pelo Manager.Start e o /metrics é o do servidor próprio do worker.
func TestWorkerMetricsServer_ExposesScheduledSyncMetrics(t *testing.T) {
	src := &flakySource{name: "worker-scheduled", failures: 2}
	m := NewManager(WithRetryPolicy(fastRetry))
	m.Register(src)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	m.Start(ctx)

	srv := httptest.NewServer(observability.NewMetricsServer("").Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics: status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		`catalogo_sync_runs_total{result="success",source="worker-scheduled",trigger="initial"} 1`,
		`catalogo_sync_retries_total{source="worker-scheduled"} 2`,
		`catalogo_sync_last_success_timestamp_seconds{source="worker-scheduled"}`,
		`catalogo_sync_duration_seconds_count{source="worker-scheduled",type="initial"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics do worker não contém %q", want)
		}
	}

	// Só GET /metrics é servido.
	if r, err := http.Get(srv.URL + "/health"); err == nil {
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("/health no servidor de métricas: esperava 404, got %d", r.StatusCode)
		}
	}
}
