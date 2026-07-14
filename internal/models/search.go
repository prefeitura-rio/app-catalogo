package models

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	DefaultSearchPage        = 1
	MaxSearchPage            = 1000
	DefaultSearchPerPage     = 10
	MaxSearchPerPage         = 100
	MaxSearchQueryRunes      = 256
	MaxSearchFilterRunes     = 100
	MaxSearchFacetValues     = 30
	MaxSearchFacetLabelRunes = 500
	SearchFacetVersion       = "catalog-facets-v1"
)

// SearchPipeline identifies the stages that materially produced the final
// result order. Cache transport is intentionally absent: cached responses keep
// the pipeline that originally ranked them.
type SearchPipeline string

const (
	SearchPipelineBrowse             SearchPipeline = "browse"
	SearchPipelineLexical            SearchPipeline = "lexical"
	SearchPipelineLexicalReranked    SearchPipeline = "lexical_reranked"
	SearchPipelineHybrid             SearchPipeline = "hybrid"
	SearchPipelineHybridReranked     SearchPipeline = "hybrid_reranked"
	SearchPipelineHybridHyDE         SearchPipeline = "hybrid_hyde"
	SearchPipelineHybridHyDEReranked SearchPipeline = "hybrid_hyde_reranked"
)

// Valid reports whether the facet population is part of the public contract.
func (scope SearchFacetScope) Valid() bool {
	switch scope {
	case SearchFacetScopeCatalogMatches,
		SearchFacetScopeRetrievalCandidates,
		SearchFacetScopeUnavailable:
		return true
	default:
		return false
	}
}

// Valid reports whether the pipeline is part of the public provenance contract.
func (pipeline SearchPipeline) Valid() bool {
	switch pipeline {
	case SearchPipelineBrowse,
		SearchPipelineLexical,
		SearchPipelineLexicalReranked,
		SearchPipelineHybrid,
		SearchPipelineHybridReranked,
		SearchPipelineHybridHyDE,
		SearchPipelineHybridHyDEReranked:
		return true
	default:
		return false
	}
}

var validSearchItemTypes = map[ItemType]struct{}{
	TypeService:        {},
	TypeCourse:         {},
	TypeJob:            {},
	TypeMEIOpportunity: {},
}

var (
	validSearchModalities    = stringSet("presencial", "digital", "hibrido")
	validSearchShifts        = stringSet("matutino", "vespertino", "noturno")
	validSearchHiringRegimes = stringSet("clt", "pj", "temporario")
	validSearchWorkModels    = stringSet("presencial", "remoto", "hibrido")
	validSearchChannels      = stringSet("presencial", "digital", "telefone")
)

// SearchRequest é o corpo da requisição de busca.
type SearchRequest struct {
	Q         string        `json:"q"`
	ExpandedQ string        `json:"-"`
	Types     []ItemType    `json:"types"`
	Filters   SearchFilters `json:"filters"`
	Page      int           `json:"page"`
	PerPage   int           `json:"per_page"`
}

// SearchRequestBody is the JSON transport for search. Filters remain flat so
// GET and POST expose the same public contract while POST keeps free text out
// of request URLs. On semantic cache misses, the normalized Q may be sent
// server-side to Google Gemini for an embedding and optional HyDE generation;
// callers must disclose that boundary and discourage personal information.
type SearchRequestBody struct {
	Q                 string     `json:"q,omitempty" maxLength:"256"`
	Types             []ItemType `json:"types,omitempty" validate:"unique"`
	Page              *int       `json:"page,omitempty" minimum:"1" maximum:"1000" default:"1"`
	PerPage           *int       `json:"per_page,omitempty" minimum:"1" maximum:"100" default:"10"`
	Modalidade        string     `json:"modalidade,omitempty" enums:"presencial,digital,hibrido"`
	Bairro            string     `json:"bairro,omitempty" maxLength:"100"`
	Orgao             string     `json:"orgao,omitempty" maxLength:"100"`
	Turno             string     `json:"turno,omitempty" enums:"matutino,vespertino,noturno"`
	RegimeContratacao string     `json:"regime_contratacao,omitempty" enums:"clt,pj,temporario"`
	ModeloTrabalho    string     `json:"modelo_trabalho,omitempty" enums:"presencial,remoto,hibrido"`
	PCD               *bool      `json:"pcd,omitempty"`
	CanalAtendimento  string     `json:"canal_atendimento,omitempty" enums:"presencial,digital,telefone"`
	Tema              string     `json:"tema,omitempty" maxLength:"100"`
	Segmento          string     `json:"segmento,omitempty" maxLength:"100"`
}

