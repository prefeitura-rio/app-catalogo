package models

import (
	"encoding/json"
	"errors"
	"strings"
)

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

// SalarioMinimo é o salário mínimo nacional (R$) usado nas faixas de
// faixa_salarial: R$ 1.621,00 a partir de 1º/01/2026 (Decreto nº 12.797/2025).
const SalarioMinimo = 1621.0

// Valores aceitos em faixa_salarial (openapi.yaml, filterFaixaSalarial).
const (
	FaixaSalarialAte2SM   = "ate-2sm"
	FaixaSalarial2a4SM    = "2-4sm"
	FaixaSalarialAcima4SM = "acima-4sm"
)

// ErrInvalidFaixaSalarial é devolvido quando faixa_salarial não é um dos valores do contrato.
var ErrInvalidFaixaSalarial = errors.New("faixa_salarial inválida: valores aceitos são ate-2sm, 2-4sm, acima-4sm")

// ParseFaixaSalarial normaliza faixa_salarial (sem espaços nas pontas, minúsculas).
// Vazio é "sem filtro"; qualquer valor fora do contrato é erro.
func ParseFaixaSalarial(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return "", nil
	}
	if _, _, ok := FaixaSalarialBounds(v); !ok {
		return "", ErrInvalidFaixaSalarial
	}
	return v, nil
}

// FaixaSalarialBounds devolve o intervalo (min, max] de valor_vaga, em reais,
// de uma faixa salarial. max = 0 quer dizer sem teto. Vaga sem valor (0) não
// entra em faixa nenhuma, por isso min começa acima de zero.
func FaixaSalarialBounds(faixa string) (minExclusive, maxInclusive float64, ok bool) {
	switch faixa {
	case FaixaSalarialAte2SM:
		return 0, 2 * SalarioMinimo, true
	case FaixaSalarial2a4SM:
		return 2 * SalarioMinimo, 4 * SalarioMinimo, true
	case FaixaSalarialAcima4SM:
		return 4 * SalarioMinimo, 0, true
	default:
		return 0, 0, false
	}
}

// NormalizeTurno converte o turno do contrato (matutino, vespertino, noturno)
// para o valor gravado pelo app-go-api (MANHA, TARDE, NOITE, INTEGRAL, LIVRE).
// Um valor desconhecido segue em maiúsculas e não casa com nenhum curso.
func NormalizeTurno(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "":
		return ""
	case "matutino", "manha", "manhã":
		return "MANHA"
	case "vespertino", "tarde":
		return "TARDE"
	case "noturno", "noite":
		return "NOITE"
	case "integral":
		return "INTEGRAL"
	case "livre":
		return "LIVRE"
	default:
		return strings.ToUpper(v)
	}
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
