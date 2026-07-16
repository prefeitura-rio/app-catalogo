package datasource

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

type stubSource struct {
	name      string
	callCount atomic.Int32
}

func (s *stubSource) Name() string               { return s.name }
func (s *stubSource) Source() models.ItemSource  { return models.ItemSource(s.name) }
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