// ToSearchRequest applies only omission defaults. Normalize and Validate must
// still run at the HTTP boundary so explicit invalid values remain invalid.
func (requestBody SearchRequestBody) ToSearchRequest() SearchRequest {
	page := DefaultSearchPage
	if requestBody.Page != nil {
		page = *requestBody.Page
	}
	perPage := DefaultSearchPerPage
	if requestBody.PerPage != nil {
		perPage = *requestBody.PerPage
	}
	return SearchRequest{
		Q:       requestBody.Q,
		Types:   requestBody.Types,
		Page:    page,
		PerPage: perPage,
		Filters: SearchFilters{
			Modalidade:        requestBody.Modalidade,
			Bairro:            requestBody.Bairro,
			Orgao:             requestBody.Orgao,
			Turno:             requestBody.Turno,
			RegimeContratacao: requestBody.RegimeContratacao,
			ModeloTrabalho:    requestBody.ModeloTrabalho,
			PCD:               requestBody.PCD,
			CanalAtendimento:  requestBody.CanalAtendimento,
			Tema:              requestBody.Tema,
			Segmento:          requestBody.Segmento,
		},
	}
}

// Normalize canonicalizes a valid request without expanding its query.
// Explicit invalid pagination values remain invalid for Validate to reject.
func (r *SearchRequest) Normalize() {
	if r.Page == 0 {
		r.Page = DefaultSearchPage
	}
	if r.PerPage == 0 {
		r.PerPage = DefaultSearchPerPage
	}

	r.Q = canonicalSearchText(r.Q)
	r.ExpandedQ = canonicalSearchText(r.ExpandedQ)
	r.Types = canonicalSearchTypes(r.Types)
	r.Filters.normalize()
}

// Validate checks the canonical request before it reaches a repository.
func (r SearchRequest) Validate() error {
	if !utf8.ValidString(r.Q) {
		return fmt.Errorf("q deve ser UTF-8 válido")
	}
	if utf8.RuneCountInString(r.Q) > MaxSearchQueryRunes {
		return fmt.Errorf("q excede o tamanho máximo permitido")
	}
	if r.Page < DefaultSearchPage || r.Page > MaxSearchPage {
		return fmt.Errorf("page deve estar entre %d e %d", DefaultSearchPage, MaxSearchPage)
	}
	if r.PerPage < 1 || r.PerPage > MaxSearchPerPage {
		return fmt.Errorf("per_page deve estar entre 1 e %d", MaxSearchPerPage)
	}

	for _, itemType := range r.Types {
		if _, valid := validSearchItemTypes[itemType]; !valid {
			return fmt.Errorf("types contém valor inválido: %q", itemType)
		}
	}

	for _, enum := range []struct {
		name    string
		value   string
		allowed map[string]struct{}
	}{
		{name: "modalidade", value: r.Filters.Modalidade, allowed: validSearchModalities},
		{name: "turno", value: r.Filters.Turno, allowed: validSearchShifts},
		{name: "regime_contratacao", value: r.Filters.RegimeContratacao, allowed: validSearchHiringRegimes},
		{name: "modelo_trabalho", value: r.Filters.ModeloTrabalho, allowed: validSearchWorkModels},
		{name: "canal_atendimento", value: r.Filters.CanalAtendimento, allowed: validSearchChannels},
	} {
		if enum.value == "" {
			continue
		}
		if _, valid := enum.allowed[enum.value]; !valid {
			return fmt.Errorf("%s contém valor inválido: %q", enum.name, enum.value)
		}
	}
	if r.Filters.Gratuito != nil {
		return fmt.Errorf("gratuito ainda não é suportado pela fonte de cursos")
	}
	if r.Filters.FaixaSalarial != "" {
		return fmt.Errorf("faixa_salarial ainda não é suportada pela fonte de vagas")
	}

	for _, textFilter := range []struct {
		name  string
		value string
	}{
		{name: "modalidade", value: r.Filters.Modalidade},
		{name: "bairro", value: r.Filters.Bairro},
		{name: "orgao", value: r.Filters.Orgao},
		{name: "turno", value: r.Filters.Turno},
		{name: "regime_contratacao", value: r.Filters.RegimeContratacao},
		{name: "modelo_trabalho", value: r.Filters.ModeloTrabalho},
		{name: "faixa_salarial", value: r.Filters.FaixaSalarial},
		{name: "canal_atendimento", value: r.Filters.CanalAtendimento},
		{name: "tema", value: r.Filters.Tema},
		{name: "segmento", value: r.Filters.Segmento},
	} {
		if !utf8.ValidString(textFilter.value) {
			return fmt.Errorf("%s deve ser UTF-8 válido", textFilter.name)
		}
		if utf8.RuneCountInString(textFilter.value) > MaxSearchFilterRunes {
			return fmt.Errorf("%s excede o tamanho máximo permitido", textFilter.name)
		}
	}

	return nil
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

func (f *SearchFilters) normalize() {
	f.Modalidade = canonicalSearchEnum(f.Modalidade)
	f.Bairro = canonicalSearchText(f.Bairro)
	f.Orgao = canonicalSearchText(f.Orgao)
	f.Turno = canonicalSearchEnum(f.Turno)
	f.RegimeContratacao = canonicalSearchEnum(f.RegimeContratacao)
	f.ModeloTrabalho = canonicalSearchEnum(f.ModeloTrabalho)
	f.FaixaSalarial = canonicalSearchEnum(f.FaixaSalarial)
	f.CanalAtendimento = canonicalSearchEnum(f.CanalAtendimento)
	f.Tema = canonicalSearchText(f.Tema)
	f.Segmento = canonicalSearchText(f.Segmento)
}

func canonicalSearchTypes(itemTypes []ItemType) []ItemType {
	if len(itemTypes) == 0 {
		return nil
	}

	uniqueItemTypes := make(map[ItemType]struct{}, len(itemTypes))
	for _, itemType := range itemTypes {
		canonicalItemType := ItemType(canonicalSearchEnum(string(itemType)))
		if canonicalItemType != "" {
			uniqueItemTypes[canonicalItemType] = struct{}{}
		}
	}

	canonicalItemTypes := make([]ItemType, 0, len(uniqueItemTypes))
	for itemType := range uniqueItemTypes {
		canonicalItemTypes = append(canonicalItemTypes, itemType)
	}
	slices.Sort(canonicalItemTypes)
	return canonicalItemTypes
}

func canonicalSearchText(searchText string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(searchText)), " ")
}

