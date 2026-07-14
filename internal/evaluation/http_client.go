package evaluation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

const (
	defaultRequestTimeout       = 5 * time.Second
	defaultMaximumResponseBytes = 4 << 20
)

var canonicalEntityIDPattern = regexp.MustCompile(`^entity-v1:[0-9a-f]{64}$`)

// HTTPClientConfig defines request and response bounds for public search calls.
type HTTPClientConfig struct {
	Endpoint             string
	RequestTimeout       time.Duration
	CandidateLimit       int
	MaximumResponseBytes int64
	Client               *http.Client
	Now                  func() time.Time
}

// HTTPClient calls the unauthenticated public search endpoint.
type HTTPClient struct {
	endpoint             *url.URL
	requestTimeout       time.Duration
	candidateLimit       int
	maximumResponseBytes int64
	client               *http.Client
	now                  func() time.Time
}

type publicSearchResponse struct {
	SearchID             string                         `json:"search_id"`
	RankerVersion        string                         `json:"ranker_version"`
	RankerDescriptor     *models.SearchRankerDescriptor `json:"ranker_descriptor"`
	CatalogRevision      string                         `json:"catalog_revision"`
	EffectivePipeline    models.SearchPipeline          `json:"effective_pipeline"`
	Degraded             *bool                          `json:"degraded"`
	Total                int                            `json:"total"`
	Page                 int                            `json:"page"`
	PerPage              int                            `json:"per_page"`
	Facets               *models.SearchFacets           `json:"facets"`
	Items                *[]publicSearchResult          `json:"items"`
	rankerDescriptorHash string
}

type publicSearchResult struct {
	CanonicalID string            `json:"canonical_id"`
	Source      models.ItemSource `json:"source"`
	SourceID    string            `json:"source_id"`
}

// NewHTTPClient validates configuration without making a network request.
func NewHTTPClient(config HTTPClientConfig) (*HTTPClient, error) {
	endpoint, endpointError := url.Parse(strings.TrimSpace(config.Endpoint))
	if endpointError != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, errors.New("search endpoint must be an absolute HTTP URL")
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, errors.New("search endpoint scheme must be http or https")
	}
	if endpoint.Scheme == "http" && !isLoopbackHost(endpoint.Hostname()) {
		return nil, errors.New("plain HTTP search endpoints must use a loopback host")
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("search endpoint must not contain credentials, query parameters, or a fragment")
	}
	if config.CandidateLimit < 1 || config.CandidateLimit > models.MaxSearchPerPage {
		return nil, fmt.Errorf("candidate limit must be between 1 and %d", models.MaxSearchPerPage)
	}

	requestTimeout := config.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = defaultRequestTimeout
	}
	maximumResponseBytes := config.MaximumResponseBytes
	if maximumResponseBytes <= 0 {
		maximumResponseBytes = defaultMaximumResponseBytes
	}
	httpClient := config.Client
	if httpClient == nil {
		httpClient = &http.Client{}
	} else {
		clientCopy := *httpClient
		httpClient = &clientCopy
	}
	httpClient.CheckRedirect = rejectHTTPRedirect
	now := config.Now
	if now == nil {
		now = time.Now
	}

	return &HTTPClient{
		endpoint:             endpoint,
		requestTimeout:       requestTimeout,
		candidateLimit:       config.CandidateLimit,
		maximumResponseBytes: maximumResponseBytes,
		client:               httpClient,
		now:                  now,
	}, nil
}

// Endpoint returns the canonical endpoint recorded in evaluation reports.
func (client *HTTPClient) Endpoint() string {
	return client.endpoint.String()
}

