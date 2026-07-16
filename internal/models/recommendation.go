package models

// RecommendationContext indica em qual contexto a recomendação está sendo solicitada.
type RecommendationContext string

const (
	ContextHomepage    RecommendationContext = "homepage"
	ContextAfterSearch RecommendationContext = "after_search"
	ContextProfile     RecommendationContext = "profile"
)

// RecommendationRequest é o request de recomendação.
type RecommendationRequest struct {
	Types       []ItemType            `json:"types"`
	Limit       int                   `json:"limit"`
	Context     RecommendationContext `json:"context"`
	ClusterHint string                `json:"cluster_hint"` // para anônimos: bairro ou faixa_etaria
}

func (r *RecommendationRequest) Normalize() {
	if r.Limit < 1 || r.Limit > 50 {
		r.Limit = 10
	}
	if r.Context == "" {
		r.Context = ContextHomepage
	}
}

// RecommendationResponse é a resposta com itens recomendados.
type RecommendationResponse struct {
	Items       []*RankedItem         `json:"items"`
	Context     RecommendationContext `json:"context"`
	Personalized bool                 `json:"personalized"`
}

// RankedItem é um item do catálogo com score de recomendação e justificativa.
type RankedItem struct {
	ID             string                `json:"id"`
	Type           ItemType              `json:"type"`
	Source         ItemSource            `json:"source"`
	Title          string                `json:"title"`
	ShortDesc      string                `json:"short_desc,omitempty"`
	Organization   string                `json:"organization,omitempty"`
	URL            string                `json:"url,omitempty"`
	ImageURL       string                `json:"image_url,omitempty"`
	Modalidade     string                `json:"modalidade,omitempty"`
	Bairros        []string              `json:"bairros,omitempty"`
	Tags           []string              `json:"tags,omitempty"`
	Score          float64               `json:"score"`
	ScoreBreakdown map[string]float64    `json:"score_breakdown,omitempty"`
}

// ScoringWeights define os pesos das dimensões de recomendação.
// A soma deve ser 1.0.
type ScoringWeights struct {
	Escolaridade  float64
	RendaFamiliar float64
	Localizacao   float64
	Acessibilidade float64
	FaixaEtaria   float64
	TipoItem      float64
}

// DefaultWeights são os pesos padrão do algoritmo v1.
var DefaultWeights = ScoringWeights{
	Escolaridade:  0.25,
	RendaFamiliar: 0.20,
	Localizacao:   0.20,
	Acessibilidade: 0.15,
	FaixaEtaria:   0.10,
	TipoItem:      0.10,
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
