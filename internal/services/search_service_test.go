package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/prefeitura-rio/app-catalogo/internal/cache"
	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

type searchRepositoryStub struct {
	searchFunction          func(context.Context, *models.SearchRequest) ([]*repository.SearchResult, int, error)
	rankedSearchFunction    func(context.Context, *models.SearchRequest, repository.RankedSearchOptions) ([]*repository.SearchResult, int, error)
	catalogRevisionFunction func(context.Context) (string, error)
	catalogSnapshotFunction func(context.Context) (repository.CatalogSnapshotVersion, error)
}

type searchRepositoryWithFacetsStub struct {
	*searchRepositoryStub
	searchFacetsFunction func(context.Context, *models.SearchRequest) (models.SearchFacets, error)
}

type searchRepositoryWithBrowseSnapshotStub struct {
	*searchRepositoryStub
	browseSnapshotFunction func(context.Context, *models.SearchRequest) (*repository.BrowseSnapshot, error)
	searchFacetsFunction   func(context.Context, *models.SearchRequest) (models.SearchFacets, error)
}

type searchRepositoryWithRankedSnapshotStub struct {
	*searchRepositoryStub
	rankedSnapshotFunction func(
		context.Context,
		*models.SearchRequest,
		repository.RankedSearchOptions,
	) (*repository.RankedSearchSnapshot, error)
}

func (repositoryStub *searchRepositoryWithRankedSnapshotStub) SearchRankedSnapshot(
	searchContext context.Context,
	searchRequest *models.SearchRequest,
	searchOptions repository.RankedSearchOptions,
) (*repository.RankedSearchSnapshot, error) {
	return repositoryStub.rankedSnapshotFunction(searchContext, searchRequest, searchOptions)
}

func (repositoryStub *searchRepositoryWithBrowseSnapshotStub) BrowseSnapshot(
	searchContext context.Context,
	searchRequest *models.SearchRequest,
) (*repository.BrowseSnapshot, error) {
	return repositoryStub.browseSnapshotFunction(searchContext, searchRequest)
}

func (repositoryStub *searchRepositoryWithBrowseSnapshotStub) SearchFacets(
	searchContext context.Context,
	searchRequest *models.SearchRequest,
) (models.SearchFacets, error) {
	return repositoryStub.searchFacetsFunction(searchContext, searchRequest)
}

func (repositoryStub *searchRepositoryWithFacetsStub) SearchFacets(
	searchContext context.Context,
	searchRequest *models.SearchRequest,
) (models.SearchFacets, error) {
	return repositoryStub.searchFacetsFunction(searchContext, searchRequest)
}

func (repositoryStub *searchRepositoryStub) CatalogRevision(searchContext context.Context) (string, error) {
	if repositoryStub.catalogRevisionFunction == nil {
		return unversionedComponent, nil
	}
	return repositoryStub.catalogRevisionFunction(searchContext)
}

func (repositoryStub *searchRepositoryStub) CatalogSnapshot(
	searchContext context.Context,
) (repository.CatalogSnapshotVersion, error) {
	if repositoryStub.catalogSnapshotFunction != nil {
		return repositoryStub.catalogSnapshotFunction(searchContext)
	}
	catalogRevision, revisionError := repositoryStub.CatalogRevision(searchContext)
	return repository.CatalogSnapshotVersion{Revision: catalogRevision}, revisionError
}

func (repositoryStub *searchRepositoryStub) Search(
	searchContext context.Context,
	searchRequest *models.SearchRequest,
) ([]*repository.SearchResult, int, error) {
	return repositoryStub.searchFunction(searchContext, searchRequest)
}

func (repositoryStub *searchRepositoryStub) SearchRanked(
	searchContext context.Context,
	searchRequest *models.SearchRequest,
	searchOptions repository.RankedSearchOptions,
) ([]*repository.SearchResult, int, error) {
	return repositoryStub.rankedSearchFunction(searchContext, searchRequest, searchOptions)
}

type semanticSearchClientStub struct {
	embedding      []float32
	embeddingError error
	metadata       clients.EmbeddingMetadata
	hydeMetadata   models.HyDEGenerationMetadata
	onEmbed        func()
}

func (clientStub *semanticSearchClientStub) HyDEMetadata() models.HyDEGenerationMetadata {
	return clientStub.hydeMetadata
}

type searchCacheRecorder struct {
	mutex                sync.Mutex
	setCount             int
	setTTL               time.Duration
	serveStored          bool
	storedResponse       *models.SearchResponse
	storedRankedSnapshot *rankedSearchSnapshot
	getKeys              []string
	setKeys              []string
	getNotification      chan struct{}
}

func (cacheRecorder *searchCacheRecorder) Get(_ context.Context, cacheKey string, destination any) error {
	cacheRecorder.mutex.Lock()
	defer cacheRecorder.mutex.Unlock()
	cacheRecorder.getKeys = append(cacheRecorder.getKeys, cacheKey)
	if cacheRecorder.getNotification != nil {
		cacheRecorder.getNotification <- struct{}{}
	}
	if !cacheRecorder.serveStored {
		return cache.ErrCacheMiss
	}
	switch typedDestination := destination.(type) {
	case *models.SearchResponse:
		if cacheRecorder.storedResponse == nil {
			return cache.ErrCacheMiss
		}
		*typedDestination = *cacheRecorder.storedResponse
	case *rankedSearchSnapshot:
		if cacheRecorder.storedRankedSnapshot == nil {
			return cache.ErrCacheMiss
		}
		*typedDestination = *cacheRecorder.storedRankedSnapshot
	default:
		return errors.New("unexpected search cache destination")
	}
	return nil
}

func (cacheRecorder *searchCacheRecorder) Set(_ context.Context, cacheKey string, cachedValue any, ttl time.Duration) error {
	cacheRecorder.mutex.Lock()
	defer cacheRecorder.mutex.Unlock()
	cacheRecorder.setCount++
	cacheRecorder.setTTL = ttl
	cacheRecorder.setKeys = append(cacheRecorder.setKeys, cacheKey)
	if cacheRecorder.serveStored {
		switch typedValue := cachedValue.(type) {
		case *models.SearchResponse:
			responseCopy := *typedValue
			cacheRecorder.storedResponse = &responseCopy
		case *rankedSearchSnapshot:
			snapshotCopy := *typedValue
			cacheRecorder.storedRankedSnapshot = &snapshotCopy
		default:
			return errors.New("unexpected search cache value")
		}
	}
	return nil
}

type searchRerankerStub struct {
	rerankFunction func(context.Context, string, []clients.RerankerDocument) ([]clients.RerankerResult, error)
}

func (rerankerStub searchRerankerStub) Rerank(
	searchContext context.Context,
	searchQuery string,
	documents []clients.RerankerDocument,
) ([]clients.RerankerResult, error) {
	return rerankerStub.rerankFunction(searchContext, searchQuery, documents)
}

func (clientStub *semanticSearchClientStub) EmbedQuery(context.Context, string) ([]float32, error) {
	if clientStub.onEmbed != nil {
		clientStub.onEmbed()
	}
	return clientStub.embedding, clientStub.embeddingError
}

func (clientStub *semanticSearchClientStub) GenerateHyDE(context.Context, string) (string, error) {
	return "", errors.New("HyDE must not be called when disabled")
}

