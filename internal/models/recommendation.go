package models

import (
	"errors"
	"fmt"
	"slices"
)

// DefaultRecommendationLimit applies when callers omit the limit.
const DefaultRecommendationLimit = 10

// MaximumRecommendationItems bounds every recommendation response and cache entry.
const MaximumRecommendationItems = 50

// ErrInvalidRecommendationContext identifies an unsupported recommendation context.
var ErrInvalidRecommendationContext = errors.New("invalid recommendation context")

// ErrInvalidRecommendationItemType identifies an unsupported catalog item type.
var ErrInvalidRecommendationItemType = errors.New("invalid recommendation item type")

// ErrInvalidRecommendationLimit identifies a supplied limit outside the public contract.
var ErrInvalidRecommendationLimit = errors.New("invalid recommendation limit")

// RecommendationContext indica em qual contexto a recomendação está sendo solicitada.
type RecommendationContext string

const (
	ContextHomepage    RecommendationContext = "homepage"
	ContextAfterSearch RecommendationContext = "after_search"
	ContextProfile     RecommendationContext = "profile"
)

// RecommendationRequest é o request de recomendação.
type RecommendationRequest struct {
	Types   []ItemType            `json:"types"`
	Limit   int                   `json:"limit"`
	Context RecommendationContext `json:"context"`
}

func (r *RecommendationRequest) Normalize() error {
	if r == nil {
		return errors.New("recommendation request cannot be nil")
	}
	if r.Limit == 0 {
		r.Limit = DefaultRecommendationLimit
	}
	if r.Limit < 1 || r.Limit > MaximumRecommendationItems {
		return fmt.Errorf("%w: %d", ErrInvalidRecommendationLimit, r.Limit)
	}
	if r.Context == "" {
		r.Context = ContextHomepage
	}
	if !r.Context.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidRecommendationContext, r.Context)
	}
	for _, itemType := range r.Types {
		if _, validItemType := validCatalogItemTypes[itemType]; !validItemType {
			return fmt.Errorf("%w: %q", ErrInvalidRecommendationItemType, itemType)
		}
	}
	canonicalItemTypes := append([]ItemType(nil), r.Types...)
	slices.Sort(canonicalItemTypes)
	r.Types = slices.Compact(canonicalItemTypes)
	return nil
}

func (c RecommendationContext) Valid() bool {
	switch c {
	case ContextHomepage, ContextAfterSearch, ContextProfile:
		return true
	default:
		return false
	}
}

// RecommendationResponse é a resposta com itens recomendados.
type RecommendationResponse struct {
	Items        []*RankedItem         `json:"items" validate:"max=50"`
	Context      RecommendationContext `json:"context"`
	Personalized bool                  `json:"personalized"`
}

// RecommendationErrorResponse carries the support identifier for a failed request.
type RecommendationErrorResponse struct {
	Error string `json:"error"`
	LogID string `json:"log_id" format:"uuid"`
}

// RankedItem é um item do catálogo com score de recomendação e justificativa.
type RankedItem struct {
	ID             string             `json:"id" format:"uuid"`
	Type           ItemType           `json:"type"`
	Source         ItemSource         `json:"source"`
	Title          string             `json:"title"`
	ShortDesc      string             `json:"short_desc,omitempty"`
	Organization   string             `json:"organization,omitempty"`
	URL            string             `json:"url,omitempty" format:"uri"`
	ImageURL       string             `json:"image_url,omitempty" format:"uri"`
	Modalidade     string             `json:"modalidade,omitempty"`
	Bairros        []string           `json:"bairros,omitempty"`
	Tags           []string           `json:"tags,omitempty"`
	Score          float64            `json:"score"`
	ScoreBreakdown map[string]float64 `json:"score_breakdown,omitempty"`
}

// ScoringWeights define os pesos das dimensões de recomendação.
// A soma deve ser 1.0.
type ScoringWeights struct {
	Escolaridade   float64
	RendaFamiliar  float64
	Localizacao    float64
	Acessibilidade float64
	FaixaEtaria    float64
	TipoItem       float64
}

// DefaultWeights são os pesos padrão do algoritmo v1.
var DefaultWeights = ScoringWeights{
	Escolaridade:   0.25,
	RendaFamiliar:  0.20,
	Localizacao:    0.20,
	Acessibilidade: 0.15,
	FaixaEtaria:    0.10,
	TipoItem:       0.10,
}

// TypeWeightsByContext define o peso de cada tipo de item por contexto.
var TypeWeightsByContext = map[RecommendationContext]map[ItemType]float64{
	ContextHomepage: {
		TypeService:        0.40,
		TypeCourse:         0.30,
		TypeJob:            0.20,
		TypeMEIOpportunity: 0.10,
	},
	ContextAfterSearch: {
		TypeService:        0.25,
		TypeCourse:         0.25,
		TypeJob:            0.25,
		TypeMEIOpportunity: 0.25,
	},
	ContextProfile: {
		TypeService:        0.20,
		TypeCourse:         0.35,
		TypeJob:            0.35,
		TypeMEIOpportunity: 0.10,
	},
}
