package middleware

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testJWTIssuer          = "https://identity.example/realms/rio"
	testJWTAudience        = "superapp-api"
	testJWTAuthorizedParty = "superapp"
	testJWTKeyIDOne        = "key-one"
	testJWTKeyIDTwo        = "key-two"
)

var (
	testJWTKeysOnce  sync.Once
	testJWTKeyOne    *rsa.PrivateKey
	testJWTKeyTwo    *rsa.PrivateKey
	testJWTKeysError error
)

type lockedJWTClock struct {
	mutex       sync.Mutex
	currentTime time.Time
}

func (clock *lockedJWTClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.currentTime
}

func (clock *lockedJWTClock) Advance(elapsed time.Duration) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.currentTime = clock.currentTime.Add(elapsed)
}

func TestJWTVerifierVerifiesRS256BeforeReturningApplicationClaims(t *testing.T) {
	t.Parallel()
	privateKey, _ := jwtTestKeys(t)
	clock := &lockedJWTClock{currentTime: time.Date(2026, time.July, 11, 15, 0, 0, 0, time.UTC)}
	var jwksRequests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		jwksRequests.Add(1)
		if request.Method != http.MethodGet || request.Header.Get("Authorization") != "" {
			t.Errorf("JWKS request = %s authorization=%q", request.Method, request.Header.Get("Authorization"))
		}
		responseWriter.Header().Set("Content-Type", "application/jwk-set+json")
		fmt.Fprint(responseWriter, jwtTestJWKS(t, testJWK{keyID: testJWTKeyIDOne, publicKey: &privateKey.PublicKey}))
	}))
	defer server.Close()
	verifier := newTestJWTVerifier(t, server, clock, nil)
	token := jwtTestToken(t, privateKey, testJWTKeyIDOne, nil, validJWTTestClaims(clock.Now()))

	claims, verificationError := verifier.Verify(context.Background(), "Bearer "+token)
	if verificationError != nil {
		t.Fatalf("Verify() error = %v", verificationError)
	}
	if claims.PreferredUsername != "123.456.789-01" || claims.Sub != "citizen-1" ||
		claims.Name != "Citizen" || claims.Email != "citizen@example.test" {
		t.Fatalf("application claims = %+v", claims)
	}
	if superappAccess, exists := claims.ResourceAccess["superapp"]; !exists ||
		len(superappAccess.Roles) != 2 || superappAccess.Roles[1] != "go:admin" {
		t.Fatalf("resource access = %+v", claims.ResourceAccess)
	}
	if _, verificationError = verifier.Verify(context.Background(), token); verificationError != nil {
		t.Fatalf("cached Verify() error = %v", verificationError)
	}
	if requestCount := jwksRequests.Load(); requestCount != 1 {
		t.Fatalf("JWKS requests = %d, want one cached fetch", requestCount)
	}
}