func (clientStub *semanticSearchClientStub) Metadata() clients.EmbeddingMetadata {
	return clientStub.metadata
}

func TestSearchSeparatesExpansionAndPaginatesAfterFusion(t *testing.T) {
	t.Parallel()

	searchResults := []*repository.SearchResult{
		newSearchResult("00000000-0000-4000-8000-000000000001", "First"),
		newSearchResult("00000000-0000-4000-8000-000000000002", "Second"),
		newSearchResult("00000000-0000-4000-8000-000000000003", "Third"),
	}
	var capturedRequest models.SearchRequest
	repositoryStub := &searchRepositoryStub{
		rankedSearchFunction: func(
			_ context.Context,
			searchRequest *models.SearchRequest,
			_ repository.RankedSearchOptions,
		) ([]*repository.SearchResult, int, error) {
			capturedRequest = *searchRequest
			return searchResults, len(searchResults), nil
		},
	}
	searchService := NewSearchService(repositoryStub, nil, 0, nil, nil, DefaultSearchRuntimeConfig())

	searchResponse, searchError := searchService.Search(context.Background(), &models.SearchRequest{
		Q:       "  sus  ",
		Page:    2,
		PerPage: 1,
	})
	if searchError != nil {
		t.Fatalf("Search returned an unexpected error: %v", searchError)
	}
	if capturedRequest.Q != "sus" {
		t.Fatalf("raw query = %q, want canonical raw query", capturedRequest.Q)
	}
	if !strings.HasPrefix(capturedRequest.ExpandedQ, "sus OR ") {
		t.Fatalf("expanded query = %q, want an OR-safe expansion", capturedRequest.ExpandedQ)
	}
	if len(searchResponse.Items) != 1 || searchResponse.Items[0].Title != "Second" {
		t.Fatalf("page items = %#v, want the globally ranked second candidate", searchResponse.Items)
	}
	if searchResponse.Total != len(searchResults) {
		t.Fatalf("total = %d, want %d", searchResponse.Total, len(searchResults))
	}
	if searchResponse.RankerVersion != searchService.RankerVersion() || !strings.HasPrefix(searchResponse.RankerVersion, defaultRankerVersion+"-") {
		t.Fatalf("ranker version = %q, want fingerprinted %q", searchResponse.RankerVersion, searchService.RankerVersion())
	}
	if searchResponse.EffectivePipeline != models.SearchPipelineLexical || searchResponse.Degraded {
		t.Fatalf("provenance = pipeline %q degraded=%t, want nominal lexical", searchResponse.EffectivePipeline, searchResponse.Degraded)
	}
	if searchResponse.CatalogRevision != unversionedComponent || searchResponse.RankerDescriptor.RetrievalVersion != repository.RetrievalVersion {
		t.Fatalf("response provenance = %+v", searchResponse)
	}
	if searchResponse.Sources.Facilita != notApplicableFacilitaDiagnostic() ||
		searchResponse.RankerDescriptor.Facilita != nil {
		t.Fatalf("retired Facilita compatibility marker = %+v", searchResponse)
	}
}

func TestRankerVersionFingerprintsConfiguredComponents(t *testing.T) {
	t.Parallel()

	baseService := NewSearchService(nil, nil, 0, nil, nil, DefaultSearchRuntimeConfig())
	semanticService := NewSearchService(
		nil,
		nil,
		0,
		&semanticSearchClientStub{metadata: clients.EmbeddingMetadata{Model: "model", Version: "v1"}},
		nil,
		DefaultSearchRuntimeConfig(),
	)
	weightedConfig := DefaultSearchRuntimeConfig()
	weightedConfig.Weights.Semantic = 2
	weightedService := NewSearchService(nil, nil, 0, nil, nil, weightedConfig)

	if baseService.RankerVersion() == semanticService.RankerVersion() ||
		baseService.RankerVersion() == weightedService.RankerVersion() ||
		semanticService.RankerVersion() == weightedService.RankerVersion() {
		t.Fatalf(
			"ranker versions do not identify component/config changes: base=%q semantic=%q weighted=%q",
			baseService.RankerVersion(),
			semanticService.RankerVersion(),
			weightedService.RankerVersion(),
		)
	}
}

func TestNewSearchServiceNormalizesTypedNilOptionalDependencies(t *testing.T) {
	t.Parallel()

	var cacheRecorder *searchCacheRecorder
	var semanticClient *semanticSearchClientStub
	var rerankerClient *searchRerankerStub
	searchService := NewSearchService(
		nil,
		cacheRecorder,
		0,
		semanticClient,
		rerankerClient,
		DefaultSearchRuntimeConfig(),
	)

	if searchService.searchCache != nil || searchService.semanticClient != nil || searchService.rerankerClient != nil {
		t.Fatalf(
			"typed nil dependencies were retained: cache=%v semantic=%v reranker=%v",
			searchService.searchCache,
			searchService.semanticClient,
			searchService.rerankerClient,
		)
	}
	rankerDescriptor := searchService.RankerDescriptor()
	if rankerDescriptor.SemanticEnabled || rankerDescriptor.HyDEEnabled || rankerDescriptor.RerankerEnabled || rankerDescriptor.Embedding != nil {
		t.Fatalf("typed nil dependencies enabled ranking components: %+v", rankerDescriptor)
	}
}

func TestRankedSnapshotCacheServesMultiplePagesWithoutReranking(t *testing.T) {
	t.Parallel()

	searchResults := []*repository.SearchResult{
		newSearchResult("00000000-0000-4000-8000-000000000001", "First"),
		newSearchResult("00000000-0000-4000-8000-000000000002", "Second"),
		newSearchResult("00000000-0000-4000-8000-000000000003", "Third"),
	}
	repositoryCalls := 0
	rerankerCalls := 0
	embeddingCalls := 0
	repositoryStub := &searchRepositoryStub{
		rankedSearchFunction: func(
			context.Context,
			*models.SearchRequest,
			repository.RankedSearchOptions,
		) ([]*repository.SearchResult, int, error) {
			repositoryCalls++
			return searchResults, len(searchResults), nil
		},
	}
	reranker := searchRerankerStub{rerankFunction: func(
		_ context.Context,
		_ string,
		documents []clients.RerankerDocument,
	) ([]clients.RerankerResult, error) {
		rerankerCalls++
		rerankedDocuments := make([]clients.RerankerResult, len(documents))
		for documentIndex, document := range documents {
			rerankedDocuments[documentIndex] = clients.RerankerResult{
				ID:    document.ID,
				Score: float64(len(documents) - documentIndex),
			}
		}
		return rerankedDocuments, nil
	}}
	cacheRecorder := &searchCacheRecorder{serveStored: true}
	semanticClient := &semanticSearchClientStub{
		embedding: []float32{1},
		metadata: clients.EmbeddingMetadata{
			Model:            "gemini-embedding-test",
			Version:          "v1",
			Dimensions:       1,
			DocumentTaskType: "RETRIEVAL_DOCUMENT",
			DocumentVersion:  "catalog-item-v1",
		},
		onEmbed: func() { embeddingCalls++ },
	}
	searchService := NewSearchService(repositoryStub, cacheRecorder, time.Minute, semanticClient, reranker, DefaultSearchRuntimeConfig())

	firstPage, firstPageError := searchService.Search(context.Background(), &models.SearchRequest{Q: "iptu", Page: 1, PerPage: 1})
	if firstPageError != nil {
		t.Fatalf("first page: %v", firstPageError)
	}
	secondPage, secondPageError := searchService.Search(context.Background(), &models.SearchRequest{Q: "iptu", Page: 2, PerPage: 1})
	if secondPageError != nil {
		t.Fatalf("second page: %v", secondPageError)
	}
	if firstPage.Items[0].Title != "First" || secondPage.Items[0].Title != "Second" ||
		firstPage.Total != 3 || secondPage.Total != 3 {
		t.Fatalf("cached pages = first %#v second %#v", firstPage, secondPage)
	}
	if repositoryCalls != 1 || embeddingCalls != 1 || rerankerCalls != 1 || cacheRecorder.setCount != 1 {
		t.Fatalf(
			"repository=%d embedding=%d reranker=%d cache sets=%d",
			repositoryCalls,
			embeddingCalls,
			rerankerCalls,
			cacheRecorder.setCount,
		)
	}
	if len(cacheRecorder.getKeys) != 2 || cacheRecorder.getKeys[0] != cacheRecorder.getKeys[1] ||
		len(cacheRecorder.setKeys) != 1 || cacheRecorder.setKeys[0] != cacheRecorder.getKeys[0] {
		t.Fatalf("ranked cache keys = get %v set %v", cacheRecorder.getKeys, cacheRecorder.setKeys)
	}
}

