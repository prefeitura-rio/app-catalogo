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

func (r *SearchRequest) Normalize() {
	if r.Page < 1 {
		r.Page = 1
	}
	if r.PerPage < 1 || r.PerPage > 100 {
		r.PerPage = 10
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
	Gratuito    *bool  `json:"gratuito"`
	Turno       string `json:"turno"`

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
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}