func TestJWTVerifierRejectsInvalidRegisteredClaims(t *testing.T) {
	t.Parallel()
	privateKey, _ := jwtTestKeys(t)
	clock := &lockedJWTClock{currentTime: time.Date(2026, time.July, 11, 15, 0, 0, 0, time.UTC)}
	server := newStaticJWKSTestServer(t, jwtTestJWKS(t, testJWK{keyID: testJWTKeyIDOne, publicKey: &privateKey.PublicKey}))
	defer server.Close()
	verifier := newTestJWTVerifier(t, server, clock, func(config *JWTVerifierConfig) {
		config.ClockSkew = time.Second
	})

	testCases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "expired", mutate: func(claims map[string]any) { claims["exp"] = clock.Now().Add(-2 * time.Second).Unix() }},
		{name: "not before in future", mutate: func(claims map[string]any) { claims["nbf"] = clock.Now().Add(2 * time.Second).Unix() }},
		{name: "issued at in future", mutate: func(claims map[string]any) { claims["iat"] = clock.Now().Add(2 * time.Second).Unix() }},
		{name: "wrong issuer", mutate: func(claims map[string]any) { claims["iss"] = "https://attacker.example" }},
		{name: "wrong audience", mutate: func(claims map[string]any) { claims["aud"] = "other-api" }},
		{name: "wrong authorized party", mutate: func(claims map[string]any) { claims["azp"] = "other-client" }},
		{name: "missing expiration", mutate: func(claims map[string]any) { delete(claims, "exp") }},
		{name: "missing issued at", mutate: func(claims map[string]any) { delete(claims, "iat") }},
		{name: "issued after expiration", mutate: func(claims map[string]any) {
			claims["iat"] = clock.Now().Add(2 * time.Minute).Unix()
			claims["exp"] = clock.Now().Add(time.Minute).Unix()
		}},
		{name: "not before after expiration", mutate: func(claims map[string]any) {
			claims["nbf"] = clock.Now().Add(2 * time.Minute).Unix()
			claims["exp"] = clock.Now().Add(time.Minute).Unix()
		}},
		{name: "numeric date is fractional", mutate: func(claims map[string]any) { claims["exp"] = 123.5 }},
		{name: "audience has duplicates", mutate: func(claims map[string]any) { claims["aud"] = []string{testJWTAudience, testJWTAudience} }},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			claims := validJWTTestClaims(clock.Now())
			testCase.mutate(claims)
			token := jwtTestToken(t, privateKey, testJWTKeyIDOne, nil, claims)
			_, verificationError := verifier.Verify(context.Background(), token)
			assertJWTVerificationCode(t, verificationError, jwtErrorInvalidClaims)
		})
	}

	arrayAudienceClaims := validJWTTestClaims(clock.Now())
	arrayAudienceClaims["aud"] = []string{"another-api", testJWTAudience}
	if _, verificationError := verifier.Verify(
		context.Background(),
		jwtTestToken(t, privateKey, testJWTKeyIDOne, nil, arrayAudienceClaims),
	); verificationError != nil {
		t.Fatalf("Verify() rejected a valid audience array: %v", verificationError)
	}
}

func TestJWTVerifierAppliesClockSkewAtTemporalBoundaries(t *testing.T) {
	t.Parallel()
	privateKey, _ := jwtTestKeys(t)
	clock := &lockedJWTClock{currentTime: time.Date(2026, time.July, 11, 15, 0, 0, 0, time.UTC)}
	server := newStaticJWKSTestServer(t, jwtTestJWKS(t, testJWK{keyID: testJWTKeyIDOne, publicKey: &privateKey.PublicKey}))
	defer server.Close()
	verifier := newTestJWTVerifier(t, server, clock, func(config *JWTVerifierConfig) {
		config.ClockSkew = 5 * time.Second
	})
	expiredWithinSkewClaims := validJWTTestClaims(clock.Now())
	expiredWithinSkewClaims["exp"] = clock.Now().Add(-4 * time.Second).Unix()
	if _, verificationError := verifier.Verify(
		context.Background(),
		jwtTestToken(t, privateKey, testJWTKeyIDOne, nil, expiredWithinSkewClaims),
	); verificationError != nil {
		t.Fatalf("Verify() rejected expiration inside skew: %v", verificationError)
	}
	futureWithinSkewClaims := validJWTTestClaims(clock.Now())
	futureWithinSkewClaims["iat"] = clock.Now().Add(4 * time.Second).Unix()
	futureWithinSkewClaims["nbf"] = clock.Now().Add(4 * time.Second).Unix()
	if _, verificationError := verifier.Verify(
		context.Background(),
		jwtTestToken(t, privateKey, testJWTKeyIDOne, nil, futureWithinSkewClaims),
	); verificationError != nil {
		t.Fatalf("Verify() rejected iat/nbf inside skew: %v", verificationError)
	}
}

