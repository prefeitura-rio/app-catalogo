package services

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"
	"golang.org/x/text/unicode/norm"

	"github.com/prefeitura-rio/app-catalogo/internal/cache"
	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/observability"
	"github.com/prefeitura-rio/app-catalogo/internal/query"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

const (
	searchRankerSchemaVersion    = "search-ranker/v2"
	searchDeduplicationVersion   = "canonical-entity-v2"
	defaultRankerVersion         = "hybrid-rrf-v3"
	unversionedComponent         = "unversioned"
	maximumRerankerCandidates    = 40
	maximumRerankerDocumentRunes = 2048
	defaultSemanticTimeout       = 3 * time.Second
	maximumRankedSnapshotBytes   = models.MaximumPublicSearchResponseBytes
	maximumCoalescedSearchTime   = 30 * time.Second
	searchPipelineInvalid        = "invalid"
	searchPipelineCache          = "cache"
)

var ErrSearchResponseTooLarge = errors.New("search response exceeds serialization budget")

type searchRepository interface {
	Search(context.Context, *models.SearchRequest) ([]*repository.SearchResult, int, error)
	SearchRanked(context.Context, *models.SearchRequest, repository.RankedSearchOptions) ([]*repository.SearchResult, int, error)
}

type catalogRevisionProvider interface {
	CatalogRevision(context.Context) (string, error)
}

type catalogSnapshotProvider interface {
	CatalogSnapshot(context.Context) (repository.CatalogSnapshotVersion, error)
}

type searchFacetProvider interface {
	SearchFacets(context.Context, *models.SearchRequest) (models.SearchFacets, error)
}

type browseSnapshotProvider interface {
	BrowseSnapshot(context.Context, *models.SearchRequest) (*repository.BrowseSnapshot, error)
}

type rankedSearchSnapshotProvider interface {
	SearchRankedSnapshot(
		context.Context,
		*models.SearchRequest,
		repository.RankedSearchOptions,
	) (*repository.RankedSearchSnapshot, error)
}

type searchCache interface {
	Get(context.Context, string, any) error
	Set(context.Context, string, any, time.Duration) error
}

type semanticSearchClient interface {
	EmbedQuery(context.Context, string) ([]float32, error)
	GenerateHyDE(context.Context, string) (string, error)
	Metadata() clients.EmbeddingMetadata
}

type hydeModelProvider interface {
	HyDEModel() string
}

type hydeMetadataProvider interface {
	HyDEMetadata() models.HyDEGenerationMetadata
}

type searchReranker interface {
	Rerank(context.Context, string, []clients.RerankerDocument) ([]clients.RerankerResult, error)
}

type SearchRuntimeConfig struct {
	RankerVersion           string
	CatalogRevision         string
	RerankerVersion         string
	MaximumSemanticDistance float64
	CandidatePoolSize       int
	SemanticOverfetchFactor int
	SemanticTimeout         time.Duration
	HyDEEnabled             bool
	Weights                 repository.RetrievalWeights
}

func DefaultSearchRuntimeConfig() SearchRuntimeConfig {
	return SearchRuntimeConfig{
		RankerVersion:           defaultRankerVersion,
		CatalogRevision:         unversionedComponent,
		MaximumSemanticDistance: repository.DefaultMaximumSemanticDistance,
		CandidatePoolSize:       repository.DefaultCandidatePoolSize,
		SemanticOverfetchFactor: repository.DefaultSemanticOverfetchFactor,
		SemanticTimeout:         defaultSemanticTimeout,
		Weights:                 repository.DefaultRetrievalWeights(),
	}
}

func (runtimeConfig SearchRuntimeConfig) normalized() SearchRuntimeConfig {
	defaultConfig := DefaultSearchRuntimeConfig()
	runtimeConfig.RankerVersion = strings.TrimSpace(runtimeConfig.RankerVersion)
	if runtimeConfig.RankerVersion == "" {
		runtimeConfig.RankerVersion = defaultConfig.RankerVersion
	}
	runtimeConfig.CatalogRevision = strings.TrimSpace(runtimeConfig.CatalogRevision)
	if runtimeConfig.CatalogRevision == "" {
		runtimeConfig.CatalogRevision = defaultConfig.CatalogRevision
	}
	runtimeConfig.RerankerVersion = strings.TrimSpace(runtimeConfig.RerankerVersion)
	if runtimeConfig.MaximumSemanticDistance <= 0 || runtimeConfig.MaximumSemanticDistance > repository.MaximumCosineDistance {
		runtimeConfig.MaximumSemanticDistance = defaultConfig.MaximumSemanticDistance
	}
	if runtimeConfig.CandidatePoolSize < 1 {
		runtimeConfig.CandidatePoolSize = defaultConfig.CandidatePoolSize
	}
	if runtimeConfig.CandidatePoolSize > repository.MaximumCandidatePoolSize {
		runtimeConfig.CandidatePoolSize = repository.MaximumCandidatePoolSize
	}
	if runtimeConfig.SemanticOverfetchFactor < 1 {
		runtimeConfig.SemanticOverfetchFactor = defaultConfig.SemanticOverfetchFactor
	}
	if runtimeConfig.SemanticOverfetchFactor > repository.MaximumSemanticOverfetchFactor {
		runtimeConfig.SemanticOverfetchFactor = repository.MaximumSemanticOverfetchFactor
	}
	if runtimeConfig.SemanticTimeout <= 0 {
		runtimeConfig.SemanticTimeout = defaultConfig.SemanticTimeout
	}
	if runtimeConfig.Weights == (repository.RetrievalWeights{}) {
		runtimeConfig.Weights = defaultConfig.Weights
	}
	return runtimeConfig
}

type SearchService struct {
	searchRepository  searchRepository
	searchCache       searchCache
	searchTTL         time.Duration
	semanticClient    semanticSearchClient
	rerankerClient    searchReranker
	runtimeConfig     SearchRuntimeConfig
	rankerDescriptor  models.SearchRankerDescriptor
	rankerVersion     string
	rankedSearchGroup singleflight.Group
}

func NewSearchService(
	searchRepository searchRepository,
	searchCache searchCache,
	searchTTL time.Duration,
	semanticClient semanticSearchClient,
	rerankerClient searchReranker,
	runtimeConfig SearchRuntimeConfig,
) *SearchService {
	if isNilInterface(searchCache) {
		searchCache = nil
	}
	if isNilInterface(semanticClient) {
		semanticClient = nil
	}
	if isNilInterface(rerankerClient) {
		rerankerClient = nil
	}
	normalizedConfig := runtimeConfig.normalized()
	service := &SearchService{
		searchRepository: searchRepository,
		searchCache:      searchCache,
		searchTTL:        searchTTL,
		semanticClient:   semanticClient,
		rerankerClient:   rerankerClient,
		runtimeConfig:    normalizedConfig,
	}
	service.rankerDescriptor = buildRankerDescriptor(
		normalizedConfig,
		semanticClient,
		rerankerClient != nil,
	)
	service.rankerVersion = buildRankerVersion(service.rankerDescriptor)
	return service
}

func isNilInterface(candidate any) bool {
	if candidate == nil {
		return true
	}
	candidateReflection := reflect.ValueOf(candidate)
	switch candidateReflection.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return candidateReflection.IsNil()
	default:
		return false
	}
}

func (service *SearchService) RankerVersion() string {
	if service.rankerVersion != "" {
		return service.rankerVersion
	}
	return buildRankerVersion(service.RankerDescriptor())
}

// RankerDescriptor returns the non-secret immutable ranking contract.
func (service *SearchService) RankerDescriptor() models.SearchRankerDescriptor {
	if service.rankerDescriptor.SchemaVersion != "" {
		return service.rankerDescriptor
	}
	return buildRankerDescriptor(
		service.runtimeConfig.normalized(),
		service.semanticClient,
		service.rerankerClient != nil,
	)
}

