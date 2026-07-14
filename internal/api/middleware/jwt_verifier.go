package middleware

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	defaultJWTMaximumTokenBytes               = 16 << 10
	defaultJWTMaximumJWKSBytes                = 256 << 10
	defaultJWTMaximumJWKCount                 = 32
	defaultJWTHTTPTimeout                     = 3 * time.Second
	defaultJWTJWKSCacheTTL                    = 15 * time.Minute
	defaultJWTClockSkew                       = 30 * time.Second
	defaultJWTUnknownKeyRefreshInterval       = 30 * time.Second
	hardJWTMaximumTokenBytes                  = 1 << 20
	hardJWTMaximumJWKSBytes                   = 4 << 20
	hardJWTMaximumJWKCount                    = 128
	hardJWTMaximumHTTPTimeout                 = time.Minute
	hardJWTMaximumJWKSCacheTTL                = 24 * time.Hour
	hardJWTMaximumClockSkew                   = 10 * time.Minute
	hardJWTMaximumKeyIDBytes                  = 256
	hardJWTMaximumJSONDepth                   = 32
	hardJWTMaximumAudienceCount               = 32
	minimumJWTRSAModulusBits                  = 2048
	maximumJWTRSAModulusBits                  = 8192
	maximumJWTNumericDateSeconds        int64 = 253402300799
)

const (
	jwtErrorMalformedToken       = "malformed_token"
	jwtErrorUnsupportedHeader    = "unsupported_header"
	jwtErrorInvalidSignature     = "invalid_signature"
	jwtErrorInvalidClaims        = "invalid_claims"
	jwtErrorUnknownKey           = "unknown_key"
	jwtErrorKeySetUnavailable    = "key_set_unavailable"
	jwtErrorVerificationCanceled = "verification_canceled"
)

// VerifiedJWTClaims is the existing application claim contract after
// cryptographic and registered-claim verification has succeeded.
type VerifiedJWTClaims = jwtClaims

// JWTVerificationError exposes only a bounded diagnostic code. Its message
// never contains token bytes, claim values, or a JWKS response body.
type JWTVerificationError struct {
	Code  string
	cause error
}

func (verificationError *JWTVerificationError) Error() string {
	return "jwt verification failed: " + verificationError.Code
}

func (verificationError *JWTVerificationError) Unwrap() error {
	return verificationError.cause
}

// JWTVerifierConfig defines the trusted issuer, recipient, JWKS transport,
// temporal leeway, and resource bounds. Issuer and audience are mandatory;
// AuthorizedParty is enforced only when configured.
type JWTVerifierConfig struct {
	JWKSURL                   string
	Issuer                    string
	Audience                  string
	AuthorizedParty           string
	ClockSkew                 time.Duration
	JWKSCacheTTL              time.Duration
	UnknownKeyRefreshInterval time.Duration
	HTTPTimeout               time.Duration
	MaximumTokenBytes         int
	MaximumJWKSBytes          int64
	MaximumJWKCount           int
	HTTPClient                *http.Client
	Now                       func() time.Time
}

type jwtVerifierSettings struct {
	jwksURL                   *url.URL
	issuer                    string
	audience                  string
	authorizedParty           string
	clockSkew                 time.Duration
	jwksCacheTTL              time.Duration
	unknownKeyRefreshInterval time.Duration
	httpTimeout               time.Duration
	maximumTokenBytes         int
	maximumJWKSBytes          int64
	maximumJWKCount           int
}

type jwksRefresh struct {
	done chan struct{}
	err  error
}

// JWTVerifier verifies compact RS256 JWTs against a bounded, concurrently
// cached JWKS. It is safe for concurrent use.
type JWTVerifier struct {
	settings   jwtVerifierSettings
	httpClient *http.Client
	now        func() time.Time

	cacheMutex         sync.Mutex
	keys               map[string]*rsa.PublicKey
	keysExpireAt       time.Time
	activeRefresh      *jwksRefresh
	lastUnknownRefresh time.Time
}

