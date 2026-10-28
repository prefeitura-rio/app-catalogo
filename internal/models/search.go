package models

import "encoding/json"

// SearchRequest é o corpo da requisição de busca.
type SearchRequest struct {
	Q       string        `json:"q"`
	Types   []ItemType    `json:"types"`
	Filters SearchFilters `json:"filters"`
	Page    int           `json:"page"`
	PerPage int           `json:"per_page"`
}

const (
	// SearchDefaultPerPage é o tamanho de página quando per_page é ausente ou menor que 1.
	SearchDefaultPerPage = 10
	// SearchMaxPerPage é o teto de per_page do contrato (openapi.yaml, searchPerPage).
	SearchMaxPerPage = 100
)

// Normalize aplica os limites de paginação: page mínimo 1; per_page menor que 1
// vira o default e acima do máximo é limitado ao máximo.
func (r *SearchRequest) Normalize() {
	if r.Page < 1 {
		r.Page = 1
	}
	if r.PerPage < 1 {
		r.PerPage = SearchDefaultPerPage
	}
	if r.PerPage > SearchMaxPerPage {
		r.PerPage = SearchMaxPerPage
	}
}

// SearchFilters agrupa todos os filtros possíveis.
// Filtros não aplicáveis ao tipo são ignorados.
type SearchFilters struct {
	// Comuns
	Modalidade string `json:"modalidade"`
	Bairro     string `json:"bairro"`
	Orgao      string `json:"orgao"`

	// Cursos
	Gratuito *bool  `json:"gratuito"`
	Turno    string `json:"turno"`

	// Vagas
	RegimeContratacao string `json:"regime_contratacao"`
	ModeloTrabalho    string `json:"modelo_trabalho"`
	PCD               *bool  `json:"pcd"`
	FaixaSalarial     string `json:"faixa_salarial"`

	// Serviços (Carta)
	CanalAtendimento string `json:"canal_atendimento"`
	Tema             string `json:"tema"`

	// MEI
	Segmento string `json:"segmento"`
}

// SearchResponse é a resposta paginada da busca.
type SearchResponse struct {
	Total   int           `json:"total"`
	Page    int           `json:"page"`
	PerPage int           `json:"per_page"`
	Items   []*SearchItem `json:"items"`
}

// SearchItem é um item nos resultados de busca.
type SearchItem struct {
	ID             string          `json:"id"`
	Type           ItemType        `json:"type"`
	Source         ItemSource      `json:"source"`
	Title          string          `json:"title"`
	ShortDesc      string          `json:"short_desc,omitempty"`
	Organization   string          `json:"organization,omitempty"`
	URL            string          `json:"url,omitempty"`
	ImageURL       string          `json:"image_url,omitempty"`
	Modalidade     string          `json:"modalidade,omitempty"`
	Bairros        []string        `json:"bairros,omitempty"`
	Tags           []string        `json:"tags,omitempty"`
	RelevanceScore float64         `json:"relevance_score"`
	Highlights     []string        `json:"highlights,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty" swaggertype:"object"`
}