func buildRankerDescriptor(
	runtimeConfig SearchRuntimeConfig,
	semanticClient semanticSearchClient,
	rerankerEnabled bool,
) models.SearchRankerDescriptor {
	normalizedConfig := runtimeConfig.normalized()
	descriptor := models.SearchRankerDescriptor{
		SchemaVersion:           searchRankerSchemaVersion,
		BaseVersion:             normalizedConfig.RankerVersion,
		RetrievalVersion:        repository.RetrievalVersion,
		QueryExpansionVersion:   query.ExpansionVersion,
		DeduplicationVersion:    searchDeduplicationVersion,
		CandidatePoolSize:       normalizedConfig.CandidatePoolSize,
		SemanticOverfetchFactor: normalizedConfig.SemanticOverfetchFactor,
		TrigramThreshold:        repository.DefaultTrigramThreshold,
		MaximumSemanticDistance: normalizedConfig.MaximumSemanticDistance,
		ReciprocalRankK:         repository.DefaultReciprocalRankK,
		Weights: models.SearchRetrievalWeights{
			Exact:    normalizedConfig.Weights.Exact,
			FullText: normalizedConfig.Weights.FullText,
			Trigram:  normalizedConfig.Weights.Trigram,
			Semantic: normalizedConfig.Weights.Semantic,
			HyDE:     normalizedConfig.Weights.HyDE,
		},
		SemanticEnabled: semanticClient != nil && normalizedConfig.Weights.Semantic > 0,
		HyDEEnabled: normalizedConfig.HyDEEnabled && semanticClient != nil &&
			normalizedConfig.Weights.Semantic > 0 && normalizedConfig.Weights.HyDE > 0,
		RerankerEnabled: rerankerEnabled,
	}
	if semanticClient != nil {
		embeddingMetadata := semanticClient.Metadata()
		descriptor.Embedding = &embeddingMetadata
	}
	if descriptor.HyDEEnabled {
		descriptor.HyDEModel = unversionedComponent
		if metadataProvider, providesMetadata := semanticClient.(hydeMetadataProvider); providesMetadata {
			hydeMetadata := metadataProvider.HyDEMetadata()
			descriptor.HyDEModel = hydeMetadata.Model
			descriptor.HyDEPromptVersion = hydeMetadata.PromptVersion
			descriptor.HyDEPromptSHA256 = hydeMetadata.PromptSHA256
			descriptor.HyDETemperature = &hydeMetadata.Temperature
			descriptor.HyDESeed = &hydeMetadata.Seed
			descriptor.HyDECandidateCount = &hydeMetadata.CandidateCount
			descriptor.HyDEMaxOutputTokens = &hydeMetadata.MaxOutputTokens
			descriptor.HyDEResponseMIMEType = hydeMetadata.ResponseMIMEType
			descriptor.HyDEDeterminismPolicy = hydeMetadata.DeterminismPolicy
		} else if modelProvider, providesModel := semanticClient.(hydeModelProvider); providesModel {
			if hydeModel := strings.TrimSpace(modelProvider.HyDEModel()); hydeModel != "" {
				descriptor.HyDEModel = hydeModel
			}
		}
	}
	if rerankerEnabled {
		descriptor.RerankerVersion = normalizedConfig.RerankerVersion
		if descriptor.RerankerVersion == "" {
			descriptor.RerankerVersion = unversionedComponent
		}
		descriptor.RerankerCandidateLimit = maximumRerankerCandidates
	}
	return descriptor
}

func buildRankerVersion(descriptor models.SearchRankerDescriptor) string {
	serializedDescriptor, marshalError := json.Marshal(descriptor)
	if marshalError != nil {
		return descriptor.BaseVersion + "-invalid-config"
	}
	descriptorDigest := sha256.Sum256(serializedDescriptor)
	return fmt.Sprintf("%s-%x", descriptor.BaseVersion, descriptorDigest[:6])
}

type searchExecution struct {
	pipeline        models.SearchPipeline
	degraded        bool
	snapshotVersion repository.CatalogSnapshotVersion
}

type rankedSearchSnapshot struct {
	RankerVersion     string                        `json:"ranker_version"`
	RankerDescriptor  models.SearchRankerDescriptor `json:"ranker_descriptor"`
	CatalogRevision   string                        `json:"catalog_revision"`
	EffectivePipeline models.SearchPipeline         `json:"effective_pipeline"`
	Degraded          bool                          `json:"degraded"`
	Total             int                           `json:"total"`
	Facets            models.SearchFacets           `json:"facets"`
	Items             []*models.SearchItem          `json:"items"`
}

type rankedSearchOutcome struct {
	snapshot        *rankedSearchSnapshot
	snapshotVersion repository.CatalogSnapshotVersion
	cacheHit        bool
}

