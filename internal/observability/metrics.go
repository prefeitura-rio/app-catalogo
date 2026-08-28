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

	SyncItemsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "sync_items_total",
		Help:      "Total de itens processados por fonte de dados",
	}, []string{"source", "status"})

	SyncDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "catalogo",
		Name:      "sync_duration_seconds",
		Help:      "Duração das sincronizações por fonte",
		Buckets:   []float64{1, 5, 15, 30, 60, 120, 300},
	}, []string{"source", "type"})

	CatalogItemsGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "catalogo",
		Name:      "items_total",
		Help:      "Total de itens ativos no catálogo por tipo",
	}, []string{"type", "source"})
)

func init() {
	prometheus.MustRegister(
		SearchDuration,
		RecommendationsDuration,
		SyncItemsTotal,
		SyncDuration,
		CatalogItemsGauge,
	)
}

// MetricsHandler expõe as métricas no formato Prometheus.
func MetricsHandler() gin.HandlerFunc {
	h := promhttp.Handler()
	return func(c *gin.Context) {
		h.ServeHTTP(c.Writer, c.Request)
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

// RateLimitMiddleware implementa rate limiting por IP com contador em memória.
// Para múltiplas réplicas, substituir pela implementação Redis sliding window.
func RateLimitMiddleware(requestsPerMinute int) gin.HandlerFunc {
	type entry struct {
		count   int
		resetAt time.Time
	}

	counters := make(map[string]*entry)
	var mu sync.Mutex

	return func(c *gin.Context) {
		ip := c.ClientIP()
		now := time.Now()

		mu.Lock()
		e, exists := counters[ip]
		if !exists || now.After(e.resetAt) {
			counters[ip] = &entry{count: 1, resetAt: now.Add(time.Minute)}
			mu.Unlock()
			c.Next()
			return
		}
		e.count++
		count := e.count
		mu.Unlock()

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