type jwtHeader struct {
	Algorithm string          `json:"alg"`
	KeyID     string          `json:"kid"`
	Type      string          `json:"typ"`
	Critical  []string        `json:"crit"`
	B64       *bool           `json:"b64"`
	JWKSetURL string          `json:"jku"`
	JWK       json.RawMessage `json:"jwk"`
	X509URL   string          `json:"x5u"`
	X509Chain json.RawMessage `json:"x5c"`
}

type jwtRegisteredClaims struct {
	Issuer          string         `json:"iss"`
	Audience        *audienceClaim `json:"aud"`
	AuthorizedParty string         `json:"azp"`
	Expiration      *numericDate   `json:"exp"`
	NotBefore       *numericDate   `json:"nbf"`
	IssuedAt        *numericDate   `json:"iat"`
}

type audienceClaim struct {
	values []string
}

type numericDate struct {
	seconds int64
}

type jwksDocument struct {
	Keys []json.RawMessage `json:"keys"`
}

type jwkDocument struct {
	KeyID     string `json:"kid"`
	KeyType   string `json:"kty"`
	PublicUse string `json:"use"`
	Algorithm string `json:"alg"`
	Modulus   string `json:"n"`
	Exponent  string `json:"e"`
}

// NewJWTVerifier validates configuration without performing network I/O.
func NewJWTVerifier(config JWTVerifierConfig) (*JWTVerifier, error) {
	settings, settingsError := normalizeJWTVerifierConfig(config)
	if settingsError != nil {
		return nil, settingsError
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	} else {
		clientCopy := *httpClient
		httpClient = &clientCopy
	}
	httpClient.CheckRedirect = rejectJWTJWKSRedirect

	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &JWTVerifier{
		settings:   settings,
		httpClient: httpClient,
		now:        now,
	}, nil
}

// Verify authenticates one raw or Bearer-prefixed compact JWT. The payload is
// decoded into application claims only after its RS256 signature is valid.
func (verifier *JWTVerifier) Verify(verificationContext context.Context, encodedToken string) (*VerifiedJWTClaims, error) {
	if verificationContext == nil {
		return nil, newJWTVerificationError(jwtErrorVerificationCanceled, errors.New("verification context is nil"))
	}
	if contextError := verificationContext.Err(); contextError != nil {
		return nil, newJWTVerificationError(jwtErrorVerificationCanceled, contextError)
	}
	compactToken, tokenError := normalizeEncodedJWT(encodedToken, verifier.settings.maximumTokenBytes)
	if tokenError != nil {
		return nil, tokenError
	}
	tokenParts := strings.Split(compactToken, ".")
	if len(tokenParts) != 3 || tokenParts[0] == "" || tokenParts[1] == "" || tokenParts[2] == "" {
		return nil, newJWTVerificationError(jwtErrorMalformedToken, errors.New("compact token must have three non-empty parts"))
	}

	headerBytes, headerDecodeError := decodeJWTPart(tokenParts[0])
	if headerDecodeError != nil {
		return nil, newJWTVerificationError(jwtErrorMalformedToken, headerDecodeError)
	}
	if structureError := validateJSONObject(headerBytes); structureError != nil {
		return nil, newJWTVerificationError(jwtErrorMalformedToken, structureError)
	}
	var header jwtHeader
	if unmarshalError := json.Unmarshal(headerBytes, &header); unmarshalError != nil {
		return nil, newJWTVerificationError(jwtErrorMalformedToken, unmarshalError)
	}
	if headerError := validateJWTHeader(header); headerError != nil {
		return nil, headerError
	}

	signature, signatureDecodeError := decodeJWTPart(tokenParts[2])
	minimumSignatureBytes := minimumJWTRSAModulusBits / 8
	maximumSignatureBytes := maximumJWTRSAModulusBits / 8
	if signatureDecodeError != nil || len(signature) < minimumSignatureBytes || len(signature) > maximumSignatureBytes {
		return nil, newJWTVerificationError(jwtErrorInvalidSignature, signatureDecodeError)
	}
	publicKey, keyError := verifier.resolveKey(verificationContext, header.KeyID)
	if keyError != nil {
		return nil, keyError
	}
	if len(signature) != publicKey.Size() {
		return nil, newJWTVerificationError(jwtErrorInvalidSignature, errors.New("signature length does not match the selected key"))
	}
	signedContent := tokenParts[0] + "." + tokenParts[1]
	signedDigest := sha256.Sum256([]byte(signedContent))
	if signatureError := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, signedDigest[:], signature); signatureError != nil {
		return nil, newJWTVerificationError(jwtErrorInvalidSignature, signatureError)
	}

	payloadBytes, payloadDecodeError := decodeJWTPart(tokenParts[1])
	if payloadDecodeError != nil {
		return nil, newJWTVerificationError(jwtErrorMalformedToken, payloadDecodeError)
	}
	if structureError := validateJSONObject(payloadBytes); structureError != nil {
		return nil, newJWTVerificationError(jwtErrorMalformedToken, structureError)
	}
	var registeredClaims jwtRegisteredClaims
	if unmarshalError := json.Unmarshal(payloadBytes, &registeredClaims); unmarshalError != nil {
		return nil, newJWTVerificationError(jwtErrorInvalidClaims, unmarshalError)
	}
	if claimsError := verifier.validateRegisteredClaims(registeredClaims); claimsError != nil {
		return nil, claimsError
	}

	var applicationClaims jwtClaims
	if unmarshalError := json.Unmarshal(payloadBytes, &applicationClaims); unmarshalError != nil {
		return nil, newJWTVerificationError(jwtErrorInvalidClaims, unmarshalError)
	}
	return &applicationClaims, nil
}