// Search executes a bounded multi-stage pipeline and paginates only after all
// optional ranking stages have completed.
func (service *SearchService) Search(searchContext context.Context, searchRequest *models.SearchRequest) (_ *models.SearchResponse, searchError error) {
	searchStartedAt := time.Now()
	searchPipeline := searchPipelineInvalid
	searchOutcome := "error"
	candidateCount := 0
	hasTypeFilter := "false"
	hasQuery := "false"
	if searchRequest != nil {
		hasTypeFilter = strconv.FormatBool(len(searchRequest.Types) > 0)
		hasQuery = strconv.FormatBool(strings.TrimSpace(searchRequest.Q) != "")
	}
	defer func() {
		searchDurationSeconds := time.Since(searchStartedAt).Seconds()
		observability.SearchDuration.WithLabelValues(hasQuery, hasTypeFilter).Observe(searchDurationSeconds)
		observability.SearchPipelineDuration.WithLabelValues(searchPipeline, hasTypeFilter).Observe(searchDurationSeconds)
		observability.SearchRequestsTotal.WithLabelValues(searchPipeline, searchOutcome).Inc()
		if searchOutcome == "success" {
			observability.SearchCandidates.WithLabelValues(searchPipeline).Observe(float64(candidateCount))
			if candidateCount == 0 {
				observability.SearchZeroResultsTotal.WithLabelValues(searchPipeline).Inc()
			}
		}
	}()
	if searchRequest == nil {
		return nil, fmt.Errorf("invalid search request: request must not be nil")
	}

	searchRequest.Normalize()
	if validationError := searchRequest.Validate(); validationError != nil {
		return nil, fmt.Errorf("invalid search request: %w", validationError)
	}
	if searchRequest.Q != "" {
		searchRequest.ExpandedQ = query.Expand(searchRequest.Q)
	}

	initialCatalogSnapshot, catalogSnapshotError := service.catalogSnapshot(searchContext)
	if catalogSnapshotError != nil {
		return nil, catalogSnapshotError
	}
	if searchRequest.Q != "" {
		rankedOutcome, rankedError := service.rankedSearchWithCache(
			searchContext,
			searchRequest,
			initialCatalogSnapshot,
		)
		if rankedError != nil {
			return nil, rankedError
		}
		searchPipeline = string(rankedOutcome.snapshot.EffectivePipeline)
		if rankedOutcome.cacheHit {
			searchPipeline = searchPipelineCache
		}
		searchResponse, responseError := rankedOutcome.snapshot.responsePage(searchRequest)
		if responseError != nil {
			return nil, responseError
		}
		searchOutcome = "success"
		candidateCount = rankedOutcome.snapshot.Total
		return searchResponse, nil
	}
	catalogRevision := initialCatalogSnapshot.Revision
	cacheKey, cacheKeyError := service.cacheKey(searchRequest, catalogRevision)
	if cacheKeyError != nil {
		return nil, cacheKeyError
	}
	if cachedResponse := service.getCachedResponse(searchContext, cacheKey, catalogRevision); cachedResponse != nil {
		revalidatedCatalogSnapshot, revalidationError := service.catalogSnapshot(searchContext)
		if revalidationError != nil {
			return nil, revalidationError
		}
		if revalidatedCatalogSnapshot.Revision == initialCatalogSnapshot.Revision {
			searchPipeline = searchPipelineCache
			searchOutcome = "success"
			candidateCount = cachedResponse.Total
			cachedResponse.SearchID = ""
			return cachedResponse, nil
		}
		observability.SearchCacheOperationsTotal.WithLabelValues("get", "stale_revision").Inc()
		initialCatalogSnapshot = revalidatedCatalogSnapshot
		catalogRevision = revalidatedCatalogSnapshot.Revision
	}

	var searchResults []*repository.SearchResult
	var totalCandidates int
	searchFacets := emptySearchFacets(models.SearchFacetScopeUnavailable)
	browseFacetsLoaded := false
	execution := searchExecution{
		pipeline:        models.SearchPipelineBrowse,
		snapshotVersion: initialCatalogSnapshot,
	}
	searchPipeline = string(execution.pipeline)
	if snapshotProvider, providesSnapshot := service.searchRepository.(browseSnapshotProvider); providesSnapshot {
		browseSnapshot, browseError := snapshotProvider.BrowseSnapshot(searchContext, searchRequest)
		if browseError != nil {
			searchError = browseError
		} else if browseSnapshot == nil || strings.TrimSpace(browseSnapshot.CatalogRevision) == "" {
			searchError = fmt.Errorf("browse snapshot provider returned an invalid snapshot")
		} else {
			searchResults = browseSnapshot.Results
			totalCandidates = browseSnapshot.Total
			searchFacets = browseSnapshot.Facets
			execution.snapshotVersion, searchError = catalogSnapshotVersionFromBrowse(
				browseSnapshot,
				initialCatalogSnapshot,
			)
			if searchError == nil {
				catalogRevision = execution.snapshotVersion.Revision
				browseFacetsLoaded = true
			}
		}
	} else {
		searchResults, totalCandidates, searchError = service.searchRepository.Search(searchContext, searchRequest)
	}
	if searchError != nil {
		return nil, fmt.Errorf("search: %w", searchError)
	}

	if !browseFacetsLoaded {
		facetProvider, providesFacets := service.searchRepository.(searchFacetProvider)
		if providesFacets {
			searchFacets, searchError = facetProvider.SearchFacets(searchContext, searchRequest)
			if searchError != nil {
				execution.degraded = true
				searchFacets = emptySearchFacets(models.SearchFacetScopeUnavailable)
				observability.SearchFallbacksTotal.WithLabelValues(
					string(execution.pipeline),
					string(execution.pipeline),
					"facets_error",
				).Inc()
				log.Warn().Err(searchError).Msg("search: facets unavailable; returning results without facets")
			}
		}
	}
	searchFacets = withNonNilSearchFacetCollections(searchFacets)
	latestCatalogSnapshot, latestSnapshotError := service.catalogSnapshot(searchContext)
	if latestSnapshotError != nil {
		return nil, latestSnapshotError
	}
	if latestCatalogSnapshot.Revision != catalogRevision {
		execution.degraded = true
		observability.SearchFallbacksTotal.WithLabelValues(
			string(execution.pipeline),
			string(execution.pipeline),
			"catalog_revision_changed",
		).Inc()
	}

	searchResponse := service.buildResponse(
		searchResults,
		totalCandidates,
		searchRequest,
		execution,
		catalogRevision,
		searchFacets,
	)
	if !execution.degraded {
		cacheKey, cacheKeyError = service.cacheKey(searchRequest, catalogRevision)
		if cacheKeyError != nil {
			return nil, cacheKeyError
		}
		cacheTTL := catalogSnapshotCacheTTL(service.searchTTL, execution.snapshotVersion)
		service.setCachedResponse(searchContext, cacheKey, searchResponse, cacheTTL)
	} else {
		observability.SearchCacheOperationsTotal.WithLabelValues("set", "skipped_degraded").Inc()
	}
	searchOutcome = "success"
	candidateCount = totalCandidates
	return searchResponse, nil
}

func (service *SearchService) rankedSearchWithCache(
	searchContext context.Context,
	searchRequest *models.SearchRequest,
	initialSnapshot repository.CatalogSnapshotVersion,
) (*rankedSearchOutcome, error) {
	executionRankerVersion := service.RankerVersion()
	cacheKey, cacheKeyError := service.rankedCacheKey(searchRequest, initialSnapshot.Revision)
	if cacheKeyError != nil {
		return nil, cacheKeyError
	}
	if cachedSnapshot := service.getCachedRankedSnapshot(
		searchContext,
		cacheKey,
		initialSnapshot.Revision,
		executionRankerVersion,
	); cachedSnapshot != nil {
		revalidatedSnapshot, revalidationError := service.catalogSnapshot(searchContext)
		if revalidationError != nil {
			return nil, revalidationError
		}
		if revalidatedSnapshot.Revision == initialSnapshot.Revision {
			return &rankedSearchOutcome{
				snapshot:        cachedSnapshot,
				snapshotVersion: initialSnapshot,
				cacheHit:        true,
			}, nil
		}
		observability.SearchCacheOperationsTotal.WithLabelValues("get", "stale_revision").Inc()
		initialSnapshot = revalidatedSnapshot
		cacheKey, cacheKeyError = service.rankedCacheKey(searchRequest, initialSnapshot.Revision)
		if cacheKeyError != nil {
			return nil, cacheKeyError
		}
	}

	rankingRequest := cloneSearchRequest(searchRequest)
	sharedResultChannel := service.rankedSearchGroup.DoChan(cacheKey, func() (any, error) {
		sharedContext, cancelSharedContext := context.WithTimeout(
			context.WithoutCancel(searchContext),
			maximumCoalescedSearchTime,
		)
		defer cancelSharedContext()
		return service.executeRankedSearchSnapshot(
			sharedContext,
			rankingRequest,
			initialSnapshot,
		)
	})
	select {
	case <-searchContext.Done():
		return nil, fmt.Errorf("ranked search wait: %w", searchContext.Err())
	case sharedResult := <-sharedResultChannel:
		if sharedResult.Err != nil {
			return nil, sharedResult.Err
		}
		rankedOutcome, validOutcome := sharedResult.Val.(*rankedSearchOutcome)
		if !validOutcome || rankedOutcome == nil || rankedOutcome.snapshot == nil {
			return nil, errors.New("ranked search coalescer returned an invalid snapshot")
		}
		return rankedOutcome, nil
	}
}

func (service *SearchService) executeRankedSearchSnapshot(
	searchContext context.Context,
	searchRequest *models.SearchRequest,
	initialSnapshot repository.CatalogSnapshotVersion,
) (*rankedSearchOutcome, error) {
	searchResults, _, execution, searchError := service.searchRanked(searchContext, searchRequest)
	if searchError != nil {
		return nil, fmt.Errorf("search: %w", searchError)
	}
	if !validCatalogSnapshotVersion(execution.snapshotVersion) {
		execution.snapshotVersion = initialSnapshot
	}
	catalogRevision := execution.snapshotVersion.Revision
	searchResults = deduplicateSearchResults(searchResults)
	searchFacets := withNonNilSearchFacetCollections(buildCandidateSearchFacets(searchResults))
	if service.rerankerClient != nil && len(searchResults) > 1 {
		var rerankerApplied bool
		var rerankerDegraded bool
		searchResults, rerankerApplied, rerankerDegraded = service.maybeRerank(
			searchContext,
			searchRequest.Q,
			searchResults,
		)
		execution.degraded = execution.degraded || rerankerDegraded
		if rerankerApplied {
			execution.pipeline = rerankedPipeline(execution.pipeline)
		}
	}
	latestSnapshot, latestSnapshotError := service.catalogSnapshot(searchContext)
	if latestSnapshotError != nil {
		return nil, latestSnapshotError
	}
	if latestSnapshot.Revision != catalogRevision {
		execution.degraded = true
		observability.SearchFallbacksTotal.WithLabelValues(
			string(execution.pipeline),
			string(execution.pipeline),
			"catalog_revision_changed",
		).Inc()
	}
	executionDescriptor := service.RankerDescriptor()

	rankedSnapshot := &rankedSearchSnapshot{
		RankerVersion:     buildRankerVersion(executionDescriptor),
		RankerDescriptor:  executionDescriptor,
		CatalogRevision:   catalogRevision,
		EffectivePipeline: execution.pipeline,
		Degraded:          execution.degraded,
		Total:             len(searchResults),
		Facets:            searchFacets,
		Items:             buildSearchItems(searchResults),
	}
	if execution.degraded {
		observability.SearchCacheOperationsTotal.WithLabelValues("set", "skipped_degraded").Inc()
		return &rankedSearchOutcome{snapshot: rankedSnapshot, snapshotVersion: execution.snapshotVersion}, nil
	}
	serializedSnapshot, serializationError := json.Marshal(rankedSnapshot)
	if serializationError != nil {
		return nil, fmt.Errorf("serialize ranked search snapshot: %w", serializationError)
	}
	if len(serializedSnapshot) > maximumRankedSnapshotBytes {
		observability.SearchCacheOperationsTotal.WithLabelValues("set", "skipped_oversize").Inc()
		return &rankedSearchOutcome{snapshot: rankedSnapshot, snapshotVersion: execution.snapshotVersion}, nil
	}
	cacheKey, cacheKeyError := service.rankedCacheKey(searchRequest, catalogRevision)
	if cacheKeyError != nil {
		return nil, cacheKeyError
	}
	cacheTTL := catalogSnapshotCacheTTL(service.searchTTL, execution.snapshotVersion)
	service.setCachedRankedSnapshot(searchContext, cacheKey, rankedSnapshot, cacheTTL)
	return &rankedSearchOutcome{snapshot: rankedSnapshot, snapshotVersion: execution.snapshotVersion}, nil
}

