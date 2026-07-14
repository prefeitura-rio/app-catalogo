package observability

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
)

const testRateLimitKeySecret = "rate-limit-test-key-with-enough-entropy"

type rateLimitStoreStub struct {
	count      atomic.Int64
	resetAfter time.Duration
	error      error
	keyMutex   sync.Mutex
	keys       []string
}

type deadlineRateLimitStoreStub struct {
	deadlineObserved atomic.Bool
}

type rateLimitClientIdentityVerifierStub struct {
	clientIdentifier string
	verified         bool
}

func (verifier rateLimitClientIdentityVerifierStub) VerifiedClientIdentifier(*http.Request) (string, bool) {
	return verifier.clientIdentifier, verifier.verified
}

func (store *deadlineRateLimitStoreStub) IncrementRateLimit(
	requestContext context.Context,
	_ string,
	_ time.Duration,
) (int64, time.Duration, error) {
	if _, deadlinePresent := requestContext.Deadline(); deadlinePresent {
		store.deadlineObserved.Store(true)
	}
	<-requestContext.Done()
	return 0, 0, requestContext.Err()
}

func (store *rateLimitStoreStub) IncrementRateLimit(
	_ context.Context,
	key string,
	_ time.Duration,
) (int64, time.Duration, error) {
	store.keyMutex.Lock()
	store.keys = append(store.keys, key)
	store.keyMutex.Unlock()
	if store.error != nil {
		return 0, 0, store.error
	}
	return store.count.Add(1), store.resetAfter, nil
}

func mustRateLimitMiddleware(
	testingContext *testing.T,
	store RateLimitStore,
	requestsPerWindow int,
	window time.Duration,
	redisTimeout time.Duration,
	clientIdentityVerifiers ...RateLimitClientIdentityVerifier,
) gin.HandlerFunc {
	testingContext.Helper()
	var clientIdentityVerifier RateLimitClientIdentityVerifier
	if len(clientIdentityVerifiers) > 0 {
		clientIdentityVerifier = clientIdentityVerifiers[0]
	}
	middlewareHandler, middlewareError := RateLimitMiddleware(
		store,
		requestsPerWindow,
		window,
		redisTimeout,
		testRateLimitKeySecret,
		clientIdentityVerifier,
	)
	if middlewareError != nil {
		testingContext.Fatalf("create rate-limit middleware: %v", middlewareError)
	}
	return middlewareHandler
}

func TestRateLimitMiddlewareRejectsWithLogIDAndHashedIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &rateLimitStoreStub{resetAfter: 1500 * time.Millisecond}
	router := gin.New()
	router.ForwardedByClientIP = false
	if trustedProxyError := router.SetTrustedProxies(nil); trustedProxyError != nil {
		t.Fatalf("disable trusted proxies: %v", trustedProxyError)
	}
	router.Use(middleware.RequestID())
	router.Use(mustRateLimitMiddleware(t, store, 2, time.Minute, 50*time.Millisecond))
	router.GET("/resource", func(context *gin.Context) {
		context.Status(http.StatusNoContent)
	})

	for requestNumber := 1; requestNumber <= 3; requestNumber++ {
		request := httptest.NewRequest(http.MethodGet, "/resource", nil)
		request.RemoteAddr = "203.0.113.8:43100"
		request.Header.Set("X-Forwarded-For", "198.51.100.9")
		responseRecorder := httptest.NewRecorder()
		router.ServeHTTP(responseRecorder, request)

		if requestNumber < 3 {
			if responseRecorder.Code != http.StatusNoContent {
				t.Fatalf("request %d status = %d, want %d", requestNumber, responseRecorder.Code, http.StatusNoContent)
			}
			continue
		}
		if responseRecorder.Code != http.StatusTooManyRequests {
			t.Fatalf("rejected status = %d, want %d", responseRecorder.Code, http.StatusTooManyRequests)
		}
		if responseRecorder.Header().Get("X-RateLimit-Limit") != "2" ||
			responseRecorder.Header().Get("X-RateLimit-Remaining") != "0" ||
			responseRecorder.Header().Get("Retry-After") != "2" {
			t.Fatalf("rate-limit headers = %#v", responseRecorder.Header())
		}

		var errorBody struct {
			Error string `json:"error"`
			LogID string `json:"log_id"`
		}
		if decodeError := json.Unmarshal(responseRecorder.Body.Bytes(), &errorBody); decodeError != nil {
			t.Fatalf("decode rejection: %v", decodeError)
		}
		if errorBody.Error == "" {
			t.Fatal("rate-limit rejection omitted error")
		}
		if _, parseError := uuid.Parse(errorBody.LogID); parseError != nil {
			t.Fatalf("log_id = %q, want UUID: %v", errorBody.LogID, parseError)
		}
	}

	store.keyMutex.Lock()
	defer store.keyMutex.Unlock()
	if len(store.keys) != 3 {
		t.Fatalf("stored keys = %d, want 3", len(store.keys))
	}
	for _, storedKey := range store.keys {
		if storedKey != rateLimitKey(testRateLimitKeySecret, 2, time.Minute, rateLimitClassOtherApplication, "203.0.113.8") {
			t.Fatalf("stored key = %q, want stable canonical client key", storedKey)
		}
		if strings.Contains(storedKey, "203.0.113.8") || strings.Contains(storedKey, "198.51.100.9") {
			t.Fatalf("stored key leaked a raw IP: %q", storedKey)
		}
	}
}