func normalizeJWTVerifierConfig(config JWTVerifierConfig) (jwtVerifierSettings, error) {
	jwksURL, jwksURLError := parseTrustedHTTPSURL(config.JWKSURL, "jwks URL")
	if jwksURLError != nil {
		return jwtVerifierSettings{}, jwksURLError
	}
	issuerURL, issuerError := parseTrustedHTTPSURL(config.Issuer, "issuer")
	if issuerError != nil {
		return jwtVerifierSettings{}, issuerError
	}
	issuer := strings.TrimSpace(config.Issuer)
	if issuerURL.String() != issuer {
		return jwtVerifierSettings{}, errors.New("issuer must use its canonical absolute HTTPS representation")
	}
	audience := strings.TrimSpace(config.Audience)
	authorizedParty := strings.TrimSpace(config.AuthorizedParty)
	if !validConfiguredClaim(audience) {
		return jwtVerifierSettings{}, errors.New("audience must be a bounded non-empty value")
	}
	if authorizedParty != "" && !validConfiguredClaim(authorizedParty) {
		return jwtVerifierSettings{}, errors.New("authorized party must be a bounded value")
	}

	clockSkew, durationError := defaultBoundedDuration(config.ClockSkew, defaultJWTClockSkew, hardJWTMaximumClockSkew, "clock skew")
	if durationError != nil {
		return jwtVerifierSettings{}, durationError
	}
	cacheTTL, durationError := defaultBoundedDuration(config.JWKSCacheTTL, defaultJWTJWKSCacheTTL, hardJWTMaximumJWKSCacheTTL, "JWKS cache TTL")
	if durationError != nil {
		return jwtVerifierSettings{}, durationError
	}
	defaultUnknownRefreshInterval := min(defaultJWTUnknownKeyRefreshInterval, cacheTTL)
	unknownRefreshInterval, durationError := defaultBoundedDuration(
		config.UnknownKeyRefreshInterval,
		defaultUnknownRefreshInterval,
		cacheTTL,
		"unknown key refresh interval",
	)
	if durationError != nil {
		return jwtVerifierSettings{}, durationError
	}
	httpTimeout, durationError := defaultBoundedDuration(config.HTTPTimeout, defaultJWTHTTPTimeout, hardJWTMaximumHTTPTimeout, "JWKS HTTP timeout")
	if durationError != nil {
		return jwtVerifierSettings{}, durationError
	}
	maximumTokenBytes, integerError := defaultBoundedInteger(config.MaximumTokenBytes, defaultJWTMaximumTokenBytes, hardJWTMaximumTokenBytes, "maximum token bytes")
	if integerError != nil {
		return jwtVerifierSettings{}, integerError
	}
	maximumJWKSBytes, integer64Error := defaultBoundedInteger64(config.MaximumJWKSBytes, defaultJWTMaximumJWKSBytes, hardJWTMaximumJWKSBytes, "maximum JWKS bytes")
	if integer64Error != nil {
		return jwtVerifierSettings{}, integer64Error
	}
	maximumJWKCount, integerError := defaultBoundedInteger(config.MaximumJWKCount, defaultJWTMaximumJWKCount, hardJWTMaximumJWKCount, "maximum JWK count")
	if integerError != nil {
		return jwtVerifierSettings{}, integerError
	}
	return jwtVerifierSettings{
		jwksURL:                   jwksURL,
		issuer:                    issuer,
		audience:                  audience,
		authorizedParty:           authorizedParty,
		clockSkew:                 clockSkew,
		jwksCacheTTL:              cacheTTL,
		unknownKeyRefreshInterval: unknownRefreshInterval,
		httpTimeout:               httpTimeout,
		maximumTokenBytes:         maximumTokenBytes,
		maximumJWKSBytes:          maximumJWKSBytes,
		maximumJWKCount:           maximumJWKCount,
	}, nil
}