func (snapshot *rankedSearchSnapshot) responsePage(
	searchRequest *models.SearchRequest,
) (*models.SearchResponse, error) {
	searchResponse := &models.SearchResponse{
		RankerVersion:     snapshot.RankerVersion,
		RankerDescriptor:  snapshot.RankerDescriptor,
		CatalogRevision:   snapshot.CatalogRevision,
		EffectivePipeline: snapshot.EffectivePipeline,
		Degraded:          snapshot.Degraded,
		Total:             snapshot.Total,
		Page:              searchRequest.Page,
		PerPage:           searchRequest.PerPage,
		Facets:            snapshot.Facets,
		Items:             paginateSearchItems(snapshot.Items, searchRequest.Page, searchRequest.PerPage),
	}
	searchResponse.SearchID = "00000000-0000-0000-0000-000000000000"
	serializedResponse, serializationError := json.Marshal(searchResponse)
	searchResponse.SearchID = ""
	if serializationError != nil {
		return nil, fmt.Errorf("serialize ranked search response: %w", serializationError)
	}
	if len(serializedResponse) > maximumRankedSnapshotBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrSearchResponseTooLarge, len(serializedResponse))
	}
	return searchResponse, nil
}

func cloneSearchRequest(searchRequest *models.SearchRequest) *models.SearchRequest {
	requestCopy := *searchRequest
	requestCopy.Types = append([]models.ItemType(nil), searchRequest.Types...)
	if searchRequest.Filters.PCD != nil {
		pcdOnly := *searchRequest.Filters.PCD
		requestCopy.Filters.PCD = &pcdOnly
	}
	return &requestCopy
}

func (service *SearchService) searchRanked(
	searchContext context.Context,
	searchRequest *models.SearchRequest,
) ([]*repository.SearchResult, int, searchExecution, error) {
	lexicalOptions := repository.RankedSearchOptions{
		CandidatePoolSize:       service.runtimeConfig.CandidatePoolSize,
		SemanticOverfetchFactor: service.runtimeConfig.SemanticOverfetchFactor,
		MaximumSemanticDistance: service.runtimeConfig.MaximumSemanticDistance,
		Weights:                 service.runtimeConfig.Weights,
	}
	if service.semanticClient == nil || service.runtimeConfig.Weights.Semantic <= 0 {
		searchResults, totalCandidates, snapshotVersion, searchError := service.searchRankedRepository(
			searchContext,
			searchRequest,
			lexicalOptions,
		)
		return searchResults, totalCandidates, searchExecution{
			pipeline:        models.SearchPipelineLexical,
			degraded:        false,
			snapshotVersion: snapshotVersion,
		}, searchError
	}

	embeddingMetadata := service.semanticClient.Metadata()
	queryEmbedding, hydeEmbedding, embeddingError, hydeError := service.generateSemanticVectors(searchContext, searchRequest.Q)
	if embeddingError != nil {
		observability.SearchFallbacksTotal.WithLabelValues(string(models.SearchPipelineHybrid), string(models.SearchPipelineLexical), "embedding_error").Inc()
		log.Warn().Err(embeddingError).Msg("search: query embedding unavailable; using lexical retrieval")
		searchResults, totalCandidates, snapshotVersion, searchError := service.searchRankedRepository(
			searchContext,
			searchRequest,
			lexicalOptions,
		)
		return searchResults, totalCandidates, searchExecution{
			pipeline:        models.SearchPipelineLexical,
			degraded:        true,
			snapshotVersion: snapshotVersion,
		}, searchError
	}

	hybridOptions := lexicalOptions
	hybridOptions.QueryEmbedding = clients.VectorLiteral(queryEmbedding)
	hybridOptions.EmbeddingModel = embeddingMetadata.Model
	hybridOptions.EmbeddingModelVersion = embeddingMetadata.Version
	hybridOptions.EmbeddingDimensions = embeddingMetadata.Dimensions
	hybridOptions.EmbeddingTaskType = embeddingMetadata.DocumentTaskType
	hybridOptions.EmbeddingDocumentVersion = embeddingMetadata.DocumentVersion
	execution := searchExecution{pipeline: models.SearchPipelineHybrid}
	if hydeError == nil && len(hydeEmbedding) > 0 {
		hybridOptions.HyDEEmbedding = clients.VectorLiteral(hydeEmbedding)
		execution.pipeline = models.SearchPipelineHybridHyDE
	} else if service.runtimeConfig.HyDEEnabled && hydeError != nil {
		observability.SearchFallbacksTotal.WithLabelValues(string(models.SearchPipelineHybridHyDE), string(models.SearchPipelineHybrid), "hyde_error").Inc()
		log.Debug().Err(hydeError).Msg("search: HyDE unavailable; using query embedding")
		execution.degraded = true
	}

	searchResults, totalCandidates, snapshotVersion, hybridError := service.searchRankedRepository(
		searchContext,
		searchRequest,
		hybridOptions,
	)
	if hybridError == nil {
		execution.snapshotVersion = snapshotVersion
		return searchResults, totalCandidates, execution, nil
	}
	if hybridOptions.HyDEEmbedding != "" {
		observability.SearchFallbacksTotal.WithLabelValues(string(models.SearchPipelineHybridHyDE), string(models.SearchPipelineHybrid), "repository_error").Inc()
		hybridOptions.HyDEEmbedding = ""
		searchResults, totalCandidates, snapshotVersion, hybridError = service.searchRankedRepository(
			searchContext,
			searchRequest,
			hybridOptions,
		)
		if hybridError == nil {
			return searchResults, totalCandidates, searchExecution{
				pipeline:        models.SearchPipelineHybrid,
				degraded:        true,
				snapshotVersion: snapshotVersion,
			}, nil
		}
	}

	observability.SearchFallbacksTotal.WithLabelValues(string(models.SearchPipelineHybrid), string(models.SearchPipelineLexical), "repository_error").Inc()
	log.Warn().Err(hybridError).Msg("search: hybrid retrieval unavailable; using lexical retrieval")
	searchResults, totalCandidates, snapshotVersion, lexicalError := service.searchRankedRepository(
		searchContext,
		searchRequest,
		lexicalOptions,
	)
	if lexicalError != nil {
		return nil, 0, searchExecution{
			pipeline: models.SearchPipelineLexical,
			degraded: true,
		}, fmt.Errorf("hybrid retrieval: %v; lexical fallback: %w", hybridError, lexicalError)
	}
	return searchResults, totalCandidates, searchExecution{
		pipeline:        models.SearchPipelineLexical,
		degraded:        true,
		snapshotVersion: snapshotVersion,
	}, nil
}