func TestRankedSearchSingleflightCoalescesAndLetsFollowerCancel(t *testing.T) {
	t.Parallel()

	repositoryStarted := make(chan struct{})
	releaseRepository := make(chan struct{})
	cacheGets := make(chan struct{}, 2)
	var repositoryCalls atomic.Int32
	repositoryStub := &searchRepositoryStub{
		rankedSearchFunction: func(
			context.Context,
			*models.SearchRequest,
			repository.RankedSearchOptions,
		) ([]*repository.SearchResult, int, error) {
			if repositoryCalls.Add(1) == 1 {
				close(repositoryStarted)
			}
			<-releaseRepository
			return []*repository.SearchResult{
				newSearchResult("00000000-0000-4000-8000-000000000001", "Shared"),
			}, 1, nil
		},
	}
	cacheRecorder := &searchCacheRecorder{getNotification: cacheGets}
	searchService := NewSearchService(repositoryStub, cacheRecorder, time.Minute, nil, nil, DefaultSearchRuntimeConfig())
	leaderResponse := make(chan *models.SearchResponse, 1)
	leaderError := make(chan error, 1)
	go func() {
		searchResponse, searchError := searchService.Search(
			context.Background(),
			&models.SearchRequest{Q: "iptu", Page: 1, PerPage: 1},
		)
		leaderResponse <- searchResponse
		leaderError <- searchError
	}()
	<-cacheGets
	<-repositoryStarted

	followerContext, cancelFollower := context.WithCancel(context.Background())
	followerError := make(chan error, 1)
	go func() {
		_, searchError := searchService.Search(
			followerContext,
			&models.SearchRequest{Q: "iptu", Page: 2, PerPage: 1},
		)
		followerError <- searchError
	}()
	<-cacheGets
	cancelFollower()
	if searchError := <-followerError; !errors.Is(searchError, context.Canceled) {
		t.Fatalf("follower cancellation error = %v", searchError)
	}
	close(releaseRepository)
	if searchError := <-leaderError; searchError != nil {
		t.Fatalf("leader error = %v", searchError)
	}
	if searchResponse := <-leaderResponse; searchResponse == nil || len(searchResponse.Items) != 1 {
		t.Fatalf("leader response = %#v", searchResponse)
	}
	if repositoryCalls.Load() != 1 {
		t.Fatalf("coalesced repository calls = %d, want 1", repositoryCalls.Load())
	}
}

func TestOversizeRankedSnapshotSkipsCacheButServesBoundedPage(t *testing.T) {
	t.Parallel()

	searchResults := make([]*repository.SearchResult, repository.MaximumCandidatePoolSize)
	for resultIndex := range searchResults {
		searchResults[resultIndex] = newSearchResult(uuid.NewString(), strings.Repeat("x", 3000))
	}
	repositoryStub := &searchRepositoryStub{
		rankedSearchFunction: func(
			context.Context,
			*models.SearchRequest,
			repository.RankedSearchOptions,
		) ([]*repository.SearchResult, int, error) {
			return searchResults, len(searchResults), nil
		},
	}
	cacheRecorder := &searchCacheRecorder{}
	runtimeConfig := DefaultSearchRuntimeConfig()
	runtimeConfig.CandidatePoolSize = repository.MaximumCandidatePoolSize
	searchService := NewSearchService(repositoryStub, cacheRecorder, time.Minute, nil, nil, runtimeConfig)

	searchResponse, searchError := searchService.Search(
		context.Background(),
		&models.SearchRequest{Q: "iptu", Page: 1, PerPage: 1},
	)
	if searchError != nil {
		t.Fatalf("bounded page from oversize snapshot: %v", searchError)
	}
	if searchResponse.Total != repository.MaximumCandidatePoolSize || len(searchResponse.Items) != 1 {
		t.Fatalf("bounded response = %#v", searchResponse)
	}
	if cacheRecorder.setCount != 0 {
		t.Fatalf("oversize ranked snapshot was cached %d time(s)", cacheRecorder.setCount)
	}
}

func TestOversizeRankedResponsePageFailsExplicitly(t *testing.T) {
	t.Parallel()

	oversizeResult := newSearchResult(
		"00000000-0000-4000-8000-000000000001",
		strings.Repeat("x", maximumRankedSnapshotBytes),
	)
	repositoryStub := &searchRepositoryStub{
		rankedSearchFunction: func(
			context.Context,
			*models.SearchRequest,
			repository.RankedSearchOptions,
		) ([]*repository.SearchResult, int, error) {
			return []*repository.SearchResult{oversizeResult}, 1, nil
		},
	}
	searchService := NewSearchService(repositoryStub, &searchCacheRecorder{}, time.Minute, nil, nil, DefaultSearchRuntimeConfig())

	searchResponse, searchError := searchService.Search(
		context.Background(),
		&models.SearchRequest{Q: "iptu", Page: 1, PerPage: 1},
	)
	if !errors.Is(searchError, ErrSearchResponseTooLarge) {
		t.Fatalf("oversize page error = %v", searchError)
	}
	if searchResponse != nil {
		t.Fatalf("oversize page response = %#v, want nil", searchResponse)
	}
}

func TestRankedSnapshotCacheValidationRejectsNilItems(t *testing.T) {
	t.Parallel()

	if validRankedSnapshotItems([]*models.SearchItem{nil}, 1) {
		t.Fatal("ranked snapshot validation accepted a nil public item")
	}
	if !validRankedSnapshotItems([]*models.SearchItem{}, 0) {
		t.Fatal("ranked snapshot validation rejected a non-nil empty item list")
	}
}

