package clients

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/prefeitura-rio/app-catalogo/internal/observability"
)

// fastResilience mantém os testes rápidos: esperas de milissegundos e circuito
// que só fecha por tempo quando o teste avança o relógio.
func fastResilience(threshold int) ResilienceConfig {
	return ResilienceConfig{
		MaxAttempts:      3,
		BaseDelay:        time.Millisecond,
		MaxDelay:         2 * time.Millisecond,
		FailureThreshold: threshold,
		OpenTimeout:      time.Hour,
	}
}

func uniqueClientName(t *testing.T) string {
	t.Helper()
	return "test-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
}

func retriesMetric(client string) float64 {
	var metric dto.Metric
	if err := observability.HTTPClientRetriesTotal.WithLabelValues(client).Write(&metric); err != nil {
		panic(err)
	}
	return metric.GetCounter().GetValue()
}

func circuitMetric(client string) float64 {
	var metric dto.Metric
	if err := observability.HTTPClientCircuitState.WithLabelValues(client).Write(&metric); err != nil {
		panic(err)
	}
	return metric.GetGauge().GetValue()
}

var errTransientTest = markTransient(errors.New("serviço indisponível"))

func TestResilientCaller_BackoffExponencialComJitter(t *testing.T) {
	t.Parallel()

	caller := newResilientCaller(uniqueClientName(t), ResilienceConfig{
		MaxAttempts: 5, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second,
		FailureThreshold: 5, OpenTimeout: time.Second,
	})

	// Jitter mínimo e máximo: a espera fica em [delay/2, delay].
	expected := map[int]time.Duration{1: 100, 2: 200, 3: 400, 4: 800, 5: 1000, 9: 1000}
	for attempt, delayMs := range expected {
		delay := delayMs * time.Millisecond
		caller.jitter = func(time.Duration) time.Duration { return 0 }
		if got := caller.backoff(attempt); got != delay/2 {
			t.Errorf("tentativa %d sem jitter: got %v, want %v", attempt, got, delay/2)
		}
		caller.jitter = func(maximum time.Duration) time.Duration { return maximum }
		if got := caller.backoff(attempt); got != delay {
			t.Errorf("tentativa %d com jitter máximo: got %v, want %v", attempt, got, delay)
		}
	}

	// Jitter real fica dentro da faixa e varia entre chamadas.
	caller.jitter = randomJitter
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		got := caller.backoff(3)
		if got < 200*time.Millisecond || got > 400*time.Millisecond {
			t.Fatalf("backoff(3) fora de [200ms, 400ms]: %v", got)
		}
		seen[got] = true
	}
	if len(seen) < 2 {
		t.Fatalf("jitter não variou em 50 chamadas")
	}
}

