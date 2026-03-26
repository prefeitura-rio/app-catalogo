package services

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/cache"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

type SearchService struct {
	searchRepo *repository.SearchRepository
	cache      *cache.RedisCache
	searchTTL  time.Duration
}

func NewSearchService(
	searchRepo *repository.SearchRepository,
	cache *cache.RedisCache,
	searchTTL time.Duration,
) *SearchService {
	return &SearchService{
		searchRepo: searchRepo,
		cache:      cache,
		searchTTL:  searchTTL,
	}
}

// Search executa a busca e aplica cache.
func (s *SearchService) Search(ctx context.Context, req *models.SearchRequest) (*models.SearchResponse, error) {
	req.Normalize()

	cacheKey := s.cacheKey(req)

	var cached models.SearchResponse
	if err := s.cache.Get(ctx, cacheKey, &cached); err == nil {
		log.Debug().Str("key", cacheKey).Msg("search: cache hit")
		return &cached, nil
	}

	results, total, err := s.searchRepo.Search(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	resp := s.buildResponse(results, total, req)

	if err := s.cache.Set(ctx, cacheKey, resp, s.searchTTL); err != nil {
		log.Warn().Err(err).Msg("search: falha ao salvar cache")
	}

	return resp, nil
}

func (s *SearchService) buildResponse(results []*repository.SearchResult, total int, req *models.SearchRequest) *models.SearchResponse {
	items := make([]*models.SearchItem, 0, len(results))
	for _, r := range results {
		item := &models.SearchItem{
			ID:             r.Item.ID.String(),
			Type:           r.Item.Type,
			Source:         r.Item.Source,
			Title:          r.Item.Title,
			ShortDesc:      r.Item.ShortDesc,
			Organization:   r.Item.Organization,
			URL:            r.Item.URL,
			ImageURL:       r.Item.ImageURL,
			Modalidade:     r.Item.Modalidade,
			Bairros:        r.Item.Bairros,
			Tags:           r.Item.Tags,
			RelevanceScore: r.Rank,
			Metadata:       r.Item.SourceData,
		}
		if r.Headline != "" {
			item.Highlights = []string{r.Headline}
		}
		items = append(items, item)
	}

	return &models.SearchResponse{
		Total:   total,
		Page:    req.Page,
		PerPage: req.PerPage,
		Items:   items,
	}
}

func (s *SearchService) cacheKey(req *models.SearchRequest) string {
	typeStrs := make([]string, len(req.Types))
	for i, t := range req.Types {
		typeStrs[i] = string(t)
	}

	filterJSON, _ := json.Marshal(req.Filters)
	raw := fmt.Sprintf("search:%s:%s:%s:%d:%d",
		req.Q,
		strings.Join(typeStrs, ","),
		string(filterJSON),
		req.Page,
		req.PerPage,
	)
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("catalogo:search:%x", h[:8])
}