func TestRankerDescriptorFingerprintsHyDEGenerationContract(t *testing.T) {
	t.Parallel()

	runtimeConfig := DefaultSearchRuntimeConfig()
	runtimeConfig.HyDEEnabled = true
	hydeMetadata := models.HyDEGenerationMetadata{
		Model:             "gemini-test",
		PromptVersion:     "prompt-v2",
		PromptSHA256:      strings.Repeat("a", 64),
		Temperature:       0,
		Seed:              42,
		CandidateCount:    1,
		MaxOutputTokens:   150,
		ResponseMIMEType:  "text/plain",
		DeterminismPolicy: "best-effort-seed",
	}
	searchService := NewSearchService(
		nil,
		nil,
		0,
		&semanticSearchClientStub{
			metadata:     clients.EmbeddingMetadata{Model: "embedding-test", Version: "v1"},
			hydeMetadata: hydeMetadata,
		},
		nil,
		runtimeConfig,
	)
	descriptor := searchService.RankerDescriptor()
	if descriptor.HyDEModel != hydeMetadata.Model || descriptor.HyDEPromptVersion != hydeMetadata.PromptVersion ||
		descriptor.HyDEPromptSHA256 != hydeMetadata.PromptSHA256 || descriptor.HyDETemperature == nil ||
		*descriptor.HyDETemperature != 0 || descriptor.HyDESeed == nil || *descriptor.HyDESeed != 42 ||
		descriptor.HyDEDeterminismPolicy != hydeMetadata.DeterminismPolicy {
		t.Fatalf("HyDE descriptor = %#v", descriptor)
	}
}

func TestRankerDescriptorFingerprintCrossLanguageGolden(t *testing.T) {
	t.Parallel()

	temperature := float32(0)
	seed := int32(42)
	candidateCount := int32(1)
	maximumOutputTokens := int32(150)
	descriptor := models.SearchRankerDescriptor{
		SchemaVersion:           "search-ranker-v1",
		BaseVersion:             "hybrid-v3",
		RetrievalVersion:        "postgres-canonical-weighted-rrf-v3",
		QueryExpansionVersion:   "pt-br-synonyms-v1",
		DeduplicationVersion:    "canonical-entity-v2",
		CandidatePoolSize:       100,
		SemanticOverfetchFactor: 4,
		TrigramThreshold:        0.25,
		MaximumSemanticDistance: 1,
		ReciprocalRankK:         60,
		Weights: models.SearchRetrievalWeights{
			Exact:    4,
			FullText: 3,
			Trigram:  2,
			Semantic: 1,
			HyDE:     0.5,
		},
		SemanticEnabled: true,
		Embedding: &models.EmbeddingMetadata{
			Model:            "gemini-embedding-001",
			Version:          "001",
			Dimensions:       1536,
			DocumentTaskType: "RETRIEVAL_DOCUMENT",
			QueryTaskType:    "RETRIEVAL_QUERY",
			DocumentVersion:  "catalog-item-v1",
		},
		HyDEEnabled:           true,
		HyDEModel:             "gemini-3.1-flash-lite",
		HyDEPromptVersion:     "rio-public-service-hyde-v2",
		HyDEPromptSHA256:      strings.Repeat("a", 64),
		HyDETemperature:       &temperature,
		HyDESeed:              &seed,
		HyDECandidateCount:    &candidateCount,
		HyDEMaxOutputTokens:   &maximumOutputTokens,
		HyDEResponseMIMEType:  "text/plain",
		HyDEDeterminismPolicy: "best-effort-seed",
		RerankerEnabled:       false,
	}

	serializedDescriptor, marshalError := json.Marshal(descriptor)
	if marshalError != nil {
		t.Fatalf("marshal descriptor: %v", marshalError)
	}
	expectedJSON := `{"schema_version":"search-ranker-v1","base_version":"hybrid-v3","retrieval_version":"postgres-canonical-weighted-rrf-v3","query_expansion_version":"pt-br-synonyms-v1","deduplication_version":"canonical-entity-v2","candidate_pool_size":100,"semantic_overfetch_factor":4,"trigram_threshold":0.25,"maximum_semantic_distance":1,"reciprocal_rank_k":60,"weights":{"exact":4,"full_text":3,"trigram":2,"semantic":1,"hyde":0.5,"facilita":0},"semantic_enabled":true,"embedding":{"model":"gemini-embedding-001","version":"001","dimensions":1536,"document_task_type":"RETRIEVAL_DOCUMENT","query_task_type":"RETRIEVAL_QUERY","document_version":"catalog-item-v1"},"hyde_enabled":true,"hyde_model":"gemini-3.1-flash-lite","hyde_prompt_version":"rio-public-service-hyde-v2","hyde_prompt_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","hyde_temperature":0,"hyde_seed":42,"hyde_candidate_count":1,"hyde_max_output_tokens":150,"hyde_response_mime_type":"text/plain","hyde_determinism_policy":"best-effort-seed","reranker_enabled":false}`
	if string(serializedDescriptor) != expectedJSON {
		t.Fatalf("descriptor JSON = %s, want %s", serializedDescriptor, expectedJSON)
	}
	if rankerVersion := buildRankerVersion(descriptor); rankerVersion != "hybrid-v3-d462586224a2" {
		t.Fatalf("ranker version = %q", rankerVersion)
	}
}

func TestSearchRejectsNilRequest(t *testing.T) {
	t.Parallel()

	searchService := NewSearchService(nil, nil, 0, nil, nil, DefaultSearchRuntimeConfig())
	searchResponse, searchError := searchService.Search(context.Background(), nil)
	if searchError == nil || !strings.Contains(searchError.Error(), "must not be nil") {
		t.Fatalf("error = %v, want explicit nil request validation", searchError)
	}
	if searchResponse != nil {
		t.Fatalf("response = %#v, want nil", searchResponse)
	}
}

func TestPaginateSearchItemsRejectsAnOverflowingPage(t *testing.T) {
	t.Parallel()

	searchItems := []*models.SearchItem{
		{ID: "00000000-0000-4000-8000-000000000001", Title: "First"},
	}
	pageItems := paginateSearchItems(searchItems, math.MaxInt, models.MaxSearchPerPage)
	if len(pageItems) != 0 {
		t.Fatalf("extreme page = %#v, want overflow-safe empty slice", pageItems)
	}
}

func TestSearchFallsBackToLexicalRetrievalWhenEmbeddingFails(t *testing.T) {
	t.Parallel()

	var capturedOptions repository.RankedSearchOptions
	repositoryStub := &searchRepositoryStub{
		rankedSearchFunction: func(
			_ context.Context,
			_ *models.SearchRequest,
			searchOptions repository.RankedSearchOptions,
		) ([]*repository.SearchResult, int, error) {
			capturedOptions = searchOptions
			return []*repository.SearchResult{newSearchResult("00000000-0000-4000-8000-000000000001", "Lexical")}, 1, nil
		},
	}
	semanticClient := &semanticSearchClientStub{
		embeddingError: errors.New("temporary embedding outage"),
		metadata: clients.EmbeddingMetadata{
			Model:      "test-model",
			Version:    "test-version",
			Dimensions: 3,
		},
	}
	cacheRecorder := &searchCacheRecorder{}
	searchService := NewSearchService(repositoryStub, cacheRecorder, time.Minute, semanticClient, nil, DefaultSearchRuntimeConfig())

	searchResponse, searchError := searchService.Search(context.Background(), &models.SearchRequest{Q: "vacina"})
	if searchError != nil {
		t.Fatalf("Search returned an unexpected error: %v", searchError)
	}
	if capturedOptions.QueryEmbedding != "" || capturedOptions.EmbeddingModel != "" {
		t.Fatalf("lexical fallback leaked semantic options: %#v", capturedOptions)
	}
	if len(searchResponse.Items) != 1 || searchResponse.Items[0].Title != "Lexical" {
		t.Fatalf("items = %#v, want lexical fallback result", searchResponse.Items)
	}
	if !searchResponse.Degraded || searchResponse.EffectivePipeline != models.SearchPipelineLexical {
		t.Fatalf("fallback provenance = pipeline %q degraded=%t", searchResponse.EffectivePipeline, searchResponse.Degraded)
	}
	if cacheRecorder.setCount != 0 {
		t.Fatalf("degraded fallback was cached %d time(s)", cacheRecorder.setCount)
	}
}

