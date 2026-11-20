package observability

import (
	"net/http"
	"strconv"
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

	SyncIntervalSeconds = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "catalogo",
		Name:      "sync_interval_seconds",
		Help:      "Intervalo (segundos) da sincronização periódica por fonte, publicado pelo processo que a agenda (worker)",
	}, []string{"source"})

	SyncScheduleStartTimestamp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "catalogo",
		Name:      "sync_schedule_start_timestamp_seconds",
		Help:      "Horário (Unix, segundos) em que a sincronização periódica da fonte começou neste processo; referência do atraso enquanto não há sucesso",
	}, []string{"source"})

	HTTPRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "http_requests_total",
		Help:      "Requisições HTTP atendidas pela API, por método, rota (modelo do Gin) e status",
	}, []string{"method", "route", "status"})

	HTTPRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "catalogo",
		Name:      "http_request_duration_seconds",
		Help:      "Latência das requisições HTTP da API, por método e rota (modelo do Gin)",
		Buckets:   []float64{.025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"method", "route"})

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

	GoroutinePanicsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "goroutine_panics_total",
		Help:      "Panics recuperados em goroutines soltas, por goroutine (o processo segue no ar)",
	}, []string{"goroutine"})
)

// collectors são todas as métricas do serviço; alerts_test.go confere contra
// elas as métricas citadas em alerts.yml.
var collectors = []prometheus.Collector{
	SearchDuration,
	RecommendationsDuration,
	SyncDuration,
	SyncRunsTotal,
	SyncRetriesTotal,
	SyncLastSuccessTimestamp,
	SyncIntervalSeconds,
	SyncScheduleStartTimestamp,
	HTTPRequestsTotal,
	HTTPRequestDuration,
	SalesForceWebhookEventsTotal,
	HTTPClientCircuitState,
	HTTPClientRetriesTotal,
	GoroutinePanicsTotal,
}

func init() {
	prometheus.MustRegister(collectors...)
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

// unmatchedRoute é o rótulo de rota das requisições que não casam com nenhuma
// rota registrada: o caminho cru viraria uma série por URL que o cliente inventar.
const unmatchedRoute = "unmatched"

// metricMethods são os métodos que viram rótulo; qualquer outro vira "OTHER",
// porque o método também vem do cliente e não pode criar séries à vontade.
var metricMethods = map[string]struct{}{
	http.MethodGet: {}, http.MethodHead: {}, http.MethodPost: {}, http.MethodPut: {},
	http.MethodPatch: {}, http.MethodDelete: {}, http.MethodOptions: {},
}

// HTTPMetrics é um middleware Gin que conta as requisições (HTTPRequestsTotal) e
// mede a latência (HTTPRequestDuration) por método, rota e status. A rota é o
// modelo registrado no Gin (ex.: /api/public/catalog/:id), nunca o caminho cru.
// Registre-o antes do gin.Recovery, para que o 500 de um panic recuperado entre
// na contagem.
func HTTPMetrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		route := c.FullPath()
		if route == "" {
			route = unmatchedRoute
		}
		method := c.Request.Method
		if _, ok := metricMethods[method]; !ok {
			method = "OTHER"
		}
		HTTPRequestsTotal.WithLabelValues(method, route, strconv.Itoa(c.Writer.Status())).Inc()
		HTTPRequestDuration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
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
