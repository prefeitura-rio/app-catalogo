package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
)

const (
	CatalogSearchClientIDHeader        = "X-Catalog-Search-Client-ID"
	CatalogSearchClientTimestampHeader = "X-Catalog-Search-Client-Timestamp"
	CatalogSearchClientSignatureHeader = "X-Catalog-Search-Client-Signature"
	SearchIDHeader                     = "X-Search-ID"

	catalogSearchRequestSignatureDomain = "catalog-search-request:v1\x00"
	minimumCatalogSearchSecretBytes     = 32
	maximumCatalogSearchSignatureSkew   = 10 * time.Minute
)

var protectedCatalogPublicPaths = map[string]struct{}{
	"/api/public/search":         {},
	"/api/public/search-summary": {},
	"/api/public/suggest":        {},
}

type CatalogSearchClientVerifier struct {
	secret      []byte
	maximumSkew time.Duration
	now         func() time.Time
}

func NewCatalogSearchClientVerifier(
	secret string,
	maximumSkew time.Duration,
	now func() time.Time,
) (*CatalogSearchClientVerifier, error) {
	if len(secret) < minimumCatalogSearchSecretBytes {
		return nil, errors.New("catalog search client verification secret is too short")
	}
	for byteIndex := range len(secret) {
		if secret[byteIndex] < 0x21 || secret[byteIndex] > 0x7e {
			return nil, errors.New("catalog search client verification secret must contain visible ASCII only")
		}
	}
	if maximumSkew < time.Second || maximumSkew > maximumCatalogSearchSignatureSkew {
		return nil, errors.New("catalog search client signature skew is outside the accepted bounds")
	}
	if now == nil {
		return nil, errors.New("catalog search client verifier clock is required")
	}
	return &CatalogSearchClientVerifier{
		secret:      append([]byte(nil), secret...),
		maximumSkew: maximumSkew,
		now:         now,
	}, nil
}

// VerifiedClientIdentifier returns a pseudonymous BFF client identifier only
// for a complete, fresh signature on a protected public catalog POST.
func (verifier *CatalogSearchClientVerifier) VerifiedClientIdentifier(request *http.Request) (string, bool) {
	if verifier == nil || request == nil || request.URL == nil || request.Method != http.MethodPost {
		return "", false
	}
	if _, protectedPath := protectedCatalogPublicPaths[request.URL.Path]; !protectedPath {
		return "", false
	}

	clientIdentifier, clientIdentifierPresent := singleRequestHeader(request, CatalogSearchClientIDHeader)
	timestamp, timestampPresent := singleRequestHeader(request, CatalogSearchClientTimestampHeader)
	providedSignature, signaturePresent := singleRequestHeader(request, CatalogSearchClientSignatureHeader)
	searchIdentifier, searchIdentifierPresent := singleRequestHeader(request, SearchIDHeader)
	requestIdentifier, requestIdentifierPresent := singleRequestHeader(request, RequestIDHeader)
	if !clientIdentifierPresent || !timestampPresent || !signaturePresent ||
		!searchIdentifierPresent || !requestIdentifierPresent {
		return "", false
	}

	decodedClientIdentifier, clientIdentifierError := base64.RawURLEncoding.DecodeString(clientIdentifier)
	if clientIdentifierError != nil || len(clientIdentifier) != 43 || len(decodedClientIdentifier) != sha256.Size {
		return "", false
	}
	decodedSignature, signatureError := base64.RawURLEncoding.DecodeString(providedSignature)
	if signatureError != nil || len(providedSignature) != 43 || len(decodedSignature) != sha256.Size {
		return "", false
	}

	timestampSeconds, timestampError := strconv.ParseInt(timestamp, 10, 64)
	if timestampError != nil || timestampSeconds <= 0 || strconv.FormatInt(timestampSeconds, 10) != timestamp {
		return "", false
	}
	signedAt := time.Unix(timestampSeconds, 0)
	currentTime := verifier.now()
	if signedAt.Before(currentTime.Add(-verifier.maximumSkew)) ||
		signedAt.After(currentTime.Add(verifier.maximumSkew)) {
		return "", false
	}

	parsedSearchIdentifier, searchIdentifierError := uuid.Parse(searchIdentifier)
	if searchIdentifierError != nil || parsedSearchIdentifier.String() != searchIdentifier {
		return "", false
	}
	if !canonicalDistributedLogID(requestIdentifier) {
		return "", false
	}

	expectedSignature := hmac.New(sha256.New, verifier.secret)
	_, _ = expectedSignature.Write([]byte(catalogSearchRequestSignatureDomain))
	_, _ = expectedSignature.Write([]byte(clientIdentifier))
	_, _ = expectedSignature.Write([]byte{0})
	_, _ = expectedSignature.Write([]byte(timestamp))
	_, _ = expectedSignature.Write([]byte{0})
	_, _ = expectedSignature.Write([]byte(searchIdentifier))
	_, _ = expectedSignature.Write([]byte{0})
	_, _ = expectedSignature.Write([]byte(requestIdentifier))
	if !hmac.Equal(expectedSignature.Sum(nil), decodedSignature) {
		return "", false
	}
	return clientIdentifier, true
}

func singleRequestHeader(request *http.Request, headerName string) (string, bool) {
	headerValues := request.Header.Values(headerName)
	if len(headerValues) != 1 || headerValues[0] == "" {
		return "", false
	}
	return headerValues[0], true
}