func TestSearchDoesNotCacheAcrossCatalogRevisionChange(t *testing.T) {
	t.Parallel()

	revisionCallCount := 0
	repositoryStub := &searchRepositoryStub{
		searchFunction: func(context.Context, *models.SearchRequest) ([]*repository.SearchResult, int, error) {
			return []*repository.SearchResult{
				newSearchResult("00000000-0000-4000-8000-000000000001", "IPTU"),
			}, 1, nil
		},
		catalogRevisionFunction: func(context.Context) (string, error) {
			revisionCallCount++
			return fmt.Sprintf("catalog-v1:%d", revisionCallCount), nil
		},
	}
	cacheRecorder := &searchCacheRecorder{}
	searchService := NewSearchService(
		repositoryStub,
		cacheRecorder,
		time.Minute,
		nil,
		nil,
		DefaultSearchRuntimeConfig(),
	)

	searchResponse, searchError := searchService.Search(context.Background(), &models.SearchRequest{
		Page:    1,
		PerPage: 10,
	})
	if searchError != nil {
		t.Fatalf("Search returned an unexpected error: %v", searchError)
	}
	if !searchResponse.Degraded || searchResponse.CatalogRevision != "catalog-v1:1" {
		t.Fatalf("revision provenance = %+v, want degraded snapshot catalog-v1:1", searchResponse)
	}
	if cacheRecorder.setCount != 0 {
		t.Fatalf("cache writes = %d, want 0 for a moving catalog snapshot", cacheRecorder.setCount)
	}
}

func TestSearchDoesNotCacheRerankerFailureAsNominal(t *testing.T) {
	t.Parallel()

	searchResults := []*repository.SearchResult{
		newSearchResult("00000000-0000-4000-8000-000000000001", "First"),
		newSearchResult("00000000-0000-4000-8000-000000000002", "Second"),
	}
	repositoryStub := &searchRepositoryStub{
		rankedSearchFunction: func(
			context.Context,
			*models.SearchRequest,
			repository.RankedSearchOptions,
		) ([]*repository.SearchResult, int, error) {
			return searchResults, len(searchResults), nil
		},
	}
	cacheRecorder := &searchCacheRecorder{}
	reranker := searchRerankerStub{rerankFunction: func(
		context.Context,
		string,
		[]clients.RerankerDocument,
	) ([]clients.RerankerResult, error) {
		return nil, errors.New("temporary reranker outage")
	}}
	searchService := NewSearchService(repositoryStub, cacheRecorder, time.Minute, nil, reranker, DefaultSearchRuntimeConfig())

	searchResponse, searchError := searchService.Search(context.Background(), &models.SearchRequest{Q: "iptu"})
	if searchError != nil {
		t.Fatalf("Search returned an unexpected error: %v", searchError)
	}
	if !searchResponse.Degraded || searchResponse.EffectivePipeline != models.SearchPipelineLexical {
		t.Fatalf("reranker fallback provenance = pipeline %q degraded=%t", searchResponse.EffectivePipeline, searchResponse.Degraded)
	}
	if cacheRecorder.setCount != 0 {
		t.Fatalf("reranker fallback was cached %d time(s)", cacheRecorder.setCount)
	}
}

func TestReorderRerankerCandidatesRejectsPartialOrInvalidResponses(t *testing.T) {
	t.Parallel()

	searchResults := []*repository.SearchResult{
		newSearchResult("00000000-0000-4000-8000-000000000001", "First"),
		newSearchResult("00000000-0000-4000-8000-000000000002", "Second"),
	}
	testCases := []struct {
		name              string
		rerankerResults   []clients.RerankerResult
		expectedValid     bool
		expectedFirstItem string
	}{
		{
			name: "complete permutation sorted by score",
			rerankerResults: []clients.RerankerResult{
				{ID: searchResults[0].Item.ID.String(), Score: 0.1},
				{ID: searchResults[1].Item.ID.String(), Score: 0.9},
			},
			expectedValid:     true,
			expectedFirstItem: "Second",
		},
		{
			name: "partial response",
			rerankerResults: []clients.RerankerResult{
				{ID: searchResults[1].Item.ID.String(), Score: 0.9},
			},
		},
		{
			name: "duplicate id",
			rerankerResults: []clients.RerankerResult{
				{ID: searchResults[0].Item.ID.String(), Score: 0.9},
				{ID: searchResults[0].Item.ID.String(), Score: 0.8},
			},
		},
		{
			name: "unknown id",
			rerankerResults: []clients.RerankerResult{
				{ID: searchResults[0].Item.ID.String(), Score: 0.9},
				{ID: "00000000-0000-4000-8000-000000000099", Score: 0.8},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			reorderedResults, validResponse := reorderRerankerCandidates(searchResults, testCase.rerankerResults)
			if validResponse != testCase.expectedValid {
				t.Fatalf("valid = %t, want %t", validResponse, testCase.expectedValid)
			}
			if testCase.expectedValid && reorderedResults[0].Item.Title != testCase.expectedFirstItem {
				t.Fatalf("first result = %q, want %q", reorderedResults[0].Item.Title, testCase.expectedFirstItem)
			}
		})
	}
}

func TestBuildResponseExposesSourceIDAndSanitizesMetadata(t *testing.T) {
	t.Parallel()

	sourceData := json.RawMessage(`{
		"id":"source-42",
		"slug":"analista-de-dados-42",
		"private_contact":"should-never-leave-the-backend",
		"internal_salary":12345
	}`)
	searchResult := newSearchResult("00000000-0000-4000-8000-000000000001", "Analista de dados")
	searchResult.Item.ExternalID = "source-42"
	searchResult.Item.Type = models.TypeJob
	searchResult.Item.Source = models.SourceJobs
	searchResult.Item.SourceData = sourceData

	searchResponse := (&SearchService{}).buildResponse(
		[]*repository.SearchResult{searchResult},
		1,
		&models.SearchRequest{Page: 1, PerPage: 10},
		searchExecution{pipeline: models.SearchPipelineBrowse},
		"catalog-v1:42",
		emptySearchFacets(models.SearchFacetScopeUnavailable),
	)
	searchItem := searchResponse.Items[0]
	if searchItem.SourceID != "source-42" {
		t.Fatalf("source_id = %q, want source-42", searchItem.SourceID)
	}
	if !strings.HasPrefix(searchItem.CanonicalID, "entity-v1:") {
		t.Fatalf("canonical_id = %q, want versioned opaque identity", searchItem.CanonicalID)
	}
	if searchItem.Slug != "analista-de-dados-42" {
		t.Fatalf("slug = %q, want analista-de-dados-42", searchItem.Slug)
	}
	metadataText := string(searchItem.Metadata)
	if strings.Contains(metadataText, "private_contact") || strings.Contains(metadataText, "internal_salary") {
		t.Fatalf("metadata leaked non-allowlisted source fields: %s", metadataText)
	}
	if !strings.Contains(metadataText, `"id":"source-42"`) || !strings.Contains(metadataText, `"slug":"analista-de-dados-42"`) {
		t.Fatalf("metadata lost safe compatibility fields: %s", metadataText)
	}
}