func TestJWTVerifierRejectsUnsupportedHeadersAndInvalidSignatures(t *testing.T) {
	t.Parallel()
	privateKey, otherPrivateKey := jwtTestKeys(t)
	clock := &lockedJWTClock{currentTime: time.Date(2026, time.July, 11, 15, 0, 0, 0, time.UTC)}
	server := newStaticJWKSTestServer(t, jwtTestJWKS(t, testJWK{keyID: testJWTKeyIDOne, publicKey: &privateKey.PublicKey}))
	defer server.Close()
	verifier := newTestJWTVerifier(t, server, clock, nil)

	headerCases := []struct {
		name     string
		override map[string]any
	}{
		{name: "algorithm none", override: map[string]any{"alg": "none"}},
		{name: "algorithm confusion", override: map[string]any{"alg": "HS256"}},
		{name: "missing kid", override: map[string]any{"kid": ""}},
		{name: "critical extension", override: map[string]any{"crit": []string{"custom"}}},
		{name: "unencoded payload", override: map[string]any{"b64": false}},
		{name: "embedded JWK URL", override: map[string]any{"jku": "https://attacker.example/jwks"}},
		{name: "embedded JWK", override: map[string]any{"jwk": map[string]any{"kty": "RSA"}}},
		{name: "unsupported type", override: map[string]any{"typ": "custom+jwt"}},
	}
	for _, testCase := range headerCases {
		t.Run(testCase.name, func(t *testing.T) {
			token := jwtTestToken(t, privateKey, testJWTKeyIDOne, testCase.override, validJWTTestClaims(clock.Now()))
			_, verificationError := verifier.Verify(context.Background(), token)
			assertJWTVerificationCode(t, verificationError, jwtErrorUnsupportedHeader)
		})
	}

	invalidSignatureToken := jwtTestToken(t, otherPrivateKey, testJWTKeyIDOne, nil, validJWTTestClaims(clock.Now()))
	_, verificationError := verifier.Verify(context.Background(), invalidSignatureToken)
	assertJWTVerificationCode(t, verificationError, jwtErrorInvalidSignature)
	if strings.Contains(verificationError.Error(), invalidSignatureToken) || strings.Contains(verificationError.Error(), "123.456.789-01") {
		t.Fatalf("verification error leaked token or claims: %v", verificationError)
	}

	duplicateHeader := `{"alg":"RS256","alg":"none","kid":"key-one","typ":"JWT"}`
	duplicateHeaderToken := jwtTestRawToken(t, privateKey, duplicateHeader, mustJSON(t, validJWTTestClaims(clock.Now())))
	_, verificationError = verifier.Verify(context.Background(), duplicateHeaderToken)
	assertJWTVerificationCode(t, verificationError, jwtErrorMalformedToken)
}

func TestJWTVerifierRefreshesJWKSOnceForConcurrentUnknownKey(t *testing.T) {
	privateKey, rotatedPrivateKey := jwtTestKeys(t)
	clock := &lockedJWTClock{currentTime: time.Date(2026, time.July, 11, 15, 0, 0, 0, time.UTC)}
	var jwksRequests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		requestNumber := jwksRequests.Add(1)
		if requestNumber == 1 {
			fmt.Fprint(responseWriter, jwtTestJWKS(t, testJWK{keyID: testJWTKeyIDOne, publicKey: &privateKey.PublicKey}))
			return
		}
		fmt.Fprint(responseWriter, jwtTestJWKS(t,
			testJWK{keyID: testJWTKeyIDOne, publicKey: &privateKey.PublicKey},
			testJWK{keyID: testJWTKeyIDTwo, publicKey: &rotatedPrivateKey.PublicKey},
		))
	}))
	defer server.Close()
	verifier := newTestJWTVerifier(t, server, clock, func(config *JWTVerifierConfig) {
		config.UnknownKeyRefreshInterval = time.Minute
	})
	if _, verificationError := verifier.Verify(
		context.Background(),
		jwtTestToken(t, privateKey, testJWTKeyIDOne, nil, validJWTTestClaims(clock.Now())),
	); verificationError != nil {
		t.Fatalf("initial Verify() error = %v", verificationError)
	}

	rotatedToken := jwtTestToken(t, rotatedPrivateKey, testJWTKeyIDTwo, nil, validJWTTestClaims(clock.Now()))
	const verifierCount = 32
	start := make(chan struct{})
	errorsChannel := make(chan error, verifierCount)
	var verifications sync.WaitGroup
	verifications.Add(verifierCount)
	for range verifierCount {
		go func() {
			defer verifications.Done()
			<-start
			_, verificationError := verifier.Verify(context.Background(), rotatedToken)
			errorsChannel <- verificationError
		}()
	}
	close(start)
	verifications.Wait()
	close(errorsChannel)
	for verificationError := range errorsChannel {
		if verificationError != nil {
			t.Errorf("concurrent Verify() error = %v", verificationError)
		}
	}
	if requestCount := jwksRequests.Load(); requestCount != 2 {
		t.Fatalf("JWKS requests = %d, want initial fetch plus one rotation refresh", requestCount)
	}

	unknownToken := jwtTestToken(t, rotatedPrivateKey, "unknown-key", nil, validJWTTestClaims(clock.Now()))
	_, verificationError := verifier.Verify(context.Background(), unknownToken)
	assertJWTVerificationCode(t, verificationError, jwtErrorUnknownKey)
	if requestCount := jwksRequests.Load(); requestCount != 2 {
		t.Fatalf("unknown-key cooldown made %d JWKS requests", requestCount)
	}
}

