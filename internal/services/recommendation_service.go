package services

import (
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/cache"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

type RecommendationService struct {
	itemRepo            recommendationRepository
	cache               recommendationCache
	weights             models.ScoringWeights
	authTTL             time.Duration
	anonymousTTL        time.Duration
	rankingVersion      string
	journeyGraphVersion string
}

const (
	defaultRecommendationRankingVersion = "recommendation-ranker-v2"
	defaultJourneyGraphVersion          = "journey-graph-v1"
)

type recommendationRepository interface {
	GetCandidates(context.Context, []models.ItemType, int) ([]*models.CatalogItem, error)
	GetJourneyBoosts(context.Context, []string) (map[string]float64, error)
}

type recommendationCache interface {
	Get(context.Context, string, any) error
	Set(context.Context, string, any, time.Duration) error
}

type recommendationCandidateSnapshotProvider interface {
	GetCandidateSnapshot(
		context.Context,
		[]models.ItemType,
		int,
	) (*repository.RecommendationCandidateSnapshot, error)
}

func NewRecommendationService(
	itemRepo *repository.CatalogItemRepository,
	cache *cache.RedisCache,
	weights models.ScoringWeights,
	authTTL, anonymousTTL time.Duration,
) *RecommendationService {
	return &RecommendationService{
		itemRepo:            itemRepo,
		cache:               cache,
		weights:             weights,
		authTTL:             authTTL,
		anonymousTTL:        anonymousTTL,
		rankingVersion:      defaultRecommendationRankingVersion,
		journeyGraphVersion: defaultJourneyGraphVersion,
	}
}

// Recommend retorna recomendações personalizadas para um cidadão autenticado.
func (s *RecommendationService) Recommend(
	ctx context.Context,
	profile *models.CitizenProfile,
	req *models.RecommendationRequest,
) (*models.RecommendationResponse, error) {
	if normalizeError := req.Normalize(); normalizeError != nil {
		return nil, fmt.Errorf("recommendation request: %w", normalizeError)
	}

	initialSnapshot, snapshotError := s.catalogSnapshot(ctx)
	if snapshotError != nil {
		return nil, snapshotError
	}
	cacheKey := s.authCacheKey(profile.CPFHash, req, initialSnapshot.Revision)
	if cachedResponse := s.cachedRecommendation(ctx, cacheKey); cachedResponse != nil {
		revalidatedSnapshot, revalidationError := s.catalogSnapshot(ctx)
		if revalidationError != nil {
			return nil, revalidationError
		}
		if revalidatedSnapshot.Revision == initialSnapshot.Revision {
			return cachedResponse, nil
		}
		initialSnapshot = revalidatedSnapshot
	}

	candidateSnapshot, candidatesError := s.candidateSnapshot(ctx, req.Types, req.Limit*5, initialSnapshot)
	if candidatesError != nil {
		return nil, fmt.Errorf("recommendation: %w", candidatesError)
	}

	ranked, rankingError := s.rankCandidates(candidateSnapshot.Items, profile, req)
	if rankingError != nil {
		return nil, rankingError
	}
	ranked, journeyBoostError := s.applyJourneyBoosts(ctx, ranked)
	if journeyBoostError != nil {
		return nil, journeyBoostError
	}

	resp := &models.RecommendationResponse{
		Items:        ranked[:min(req.Limit, len(ranked))],
		Context:      req.Context,
		Personalized: true,
	}

	latestSnapshot, latestSnapshotError := s.catalogSnapshot(ctx)
	if latestSnapshotError != nil {
		return nil, latestSnapshotError
	}
	if latestSnapshot.Revision == candidateSnapshot.SnapshotVersion.Revision {
		cacheKey = s.authCacheKey(profile.CPFHash, req, candidateSnapshot.SnapshotVersion.Revision)
		s.cacheRecommendation(ctx, cacheKey, resp, s.authTTL, candidateSnapshot.SnapshotVersion)
	}
	return resp, nil
}

// RecommendAnonymous retorna recomendações para usuários não autenticados.
func (s *RecommendationService) RecommendAnonymous(
	ctx context.Context,
	req *models.RecommendationRequest,
) (*models.RecommendationResponse, error) {
	if normalizeError := req.Normalize(); normalizeError != nil {
		return nil, fmt.Errorf("recommendation anonymous request: %w", normalizeError)
	}

	initialSnapshot, snapshotError := s.catalogSnapshot(ctx)
	if snapshotError != nil {
		return nil, snapshotError
	}
	cacheKey := s.anonymousCacheKey(req, initialSnapshot.Revision)
	if cachedResponse := s.cachedRecommendation(ctx, cacheKey); cachedResponse != nil {
		revalidatedSnapshot, revalidationError := s.catalogSnapshot(ctx)
		if revalidationError != nil {
			return nil, revalidationError
		}
		if revalidatedSnapshot.Revision == initialSnapshot.Revision {
			return cachedResponse, nil
		}
		initialSnapshot = revalidatedSnapshot
	}

	candidateSnapshot, candidatesError := s.candidateSnapshot(ctx, req.Types, req.Limit*3, initialSnapshot)
	if candidatesError != nil {
		return nil, fmt.Errorf("recommendation anonymous: %w", candidatesError)
	}

	ranked, rankingError := s.rankCandidates(candidateSnapshot.Items, nil, req)
	if rankingError != nil {
		return nil, rankingError
	}
	ranked, journeyBoostError := s.applyJourneyBoosts(ctx, ranked)
	if journeyBoostError != nil {
		return nil, journeyBoostError
	}

	resp := &models.RecommendationResponse{
		Items:        ranked[:min(req.Limit, len(ranked))],
		Context:      req.Context,
		Personalized: false,
	}

	latestSnapshot, latestSnapshotError := s.catalogSnapshot(ctx)
	if latestSnapshotError != nil {
		return nil, latestSnapshotError
	}
	if latestSnapshot.Revision == candidateSnapshot.SnapshotVersion.Revision {
		cacheKey = s.anonymousCacheKey(req, candidateSnapshot.SnapshotVersion.Revision)
		s.cacheRecommendation(ctx, cacheKey, resp, s.anonymousTTL, candidateSnapshot.SnapshotVersion)
	}
	return resp, nil
}

// applyJourneyBoosts aplica o boost de jornadas do cidadão aos itens já rankeados.
// Pega os top-5 pelo score, consulta vizinhos de jornada e adiciona boost nos itens
// que já estão na lista. Re-ordena após o boost.
func (s *RecommendationService) applyJourneyBoosts(
	ctx context.Context,
	ranked []*models.RankedItem,
) ([]*models.RankedItem, error) {
	if len(ranked) == 0 {
		return ranked, nil
	}

	// Extrai IDs dos top-5 para consultar jornadas
	topN := min(5, len(ranked))
	fromIDs := make([]string, topN)
	for i := 0; i < topN; i++ {
		fromIDs[i] = ranked[i].ID
	}

	boosts, err := s.itemRepo.GetJourneyBoosts(ctx, fromIDs)
	if err != nil {
		return nil, fmt.Errorf("recommendation journey boosts: %w", err)
	}
	if len(boosts) == 0 {
		return ranked, nil
	}

	for _, item := range ranked {
		if boost, ok := boosts[item.ID]; ok {
			item.Score = round2(item.Score + boost)
			if item.ScoreBreakdown != nil {
				item.ScoreBreakdown["journey"] = round2(boost)
			}
		}
	}

	slices.SortFunc(ranked, compareRankedItems)
	return ranked, nil
}

// rankCandidates calcula o score de cada item e ordena decrescentemente.
func (s *RecommendationService) rankCandidates(
	items []*models.CatalogItem,
	profile *models.CitizenProfile,
	req *models.RecommendationRequest,
) ([]*models.RankedItem, error) {
	typeWeights := models.TypeWeightsByContext[req.Context]
	if typeWeights == nil {
		typeWeights = models.TypeWeightsByContext[models.ContextHomepage]
	}

	ranked := make([]*models.RankedItem, 0, len(items))
	for _, item := range items {
		score, breakdown, scoringError := s.scoreItem(item, profile, typeWeights)
		if scoringError != nil {
			return nil, scoringError
		}
		ranked = append(ranked, &models.RankedItem{
			ID:             item.ID.String(),
			Type:           item.Type,
			Source:         item.Source,
			Title:          item.Title,
			ShortDesc:      item.ShortDesc,
			Organization:   item.Organization,
			URL:            item.URL,
			ImageURL:       item.ImageURL,
			Modalidade:     item.Modalidade,
			Bairros:        item.Bairros,
			Tags:           item.Tags,
			Score:          score,
			ScoreBreakdown: breakdown,
		})
	}

	slices.SortFunc(ranked, compareRankedItems)
	return ranked, nil
}

func compareRankedItems(firstItem *models.RankedItem, secondItem *models.RankedItem) int {
	if scoreComparison := cmp.Compare(secondItem.Score, firstItem.Score); scoreComparison != 0 {
		return scoreComparison
	}
	return cmp.Compare(firstItem.ID, secondItem.ID)
}

// scoreItem calculates the profile-based score before unconstrained journey
// graph boosts are applied.
func (s *RecommendationService) scoreItem(
	item *models.CatalogItem,
	profile *models.CitizenProfile,
	typeWeights map[models.ItemType]float64,
) (float64, map[string]float64, error) {
	ta, targetAudienceError := item.ParseTargetAudience()
	if targetAudienceError != nil {
		return 0, nil, fmt.Errorf("recommendation: invalid catalog target audience: %w", targetAudienceError)
	}

	var escolaridadeScore, rendaScore, locScore, acessibilidadeScore, faixaEtariaScore, tipoScore float64

	if profile == nil {
		// Sem perfil: scores neutros
		escolaridadeScore = 0.7
		rendaScore = 0.8
		locScore = 0.7
		acessibilidadeScore = 1.0
		faixaEtariaScore = 0.8
	} else {
		// Escolaridade
		escolaridadeScore = matchStringSlice(profile.Escolaridade, ta.Escolaridade, 0.7)

		// Renda familiar
		rendaScore = matchRenda(profile.RendaFamiliar, ta.Renda)

		// Localização: bairro match ou modalidade online
		locScore = matchLocalizacao(profile.Bairro, item.Bairros, item.Modalidade)

		// Acessibilidade: PCD ou item universal
		acessibilidadeScore = matchAcessibilidade(profile.Deficiencia, ta.Deficiencia)

		// Faixa etária
		faixaEtariaScore = matchStringSlice(profile.FaixaEtaria, ta.FaixaEtaria, 0.8)
	}

	// Peso do tipo de item no contexto
	tipoScore = typeWeights[item.Type]
	if tipoScore == 0 {
		tipoScore = 0.25
	}

	w := s.weights
	total := w.Escolaridade*escolaridadeScore +
		w.RendaFamiliar*rendaScore +
		w.Localizacao*locScore +
		w.Acessibilidade*acessibilidadeScore +
		w.FaixaEtaria*faixaEtariaScore +
		w.TipoItem*tipoScore

	breakdown := map[string]float64{
		"escolaridade":   round2(w.Escolaridade * escolaridadeScore),
		"renda":          round2(w.RendaFamiliar * rendaScore),
		"localizacao":    round2(w.Localizacao * locScore),
		"acessibilidade": round2(w.Acessibilidade * acessibilidadeScore),
		"faixa_etaria":   round2(w.FaixaEtaria * faixaEtariaScore),
		"tipo":           round2(w.TipoItem * tipoScore),
	}

	return round2(total), breakdown, nil
}

func matchStringSlice(profileVal string, targetVals []string, defaultScore float64) float64 {
	if len(targetVals) == 0 {
		return defaultScore // sem restrição
	}
	if profileVal == "" {
		return defaultScore
	}
	for _, v := range targetVals {
		if strings.EqualFold(v, profileVal) {
			return 1.0
		}
	}
	return 0.3
}

func matchRenda(profileRenda, targetRenda string) float64 {
	if targetRenda == "" {
		return 0.8
	}
	if profileRenda == "" {
		return 0.7
	}
	if strings.EqualFold(profileRenda, targetRenda) {
		return 1.0
	}
	return 0.4
}

func matchLocalizacao(profileBairro string, itemBairros []string, modalidade string) float64 {
	// Modalidade online é relevante para todos
	if strings.Contains(strings.ToLower(modalidade), "online") ||
		strings.Contains(strings.ToLower(modalidade), "remoto") ||
		strings.Contains(strings.ToLower(modalidade), "ead") {
		return 0.6
	}

	if len(itemBairros) == 0 {
		return 0.7 // sem restrição geográfica
	}
	if profileBairro == "" {
		return 0.5
	}
	for _, b := range itemBairros {
		if strings.EqualFold(b, profileBairro) {
			return 1.0
		}
	}
	return 0.3
}

func matchAcessibilidade(profileDef string, targetDef []string) float64 {
	if profileDef == "" {
		return 1.0 // sem deficiência: todos os itens são elegíveis
	}
	// Tem deficiência: verificar se o item tem acessibilidade
	if len(targetDef) == 0 {
		return 0.6 // item não declara acessibilidade
	}
	for _, d := range targetDef {
		if strings.EqualFold(d, profileDef) || strings.EqualFold(d, "todos") {
			return 1.0
		}
	}
	return 0.4
}

func round2(v float64) float64 {
	return float64(int(v*100)) / 100
}

func (s *RecommendationService) catalogSnapshot(
	ctx context.Context,
) (repository.CatalogSnapshotVersion, error) {
	if snapshotProvider, providesSnapshot := s.itemRepo.(catalogSnapshotProvider); providesSnapshot {
		snapshotVersion, snapshotError := snapshotProvider.CatalogSnapshot(ctx)
		if snapshotError != nil {
			return repository.CatalogSnapshotVersion{}, fmt.Errorf("recommendation catalog snapshot: %w", snapshotError)
		}
		if strings.TrimSpace(snapshotVersion.Revision) == "" {
			return repository.CatalogSnapshotVersion{}, fmt.Errorf("recommendation catalog snapshot: provider returned an empty revision")
		}
		return snapshotVersion, nil
	}
	if revisionProvider, providesRevision := s.itemRepo.(catalogRevisionProvider); providesRevision {
		catalogRevision, revisionError := revisionProvider.CatalogRevision(ctx)
		if revisionError != nil {
			return repository.CatalogSnapshotVersion{}, fmt.Errorf("recommendation catalog revision: %w", revisionError)
		}
		if strings.TrimSpace(catalogRevision) == "" {
			return repository.CatalogSnapshotVersion{}, fmt.Errorf("recommendation catalog revision: provider returned an empty revision")
		}
		return repository.CatalogSnapshotVersion{Revision: catalogRevision}, nil
	}
	return repository.CatalogSnapshotVersion{Revision: unversionedComponent}, nil
}

func (s *RecommendationService) candidateSnapshot(
	ctx context.Context,
	itemTypes []models.ItemType,
	limit int,
	fallbackVersion repository.CatalogSnapshotVersion,
) (*repository.RecommendationCandidateSnapshot, error) {
	if snapshotProvider, providesSnapshot := s.itemRepo.(recommendationCandidateSnapshotProvider); providesSnapshot {
		candidateSnapshot, snapshotError := snapshotProvider.GetCandidateSnapshot(ctx, itemTypes, limit)
		if snapshotError != nil {
			return nil, snapshotError
		}
		if candidateSnapshot == nil || strings.TrimSpace(candidateSnapshot.CatalogRevision) == "" {
			return nil, fmt.Errorf("recommendation candidate provider returned an invalid snapshot")
		}
		if validCatalogSnapshotVersion(candidateSnapshot.SnapshotVersion) &&
			candidateSnapshot.SnapshotVersion.Revision != candidateSnapshot.CatalogRevision {
			return nil, fmt.Errorf(
				"recommendation candidate provider returned conflicting revisions %q and %q",
				candidateSnapshot.SnapshotVersion.Revision,
				candidateSnapshot.CatalogRevision,
			)
		}
		if !validCatalogSnapshotVersion(candidateSnapshot.SnapshotVersion) {
			if fallbackVersion.Revision == candidateSnapshot.CatalogRevision {
				candidateSnapshot.SnapshotVersion = fallbackVersion
			} else {
				candidateSnapshot.SnapshotVersion = repository.CatalogSnapshotVersion{
					Revision: candidateSnapshot.CatalogRevision,
				}
			}
		}
		return candidateSnapshot, nil
	}

	candidates, candidatesError := s.itemRepo.GetCandidates(ctx, itemTypes, limit)
	if candidatesError != nil {
		return nil, candidatesError
	}
	return &repository.RecommendationCandidateSnapshot{
		SnapshotVersion: fallbackVersion,
		CatalogRevision: fallbackVersion.Revision,
		Items:           candidates,
	}, nil
}

func (s *RecommendationService) cachedRecommendation(
	ctx context.Context,
	cacheKey string,
) *models.RecommendationResponse {
	if s.cache == nil {
		return nil
	}
	var cachedResponse models.RecommendationResponse
	if cacheError := s.cache.Get(ctx, cacheKey, &cachedResponse); cacheError != nil {
		return nil
	}
	if cachedResponse.Items == nil || len(cachedResponse.Items) > models.MaximumRecommendationItems {
		return nil
	}
	return &cachedResponse
}

func (s *RecommendationService) cacheRecommendation(
	ctx context.Context,
	cacheKey string,
	recommendationResponse *models.RecommendationResponse,
	configuredTTL time.Duration,
	snapshotVersion repository.CatalogSnapshotVersion,
) {
	if s.cache == nil {
		return
	}
	cacheTTL := catalogSnapshotCacheTTL(configuredTTL, snapshotVersion)
	if cacheTTL <= 0 {
		return
	}
	if cacheError := s.cache.Set(ctx, cacheKey, recommendationResponse, cacheTTL); cacheError != nil {
		log.Warn().Err(cacheError).Str("operation", "recommendation_cache_set").Msg("recommendation cache write failed")
	}
}

func (s *RecommendationService) authCacheKey(
	cpfHash string,
	req *models.RecommendationRequest,
	catalogRevision string,
) string {
	typeStrs := make([]string, len(req.Types))
	for i, t := range req.Types {
		typeStrs[i] = string(t)
	}
	raw := fmt.Sprintf(
		"rec:auth:%s:%s:%s:%d:%s:%s:%s",
		cpfHash,
		strings.Join(typeStrs, ","),
		req.Context,
		req.Limit,
		catalogRevision,
		s.recommendationRankingVersion(),
		s.recommendationJourneyGraphVersion(),
	)
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("catalogo:rec:auth:%x", h[:])
}

func (s *RecommendationService) anonymousCacheKey(
	req *models.RecommendationRequest,
	catalogRevision string,
) string {
	typeStrs := make([]string, len(req.Types))
	for i, t := range req.Types {
		typeStrs[i] = string(t)
	}
	raw := fmt.Sprintf(
		"rec:anon:%s:%s:%d:%s:%s:%s",
		strings.Join(typeStrs, ","),
		req.Context,
		req.Limit,
		catalogRevision,
		s.recommendationRankingVersion(),
		s.recommendationJourneyGraphVersion(),
	)
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("catalogo:rec:anon:%x", h[:])
}

func (s *RecommendationService) recommendationRankingVersion() string {
	if rankingVersion := strings.TrimSpace(s.rankingVersion); rankingVersion != "" {
		return rankingVersion
	}
	return defaultRecommendationRankingVersion
}

func (s *RecommendationService) recommendationJourneyGraphVersion() string {
	if journeyGraphVersion := strings.TrimSpace(s.journeyGraphVersion); journeyGraphVersion != "" {
		return journeyGraphVersion
	}
	return defaultJourneyGraphVersion
}
