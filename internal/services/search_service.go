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
	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/query"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

// rerankerTop é o número máximo de resultados enviados ao cross-encoder.
const rerankerTop = 20

type SearchService struct {
	searchRepo *repository.SearchRepository
	cache      *cache.RedisCache
	searchTTL  time.Duration
	gemini     *clients.GeminiEmbeddingClient // nil = busca semântica desativada
	reranker   *clients.RerankerClient        // nil = reranking desativado
}

func NewSearchService(
	searchRepo *repository.SearchRepository,
	cache *cache.RedisCache,
	searchTTL time.Duration,
	gemini *clients.GeminiEmbeddingClient,
	reranker *clients.RerankerClient,
) *SearchService {
	return &SearchService{
		searchRepo: searchRepo,
		cache:      cache,
		searchTTL:  searchTTL,
		gemini:     gemini,
		reranker:   reranker,
	}
}

// Search executa o pipeline completo:
// 1. Expansão de sinônimos (query.Expand)
// 2. Busca híbrida FTS+semântica via RRF (ou FTS puro como fallback)
// 3. Reranking com cross-encoder (se RERANKER_URL configurado)
func (s *SearchService) Search(ctx context.Context, req *models.SearchRequest) (*models.SearchResponse, error) {
	req.Normalize()

	cacheKey := s.cacheKey(req)
	var cached models.SearchResponse
	if err := s.cache.Get(ctx, cacheKey, &cached); err == nil {
		log.Debug().Str("key", cacheKey).Msg("search: cache hit")
		return &cached, nil
	}

	// 1. Expansão de sinônimos — preserva a query original para o embedding
	originalQ := req.Q
	if req.Q != "" {
		req.Q = query.Expand(req.Q)
		if req.Q != originalQ {
			log.Debug().Str("original", originalQ).Str("expanded", req.Q).Msg("search: query expandida")
		}
	}

	// 2. Busca principal
	var results []*repository.SearchResult
	var total int
	var err error

	if originalQ != "" && s.gemini != nil {
		// Embedding usa a query original (sem expansão) para preservar o intent semântico
		embedCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		embedding, embedErr := s.gemini.EmbedQuery(embedCtx, originalQ)
		cancel()

		if embedErr == nil {
			results, total, err = s.searchRepo.SearchHybrid(ctx, req, clients.VectorLiteral(embedding))
			if err != nil {
				log.Warn().Err(err).Msg("search: hybrid falhou, caindo para FTS")
				results, total, err = s.searchRepo.Search(ctx, req)
			}
		} else {
			log.Warn().Err(embedErr).Msg("search: embedding indisponível, usando FTS")
			results, total, err = s.searchRepo.Search(ctx, req)
		}
	} else {
		results, total, err = s.searchRepo.Search(ctx, req)
	}

	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	// 3. Reranking com cross-encoder (só quando há query e resultados suficientes)
	if originalQ != "" && s.reranker != nil && len(results) > 1 {
		results = s.maybeRerank(ctx, originalQ, results)
	}

	resp := s.buildResponse(results, total, req)

	if err := s.cache.Set(ctx, cacheKey, resp, s.searchTTL); err != nil {
		log.Warn().Err(err).Msg("search: falha ao salvar cache")
	}

	return resp, nil
}

// maybeRerank envia os top-rerankerTop resultados ao cross-encoder.
// Retorna os resultados na ordem original em caso de falha (fallback silencioso).
func (s *SearchService) maybeRerank(ctx context.Context, originalQ string, results []*repository.SearchResult) []*repository.SearchResult {
	top := results
	rest := []*repository.SearchResult{}
	if len(results) > rerankerTop {
		top = results[:rerankerTop]
		rest = results[rerankerTop:]
	}

	docs := make([]clients.RerankerDocument, len(top))
	for i, r := range top {
		text := r.Item.Title
		if r.Item.ShortDesc != "" {
			text += ". " + r.Item.ShortDesc
		}
		docs[i] = clients.RerankerDocument{ID: r.Item.ID.String(), Text: text}
	}

	reranked, err := s.reranker.Rerank(ctx, originalQ, docs)
	if err != nil || len(reranked) == 0 {
		return results
	}

	byID := make(map[string]*repository.SearchResult, len(top))
	for _, r := range top {
		byID[r.Item.ID.String()] = r
	}

	ordered := make([]*repository.SearchResult, 0, len(results))
	for _, rr := range reranked {
		if sr, ok := byID[rr.ID]; ok {
			sr.Rank = rr.Score
			ordered = append(ordered, sr)
		}
	}
	return append(ordered, rest...)
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