func parseTrustedHTTPSURL(rawURL string, fieldName string) (*url.URL, error) {
	trimmedURL := strings.TrimSpace(rawURL)
	parsedURL, parseError := url.Parse(trimmedURL)
	if parseError != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" {
		return nil, fmt.Errorf("%s must be an absolute HTTPS URL", fieldName)
	}
	if parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return nil, fmt.Errorf("%s must not contain credentials, query parameters, or a fragment", fieldName)
	}
	return parsedURL, nil
}

func validConfiguredClaim(claimValue string) bool {
	return claimValue != "" && len(claimValue) <= hardJWTMaximumKeyIDBytes &&
		strings.IndexFunc(claimValue, unicode.IsControl) == -1
}

func defaultBoundedDuration(configuredDuration, defaultDuration, maximumDuration time.Duration, fieldName string) (time.Duration, error) {
	if configuredDuration == 0 {
		return defaultDuration, nil
	}
	if configuredDuration < 0 || configuredDuration > maximumDuration {
		return 0, fmt.Errorf("%s is outside the supported bounds", fieldName)
	}
	return configuredDuration, nil
}

func defaultBoundedInteger(configuredValue, defaultValue, maximumValue int, fieldName string) (int, error) {
	if configuredValue == 0 {
		return defaultValue, nil
	}
	if configuredValue < 0 || configuredValue > maximumValue {
		return 0, fmt.Errorf("%s is outside the supported bounds", fieldName)
	}
	return configuredValue, nil
}

func defaultBoundedInteger64(configuredValue, defaultValue, maximumValue int64, fieldName string) (int64, error) {
	if configuredValue == 0 {
		return defaultValue, nil
	}
	if configuredValue < 0 || configuredValue > maximumValue {
		return 0, fmt.Errorf("%s is outside the supported bounds", fieldName)
	}
	return configuredValue, nil
}

func normalizeEncodedJWT(encodedToken string, maximumTokenBytes int) (string, error) {
	if len(encodedToken) == 0 || len(encodedToken) > maximumTokenBytes {
		return "", newJWTVerificationError(jwtErrorMalformedToken, errors.New("token size is outside the configured bound"))
	}
	trimmedToken := strings.TrimSpace(encodedToken)
	if strings.ContainsAny(trimmedToken, "\r\n\t") {
		return "", newJWTVerificationError(jwtErrorMalformedToken, errors.New("token contains unsupported whitespace"))
	}
	tokenFields := strings.Fields(trimmedToken)
	switch len(tokenFields) {
	case 1:
		trimmedToken = tokenFields[0]
	case 2:
		if !strings.EqualFold(tokenFields[0], "Bearer") {
			return "", newJWTVerificationError(jwtErrorMalformedToken, errors.New("unsupported authorization scheme"))
		}
		trimmedToken = tokenFields[1]
	default:
		return "", newJWTVerificationError(jwtErrorMalformedToken, errors.New("token contains unsupported whitespace"))
	}
	if len(trimmedToken) == 0 || len(trimmedToken) > maximumTokenBytes {
		return "", newJWTVerificationError(jwtErrorMalformedToken, errors.New("token size is outside the configured bound"))
	}
	return trimmedToken, nil
}

