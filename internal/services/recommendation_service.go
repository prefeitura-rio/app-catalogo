package services

import (
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/cache"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

type RecommendationService struct {
	itemRepo   *repository.CatalogItemRepository
	cache      *cache.RedisCache
	weights    models.ScoringWeights
	authTTL    time.Duration
	clusterTTL time.Duration
}

func NewRecommendationService(
	itemRepo *repository.CatalogItemRepository,
	cache *cache.RedisCache,
	weights models.ScoringWeights,
	authTTL, clusterTTL time.Duration,
) *RecommendationService {
	return &RecommendationService{
		itemRepo:   itemRepo,
		cache:      cache,
		weights:    weights,
		authTTL:    authTTL,
		clusterTTL: clusterTTL,
	}
}

// Recommend retorna recomendações personalizadas para um cidadão autenticado.
func (s *RecommendationService) Recommend(
	ctx context.Context,
	profile *models.CitizenProfile,
	req *models.RecommendationRequest,
) (*models.RecommendationResponse, error) {
	req.Normalize()

	cacheKey := s.authCacheKey(profile.CPFHash, req)

	var cached models.RecommendationResponse
	if err := s.cache.Get(ctx, cacheKey, &cached); err == nil {
		return &cached, nil
	}

	candidates, err := s.itemRepo.GetCandidates(ctx, req.Types, req.Limit*5)
	if err != nil {
		return nil, fmt.Errorf("recommendation: %w", err)
	}

	ranked := s.rankCandidates(candidates, profile, req)

	resp := &models.RecommendationResponse{
		Items:        ranked[:min(req.Limit, len(ranked))],
		Context:      req.Context,
		Personalized: true,
	}

	_ = s.cache.Set(ctx, cacheKey, resp, s.authTTL)
	return resp, nil
}

// RecommendAnonymous retorna recomendações para usuários não autenticados.
func (s *RecommendationService) RecommendAnonymous(
	ctx context.Context,
	req *models.RecommendationRequest,
) (*models.RecommendationResponse, error) {
	req.Normalize()

	cacheKey := s.clusterCacheKey(req)

	var cached models.RecommendationResponse
	if err := s.cache.Get(ctx, cacheKey, &cached); err == nil {
		return &cached, nil
	}

	// Sem perfil: ranking apenas por tipo (pesos do contexto) + recência
	candidates, err := s.itemRepo.GetCandidates(ctx, req.Types, req.Limit*3)
	if err != nil {
		return nil, fmt.Errorf("recommendation anonymous: %w", err)
	}

	ranked := s.rankCandidates(candidates, nil, req)

	resp := &models.RecommendationResponse{
		Items:        ranked[:min(req.Limit, len(ranked))],
		Context:      req.Context,
		Personalized: false,
	}

	_ = s.cache.Set(ctx, cacheKey, resp, s.clusterTTL)
	return resp, nil
}

// rankCandidates calcula o score de cada item e ordena decrescentemente.
func (s *RecommendationService) rankCandidates(
	items []*models.CatalogItem,
	profile *models.CitizenProfile,
	req *models.RecommendationRequest,
) []*models.RankedItem {
	typeWeights := models.TypeWeightsByContext[req.Context]
	if typeWeights == nil {
		typeWeights = models.TypeWeightsByContext[models.ContextHomepage]
	}

	ranked := make([]*models.RankedItem, 0, len(items))
	for _, item := range items {
		score, breakdown := s.scoreItem(item, profile, typeWeights)
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

	slices.SortFunc(ranked, func(a, b *models.RankedItem) int {
		return cmp.Compare(b.Score, a.Score)
	})
	return ranked
}

// scoreItem calcula o score de um item vs o perfil do cidadão.
// score ∈ [0, 1]. Dimensões e pesos conforme models.ScoringWeights.
func (s *RecommendationService) scoreItem(
	item *models.CatalogItem,
	profile *models.CitizenProfile,
	typeWeights map[models.ItemType]float64,
) (float64, map[string]float64) {
	ta, _ := item.ParseTargetAudience()

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
		"escolaridade":  round2(w.Escolaridade * escolaridadeScore),
		"renda":         round2(w.RendaFamiliar * rendaScore),
		"localizacao":   round2(w.Localizacao * locScore),
		"acessibilidade": round2(w.Acessibilidade * acessibilidadeScore),
		"faixa_etaria":  round2(w.FaixaEtaria * faixaEtariaScore),
		"tipo":          round2(w.TipoItem * tipoScore),
	}

	return round2(total), breakdown
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

func (s *RecommendationService) authCacheKey(cpfHash string, req *models.RecommendationRequest) string {
	typeStrs := make([]string, len(req.Types))
	for i, t := range req.Types {
		typeStrs[i] = string(t)
	}
	raw := fmt.Sprintf("rec:auth:%s:%s:%s:%d", cpfHash, strings.Join(typeStrs, ","), req.Context, req.Limit)
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("catalogo:rec:auth:%x", h[:8])
}

func (s *RecommendationService) clusterCacheKey(req *models.RecommendationRequest) string {
	typeStrs := make([]string, len(req.Types))
	for i, t := range req.Types {
		typeStrs[i] = string(t)
	}
	raw := fmt.Sprintf("rec:anon:%s:%s:%s:%d", req.ClusterHint, strings.Join(typeStrs, ","), req.Context, req.Limit)
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("catalogo:rec:anon:%x", h[:8])
}