func TestBuildCandidateSearchFacetsCanonicalizesCountsAndDeduplicatesNeighborhoods(t *testing.T) {
	t.Parallel()

	firstResult := newSearchResult("00000000-0000-4000-8000-000000000001", "First")
	firstResult.Item.Modalidade = "Híbrido"
	firstResult.Item.Organization = "Secretaria Municipal de Saúde"
	firstResult.Item.Bairros = []string{"Tijuca", "tijuca", "Méier"}
	secondResult := newSearchResult("00000000-0000-4000-8000-000000000002", "Second")
	secondResult.Item.Modalidade = "hibrido"
	secondResult.Item.Organization = "Secretaria Municipal de Saúde"
	secondResult.Item.Bairros = []string{"TIJUCA"}

	searchFacets := buildCandidateSearchFacets([]*repository.SearchResult{firstResult, secondResult})
	if searchFacets.Scope != models.SearchFacetScopeRetrievalCandidates {
		t.Fatalf("facet scope = %q", searchFacets.Scope)
	}
	if len(searchFacets.Modalidades) != 1 || searchFacets.Modalidades[0].Value != "hibrido" || searchFacets.Modalidades[0].Count != 2 {
		t.Fatalf("modality facets = %#v", searchFacets.Modalidades)
	}
	if len(searchFacets.Bairros) != 2 || searchFacets.Bairros[0].Value != "tijuca" || searchFacets.Bairros[0].Count != 2 {
		t.Fatalf("neighborhood facets = %#v", searchFacets.Bairros)
	}
	if len(searchFacets.Organizations) != 1 || searchFacets.Organizations[0].Count != 2 {
		t.Fatalf("organization facets = %#v", searchFacets.Organizations)
	}
}

func TestBrowseSearchReturnsCatalogFacetProviderCounts(t *testing.T) {
	t.Parallel()

	repositoryStub := &searchRepositoryWithFacetsStub{
		searchRepositoryStub: &searchRepositoryStub{
			searchFunction: func(context.Context, *models.SearchRequest) ([]*repository.SearchResult, int, error) {
				return []*repository.SearchResult{
					newSearchResult("00000000-0000-4000-8000-000000000001", "IPTU"),
				}, 1, nil
			},
		},
		searchFacetsFunction: func(context.Context, *models.SearchRequest) (models.SearchFacets, error) {
			return models.SearchFacets{
				Version: models.SearchFacetVersion,
				Scope:   models.SearchFacetScopeCatalogMatches,
				Types: []models.SearchFacetValue{{
					Value: "service",
					Label: "service",
					Count: 1,
				}},
				Modalidades:   []models.SearchFacetValue{},
				Bairros:       []models.SearchFacetValue{},
				Organizations: []models.SearchFacetValue{},
			}, nil
		},
	}
	searchService := NewSearchService(repositoryStub, nil, 0, nil, nil, DefaultSearchRuntimeConfig())

	searchResponse, searchError := searchService.Search(context.Background(), &models.SearchRequest{})
	if searchError != nil {
		t.Fatalf("Search returned an unexpected error: %v", searchError)
	}
	if searchResponse.Facets.Scope != models.SearchFacetScopeCatalogMatches ||
		len(searchResponse.Facets.Types) != 1 ||
		searchResponse.Facets.Types[0].Count != 1 {
		t.Fatalf("facets = %#v", searchResponse.Facets)
	}
}

func TestBrowseSearchUsesAtomicSnapshotAndPreservesItsRevision(t *testing.T) {
	t.Parallel()

	catalogRevisionCalls := 0
	legacySearchCalls := 0
	legacyFacetCalls := 0
	repositoryStub := &searchRepositoryWithBrowseSnapshotStub{
		searchRepositoryStub: &searchRepositoryStub{
			catalogRevisionFunction: func(context.Context) (string, error) {
				catalogRevisionCalls++
				if catalogRevisionCalls == 1 {
					return "catalog-v1:9", nil
				}
				return "catalog-v1:11", nil
			},
			searchFunction: func(context.Context, *models.SearchRequest) ([]*repository.SearchResult, int, error) {
				legacySearchCalls++
				return nil, 0, errors.New("legacy browse must not run")
			},
		},
		browseSnapshotFunction: func(context.Context, *models.SearchRequest) (*repository.BrowseSnapshot, error) {
			return &repository.BrowseSnapshot{
				CatalogRevision: "catalog-v1:10",
				Results: []*repository.SearchResult{
					newSearchResult("00000000-0000-4000-8000-000000000001", "IPTU"),
				},
				Total: 1,
				Facets: models.SearchFacets{
					Version: models.SearchFacetVersion,
					Scope:   models.SearchFacetScopeCatalogMatches,
					Types: []models.SearchFacetValue{
						{Value: "service", Label: "service", Count: 1},
					},
					Modalidades:   []models.SearchFacetValue{},
					Bairros:       []models.SearchFacetValue{},
					Organizations: []models.SearchFacetValue{},
				},
			}, nil
		},
		searchFacetsFunction: func(context.Context, *models.SearchRequest) (models.SearchFacets, error) {
			legacyFacetCalls++
			return models.SearchFacets{}, errors.New("legacy facets must not run")
		},
	}
	cacheRecorder := &searchCacheRecorder{}
	searchService := NewSearchService(repositoryStub, cacheRecorder, time.Minute, nil, nil, DefaultSearchRuntimeConfig())

	searchResponse, searchError := searchService.Search(context.Background(), &models.SearchRequest{})
	if searchError != nil {
		t.Fatalf("Search returned an unexpected error: %v", searchError)
	}
	if legacySearchCalls != 0 {
		t.Fatalf("legacy browse calls = %d, want 0", legacySearchCalls)
	}
	if legacyFacetCalls != 0 {
		t.Fatalf("legacy facet calls = %d, want 0", legacyFacetCalls)
	}
	if searchResponse.CatalogRevision != "catalog-v1:10" || !searchResponse.Degraded {
		t.Fatalf("snapshot provenance = revision %q degraded=%t", searchResponse.CatalogRevision, searchResponse.Degraded)
	}
	if searchResponse.Total != 1 || len(searchResponse.Items) != 1 || len(searchResponse.Facets.Types) != 1 {
		t.Fatalf("snapshot response = %#v", searchResponse)
	}
	if cacheRecorder.setCount != 0 {
		t.Fatalf("moving snapshot was cached %d time(s)", cacheRecorder.setCount)
	}
}