// Search executes and validates one bounded public search request.
func (client *HTTPClient) Search(searchContext context.Context, query Query) (SearchObservation, error) {
	requestBody, requestBodyError := json.Marshal(client.requestBody(query))
	if requestBodyError != nil {
		return SearchObservation{}, newSearchFailure(FailureStageTransport, "request_encoding", "search request could not be encoded", requestBodyError)
	}
	requestContext, cancelRequest := context.WithTimeout(searchContext, client.requestTimeout)
	defer cancelRequest()

	request, requestError := http.NewRequestWithContext(requestContext, http.MethodPost, client.endpoint.String(), bytes.NewReader(requestBody))
	if requestError != nil {
		return SearchObservation{}, newSearchFailure(FailureStageTransport, "request_creation", "search request could not be created", requestError)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "app-catalogo-search-eval/1")

	requestStartedAt := client.now()
	response, responseError := client.client.Do(request)
	if responseError != nil {
		return SearchObservation{}, newSearchFailure(FailureStageTransport, "request_failed", "search request failed", responseError)
	}

	responseBody, readError := io.ReadAll(io.LimitReader(response.Body, client.maximumResponseBytes+1))
	closeError := response.Body.Close()
	requestLatency := client.now().Sub(requestStartedAt)
	if readError != nil {
		return SearchObservation{}, newSearchFailure(FailureStageTransport, "response_read", "search response could not be read", errors.Join(readError, closeError))
	}
	if closeError != nil {
		return SearchObservation{}, newSearchFailure(FailureStageTransport, "response_close", "search response could not be closed", closeError)
	}
	if int64(len(responseBody)) > client.maximumResponseBytes {
		return SearchObservation{}, newSearchFailure(FailureStageContract, "response_too_large", "search response exceeds the configured size bound", nil)
	}
	if response.StatusCode != http.StatusOK {
		return SearchObservation{}, newSearchFailure(FailureStageTransport, "http_status", "search endpoint returned a non-success status", nil)
	}

	searchResponse, contractError := decodePublicSearchResponse(responseBody, client.candidateLimit)
	if contractError != nil {
		return SearchObservation{}, contractError
	}
	documents := make([]DocumentKey, len(*searchResponse.Items))
	for resultIndex, searchResult := range *searchResponse.Items {
		documents[resultIndex] = DocumentKey{Source: searchResult.Source, SourceID: searchResult.SourceID}
	}
	return SearchObservation{
		QueryID:              query.QueryID,
		Documents:            documents,
		SearchID:             searchResponse.SearchID,
		RankerVersion:        searchResponse.RankerVersion,
		RankerDescriptorHash: searchResponse.rankerDescriptorHash,
		CatalogRevision:      searchResponse.CatalogRevision,
		EffectivePipeline:    searchResponse.EffectivePipeline,
		Degraded:             *searchResponse.Degraded,
		Latency:              requestLatency,
	}, nil
}

func (client *HTTPClient) requestBody(query Query) models.SearchRequestBody {
	page := models.DefaultSearchPage
	perPage := client.candidateLimit
	return models.SearchRequestBody{
		Q:                 query.Text,
		Types:             query.Types,
		Page:              &page,
		PerPage:           &perPage,
		Modalidade:        query.Filters.Modalidade,
		Bairro:            query.Filters.Bairro,
		Orgao:             query.Filters.Orgao,
		Turno:             query.Filters.Turno,
		RegimeContratacao: query.Filters.RegimeContratacao,
		ModeloTrabalho:    query.Filters.ModeloTrabalho,
		PCD:               query.Filters.PCD,
		CanalAtendimento:  query.Filters.CanalAtendimento,
		Tema:              query.Filters.Tema,
		Segmento:          query.Filters.Segmento,
	}
}

