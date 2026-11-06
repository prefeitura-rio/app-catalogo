package observability

import (
	"net/http"
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