func TestRateLimitMiddlewareUsesOnlyVerifiedBFFClientIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clientIdentifier := strings.Repeat("A", 43)
	store := &rateLimitStoreStub{resetAfter: time.Minute}
	router := gin.New()
	router.ForwardedByClientIP = false
	if trustedProxyError := router.SetTrustedProxies(nil); trustedProxyError != nil {
		t.Fatalf("disable trusted proxies: %v", trustedProxyError)
	}
	router.Use(mustRateLimitMiddleware(
		t,
		store,
		5,
		time.Minute,
		50*time.Millisecond,
		rateLimitClientIdentityVerifierStub{clientIdentifier: clientIdentifier, verified: true},
	))
	router.POST("/api/public/search", func(context *gin.Context) {
		context.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/api/public/search", nil)
	request.RemoteAddr = "203.0.113.8:43100"
	responseRecorder := httptest.NewRecorder()
	router.ServeHTTP(responseRecorder, request)
	if responseRecorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusNoContent)
	}

	store.keyMutex.Lock()
	defer store.keyMutex.Unlock()
	if len(store.keys) != 1 {
		t.Fatalf("stored keys = %v, want one", store.keys)
	}
	wantKey := rateLimitKeyForIdentity(
		testRateLimitKeySecret,
		5,
		time.Minute,
		rateLimitClassPublic,
		"catalog-search-client:"+clientIdentifier,
	)
	if store.keys[0] != wantKey {
		t.Fatalf("stored key = %q, want verified client key", store.keys[0])
	}
	if store.keys[0] == rateLimitKey(testRateLimitKeySecret, 5, time.Minute, rateLimitClassPublic, "203.0.113.8") {
		t.Fatal("verified BFF request reused the shared egress IP key")
	}
}

func TestRateLimitMiddlewareFallsBackToCanonicalIPWhenBFFIdentityIsInvalid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	currentTime := time.Date(2027, time.January, 15, 12, 0, 0, 0, time.UTC)
	clientIdentityVerifier, verifierError := middleware.NewCatalogSearchClientVerifier(
		"catalog-search-internal-api-test-key-with-entropy",
		2*time.Minute,
		func() time.Time { return currentTime },
	)
	if verifierError != nil {
		t.Fatalf("create client identity verifier: %v", verifierError)
	}
	store := &rateLimitStoreStub{resetAfter: time.Minute}
	router := gin.New()
	router.ForwardedByClientIP = false
	if trustedProxyError := router.SetTrustedProxies(nil); trustedProxyError != nil {
		t.Fatalf("disable trusted proxies: %v", trustedProxyError)
	}
	router.Use(mustRateLimitMiddleware(
		t,
		store,
		5,
		time.Minute,
		50*time.Millisecond,
		clientIdentityVerifier,
	))
	router.POST("/api/public/search", func(context *gin.Context) {
		context.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/api/public/search", nil)
	request.RemoteAddr = "[::ffff:203.0.113.8]:43100"
	request.Header.Set(middleware.CatalogSearchClientIDHeader, strings.Repeat("A", 43))
	request.Header.Set(middleware.CatalogSearchClientTimestampHeader, strconv.FormatInt(currentTime.Unix(), 10))
	request.Header.Set(middleware.CatalogSearchClientSignatureHeader, strings.Repeat("A", 43))
	request.Header.Set(middleware.SearchIDHeader, "00000000-0000-4000-8000-000000000001")
	request.Header.Set(middleware.RequestIDHeader, "123456789")
	responseRecorder := httptest.NewRecorder()
	router.ServeHTTP(responseRecorder, request)

	store.keyMutex.Lock()
	defer store.keyMutex.Unlock()
	wantKey := rateLimitKey(testRateLimitKeySecret, 5, time.Minute, rateLimitClassPublic, "203.0.113.8")
	if len(store.keys) != 1 || store.keys[0] != wantKey {
		t.Fatalf("stored keys = %v, want canonical IP fallback", store.keys)
	}
}

func TestRateLimitMiddlewareUsesBoundedLocalProtectionWhenRedisIsUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &rateLimitStoreStub{error: errors.New("redis unavailable")}
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(mustRateLimitMiddleware(t, store, 1, time.Minute, 50*time.Millisecond))
	router.GET("/resource", func(context *gin.Context) {
		context.Status(http.StatusNoContent)
	})

	for requestNumber := 0; requestNumber < 2; requestNumber++ {
		responseRecorder := httptest.NewRecorder()
		router.ServeHTTP(responseRecorder, httptest.NewRequest(http.MethodGet, "/resource", nil))
		expectedStatus := http.StatusNoContent
		if requestNumber == 1 {
			expectedStatus = http.StatusTooManyRequests
		}
		if responseRecorder.Code != expectedStatus {
			t.Fatalf("local fallback status = %d, want %d", responseRecorder.Code, expectedStatus)
		}
		if responseRecorder.Header().Get("X-RateLimit-Remaining") == "" {
			t.Fatal("local fallback omitted remaining quota")
		}
	}
}

