package observability

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
)

var (
	SearchDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "catalogo",
		Name:      "search_duration_seconds",
		Help:      "Latência das requisições de busca",
		Buckets:   []float64{.025, .05, .1, .25, .5, 1, 2.5},
	}, []string{"has_query", "type_filter"})

	RecommendationsDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "catalogo",
		Name:      "recommendations_duration_seconds",
		Help:      "Latência das requisições de recomendação",
		Buckets:   []float64{.025, .05, .1, .25, .5, 1},
	}, []string{"personalized"})

	SyncDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "catalogo",
		Name:      "sync_duration_seconds",
		Help:      "Duração das sincronizações por fonte",
		Buckets:   []float64{1, 5, 15, 30, 60, 120, 300},
	}, []string{"source", "type"})

	SyncRunsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "sync_runs_total",
		Help:      "Execuções de sincronização por fonte, contadas após as novas tentativas (result=success|failure)",
	}, []string{"source", "trigger", "result"})

	SyncRetriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "sync_retries_total",
		Help:      "Novas tentativas de sincronização após falha, por fonte",
	}, []string{"source"})

	SyncLastSuccessTimestamp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "catalogo",
		Name:      "sync_last_success_timestamp_seconds",
		Help:      "Horário (Unix, segundos) da última sincronização bem-sucedida por fonte",
	}, []string{"source"})

	SalesForceWebhookEventsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "salesforce_webhook_events_total",
		Help:      "Eventos do webhook SalesForce por resultado (enqueued|duplicate|processed|failed)",
	}, []string{"result"})

	HTTPClientCircuitState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "catalogo",
		Name:      "http_client_circuit_state",
		Help:      "Estado do circuit breaker por cliente HTTP (0=fechado, 1=meio-aberto, 2=aberto)",
	}, []string{"client"})

	HTTPClientRetriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "http_client_retries_total",
		Help:      "Total de novas tentativas (retries) por cliente HTTP",
	}, []string{"client"})
)

func init() {
	prometheus.MustRegister(
		SearchDuration,
		RecommendationsDuration,
		SyncDuration,
		SyncRunsTotal,
		SyncRetriesTotal,
		SyncLastSuccessTimestamp,
		SalesForceWebhookEventsTotal,
		HTTPClientCircuitState,
		HTTPClientRetriesTotal,
	)
}

// NewMetricsServer devolve um servidor HTTP que expõe só GET /metrics, numa
// porta própria: a do worker (syncs agendadas) e a da API (busca, recomendação,
// webhook, circuit breakers HTTP e syncs manuais). Na API, ele fica fora da porta
// pública do router, que o Service e o gateway levam para fora do cluster.
func NewMetricsServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// RequestLogger é um middleware Gin que loga requisições com zerolog.
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		event := log.Info()
		if status >= 500 {
			event = log.Error()
		} else if status >= 400 {
			event = log.Warn()
		}

		event.
			Str("method", c.Request.Method).
			Str("path", path).
			Int("status", status).
			Dur("latency", latency).
			Str("request_id", c.GetString("request_id")).
			Str("ip", c.ClientIP()).
			Msg("request")
	}
}

// rateLimitWindow é a janela fixa de contagem por IP.
const rateLimitWindow = time.Minute

// rateLimitMaxEntries limita quantos IPs o rate limiter acompanha ao mesmo
// tempo, para que o estado em memória não cresça sem limite numa API pública.
const rateLimitMaxEntries = 100_000

// RateLimitMiddleware implementa rate limiting por IP com contador em memória.
// Para múltiplas réplicas, substituir pela implementação Redis sliding window.
func RateLimitMiddleware(requestsPerMinute int) gin.HandlerFunc {
	return newRateLimiter(rateLimitMaxEntries, time.Now).middleware(requestsPerMinute)
}

type rateLimitEntry struct {
	count   int
	resetAt time.Time
}

// rateLimiter guarda um contador por IP numa janela fixa. As entradas com a
// janela vencida são removidas por uma varredura feita na própria requisição,
// no máximo uma vez por janela (sem goroutine), e o número de entradas tem um
// teto: cheio, uma entrada arbitrária dá lugar ao IP novo, em O(1).
type rateLimiter struct {
	mu         sync.Mutex
	maxEntries int
	now        func() time.Time
	counters   map[string]*rateLimitEntry
	nextSweep  time.Time
}

func newRateLimiter(maxEntries int, now func() time.Time) *rateLimiter {
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &rateLimiter{
		maxEntries: maxEntries,
		now:        now,
		counters:   make(map[string]*rateLimitEntry),
		nextSweep:  now().Add(rateLimitWindow),
	}
}

// hit conta uma requisição do IP e devolve o total na janela atual.
func (l *rateLimiter) hit(ip string) int {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if !now.Before(l.nextSweep) {
		l.sweep(now)
		l.nextSweep = now.Add(rateLimitWindow)
	}

	e, exists := l.counters[ip]
	if exists && !now.After(e.resetAt) {
		e.count++
		return e.count
	}
	if !exists && len(l.counters) >= l.maxEntries {
		l.evictOne()
	}
	l.counters[ip] = &rateLimitEntry{count: 1, resetAt: now.Add(rateLimitWindow)}
	return 1
}

// sweep remove as entradas cuja janela já venceu. Chamada com mu travado.
func (l *rateLimiter) sweep(now time.Time) {
	for ip, e := range l.counters {
		if now.After(e.resetAt) {
			delete(l.counters, ip)
		}
	}
}

// evictOne remove uma entrada qualquer do mapa. Chamada com mu travado.
func (l *rateLimiter) evictOne() {
	for ip := range l.counters {
		delete(l.counters, ip)
		return
	}
}

func (l *rateLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.counters)
}

func (l *rateLimiter) middleware(requestsPerMinute int) gin.HandlerFunc {
	return func(c *gin.Context) {
		count := l.hit(c.ClientIP())
		if count == 1 {
			c.Next()
			return
		}

		c.Header("X-RateLimit-Limit", strconv.Itoa(requestsPerMinute))
		remaining := requestsPerMinute - count
		if remaining < 0 {
			remaining = 0
		}
		c.Header("X-RateLimit-Remaining", strconv.Itoa(remaining))

		if count > requestsPerMinute {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit excedido"})
			c.Abort()
			return
		}

		c.Next()
	}
}