func TestResilientCaller_RepeteFalhaTransitoriaAteSucesso(t *testing.T) {
	t.Parallel()

	name := uniqueClientName(t)
	caller := newResilientCaller(name, fastResilience(5))

	var calls int
	err := caller.do(context.Background(), func(context.Context) error {
		calls++
		if calls < 3 {
			return errTransientTest
		}
		return nil
	})
	if err != nil {
		t.Fatalf("esperava sucesso na 3ª tentativa, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("esperava 3 tentativas, got %d", calls)
	}
	if got := retriesMetric(name); got != 2 {
		t.Fatalf("catalogo_http_client_retries_total=%v, want 2", got)
	}
	if caller.breaker.State() != CircuitClosed {
		t.Fatalf("circuito deveria seguir fechado, got %s", caller.breaker.State())
	}
}

func TestResilientCaller_DesisteDepoisDeMaxAttempts(t *testing.T) {
	t.Parallel()

	name := uniqueClientName(t)
	caller := newResilientCaller(name, fastResilience(10))

	var calls int
	err := caller.do(context.Background(), func(context.Context) error {
		calls++
		return errTransientTest
	})
	if !errors.Is(err, errTransientTest) {
		t.Fatalf("esperava o último erro transitório, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("esperava 3 tentativas, got %d", calls)
	}
	if got := retriesMetric(name); got != 2 {
		t.Fatalf("retries=%v, want 2", got)
	}
}

func TestResilientCaller_ErroNaoTransitorioNaoRepeteNemAbreCircuito(t *testing.T) {
	t.Parallel()

	name := uniqueClientName(t)
	caller := newResilientCaller(name, fastResilience(1))
	permanent := errors.New("status 400")

	for i := 0; i < 5; i++ {
		var calls int
		err := caller.do(context.Background(), func(context.Context) error {
			calls++
			return permanent
		})
		if !errors.Is(err, permanent) {
			t.Fatalf("esperava o erro permanente, got %v", err)
		}
		if calls != 1 {
			t.Fatalf("erro não transitório não deve repetir, got %d tentativas", calls)
		}
	}
	if caller.breaker.State() != CircuitClosed {
		t.Fatalf("4xx não deve abrir o circuito, got %s", caller.breaker.State())
	}
	if got := retriesMetric(name); got != 0 {
		t.Fatalf("retries=%v, want 0", got)
	}
}

func TestCircuitBreaker_AbreRecusaSemChamarEFechaPorTeste(t *testing.T) {
	t.Parallel()

	name := uniqueClientName(t)
	cfg := fastResilience(3)
	cfg.MaxAttempts = 1
	caller := newResilientCaller(name, cfg)
	now := time.Now()
	caller.breaker.now = func() time.Time { return now }

	failing := func(context.Context) error { return errTransientTest }
	for i := 0; i < 3; i++ {
		if err := caller.do(context.Background(), failing); !errors.Is(err, errTransientTest) {
			t.Fatalf("falha %d: got %v", i, err)
		}
	}
	if caller.breaker.State() != CircuitOpen {
		t.Fatalf("esperava circuito aberto depois de 3 falhas, got %s", caller.breaker.State())
	}
	if got := circuitMetric(name); got != float64(CircuitOpen) {
		t.Fatalf("catalogo_http_client_circuit_state=%v, want %v", got, float64(CircuitOpen))
	}

	var called bool
	err := caller.do(context.Background(), func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("esperava ErrCircuitOpen, got %v", err)
	}
	if called {
		t.Fatalf("com o circuito aberto a operação não pode ser chamada")
	}

	// Passado o OpenTimeout, uma chamada de teste passa; falha reabre.
	now = now.Add(cfg.OpenTimeout)
	if err := caller.do(context.Background(), failing); !errors.Is(err, errTransientTest) {
		t.Fatalf("chamada de teste deveria passar e falhar, got %v", err)
	}
	if caller.breaker.State() != CircuitOpen {
		t.Fatalf("falha no meio-aberto deve reabrir, got %s", caller.breaker.State())
	}

	// Novo OpenTimeout: chamada de teste com sucesso fecha o circuito.
	now = now.Add(cfg.OpenTimeout)
	if err := caller.do(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("chamada de teste com sucesso: %v", err)
	}
	if caller.breaker.State() != CircuitClosed {
		t.Fatalf("sucesso no meio-aberto deve fechar, got %s", caller.breaker.State())
	}
	if got := circuitMetric(name); got != float64(CircuitClosed) {
		t.Fatalf("catalogo_http_client_circuit_state=%v, want 0", got)
	}
}

func TestCircuitBreaker_MeioAbertoDeixaPassarUmaChamadaPorVez(t *testing.T) {
	t.Parallel()

	cfg := fastResilience(1)
	cfg.MaxAttempts = 1
	caller := newResilientCaller(uniqueClientName(t), cfg)
	now := time.Now()
	caller.breaker.now = func() time.Time { return now }

	_ = caller.do(context.Background(), func(context.Context) error { return errTransientTest })
	if caller.breaker.State() != CircuitOpen {
		t.Fatalf("esperava aberto, got %s", caller.breaker.State())
	}
	now = now.Add(cfg.OpenTimeout)

	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = caller.do(context.Background(), func(context.Context) error {
			close(probeStarted)
			<-releaseProbe
			return nil
		})
	}()
	<-probeStarted

	var concurrentCalls atomic.Int32
	err := caller.do(context.Background(), func(context.Context) error {
		concurrentCalls.Add(1)
		return nil
	})
	if !errors.Is(err, ErrCircuitOpen) || concurrentCalls.Load() != 0 {
		t.Fatalf("segunda chamada no meio-aberto deveria ser recusada, err=%v calls=%d", err, concurrentCalls.Load())
	}

	close(releaseProbe)
	wg.Wait()
	if caller.breaker.State() != CircuitClosed {
		t.Fatalf("esperava fechado depois do teste com sucesso, got %s", caller.breaker.State())
	}
}

func TestResilientCaller_ContextoCanceladoInterrompeSemAbrirCircuito(t *testing.T) {
	t.Parallel()

	cfg := fastResilience(1)
	cfg.BaseDelay = time.Hour
	cfg.MaxDelay = time.Hour
	caller := newResilientCaller(uniqueClientName(t), cfg)

	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	done := make(chan error, 1)
	go func() {
		done <- caller.do(ctx, func(context.Context) error {
			calls++
			return errTransientTest
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, errTransientTest) {
			t.Fatalf("esperava o último erro, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("do não respeitou o cancelamento durante o backoff")
	}
	if calls != 1 {
		t.Fatalf("esperava 1 tentativa, got %d", calls)
	}

	// Chamada abandonada pelo chamador não conta como falha do serviço.
	abandon := newResilientCaller(uniqueClientName(t)+"-abandon", fastResilience(1))
	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	_ = abandon.do(cancelled, func(context.Context) error { return errTransientTest })
	if abandon.breaker.State() != CircuitClosed {
		t.Fatalf("cancelamento do chamador não deve abrir o circuito, got %s", abandon.breaker.State())
	}
}

func TestMetricas_ExpostasPorCliente(t *testing.T) {
	_ = NewRMIClient("http://rmi.invalid", nil)
	_ = NewAppGoAPIClient("http://appgoapi.invalid", nil)

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	want := map[string]map[string]bool{
		"catalogo_http_client_circuit_state": {"rmi": false, "appgoapi": false},
		"catalogo_http_client_retries_total": {"rmi": false, "appgoapi": false},
	}
	for _, family := range families {
		clients, ok := want[family.GetName()]
		if !ok {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "client" {
					if _, tracked := clients[label.GetValue()]; tracked {
						clients[label.GetValue()] = true
					}
				}
			}
		}
	}
	for metricName, clients := range want {
		for client, found := range clients {
			if !found {
				t.Errorf("%s{client=%q} não exposta", metricName, client)
			}
		}
	}
}