func decodeJWTPart(encodedPart string) ([]byte, error) {
	if encodedPart == "" || strings.Contains(encodedPart, "=") {
		return nil, errors.New("jwt part is not canonical base64url")
	}
	decodedPart, decodeError := base64.RawURLEncoding.DecodeString(encodedPart)
	if decodeError != nil {
		return nil, errors.New("jwt part is not valid base64url")
	}
	return decodedPart, nil
}

func validateJWTHeader(header jwtHeader) error {
	if header.Algorithm != "RS256" {
		return newJWTVerificationError(jwtErrorUnsupportedHeader, errors.New("only RS256 is accepted"))
	}
	if !validKeyID(header.KeyID) {
		return newJWTVerificationError(jwtErrorUnsupportedHeader, errors.New("kid is missing or invalid"))
	}
	if header.Type != "" && header.Type != "JWT" && header.Type != "at+jwt" {
		return newJWTVerificationError(jwtErrorUnsupportedHeader, errors.New("typ is unsupported"))
	}
	if len(header.Critical) != 0 || (header.B64 != nil && !*header.B64) ||
		header.JWKSetURL != "" || len(header.JWK) != 0 || header.X509URL != "" || len(header.X509Chain) != 0 {
		return newJWTVerificationError(jwtErrorUnsupportedHeader, errors.New("header requests unsupported key or critical processing"))
	}
	return nil
}

func validKeyID(keyID string) bool {
	return keyID != "" && len(keyID) <= hardJWTMaximumKeyIDBytes &&
		strings.IndexFunc(keyID, unicode.IsControl) == -1
}

func (verifier *JWTVerifier) resolveKey(verificationContext context.Context, keyID string) (*rsa.PublicKey, error) {
	now := verifier.now()
	verifier.cacheMutex.Lock()
	cacheValid := len(verifier.keys) != 0 && now.Before(verifier.keysExpireAt)
	if cacheValid {
		if publicKey := verifier.keys[keyID]; publicKey != nil {
			verifier.cacheMutex.Unlock()
			return publicKey, nil
		}
	}

	refresh := verifier.activeRefresh
	if refresh == nil {
		if cacheValid && !verifier.lastUnknownRefresh.IsZero() &&
			now.Sub(verifier.lastUnknownRefresh) < verifier.settings.unknownKeyRefreshInterval {
			verifier.cacheMutex.Unlock()
			return nil, newJWTVerificationError(jwtErrorUnknownKey, errors.New("key is not present in the current JWKS"))
		}
		forcedRefresh := cacheValid
		if forcedRefresh {
			verifier.lastUnknownRefresh = now
		}
		refresh = &jwksRefresh{done: make(chan struct{})}
		verifier.activeRefresh = refresh
		go verifier.runJWKSRefresh(refresh, forcedRefresh)
	}
	verifier.cacheMutex.Unlock()

	select {
	case <-refresh.done:
	case <-verificationContext.Done():
		return nil, newJWTVerificationError(jwtErrorVerificationCanceled, verificationContext.Err())
	}
	if refresh.err != nil {
		return nil, newJWTVerificationError(jwtErrorKeySetUnavailable, refresh.err)
	}

	verifier.cacheMutex.Lock()
	publicKey := verifier.keys[keyID]
	cacheStillValid := len(verifier.keys) != 0 && verifier.now().Before(verifier.keysExpireAt)
	if cacheStillValid && publicKey == nil && verifier.lastUnknownRefresh.IsZero() {
		verifier.lastUnknownRefresh = verifier.now()
	}
	verifier.cacheMutex.Unlock()
	if !cacheStillValid || publicKey == nil {
		return nil, newJWTVerificationError(jwtErrorUnknownKey, errors.New("key is not present after JWKS refresh"))
	}
	return publicKey, nil
}