func TestBrowseSearchCachesAStableSnapshotUnderItsOwnRevision(t *testing.T) {
	t.Parallel()

	catalogRevisionCalls := 0
	repositoryStub := &searchRepositoryWithBrowseSnapshotStub{
		searchRepositoryStub: &searchRepositoryStub{
			catalogRevisionFunction: func(context.Context) (string, error) {
				catalogRevisionCalls++
				if catalogRevisionCalls == 1 {
					return "catalog-v1:9", nil
				}
				return "catalog-v1:10", nil
			},
		},
		browseSnapshotFunction: func(context.Context, *models.SearchRequest) (*repository.BrowseSnapshot, error) {
			return &repository.BrowseSnapshot{
				CatalogRevision: "catalog-v1:10",
				Results:         []*repository.SearchResult{},
				Total:           0,
				Facets: models.SearchFacets{
					Version: models.SearchFacetVersion,
					Scope:   models.SearchFacetScopeCatalogMatches,
				},
			}, nil
		},
		searchFacetsFunction: func(context.Context, *models.SearchRequest) (models.SearchFacets, error) {
			return models.SearchFacets{}, errors.New("legacy facets must not run")
		},
	}
	cacheRecorder := &searchCacheRecorder{}
	searchService := NewSearchService(repositoryStub, cacheRecorder, time.Minute, nil, nil, DefaultSearchRuntimeConfig())

	searchResponse, searchError := searchService.Search(context.Background(), &models.SearchRequest{})
	if searchError != nil {
		t.Fatalf("Search returned an unexpected error: %v", searchError)
	}
	if searchResponse.CatalogRevision != "catalog-v1:10" || searchResponse.Degraded {
		t.Fatalf("snapshot provenance = revision %q degraded=%t", searchResponse.CatalogRevision, searchResponse.Degraded)
	}
	if searchResponse.Facets.Types == nil || searchResponse.Facets.Modalidades == nil ||
		searchResponse.Facets.Bairros == nil || searchResponse.Facets.Organizations == nil {
		t.Fatalf("snapshot facets contain nil collections: %#v", searchResponse.Facets)
	}
	if cacheRecorder.setCount != 1 {
		t.Fatalf("stable snapshot cache writes = %d, want 1", cacheRecorder.setCount)
	}
}

func TestBrowseSearchReturnsAtomicSnapshotErrors(t *testing.T) {
	t.Parallel()

	repositoryStub := &searchRepositoryWithBrowseSnapshotStub{
		searchRepositoryStub: &searchRepositoryStub{},
		browseSnapshotFunction: func(context.Context, *models.SearchRequest) (*repository.BrowseSnapshot, error) {
			return nil, errors.New("snapshot unavailable")
		},
	}
	searchService := NewSearchService(repositoryStub, nil, 0, nil, nil, DefaultSearchRuntimeConfig())

	searchResponse, searchError := searchService.Search(context.Background(), &models.SearchRequest{})
	if searchError == nil || !strings.Contains(searchError.Error(), "snapshot unavailable") {
		t.Fatalf("error = %v, want snapshot failure", searchError)
	}
	if searchResponse != nil {
		t.Fatalf("response = %#v, want nil", searchResponse)
	}
}

func TestRankedSearchUsesResultSnapshotRevisionAndTemporalTTL(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	nextTransition := observedAt.Add(2500 * time.Microsecond)
	initialSnapshot := repository.CatalogSnapshotVersion{
		Revision: "catalog-v2:8:window-until-1783771200000000",
	}
	resultSnapshot := repository.CatalogSnapshotVersion{
		Revision:                  "catalog-v2:8:window-until-1783771200002500",
		ObservedAt:                observedAt,
		NextEligibilityTransition: &nextTransition,
	}
	catalogSnapshotCalls := 0
	embeddingCompleted := false
	legacyRankedCalls := 0
	baseRepository := &searchRepositoryStub{
		catalogSnapshotFunction: func(context.Context) (repository.CatalogSnapshotVersion, error) {
			catalogSnapshotCalls++
			if catalogSnapshotCalls == 1 {
				return initialSnapshot, nil
			}
			return resultSnapshot, nil
		},
		rankedSearchFunction: func(
			context.Context,
			*models.SearchRequest,
			repository.RankedSearchOptions,
		) ([]*repository.SearchResult, int, error) {
			legacyRankedCalls++
			return nil, 0, errors.New("legacy ranked search must not run")
		},
	}
	repositoryStub := &searchRepositoryWithRankedSnapshotStub{
		searchRepositoryStub: baseRepository,
		rankedSnapshotFunction: func(
			context.Context,
			*models.SearchRequest,
			repository.RankedSearchOptions,
		) (*repository.RankedSearchSnapshot, error) {
			if !embeddingCompleted {
				return nil, errors.New("ranked database snapshot started before remote embedding completed")
			}
			return &repository.RankedSearchSnapshot{
				SnapshotVersion: resultSnapshot,
				CatalogRevision: resultSnapshot.Revision,
				Results: []*repository.SearchResult{
					newSearchResult("00000000-0000-4000-8000-000000000001", "IPTU"),
				},
				Total: 1,
			}, nil
		},
	}
	semanticClient := &semanticSearchClientStub{
		embedding: []float32{0.1, 0.2},
		metadata: clients.EmbeddingMetadata{
			Model:            "embedding-test",
			Version:          "v1",
			Dimensions:       2,
			DocumentTaskType: "RETRIEVAL_DOCUMENT",
			DocumentVersion:  "document-v1",
		},
		onEmbed: func() {
			embeddingCompleted = true
		},
	}
	cacheRecorder := &searchCacheRecorder{}
	searchService := NewSearchService(
		repositoryStub,
		cacheRecorder,
		time.Minute,
		semanticClient,
		nil,
		DefaultSearchRuntimeConfig(),
	)

	searchResponse, searchError := searchService.Search(context.Background(), &models.SearchRequest{Q: "iptu"})
	if searchError != nil {
		t.Fatalf("Search returned an unexpected error: %v", searchError)
	}
	if legacyRankedCalls != 0 {
		t.Fatalf("legacy ranked calls = %d, want 0", legacyRankedCalls)
	}
	if searchResponse.CatalogRevision != resultSnapshot.Revision || searchResponse.Degraded {
		t.Fatalf("ranked snapshot provenance = revision %q degraded=%t", searchResponse.CatalogRevision, searchResponse.Degraded)
	}
	if cacheRecorder.setCount != 1 || cacheRecorder.setTTL != 2*time.Millisecond {
		t.Fatalf("cache writes = %d TTL=%s, want one write with 2ms", cacheRecorder.setCount, cacheRecorder.setTTL)
	}
}