func TestJWTVerifierRefreshesExpiredCacheAndKeepsValidCache(t *testing.T) {
	t.Parallel()
	privateKey, _ := jwtTestKeys(t)
	clock := &lockedJWTClock{currentTime: time.Date(2026, time.July, 11, 15, 0, 0, 0, time.UTC)}
	var jwksRequests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		jwksRequests.Add(1)
		fmt.Fprint(responseWriter, jwtTestJWKS(t, testJWK{keyID: testJWTKeyIDOne, publicKey: &privateKey.PublicKey}))
	}))
	defer server.Close()
	verifier := newTestJWTVerifier(t, server, clock, func(config *JWTVerifierConfig) {
		config.JWKSCacheTTL = time.Minute
		config.UnknownKeyRefreshInterval = 10 * time.Second
	})
	token := jwtTestToken(t, privateKey, testJWTKeyIDOne, nil, validJWTTestClaims(clock.Now()))

	if _, verificationError := verifier.Verify(context.Background(), token); verificationError != nil {
		t.Fatalf("initial Verify() error = %v", verificationError)
	}
	clock.Advance(59 * time.Second)
	if _, verificationError := verifier.Verify(context.Background(), token); verificationError != nil {
		t.Fatalf("cached Verify() error = %v", verificationError)
	}
	if requestCount := jwksRequests.Load(); requestCount != 1 {
		t.Fatalf("valid cache made %d JWKS requests", requestCount)
	}
	clock.Advance(2 * time.Second)
	refreshedClaims := validJWTTestClaims(clock.Now())
	if _, verificationError := verifier.Verify(
		context.Background(),
		jwtTestToken(t, privateKey, testJWTKeyIDOne, nil, refreshedClaims),
	); verificationError != nil {
		t.Fatalf("expired-cache Verify() error = %v", verificationError)
	}
	if requestCount := jwksRequests.Load(); requestCount != 2 {
		t.Fatalf("expired cache made %d JWKS requests, want two", requestCount)
	}
}

