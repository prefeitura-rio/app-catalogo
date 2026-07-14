package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
	"github.com/prefeitura-rio/app-catalogo/internal/config"
)

type countingRateLimitStore struct {
	calls atomic.Int64
}

type rejectingCatalogSearchClientVerifier struct{}

type unusedJWTVerifier struct{}

func (unusedJWTVerifier) Verify(context.Context, string) (*middleware.VerifiedJWTClaims, error) {
	return nil, nil
}

func (rejectingCatalogSearchClientVerifier) VerifiedClientIdentifier(*http.Request) (string, bool) {
	return "", false
}

func (store *countingRateLimitStore) IncrementRateLimit(
	_ context.Context,
	_ string,
	window time.Duration,
) (int64, time.Duration, error) {
	return store.calls.Add(1), window, nil
}

func TestConfigureTrustedProxiesIgnoresForwardedHeadersByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if configurationError := configureTrustedProxies(router, nil); configurationError != nil {
		t.Fatalf("configure no trusted proxies: %v", configurationError)
	}
	router.GET("/client-ip", func(context *gin.Context) {
		context.String(http.StatusOK, context.ClientIP())
	})

	request := httptest.NewRequest(http.MethodGet, "/client-ip", nil)
	request.RemoteAddr = "203.0.113.9:43100"
	request.Header.Set("X-Forwarded-For", "198.51.100.8")
	responseRecorder := httptest.NewRecorder()
	router.ServeHTTP(responseRecorder, request)

	if responseRecorder.Body.String() != "203.0.113.9" {
		t.Fatalf("client IP = %q, want direct peer", responseRecorder.Body.String())
	}
}

func TestConfigureTrustedProxiesAcceptsHeaderOnlyFromConfiguredPeer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if configurationError := configureTrustedProxies(router, []string{"203.0.113.0/24"}); configurationError != nil {
		t.Fatalf("configure trusted proxy: %v", configurationError)
	}
	router.GET("/client-ip", func(context *gin.Context) {
		context.String(http.StatusOK, context.ClientIP())
	})

	trustedRequest := httptest.NewRequest(http.MethodGet, "/client-ip", nil)
	trustedRequest.RemoteAddr = "203.0.113.9:43100"
	trustedRequest.Header.Set("X-Forwarded-For", "198.51.100.8")
	trustedResponseRecorder := httptest.NewRecorder()
	router.ServeHTTP(trustedResponseRecorder, trustedRequest)
	if trustedResponseRecorder.Body.String() != "198.51.100.8" {
		t.Fatalf("trusted client IP = %q, want forwarded client", trustedResponseRecorder.Body.String())
	}

	untrustedRequest := httptest.NewRequest(http.MethodGet, "/client-ip", nil)
	untrustedRequest.RemoteAddr = "192.0.2.7:43100"
	untrustedRequest.Header.Set("X-Forwarded-For", "198.51.100.8")
	untrustedResponseRecorder := httptest.NewRecorder()
	router.ServeHTTP(untrustedResponseRecorder, untrustedRequest)
	if untrustedResponseRecorder.Body.String() != "192.0.2.7" {
		t.Fatalf("untrusted client IP = %q, want direct peer", untrustedResponseRecorder.Body.String())
	}
}

func TestConfigureTrustedProxiesRejectsMalformedNetwork(t *testing.T) {
	if configurationError := configureTrustedProxies(gin.New(), []string{"not-a-network"}); configurationError == nil {
		t.Fatal("configureTrustedProxies accepted malformed network")
	}
}

func TestSetupRouterExemptsInfrastructureEndpointsFromRateLimiting(t *testing.T) {
	rateLimitStore := &countingRateLimitStore{}
	router, setupError := SetupRouter(&config.AppConfig{
		App: config.AppSettings{Environment: "test"},
		Server: config.ServerSettings{
			RequestTimeout: time.Second,
		},
		Tracing: config.TracingSettings{ServiceName: "app-catalogo-test"},
		JWT:     config.JWTSettings{RoleClientID: "superapp"},
		RateLimit: config.RateLimitSettings{
			RequestsPerWindow: 5,
			Window:            time.Minute,
			RedisTimeout:      50 * time.Millisecond,
			KeySecret:         "rate-limit-router-test-key-with-entropy",
		},
	}, nil, RouterDeps{
		RateLimitStore:              rateLimitStore,
		CatalogSearchClientVerifier: rejectingCatalogSearchClientVerifier{},
		JWTVerifier:                 unusedJWTVerifier{},
	})
	if setupError != nil {
		t.Fatalf("setup router: %v", setupError)
	}

	for _, infrastructurePath := range []string{"/health", "/metrics"} {
		responseRecorder := httptest.NewRecorder()
		router.ServeHTTP(responseRecorder, httptest.NewRequest(http.MethodGet, infrastructurePath, nil))
		if responseRecorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", infrastructurePath, responseRecorder.Code, http.StatusOK)
		}
	}
	if rateLimitStore.calls.Load() != 0 {
		t.Fatalf("infrastructure endpoints consumed %d rate-limit slots", rateLimitStore.calls.Load())
	}

	applicationResponseRecorder := httptest.NewRecorder()
	router.ServeHTTP(applicationResponseRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/search", nil))
	if rateLimitStore.calls.Load() != 1 {
		t.Fatalf("application endpoint consumed %d rate-limit slots, want 1", rateLimitStore.calls.Load())
	}
}

