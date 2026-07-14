package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testCatalogSearchInternalAPIKey = "catalog-search-internal-api-test-key-with-entropy"

var testCatalogSearchCurrentTime = time.Date(2027, time.January, 15, 12, 0, 0, 0, time.UTC)

func TestCatalogSearchClientVerifierAcceptsFreshCanonicalSignature(t *testing.T) {
	verifier := newCatalogSearchClientVerifierForTest(t, 2*time.Minute)
	request := signedCatalogSearchRequest(testCatalogSearchCurrentTime)
	if clientIdentifier := request.Header.Get(CatalogSearchClientIDHeader); clientIdentifier !=
		"nwz9lGjssy9THhOHbllwqBuVOgrxuxasNbKTv5J7i_Y" {
		t.Fatalf("client identifier = %q, want shared Node.js test vector", clientIdentifier)
	}
	if signature := request.Header.Get(CatalogSearchClientSignatureHeader); signature !=
		"4grISAnjQa1va8boSQxT0wW46ERWpeAQCa6iepXuTmY" {
		t.Fatalf("signature = %q, want shared Node.js test vector", signature)
	}

	clientIdentifier, verified := verifier.VerifiedClientIdentifier(request)

	if !verified {
		t.Fatal("fresh canonical request was not verified")
	}
	if clientIdentifier != request.Header.Get(CatalogSearchClientIDHeader) {
		t.Fatalf("verified client identifier = %q, want signed value", clientIdentifier)
	}
}

func TestCatalogSearchClientVerifierAcceptsSignedDistributedLogIdentifier(t *testing.T) {
	verifier := newCatalogSearchClientVerifierForTest(t, 2*time.Minute)
	request := signedCatalogSearchRequest(testCatalogSearchCurrentTime)
	requestIdentifier := maximumDistributedLogID
	request.Header.Set(RequestIDHeader, requestIdentifier)
	request.Header.Set(
		CatalogSearchClientSignatureHeader,
		catalogSearchRequestSignature(
			testCatalogSearchInternalAPIKey,
			request.Header.Get(CatalogSearchClientIDHeader),
			request.Header.Get(CatalogSearchClientTimestampHeader),
			request.Header.Get(SearchIDHeader),
			requestIdentifier,
		),
	)

	if _, verified := verifier.VerifiedClientIdentifier(request); !verified {
		t.Fatal("signed distributed log identifier was not verified")
	}
}

func TestCatalogSearchClientVerifierRejectsTamperedSignedFields(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		headerName string
		value      string
	}{
		{name: "client identifier", headerName: CatalogSearchClientIDHeader, value: catalogSearchClientIdentifier("different-client")},
		{name: "timestamp", headerName: CatalogSearchClientTimestampHeader, value: strconv.FormatInt(testCatalogSearchCurrentTime.Add(time.Second).Unix(), 10)},
		{name: "signature", headerName: CatalogSearchClientSignatureHeader, value: strings.Repeat("A", 43)},
		{name: "search identifier", headerName: SearchIDHeader, value: "00000000-0000-4000-8000-000000000002"},
		{name: "request identifier", headerName: RequestIDHeader, value: "123456790"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			verifier := newCatalogSearchClientVerifierForTest(t, 2*time.Minute)
			request := signedCatalogSearchRequest(testCatalogSearchCurrentTime)
			request.Header.Set(testCase.headerName, testCase.value)

			if _, verified := verifier.VerifiedClientIdentifier(request); verified {
				t.Fatalf("verifier accepted tampered %s", testCase.name)
			}
		})
	}
}

func TestCatalogSearchClientVerifierEnforcesPastAndFutureSkew(t *testing.T) {
	verifier := newCatalogSearchClientVerifierForTest(t, 2*time.Minute)
	for _, testCase := range []struct {
		name       string
		signedAt   time.Time
		wantVerify bool
	}{
		{name: "past boundary", signedAt: testCatalogSearchCurrentTime.Add(-2 * time.Minute), wantVerify: true},
		{name: "future boundary", signedAt: testCatalogSearchCurrentTime.Add(2 * time.Minute), wantVerify: true},
		{name: "expired", signedAt: testCatalogSearchCurrentTime.Add(-2*time.Minute - time.Second)},
		{name: "too far in future", signedAt: testCatalogSearchCurrentTime.Add(2*time.Minute + time.Second)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, verified := verifier.VerifiedClientIdentifier(signedCatalogSearchRequest(testCase.signedAt))
			if verified != testCase.wantVerify {
				t.Fatalf("verified = %t, want %t", verified, testCase.wantVerify)
			}
		})
	}
}