func (verifier *JWTVerifier) runJWKSRefresh(refresh *jwksRefresh, forcedRefresh bool) {
	refreshContext, cancelRefresh := context.WithTimeout(context.Background(), verifier.settings.httpTimeout)
	defer cancelRefresh()
	refreshedKeys, refreshError := verifier.fetchJWKS(refreshContext)

	verifier.cacheMutex.Lock()
	if refreshError == nil {
		verifier.keys = refreshedKeys
		verifier.keysExpireAt = verifier.now().Add(verifier.settings.jwksCacheTTL)
		if !forcedRefresh {
			verifier.lastUnknownRefresh = time.Time{}
		}
	}
	refresh.err = refreshError
	verifier.activeRefresh = nil
	close(refresh.done)
	verifier.cacheMutex.Unlock()
}

func (verifier *JWTVerifier) fetchJWKS(fetchContext context.Context) (map[string]*rsa.PublicKey, error) {
	request, requestError := http.NewRequestWithContext(fetchContext, http.MethodGet, verifier.settings.jwksURL.String(), nil)
	if requestError != nil {
		return nil, errors.New("jwks request could not be created")
	}
	request.Header.Set("Accept", "application/json, application/jwk-set+json")
	request.Header.Set("User-Agent", "app-catalogo-jwt-verifier/1")

	response, responseError := verifier.httpClient.Do(request)
	if responseError != nil {
		return nil, fmt.Errorf("jwks request failed: %w", responseError)
	}
	responseBody, readError := io.ReadAll(io.LimitReader(response.Body, verifier.settings.maximumJWKSBytes+1))
	closeError := response.Body.Close()
	if readError != nil || closeError != nil {
		return nil, fmt.Errorf("jwks response could not be read: %w", errors.Join(readError, closeError))
	}
	if int64(len(responseBody)) > verifier.settings.maximumJWKSBytes {
		return nil, errors.New("jwks response exceeds the configured size bound")
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("jwks endpoint returned a non-success status")
	}
	return parseJWKS(responseBody, verifier.settings.maximumJWKCount)
}

func parseJWKS(encodedJWKS []byte, maximumJWKCount int) (map[string]*rsa.PublicKey, error) {
	if structureError := validateJSONObject(encodedJWKS); structureError != nil {
		return nil, fmt.Errorf("jwks JSON is invalid: %w", structureError)
	}
	var keySet jwksDocument
	if unmarshalError := json.Unmarshal(encodedJWKS, &keySet); unmarshalError != nil {
		return nil, errors.New("jwks document could not be decoded")
	}
	if len(keySet.Keys) == 0 || len(keySet.Keys) > maximumJWKCount {
		return nil, errors.New("jwks key count is outside the configured bound")
	}

	publicKeys := make(map[string]*rsa.PublicKey, len(keySet.Keys))
	seenKeyIDs := make(map[string]struct{}, len(keySet.Keys))
	for _, encodedKey := range keySet.Keys {
		if structureError := validateJSONObject(encodedKey); structureError != nil {
			return nil, errors.New("jwk JSON is invalid")
		}
		var encodedJWK jwkDocument
		if unmarshalError := json.Unmarshal(encodedKey, &encodedJWK); unmarshalError != nil {
			return nil, errors.New("jwk could not be decoded")
		}
		if !validKeyID(encodedJWK.KeyID) {
			return nil, errors.New("jwk kid is missing or invalid")
		}
		if _, duplicateKeyID := seenKeyIDs[encodedJWK.KeyID]; duplicateKeyID {
			return nil, errors.New("jwks contains a duplicate kid")
		}
		seenKeyIDs[encodedJWK.KeyID] = struct{}{}
		if encodedJWK.KeyType != "RSA" || encodedJWK.PublicUse != "sig" || encodedJWK.Algorithm != "RS256" {
			continue
		}
		publicKey, publicKeyError := decodeRSAPublicKey(encodedJWK.Modulus, encodedJWK.Exponent)
		if publicKeyError != nil {
			return nil, publicKeyError
		}
		publicKeys[encodedJWK.KeyID] = publicKey
	}
	if len(publicKeys) == 0 {
		return nil, errors.New("jwks contains no compatible RS256 signing keys")
	}
	return publicKeys, nil
}

