package models

import (
	"errors"
	"strings"
)

// RecommendationContext indica em qual contexto a recomendação está sendo solicitada.
type RecommendationContext string

const (
	ContextHomepage    RecommendationContext = "homepage"
	ContextAfterSearch RecommendationContext = "after_search"
	ContextProfile     RecommendationContext = "profile"
)

// RecommendationContexts são os valores aceitos de `context` (enum
// RecommendationContext do contrato OpenAPI).
var RecommendationContexts = []RecommendationContext{ContextHomepage, ContextAfterSearch, ContextProfile}

// ErrInvalidRecommendationContext indica um valor de `context` fora de
// RecommendationContexts.
var ErrInvalidRecommendationContext = errors.New("context inválido: valores aceitos são " + joinRecommendationContexts(RecommendationContexts))

func joinRecommendationContexts(contexts []RecommendationContext) string {
	strs := make([]string, len(contexts))
	for i, c := range contexts {
		strs[i] = string(c)
	}
	return strings.Join(strs, ", ")
}

// Valid informa se c é um dos valores do enum RecommendationContext
// (comparação exata).
func (c RecommendationContext) Valid() bool {
	for _, known := range RecommendationContexts {
		if c == known {
			return true
		}
	}
	return false
}

// ParseRecommendationContext lê o parâmetro `context`. Ausente ou vazio vira
// homepage (o default do contrato); valor fora do enum devolve
// ErrInvalidRecommendationContext, para que a entrada do cliente nunca chegue
// ao ranking nem seja ecoada na resposta.
func ParseRecommendationContext(raw string) (RecommendationContext, error) {
	if raw == "" {
		return ContextHomepage, nil
	}
	c := RecommendationContext(raw)
	if !c.Valid() {
		return "", ErrInvalidRecommendationContext
	}
	return c, nil
}

// RecommendationRequest é o request de recomendação.
type RecommendationRequest struct {
	Types       []ItemType            `json:"types"`
	Limit       int                   `json:"limit"`
	Context     RecommendationContext `json:"context"`
	ClusterHint string                `json:"cluster_hint"` // para anônimos: bairro ou faixa_etaria
}

const (
	// RecommendationDefaultLimit é o limit quando ausente ou menor que 1.
	RecommendationDefaultLimit = 10
	// RecommendationMaxLimit é o teto de limit do contrato (openapi-v3.json: max 50).
	RecommendationMaxLimit = 50
)

// Normalize aplica os limites do request: limit menor que 1 vira o default e
// acima do máximo é limitado ao máximo; contexto vazio vira homepage.
func (r *RecommendationRequest) Normalize() {
	if r.Limit < 1 {
		r.Limit = RecommendationDefaultLimit
	}
	if r.Limit > RecommendationMaxLimit {
		r.Limit = RecommendationMaxLimit
	}
	if r.Context == "" {
		r.Context = ContextHomepage
	}
}

// RecommendationResponse é a resposta com itens recomendados.
type RecommendationResponse struct {
	Items        []*RankedItem         `json:"items"`
	Context      RecommendationContext `json:"context"`
	Personalized bool                  `json:"personalized"`
}

// RankedItem é um item do catálogo com score de recomendação e justificativa.
type RankedItem struct {
	ID             string             `json:"id"`
	Type           ItemType           `json:"type"`
	Source         ItemSource         `json:"source"`
	Title          string             `json:"title"`
	ShortDesc      string             `json:"short_desc,omitempty"`
	Organization   string             `json:"organization,omitempty"`
	URL            string             `json:"url,omitempty"`
	ImageURL       string             `json:"image_url,omitempty"`
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