func TestCatalogSearchClientVerifierRejectsIncompleteAmbiguousOrWrongRouteHeaders(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{name: "missing signature", mutate: func(request *http.Request) { request.Header.Del(CatalogSearchClientSignatureHeader) }},
		{name: "duplicate client identifier", mutate: func(request *http.Request) {
			request.Header.Add(CatalogSearchClientIDHeader, request.Header.Get(CatalogSearchClientIDHeader))
		}},
		{name: "noncanonical timestamp", mutate: func(request *http.Request) { request.Header.Set(CatalogSearchClientTimestampHeader, "01800000000") }},
		{name: "noncanonical request identifier", mutate: func(request *http.Request) { request.Header.Set(RequestIDHeader, "0123456789") }},
		{name: "noncanonical search identifier", mutate: func(request *http.Request) {
			request.Header.Set(SearchIDHeader, "{00000000-0000-4000-8000-000000000001}")
		}},
		{name: "wrong method", mutate: func(request *http.Request) { request.Method = http.MethodGet }},
		{name: "wrong path", mutate: func(request *http.Request) { request.URL.Path = "/api/v1/search" }},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			verifier := newCatalogSearchClientVerifierForTest(t, 2*time.Minute)
			request := signedCatalogSearchRequest(testCatalogSearchCurrentTime)
			testCase.mutate(request)
			if _, verified := verifier.VerifiedClientIdentifier(request); verified {
				t.Fatalf("verifier accepted %s request", testCase.name)
			}
		})
	}
}

func TestNewCatalogSearchClientVerifierRejectsUnsafeConfiguration(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		secret      string
		maximumSkew time.Duration
		now         func() time.Time
	}{
		{name: "short secret", secret: "short", maximumSkew: time.Minute, now: time.Now},
		{name: "unicode secret", secret: strings.Repeat("é", 32), maximumSkew: time.Minute, now: time.Now},
		{name: "space in secret", secret: "catalog search internal api key with entropy", maximumSkew: time.Minute, now: time.Now},
		{name: "zero skew", secret: testCatalogSearchInternalAPIKey, now: time.Now},
		{name: "unbounded skew", secret: testCatalogSearchInternalAPIKey, maximumSkew: 11 * time.Minute, now: time.Now},
		{name: "missing clock", secret: testCatalogSearchInternalAPIKey, maximumSkew: time.Minute},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, constructorError := NewCatalogSearchClientVerifier(
				testCase.secret,
				testCase.maximumSkew,
				testCase.now,
			); constructorError == nil {
				t.Fatalf("constructor accepted %s", testCase.name)
			}
		})
	}
}

func newCatalogSearchClientVerifierForTest(
	testingContext *testing.T,
	maximumSkew time.Duration,
) *CatalogSearchClientVerifier {
	testingContext.Helper()
	verifier, constructorError := NewCatalogSearchClientVerifier(
		testCatalogSearchInternalAPIKey,
		maximumSkew,
		func() time.Time { return testCatalogSearchCurrentTime },
	)
	if constructorError != nil {
		testingContext.Fatalf("create verifier: %v", constructorError)
	}
	return verifier
}

func signedCatalogSearchRequest(signedAt time.Time) *http.Request {
	clientIdentifier := catalogSearchClientIdentifier("client-address")
	timestamp := strconv.FormatInt(signedAt.Unix(), 10)
	searchIdentifier := "00000000-0000-4000-8000-000000000001"
	requestIdentifier := "123456789"
	request := httptest.NewRequest(http.MethodPost, catalogSearchPublicPath, nil)
	request.Header.Set(CatalogSearchClientIDHeader, clientIdentifier)
	request.Header.Set(CatalogSearchClientTimestampHeader, timestamp)
	request.Header.Set(SearchIDHeader, searchIdentifier)
	request.Header.Set(RequestIDHeader, requestIdentifier)
	request.Header.Set(
		CatalogSearchClientSignatureHeader,
		catalogSearchRequestSignature(
			testCatalogSearchInternalAPIKey,
			clientIdentifier,
			timestamp,
			searchIdentifier,
			requestIdentifier,
		),
	)
	return request
}

func catalogSearchClientIdentifier(source string) string {
	digest := sha256.Sum256([]byte(source))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func catalogSearchRequestSignature(
	secret string,
	clientIdentifier string,
	timestamp string,
	searchIdentifier string,
	requestIdentifier string,
) string {
	messageAuthenticationCode := hmac.New(sha256.New, []byte(secret))
	_, _ = messageAuthenticationCode.Write([]byte(catalogSearchRequestSignatureDomain))
	_, _ = messageAuthenticationCode.Write([]byte(clientIdentifier))
	_, _ = messageAuthenticationCode.Write([]byte{0})
	_, _ = messageAuthenticationCode.Write([]byte(timestamp))
	_, _ = messageAuthenticationCode.Write([]byte{0})
	_, _ = messageAuthenticationCode.Write([]byte(searchIdentifier))
	_, _ = messageAuthenticationCode.Write([]byte{0})
	_, _ = messageAuthenticationCode.Write([]byte(requestIdentifier))
	return base64.RawURLEncoding.EncodeToString(messageAuthenticationCode.Sum(nil))
}