func decodeRSAPublicKey(encodedModulus string, encodedExponent string) (*rsa.PublicKey, error) {
	modulusBytes, modulusError := decodeBase64URLUInt(encodedModulus)
	if modulusError != nil {
		return nil, errors.New("jwk modulus is invalid")
	}
	exponentBytes, exponentError := decodeBase64URLUInt(encodedExponent)
	if exponentError != nil || len(exponentBytes) > 4 {
		return nil, errors.New("jwk exponent is invalid")
	}
	modulus := new(big.Int).SetBytes(modulusBytes)
	if modulus.BitLen() < minimumJWTRSAModulusBits || modulus.BitLen() > maximumJWTRSAModulusBits || modulus.Bit(0) == 0 {
		return nil, errors.New("jwk RSA modulus is outside the supported bounds")
	}
	var exponent uint64
	for _, exponentByte := range exponentBytes {
		exponent = exponent<<8 | uint64(exponentByte)
	}
	if exponent < 3 || exponent > math.MaxInt32 || exponent%2 == 0 {
		return nil, errors.New("jwk RSA exponent is outside the supported bounds")
	}
	return &rsa.PublicKey{N: modulus, E: int(exponent)}, nil
}

func decodeBase64URLUInt(encodedInteger string) ([]byte, error) {
	if encodedInteger == "" || strings.Contains(encodedInteger, "=") {
		return nil, errors.New("integer is not canonical base64url")
	}
	decodedInteger, decodeError := base64.RawURLEncoding.DecodeString(encodedInteger)
	if decodeError != nil || len(decodedInteger) == 0 || decodedInteger[0] == 0 {
		return nil, errors.New("integer is not a canonical base64urlUInt")
	}
	return decodedInteger, nil
}

func (verifier *JWTVerifier) validateRegisteredClaims(claims jwtRegisteredClaims) error {
	if claims.Issuer != verifier.settings.issuer || claims.Audience == nil ||
		!claims.Audience.contains(verifier.settings.audience) {
		return newJWTVerificationError(jwtErrorInvalidClaims, errors.New("issuer or audience validation failed"))
	}
	if verifier.settings.authorizedParty != "" && claims.AuthorizedParty != verifier.settings.authorizedParty {
		return newJWTVerificationError(jwtErrorInvalidClaims, errors.New("authorized party validation failed"))
	}
	if claims.Expiration == nil || claims.IssuedAt == nil {
		return newJWTVerificationError(jwtErrorInvalidClaims, errors.New("exp and iat are required"))
	}

	now := verifier.now()
	expiration := time.Unix(claims.Expiration.seconds, 0)
	issuedAt := time.Unix(claims.IssuedAt.seconds, 0)
	if !now.Before(expiration.Add(verifier.settings.clockSkew)) {
		return newJWTVerificationError(jwtErrorInvalidClaims, errors.New("token is expired"))
	}
	if issuedAt.After(now.Add(verifier.settings.clockSkew)) || issuedAt.After(expiration) {
		return newJWTVerificationError(jwtErrorInvalidClaims, errors.New("iat is invalid"))
	}
	if claims.NotBefore != nil {
		notBefore := time.Unix(claims.NotBefore.seconds, 0)
		if now.Add(verifier.settings.clockSkew).Before(notBefore) || notBefore.After(expiration) {
			return newJWTVerificationError(jwtErrorInvalidClaims, errors.New("nbf is invalid"))
		}
	}
	return nil
}