func TestRateLimitMiddlewareBoundsRedisLatency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &deadlineRateLimitStoreStub{}
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(mustRateLimitMiddleware(t, store, 1, time.Minute, 5*time.Millisecond))
	router.GET("/resource", func(context *gin.Context) {
		context.Status(http.StatusNoContent)
	})

	responseRecorder := httptest.NewRecorder()
	router.ServeHTTP(responseRecorder, httptest.NewRequest(http.MethodGet, "/resource", nil))
	if responseRecorder.Code != http.StatusNoContent {
		t.Fatalf("deadline fallback status = %d, want %d", responseRecorder.Code, http.StatusNoContent)
	}
	if !store.deadlineObserved.Load() {
		t.Fatal("rate-limit store did not receive a bounded context")
	}
}

func TestRateLimitKeyCanonicalizesEquivalentIPAddresses(t *testing.T) {
	compressedIPv6Key := rateLimitKey(testRateLimitKeySecret, 5, time.Minute, rateLimitClassPublic, "2001:db8::1")
	expandedIPv6Key := rateLimitKey(testRateLimitKeySecret, 5, time.Minute, rateLimitClassPublic, "2001:0db8:0000:0000:0000:0000:0000:0001")
	if compressedIPv6Key != expandedIPv6Key {
		t.Fatalf("equivalent IPv6 addresses produced different keys")
	}
	if rateLimitKey(testRateLimitKeySecret, 5, time.Minute, rateLimitClassPublic, "192.0.2.7") !=
		rateLimitKey(testRateLimitKeySecret, 5, time.Minute, rateLimitClassPublic, "::ffff:192.0.2.7") {
		t.Fatalf("IPv4 and IPv4-mapped IPv6 addresses produced different keys")
	}
	if rateLimitKey(testRateLimitKeySecret, 5, time.Minute, rateLimitClassPublic, "invalid-one") !=
		rateLimitKey(testRateLimitKeySecret, 5, time.Minute, rateLimitClassPublic, "invalid-two") {
		t.Fatalf("invalid client addresses did not use a bounded fallback identity")
	}
}

func TestRateLimitKeyChangesWithSecretAndPolicy(t *testing.T) {
	baseKey := rateLimitKey(testRateLimitKeySecret, 5, time.Minute, rateLimitClassPublic, "192.0.2.8")
	for testName, changedKey := range map[string]string{
		"secret": rateLimitKey("different-rate-limit-key-with-entropy", 5, time.Minute, rateLimitClassPublic, "192.0.2.8"),
		"limit":  rateLimitKey(testRateLimitKeySecret, 6, time.Minute, rateLimitClassPublic, "192.0.2.8"),
		"window": rateLimitKey(testRateLimitKeySecret, 5, 2*time.Minute, rateLimitClassPublic, "192.0.2.8"),
		"class":  rateLimitKey(testRateLimitKeySecret, 5, time.Minute, rateLimitClassAdmin, "192.0.2.8"),
	} {
		t.Run(testName, func(t *testing.T) {
			if changedKey == baseKey {
				t.Fatal("rate-limit key did not fingerprint the changed secret or policy")
			}
		})
	}
	if strings.Contains(baseKey, "192.0.2.8") || strings.Contains(baseKey, rateLimitClassPublic) {
		t.Fatalf("rate-limit key leaked client identity or policy class: %q", baseKey)
	}
}