func decodePublicSearchResponse(responseBody []byte, candidateLimit int) (publicSearchResponse, error) {
	responseDecoder := json.NewDecoder(bytes.NewReader(responseBody))
	var searchResponse publicSearchResponse
	if decodeError := responseDecoder.Decode(&searchResponse); decodeError != nil {
		return publicSearchResponse{}, newSearchFailure(FailureStageContract, "invalid_json", "search response is not valid JSON", decodeError)
	}
	var trailingJSON any
	if trailingError := responseDecoder.Decode(&trailingJSON); !errors.Is(trailingError, io.EOF) {
		return publicSearchResponse{}, newSearchFailure(FailureStageContract, "trailing_json", "search response contains more than one JSON value", trailingError)
	}
	if _, parseError := uuid.Parse(searchResponse.SearchID); parseError != nil {
		return publicSearchResponse{}, newSearchFailure(FailureStageContract, "invalid_search_id", "search response has an invalid search_id", parseError)
	}
	if !stableIdentifierPattern.MatchString(searchResponse.RankerVersion) {
		return publicSearchResponse{}, newSearchFailure(FailureStageContract, "invalid_ranker_version", "search response has an invalid ranker_version", nil)
	}
	if searchResponse.RankerDescriptor == nil {
		return publicSearchResponse{}, newSearchFailure(FailureStageContract, "missing_ranker_descriptor", "search response has no ranker descriptor", nil)
	}
	descriptorHash, descriptorError := validateRankerDescriptor(*searchResponse.RankerDescriptor, searchResponse.RankerVersion)
	if descriptorError != nil {
		return publicSearchResponse{}, descriptorError
	}
	searchResponse.rankerDescriptorHash = descriptorHash
	if !stableIdentifierPattern.MatchString(searchResponse.CatalogRevision) {
		return publicSearchResponse{}, newSearchFailure(FailureStageContract, "invalid_catalog_revision", "search response has an invalid catalog revision", nil)
	}
	if !searchResponse.EffectivePipeline.Valid() {
		return publicSearchResponse{}, newSearchFailure(FailureStageContract, "invalid_effective_pipeline", "search response has an invalid effective pipeline", nil)
	}
	if searchResponse.Degraded == nil {
		return publicSearchResponse{}, newSearchFailure(FailureStageContract, "missing_degraded", "search response has no degraded state", nil)
	}
	if searchResponse.Total < 0 || searchResponse.Page != models.DefaultSearchPage || searchResponse.PerPage != candidateLimit {
		return publicSearchResponse{}, newSearchFailure(FailureStageContract, "invalid_pagination", "search response has invalid pagination metadata", nil)
	}
	if searchResponse.Facets == nil {
		return publicSearchResponse{}, newSearchFailure(FailureStageContract, "missing_facets", "search response has no facets", nil)
	}
	if facetsError := validateSearchFacets(*searchResponse.Facets, searchResponse.Total); facetsError != nil {
		return publicSearchResponse{}, facetsError
	}
	if searchResponse.Items == nil {
		return publicSearchResponse{}, newSearchFailure(FailureStageContract, "missing_items", "search response has no items array", nil)
	}
	if len(*searchResponse.Items) > candidateLimit {
		return publicSearchResponse{}, newSearchFailure(FailureStageContract, "too_many_items", "search response exceeds the requested candidate limit", nil)
	}
	if searchResponse.Total < len(*searchResponse.Items) {
		return publicSearchResponse{}, newSearchFailure(FailureStageContract, "invalid_total", "search response total is smaller than its items array", nil)
	}
	expectedItemCount := min(searchResponse.Total, candidateLimit)
	if len(*searchResponse.Items) != expectedItemCount {
		return publicSearchResponse{}, newSearchFailure(FailureStageContract, "incomplete_first_page", "search response first page is incomplete", nil)
	}
	seenDocuments := make(map[DocumentKey]struct{}, len(*searchResponse.Items))
	seenCanonicalEntities := make(map[string]struct{}, len(*searchResponse.Items))
	for _, searchResult := range *searchResponse.Items {
		if _, validSource := validItemSources[searchResult.Source]; !validSource {
			return publicSearchResponse{}, newSearchFailure(FailureStageContract, "invalid_source", "search result has an invalid source", nil)
		}
		if !isCanonicalSourceID(searchResult.SourceID) {
			return publicSearchResponse{}, newSearchFailure(FailureStageContract, "missing_source_id", "search result has no source_id", nil)
		}
		document := DocumentKey{Source: searchResult.Source, SourceID: searchResult.SourceID}
		if _, duplicate := seenDocuments[document]; duplicate {
			return publicSearchResponse{}, newSearchFailure(FailureStageContract, "duplicate_document", "search response contains a duplicate source and source_id", nil)
		}
		seenDocuments[document] = struct{}{}
		if !canonicalEntityIDPattern.MatchString(searchResult.CanonicalID) {
			return publicSearchResponse{}, newSearchFailure(FailureStageContract, "invalid_canonical_id", "search result has an invalid canonical_id", nil)
		}
		if _, duplicate := seenCanonicalEntities[searchResult.CanonicalID]; duplicate {
			return publicSearchResponse{}, newSearchFailure(FailureStageContract, "duplicate_canonical_entity", "search response contains a duplicate canonical entity", nil)
		}
		seenCanonicalEntities[searchResult.CanonicalID] = struct{}{}
	}
	return searchResponse, nil
}

func validateSearchFacets(searchFacets models.SearchFacets, total int) error {
	if searchFacets.Version != models.SearchFacetVersion || !searchFacets.Scope.Valid() ||
		searchFacets.Types == nil || searchFacets.Modalidades == nil ||
		searchFacets.Bairros == nil || searchFacets.Organizations == nil {
		return newSearchFailure(FailureStageContract, "invalid_facets", "search response has invalid facet provenance", nil)
	}
	facetCollections := [][]models.SearchFacetValue{
		searchFacets.Types,
		searchFacets.Modalidades,
		searchFacets.Bairros,
		searchFacets.Organizations,
	}
	if searchFacets.Scope == models.SearchFacetScopeUnavailable {
		for _, facetValues := range facetCollections {
			if len(facetValues) > 0 {
				return newSearchFailure(FailureStageContract, "invalid_facets", "unavailable search facets contain values", nil)
			}
		}
	}
	for _, facetValues := range facetCollections {
		if len(facetValues) > models.MaxSearchFacetValues {
			return newSearchFailure(FailureStageContract, "invalid_facets", "search response exceeds the facet value bound", nil)
		}
		seenValues := make(map[string]struct{}, len(facetValues))
		previousCount := int(^uint(0) >> 1)
		previousValue := ""
		for facetIndex, facetValue := range facetValues {
			if !validFacetText(facetValue.Value, models.MaxSearchFilterRunes) ||
				!validFacetText(facetValue.Label, models.MaxSearchFacetLabelRunes) || facetValue.Count < 1 ||
				facetValue.Count > total || facetValue.Count > previousCount ||
				(facetIndex > 0 && facetValue.Count == previousCount && facetValue.Value < previousValue) {
				return newSearchFailure(FailureStageContract, "invalid_facets", "search response contains an invalid facet value", nil)
			}
			if _, duplicate := seenValues[facetValue.Value]; duplicate {
				return newSearchFailure(FailureStageContract, "invalid_facets", "search response contains a duplicate facet value", nil)
			}
			seenValues[facetValue.Value] = struct{}{}
			previousCount = facetValue.Count
			previousValue = facetValue.Value
		}
	}
	return nil
}

