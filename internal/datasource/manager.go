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
	mu        sync.RWMutex
	sources   []DataSource
	syncHooks []SyncHook
}

type SyncHook func(ctx context.Context, source DataSource) error

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

// AddSyncHook registra uma ação executada após uma sync bem-sucedida.
func (m *Manager) AddSyncHook(hook SyncHook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncHooks = append(m.syncHooks, hook)
}

// HasSource reports whether a source name or canonical source identifier is
// registered. It is safe to call while manual triggers are being served.
func (m *Manager) HasSource(sourceName string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, source := range m.sources {
		if matchesSource(source, sourceName) {
			return true
		}
	}
	return false
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
			m.runSource(ctx, s)
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
			m.syncSource(ctx, s, "datasource: sync manual (all) falhou")
		}(src)
	}
}

// TriggerSync dispara uma sync imediata para uma fonte específica (por nome).
// Retorna false se a fonte não foi encontrada.
func (m *Manager) TriggerSync(ctx context.Context, sourceName string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, src := range m.sources {
		if matchesSource(src, sourceName) {
			go func(s DataSource) {
				m.syncSource(ctx, s, "datasource: sync manual falhou")
			}(src)
			return true
		}
	}
	return false
}

func matchesSource(source DataSource, sourceName string) bool {
	return source.Name() == sourceName || string(source.Source()) == sourceName
}

func (m *Manager) runSource(ctx context.Context, s DataSource) {
	log.Info().Str("source", s.Name()).Msg("datasource: iniciando")

	// Sync inicial na startup
	m.syncSource(ctx, s, "datasource: erro na sync inicial")

	ticker := time.NewTicker(s.SyncInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Str("source", s.Name()).Msg("datasource: encerrado")
			return
		case <-ticker.C:
			m.syncSource(ctx, s, "datasource: erro no sync periódico")
		}
	}
}

func (m *Manager) syncSource(ctx context.Context, s DataSource, errorMsg string) {
	changed, err := s.Sync(ctx)
	// Uma fonte pode confirmar alterações em um subconjunto e ainda retornar
	// erro para outro. O cache precisa ser invalidado para toda mutação efetiva,
	// mesmo quando a execução global é reportada como parcial/falha.
	if changed > 0 {
		m.runSyncHooks(ctx, s)
	}
	if err != nil {
		log.Error().Err(err).Str("source", s.Name()).Msg(errorMsg)
		return
	}
}

func (m *Manager) runSyncHooks(ctx context.Context, source DataSource) {
	m.mu.RLock()
	hooks := make([]SyncHook, len(m.syncHooks))
	copy(hooks, m.syncHooks)
	m.mu.RUnlock()

	for _, hook := range hooks {
		if err := hook(ctx, source); err != nil {
			log.Warn().Err(err).Str("source", source.Name()).Msg("datasource: sync hook falhou")
		}
	}
}
