package datasource

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/observability"
)

// Origens de uma sincronização, usadas no label trigger das métricas.
const (
	triggerInitial  = "initial"
	triggerPeriodic = "periodic"
	triggerManual   = "manual"
)

// SyncMode é o modo de uma sincronização manual.
type SyncMode string

const (
	// SyncModeDelta executa a sync normal da fonte (incremental quando há cursor).
	SyncModeDelta SyncMode = "delta"
	// SyncModeFull executa FullSync nas fontes que implementam FullSyncer; as
	// demais, que não têm modo completo distinto, executam a sync normal.
	SyncModeFull SyncMode = "full"
)

// ParseSyncMode converte o modo recebido na API; vazio equivale a delta.
func ParseSyncMode(v string) (SyncMode, error) {
	switch SyncMode(v) {
	case "", SyncModeDelta:
		return SyncModeDelta, nil
	case SyncModeFull:
		return SyncModeFull, nil
	default:
		return "", fmt.Errorf("modo de sync inválido %q: use %q ou %q", v, SyncModeDelta, SyncModeFull)
	}
}

// RetryPolicy define as novas tentativas de uma sincronização que falhou:
// a espera começa em BaseDelay e dobra a cada tentativa, limitada a MaxDelay,
// com jitter (entre metade e o valor cheio da espera).
type RetryPolicy struct {
	// MaxAttempts é o total de tentativas por execução (1 = sem retry).
	MaxAttempts int
	// BaseDelay é a espera antes da 2ª tentativa.
	BaseDelay time.Duration
	// MaxDelay limita a espera entre tentativas.
	MaxDelay time.Duration
}

// DefaultRetryPolicy cobre falhas transitórias (rede, 5xx, banco reiniciando)
// sem esperar o próximo ciclo de 15-30 min: 4 tentativas em ~3,5 min no pior caso.
var DefaultRetryPolicy = RetryPolicy{
	MaxAttempts: 4,
	BaseDelay:   30 * time.Second,
	MaxDelay:    5 * time.Minute,
}

func (p RetryPolicy) normalized() RetryPolicy {
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	if p.BaseDelay < 0 {
		p.BaseDelay = 0
	}
	if p.MaxDelay < p.BaseDelay {
		p.MaxDelay = p.BaseDelay
	}
	return p
}

// backoff devolve a espera antes da tentativa attempt+1 (attempt começa em 1).
func (p RetryPolicy) backoff(attempt int) time.Duration {
	d := p.BaseDelay
	for i := 1; i < attempt && d < p.MaxDelay; i++ {
		d *= 2
	}
	if d > p.MaxDelay {
		d = p.MaxDelay
	}
	if d <= 1 {
		return d
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(d-half)+1))
}

// Option configura o Manager.
type Option func(*Manager)

// WithRetryPolicy troca a política de novas tentativas das sincronizações.
func WithRetryPolicy(p RetryPolicy) Option {
	return func(m *Manager) { m.retry = p.normalized() }
}

// Manager orquestra todas as fontes de dados registradas.
// Cada fonte roda em sua própria goroutine com seu próprio ticker.
type Manager struct {
	mu      sync.RWMutex
	sources []DataSource
	retry   RetryPolicy
}

func NewManager(opts ...Option) *Manager {
	m := &Manager{retry: DefaultRetryPolicy.normalized()}
	for _, opt := range opts {
		opt(m)
	}
	return m
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

// SourceNames devolve o nome de cada fonte registrada, na ordem de registro.
// Sem fontes, devolve uma lista vazia (nunca nil).
func (m *Manager) SourceNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.sources))
	for _, src := range m.sources {
		names = append(names, src.Name())
	}
	return names
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
	m.TriggerAllMode(ctx, SyncModeDelta)
}

// TriggerAllMode dispara sync imediata, no modo pedido, em todas as fontes registradas em paralelo.
func (m *Manager) TriggerAllMode(ctx context.Context, mode SyncMode) {
	m.mu.RLock()
	sources := make([]DataSource, len(m.sources))
	copy(sources, m.sources)
	m.mu.RUnlock()

	for _, src := range sources {
		go func(s DataSource) {
			if err := m.syncWithRetryMode(ctx, s, triggerManual, mode); err != nil {
				log.Error().Err(err).Str("source", s.Name()).Msg("datasource: sync manual (all) falhou")
			}
		}(src)
	}
}