func (audience *audienceClaim) UnmarshalJSON(encodedAudience []byte) error {
	if len(encodedAudience) == 0 {
		return errors.New("aud is empty")
	}
	var audiences []string
	if encodedAudience[0] == '"' {
		var singularAudience string
		if unmarshalError := json.Unmarshal(encodedAudience, &singularAudience); unmarshalError != nil {
			return errors.New("aud is not a string")
		}
		audiences = []string{singularAudience}
	} else if encodedAudience[0] == '[' {
		if unmarshalError := json.Unmarshal(encodedAudience, &audiences); unmarshalError != nil {
			return errors.New("aud is not a string array")
		}
	} else {
		return errors.New("aud must be a string or string array")
	}
	if len(audiences) == 0 || len(audiences) > hardJWTMaximumAudienceCount {
		return errors.New("aud count is outside the supported bound")
	}
	seenAudiences := make(map[string]struct{}, len(audiences))
	for _, audienceValue := range audiences {
		if !validConfiguredClaim(audienceValue) {
			return errors.New("aud contains an invalid value")
		}
		if _, duplicateAudience := seenAudiences[audienceValue]; duplicateAudience {
			return errors.New("aud contains a duplicate value")
		}
		seenAudiences[audienceValue] = struct{}{}
	}
	audience.values = audiences
	return nil
}

func (audience audienceClaim) contains(expectedAudience string) bool {
	for _, audienceValue := range audience.values {
		if audienceValue == expectedAudience {
			return true
		}
	}
	return false
}

func (date *numericDate) UnmarshalJSON(encodedDate []byte) error {
	if len(encodedDate) == 0 || bytes.ContainsAny(encodedDate, ".eE+\"") || bytes.Equal(encodedDate, []byte("null")) {
		return errors.New("numeric date must be an integer")
	}
	parsedSeconds, parseError := strconv.ParseInt(string(encodedDate), 10, 64)
	if parseError != nil || parsedSeconds < 0 || parsedSeconds > maximumJWTNumericDateSeconds {
		return errors.New("numeric date is outside the supported range")
	}
	date.seconds = parsedSeconds
	return nil
}

func validateJSONObject(encodedJSON []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encodedJSON))
	decoder.UseNumber()
	firstToken, tokenError := decoder.Token()
	if tokenError != nil || firstToken != json.Delim('{') {
		return errors.New("json value must be an object")
	}
	if objectError := consumeJSONObject(decoder, 1); objectError != nil {
		return objectError
	}
	if _, trailingError := decoder.Token(); !errors.Is(trailingError, io.EOF) {
		return errors.New("json contains trailing content")
	}
	return nil
}

func consumeJSONObject(decoder *json.Decoder, depth int) error {
	if depth > hardJWTMaximumJSONDepth {
		return errors.New("json nesting exceeds the supported bound")
	}
	seenKeys := make(map[string]struct{})
	for decoder.More() {
		keyToken, keyError := decoder.Token()
		if keyError != nil {
			return errors.New("json object key is invalid")
		}
		key, validKey := keyToken.(string)
		if !validKey {
			return errors.New("json object key is not a string")
		}
		if _, duplicateKey := seenKeys[key]; duplicateKey {
			return errors.New("json object contains a duplicate key")
		}
		seenKeys[key] = struct{}{}
		if valueError := consumeJSONValue(decoder, depth+1); valueError != nil {
			return valueError
		}
	}
	closingToken, closingError := decoder.Token()
	if closingError != nil || closingToken != json.Delim('}') {
		return errors.New("json object is not closed")
	}
	return nil
}

func consumeJSONArray(decoder *json.Decoder, depth int) error {
	if depth > hardJWTMaximumJSONDepth {
		return errors.New("json nesting exceeds the supported bound")
	}
	for decoder.More() {
		if valueError := consumeJSONValue(decoder, depth+1); valueError != nil {
			return valueError
		}
	}
	closingToken, closingError := decoder.Token()
	if closingError != nil || closingToken != json.Delim(']') {
		return errors.New("json array is not closed")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder, depth int) error {
	valueToken, valueError := decoder.Token()
	if valueError != nil {
		return errors.New("json value is invalid")
	}
	valueDelimiter, isDelimiter := valueToken.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch valueDelimiter {
	case '{':
		return consumeJSONObject(decoder, depth)
	case '[':
		return consumeJSONArray(decoder, depth)
	default:
		return errors.New("json value has an unexpected delimiter")
	}
}

func rejectJWTJWKSRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func newJWTVerificationError(code string, cause error) *JWTVerificationError {
	return &JWTVerificationError{Code: code, cause: cause}
}
