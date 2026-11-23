package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/prefeitura-rio/app-catalogo/internal/observability"
)

func backfillPanics(t *testing.T) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := observability.GoroutinePanicsTotal.WithLabelValues(backfillGoroutine).Write(m); err != nil {
		t.Fatalf("lendo a métrica: %v", err)
	}
	return m.GetCounter().GetValue()
}

// Sem o recover, o panic da passagem (resposta do Gemini malformada) derruba o
// binário de teste inteiro, como derrubaria o worker.
func TestRunEmbeddingBackfill_PanicKeepsLoopRunning(t *testing.T) {
	var calls atomic.Int32
	pass := func(context.Context) int {
		if n := calls.Add(1); n <= 2 {
			panic("resposta do Gemini malformada")
		}
		return 1
	}
	before := backfillPanics(t)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runEmbeddingBackfill(ctx, 20*time.Millisecond, pass)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("o backfill deveria encerrar quando o context é cancelado")
	}

	// 1ª passagem (inicial) e 2ª (1º tick) com panic; o laço segue nos ticks seguintes.
	if got := calls.Load(); got < 3 {
		t.Fatalf("esperava o backfill seguir depois dos panics (>= 3 passagens), got %d", got)
	}
	if got := backfillPanics(t) - before; got != 2 {
		t.Errorf("goroutine_panics_total{goroutine=%s}: esperava +2, got %+v", backfillGoroutine, got)
	}
}

func TestBackfillPass_ReturnsProcessedWithoutPanic(t *testing.T) {
	before := backfillPanics(t)
	n, ok := backfillPass(context.Background(), func(context.Context) int { return 7 })
	if !ok || n != 7 {
		t.Fatalf("esperava (7, true), got (%d, %v)", n, ok)
	}
	if got := backfillPanics(t); got != before {
		t.Errorf("métrica mudou sem panic: %v -> %v", before, got)
	}

	n, ok = backfillPass(context.Background(), func(context.Context) int { panic("falha simulada") })
	if ok || n != 0 {
		t.Fatalf("com panic, esperava (0, false), got (%d, %v)", n, ok)
	}
}