func (service *SearchService) searchRankedRepository(
	searchContext context.Context,
	searchRequest *models.SearchRequest,
	searchOptions repository.RankedSearchOptions,
) ([]*repository.SearchResult, int, repository.CatalogSnapshotVersion, error) {
	if snapshotProvider, providesSnapshot := service.searchRepository.(rankedSearchSnapshotProvider); providesSnapshot {
		rankedSnapshot, snapshotError := snapshotProvider.SearchRankedSnapshot(
			searchContext,
			searchRequest,
			searchOptions,
		)
		if snapshotError != nil {
			return nil, 0, repository.CatalogSnapshotVersion{}, snapshotError
		}
		if rankedSnapshot == nil || strings.TrimSpace(rankedSnapshot.CatalogRevision) == "" {
			return nil, 0, repository.CatalogSnapshotVersion{}, fmt.Errorf("ranked snapshot provider returned an invalid snapshot")
		}
		snapshotVersion := rankedSnapshot.SnapshotVersion
		if validCatalogSnapshotVersion(snapshotVersion) &&
			snapshotVersion.Revision != rankedSnapshot.CatalogRevision {
			return nil, 0, repository.CatalogSnapshotVersion{}, fmt.Errorf(
				"ranked snapshot provider returned conflicting revisions %q and %q",
				snapshotVersion.Revision,
				rankedSnapshot.CatalogRevision,
			)
		}
		if !validCatalogSnapshotVersion(snapshotVersion) {
			snapshotVersion.Revision = rankedSnapshot.CatalogRevision
		}
		return rankedSnapshot.Results, rankedSnapshot.Total, snapshotVersion, nil
	}

	searchResults, totalCandidates, searchError := service.searchRepository.SearchRanked(
		searchContext,
		searchRequest,
		searchOptions,
	)
	return searchResults, totalCandidates, repository.CatalogSnapshotVersion{}, searchError
}

type vectorGenerationResult struct {
	embedding []float32
	err       error
}

func (service *SearchService) generateSemanticVectors(
	searchContext context.Context,
	searchQuery string,
) ([]float32, []float32, error, error) {
	semanticContext, cancelSemanticContext := context.WithTimeout(searchContext, service.runtimeConfig.SemanticTimeout)
	defer cancelSemanticContext()

	if !service.runtimeConfig.HyDEEnabled || service.runtimeConfig.Weights.HyDE <= 0 {
		queryEmbedding, embeddingError := service.semanticClient.EmbedQuery(semanticContext, searchQuery)
		return queryEmbedding, nil, embeddingError, nil
	}

	queryEmbeddingChannel := make(chan vectorGenerationResult, 1)
	hydeEmbeddingChannel := make(chan vectorGenerationResult, 1)
	go service.generateQueryEmbedding(semanticContext, searchQuery, queryEmbeddingChannel)
	go service.generateHyDEEmbedding(semanticContext, searchQuery, hydeEmbeddingChannel)

	queryEmbeddingResult := <-queryEmbeddingChannel
	hydeEmbeddingResult := <-hydeEmbeddingChannel
	return queryEmbeddingResult.embedding, hydeEmbeddingResult.embedding, queryEmbeddingResult.err, hydeEmbeddingResult.err
}

func (service *SearchService) generateQueryEmbedding(
	searchContext context.Context,
	searchQuery string,
	embeddingChannel chan<- vectorGenerationResult,
) {
	queryEmbedding, embeddingError := service.semanticClient.EmbedQuery(searchContext, searchQuery)
	embeddingChannel <- vectorGenerationResult{embedding: queryEmbedding, err: embeddingError}
}

func (service *SearchService) generateHyDEEmbedding(
	searchContext context.Context,
	searchQuery string,
	embeddingChannel chan<- vectorGenerationResult,
) {
	hydeDocument, generationError := service.semanticClient.GenerateHyDE(searchContext, searchQuery)
	if generationError != nil {
		embeddingChannel <- vectorGenerationResult{err: generationError}
		return
	}
	hydeEmbedding, embeddingError := service.semanticClient.EmbedQuery(searchContext, hydeDocument)
	embeddingChannel <- vectorGenerationResult{embedding: hydeEmbedding, err: embeddingError}
}

func (service *SearchService) maybeRerank(
	searchContext context.Context,
	searchQuery string,
	searchResults []*repository.SearchResult,
) ([]*repository.SearchResult, bool, bool) {
	rerankerCandidateCount := min(len(searchResults), maximumRerankerCandidates)
	rerankerCandidates := searchResults[:rerankerCandidateCount]
	rerankerDocuments := make([]clients.RerankerDocument, len(rerankerCandidates))
	for candidateIndex, searchResult := range rerankerCandidates {
		documentParts := []string{
			searchResult.Item.Title,
			searchResult.Item.ShortDesc,
			searchResult.Item.Organization,
			strings.Join(searchResult.Item.Tags, " "),
		}
		rerankerDocuments[candidateIndex] = clients.RerankerDocument{
			ID: searchResult.Item.ID.String(),
			Text: truncateRerankerText(
				strings.Join(nonEmptyStrings(documentParts), ". "),
				maximumRerankerDocumentRunes,
			),
		}
	}

	rerankerResults, rerankerError := service.rerankerClient.Rerank(searchContext, searchQuery, rerankerDocuments)
	if rerankerError != nil {
		observability.SearchReranksTotal.WithLabelValues("error").Inc()
		log.Warn().Err(rerankerError).Msg("search: reranker unavailable; preserving fused order")
		return searchResults, false, true
	}
	reorderedCandidates, validResponse := reorderRerankerCandidates(rerankerCandidates, rerankerResults)
	if !validResponse {
		observability.SearchReranksTotal.WithLabelValues("invalid_response").Inc()
		log.Warn().Msg("search: reranker returned an invalid permutation; preserving fused order")
		return searchResults, false, true
	}
	observability.SearchReranksTotal.WithLabelValues("success").Inc()
	return append(reorderedCandidates, searchResults[rerankerCandidateCount:]...), true, false
}

func truncateRerankerText(sourceText string, maximumRunes int) string {
	if maximumRunes <= 0 {
		return ""
	}
	textRunes := []rune(sourceText)
	if len(textRunes) <= maximumRunes {
		return sourceText
	}
	return string(textRunes[:maximumRunes])
}

func rerankedPipeline(pipeline models.SearchPipeline) models.SearchPipeline {
	switch pipeline {
	case models.SearchPipelineLexical:
		return models.SearchPipelineLexicalReranked
	case models.SearchPipelineHybrid:
		return models.SearchPipelineHybridReranked
	case models.SearchPipelineHybridHyDE:
		return models.SearchPipelineHybridHyDEReranked
	default:
		return pipeline
	}
}

func reorderRerankerCandidates(
	searchResults []*repository.SearchResult,
	rerankerResults []clients.RerankerResult,
) ([]*repository.SearchResult, bool) {
	if len(searchResults) != len(rerankerResults) {
		return nil, false
	}

	searchResultsByID := make(map[string]*repository.SearchResult, len(searchResults))
	originalPositions := make(map[string]int, len(searchResults))
	for resultIndex, searchResult := range searchResults {
		resultID := searchResult.Item.ID.String()
		searchResultsByID[resultID] = searchResult
		originalPositions[resultID] = resultIndex
	}

	seenResultIDs := make(map[string]struct{}, len(rerankerResults))
	validatedResults := append([]clients.RerankerResult(nil), rerankerResults...)
	for _, rerankerResult := range validatedResults {
		if math.IsNaN(rerankerResult.Score) || math.IsInf(rerankerResult.Score, 0) {
			return nil, false
		}
		if _, exists := searchResultsByID[rerankerResult.ID]; !exists {
			return nil, false
		}
		if _, duplicate := seenResultIDs[rerankerResult.ID]; duplicate {
			return nil, false
		}
		seenResultIDs[rerankerResult.ID] = struct{}{}
	}

	sort.SliceStable(validatedResults, func(firstIndex int, secondIndex int) bool {
		if validatedResults[firstIndex].Score == validatedResults[secondIndex].Score {
			return originalPositions[validatedResults[firstIndex].ID] < originalPositions[validatedResults[secondIndex].ID]
		}
		return validatedResults[firstIndex].Score > validatedResults[secondIndex].Score
	})

	reorderedResults := make([]*repository.SearchResult, len(validatedResults))
	for resultIndex, rerankerResult := range validatedResults {
		reorderedResults[resultIndex] = searchResultsByID[rerankerResult.ID]
	}
	return reorderedResults, true
}