// TriggerSync dispara uma sync imediata para uma fonte específica (por nome).
// Retorna false se a fonte não foi encontrada.
func (m *Manager) TriggerSync(ctx context.Context, sourceName string) bool {
	return m.TriggerSyncMode(ctx, sourceName, SyncModeDelta)
}

// TriggerSyncMode dispara uma sync imediata, no modo pedido, para uma fonte
// específica (por nome). Retorna false se a fonte não foi encontrada.
func (m *Manager) TriggerSyncMode(ctx context.Context, sourceName string, mode SyncMode) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, src := range m.sources {
		if src.Name() == sourceName || string(src.Source()) == sourceName {
			go func(s DataSource) {
				if err := m.syncWithRetryMode(ctx, s, triggerManual, mode); err != nil {
					log.Error().Err(err).Str("source", s.Name()).Msg("datasource: sync manual falhou")
				}
			}(src)
			return true
		}
	}
	return false
}

func (m *Manager) runSource(ctx context.Context, s DataSource) {
	log.Info().Str("source", s.Name()).Msg("datasource: iniciando")

	// Sync inicial na startup
	if err := m.syncWithRetry(ctx, s, triggerInitial); err != nil {
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
			if err := m.syncWithRetry(ctx, s, triggerPeriodic); err != nil {
				log.Error().Err(err).Str("source", s.Name()).Msg("datasource: erro no sync periódico")
			}
		}
	}
}

// syncWithRetry executa s.Sync com novas tentativas (espera que dobra a cada falha) e
// registra as métricas da execução. Para quando o context é cancelado.
func (m *Manager) syncWithRetry(ctx context.Context, s DataSource, trigger string) error {
	return m.syncWithRetryMode(ctx, s, trigger, SyncModeDelta)
}

// syncFunc escolhe a operação do modo pedido: FullSync quando o modo é full e a
// fonte o implementa; Sync nos demais casos.
func syncFunc(s DataSource, mode SyncMode) func(context.Context) error {
	if mode == SyncModeFull {
		if fs, ok := s.(FullSyncer); ok {
			return fs.FullSync
		}
		log.Info().Str("source", s.Name()).Msg("datasource: fonte sem full sync dedicado, executando a sync normal")
	}
	return s.Sync
}

// syncWithRetryMode é syncWithRetry no modo pedido (ver syncFunc).
func (m *Manager) syncWithRetryMode(ctx context.Context, s DataSource, trigger string, mode SyncMode) error {
	policy := m.retry
	name := s.Name()
	run := syncFunc(s, mode)

	var err error
	for attempt := 1; ; attempt++ {
		started := time.Now()
		err = run(ctx)
		observability.SyncDuration.WithLabelValues(name, trigger).Observe(time.Since(started).Seconds())

		if err == nil {
			observability.SyncRunsTotal.WithLabelValues(name, trigger, "success").Inc()
			observability.SyncLastSuccessTimestamp.WithLabelValues(name).SetToCurrentTime()
			if attempt > 1 {
				log.Info().Str("source", name).Int("attempt", attempt).Msg("datasource: sync concluída após nova tentativa")
			}
			return nil
		}

		if attempt >= policy.MaxAttempts || ctx.Err() != nil {
			break
		}

		wait := policy.backoff(attempt)
		log.Warn().Err(err).
			Str("source", name).
			Str("trigger", trigger).
			Str("mode", string(mode)).
			Int("attempt", attempt).
			Int("max_attempts", policy.MaxAttempts).
			Dur("retry_in", wait).
			Msg("datasource: sync falhou, nova tentativa agendada")

		if !sleepCtx(ctx, wait) {
			break
		}
		observability.SyncRetriesTotal.WithLabelValues(name).Inc()
	}

	observability.SyncRunsTotal.WithLabelValues(name, trigger, "failure").Inc()
	if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
		return errors.Join(err, ctxErr)
	}
	return err
}

// sleepCtx espera d ou até o context ser cancelado; devolve false se cancelado.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
