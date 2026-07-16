package datasource

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

type stubSource struct {
	name      string
	callCount atomic.Int32
	err       error
}

func (s *stubSource) Name() string                { return s.name }
func (s *stubSource) Source() models.ItemSource   { return models.ItemSource(s.name) }
func (s *stubSource) SyncInterval() time.Duration { return time.Hour } // ticker longo: não dispara em teste
func (s *stubSource) Sync(_ context.Context) error {
	s.callCount.Add(1)
	return s.err
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

func TestManager_SyncHookRunsAfterSuccessfulSync(t *testing.T) {
	m := NewManager()
	s := &stubSource{name: "app-go-api"}
	hookCalled := make(chan string, 1)

	m.Register(s)
	m.AddSyncHook(func(_ context.Context, source DataSource) error {
		hookCalled <- source.Name()
		return nil
	})

	if !m.TriggerSync(context.Background(), "app-go-api") {
		t.Fatal("TriggerSync deveria retornar true para 'app-go-api'")
	}

	select {
	case got := <-hookCalled:
		if got != "app-go-api" {
			t.Fatalf("hook recebeu source %q, esperava app-go-api", got)
		}
	case <-time.After(time.Second):
		t.Fatal("hook não foi chamado")
	}
}

func TestManager_SyncHookDoesNotRunAfterFailedSync(t *testing.T) {
	m := NewManager()
	s := &stubSource{name: "app-go-api", err: errors.New("sync failed")}
	hookCalled := make(chan string, 1)

	m.Register(s)
	m.AddSyncHook(func(_ context.Context, source DataSource) error {
		hookCalled <- source.Name()
		return nil
	})

	if !m.TriggerSync(context.Background(), "app-go-api") {
		t.Fatal("TriggerSync deveria retornar true para 'app-go-api'")
	}

	select {
	case got := <-hookCalled:
		t.Fatalf("hook não deveria ser chamado, recebeu source %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}