func TestSetupRouterDoesNotRegisterSalesForceWebhookWhenSourceIsDisabled(t *testing.T) {
	router, setupError := SetupRouter(&config.AppConfig{
		App: config.AppSettings{Environment: "test"},
		Server: config.ServerSettings{
			RequestTimeout: time.Second,
		},
		Tracing: config.TracingSettings{ServiceName: "app-catalogo-test"},
		JWT:     config.JWTSettings{RoleClientID: "superapp"},
		RateLimit: config.RateLimitSettings{
			RequestsPerWindow: 5,
			Window:            time.Minute,
			RedisTimeout:      50 * time.Millisecond,
			KeySecret:         "rate-limit-router-test-key-with-entropy",
		},
	}, nil, RouterDeps{
		RateLimitStore:              &countingRateLimitStore{},
		CatalogSearchClientVerifier: rejectingCatalogSearchClientVerifier{},
		JWTVerifier:                 unusedJWTVerifier{},
	})
	if setupError != nil {
		t.Fatalf("setup router: %v", setupError)
	}

	responseRecorder := httptest.NewRecorder()
	router.ServeHTTP(responseRecorder, httptest.NewRequest(http.MethodPost, "/api/webhooks/salesforce", nil))
	if responseRecorder.Code != http.StatusNotFound {
		t.Fatalf("disabled Salesforce webhook status = %d, want %d", responseRecorder.Code, http.StatusNotFound)
	}
}

func TestSetupRouterRejectsMissingCatalogSearchClientVerifier(t *testing.T) {
	_, setupError := SetupRouter(&config.AppConfig{
		App: config.AppSettings{Environment: "test"},
		Server: config.ServerSettings{
			RequestTimeout: time.Second,
		},
		Tracing: config.TracingSettings{ServiceName: "app-catalogo-test"},
		JWT:     config.JWTSettings{RoleClientID: "superapp"},
		RateLimit: config.RateLimitSettings{
			RequestsPerWindow: 5,
			Window:            time.Minute,
			RedisTimeout:      50 * time.Millisecond,
			KeySecret:         "rate-limit-router-test-key-with-entropy",
		},
	}, nil, RouterDeps{
		RateLimitStore: &countingRateLimitStore{},
		JWTVerifier:    unusedJWTVerifier{},
	})
	if setupError == nil {
		t.Fatal("SetupRouter accepted a missing catalog search client verifier")
	}
}

func TestSetupRouterRejectsMissingJWTVerifier(t *testing.T) {
	_, setupError := SetupRouter(&config.AppConfig{
		App:     config.AppSettings{Environment: "test"},
		Server:  config.ServerSettings{RequestTimeout: time.Second},
		Tracing: config.TracingSettings{ServiceName: "app-catalogo-test"},
		JWT:     config.JWTSettings{RoleClientID: "superapp"},
		RateLimit: config.RateLimitSettings{
			RequestsPerWindow: 5,
			Window:            time.Minute,
			RedisTimeout:      50 * time.Millisecond,
			KeySecret:         "rate-limit-router-test-key-with-entropy",
		},
	}, nil, RouterDeps{
		RateLimitStore:              &countingRateLimitStore{},
		CatalogSearchClientVerifier: rejectingCatalogSearchClientVerifier{},
	})
	if setupError == nil {
		t.Fatal("SetupRouter accepted a missing JWT verifier")
	}
}

func TestSetupRouterRejectsEnabledSalesForceWithoutWebhookSecret(t *testing.T) {
	_, setupError := SetupRouter(&config.AppConfig{
		App: config.AppSettings{Environment: "test"},
		Server: config.ServerSettings{
			RequestTimeout: time.Second,
		},
		Tracing: config.TracingSettings{ServiceName: "app-catalogo-test"},
		JWT:     config.JWTSettings{RoleClientID: "superapp"},
		RateLimit: config.RateLimitSettings{
			RequestsPerWindow: 5,
			Window:            time.Minute,
			RedisTimeout:      50 * time.Millisecond,
			KeySecret:         "rate-limit-router-test-key-with-entropy",
		},
		SalesForce: config.SalesForceSettings{InstanceURL: "https://example.my.salesforce.com"},
	}, nil, RouterDeps{
		RateLimitStore:              &countingRateLimitStore{},
		CatalogSearchClientVerifier: rejectingCatalogSearchClientVerifier{},
		JWTVerifier:                 unusedJWTVerifier{},
	})
	if setupError == nil {
		t.Fatal("SetupRouter accepted enabled Salesforce without a webhook secret")
	}
}
