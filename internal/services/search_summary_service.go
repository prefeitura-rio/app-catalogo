package services

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"

	"github.com/prefeitura-rio/app-catalogo/internal/cache"
	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/observability"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

const searchSummaryCacheKeyPrefix = "catalogo:search-summary:v1:"

type searchSummaryRepository interface {
	GetSearchSummaryCandidates(context.Context, string, []uuid.UUID) (*repository.SearchSummaryCandidateSnapshot, error)
}

type groundedSummaryGenerator interface {
	GenerateGroundedSummary(
		context.Context,
		string,
		[]clients.GroundedSummaryCandidate,
	) ([]clients.GeneratedSummarySegment, error)
}

type searchSummaryCache interface {
	Get(context.Context, string, any) error
	Set(context.Context, string, any, time.Duration) error
}

type SearchSummaryService struct {
	repository   searchSummaryRepository
	generator    groundedSummaryGenerator
	cache        searchSummaryCache
	timeout      time.Duration
	cacheTTL     time.Duration
	capacity     chan struct{}
	singleflight singleflight.Group
}

type searchSummaryGenerationResult struct {
	response *models.SearchSummaryResponse
	outcome  string
}

func NewSearchSummaryService(
	repositoryProvider searchSummaryRepository,
	generator groundedSummaryGenerator,
	cacheProvider searchSummaryCache,
	timeout time.Duration,
	cacheTTL time.Duration,
	maximumConcurrency int,
) *SearchSummaryService {
	maximumConcurrency = max(maximumConcurrency, 1)
	capacity := make(chan struct{}, maximumConcurrency)
	for capacityIndex := 0; capacityIndex < maximumConcurrency; capacityIndex++ {
		capacity <- struct{}{}
	}
	return &SearchSummaryService{
		repository: repositoryProvider,
		generator:  generator,
		cache:      cacheProvider,
		timeout:    timeout,
		cacheTTL:   cacheTTL,
		capacity:   capacity,
	}
}

func (service *SearchSummaryService) Generate(
	requestContext context.Context,
	request *models.SearchSummaryRequest,
) (*models.SearchSummaryResponse, error) {
	startedAt := time.Now()
	metricOutcome := "error"
	defer func() {
		observability.SearchSummaryRequestsTotal.WithLabelValues(metricOutcome).Inc()
		observability.SearchSummaryDuration.WithLabelValues(metricOutcome).Observe(time.Since(startedAt).Seconds())
	}()
	if normalizationError := request.Normalize(); normalizationError != nil {
		metricOutcome = "invalid_request"
		return nil, fmt.Errorf("search summary request: %w", normalizationError)
	}
	candidateSnapshot, repositoryError := service.repository.GetSearchSummaryCandidates(
		requestContext, request.CatalogRevision, request.CandidateIDs,
	)
	if repositoryError != nil {
		if errors.Is(repositoryError, models.ErrCatalogRevisionMismatch) {
			metricOutcome = "stale_revision"
		} else {
			metricOutcome = "catalog_error"
		}
		return nil, fmt.Errorf("search summary candidates: %w", repositoryError)
	}
	emptyResponse := &models.SearchSummaryResponse{
		Query: request.Query, Segments: []models.SearchSummarySegment{}, Generated: false,
	}
	if service.generator == nil {
		metricOutcome = "disabled"
		return emptyResponse, nil
	}
	cacheKey := searchSummaryCacheKey(request, candidateSnapshot.CatalogRevision)
	if cachedSummary := service.cachedSummary(requestContext, cacheKey); cachedSummary != nil {
		metricOutcome = "cache_hit"
		return cachedSummary, nil
	}

	resultChannel := service.singleflight.DoChan(cacheKey, func() (any, error) {
		if cachedSummary := service.cachedSummary(requestContext, cacheKey); cachedSummary != nil {
			return searchSummaryGenerationResult{response: cachedSummary, outcome: "cache_hit"}, nil
		}
		select {
		case <-service.capacity:
			defer func() { service.capacity <- struct{}{} }()
		default:
			return searchSummaryGenerationResult{response: emptyResponse, outcome: "capacity_exhausted"}, nil
		}
		generationContext, cancelGeneration := context.WithTimeout(context.WithoutCancel(requestContext), service.timeout)
		defer cancelGeneration()
		generatedSummary, generationError := service.generateGrounded(
			generationContext, request.Query, candidateSnapshot.Items,
		)
		if generationError != nil {
			log.Warn().Err(generationError).Msg("search summary generation failed")
			return searchSummaryGenerationResult{response: emptyResponse, outcome: "provider_failure"}, nil
		}
		if cacheError := service.cacheSummary(generationContext, cacheKey, generatedSummary); cacheError != nil {
			log.Warn().Err(cacheError).Msg("search summary cache write failed")
		}
		return searchSummaryGenerationResult{response: generatedSummary, outcome: "generated"}, nil
	})
	select {
	case <-requestContext.Done():
		metricOutcome = "canceled"
		return nil, requestContext.Err()
	case singleflightResult := <-resultChannel:
		if singleflightResult.Err != nil {
			return nil, singleflightResult.Err
		}
		generationResult, validResponse := singleflightResult.Val.(searchSummaryGenerationResult)
		if !validResponse {
			return nil, errors.New("search summary singleflight returned an invalid response")
		}
		metricOutcome = generationResult.outcome
		return generationResult.response, nil
	}
}

