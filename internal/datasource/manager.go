package datasource

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Manager orquestra todas as fontes de dados registradas.
// Cada fonte roda em sua própria goroutine com seu próprio ticker.
type Manager struct {
	mu      sync.RWMutex
	sources []DataSource
}

func NewManager() *Manager {
	return &Manager{}
}

// Register adiciona uma fonte de dados ao manager.
// Deve ser chamado antes de Start().
func (m *Manager) Register(source DataSource) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sources = append(m.sources, source)
	log.Info().
		Str("source", source.Name()).
		Dur("interval", source.SyncInterval()).
		Msg("datasource: fonte registrada")
}

// Start inicia todas as fontes registradas. Bloqueia até o context ser cancelado.
func (m *Manager) Start(ctx context.Context) {
	m.mu.RLock()
	sources := make([]DataSource, len(m.sources))
	copy(sources, m.sources)
	m.mu.RUnlock()

	if len(sources) == 0 {
		log.Warn().Msg("datasource manager: nenhuma fonte registrada")
		<-ctx.Done()
		return
	}

	var wg sync.WaitGroup
	for _, src := range sources {
		wg.Add(1)
		go func(s DataSource) {
			defer wg.Done()
			runSource(ctx, s)
		}(src)
	}

	wg.Wait()
	log.Info().Msg("datasource manager: todas as fontes encerradas")
}

// TriggerAll dispara sync imediata em todas as fontes registradas em paralelo.
func (m *Manager) TriggerAll(ctx context.Context) {
	m.mu.RLock()
	sources := make([]DataSource, len(m.sources))
	copy(sources, m.sources)
	m.mu.RUnlock()

	for _, src := range sources {
		go func(s DataSource) {
			if err := s.Sync(ctx); err != nil {
				log.Error().Err(err).Str("source", s.Name()).Msg("datasource: sync manual (all) falhou")
			}
		}(src)
	}
}

// TriggerSync dispara uma sync imediata para uma fonte específica (por nome).
// Retorna false se a fonte não foi encontrada.
func (m *Manager) TriggerSync(ctx context.Context, sourceName string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, src := range m.sources {
		if src.Name() == sourceName || string(src.Source()) == sourceName {
			go func(s DataSource) {
				if err := s.Sync(ctx); err != nil {
					log.Error().Err(err).Str("source", s.Name()).Msg("datasource: sync manual falhou")
				}
			}(src)
			return true
		}
	}
	return false
}

func runSource(ctx context.Context, s DataSource) {
	log.Info().Str("source", s.Name()).Msg("datasource: iniciando")

	// Sync inicial na startup
	if err := s.Sync(ctx); err != nil {
		log.Error().Err(err).Str("source", s.Name()).Msg("datasource: erro na sync inicial")
	}

	ticker := time.NewTicker(s.SyncInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Str("source", s.Name()).Msg("datasource: encerrado")
			return
		case <-ticker.C:
			if err := s.Sync(ctx); err != nil {
				log.Error().Err(err).Str("source", s.Name()).Msg("datasource: erro no sync periódico")
			}
		}
	}
}
