package clients

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	FacilitaServiceCandidateSchemaVersion = "facilita-service-candidates/v2"
	maximumFacilitaCandidateRequestBytes  = 4 << 10
	maximumFacilitaCandidateResponseBytes = 64 << 10
	maximumFacilitaCandidateLimit         = 50
	maximumFacilitaCredentialBytes        = 512
	minimumFacilitaCredentialBytes        = 32
	facilitaClusterHTTPOrigin             = "http://app-busca-search.busca.svc.cluster.local:8080"
)

var (
	facilitaSlugPattern            = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	facilitaRankerVersionPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
	facilitaCatalogRevisionPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

type FacilitaSearchFailure string

const (
	FacilitaSearchFailureTimeout         FacilitaSearchFailure = "timeout"
	FacilitaSearchFailureTransport       FacilitaSearchFailure = "transport"
	FacilitaSearchFailureRejected        FacilitaSearchFailure = "rejected"
	FacilitaSearchFailureInvalidContract FacilitaSearchFailure = "invalid_contract"
)

type FacilitaSearchError struct {
	Failure FacilitaSearchFailure
	Cause   error
}

func (candidateError *FacilitaSearchError) Error() string {
	return fmt.Sprintf("facilita search: %s: %v", candidateError.Failure, candidateError.Cause)
}

func (candidateError *FacilitaSearchError) Unwrap() error {
	return candidateError.Cause
}

func FacilitaSearchFailureFromError(candidateError error) FacilitaSearchFailure {
	var typedError *FacilitaSearchError
	if errors.As(candidateError, &typedError) {
		return typedError.Failure
	}
	return FacilitaSearchFailureTransport
}

type FacilitaServiceCandidate struct {
	Slug string
	Rank int
}

type FacilitaCandidateProvenance struct {
	CatalogRevision       string
	RetrievalVersion      string
	QueryExpansionVersion string
	RankerVersion         string
}

type FacilitaServiceCandidateBatch struct {
	SchemaVersion string
	Provenance    FacilitaCandidateProvenance
	Candidates    []FacilitaServiceCandidate
}

type FacilitaSearchClient struct {
	candidateURL     string
	internalAPIKey   string
	clientIdentifier string
	httpClient       *http.Client
}

type facilitaCandidateRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type facilitaCandidateResponse struct {
	SchemaVersion string `json:"schema_version"`
	Provenance    struct {
		CatalogRevision       string `json:"catalog_revision"`
		RetrievalVersion      string `json:"retrieval_version"`
		QueryExpansionVersion string `json:"query_expansion_version"`
		RankerVersion         string `json:"ranker_version"`
	} `json:"provenance"`
	Candidates []struct {
		Slug string `json:"slug"`
		Rank int    `json:"rank"`
	} `json:"candidates"`
}

func NewFacilitaSearchClient(baseURL string, internalAPIKey string, timeout time.Duration) (*FacilitaSearchClient, error) {
	parsedBaseURL, baseURLError := validateServiceBaseURL(baseURL, false, false)
	if baseURLError != nil {
		return nil, fmt.Errorf("facilita search: invalid base URL: %w", baseURLError)
	}
	if parsedBaseURL.Scheme != "https" && !isLoopbackServiceHost(parsedBaseURL.Hostname()) && parsedBaseURL.String() != facilitaClusterHTTPOrigin {
		return nil, errors.New("facilita search: base URL must use HTTPS outside loopback or the approved cluster origin")
	}
	if timeout <= 0 {
		return nil, errors.New("facilita search: timeout must be positive")
	}
	if credentialError := validateFacilitaCredential(internalAPIKey); credentialError != nil {
		return nil, credentialError
	}
	candidateURL, joinError := url.JoinPath(parsedBaseURL.String(), "api/v1/search/candidates")
	if joinError != nil {
		return nil, errors.New("facilita search: build candidate endpoint URL")
	}
	return &FacilitaSearchClient{
		candidateURL:     candidateURL,
		internalAPIKey:   internalAPIKey,
		clientIdentifier: facilitaClientIdentifier(),
		httpClient:       noRedirectHTTPClient(timeout),
	}, nil
}

func (client *FacilitaSearchClient) SearchCandidates(
	searchContext context.Context,
	query string,
	limit int,
) (FacilitaServiceCandidateBatch, error) {
	canonicalQuery := strings.Join(strings.Fields(query), " ")
	if canonicalQuery == "" {
		return FacilitaServiceCandidateBatch{}, errors.New("facilita search: query must not be empty")
	}
	if limit < 1 || limit > maximumFacilitaCandidateLimit {
		return FacilitaServiceCandidateBatch{}, fmt.Errorf("facilita search: candidate limit must be between 1 and %d", maximumFacilitaCandidateLimit)
	}
	requestBody, marshalError := json.Marshal(facilitaCandidateRequest{Query: canonicalQuery, Limit: limit})
	if marshalError != nil {
		return FacilitaServiceCandidateBatch{}, errors.New("facilita search: encode request")
	}
	if len(requestBody) > maximumFacilitaCandidateRequestBytes {
		return FacilitaServiceCandidateBatch{}, errors.New("facilita search: request exceeds size limit")
	}
	request, requestError := http.NewRequestWithContext(
		searchContext,
		http.MethodPost,
		client.candidateURL,
		bytes.NewReader(requestBody),
	)
	if requestError != nil {
		return FacilitaServiceCandidateBatch{}, errors.New("facilita search: create request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-facilita-internal-key", client.internalAPIKey)
	request.Header.Set("x-facilita-client-id", client.clientIdentifier)

	response, responseError := client.httpClient.Do(request)
	if responseError != nil {
		failure := FacilitaSearchFailureTransport
		if errors.Is(responseError, context.DeadlineExceeded) {
			failure = FacilitaSearchFailureTimeout
		}
		return FacilitaServiceCandidateBatch{}, &FacilitaSearchError{Failure: failure, Cause: responseError}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return FacilitaServiceCandidateBatch{}, &FacilitaSearchError{
			Failure: FacilitaSearchFailureRejected,
			Cause:   fmt.Errorf("unexpected status %d", response.StatusCode),
		}
	}
	if contentType := strings.ToLower(response.Header.Get("Content-Type")); !strings.HasPrefix(contentType, "application/json") {
		return FacilitaServiceCandidateBatch{}, &FacilitaSearchError{Failure: FacilitaSearchFailureInvalidContract, Cause: errors.New("response content type is not JSON")}
	}
	responseBody, readError := readBoundedHTTPBody(response.Body, maximumFacilitaCandidateResponseBytes)
	if readError != nil {
		return FacilitaServiceCandidateBatch{}, &FacilitaSearchError{Failure: FacilitaSearchFailureInvalidContract, Cause: readError}
	}
	responseDecoder := json.NewDecoder(bytes.NewReader(responseBody))
	responseDecoder.DisallowUnknownFields()
	var candidateResponse facilitaCandidateResponse
	if decodeError := responseDecoder.Decode(&candidateResponse); decodeError != nil {
		return FacilitaServiceCandidateBatch{}, &FacilitaSearchError{Failure: FacilitaSearchFailureInvalidContract, Cause: fmt.Errorf("decode response: %w", decodeError)}
	}
	var trailingJSON any
	if trailingError := responseDecoder.Decode(&trailingJSON); !errors.Is(trailingError, io.EOF) {
		return FacilitaServiceCandidateBatch{}, &FacilitaSearchError{Failure: FacilitaSearchFailureInvalidContract, Cause: errors.New("trailing JSON")}
	}
	validatedResponse, validationError := validateFacilitaCandidateResponse(candidateResponse, limit)
	if validationError != nil {
		return FacilitaServiceCandidateBatch{}, &FacilitaSearchError{Failure: FacilitaSearchFailureInvalidContract, Cause: validationError}
	}
	return validatedResponse, nil
}

func validateFacilitaCandidateResponse(
	candidateResponse facilitaCandidateResponse,
	requestedLimit int,
) (FacilitaServiceCandidateBatch, error) {
	if candidateResponse.SchemaVersion != FacilitaServiceCandidateSchemaVersion {
		return FacilitaServiceCandidateBatch{}, errors.New("unsupported response schema")
	}
	if !facilitaCatalogRevisionPattern.MatchString(candidateResponse.Provenance.CatalogRevision) {
		return FacilitaServiceCandidateBatch{}, errors.New("invalid catalog revision")
	}
	for componentName, componentVersion := range map[string]string{
		"retrieval":       candidateResponse.Provenance.RetrievalVersion,
		"query expansion": candidateResponse.Provenance.QueryExpansionVersion,
		"ranker":          candidateResponse.Provenance.RankerVersion,
	} {
		if len(componentVersion) > 128 || !facilitaRankerVersionPattern.MatchString(componentVersion) {
			return FacilitaServiceCandidateBatch{}, fmt.Errorf("invalid %s version", componentName)
		}
	}
	if len(candidateResponse.Candidates) > requestedLimit {
		return FacilitaServiceCandidateBatch{}, errors.New("response exceeds requested candidate limit")
	}
	validatedCandidates := make([]FacilitaServiceCandidate, 0, len(candidateResponse.Candidates))
	seenSlugs := make(map[string]struct{}, len(candidateResponse.Candidates))
	for candidateIndex, candidate := range candidateResponse.Candidates {
		if len(candidate.Slug) > 200 || !facilitaSlugPattern.MatchString(candidate.Slug) {
			return FacilitaServiceCandidateBatch{}, errors.New("invalid candidate slug")
		}
		if candidate.Rank != candidateIndex+1 {
			return FacilitaServiceCandidateBatch{}, errors.New("candidate ranks must be contiguous and ordered")
		}
		if _, duplicated := seenSlugs[candidate.Slug]; duplicated {
			return FacilitaServiceCandidateBatch{}, errors.New("duplicate candidate slug")
		}
		seenSlugs[candidate.Slug] = struct{}{}
		validatedCandidates = append(validatedCandidates, FacilitaServiceCandidate{Slug: candidate.Slug, Rank: candidate.Rank})
	}
	return FacilitaServiceCandidateBatch{
		SchemaVersion: candidateResponse.SchemaVersion,
		Provenance: FacilitaCandidateProvenance{
			CatalogRevision:       candidateResponse.Provenance.CatalogRevision,
			RetrievalVersion:      candidateResponse.Provenance.RetrievalVersion,
			QueryExpansionVersion: candidateResponse.Provenance.QueryExpansionVersion,
			RankerVersion:         candidateResponse.Provenance.RankerVersion,
		},
		Candidates: validatedCandidates,
	}, nil
}

func validateFacilitaCredential(internalAPIKey string) error {
	if credentialError := validateServiceCredential(internalAPIKey, maximumFacilitaCredentialBytes); credentialError != nil {
		return fmt.Errorf("facilita search: invalid internal API key: %w", credentialError)
	}
	if len(internalAPIKey) < minimumFacilitaCredentialBytes || strings.IndexFunc(internalAPIKey, func(character rune) bool {
		return character > unicode.MaxASCII || unicode.IsSpace(character)
	}) >= 0 {
		return errors.New("facilita search: internal API key must contain at least 32 visible ASCII bytes")
	}
	return nil
}

func facilitaClientIdentifier() string {
	identifierDigest := sha256.Sum256([]byte("app-catalogo:facilita-search-candidates:v1"))
	return base64.RawURLEncoding.EncodeToString(identifierDigest[:])
}