func (service *SearchSummaryService) generateGrounded(
	generationContext context.Context,
	query string,
	catalogItems []*models.CatalogItem,
) (*models.SearchSummaryResponse, error) {
	generatorCandidates := make([]clients.GroundedSummaryCandidate, 0, len(catalogItems))
	for _, catalogItem := range catalogItems {
		generatorCandidates = append(generatorCandidates, clients.GroundedSummaryCandidate{
			Title: catalogItem.Title, Summary: catalogItem.ShortDesc,
		})
	}
	generatedSegments, generationError := service.generator.GenerateGroundedSummary(
		generationContext, query, generatorCandidates,
	)
	if generationError != nil {
		return nil, generationError
	}
	if len(generatedSegments) < 1 || len(generatedSegments) > models.MaximumSearchSummarySegments {
		return nil, errors.New("search summary generator returned an invalid segment count")
	}
	segments := make([]models.SearchSummarySegment, 0, len(generatedSegments))
	hasGroundedCitation := false
	for segmentIndex, generatedSegment := range generatedSegments {
		generatedSegment.Text = strings.TrimSpace(generatedSegment.Text)
		if generatedSegment.Text == "" || len([]rune(generatedSegment.Text)) > models.MaximumCatalogDescriptionRunes {
			return nil, fmt.Errorf("search summary generator returned invalid segment %d text", segmentIndex)
		}
		segment := models.SearchSummarySegment{Text: generatedSegment.Text}
		if generatedSegment.CandidateIndex != nil {
			if *generatedSegment.CandidateIndex < 0 || *generatedSegment.CandidateIndex >= len(catalogItems) {
				return nil, fmt.Errorf("search summary generator returned invalid segment %d citation", segmentIndex)
			}
			catalogItem := catalogItems[*generatedSegment.CandidateIndex]
			segment.Slug, segment.URL = searchSummaryCitation(catalogItem)
			hasGroundedCitation = true
		}
		segments = append(segments, segment)
	}
	if !hasGroundedCitation {
		return nil, errors.New("search summary generator returned no grounded citation")
	}
	return &models.SearchSummaryResponse{Query: query, Segments: segments, Generated: true}, nil
}

func searchSummaryCitation(catalogItem *models.CatalogItem) (string, string) {
	if catalogItem.Type == models.TypeService {
		canonicalSlug, _, slugError := models.PublicServiceSlugs(catalogItem)
		if slugError == nil && canonicalSlug != "" {
			serviceDetail, detailError := models.NewPublicServiceDetail(catalogItem)
			if detailError == nil && strings.TrimSpace(serviceDetail.Category) != "" {
				return canonicalSlug, models.PublicServiceURL(serviceDetail.Category, canonicalSlug)
			}
			return canonicalSlug, catalogItem.URL
		}
	}
	return catalogItem.ID.String(), catalogItem.URL
}

func searchSummaryCacheKey(request *models.SearchSummaryRequest, catalogRevision string) string {
	requestParts := []string{request.Query, catalogRevision}
	for _, candidateID := range request.CandidateIDs {
		requestParts = append(requestParts, candidateID.String())
	}
	digest := sha256.Sum256([]byte(strings.Join(requestParts, "\x00")))
	return fmt.Sprintf("%s%x", searchSummaryCacheKeyPrefix, digest)
}

func (service *SearchSummaryService) cachedSummary(
	requestContext context.Context,
	cacheKey string,
) *models.SearchSummaryResponse {
	if service.cache == nil {
		return nil
	}
	var cachedResponse models.SearchSummaryResponse
	if cacheError := service.cache.Get(requestContext, cacheKey, &cachedResponse); cacheError != nil {
		if !errors.Is(cacheError, cache.ErrCacheMiss) {
			log.Warn().Err(cacheError).Msg("search summary cache read failed")
		}
		return nil
	}
	return &cachedResponse
}

func (service *SearchSummaryService) cacheSummary(
	requestContext context.Context,
	cacheKey string,
	response *models.SearchSummaryResponse,
) error {
	if service.cache == nil || service.cacheTTL <= 0 || !response.Generated {
		return nil
	}
	return service.cache.Set(requestContext, cacheKey, response, service.cacheTTL)
}
