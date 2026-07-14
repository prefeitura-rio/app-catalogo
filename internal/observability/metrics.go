package observability

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
)

const (
	rateLimitKeyPrefix             = "catalogo:rate-limit:v3:policy:"
	localRateLimitMaximumClients   = 10_000
	rateLimitClassWebhook          = "webhook"
	rateLimitClassAdmin            = "admin"
	rateLimitClassAuthenticated    = "authenticated"
	rateLimitClassPublic           = "public"
	rateLimitClassOtherApplication = "application"
)

var rateLimitBackendFailureSampler = &zerolog.BurstSampler{
	Burst:  1,
	Period: time.Minute,
}

var (
	SearchDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "catalogo",
		Name:      "search_duration_seconds",
		Help:      "Search request latency grouped by the legacy label contract",
		Buckets:   []float64{.025, .05, .1, .25, .5, 1, 2.5},
	}, []string{"has_query", "type_filter"})

	SearchPipelineDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "catalogo",
		Name:      "search_pipeline_duration_seconds",
		Help:      "End-to-end search request latency by retrieval pipeline",
		Buckets:   []float64{.025, .05, .1, .25, .5, 1, 2.5},
	}, []string{"pipeline", "has_type_filter"})

	SearchRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "search_requests_total",
		Help:      "Search requests by bounded pipeline and outcome",
	}, []string{"pipeline", "outcome"})

	SearchCandidates = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "catalogo",
		Name:      "search_candidates",
		Help:      "Number of fused candidates before pagination",
		Buckets:   []float64{0, 1, 5, 10, 20, 40, 80, 120, 200, 400},
	}, []string{"pipeline"})

	SearchCacheOperationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "search_cache_operations_total",
		Help:      "Search cache operations by bounded outcome",
	}, []string{"operation", "outcome"})

	SearchFallbacksTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "search_fallbacks_total",
		Help:      "Search pipeline fallbacks without user query labels",
	}, []string{"source", "destination", "reason"})

	SearchReranksTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "search_reranks_total",
		Help:      "Cross-encoder reranking attempts by bounded outcome",
	}, []string{"outcome"})

	SearchZeroResultsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "search_zero_results_total",
		Help:      "Successful searches with no candidates",
	}, []string{"pipeline"})

	SearchExternalSourceRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "search_external_source_requests_total",
		Help:      "External candidate source calls by bounded source and outcome",
	}, []string{"source", "outcome"})

	SearchExternalSourceDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "catalogo",
		Name:      "search_external_source_duration_seconds",
		Help:      "External candidate source latency by bounded source and outcome",
		Buckets:   []float64{.025, .05, .1, .25, .5, 1, 2.5, 5},
	}, []string{"source", "outcome"})

	SearchExternalCandidates = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "catalogo",
		Name:      "search_external_candidates",
		Help:      "External candidates received and eligible for canonical ranking",
		Buckets:   []float64{0, 1, 5, 10, 20, 40, 50},
	}, []string{"source", "stage"})

	SearchSummaryRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "search_summary_requests_total",
		Help:      "Grounded search summary requests by bounded outcome",
	}, []string{"outcome"})

	SearchSummaryDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "catalogo",
		Name:      "search_summary_duration_seconds",
		Help:      "Grounded search summary latency by bounded outcome",
		Buckets:   []float64{.025, .05, .1, .25, .5, 1, 2.5, 5, 10, 20},
	}, []string{"outcome"})

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

	RateLimitDecisionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "catalogo",
		Name:      "rate_limit_decisions_total",
		Help:      "Rate-limit decisions by bounded outcome",
	}, []string{"outcome"})
)

func init() {
	prometheus.MustRegister(
		SearchDuration,
		SearchPipelineDuration,
		SearchRequestsTotal,
		SearchCandidates,
		SearchCacheOperationsTotal,
		SearchFallbacksTotal,
		SearchReranksTotal,
		SearchZeroResultsTotal,
		SearchExternalSourceRequestsTotal,
		SearchExternalSourceDuration,
		SearchExternalCandidates,
		SearchSummaryRequestsTotal,
		SearchSummaryDuration,
		RecommendationsDuration,
		SyncItemsTotal,
		SyncDuration,
		CatalogItemsGauge,
		RateLimitDecisionsTotal,
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
			Str("upstream_request_id", c.GetString(middleware.UpstreamRequestIDKey)).
			Str("search_id", c.GetString(middleware.SearchIDKey)).
			Msg("request")
	}
}