func paginateSearchItems(searchItems []*models.SearchItem, page int, perPage int) []*models.SearchItem {
	if len(searchItems) == 0 || page < 1 || perPage < 1 {
		return []*models.SearchItem{}
	}
	pageIndex := page - 1
	if pageIndex > (len(searchItems)-1)/perPage {
		return []*models.SearchItem{}
	}
	itemOffset := pageIndex * perPage
	itemLimit := min(itemOffset+perPage, len(searchItems))
	return searchItems[itemOffset:itemLimit]
}

func nonEmptyStrings(stringsToFilter []string) []string {
	filteredStrings := make([]string, 0, len(stringsToFilter))
	for _, candidateString := range stringsToFilter {
		if trimmedString := strings.TrimSpace(candidateString); trimmedString != "" {
			filteredStrings = append(filteredStrings, trimmedString)
		}
	}
	return filteredStrings
}

func deduplicateSearchResults(searchResults []*repository.SearchResult) []*repository.SearchResult {
	uniqueResults := make([]*repository.SearchResult, 0, len(searchResults))
	seenCanonicalEntities := make(map[string]struct{}, len(searchResults))
	for _, searchResult := range searchResults {
		if searchResult == nil || searchResult.Item == nil {
			continue
		}
		canonicalID := canonicalSearchEntityID(searchResult.Item)
		if _, duplicate := seenCanonicalEntities[canonicalID]; duplicate {
			continue
		}
		seenCanonicalEntities[canonicalID] = struct{}{}
		uniqueResults = append(uniqueResults, searchResult)
	}
	return uniqueResults
}

func canonicalSearchEntityID(catalogItem *models.CatalogItem) string {
	canonicalEvidence := canonicalSearchEntityEvidence(catalogItem)
	canonicalDigest := sha256.Sum256([]byte("catalog-search-entity:v1\x00" + canonicalEvidence))
	return fmt.Sprintf("entity-v1:%x", canonicalDigest)
}

func canonicalSearchEntityEvidence(catalogItem *models.CatalogItem) string {
	if catalogItem == nil {
		return "invalid"
	}
	var sourceFields struct {
		CanonicalID string `json:"canonical_id"`
		Slug        string `json:"slug"`
	}
	if len(catalogItem.SourceData) > 0 {
		_ = json.Unmarshal(catalogItem.SourceData, &sourceFields)
	}
	if canonicalID := strings.TrimSpace(sourceFields.CanonicalID); canonicalID != "" {
		return "explicit\x00" + strings.ToLower(canonicalID)
	}
	if catalogItem.Type == models.TypeService {
		if slug := canonicalServiceSlug(sourceFields.Slug, catalogItem.URL); slug != "" {
			return "service-slug\x00" + slug
		}
	}
	return strings.Join([]string{
		"source-document",
		string(catalogItem.Type),
		string(catalogItem.Source),
		catalogItem.ExternalID,
	}, "\x00")
}

func canonicalServiceSlug(sourceSlug string, serviceURL string) string {
	if canonicalSlug := canonicalSlugComponent(sourceSlug); canonicalSlug != "" {
		return canonicalSlug
	}
	parsedURL, parseError := url.Parse(strings.TrimSpace(serviceURL))
	if parseError != nil {
		return ""
	}
	pathSegments := strings.Split(strings.Trim(parsedURL.EscapedPath(), "/"), "/")
	for segmentIndex, pathSegment := range pathSegments {
		if strings.EqualFold(pathSegment, "servicos") && segmentIndex+1 < len(pathSegments) {
			return canonicalSlugComponent(pathSegments[segmentIndex+1])
		}
	}
	return ""
}

func canonicalSlugComponent(slug string) string {
	canonicalSlug := strings.ToLower(strings.TrimSpace(slug))
	if canonicalSlug == "" || strings.ContainsAny(canonicalSlug, "/\\\x00") {
		return ""
	}
	return canonicalSlug
}

func (service *SearchService) buildResponse(
	searchResults []*repository.SearchResult,
	totalCandidates int,
	searchRequest *models.SearchRequest,
	execution searchExecution,
	catalogRevision string,
	searchFacets models.SearchFacets,
) *models.SearchResponse {
	return &models.SearchResponse{
		RankerVersion:     service.RankerVersion(),
		RankerDescriptor:  service.RankerDescriptor(),
		CatalogRevision:   catalogRevision,
		EffectivePipeline: execution.pipeline,
		Degraded:          execution.degraded,
		Total:             totalCandidates,
		Page:              searchRequest.Page,
		PerPage:           searchRequest.PerPage,
		Facets:            searchFacets,
		Items:             buildSearchItems(searchResults),
	}
}

func buildSearchItems(searchResults []*repository.SearchResult) []*models.SearchItem {
	searchItems := make([]*models.SearchItem, 0, len(searchResults))
	for _, searchResult := range searchResults {
		searchItem := &models.SearchItem{
			ID:             searchResult.Item.ID.String(),
			CanonicalID:    canonicalSearchEntityID(searchResult.Item),
			Type:           searchResult.Item.Type,
			Source:         searchResult.Item.Source,
			SourceID:       searchResult.Item.ExternalID,
			Slug:           extractSlug(searchResult.Item.SourceData),
			Title:          searchResult.Item.Title,
			ShortDesc:      searchResult.Item.ShortDesc,
			Organization:   searchResult.Item.Organization,
			URL:            searchResult.Item.URL,
			ImageURL:       searchResult.Item.ImageURL,
			Modalidade:     searchResult.Item.Modalidade,
			Bairros:        searchResult.Item.Bairros,
			Tags:           searchResult.Item.Tags,
			RelevanceScore: searchResult.Rank,
			Metadata:       sanitizedSearchMetadata(searchResult.Item.Type, searchResult.Item.SourceData),
		}
		if searchResult.Headline != "" {
			searchItem.Highlights = []string{searchResult.Headline}
		}
		searchItems = append(searchItems, searchItem)
	}
	return searchItems
}

type searchFacetAccumulator struct {
	label string
	count int
}

func buildCandidateSearchFacets(searchResults []*repository.SearchResult) models.SearchFacets {
	typeValues := make(map[string]searchFacetAccumulator)
	modalityValues := make(map[string]searchFacetAccumulator)
	neighborhoodValues := make(map[string]searchFacetAccumulator)
	organizationValues := make(map[string]searchFacetAccumulator)

	for _, searchResult := range searchResults {
		if searchResult == nil || searchResult.Item == nil {
			continue
		}
		catalogItem := searchResult.Item
		addSearchFacetValue(typeValues, string(catalogItem.Type), string(catalogItem.Type))
		addSearchModalityFacetValue(modalityValues, catalogItem.Modalidade)
		addSearchFacetValue(organizationValues, catalogItem.Organization, catalogItem.Organization)

		seenNeighborhoods := make(map[string]struct{}, len(catalogItem.Bairros))
		for _, neighborhood := range catalogItem.Bairros {
			canonicalNeighborhood := canonicalSearchFacetValue(neighborhood)
			if _, alreadyCounted := seenNeighborhoods[canonicalNeighborhood]; alreadyCounted {
				continue
			}
			seenNeighborhoods[canonicalNeighborhood] = struct{}{}
			addCanonicalSearchFacetValue(
				neighborhoodValues,
				canonicalNeighborhood,
				strings.TrimSpace(neighborhood),
			)
		}
	}

	return models.SearchFacets{
		Version:       models.SearchFacetVersion,
		Scope:         models.SearchFacetScopeRetrievalCandidates,
		Types:         sortedSearchFacetValues(typeValues),
		Modalidades:   sortedSearchFacetValues(modalityValues),
		Bairros:       sortedSearchFacetValues(neighborhoodValues),
		Organizations: sortedSearchFacetValues(organizationValues),
	}
}