func canonicalSearchEnum(enumValue string) string {
	normalizedValue := norm.NFD.String(strings.ToLower(canonicalSearchText(enumValue)))
	var canonicalValue strings.Builder
	canonicalValue.Grow(len(normalizedValue))
	for _, character := range normalizedValue {
		if !unicode.Is(unicode.Mn, character) {
			canonicalValue.WriteRune(character)
		}
	}
	return canonicalValue.String()
}

func stringSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

// SearchRetrievalWeights is the public, immutable weight set used by fusion.
type SearchRetrievalWeights struct {
	Exact    float64 `json:"exact"`
	FullText float64 `json:"full_text"`
	Trigram  float64 `json:"trigram"`
	Semantic float64 `json:"semantic"`
	HyDE     float64 `json:"hyde"`
	Facilita float64 `json:"facilita"`
}

// SearchSourceStatus is the bounded outcome of one optional candidate source.
type SearchSourceStatus string

const (
	SearchSourceStatusNotApplicable SearchSourceStatus = "not_applicable"
	SearchSourceStatusUnavailable   SearchSourceStatus = "unavailable"
	SearchSourceStatusNoEffect      SearchSourceStatus = "no_effect"
	SearchSourceStatusApplied       SearchSourceStatus = "applied"
)

func (sourceStatus SearchSourceStatus) Valid() bool {
	switch sourceStatus {
	case SearchSourceStatusNotApplicable, SearchSourceStatusUnavailable, SearchSourceStatusNoEffect, SearchSourceStatusApplied:
		return true
	default:
		return false
	}
}

// SearchSourceFailure classifies bounded external failures without exposing details.
type SearchSourceFailure string

const (
	SearchSourceFailureTimeout         SearchSourceFailure = "timeout"
	SearchSourceFailureTransport       SearchSourceFailure = "transport"
	SearchSourceFailureRejected        SearchSourceFailure = "rejected"
	SearchSourceFailureInvalidContract SearchSourceFailure = "invalid_contract"
)

// SearchExternalRetrieverDescriptor identifies the exact remote ranking inputs.
type SearchExternalRetrieverDescriptor struct {
	SchemaVersion         string `json:"schema_version"`
	CatalogRevision       string `json:"catalog_revision"`
	RetrievalVersion      string `json:"retrieval_version"`
	QueryExpansionVersion string `json:"query_expansion_version"`
	RankerVersion         string `json:"ranker_version"`
}

// SearchSourceDiagnostic reports a bounded, query-free source outcome.
type SearchSourceDiagnostic struct {
	Status                SearchSourceStatus                 `json:"status" enums:"not_applicable,unavailable,no_effect,applied"`
	Failure               SearchSourceFailure                `json:"failure,omitempty" enums:"timeout,transport,rejected,invalid_contract"`
	Provenance            *SearchExternalRetrieverDescriptor `json:"provenance,omitempty"`
	LatencyMilliseconds   int64                              `json:"latency_ms"`
	CandidatesReceived    int                                `json:"candidates_received"`
	EligibleContributions int                                `json:"eligible_contributions"`
}

// SearchSources contains diagnostics for every optional candidate authority.
type SearchSources struct {
	Facilita SearchSourceDiagnostic `json:"facilita"`
}

