package clients

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/observability"
)

// ErrCircuitOpen indica que o circuit breaker do cliente está aberto e a
// chamada foi recusada sem tocar o serviço remoto.
var ErrCircuitOpen = errors.New("circuit breaker aberto")

// CircuitState é o estado do circuit breaker. O valor numérico é o exposto na
// métrica catalogo_http_client_circuit_state.
type CircuitState int

const (
	CircuitClosed   CircuitState = 0
	CircuitHalfOpen CircuitState = 1
	CircuitOpen     CircuitState = 2
)

func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitHalfOpen:
		return "half-open"
	case CircuitOpen:
		return "open"
	default:
		return "unknown"
	}
}

// ResilienceConfig define o retry (espera que dobra a cada tentativa, com jitter) e o
// circuit breaker de um cliente HTTP.
type ResilienceConfig struct {
	// MaxAttempts é o total de tentativas por chamada (1 = sem retry).
	MaxAttempts int
	// BaseDelay é a espera antes da 2ª tentativa; dobra a cada nova tentativa.
	BaseDelay time.Duration
	// MaxDelay limita a espera entre tentativas.
	MaxDelay time.Duration
	// FailureThreshold é o número de falhas transitórias consecutivas que abre o circuito.
	FailureThreshold int
	// OpenTimeout é quanto tempo o circuito fica aberto antes de deixar passar
	// uma chamada de teste (meio-aberto).
	OpenTimeout time.Duration
}

// O RMI está no caminho síncrono de GET /api/v1/recommendations: poucas
// tentativas, espera curta e circuito que abre cedo, para a recomendação cair
// rápido na genérica quando o serviço está fora.
var defaultRMIResilience = ResilienceConfig{
	MaxAttempts:      3,
	BaseDelay:        100 * time.Millisecond,
	MaxDelay:         1 * time.Second,
	FailureThreshold: 5,
	OpenTimeout:      30 * time.Second,
}

// O app-go-api é consumido pelo sync em background: aceita esperar mais entre
// tentativas.
var defaultAppGoAPIResilience = ResilienceConfig{
	MaxAttempts:      3,
	BaseDelay:        500 * time.Millisecond,
	MaxDelay:         5 * time.Second,
	FailureThreshold: 5,
	OpenTimeout:      60 * time.Second,
}

// transientError marca uma falha que vale repetir e que conta para o circuit
// breaker: erro de transporte, timeout, 5xx ou 429.
type transientError struct {
	err error
}

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

func markTransient(err error) error {
	if err == nil {
		return nil
	}
	return &transientError{err: err}
}

func isTransient(err error) bool {
	var target *transientError
	return errors.As(err, &target)
}

// isTransientStatus diz se um status HTTP indica indisponibilidade
// momentânea do serviço remoto.
func isTransientStatus(statusCode int) bool {
	return statusCode >= http.StatusInternalServerError || statusCode == http.StatusTooManyRequests
}

// circuitBreaker é um breaker clássico fechado → aberto → meio-aberto.
// Fechado: as chamadas passam e falhas transitórias consecutivas são contadas.
// Aberto: as chamadas são recusadas com ErrCircuitOpen até OpenTimeout.
// Meio-aberto: uma única chamada de teste passa; sucesso fecha, falha reabre.
type circuitBreaker struct {
	client           string
	failureThreshold int
	openTimeout      time.Duration
	now              func() time.Time

	mu            sync.Mutex
	state         CircuitState
	failures      int
	openedAt      time.Time
	probeInFlight bool
}

func newCircuitBreaker(client string, failureThreshold int, openTimeout time.Duration) *circuitBreaker {
	if failureThreshold < 1 {
		failureThreshold = 1
	}
	breaker := &circuitBreaker{
		client:           client,
		failureThreshold: failureThreshold,
		openTimeout:      openTimeout,
		now:              time.Now,
		state:            CircuitClosed,
	}
	observability.HTTPClientCircuitState.WithLabelValues(client).Set(float64(CircuitClosed))
	return breaker
}