func TestJWTVerifierRejectsInvalidJWKSMetadataAndBounds(t *testing.T) {
	t.Parallel()
	privateKey, _ := jwtTestKeys(t)
	validKey := testJWK{keyID: testJWTKeyIDOne, publicKey: &privateKey.PublicKey}
	validEncodedKey := jwtTestJWKMap(validKey)

	testCases := []struct {
		name        string
		encodedJWKS string
		maximumKeys int
	}{
		{name: "empty keys", encodedJWKS: `{"keys":[]}`, maximumKeys: 2},
		{name: "too many keys", encodedJWKS: jwtTestJWKS(t, validKey, testJWK{keyID: testJWTKeyIDTwo, publicKey: &privateKey.PublicKey}), maximumKeys: 1},
		{name: "duplicate kid", encodedJWKS: jwtTestJWKS(t, validKey, validKey), maximumKeys: 2},
		{name: "wrong key type", encodedJWKS: jwksWithMutatedKey(t, validEncodedKey, "kty", "EC"), maximumKeys: 2},
		{name: "wrong use", encodedJWKS: jwksWithMutatedKey(t, validEncodedKey, "use", "enc"), maximumKeys: 2},
		{name: "wrong algorithm", encodedJWKS: jwksWithMutatedKey(t, validEncodedKey, "alg", "RS512"), maximumKeys: 2},
		{name: "missing modulus", encodedJWKS: jwksWithMutatedKey(t, validEncodedKey, "n", ""), maximumKeys: 2},
		{name: "even exponent", encodedJWKS: jwksWithMutatedKey(t, validEncodedKey, "e", base64.RawURLEncoding.EncodeToString([]byte{2})), maximumKeys: 2},
		{name: "weak modulus", encodedJWKS: jwksWithMutatedKey(t, validEncodedKey, "n", base64.RawURLEncoding.EncodeToString(big.NewInt(3).Bytes())), maximumKeys: 2},
		{name: "duplicate JSON key", encodedJWKS: `{"keys":[{"kid":"one","kid":"two","kty":"RSA","use":"sig","alg":"RS256","n":"AQ","e":"Aw"}]}`, maximumKeys: 2},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, parseError := parseJWKS([]byte(testCase.encodedJWKS), testCase.maximumKeys); parseError == nil {
				t.Fatal("parseJWKS() accepted an invalid key set")
			}
		})
	}

	if parsedKeys, parseError := parseJWKS([]byte(jwtTestJWKS(t, validKey)), 1); parseError != nil || parsedKeys[testJWTKeyIDOne] == nil {
		t.Fatalf("parseJWKS() valid result = %v, error = %v", parsedKeys, parseError)
	}
	unrelatedKey := jwtTestJWKMap(testJWK{keyID: "unrelated-ec", publicKey: &privateKey.PublicKey})
	unrelatedKey["kty"] = "EC"
	unrelatedKey["alg"] = "ES256"
	mixedKeySet := mustJSON(t, map[string]any{"keys": []map[string]any{unrelatedKey, validEncodedKey}})
	if parsedKeys, parseError := parseJWKS([]byte(mixedKeySet), 2); parseError != nil || parsedKeys[testJWTKeyIDOne] == nil {
		t.Fatalf("parseJWKS() rejected a valid RS256 key beside an unrelated key: %v", parseError)
	}
}

func TestJWTVerifierEnforcesTransportAndPayloadBounds(t *testing.T) {
	t.Parallel()
	if _, constructorError := NewJWTVerifier(JWTVerifierConfig{
		JWKSURL:  "http://identity.example/certs",
		Issuer:   testJWTIssuer,
		Audience: testJWTAudience,
	}); constructorError == nil {
		t.Fatal("NewJWTVerifier() accepted a non-HTTPS JWKS URL")
	}
	if _, constructorError := NewJWTVerifier(JWTVerifierConfig{
		JWKSURL:  "https://identity.example/certs",
		Issuer:   "http://identity.example/realms/rio",
		Audience: testJWTAudience,
	}); constructorError == nil {
		t.Fatal("NewJWTVerifier() accepted a non-HTTPS issuer")
	}

	privateKey, _ := jwtTestKeys(t)
	clock := &lockedJWTClock{currentTime: time.Date(2026, time.July, 11, 15, 0, 0, 0, time.UTC)}
	var jwksRequests atomic.Int32
	oversizedServer := httptest.NewTLSServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		jwksRequests.Add(1)
		fmt.Fprint(responseWriter, strings.Repeat("x", 65))
	}))
	defer oversizedServer.Close()
	verifier := newTestJWTVerifier(t, oversizedServer, clock, func(config *JWTVerifierConfig) {
		config.MaximumJWKSBytes = 64
		config.MaximumTokenBytes = 256
	})
	oversizedToken := strings.Repeat("x", 257)
	_, verificationError := verifier.Verify(context.Background(), oversizedToken)
	assertJWTVerificationCode(t, verificationError, jwtErrorMalformedToken)
	if jwksRequests.Load() != 0 {
		t.Fatal("oversized token triggered a JWKS request")
	}

	validToken := jwtTestToken(t, privateKey, testJWTKeyIDOne, nil, validJWTTestClaims(clock.Now()))
	canceledContext, cancelVerification := context.WithCancel(context.Background())
	cancelVerification()
	_, verificationError = verifier.Verify(canceledContext, validToken)
	assertJWTVerificationCode(t, verificationError, jwtErrorVerificationCanceled)
	if jwksRequests.Load() != 0 {
		t.Fatal("canceled verification triggered a JWKS request")
	}
	verifier.settings.maximumTokenBytes = len(validToken)
	_, verificationError = verifier.Verify(context.Background(), validToken)
	assertJWTVerificationCode(t, verificationError, jwtErrorKeySetUnavailable)
}

