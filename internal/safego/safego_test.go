package safego

import (
	"errors"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/prefeitura-rio/app-catalogo/internal/observability"
)

func panicCount(t *testing.T, name string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := observability.GoroutinePanicsTotal.WithLabelValues(name).Write(m); err != nil {
		t.Fatalf("lendo a métrica: %v", err)
	}
	return m.GetCounter().GetValue()
}

func waitPanics(t *testing.T, name string, want float64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if panicCount(t, name) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("panic da goroutine %q não foi contado (esperado >= %v)", name, want)
}

// Sem o recover, o panic numa goroutine solta derruba o binário de teste
// inteiro, como derrubaria a API.
func TestGo_RecoversPanicAndKeepsProcessAlive(t *testing.T) {
	const name = "teste_go_panic"
	before := panicCount(t, name)

	Go(name, func() { panic("falha simulada") })
	waitPanics(t, name, before+1)

	done := make(chan struct{})
	Go(name+"_ok", func() { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine sem panic deveria rodar normalmente")
	}
}

func TestRecover_DeliversPanicAsError(t *testing.T) {
	errCh := make(chan error, 1)
	go func() {
		defer Recover("teste_recover_erro", func(err error) { errCh <- err })
		var m map[string]int
		m["x"] = 1 // panic: escrita em map nil
	}()

	select {
	case err := <-errCh:
		var pe *PanicError
		if !errors.As(err, &pe) {
			t.Fatalf("esperado *PanicError, got %T: %v", err, err)
		}
		if pe.Goroutine != "teste_recover_erro" || pe.Value == nil {
			t.Fatalf("PanicError incompleto: %+v", pe)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("o panic deveria ser entregue como erro")
	}
}

func TestRecover_NoPanicDoesNothing(t *testing.T) {
	const name = "teste_sem_panic"
	before := panicCount(t, name)
	called := false
	func() {
		defer Recover(name, func(error) { called = true })
	}()
	if called {
		t.Fatal("onPanic não pode ser chamado sem panic")
	}
	if got := panicCount(t, name); got != before {
		t.Fatalf("métrica mudou sem panic: %v -> %v", before, got)
	}
}