type RateLimitStore interface {
	IncrementRateLimit(ctx context.Context, key string, window time.Duration) (int64, time.Duration, error)
}

type RateLimitClientIdentityVerifier interface {
	VerifiedClientIdentifier(request *http.Request) (string, bool)
}

type localRateLimitEntry struct {
	count     int64
	expiresAt time.Time
}

type localRateLimitFallback struct {
	mutex          sync.Mutex
	entries        map[string]localRateLimitEntry
	maximumEntries int
	now            func() time.Time
}

func newLocalRateLimitFallback(maximumEntries int, now func() time.Time) *localRateLimitFallback {
	return &localRateLimitFallback{
		entries:        make(map[string]localRateLimitEntry, maximumEntries),
		maximumEntries: maximumEntries,
		now:            now,
	}
}

func (fallback *localRateLimitFallback) Increment(key string, window time.Duration) (int64, time.Duration) {
	now := fallback.now()
	fallback.mutex.Lock()
	defer fallback.mutex.Unlock()

	if existingEntry, exists := fallback.entries[key]; exists && now.Before(existingEntry.expiresAt) {
		existingEntry.count++
		fallback.entries[key] = existingEntry
		return existingEntry.count, existingEntry.expiresAt.Sub(now)
	}

	if len(fallback.entries) >= fallback.maximumEntries {
		fallback.evictExpiredOrOldest(now)
	}
	expiresAt := now.Add(window)
	fallback.entries[key] = localRateLimitEntry{count: 1, expiresAt: expiresAt}
	return 1, window
}

func (fallback *localRateLimitFallback) evictExpiredOrOldest(now time.Time) {
	var oldestKey string
	var oldestExpiration time.Time
	for clientKey, clientEntry := range fallback.entries {
		if !now.Before(clientEntry.expiresAt) {
			delete(fallback.entries, clientKey)
			continue
		}
		if oldestKey == "" || clientEntry.expiresAt.Before(oldestExpiration) {
			oldestKey = clientKey
			oldestExpiration = clientEntry.expiresAt
		}
	}
	if len(fallback.entries) >= fallback.maximumEntries && oldestKey != "" {
		delete(fallback.entries, oldestKey)
	}
}

// RateLimitMiddleware enforces a shared fixed-window policy through Redis.
// Backend failures degrade to an in-process, resource-bounded fixed window.
func RateLimitMiddleware(
	store RateLimitStore,
	requestsPerWindow int,
	window time.Duration,
	redisTimeout time.Duration,
	keySecret string,
	clientIdentityVerifier RateLimitClientIdentityVerifier,
) (gin.HandlerFunc, error) {
	if requestsPerWindow <= 0 || window <= 0 || redisTimeout <= 0 {
		return nil, errors.New("rate-limit policy must use positive limits and durations")
	}
	if strings.TrimSpace(keySecret) == "" {
		return nil, errors.New("rate-limit key secret is required")
	}

	localFallback := newLocalRateLimitFallback(localRateLimitMaximumClients, time.Now)
	return func(c *gin.Context) {
		c.Header("X-RateLimit-Limit", strconv.Itoa(requestsPerWindow))
		clientKey := rateLimitKeyForIdentity(
			keySecret,
			requestsPerWindow,
			window,
			rateLimitEndpointClass(c.Request.URL.Path),
			rateLimitClientIdentity(c, clientIdentityVerifier),
		)

		redisContext, cancelRedisRequest := context.WithTimeout(c.Request.Context(), redisTimeout)
		defer cancelRedisRequest()
		var count int64
		var resetAfter time.Duration
		var incrementError error
		if store == nil {
			incrementError = errors.New("redis rate-limit store is not configured")
		} else {
			count, resetAfter, incrementError = store.IncrementRateLimit(
				redisContext,
				clientKey,
				window,
			)
		}
		if incrementError != nil {
			sampledLogger := log.Logger.Sample(rateLimitBackendFailureSampler)
			sampledLogger.Warn().
				Str("error_type", fmt.Sprintf("%T", incrementError)).
				Str("request_id", c.GetString("request_id")).
				Msg("redis rate limit unavailable; local protection active")
			count, resetAfter = localFallback.Increment(clientKey, window)
			setRateLimitRemaining(c, requestsPerWindow, count)
			if count > int64(requestsPerWindow) {
				rejectRateLimited(c, resetAfter, "backend_error_local_rejected")
				return
			}
			RateLimitDecisionsTotal.WithLabelValues("backend_error_local_allowed").Inc()
			c.Next()
			return
		}

		setRateLimitRemaining(c, requestsPerWindow, count)

		if count > int64(requestsPerWindow) {
			rejectRateLimited(c, resetAfter, "rejected")
			return
		}

		RateLimitDecisionsTotal.WithLabelValues("allowed").Inc()
		c.Next()
	}, nil
}