func emptySearchFacets(scope models.SearchFacetScope) models.SearchFacets {
	return models.SearchFacets{
		Version:       models.SearchFacetVersion,
		Scope:         scope,
		Types:         []models.SearchFacetValue{},
		Modalidades:   []models.SearchFacetValue{},
		Bairros:       []models.SearchFacetValue{},
		Organizations: []models.SearchFacetValue{},
	}
}

func withNonNilSearchFacetCollections(searchFacets models.SearchFacets) models.SearchFacets {
	if searchFacets.Types == nil {
		searchFacets.Types = []models.SearchFacetValue{}
	}
	if searchFacets.Modalidades == nil {
		searchFacets.Modalidades = []models.SearchFacetValue{}
	}
	if searchFacets.Bairros == nil {
		searchFacets.Bairros = []models.SearchFacetValue{}
	}
	if searchFacets.Organizations == nil {
		searchFacets.Organizations = []models.SearchFacetValue{}
	}
	return searchFacets
}

func addSearchFacetValue(
	accumulators map[string]searchFacetAccumulator,
	rawValue string,
	label string,
) {
	addCanonicalSearchFacetValue(
		accumulators,
		canonicalSearchFacetValue(rawValue),
		label,
	)
}

func addSearchModalityFacetValue(
	accumulators map[string]searchFacetAccumulator,
	rawModality string,
) {
	canonicalModality := canonicalSearchModalityFacet(rawModality)
	addCanonicalSearchFacetValue(accumulators, canonicalModality, rawModality)
}

func canonicalSearchModalityFacet(rawModality string) string {
	switch canonicalSearchFacetValue(rawModality) {
	case "presencial", "presencialmente", "in loco", "local", "onsite", "on-site":
		return "presencial"
	case "digital", "online", "remoto", "remota", "ead", "virtual", "a distancia":
		return "digital"
	case "hibrido", "hybrid", "misto", "mista", "semipresencial":
		return "hibrido"
	default:
		return ""
	}
}

func addCanonicalSearchFacetValue(
	accumulators map[string]searchFacetAccumulator,
	canonicalValue string,
	label string,
) {
	canonicalLabel := canonicalSearchFacetLabel(label)
	if canonicalValue == "" || canonicalLabel == "" ||
		utf8.RuneCountInString(canonicalValue) > models.MaxSearchFilterRunes {
		return
	}
	canonicalLabel = truncateSearchFacetLabel(canonicalLabel)
	accumulator := accumulators[canonicalValue]
	accumulator.count++
	if accumulator.label == "" || canonicalLabel < accumulator.label {
		accumulator.label = canonicalLabel
	}
	accumulators[canonicalValue] = accumulator
}

func canonicalSearchFacetValue(rawValue string) string {
	normalizedValue := norm.NFD.String(strings.ToLower(strings.Join(strings.Fields(rawValue), " ")))
	var canonicalValue strings.Builder
	canonicalValue.Grow(len(normalizedValue))
	for _, character := range normalizedValue {
		if !unicode.Is(unicode.Mn, character) {
			canonicalValue.WriteRune(character)
		}
	}
	return canonicalValue.String()
}

func canonicalSearchFacetLabel(rawLabel string) string {
	return strings.Join(strings.Fields(rawLabel), " ")
}

func truncateSearchFacetLabel(label string) string {
	if utf8.RuneCountInString(label) <= models.MaxSearchFacetLabelRunes {
		return label
	}
	return string([]rune(label)[:models.MaxSearchFacetLabelRunes])
}

func sortedSearchFacetValues(
	accumulators map[string]searchFacetAccumulator,
) []models.SearchFacetValue {
	facetValues := make([]models.SearchFacetValue, 0, len(accumulators))
	for value, accumulator := range accumulators {
		facetValues = append(facetValues, models.SearchFacetValue{
			Value: value,
			Label: accumulator.label,
			Count: accumulator.count,
		})
	}
	sort.Slice(facetValues, func(firstIndex int, secondIndex int) bool {
		if facetValues[firstIndex].Count != facetValues[secondIndex].Count {
			return facetValues[firstIndex].Count > facetValues[secondIndex].Count
		}
		return facetValues[firstIndex].Value < facetValues[secondIndex].Value
	})
	if len(facetValues) > models.MaxSearchFacetValues {
		facetValues = facetValues[:models.MaxSearchFacetValues]
	}
	return facetValues
}

func extractSlug(sourceData json.RawMessage) string {
	if len(sourceData) == 0 {
		return ""
	}
	var sourceFields struct {
		Slug string `json:"slug"`
	}
	if unmarshalError := json.Unmarshal(sourceData, &sourceFields); unmarshalError != nil {
		return ""
	}
	return sourceFields.Slug
}

func sanitizedSearchMetadata(itemType models.ItemType, sourceData json.RawMessage) json.RawMessage {
	if len(sourceData) == 0 {
		return nil
	}
	var sourceFields map[string]json.RawMessage
	if unmarshalError := json.Unmarshal(sourceData, &sourceFields); unmarshalError != nil {
		return nil
	}

	allowedFields := []string{"id", "slug"}
	if itemType == models.TypeService {
		allowedFields = append(allowedFields, "tema_geral", "tema_especifico")
	}
	sanitizedFields := make(map[string]json.RawMessage, len(allowedFields))
	for _, allowedField := range allowedFields {
		if fieldValue, exists := sourceFields[allowedField]; exists && len(fieldValue) > 0 && string(fieldValue) != "null" {
			sanitizedFields[allowedField] = fieldValue
		}
	}
	if len(sanitizedFields) == 0 {
		return nil
	}
	sanitizedJSON, marshalError := json.Marshal(sanitizedFields)
	if marshalError != nil {
		return nil
	}
	return sanitizedJSON
}

func (service *SearchService) getCachedResponse(
	searchContext context.Context,
	cacheKey string,
	expectedCatalogRevision string,
) *models.SearchResponse {
	if service.searchCache == nil {
		return nil
	}
	var cachedResponse models.SearchResponse
	cacheError := service.searchCache.Get(searchContext, cacheKey, &cachedResponse)
	if cacheError == nil {
		if cachedResponse.Degraded || !cachedResponse.EffectivePipeline.Valid() ||
			cachedResponse.RankerVersion != service.RankerVersion() ||
			cachedResponse.RankerDescriptor.SchemaVersion == "" ||
			cachedResponse.Facets.Version != models.SearchFacetVersion ||
			!cachedResponse.Facets.Scope.Valid() ||
			cachedResponse.Facets.Types == nil ||
			cachedResponse.Facets.Modalidades == nil ||
			cachedResponse.Facets.Bairros == nil ||
			cachedResponse.Facets.Organizations == nil ||
			cachedResponse.CatalogRevision != expectedCatalogRevision {
			observability.SearchCacheOperationsTotal.WithLabelValues("get", "incompatible").Inc()
			return nil
		}
		observability.SearchCacheOperationsTotal.WithLabelValues("get", "hit").Inc()
		return &cachedResponse
	}
	if cache.IsMiss(cacheError) {
		observability.SearchCacheOperationsTotal.WithLabelValues("get", "miss").Inc()
	} else {
		observability.SearchCacheOperationsTotal.WithLabelValues("get", "error").Inc()
		log.Warn().Err(cacheError).Msg("search: cache read failed")
	}
	return nil
}