func validFacetText(facetText string, maximumRunes int) bool {
	if !utf8.ValidString(facetText) || facetText == "" || strings.TrimSpace(facetText) != facetText ||
		utf8.RuneCountInString(facetText) > maximumRunes {
		return false
	}
	for _, character := range facetText {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validateRankerDescriptor(descriptor models.SearchRankerDescriptor, rankerVersion string) (string, error) {
	if descriptor.SchemaVersion != "search-ranker/v1" ||
		!stableIdentifierPattern.MatchString(descriptor.BaseVersion) ||
		!stableIdentifierPattern.MatchString(descriptor.RetrievalVersion) ||
		!stableIdentifierPattern.MatchString(descriptor.QueryExpansionVersion) ||
		!stableIdentifierPattern.MatchString(descriptor.DeduplicationVersion) ||
		descriptor.CandidatePoolSize < 1 ||
		!positiveFinite(descriptor.ReciprocalRankK) ||
		!positiveFinite(descriptor.TrigramThreshold) ||
		!positiveFinite(descriptor.MaximumSemanticDistance) ||
		descriptor.MaximumSemanticDistance > repository.MaximumCosineDistance ||
		!validRetrievalWeights(descriptor.Weights) {
		return "", newSearchFailure(FailureStageContract, "invalid_ranker_descriptor", "search response has an invalid ranker descriptor", nil)
	}
	if descriptor.SemanticEnabled {
		if descriptor.Embedding == nil || descriptor.Embedding.Dimensions < 1 ||
			!stableIdentifierPattern.MatchString(descriptor.Embedding.Model) ||
			!stableIdentifierPattern.MatchString(descriptor.Embedding.Version) ||
			!stableIdentifierPattern.MatchString(descriptor.Embedding.DocumentTaskType) ||
			!stableIdentifierPattern.MatchString(descriptor.Embedding.QueryTaskType) ||
			!stableIdentifierPattern.MatchString(descriptor.Embedding.DocumentVersion) {
			return "", newSearchFailure(FailureStageContract, "invalid_ranker_descriptor", "search response has an invalid semantic descriptor", nil)
		}
	}
	if descriptor.HyDEEnabled && (!descriptor.SemanticEnabled || !stableIdentifierPattern.MatchString(descriptor.HyDEModel)) {
		return "", newSearchFailure(FailureStageContract, "invalid_ranker_descriptor", "search response has an invalid HyDE descriptor", nil)
	}
	if descriptor.RerankerEnabled &&
		(!stableIdentifierPattern.MatchString(descriptor.RerankerVersion) || descriptor.RerankerCandidateLimit < 1) {
		return "", newSearchFailure(FailureStageContract, "invalid_ranker_descriptor", "search response has an invalid reranker descriptor", nil)
	}

	encodedDescriptor, encodeError := json.Marshal(descriptor)
	if encodeError != nil {
		return "", newSearchFailure(FailureStageContract, "invalid_ranker_descriptor", "search response ranker descriptor could not be encoded", encodeError)
	}
	descriptorDigest := sha256.Sum256(encodedDescriptor)
	descriptorHash := fmt.Sprintf("%x", descriptorDigest)
	expectedRankerVersion := fmt.Sprintf("%s-%s", descriptor.BaseVersion, descriptorHash[:12])
	if rankerVersion != expectedRankerVersion {
		return "", newSearchFailure(FailureStageContract, "ranker_descriptor_mismatch", "search response ranker version does not match its descriptor", nil)
	}
	return descriptorHash, nil
}

func positiveFinite(number float64) bool {
	return number > 0 && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func validRetrievalWeights(weights models.SearchRetrievalWeights) bool {
	for _, weight := range []float64{weights.Exact, weights.FullText, weights.Trigram, weights.Semantic, weights.HyDE} {
		if weight < 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
			return false
		}
	}
	return weights.Exact+weights.FullText+weights.Trigram+weights.Semantic+weights.HyDE > 0
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		return true
	}
	ipAddress := net.ParseIP(host)
	return ipAddress != nil && ipAddress.IsLoopback()
}

func rejectHTTPRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func newSearchFailure(stage FailureStage, code, safeMessage string, cause error) *SearchFailureError {
	return &SearchFailureError{
		Stage:       stage,
		Code:        code,
		SafeMessage: safeMessage,
		Cause:       cause,
	}
}