func TestJWTVerifierBoundsJWKSRequestTimeoutAndRejectsRedirects(t *testing.T) {
	t.Parallel()
	privateKey, _ := jwtTestKeys(t)
	clock := &lockedJWTClock{currentTime: time.Date(2026, time.July, 11, 15, 0, 0, 0, time.UTC)}
	releaseHandler := make(chan struct{})
	timeoutServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-releaseHandler
	}))
	defer func() {
		close(releaseHandler)
		timeoutServer.Close()
	}()
	verifier := newTestJWTVerifier(t, timeoutServer, clock, func(config *JWTVerifierConfig) {
		config.HTTPTimeout = 10 * time.Millisecond
	})
	_, verificationError := verifier.Verify(
		context.Background(),
		jwtTestToken(t, privateKey, testJWTKeyIDOne, nil, validJWTTestClaims(clock.Now())),
	)
	assertJWTVerificationCode(t, verificationError, jwtErrorKeySetUnavailable)

	var redirectTargetRequests atomic.Int32
	redirectTarget := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectTargetRequests.Add(1)
	}))
	defer redirectTarget.Close()
	redirectSource := httptest.NewTLSServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		http.Redirect(responseWriter, request, redirectTarget.URL, http.StatusFound)
	}))
	defer redirectSource.Close()
	redirectVerifier := newTestJWTVerifier(t, redirectSource, clock, nil)
	_, verificationError = redirectVerifier.Verify(
		context.Background(),
		jwtTestToken(t, privateKey, testJWTKeyIDOne, nil, validJWTTestClaims(clock.Now())),
	)
	assertJWTVerificationCode(t, verificationError, jwtErrorKeySetUnavailable)
	if redirectTargetRequests.Load() != 0 {
		t.Fatal("JWKS client followed a redirect")
	}
}

type testJWK struct {
	keyID     string
	publicKey *rsa.PublicKey
}

func jwtTestKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	testJWTKeysOnce.Do(func() {
		testJWTKeyOne, testJWTKeysError = rsa.GenerateKey(rand.Reader, minimumJWTRSAModulusBits)
		if testJWTKeysError == nil {
			testJWTKeyTwo, testJWTKeysError = rsa.GenerateKey(rand.Reader, minimumJWTRSAModulusBits)
		}
	})
	if testJWTKeysError != nil {
		t.Fatalf("generate test RSA keys: %v", testJWTKeysError)
	}
	return testJWTKeyOne, testJWTKeyTwo
}

func newStaticJWKSTestServer(t *testing.T, encodedJWKS string) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/jwk-set+json")
		fmt.Fprint(responseWriter, encodedJWKS)
	}))
}

