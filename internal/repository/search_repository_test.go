package repository

import (
	"math"
	"strings"
	"testing"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestBuildFilterClausesImplementsEveryPublicSearchFilter(t *testing.T) {
	t.Parallel()

	pcdOnly := true
	searchRequest := &models.SearchRequest{
		Types: []models.ItemType{models.TypeCourse, models.TypeJob},
		Filters: models.SearchFilters{
			Modalidade:        "hibrido",
			Bairro:            "Tijuca",
			Orgao:             "Trabalho",
			Turno:             "noturno",
			RegimeContratacao: "clt",
			ModeloTrabalho:    "remoto",
			PCD:               &pcdOnly,
			CanalAtendimento:  "digital",
			Tema:              "Emprego",
			Segmento:          "Tecnologia",
		},
	}

	filterSQL, filterArguments, nextParameterIndex := buildFilterClauses(searchRequest, 3)
	if len(filterArguments) != 11 {
		t.Fatalf("filter argument count = %d, want 11", len(filterArguments))
	}
	if nextParameterIndex != 14 {
		t.Fatalf("next parameter index = %d, want 14", nextParameterIndex)
	}
	for _, expectedSQLFragment := range []string{
		"ci.type = ANY",
		"unnest(ci.bairros)",
		"ci.organization",
		"ci.modalidade",
		"unnest(ci.tags)",
		"canal_atendimento",
		"source_data->>'turno'",
		"regime_contratacao",
		"modelo_trabalho",
		"acessibilidade_pcd",
		"canais_digitais",
		"canais_presenciais",
	} {
		if !strings.Contains(filterSQL, expectedSQLFragment) {
			t.Errorf("filter SQL does not implement %q:\n%s", expectedSQLFragment, filterSQL)
		}
	}
}

func TestBuildFilterClausesKeepsUnrelatedVerticalsForTypeSpecificFilters(t *testing.T) {
	t.Parallel()

	pcdOnly := true
	filterSQL, _, _ := buildFilterClauses(&models.SearchRequest{
		Filters: models.SearchFilters{
			Turno:             "noturno",
			RegimeContratacao: "clt",
			PCD:               &pcdOnly,
			CanalAtendimento:  "digital",
			Segmento:          "tecnologia",
		},
	}, 1)

	for _, expectedScope := range []string{
		"ci.type <> 'course'",
		"ci.type <> 'job'",
		"ci.type <> 'service'",
		"ci.type <> 'mei_opportunity'",
	} {
		if !strings.Contains(filterSQL, expectedScope) {
			t.Errorf("type-specific filters are not scoped by %q:\n%s", expectedScope, filterSQL)
		}
	}
}

func TestRankedSearchOptionsApplyBoundedDefaults(t *testing.T) {
	t.Parallel()

	defaultOptions := (RankedSearchOptions{}).normalized()
	if defaultOptions.CandidatePoolSize != DefaultCandidatePoolSize {
		t.Fatalf("default candidate pool = %d, want %d", defaultOptions.CandidatePoolSize, DefaultCandidatePoolSize)
	}
	if defaultOptions.Weights != DefaultRetrievalWeights() {
		t.Fatalf("default weights = %#v, want %#v", defaultOptions.Weights, DefaultRetrievalWeights())
	}
	if defaultOptions.MaximumSemanticDistance != DefaultMaximumSemanticDistance {
		t.Fatalf(
			"default maximum semantic distance = %f, want %f",
			defaultOptions.MaximumSemanticDistance,
			DefaultMaximumSemanticDistance,
		)
	}

	boundedOptions := (RankedSearchOptions{CandidatePoolSize: MaximumCandidatePoolSize + 1}).normalized()
	if boundedOptions.CandidatePoolSize != MaximumCandidatePoolSize {
		t.Fatalf("bounded candidate pool = %d, want %d", boundedOptions.CandidatePoolSize, MaximumCandidatePoolSize)
	}
	if boundedOptions.SemanticOverfetchFactor != DefaultSemanticOverfetchFactor ||
		boundedOptions.semanticAliasPoolSize() != MaximumCandidatePoolSize*DefaultSemanticOverfetchFactor {
		t.Fatalf("bounded semantic overfetch = %#v pool=%d", boundedOptions, boundedOptions.semanticAliasPoolSize())
	}
	maximumOverfetchOptions := RankedSearchOptions{
		CandidatePoolSize:       MaximumCandidatePoolSize,
		SemanticOverfetchFactor: MaximumSemanticOverfetchFactor + 1,
	}.normalized()
	if maximumOverfetchOptions.SemanticOverfetchFactor != MaximumSemanticOverfetchFactor ||
		maximumOverfetchOptions.semanticAliasPoolSize() != MaximumSemanticAliasPoolSize {
		t.Fatalf("maximum semantic overfetch = %#v pool=%d", maximumOverfetchOptions, maximumOverfetchOptions.semanticAliasPoolSize())
	}
}

func TestBuildRankedSearchQueryAppliesSemanticDistanceBeforeFusion(t *testing.T) {
	t.Parallel()

	queryStatement, queryArguments := buildRankedSearchQuery(
		&models.SearchRequest{Q: "iptu"},
		RankedSearchOptions{MaximumSemanticDistance: 0.42},
	)
	if !strings.Contains(queryStatement, "WHERE distance <= $17") {
		t.Fatalf("ranked query does not bound semantic candidates:\n%s", queryStatement)
	}
	if len(queryArguments) < 17 || queryArguments[16] != 0.42 {
		t.Fatalf("maximum semantic distance argument = %#v, want 0.42", queryArguments)
	}
	if len(queryArguments) < 18 || queryArguments[17] != DefaultCandidatePoolSize*DefaultSemanticOverfetchFactor {
		t.Fatalf("semantic alias pool argument = %#v", queryArguments)
	}
	if strings.Contains(queryStatement, "%!") {
		t.Fatalf("ranked query contains an unresolved format directive:\n%s", queryStatement)
	}
}

func TestBuildRankedSearchQueryLimitsAndFusesCanonicalEntities(t *testing.T) {
	t.Parallel()

	queryStatement, _ := buildRankedSearchQuery(&models.SearchRequest{Q: "iptu"}, RankedSearchOptions{})
	for _, requiredFragment := range []string{
		"exact_entity_best_alias",
		"full_text_entity_best_alias",
		"trigram_entity_best_alias",
		"semantic_entity_best_alias",
		"hyde_entity_best_alias",
		"GROUP BY canonical_entity_key",
		"WHERE candidate_rank <= $10",
		"WHERE fused_rank <= $10",
		"canonical_representatives",
		"JOIN catalog_items ci ON",
		"LIMIT $18",
	} {
		if !strings.Contains(queryStatement, requiredFragment) {
			t.Errorf("canonical ranked query is missing %q", requiredFragment)
		}
	}
	if strings.Contains(queryStatement, "GROUP BY id") || strings.Contains(queryStatement, "LIMIT $10\n\t\t),\n\t\tfull_text") {
		t.Fatalf("ranked query limits aliases before canonicalization:\n%s", queryStatement)
	}
	if strings.Contains(queryStatement, "candidate_representative_aliases") {
		t.Fatalf("canonical representation is incorrectly limited to aliases that matched retrieval:\n%s", queryStatement)
	}
}

func TestBuildRankedSearchQueryMatchesDistinctOnCollationToInitialOrderBy(t *testing.T) {
	t.Parallel()

	queryStatement, _ := buildRankedSearchQuery(&models.SearchRequest{Q: "iptu"}, RankedSearchOptions{})
	if distinctAliasCount := strings.Count(
		queryStatement,
		`SELECT DISTINCT ON (canonical_entity_key COLLATE "C")`,
	); distinctAliasCount != 5 {
		t.Fatalf("collated alias DISTINCT ON count = %d, want 5:\n%s", distinctAliasCount, queryStatement)
	}
	if !strings.Contains(
		queryStatement,
		`SELECT DISTINCT ON (fused_candidates.canonical_entity_key COLLATE "C")`,
	) {
		t.Fatalf("canonical representative DISTINCT ON does not match its collated ORDER BY:\n%s", queryStatement)
	}
	if strings.Contains(queryStatement, `DISTINCT ON (canonical_entity_key)`) ||
		strings.Contains(queryStatement, `DISTINCT ON (fused_candidates.canonical_entity_key)`) {
		t.Fatalf("ranked query contains an uncollated DISTINCT ON before a collated ORDER BY:\n%s", queryStatement)
	}
}

func TestCanonicalEntityKeySQLExtractsServiceSlugFromEscapedPathOnly(t *testing.T) {
	t.Parallel()

	canonicalExpression := canonicalEntityKeySQL("ci")
	for _, requiredFragment := range []string{
		"split_part(split_part(COALESCE(ci.url, ''), '?', 1), '#', 1)",
		"regexp_replace",
		"//[^/]*",
		"servicos/([^/]+)",
	} {
		if !strings.Contains(canonicalExpression, requiredFragment) {
			t.Errorf("canonical SQL URL fallback is missing %q:\n%s", requiredFragment, canonicalExpression)
		}
	}
	if strings.Contains(canonicalExpression, "servicos/([^/?#]+)") {
		t.Fatalf("canonical SQL can still inspect query or fragment text:\n%s", canonicalExpression)
	}
}

func TestPaginateRepositoryResultsReturnsStableEmptyAndPartialPages(t *testing.T) {
	t.Parallel()

	searchResults := []*SearchResult{{Rank: 1}, {Rank: 2}, {Rank: 3}}
	secondPage := paginateRepositoryResults(searchResults, 2, 2)
	if len(secondPage) != 1 || secondPage[0].Rank != 3 {
		t.Fatalf("second page = %#v, want final result only", secondPage)
	}
	beyondLastPage := paginateRepositoryResults(searchResults, 3, 2)
	if len(beyondLastPage) != 0 {
		t.Fatalf("page beyond result set = %#v, want empty non-nil slice", beyondLastPage)
	}
	extremePage := paginateRepositoryResults(searchResults, math.MaxInt, 100)
	if len(extremePage) != 0 {
		t.Fatalf("extreme page = %#v, want overflow-safe empty slice", extremePage)
	}
}