func TestSearchRevalidatesCacheHitBeforeReturningIt(t *testing.T) {
	t.Parallel()

	catalogSnapshotCalls := 0
	searchCalls := 0
	repositoryStub := &searchRepositoryStub{
		catalogSnapshotFunction: func(context.Context) (repository.CatalogSnapshotVersion, error) {
			catalogSnapshotCalls++
			revision := "catalog-v2:1:window-until-100"
			if catalogSnapshotCalls >= 4 {
				revision = "catalog-v2:1:window-until-200"
			}
			return repository.CatalogSnapshotVersion{Revision: revision}, nil
		},
		searchFunction: func(context.Context, *models.SearchRequest) ([]*repository.SearchResult, int, error) {
			searchCalls++
			title := "Cached before boundary"
			if searchCalls == 2 {
				title = "Fresh after boundary"
			}
			return []*repository.SearchResult{
				newSearchResult("00000000-0000-4000-8000-000000000001", title),
			}, 1, nil
		},
	}
	cacheRecorder := &searchCacheRecorder{serveStored: true}
	searchService := NewSearchService(repositoryStub, cacheRecorder, time.Minute, nil, nil, DefaultSearchRuntimeConfig())

	firstResponse, firstError := searchService.Search(context.Background(), &models.SearchRequest{})
	if firstError != nil {
		t.Fatalf("first Search returned an unexpected error: %v", firstError)
	}
	if len(firstResponse.Items) != 1 || firstResponse.Items[0].Title != "Cached before boundary" {
		t.Fatalf("first response = %#v", firstResponse)
	}

	secondResponse, secondError := searchService.Search(context.Background(), &models.SearchRequest{})
	if secondError != nil {
		t.Fatalf("second Search returned an unexpected error: %v", secondError)
	}
	if len(secondResponse.Items) != 1 || secondResponse.Items[0].Title != "Fresh after boundary" {
		t.Fatalf("stale cache hit escaped revalidation: %#v", secondResponse)
	}
	if secondResponse.CatalogRevision != "catalog-v2:1:window-until-200" || secondResponse.Degraded {
		t.Fatalf("second response provenance = revision %q degraded=%t", secondResponse.CatalogRevision, secondResponse.Degraded)
	}
	if searchCalls != 2 || cacheRecorder.setCount != 2 {
		t.Fatalf("search calls = %d cache writes = %d, want 2 and 2", searchCalls, cacheRecorder.setCount)
	}
}

func TestCandidateSearchFacetsOmitUnknownOrOversizedValuesAndBoundLabels(t *testing.T) {
	t.Parallel()

	unknownModalityResult := newSearchResult("00000000-0000-4000-8000-000000000001", "Unknown modality")
	unknownModalityResult.Item.Modalidade = "teletransporte"
	unknownModalityResult.Item.Organization = strings.Repeat("ó", models.MaxSearchFilterRunes+1)
	onlineResult := newSearchResult("00000000-0000-4000-8000-000000000002", "Online")
	onlineResult.Item.Modalidade = "  ONLINE  "
	onlineResult.Item.Bairros = []string{"  Méier\t", strings.Repeat("x", models.MaxSearchFilterRunes+1)}
	remoteResult := newSearchResult("00000000-0000-4000-8000-000000000003", "Remote")
	remoteResult.Item.Modalidade = "À distância"
	remoteResult.Item.Bairros = []string{"MEIER"}

	searchFacets := buildCandidateSearchFacets([]*repository.SearchResult{
		unknownModalityResult,
		onlineResult,
		remoteResult,
	})
	if len(searchFacets.Modalidades) != 1 ||
		searchFacets.Modalidades[0].Value != "digital" ||
		searchFacets.Modalidades[0].Count != 2 {
		t.Fatalf("modality facets = %#v", searchFacets.Modalidades)
	}
	if len(searchFacets.Organizations) != 0 {
		t.Fatalf("oversized organization facet was emitted: %#v", searchFacets.Organizations)
	}
	if len(searchFacets.Bairros) != 1 ||
		searchFacets.Bairros[0].Value != "meier" ||
		searchFacets.Bairros[0].Count != 2 {
		t.Fatalf("neighborhood facets = %#v", searchFacets.Bairros)
	}

	accumulators := make(map[string]searchFacetAccumulator)
	addCanonicalSearchFacetValue(
		accumulators,
		"organization",
		strings.Repeat("á", models.MaxSearchFacetLabelRunes+1),
	)
	facetValues := sortedSearchFacetValues(accumulators)
	if len(facetValues) != 1 || utf8.RuneCountInString(facetValues[0].Label) != models.MaxSearchFacetLabelRunes {
		t.Fatalf("bounded facet label = %#v", facetValues)
	}
}

func TestDeduplicateSearchResultsCollapsesCanonicalServiceAliases(t *testing.T) {
	t.Parallel()

	legacyResult := newSearchResult("00000000-0000-4000-8000-000000000001", "Vacina animal")
	legacyResult.Item.Source = models.SourceTypesense
	legacyResult.Item.ExternalID = "legacy-42"
	legacyResult.Item.SourceData = json.RawMessage(`{"slug":"vacina-animal"}`)
	canonicalResult := newSearchResult("00000000-0000-4000-8000-000000000002", "Vacina animal")
	canonicalResult.Item.Source = models.SourceSalesForce
	canonicalResult.Item.ExternalID = "salesforce-84"
	canonicalResult.Item.URL = "https://pref.rio/servicos/vacina-animal?campaign=ignored"
	otherResult := newSearchResult("00000000-0000-4000-8000-000000000003", "Licença")
	otherResult.Item.Source = models.SourceSalesForce
	otherResult.Item.ExternalID = "salesforce-99"

	deduplicatedResults := deduplicateSearchResults([]*repository.SearchResult{
		legacyResult,
		canonicalResult,
		otherResult,
	})
	if len(deduplicatedResults) != 2 {
		t.Fatalf("deduplicated result count = %d, want 2", len(deduplicatedResults))
	}
	if deduplicatedResults[0] != legacyResult || deduplicatedResults[1] != otherResult {
		t.Fatalf("deduplicated results changed stable winner order: %#v", deduplicatedResults)
	}
	if canonicalSearchEntityID(legacyResult.Item) != canonicalSearchEntityID(canonicalResult.Item) {
		t.Fatal("equivalent service aliases received different canonical identities")
	}
	if canonicalSearchEntityID(canonicalResult.Item) == canonicalSearchEntityID(otherResult.Item) {
		t.Fatal("unrelated source documents received the same canonical identity")
	}
}

func TestCanonicalServiceSlugKeepsPercentEncodingAlignedWithSQLFallback(t *testing.T) {
	t.Parallel()

	encodedSlug := canonicalServiceSlug("", "https://pref.rio/servicos/inscri%C3%A7%C3%A3o-online")
	if encodedSlug != "inscri%c3%a7%c3%a3o-online" {
		t.Fatalf("encoded canonical slug = %q", encodedSlug)
	}
	if encodedSlug == "inscrição-online" {
		t.Fatal("Go canonicalization percent-decoded a slug that SQL keeps encoded")
	}
	for _, nonPathURL := range []string{
		"https://pref.rio/other?next=/servicos/query-only",
		"https://pref.rio/other#/servicos/fragment-only",
		"https://servicos/authority-only",
	} {
		if slug := canonicalServiceSlug("", nonPathURL); slug != "" {
			t.Errorf("canonicalServiceSlug(%q) = %q, want no path evidence", nonPathURL, slug)
		}
	}
}

func newSearchResult(identifier string, title string) *repository.SearchResult {
	return &repository.SearchResult{
		Item: &models.CatalogItem{
			ID:         uuid.MustParse(identifier),
			ExternalID: identifier,
			Type:       models.TypeService,
			Source:     models.SourceTypesense,
			Title:      title,
		},
		Rank: 1,
	}
}