// State retorna o estado atual.
func (b *circuitBreaker) State() CircuitState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// allow decide se a chamada pode seguir. Retorna ErrCircuitOpen quando não pode.
func (b *circuitBreaker) allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case CircuitOpen:
		if b.now().Sub(b.openedAt) < b.openTimeout {
			return ErrCircuitOpen
		}
		b.setStateLocked(CircuitHalfOpen)
		b.probeInFlight = true
		return nil
	case CircuitHalfOpen:
		if b.probeInFlight {
			return ErrCircuitOpen
		}
		b.probeInFlight = true
		return nil
	default:
		return nil
	}
}

// onSuccess registra que o serviço respondeu (inclusive com 4xx).
func (b *circuitBreaker) onSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.probeInFlight = false
	b.setStateLocked(CircuitClosed)
}

// onFailure registra uma falha transitória do serviço.
func (b *circuitBreaker) onFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probeInFlight = false
	if b.state == CircuitHalfOpen {
		b.openLocked()
		return
	}
	b.failures++
	if b.failures >= b.failureThreshold {
		b.openLocked()
	}
}

// onAbandon libera a chamada sem julgar o serviço: quem chamou desistiu
// (contexto cancelado ou expirado) antes da resposta.
func (b *circuitBreaker) onAbandon() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probeInFlight = false
}

func (b *circuitBreaker) openLocked() {
	b.failures = 0
	b.openedAt = b.now()
	b.setStateLocked(CircuitOpen)
}

func (b *circuitBreaker) setStateLocked(state CircuitState) {
	b.state = state
	observability.HTTPClientCircuitState.WithLabelValues(b.client).Set(float64(state))
}

// resilientCaller aplica retry com backoff que dobra, jitter e o circuit
// breaker a uma operação HTTP de um cliente.
type resilientCaller struct {
	client  string
	config  ResilienceConfig
	breaker *circuitBreaker
	jitter  func(maximum time.Duration) time.Duration
}

func newResilientCaller(client string, config ResilienceConfig) *resilientCaller {
	if config.MaxAttempts < 1 {
		config.MaxAttempts = 1
	}
	observability.HTTPClientRetriesTotal.WithLabelValues(client).Add(0)
	return &resilientCaller{
		client:  client,
		config:  config,
		breaker: newCircuitBreaker(client, config.FailureThreshold, config.OpenTimeout),
		jitter:  randomJitter,
	}
}

func randomJitter(maximum time.Duration) time.Duration {
	if maximum <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(maximum) + 1))
}

// backoff calcula a espera depois da tentativa `attempt` (1-based):
// metade fixa de min(MaxDelay, BaseDelay·2^(attempt-1)) mais jitter aleatório
// na outra metade ("equal jitter"), para espalhar as tentativas das réplicas.
func (r *resilientCaller) backoff(attempt int) time.Duration {
	delay := r.config.BaseDelay
	for i := 1; i < attempt && delay < r.config.MaxDelay; i++ {
		delay *= 2
	}
	if r.config.MaxDelay > 0 && delay > r.config.MaxDelay {
		delay = r.config.MaxDelay
	}
	half := delay / 2
	return half + r.jitter(delay-half)
}

// do executa op com retry e circuit breaker. Só falhas marcadas como
// transitórias são repetidas e contam para abrir o circuito; um erro não
// transitório (4xx, resposta inválida) volta na hora e conta como serviço
// disponível. Com o circuito aberto, retorna um erro que envolve ErrCircuitOpen
// sem chamar op.
func (r *resilientCaller) do(ctx context.Context, op func(context.Context) error) error {
	for attempt := 1; ; attempt++ {
		if err := r.breaker.allow(); err != nil {
			return fmt.Errorf("%s: %w", r.client, err)
		}

		err := op(ctx)
		switch {
		case err == nil:
			r.breaker.onSuccess()
			return nil
		case ctx.Err() != nil:
			r.breaker.onAbandon()
			return err
		case !isTransient(err):
			r.breaker.onSuccess()
			return err
		default:
			r.breaker.onFailure()
		}

		if attempt >= r.config.MaxAttempts {
			return err
		}

		timer := time.NewTimer(r.backoff(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
		observability.HTTPClientRetriesTotal.WithLabelValues(r.client).Inc()
	}
}