// HyDEGenerationMetadata is the immutable non-secret generation contract used
// to fingerprint hypothetical-document expansion.
type HyDEGenerationMetadata struct {
	Model             string
	PromptVersion     string
	PromptSHA256      string
	Temperature       float32
	Seed              int32
	CandidateCount    int32
	MaxOutputTokens   int32
	ResponseMIMEType  string
	DeterminismPolicy string
}

// SearchRankerDescriptor contains every explicitly versioned component that
// can change retrieval or final ordering. It is safe to expose publicly and
// must never contain provider credentials or internal URLs.
type SearchRankerDescriptor struct {
	SchemaVersion           string                             `json:"schema_version"`
	BaseVersion             string                             `json:"base_version"`
	RetrievalVersion        string                             `json:"retrieval_version"`
	QueryExpansionVersion   string                             `json:"query_expansion_version"`
	DeduplicationVersion    string                             `json:"deduplication_version"`
	CandidatePoolSize       int                                `json:"candidate_pool_size"`
	SemanticOverfetchFactor int                                `json:"semantic_overfetch_factor"`
	TrigramThreshold        float64                            `json:"trigram_threshold"`
	MaximumSemanticDistance float64                            `json:"maximum_semantic_distance"`
	ReciprocalRankK         float64                            `json:"reciprocal_rank_k"`
	Weights                 SearchRetrievalWeights             `json:"weights"`
	SemanticEnabled         bool                               `json:"semantic_enabled"`
	Embedding               *EmbeddingMetadata                 `json:"embedding,omitempty"`
	HyDEEnabled             bool                               `json:"hyde_enabled"`
	HyDEModel               string                             `json:"hyde_model,omitempty"`
	HyDEPromptVersion       string                             `json:"hyde_prompt_version,omitempty"`
	HyDEPromptSHA256        string                             `json:"hyde_prompt_sha256,omitempty"`
	HyDETemperature         *float32                           `json:"hyde_temperature,omitempty"`
	HyDESeed                *int32                             `json:"hyde_seed,omitempty"`
	HyDECandidateCount      *int32                             `json:"hyde_candidate_count,omitempty"`
	HyDEMaxOutputTokens     *int32                             `json:"hyde_max_output_tokens,omitempty"`
	HyDEResponseMIMEType    string                             `json:"hyde_response_mime_type,omitempty"`
	HyDEDeterminismPolicy   string                             `json:"hyde_determinism_policy,omitempty"`
	RerankerEnabled         bool                               `json:"reranker_enabled"`
	RerankerVersion         string                             `json:"reranker_version,omitempty"`
	RerankerCandidateLimit  int                                `json:"reranker_candidate_limit,omitempty"`
	Facilita                *SearchExternalRetrieverDescriptor `json:"facilita,omitempty"`
}

// SearchFacetScope makes the population behind facet counts explicit.
type SearchFacetScope string

const (
	SearchFacetScopeCatalogMatches      SearchFacetScope = "catalog_matches"
	SearchFacetScopeRetrievalCandidates SearchFacetScope = "retrieval_candidates"
	SearchFacetScopeUnavailable         SearchFacetScope = "unavailable"
)

// SearchFacetValue is a bounded, deterministic filter option. Value is the
// canonical request value while Label preserves a public display spelling.
type SearchFacetValue struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// SearchFacets contains only dimensions backed by normalized catalog fields.
// Vertical-specific source JSON is deliberately excluded until each source
// publishes a stable schema for those dimensions.
type SearchFacets struct {
	Version       string             `json:"version"`
	Scope         SearchFacetScope   `json:"scope"`
	Types         []SearchFacetValue `json:"types"`
	Modalidades   []SearchFacetValue `json:"modalidades"`
	Bairros       []SearchFacetValue `json:"bairros"`
	Organizations []SearchFacetValue `json:"organizations"`
}

// SearchResponse é a resposta paginada da busca.
type SearchResponse struct {
	SearchID          string                 `json:"search_id"`
	RankerVersion     string                 `json:"ranker_version"`
	RankerDescriptor  SearchRankerDescriptor `json:"ranker_descriptor"`
	CatalogRevision   string                 `json:"catalog_revision"`
	EffectivePipeline SearchPipeline         `json:"effective_pipeline"`
	Degraded          bool                   `json:"degraded"`
	Sources           SearchSources          `json:"sources"`
	Total             int                    `json:"total"`
	Page              int                    `json:"page"`
	PerPage           int                    `json:"per_page"`
	Facets            SearchFacets           `json:"facets"`
	Items             []*SearchItem          `json:"items"`
}

// SearchItem é um item nos resultados de busca.
type SearchItem struct {
	ID             string          `json:"id"`
	CanonicalID    string          `json:"canonical_id"`
	Type           ItemType        `json:"type"`
	Source         ItemSource      `json:"source"`
	SourceID       string          `json:"source_id"`
	Slug           string          `json:"slug,omitempty"`
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