func newTestJWTVerifier(
	t *testing.T,
	server *httptest.Server,
	clock *lockedJWTClock,
	configure func(*JWTVerifierConfig),
) *JWTVerifier {
	t.Helper()
	config := JWTVerifierConfig{
		JWKSURL:         server.URL + "/realms/rio/protocol/openid-connect/certs",
		Issuer:          testJWTIssuer,
		Audience:        testJWTAudience,
		AuthorizedParty: testJWTAuthorizedParty,
		HTTPClient:      server.Client(),
		Now:             clock.Now,
	}
	if configure != nil {
		configure(&config)
	}
	verifier, constructorError := NewJWTVerifier(config)
	if constructorError != nil {
		t.Fatalf("NewJWTVerifier() error = %v", constructorError)
	}
	return verifier
}

func validJWTTestClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss":                testJWTIssuer,
		"aud":                testJWTAudience,
		"azp":                testJWTAuthorizedParty,
		"exp":                now.Add(5 * time.Minute).Unix(),
		"nbf":                now.Add(-time.Minute).Unix(),
		"iat":                now.Add(-time.Minute).Unix(),
		"preferred_username": "123.456.789-01",
		"sub":                "citizen-1",
		"name":               "Citizen",
		"email":              "citizen@example.test",
		"resource_access": map[string]any{
			"superapp": map[string]any{"roles": []string{"USER", "go:admin"}},
		},
	}
}

func jwtTestToken(
	t *testing.T,
	privateKey *rsa.PrivateKey,
	keyID string,
	headerOverrides map[string]any,
	claims map[string]any,
) string {
	t.Helper()
	header := map[string]any{"alg": "RS256", "kid": keyID, "typ": "JWT"}
	for headerName, headerValue := range headerOverrides {
		header[headerName] = headerValue
	}
	return jwtTestRawToken(t, privateKey, mustJSON(t, header), mustJSON(t, claims))
}

func jwtTestRawToken(t *testing.T, privateKey *rsa.PrivateKey, headerJSON string, claimsJSON string) string {
	t.Helper()
	encodedHeader := base64.RawURLEncoding.EncodeToString([]byte(headerJSON))
	encodedClaims := base64.RawURLEncoding.EncodeToString([]byte(claimsJSON))
	signedContent := encodedHeader + "." + encodedClaims
	signedDigest := sha256.Sum256([]byte(signedContent))
	signature, signatureError := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, signedDigest[:])
	if signatureError != nil {
		t.Fatalf("sign JWT: %v", signatureError)
	}
	return signedContent + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func jwtTestJWKS(t *testing.T, keys ...testJWK) string {
	t.Helper()
	encodedKeys := make([]map[string]any, len(keys))
	for keyIndex, key := range keys {
		encodedKeys[keyIndex] = jwtTestJWKMap(key)
	}
	return mustJSON(t, map[string]any{"keys": encodedKeys})
}

func jwtTestJWKMap(key testJWK) map[string]any {
	return map[string]any{
		"kid": key.keyID,
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"n":   base64.RawURLEncoding.EncodeToString(key.publicKey.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.publicKey.E)).Bytes()),
	}
}

func jwksWithMutatedKey(t *testing.T, sourceKey map[string]any, fieldName string, fieldValue any) string {
	t.Helper()
	mutatedKey := make(map[string]any, len(sourceKey))
	for key, value := range sourceKey {
		mutatedKey[key] = value
	}
	mutatedKey[fieldName] = fieldValue
	return mustJSON(t, map[string]any{"keys": []map[string]any{mutatedKey}})
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encodedValue, encodeError := json.Marshal(value)
	if encodeError != nil {
		t.Fatalf("encode JSON fixture: %v", encodeError)
	}
	return string(encodedValue)
}

func assertJWTVerificationCode(t *testing.T, verificationError error, expectedCode string) {
	t.Helper()
	var typedError *JWTVerificationError
	if !errors.As(verificationError, &typedError) || typedError.Code != expectedCode {
		t.Fatalf("verification error = %v, want code %q", verificationError, expectedCode)
	}
}