func setRateLimitRemaining(context *gin.Context, requestsPerWindow int, count int64) {
	remaining := int64(requestsPerWindow) - count
	if remaining < 0 {
		remaining = 0
	}
	context.Header("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
}

func rejectRateLimited(context *gin.Context, resetAfter time.Duration, outcome string) {
	RateLimitDecisionsTotal.WithLabelValues(outcome).Inc()
	context.Header("Retry-After", retryAfterSeconds(resetAfter))
	context.JSON(http.StatusTooManyRequests, gin.H{
		"error":  "rate limit excedido",
		"log_id": context.GetString("request_id"),
	})
	context.Abort()
}

func rateLimitKey(
	keySecret string,
	requestsPerWindow int,
	window time.Duration,
	endpointClass string,
	clientIP string,
) string {
	canonicalClientIP := "invalid"
	if parsedClientIP, parseError := netip.ParseAddr(strings.TrimSpace(clientIP)); parseError == nil {
		canonicalClientIP = parsedClientIP.Unmap().String()
	}
	return rateLimitKeyForIdentity(
		keySecret,
		requestsPerWindow,
		window,
		endpointClass,
		"ip:"+canonicalClientIP,
	)
}

func rateLimitKeyForIdentity(
	keySecret string,
	requestsPerWindow int,
	window time.Duration,
	endpointClass string,
	clientIdentity string,
) string {
	policyInput := fmt.Sprintf(
		"fixed-window-v3|%d|%d|%s",
		requestsPerWindow,
		window.Nanoseconds(),
		endpointClass,
	)
	policyFingerprint := sha256.Sum256([]byte(policyInput))
	clientIdentifier := hmac.New(sha256.New, []byte(keySecret))
	_, _ = clientIdentifier.Write([]byte(clientIdentity))
	return rateLimitKeyPrefix + hex.EncodeToString(policyFingerprint[:]) + ":client:" +
		hex.EncodeToString(clientIdentifier.Sum(nil))
}

func rateLimitClientIdentity(
	context *gin.Context,
	clientIdentityVerifier RateLimitClientIdentityVerifier,
) string {
	if clientIdentityVerifier != nil {
		if verifiedClientIdentifier, verified := clientIdentityVerifier.VerifiedClientIdentifier(context.Request); verified && verifiedClientIdentifier != "" {
			return "catalog-search-client:" + verifiedClientIdentifier
		}
	}
	canonicalClientIP := "invalid"
	if parsedClientIP, parseError := netip.ParseAddr(strings.TrimSpace(context.ClientIP())); parseError == nil {
		canonicalClientIP = parsedClientIP.Unmap().String()
	}
	return "ip:" + canonicalClientIP
}

func rateLimitEndpointClass(requestPath string) string {
	switch {
	case hasPathPrefix(requestPath, "/api/webhooks"):
		return rateLimitClassWebhook
	case hasPathPrefix(requestPath, "/api/v1/admin"):
		return rateLimitClassAdmin
	case hasPathPrefix(requestPath, "/api/v1"):
		return rateLimitClassAuthenticated
	case hasPathPrefix(requestPath, "/api/public"):
		return rateLimitClassPublic
	default:
		return rateLimitClassOtherApplication
	}
}

func hasPathPrefix(requestPath string, prefix string) bool {
	return requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/")
}

func retryAfterSeconds(resetAfter time.Duration) string {
	seconds := resetAfter / time.Second
	if resetAfter%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return strconv.FormatInt(int64(seconds), 10)
}