func TestRateLimitKeySeparatesVerifiedClientsBehindSameBFFAddress(t *testing.T) {
	firstClientKey := rateLimitKeyForIdentity(
		testRateLimitKeySecret,
		5,
		time.Minute,
		rateLimitClassPublic,
		"catalog-search-client:"+strings.Repeat("A", 43),
	)
	secondClientKey := rateLimitKeyForIdentity(
		testRateLimitKeySecret,
		5,
		time.Minute,
		rateLimitClassPublic,
		"catalog-search-client:"+strings.Repeat("B", 43),
	)
	if firstClientKey == secondClientKey {
		t.Fatal("distinct verified clients shared a rate-limit key")
	}
}

func TestRateLimitEndpointClassSeparatesSensitiveRoutes(t *testing.T) {
	for requestPath, expectedClass := range map[string]string{
		"/api/webhooks/salesforce":  rateLimitClassWebhook,
		"/api/v1/admin/sync/status": rateLimitClassAdmin,
		"/api/v1/search":            rateLimitClassAuthenticated,
		"/api/public/search":        rateLimitClassPublic,
		"/api/unknown":              rateLimitClassOtherApplication,
	} {
		if endpointClass := rateLimitEndpointClass(requestPath); endpointClass != expectedClass {
			t.Fatalf("rateLimitEndpointClass(%q) = %q, want %q", requestPath, endpointClass, expectedClass)
		}
	}
}

func TestLocalRateLimitFallbackEvictsEntriesAtCapacity(t *testing.T) {
	currentTime := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	fallback := newLocalRateLimitFallback(2, func() time.Time { return currentTime })
	fallback.Increment("client-one", time.Minute)
	currentTime = currentTime.Add(time.Second)
	fallback.Increment("client-two", time.Minute)
	currentTime = currentTime.Add(time.Second)
	fallback.Increment("client-three", time.Minute)

	fallback.mutex.Lock()
	defer fallback.mutex.Unlock()
	if len(fallback.entries) != 2 {
		t.Fatalf("local fallback entries = %d, want bounded capacity", len(fallback.entries))
	}
	if _, oldestEntryPresent := fallback.entries["client-one"]; oldestEntryPresent {
		t.Fatal("local fallback did not evict the oldest entry at capacity")
	}
}

func TestRateLimitMiddlewareRejectsMissingKeySecret(t *testing.T) {
	if _, middlewareError := RateLimitMiddleware(nil, 1, time.Minute, time.Millisecond, "", nil); middlewareError == nil {
		t.Fatal("RateLimitMiddleware accepted a missing HMAC key secret")
	}
}

func TestRateLimitMiddlewareEnforcesLimitUnderConcurrency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const requestCount = 64
	const allowedRequestCount = 17
	store := &rateLimitStoreStub{resetAfter: time.Minute}
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(mustRateLimitMiddleware(t, store, allowedRequestCount, time.Minute, 50*time.Millisecond))
	router.GET("/resource", func(context *gin.Context) {
		context.Status(http.StatusNoContent)
	})

	var allowedResponses atomic.Int64
	var rejectedResponses atomic.Int64
	var waitGroup sync.WaitGroup
	for requestNumber := 0; requestNumber < requestCount; requestNumber++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			request := httptest.NewRequest(http.MethodGet, "/resource", nil)
			request.RemoteAddr = "203.0.113.10:43100"
			responseRecorder := httptest.NewRecorder()
			router.ServeHTTP(responseRecorder, request)
			switch responseRecorder.Code {
			case http.StatusNoContent:
				allowedResponses.Add(1)
			case http.StatusTooManyRequests:
				rejectedResponses.Add(1)
			default:
				t.Errorf("unexpected response status %d", responseRecorder.Code)
			}
		}()
	}
	waitGroup.Wait()

	if allowedResponses.Load() != allowedRequestCount {
		t.Fatalf("allowed responses = %d, want %d", allowedResponses.Load(), allowedRequestCount)
	}
	if rejectedResponses.Load() != requestCount-allowedRequestCount {
		t.Fatalf("rejected responses = %d, want %d", rejectedResponses.Load(), requestCount-allowedRequestCount)
	}
}