func (service *SearchService) getCachedRankedSnapshot(
	searchContext context.Context,
	cacheKey string,
	expectedCatalogRevision string,
	expectedRankerVersion string,
) *rankedSearchSnapshot {
	if service.searchCache == nil {
		return nil
	}
	var cachedSnapshot rankedSearchSnapshot
	cacheError := service.searchCache.Get(searchContext, cacheKey, &cachedSnapshot)
	if cacheError == nil {
		serializedSnapshot, serializationError := json.Marshal(&cachedSnapshot)
		if serializationError != nil || len(serializedSnapshot) > maximumRankedSnapshotBytes ||
			cachedSnapshot.Degraded || !cachedSnapshot.EffectivePipeline.Valid() ||
			cachedSnapshot.RankerVersion != expectedRankerVersion ||
			cachedSnapshot.RankerDescriptor.SchemaVersion == "" ||
			cachedSnapshot.Facets.Version != models.SearchFacetVersion ||
			!cachedSnapshot.Facets.Scope.Valid() ||
			cachedSnapshot.Facets.Types == nil ||
			cachedSnapshot.Facets.Modalidades == nil ||
			cachedSnapshot.Facets.Bairros == nil ||
			cachedSnapshot.Facets.Organizations == nil ||
			!validRankedSnapshotItems(cachedSnapshot.Items, cachedSnapshot.Total) ||
			cachedSnapshot.CatalogRevision != expectedCatalogRevision {
			observability.SearchCacheOperationsTotal.WithLabelValues("get", "incompatible").Inc()
			return nil
		}
		observability.SearchCacheOperationsTotal.WithLabelValues("get", "hit").Inc()
		return &cachedSnapshot
	}
	if cache.IsMiss(cacheError) {
		observability.SearchCacheOperationsTotal.WithLabelValues("get", "miss").Inc()
	} else {
		observability.SearchCacheOperationsTotal.WithLabelValues("get", "error").Inc()
		log.Warn().Err(cacheError).Msg("search: ranked snapshot cache read failed")
	}
	return nil
}

func validRankedSnapshotItems(searchItems []*models.SearchItem, total int) bool {
	if searchItems == nil || total != len(searchItems) {
		return false
	}
	for _, searchItem := range searchItems {
		if searchItem == nil {
			return false
		}
	}
	return true
}

func (service *SearchService) setCachedResponse(
	searchContext context.Context,
	cacheKey string,
	searchResponse *models.SearchResponse,
	cacheTTL time.Duration,
) {
	if service.searchCache == nil {
		return
	}
	if cacheTTL <= 0 {
		observability.SearchCacheOperationsTotal.WithLabelValues("set", "skipped_temporal_boundary").Inc()
		return
	}
	if cacheError := service.searchCache.Set(searchContext, cacheKey, searchResponse, cacheTTL); cacheError != nil {
		observability.SearchCacheOperationsTotal.WithLabelValues("set", "error").Inc()
		log.Warn().Err(cacheError).Msg("search: cache write failed")
		return
	}
	observability.SearchCacheOperationsTotal.WithLabelValues("set", "success").Inc()
}

func (service *SearchService) setCachedRankedSnapshot(
	searchContext context.Context,
	cacheKey string,
	rankedSnapshot *rankedSearchSnapshot,
	cacheTTL time.Duration,
) {
	if service.searchCache == nil {
		return
	}
	if cacheTTL <= 0 {
		observability.SearchCacheOperationsTotal.WithLabelValues("set", "skipped_temporal_boundary").Inc()
		return
	}
	if cacheError := service.searchCache.Set(searchContext, cacheKey, rankedSnapshot, cacheTTL); cacheError != nil {
		observability.SearchCacheOperationsTotal.WithLabelValues("set", "error").Inc()
		log.Warn().Err(cacheError).Msg("search: ranked snapshot cache write failed")
		return
	}
	observability.SearchCacheOperationsTotal.WithLabelValues("set", "success").Inc()
}

func (service *SearchService) cacheKey(
	searchRequest *models.SearchRequest,
	catalogRevision string,
) (string, error) {
	cacheDescriptor := struct {
		RankerVersion    string                        `json:"ranker_version"`
		RankerDescriptor models.SearchRankerDescriptor `json:"ranker_descriptor"`
		CatalogRevision  string                        `json:"catalog_revision"`
		FacetVersion     string                        `json:"facet_version"`
		Request          *models.SearchRequest         `json:"request"`
		ExpandedQuery    string                        `json:"expanded_query"`
	}{
		RankerVersion:    service.RankerVersion(),
		RankerDescriptor: service.RankerDescriptor(),
		CatalogRevision:  catalogRevision,
		FacetVersion:     models.SearchFacetVersion,
		Request:          searchRequest,
		ExpandedQuery:    searchRequest.ExpandedQ,
	}
	serializedDescriptor, marshalError := json.Marshal(cacheDescriptor)
	if marshalError != nil {
		return "", fmt.Errorf("search cache key: %w", marshalError)
	}
	cacheDigest := sha256.Sum256(serializedDescriptor)
	return fmt.Sprintf("%s%x", cache.SearchKeyPrefix, cacheDigest), nil
}

func (service *SearchService) rankedCacheKey(
	searchRequest *models.SearchRequest,
	catalogRevision string,
) (string, error) {
	rankingRequest := cloneSearchRequest(searchRequest)
	rankingRequest.Page = 0
	rankingRequest.PerPage = 0
	return service.cacheKey(rankingRequest, catalogRevision)
}

func (service *SearchService) catalogSnapshot(
	searchContext context.Context,
) (repository.CatalogSnapshotVersion, error) {
	if snapshotProvider, providesSnapshot := service.searchRepository.(catalogSnapshotProvider); providesSnapshot {
		catalogSnapshot, snapshotError := snapshotProvider.CatalogSnapshot(searchContext)
		if snapshotError != nil {
			return repository.CatalogSnapshotVersion{}, fmt.Errorf("search catalog snapshot: %w", snapshotError)
		}
		if strings.TrimSpace(catalogSnapshot.Revision) == "" {
			return repository.CatalogSnapshotVersion{}, fmt.Errorf("search catalog snapshot: provider returned an empty revision")
		}
		return catalogSnapshot, nil
	}
	if revisionProvider, providesRevision := service.searchRepository.(catalogRevisionProvider); providesRevision {
		catalogRevision, revisionError := revisionProvider.CatalogRevision(searchContext)
		if revisionError != nil {
			return repository.CatalogSnapshotVersion{}, fmt.Errorf("search catalog revision: %w", revisionError)
		}
		if strings.TrimSpace(catalogRevision) == "" {
			return repository.CatalogSnapshotVersion{}, fmt.Errorf("search catalog revision: provider returned an empty revision")
		}
		return repository.CatalogSnapshotVersion{Revision: catalogRevision}, nil
	}
	return repository.CatalogSnapshotVersion{
		Revision: service.runtimeConfig.normalized().CatalogRevision,
	}, nil
}

func catalogSnapshotVersionFromBrowse(
	browseSnapshot *repository.BrowseSnapshot,
	fallbackVersion repository.CatalogSnapshotVersion,
) (repository.CatalogSnapshotVersion, error) {
	if validCatalogSnapshotVersion(browseSnapshot.SnapshotVersion) {
		if browseSnapshot.SnapshotVersion.Revision != browseSnapshot.CatalogRevision {
			return repository.CatalogSnapshotVersion{}, fmt.Errorf(
				"browse snapshot provider returned conflicting revisions %q and %q",
				browseSnapshot.SnapshotVersion.Revision,
				browseSnapshot.CatalogRevision,
			)
		}
		return browseSnapshot.SnapshotVersion, nil
	}
	if fallbackVersion.Revision != browseSnapshot.CatalogRevision {
		return repository.CatalogSnapshotVersion{Revision: browseSnapshot.CatalogRevision}, nil
	}
	return fallbackVersion, nil
}
