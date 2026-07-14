package repository

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

const (
	RetrievalVersion               = "postgres-canonical-weighted-rrf-v4"
	DefaultCandidatePoolSize       = 40
	MaximumCandidatePoolSize       = 200
	DefaultSemanticOverfetchFactor = 4
	MaximumSemanticOverfetchFactor = 10
	MaximumSemanticAliasPoolSize   = 1000
	DefaultTrigramThreshold        = 0.18
	DefaultMaximumSemanticDistance = 1.0
	MaximumCosineDistance          = 2.0
	DefaultReciprocalRankK         = 60.0
)

type SearchRepository struct {
	databasePool *pgxpool.Pool
}

func NewSearchRepository(databasePool *pgxpool.Pool) *SearchRepository {
	return &SearchRepository{databasePool: databasePool}
}

// CatalogSnapshot returns the content and temporal eligibility version visible
// to a new PostgreSQL statement.
func (repository *SearchRepository) CatalogSnapshot(
	searchContext context.Context,
) (CatalogSnapshotVersion, error) {
	return readCatalogSnapshotVersion(searchContext, repository.databasePool)
}

// CatalogRevision preserves the legacy provider contract while returning the
// complete content-and-eligibility revision.
func (repository *SearchRepository) CatalogRevision(searchContext context.Context) (string, error) {
	catalogSnapshot, snapshotError := repository.CatalogSnapshot(searchContext)
	if snapshotError != nil {
		return "", snapshotError
	}
	return catalogSnapshot.Revision, nil
}

type SearchResult struct {
	Item                *models.CatalogItem
	Rank                float64
	Headline            string
	FacilitaContributed bool
}

// BrowseSnapshot is a self-consistent catalog browse read. Results, total,
// facets, and revision are all produced by one repeatable-read transaction.
type BrowseSnapshot struct {
	SnapshotVersion CatalogSnapshotVersion
	CatalogRevision string
	Results         []*SearchResult
	Total           int
	Facets          models.SearchFacets
}

// RankedSearchSnapshot binds ranked candidates to the repeatable-read catalog
// snapshot that produced them.
type RankedSearchSnapshot struct {
	SnapshotVersion CatalogSnapshotVersion
	CatalogRevision string
	Results         []*SearchResult
	Total           int
}

type RetrievalWeights struct {
	Exact    float64
	FullText float64
	Trigram  float64
	Semantic float64
	HyDE     float64
	Facilita float64
}

func DefaultRetrievalWeights() RetrievalWeights {
	return RetrievalWeights{
		Exact:    3.0,
		FullText: 1.0,
		Trigram:  1.0,
		Semantic: 1.0,
		HyDE:     0.5,
		Facilita: 0,
	}
}

type RankedServiceCandidate struct {
	Slug string
	Rank int
}

type RankedSearchOptions struct {
	QueryEmbedding           string
	HyDEEmbedding            string
	EmbeddingModel           string
	EmbeddingModelVersion    string
	EmbeddingDimensions      int
	EmbeddingTaskType        string
	EmbeddingDocumentVersion string
	MaximumSemanticDistance  float64
	CandidatePoolSize        int
	SemanticOverfetchFactor  int
	Weights                  RetrievalWeights
	FacilitaCandidates       []RankedServiceCandidate
}

func (options RankedSearchOptions) normalized() RankedSearchOptions {
	if options.CandidatePoolSize < 1 {
		options.CandidatePoolSize = DefaultCandidatePoolSize
	}
	if options.CandidatePoolSize > MaximumCandidatePoolSize {
		options.CandidatePoolSize = MaximumCandidatePoolSize
	}
	if options.SemanticOverfetchFactor < 1 {
		options.SemanticOverfetchFactor = DefaultSemanticOverfetchFactor
	}
	if options.SemanticOverfetchFactor > MaximumSemanticOverfetchFactor {
		options.SemanticOverfetchFactor = MaximumSemanticOverfetchFactor
	}
	if options.Weights == (RetrievalWeights{}) {
		options.Weights = DefaultRetrievalWeights()
	}
	if options.MaximumSemanticDistance <= 0 || options.MaximumSemanticDistance > MaximumCosineDistance {
		options.MaximumSemanticDistance = DefaultMaximumSemanticDistance
	}
	options.FacilitaCandidates = normalizeRankedServiceCandidates(
		options.FacilitaCandidates,
		options.CandidatePoolSize,
	)
	return options
}

var rankedServiceCandidateSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func normalizeRankedServiceCandidates(
	candidates []RankedServiceCandidate,
	maximumCandidates int,
) []RankedServiceCandidate {
	bestRankBySlug := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		canonicalSlug := strings.ToLower(strings.TrimSpace(candidate.Slug))
		if candidate.Rank < 1 || !rankedServiceCandidateSlugPattern.MatchString(canonicalSlug) {
			continue
		}
		if previousRank, found := bestRankBySlug[canonicalSlug]; !found || candidate.Rank < previousRank {
			bestRankBySlug[canonicalSlug] = candidate.Rank
		}
	}
	normalizedCandidates := make([]RankedServiceCandidate, 0, len(bestRankBySlug))
	for slug, rank := range bestRankBySlug {
		normalizedCandidates = append(normalizedCandidates, RankedServiceCandidate{Slug: slug, Rank: rank})
	}
	sort.Slice(normalizedCandidates, func(leftIndex int, rightIndex int) bool {
		leftCandidate := normalizedCandidates[leftIndex]
		rightCandidate := normalizedCandidates[rightIndex]
		if leftCandidate.Rank != rightCandidate.Rank {
			return leftCandidate.Rank < rightCandidate.Rank
		}
		return leftCandidate.Slug < rightCandidate.Slug
	})
	if len(normalizedCandidates) > maximumCandidates {
		normalizedCandidates = normalizedCandidates[:maximumCandidates]
	}
	return normalizedCandidates
}

func (options RankedSearchOptions) facilitaCandidateArrays() ([]string, []int) {
	normalizedCandidates := options.normalized().FacilitaCandidates
	slugs := make([]string, len(normalizedCandidates))
	ranks := make([]int, len(normalizedCandidates))
	for candidateIndex, candidate := range normalizedCandidates {
		slugs[candidateIndex] = candidate.Slug
		ranks[candidateIndex] = candidate.Rank
	}
	return slugs, ranks
}

func (options RankedSearchOptions) semanticAliasPoolSize() int {
	normalizedOptions := options.normalized()
	if normalizedOptions.CandidatePoolSize > MaximumSemanticAliasPoolSize/normalizedOptions.SemanticOverfetchFactor {
		return MaximumSemanticAliasPoolSize
	}
	return min(
		normalizedOptions.CandidatePoolSize*normalizedOptions.SemanticOverfetchFactor,
		MaximumSemanticAliasPoolSize,
	)
}

// buildFilterClauses creates parameterized catalog filters shared by every retriever.
func buildFilterClauses(searchRequest *models.SearchRequest, firstParameterIndex int) (string, []any, int) {
	filterClauses := make([]string, 0, 12)
	filterArguments := make([]any, 0, 12)
	nextParameterIndex := firstParameterIndex

	appendFilter := func(clause string, argument any) {
		filterClauses = append(filterClauses, fmt.Sprintf(clause, nextParameterIndex))
		filterArguments = append(filterArguments, argument)
		nextParameterIndex++
	}

	if len(searchRequest.Types) > 0 {
		itemTypes := make([]string, len(searchRequest.Types))
		for itemTypeIndex, itemType := range searchRequest.Types {
			itemTypes[itemTypeIndex] = string(itemType)
		}
		appendFilter("ci.type = ANY($%d::item_type[])", itemTypes)
	}
	if searchRequest.Filters.Bairro != "" {
		appendFilter(`EXISTS (
			SELECT 1 FROM unnest(ci.bairros) AS catalog_bairro
			WHERE immutable_unaccent(lower(btrim(regexp_replace(catalog_bairro, '[[:space:]]+', ' ', 'g'))))
				= immutable_unaccent(lower(btrim(regexp_replace($%d, '[[:space:]]+', ' ', 'g'))))
		)`, searchRequest.Filters.Bairro)
	}
	if searchRequest.Filters.Orgao != "" {
		appendFilter(`strpos(
			immutable_unaccent(lower(btrim(regexp_replace(COALESCE(ci.organization, ''), '[[:space:]]+', ' ', 'g')))),
			immutable_unaccent(lower(btrim(regexp_replace($%d, '[[:space:]]+', ' ', 'g'))))
		) > 0`, searchRequest.Filters.Orgao)
	}
	if searchRequest.Filters.Modalidade != "" {
		appendFilter(`CASE
			WHEN immutable_unaccent(lower(btrim(regexp_replace(COALESCE(ci.modalidade, ''), '[[:space:]]+', ' ', 'g'))))
				IN ('presencial', 'presencialmente', 'in loco', 'local', 'onsite', 'on-site') THEN 'presencial'
			WHEN immutable_unaccent(lower(btrim(regexp_replace(COALESCE(ci.modalidade, ''), '[[:space:]]+', ' ', 'g'))))
				IN ('digital', 'online', 'remoto', 'remota', 'ead', 'virtual', 'a distancia') THEN 'digital'
			WHEN immutable_unaccent(lower(btrim(regexp_replace(COALESCE(ci.modalidade, ''), '[[:space:]]+', ' ', 'g'))))
				IN ('hibrido', 'hybrid', 'misto', 'mista', 'semipresencial') THEN 'hibrido'
			ELSE NULL
		END = $%d`, searchRequest.Filters.Modalidade)
	}
	if searchRequest.Filters.Tema != "" {
		appendFilter(`(ci.type <> 'service' OR EXISTS (
			SELECT 1 FROM unnest(ci.tags) AS catalog_tag
			WHERE immutable_unaccent(lower(catalog_tag)) = immutable_unaccent(lower($%d))
		))`, searchRequest.Filters.Tema)
	}
	if searchRequest.Filters.CanalAtendimento != "" {
		appendFilter(`(ci.type <> 'service' OR CASE $%d
			WHEN 'digital' THEN
				immutable_unaccent(lower(COALESCE(ci.modalidade, ''))) IN ('digital', 'hibrido', 'hybrid', 'online')
				OR COALESCE(ci.source_data->'canais_digitais', '[]'::jsonb) <> '[]'::jsonb
				OR immutable_unaccent(lower(COALESCE(ci.source_data->>'canal_atendimento', ci.source_data->>'canal', ci.source_data->>'Channel__c', ''))) IN ('digital', 'hibrido', 'hybrid', 'online')
			WHEN 'presencial' THEN
				immutable_unaccent(lower(COALESCE(ci.modalidade, ''))) IN ('presencial', 'hibrido', 'hybrid')
				OR COALESCE(ci.source_data->'canais_presenciais', '[]'::jsonb) <> '[]'::jsonb
				OR immutable_unaccent(lower(COALESCE(ci.source_data->>'canal_atendimento', ci.source_data->>'canal', ci.source_data->>'Channel__c', ''))) IN ('presencial', 'hibrido', 'hybrid')
			WHEN 'telefone' THEN
				immutable_unaccent(lower(COALESCE(ci.modalidade, ''))) = 'telefone'
				OR strpos(immutable_unaccent(lower(
					COALESCE(ci.source_data->>'canais_digitais', '') || ' ' ||
					COALESCE(ci.source_data->>'canais_presenciais', '') || ' ' ||
					COALESCE(ci.source_data->>'canal_atendimento', '') || ' ' ||
					COALESCE(ci.source_data->>'canal', '') || ' ' ||
					COALESCE(ci.source_data->>'Channel__c', '')
				)), 'telefone') > 0
			ELSE FALSE
		END)`, searchRequest.Filters.CanalAtendimento)
	}
	if searchRequest.Filters.Segmento != "" {
		appendFilter(`(ci.type <> 'mei_opportunity' OR EXISTS (
			SELECT 1 FROM unnest(ci.tags) AS catalog_tag
			WHERE immutable_unaccent(lower(catalog_tag)) = immutable_unaccent(lower($%d))
		))`, searchRequest.Filters.Segmento)
	}
	if searchRequest.Filters.Turno != "" {
		appendFilter("(ci.type <> 'course' OR immutable_unaccent(lower(COALESCE(ci.source_data->>'turno', ''))) = immutable_unaccent(lower($%d)))", searchRequest.Filters.Turno)
	}
	if searchRequest.Filters.RegimeContratacao != "" {
		appendFilter(`(ci.type <> 'job' OR immutable_unaccent(lower(COALESCE(
			ci.source_data#>>'{regime_contratacao,descricao}',
			ci.source_data->>'regime_contratacao',
			''
		))) = immutable_unaccent(lower($%d)))`, searchRequest.Filters.RegimeContratacao)
	}
	if searchRequest.Filters.ModeloTrabalho != "" {
		appendFilter(`(ci.type <> 'job' OR immutable_unaccent(lower(COALESCE(
			ci.source_data#>>'{modelo_trabalho,descricao}',
			ci.source_data->>'modelo_trabalho',
			ci.modalidade,
			''
		))) = immutable_unaccent(lower($%d)))`, searchRequest.Filters.ModeloTrabalho)
	}
	if searchRequest.Filters.PCD != nil {
		appendFilter(`(ci.type <> 'job' OR CASE
			WHEN lower(COALESCE(ci.source_data->>'acessibilidade_pcd', ci.target_audience->>'pcd', '')) IN ('', 'false', '0', 'nao', 'não', 'sem_restricao') THEN FALSE
			ELSE TRUE
		END = $%d)`, *searchRequest.Filters.PCD)
	}

	if len(filterClauses) == 0 {
		return "", filterArguments, nextParameterIndex
	}
	return "AND " + strings.Join(filterClauses, " AND "), filterArguments, nextParameterIndex
}

func activeCatalogPredicate(filterSQL string) string {
	return `
		ci.status = 'active'
		AND ci.deleted_at IS NULL
		AND (ci.valid_from IS NULL OR ci.valid_from <= NOW())
		AND (ci.valid_until IS NULL OR ci.valid_until > NOW())
		` + filterSQL
}

// Search returns a stable, paginated catalog browse. Text queries use the same
// independent lexical candidate pools as the hybrid pipeline.
func (repository *SearchRepository) Search(searchContext context.Context, searchRequest *models.SearchRequest) ([]*SearchResult, int, error) {
	if searchRequest.Q != "" {
		candidates, totalCandidates, searchError := repository.SearchRanked(
			searchContext,
			searchRequest,
			RankedSearchOptions{},
		)
		if searchError != nil {
			return nil, 0, searchError
		}
		return paginateRepositoryResults(candidates, searchRequest.Page, searchRequest.PerPage), totalCandidates, nil
	}
	browseSnapshot, browseError := repository.BrowseSnapshot(searchContext, searchRequest)
	if browseError != nil {
		return nil, 0, browseError
	}
	return browseSnapshot.Results, browseSnapshot.Total, nil
}

// BrowseSnapshot deduplicates canonical entities before counting, faceting, or
// pagination. The URL fallback uses the escaped path only and keeps percent
// encoding intact so PostgreSQL and Go derive the same service slug evidence.
func (repository *SearchRepository) BrowseSnapshot(
	searchContext context.Context,
	searchRequest *models.SearchRequest,
) (*BrowseSnapshot, error) {
	filterSQL, filterArguments, nextParameterIndex := buildFilterClauses(searchRequest, 1)
	basePredicate := activeCatalogPredicate(filterSQL)
	canonicalCatalogCTE := buildCanonicalBrowseCTE(basePredicate)
	browseTransaction, beginError := repository.databasePool.BeginTx(searchContext, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if beginError != nil {
		return nil, fmt.Errorf("search browse transaction: %w", beginError)
	}
	defer browseTransaction.Rollback(searchContext)

	snapshotVersion, snapshotError := readCatalogSnapshotVersion(searchContext, browseTransaction)
	if snapshotError != nil {
		return nil, fmt.Errorf("search browse catalog snapshot: %w", snapshotError)
	}

	countStatement := canonicalCatalogCTE + " SELECT COUNT(*) FROM canonical_catalog"
	var totalItems int
	if countError := browseTransaction.QueryRow(searchContext, countStatement, filterArguments...).Scan(&totalItems); countError != nil {
		return nil, fmt.Errorf("search browse count: %w", countError)
	}

	browseSnapshot := &BrowseSnapshot{
		SnapshotVersion: snapshotVersion,
		CatalogRevision: snapshotVersion.Revision,
		Results:         []*SearchResult{},
		Total:           totalItems,
		Facets:          emptySearchFacets(models.SearchFacetScopeCatalogMatches),
	}
	if totalItems == 0 {
		if commitError := browseTransaction.Commit(searchContext); commitError != nil {
			return nil, fmt.Errorf("search browse commit: %w", commitError)
		}
		return browseSnapshot, nil
	}

	resultOffset, pageAvailable := boundedResultOffset(totalItems, searchRequest.Page, searchRequest.PerPage)
	if pageAvailable {
		queryArguments := append(append([]any(nil), filterArguments...), searchRequest.PerPage, resultOffset)
		limitParameterIndex := nextParameterIndex
		queryStatement := canonicalCatalogCTE + fmt.Sprintf(`
			SELECT
				ci.id, ci.external_id, ci.source, ci.type,
				ci.title, COALESCE(ci.description, ''), COALESCE(ci.short_desc, ''),
				COALESCE(ci.organization, ''), COALESCE(ci.url, ''), COALESCE(ci.image_url, ''),
				ci.target_audience, ci.bairros, COALESCE(ci.modalidade, ''),
				ci.status, ci.tags, ci.source_data,
				ci.valid_from, ci.valid_until, ci.source_updated_at,
				ci.created_at, ci.updated_at,
				1.0 AS rank,
				'' AS headline
			FROM canonical_catalog ci
			ORDER BY ci.created_at DESC, ci.id ASC
			LIMIT $%d OFFSET $%d
		`, limitParameterIndex, limitParameterIndex+1)

		searchResults, scanError := scanResults(searchContext, browseTransaction, queryStatement, queryArguments)
		if scanError != nil {
			return nil, scanError
		}
		browseSnapshot.Results = searchResults
	}

	searchFacets, facetsError := querySearchFacets(
		searchContext,
		browseTransaction,
		canonicalCatalogCTE,
		filterArguments,
		nextParameterIndex,
	)
	if facetsError != nil {
		return nil, facetsError
	}
	browseSnapshot.Facets = searchFacets

	if commitError := browseTransaction.Commit(searchContext); commitError != nil {
		return nil, fmt.Errorf("search browse commit: %w", commitError)
	}
	return browseSnapshot, nil
}

// SearchFacets aggregates bounded filter options over every active catalog row
// matching the current non-query filters. It delegates to BrowseSnapshot so a
// direct repository caller receives canonical-entity counts as well.
func (repository *SearchRepository) SearchFacets(
	searchContext context.Context,
	searchRequest *models.SearchRequest,
) (models.SearchFacets, error) {
	browseSnapshot, browseError := repository.BrowseSnapshot(searchContext, searchRequest)
	if browseError != nil {
		return models.SearchFacets{}, browseError
	}
	return browseSnapshot.Facets, nil
}

func buildCanonicalBrowseCTE(basePredicate string) string {
	return fmt.Sprintf(`
		WITH filtered_catalog AS MATERIALIZED (
			SELECT
				ci.id,
				ci.created_at,
				%s AS canonical_entity_key
			FROM catalog_items ci
			WHERE %s
		),
		canonical_winners AS MATERIALIZED (
			SELECT id
			FROM (
				SELECT
					id,
					ROW_NUMBER() OVER (
						PARTITION BY canonical_entity_key
						ORDER BY created_at DESC, id ASC
					) AS canonical_entity_rank
				FROM filtered_catalog
			) canonical_rows
			WHERE canonical_entity_rank = 1
		),
		canonical_catalog AS MATERIALIZED (
			SELECT
				ci.id, ci.external_id, ci.source, ci.type,
				ci.title, ci.description, ci.short_desc,
				ci.organization, ci.url, ci.image_url,
				ci.target_audience, ci.bairros, ci.modalidade,
				ci.status, ci.tags, ci.source_data,
				ci.valid_from, ci.valid_until, ci.source_updated_at,
				ci.created_at, ci.updated_at
			FROM canonical_winners
			JOIN catalog_items ci USING (id)
		)
	`, canonicalEntityKeySQL("ci"), basePredicate)
}

func canonicalEntityKeySQL(catalogAlias string) string {
	explicitCanonicalID := fmt.Sprintf("btrim(%[1]s.source_data->>'canonical_id')", catalogAlias)
	sourceSlug := fmt.Sprintf("btrim(%[1]s.source_data->>'slug')", catalogAlias)
	serviceURLPath := fmt.Sprintf(
		"regexp_replace(split_part(split_part(COALESCE(%s.url, ''), '?', 1), '#', 1), '^(?:[A-Za-z][A-Za-z0-9+.-]*:)?//[^/]*', '', 'i')",
		catalogAlias,
	)
	serviceURLSlug := fmt.Sprintf(
		"btrim(substring(%s FROM '(?i)(?:^|/)servicos/([^/]+)'))",
		serviceURLPath,
	)
	return fmt.Sprintf(`
		CASE
			WHEN NULLIF(%[1]s, '') IS NOT NULL THEN
				jsonb_build_array('explicit', lower(%[1]s))::text COLLATE "C"
			WHEN %[4]s.type = 'service'
				AND NULLIF(%[2]s, '') IS NOT NULL
				AND strpos(%[2]s, '/') = 0
				AND strpos(%[2]s, chr(92)) = 0 THEN
				jsonb_build_array('service-slug', lower(%[2]s))::text COLLATE "C"
			WHEN %[4]s.type = 'service'
				AND NULLIF(%[3]s, '') IS NOT NULL
				AND strpos(%[3]s, chr(92)) = 0 THEN
				jsonb_build_array('service-slug', lower(%[3]s))::text COLLATE "C"
			ELSE jsonb_build_array(
				'source-document',
				%[4]s.type::text,
				%[4]s.source::text,
				%[4]s.external_id
			)::text COLLATE "C"
		END
	`, explicitCanonicalID, sourceSlug, serviceURLSlug, catalogAlias)
}

func serviceSlugEvidenceSQL(catalogAlias string) string {
	sourceSlug := fmt.Sprintf("btrim(%[1]s.source_data->>'slug')", catalogAlias)
	serviceURLPath := fmt.Sprintf(
		"regexp_replace(split_part(split_part(COALESCE(%s.url, ''), '?', 1), '#', 1), '^(?:[A-Za-z][A-Za-z0-9+.-]*:)?//[^/]*', '', 'i')",
		catalogAlias,
	)
	serviceURLSlug := fmt.Sprintf(
		"btrim(substring(%s FROM '(?i)(?:^|/)servicos/([^/]+)'))",
		serviceURLPath,
	)
	return fmt.Sprintf(`
		CASE
			WHEN %[3]s.type = 'service'
				AND NULLIF(%[1]s, '') IS NOT NULL
				AND strpos(%[1]s, '/') = 0
				AND strpos(%[1]s, chr(92)) = 0 THEN lower(%[1]s)
			WHEN %[3]s.type = 'service'
				AND NULLIF(%[2]s, '') IS NOT NULL
				AND strpos(%[2]s, chr(92)) = 0 THEN lower(%[2]s)
			ELSE ''
		END
	`, sourceSlug, serviceURLSlug, catalogAlias)
}

func querySearchFacets(
	searchContext context.Context,
	queryer searchQueryer,
	canonicalCatalogCTE string,
	filterArguments []any,
	firstFacetParameterIndex int,
) (models.SearchFacets, error) {
	queryArguments := append(
		append([]any(nil), filterArguments...),
		models.MaxSearchFacetValues,
		models.MaxSearchFilterRunes,
		models.MaxSearchFacetLabelRunes,
	)
	queryStatement := canonicalCatalogCTE + fmt.Sprintf(`,
			raw_facets AS (
				SELECT id, 'types'::text AS facet_name, item_type AS raw_value, item_type AS label
				FROM (
					SELECT id, type::text AS item_type FROM canonical_catalog
				) canonical_types
				UNION ALL
				SELECT id, 'modalidades', modalidade, modalidade
				FROM canonical_catalog
				UNION ALL
				SELECT id, 'organizations', organization, organization
				FROM canonical_catalog
				UNION ALL
				SELECT canonical_catalog.id, 'bairros', catalog_bairro, catalog_bairro
				FROM canonical_catalog
				CROSS JOIN LATERAL unnest(canonical_catalog.bairros) AS catalog_bairro
			),
			prepared_facets AS MATERIALIZED (
				SELECT
					id,
					facet_name,
					immutable_unaccent(lower(btrim(regexp_replace(COALESCE(raw_value, ''), '[[:space:]]+', ' ', 'g')))) COLLATE "C" AS normalized_value,
					left(btrim(regexp_replace(COALESCE(label, ''), '[[:space:]]+', ' ', 'g')), $%d) COLLATE "C" AS label
				FROM raw_facets
			),
			normalized_facets AS MATERIALIZED (
				SELECT
					id,
					facet_name,
					CASE
						WHEN facet_name <> 'modalidades' THEN normalized_value
						WHEN normalized_value IN ('presencial', 'presencialmente', 'in loco', 'local', 'onsite', 'on-site') THEN 'presencial'
						WHEN normalized_value IN ('digital', 'online', 'remoto', 'remota', 'ead', 'virtual', 'a distancia') THEN 'digital'
						WHEN normalized_value IN ('hibrido', 'hybrid', 'misto', 'mista', 'semipresencial') THEN 'hibrido'
						ELSE NULL
					END COLLATE "C" AS value,
					label
				FROM prepared_facets
			),
			bounded_facets AS (
				SELECT id, facet_name, value, label
				FROM normalized_facets
				WHERE value IS NOT NULL
				  AND value <> ''
				  AND char_length(value) <= $%d
				  AND label <> ''
			),
			grouped_facets AS (
				SELECT
					facet_name,
					value,
					MIN(label) AS label,
					COUNT(DISTINCT id)::integer AS item_count
				FROM bounded_facets
				GROUP BY facet_name, value
			),
		ranked_facets AS (
			SELECT
				facet_name,
				value,
				label,
				item_count,
					ROW_NUMBER() OVER (
						PARTITION BY facet_name
						ORDER BY item_count DESC, value COLLATE "C" ASC
					) AS facet_rank
				FROM grouped_facets
			)
			SELECT facet_name, value, label, item_count
			FROM ranked_facets
			WHERE facet_rank <= $%d
			ORDER BY facet_name ASC, facet_rank ASC
		`, firstFacetParameterIndex+2, firstFacetParameterIndex+1, firstFacetParameterIndex)

	queryRows, queryError := queryer.Query(searchContext, queryStatement, queryArguments...)
	if queryError != nil {
		return models.SearchFacets{}, fmt.Errorf("search facets query: %w", queryError)
	}
	defer queryRows.Close()

	searchFacets := emptySearchFacets(models.SearchFacetScopeCatalogMatches)
	for queryRows.Next() {
		var facetName string
		var facetValue models.SearchFacetValue
		if scanError := queryRows.Scan(
			&facetName,
			&facetValue.Value,
			&facetValue.Label,
			&facetValue.Count,
		); scanError != nil {
			return models.SearchFacets{}, fmt.Errorf("search facets scan: %w", scanError)
		}

		switch facetName {
		case "types":
			searchFacets.Types = append(searchFacets.Types, facetValue)
		case "modalidades":
			searchFacets.Modalidades = append(searchFacets.Modalidades, facetValue)
		case "bairros":
			searchFacets.Bairros = append(searchFacets.Bairros, facetValue)
		case "organizations":
			searchFacets.Organizations = append(searchFacets.Organizations, facetValue)
		default:
			return models.SearchFacets{}, fmt.Errorf("search facets: unsupported facet %q", facetName)
		}
	}
	if rowsError := queryRows.Err(); rowsError != nil {
		return models.SearchFacets{}, fmt.Errorf("search facets rows: %w", rowsError)
	}
	return searchFacets, nil
}

func emptySearchFacets(scope models.SearchFacetScope) models.SearchFacets {
	return models.SearchFacets{
		Version:       models.SearchFacetVersion,
		Scope:         scope,
		Types:         []models.SearchFacetValue{},
		Modalidades:   []models.SearchFacetValue{},
		Bairros:       []models.SearchFacetValue{},
		Organizations: []models.SearchFacetValue{},
	}
}

// SearchRanked retrieves independent exact, full-text, trigram, semantic, and
// optional HyDE pools and fuses them with deterministic weighted RRF. It returns
// the complete bounded candidate union so reranking can happen before pagination.
func (repository *SearchRepository) SearchRanked(
	searchContext context.Context,
	searchRequest *models.SearchRequest,
	searchOptions RankedSearchOptions,
) ([]*SearchResult, int, error) {
	rankedSnapshot, searchError := repository.SearchRankedSnapshot(searchContext, searchRequest, searchOptions)
	if searchError != nil {
		return nil, 0, searchError
	}
	return rankedSnapshot.Results, rankedSnapshot.Total, nil
}

// SearchRankedSnapshot keeps database work in one repeatable-read transaction.
// Query embeddings and other remote work must be completed by callers before
// entering this method.
func (repository *SearchRepository) SearchRankedSnapshot(
	searchContext context.Context,
	searchRequest *models.SearchRequest,
	searchOptions RankedSearchOptions,
) (*RankedSearchSnapshot, error) {
	searchOptions = searchOptions.normalized()
	searchTransaction, beginError := repository.databasePool.BeginTx(
		searchContext,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
	)
	if beginError != nil {
		return nil, fmt.Errorf("ranked search transaction: %w", beginError)
	}
	defer searchTransaction.Rollback(searchContext)

	snapshotVersion, snapshotError := readCatalogSnapshotVersion(searchContext, searchTransaction)
	if snapshotError != nil {
		return nil, fmt.Errorf("ranked search catalog snapshot: %w", snapshotError)
	}
	if configurationError := configureRankedSearch(searchContext, searchTransaction, searchOptions); configurationError != nil {
		return nil, configurationError
	}
	queryStatement, queryArguments := buildRankedSearchQuery(searchRequest, searchOptions)
	searchResults, totalCandidates, queryError := scanRankedResults(
		searchContext,
		searchTransaction,
		queryStatement,
		queryArguments,
	)
	if queryError != nil {
		return nil, queryError
	}
	if commitError := searchTransaction.Commit(searchContext); commitError != nil {
		return nil, fmt.Errorf("ranked search commit: %w", commitError)
	}
	return &RankedSearchSnapshot{
		SnapshotVersion: snapshotVersion,
		CatalogRevision: snapshotVersion.Revision,
		Results:         searchResults,
		Total:           totalCandidates,
	}, nil
}

func configureRankedSearch(
	searchContext context.Context,
	searchTransaction pgx.Tx,
	searchOptions RankedSearchOptions,
) error {
	_, configurationError := searchTransaction.Exec(searchContext, `
		SELECT
			set_config(
				'pg_trgm.similarity_threshold',
				($1::double precision)::text,
				TRUE
			),
			set_config(
				'hnsw.ef_search',
				($2::integer)::text,
				TRUE
			),
			set_config('hnsw.iterative_scan', 'strict_order', TRUE)
	`, DefaultTrigramThreshold, searchOptions.semanticAliasPoolSize())
	if configurationError != nil {
		return fmt.Errorf("ranked search configuration: %w", configurationError)
	}
	return nil
}

func buildRankedSearchQuery(
	searchRequest *models.SearchRequest,
	searchOptions RankedSearchOptions,
) (string, []any) {
	searchOptions = searchOptions.normalized()
	expandedQuery := searchRequest.ExpandedQ
	if expandedQuery == "" {
		expandedQuery = searchRequest.Q
	}

	const firstFilterParameterIndex = 22
	filterSQL, filterArguments, _ := buildFilterClauses(searchRequest, firstFilterParameterIndex)
	basePredicate := activeCatalogPredicate(filterSQL)
	canonicalKeyExpression := canonicalEntityKeySQL("ci")
	serviceSlugExpression := serviceSlugEvidenceSQL("ci")
	facilitaCandidateSlugs, facilitaCandidateRanks := searchOptions.facilitaCandidateArrays()
	queryArguments := []any{
		searchRequest.Q,
		expandedQuery,
		searchOptions.QueryEmbedding,
		searchOptions.HyDEEmbedding,
		searchOptions.EmbeddingModel,
		searchOptions.EmbeddingModelVersion,
		searchOptions.EmbeddingDimensions,
		searchOptions.EmbeddingTaskType,
		searchOptions.EmbeddingDocumentVersion,
		searchOptions.CandidatePoolSize,
		DefaultReciprocalRankK,
		searchOptions.Weights.Exact,
		searchOptions.Weights.FullText,
		searchOptions.Weights.Trigram,
		searchOptions.Weights.Semantic,
		searchOptions.Weights.HyDE,
		searchOptions.MaximumSemanticDistance,
		searchOptions.semanticAliasPoolSize(),
		facilitaCandidateSlugs,
		facilitaCandidateRanks,
		searchOptions.Weights.Facilita,
	}
	queryArguments = append(queryArguments, filterArguments...)

	queryStatement := fmt.Sprintf(`
		WITH
		exact_alias_matches AS MATERIALIZED (
			SELECT
				ci.id,
				%s AS canonical_entity_key,
				ci.created_at,
				GREATEST(
					CASE WHEN immutable_unaccent(lower(ci.title)) = immutable_unaccent(lower($1)) THEN 4 ELSE 0 END,
					CASE WHEN lower(ci.external_id) = lower($1) THEN 3 ELSE 0 END,
					CASE WHEN lower(ci.source_data->>'slug') = lower($1) THEN 2 ELSE 0 END
				) AS match_priority
			FROM catalog_items ci
			WHERE %s
			  AND (
				immutable_unaccent(lower(ci.title)) = immutable_unaccent(lower($1))
				OR lower(ci.external_id) = lower($1)
				OR lower(ci.source_data->>'slug') = lower($1)
			  )
		),
		exact_entity_best_alias AS (
			SELECT DISTINCT ON (canonical_entity_key COLLATE "C")
				canonical_entity_key,
				id AS representative_id,
				match_priority
			FROM exact_alias_matches
			ORDER BY canonical_entity_key COLLATE "C", match_priority DESC, created_at DESC, id ASC
		),
		exact_ranked_entities AS (
			SELECT
				canonical_entity_key,
				representative_id,
				ROW_NUMBER() OVER (
					ORDER BY match_priority DESC, canonical_entity_key COLLATE "C" ASC
				) AS candidate_rank
			FROM exact_entity_best_alias
		),
		exact_candidates AS (
			SELECT canonical_entity_key, representative_id, candidate_rank
			FROM exact_ranked_entities
			WHERE candidate_rank <= $10
		),
		full_text_alias_matches AS MATERIALIZED (
			SELECT
				ci.id,
				%s AS canonical_entity_key,
				ci.created_at,
				ts_rank_cd(
					'{0.05,0.1,0.3,1.0}',
					ci.search_vector,
					websearch_to_tsquery('portuguese', immutable_unaccent($2)),
					32
				) AS relevance,
				ts_headline(
					'portuguese',
					ci.title || ' ' || COALESCE(ci.short_desc, ''),
					websearch_to_tsquery('portuguese', immutable_unaccent($2)),
					'StartSel=<mark>,StopSel=</mark>,MaxFragments=2,MaxWords=15,MinWords=5'
				) AS headline
			FROM catalog_items ci
			WHERE %s
			  AND ci.search_vector @@ websearch_to_tsquery('portuguese', immutable_unaccent($2))
		),
		full_text_entity_best_alias AS (
			SELECT DISTINCT ON (canonical_entity_key COLLATE "C")
				canonical_entity_key,
				id AS representative_id,
				relevance,
				headline
			FROM full_text_alias_matches
			ORDER BY canonical_entity_key COLLATE "C", relevance DESC, created_at DESC, id ASC
		),
		full_text_ranked_entities AS (
			SELECT
				canonical_entity_key,
				representative_id,
				headline,
				ROW_NUMBER() OVER (
					ORDER BY relevance DESC, canonical_entity_key COLLATE "C" ASC
				) AS candidate_rank
			FROM full_text_entity_best_alias
		),
		full_text_candidates AS (
			SELECT canonical_entity_key, representative_id, headline, candidate_rank
			FROM full_text_ranked_entities
			WHERE candidate_rank <= $10
		),
		trigram_alias_matches AS MATERIALIZED (
			SELECT
				ci.id,
				%s AS canonical_entity_key,
				ci.created_at,
				similarity(
					immutable_unaccent(lower(ci.title)),
					immutable_unaccent(lower($1))
				) AS relevance
			FROM catalog_items ci
			WHERE %s
			  AND length($1) >= 2
			  AND immutable_unaccent(lower(ci.title)) %% immutable_unaccent(lower($1))
		),
		trigram_entity_best_alias AS (
			SELECT DISTINCT ON (canonical_entity_key COLLATE "C")
				canonical_entity_key,
				id AS representative_id,
				relevance
			FROM trigram_alias_matches
			ORDER BY canonical_entity_key COLLATE "C", relevance DESC, created_at DESC, id ASC
		),
		trigram_ranked_entities AS (
			SELECT
				canonical_entity_key,
				representative_id,
				ROW_NUMBER() OVER (
					ORDER BY relevance DESC, canonical_entity_key COLLATE "C" ASC
				) AS candidate_rank
			FROM trigram_entity_best_alias
		),
		trigram_candidates AS (
			SELECT canonical_entity_key, representative_id, candidate_rank
			FROM trigram_ranked_entities
			WHERE candidate_rank <= $10
		),
		semantic_alias_pool AS MATERIALIZED (
			SELECT
				ci.id,
				%s AS canonical_entity_key,
				ci.created_at,
				ci.embedding <=> NULLIF($3, '')::vector AS distance
			FROM catalog_items ci
			WHERE %s
			  AND $3 <> ''
			  AND ci.embedding IS NOT NULL
			  AND ci.embedding_model = $5
			  AND ci.embedding_model_version = $6
			  AND ci.embedding_dimensions = $7
			  AND ci.embedding_task_type = $8
			  AND ci.embedding_document_version = $9
			  AND ci.embedding_source_hash IS NOT NULL
			  AND ci.embedding_generated_at IS NOT NULL
			ORDER BY distance ASC
			LIMIT $18
		),
		semantic_entity_best_alias AS (
			SELECT DISTINCT ON (canonical_entity_key COLLATE "C")
				canonical_entity_key,
				id AS representative_id,
				distance
			FROM semantic_alias_pool
			WHERE distance <= $17
			ORDER BY canonical_entity_key COLLATE "C", distance ASC, created_at DESC, id ASC
		),
		semantic_ranked_entities AS (
			SELECT
				canonical_entity_key,
				representative_id,
				ROW_NUMBER() OVER (
					ORDER BY distance ASC, canonical_entity_key COLLATE "C" ASC
				) AS candidate_rank
			FROM semantic_entity_best_alias
		),
		semantic_candidates AS (
			SELECT canonical_entity_key, representative_id, candidate_rank
			FROM semantic_ranked_entities
			WHERE candidate_rank <= $10
		),
		hyde_alias_pool AS MATERIALIZED (
			SELECT
				ci.id,
				%s AS canonical_entity_key,
				ci.created_at,
				ci.embedding <=> NULLIF($4, '')::vector AS distance
			FROM catalog_items ci
			WHERE %s
			  AND $4 <> ''
			  AND ci.embedding IS NOT NULL
			  AND ci.embedding_model = $5
			  AND ci.embedding_model_version = $6
			  AND ci.embedding_dimensions = $7
			  AND ci.embedding_task_type = $8
			  AND ci.embedding_document_version = $9
			  AND ci.embedding_source_hash IS NOT NULL
			  AND ci.embedding_generated_at IS NOT NULL
			ORDER BY distance ASC
			LIMIT $18
		),
		hyde_entity_best_alias AS (
			SELECT DISTINCT ON (canonical_entity_key COLLATE "C")
				canonical_entity_key,
				id AS representative_id,
				distance
			FROM hyde_alias_pool
			WHERE distance <= $17
			ORDER BY canonical_entity_key COLLATE "C", distance ASC, created_at DESC, id ASC
		),
		hyde_ranked_entities AS (
			SELECT
				canonical_entity_key,
				representative_id,
				ROW_NUMBER() OVER (
					ORDER BY distance ASC, canonical_entity_key COLLATE "C" ASC
				) AS candidate_rank
			FROM hyde_entity_best_alias
		),
		hyde_candidates AS (
			SELECT canonical_entity_key, representative_id, candidate_rank
			FROM hyde_ranked_entities
			WHERE candidate_rank <= $10
		),
		facilita_candidate_input AS (
			SELECT candidate_slug, candidate_rank
			FROM unnest($19::text[], $20::integer[]) AS candidates(candidate_slug, candidate_rank)
		),
		facilita_alias_matches AS MATERIALIZED (
			SELECT
				ci.id,
				%s AS canonical_entity_key,
				ci.created_at,
				facilita_candidate_input.candidate_rank
			FROM facilita_candidate_input
			JOIN catalog_items ci ON %s = facilita_candidate_input.candidate_slug
			WHERE %s
		),
		facilita_entity_best_alias AS (
			SELECT DISTINCT ON (canonical_entity_key COLLATE "C")
				canonical_entity_key,
				id AS representative_id,
				candidate_rank
			FROM facilita_alias_matches
			ORDER BY canonical_entity_key COLLATE "C", candidate_rank ASC, created_at DESC, id ASC
		),
		facilita_candidates AS (
			SELECT canonical_entity_key, representative_id, candidate_rank
			FROM facilita_entity_best_alias
			WHERE candidate_rank <= $10
		),
		retrieval_signals AS (
			SELECT canonical_entity_key, representative_id, ''::text AS headline, 'exact'::text AS retriever, $12::double precision / ($11::double precision + candidate_rank::double precision) AS contribution FROM exact_candidates
			UNION ALL
			SELECT canonical_entity_key, representative_id, headline, 'full_text', $13::double precision / ($11::double precision + candidate_rank::double precision) FROM full_text_candidates
			UNION ALL
			SELECT canonical_entity_key, representative_id, '', 'trigram', $14::double precision / ($11::double precision + candidate_rank::double precision) FROM trigram_candidates
			UNION ALL
			SELECT canonical_entity_key, representative_id, '', 'semantic', $15::double precision / ($11::double precision + candidate_rank::double precision) FROM semantic_candidates
			UNION ALL
			SELECT canonical_entity_key, representative_id, '', 'hyde', $16::double precision / ($11::double precision + candidate_rank::double precision) FROM hyde_candidates
			UNION ALL
			SELECT canonical_entity_key, representative_id, '', 'facilita', $21::double precision / ($11::double precision + candidate_rank::double precision) FROM facilita_candidates
		),
		fused_entities AS (
			SELECT
				canonical_entity_key,
				SUM(contribution) AS reciprocal_rank_score,
				COUNT(DISTINCT retriever) AS matching_retrievers,
				BOOL_OR(retriever = 'exact') AS has_exact_match,
				BOOL_OR(retriever = 'facilita') AS facilita_contributed,
				COALESCE(MAX(headline) FILTER (WHERE headline <> ''), '') AS headline
			FROM retrieval_signals
			WHERE contribution > 0
			GROUP BY canonical_entity_key
		),
		fused_ranked_entities AS (
			SELECT
				fused_entities.*,
				ROW_NUMBER() OVER (
					ORDER BY
						has_exact_match DESC,
						reciprocal_rank_score DESC,
						matching_retrievers DESC,
						canonical_entity_key COLLATE "C" ASC
				) AS fused_rank
			FROM fused_entities
		),
		fused_candidates AS (
			SELECT *
			FROM fused_ranked_entities
			WHERE fused_rank <= $10
		),
		canonical_representatives AS (
			SELECT DISTINCT ON (fused_candidates.canonical_entity_key COLLATE "C")
				fused_candidates.canonical_entity_key,
				ci.id AS representative_id
			FROM fused_candidates
			JOIN catalog_items ci ON %s = fused_candidates.canonical_entity_key
			WHERE %s
			ORDER BY
				fused_candidates.canonical_entity_key COLLATE "C",
				ci.created_at DESC,
				ci.id ASC
		)
		SELECT
			ci.id, ci.external_id, ci.source, ci.type,
			ci.title, COALESCE(ci.description, ''), COALESCE(ci.short_desc, ''),
			COALESCE(ci.organization, ''), COALESCE(ci.url, ''), COALESCE(ci.image_url, ''),
			ci.target_audience, ci.bairros, COALESCE(ci.modalidade, ''),
			ci.status, ci.tags, ci.source_data,
			ci.valid_from, ci.valid_until, ci.source_updated_at,
			ci.created_at, ci.updated_at,
			fused_candidates.reciprocal_rank_score AS rank,
			fused_candidates.headline,
			fused_candidates.facilita_contributed,
			COUNT(*) OVER() AS total_candidates
		FROM fused_candidates
		JOIN canonical_representatives USING (canonical_entity_key)
		JOIN catalog_items ci ON ci.id = canonical_representatives.representative_id
		ORDER BY fused_candidates.fused_rank ASC
	`,
		canonicalKeyExpression,
		basePredicate,
		canonicalKeyExpression,
		basePredicate,
		canonicalKeyExpression,
		basePredicate,
		canonicalKeyExpression,
		basePredicate,
		canonicalKeyExpression,
		basePredicate,
		canonicalKeyExpression,
		serviceSlugExpression,
		basePredicate,
		canonicalKeyExpression,
		basePredicate,
	)

	return queryStatement, queryArguments
}

func paginateRepositoryResults(searchResults []*SearchResult, page int, perPage int) []*SearchResult {
	resultOffset, pageAvailable := boundedResultOffset(len(searchResults), page, perPage)
	if !pageAvailable {
		return []*SearchResult{}
	}
	resultLimit := min(resultOffset+perPage, len(searchResults))
	return searchResults[resultOffset:resultLimit]
}

func boundedResultOffset(resultCount int, page int, perPage int) (int, bool) {
	if resultCount <= 0 || page < 1 || perPage < 1 {
		return 0, false
	}
	pageIndex := page - 1
	if pageIndex > (resultCount-1)/perPage {
		return 0, false
	}
	return pageIndex * perPage, true
}

type searchQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func scanRankedResults(
	searchContext context.Context,
	queryer searchQueryer,
	queryStatement string,
	queryArguments []any,
) ([]*SearchResult, int, error) {
	queryRows, queryError := queryer.Query(searchContext, queryStatement, queryArguments...)
	if queryError != nil {
		return nil, 0, fmt.Errorf("ranked search query: %w", queryError)
	}
	defer queryRows.Close()

	searchResults := make([]*SearchResult, 0)
	totalCandidates := 0
	for queryRows.Next() {
		searchResult, scanError := scanSearchResult(queryRows, &totalCandidates, true)
		if scanError != nil {
			return nil, 0, scanError
		}
		searchResults = append(searchResults, searchResult)
	}
	if iterationError := queryRows.Err(); iterationError != nil {
		return nil, 0, fmt.Errorf("ranked search rows: %w", iterationError)
	}
	return searchResults, totalCandidates, nil
}

func scanResults(
	searchContext context.Context,
	queryer searchQueryer,
	queryStatement string,
	queryArguments []any,
) ([]*SearchResult, error) {
	queryRows, queryError := queryer.Query(searchContext, queryStatement, queryArguments...)
	if queryError != nil {
		return nil, fmt.Errorf("search query: %w", queryError)
	}
	defer queryRows.Close()

	searchResults := make([]*SearchResult, 0)
	for queryRows.Next() {
		searchResult, scanError := scanSearchResult(queryRows, nil, false)
		if scanError != nil {
			return nil, scanError
		}
		searchResults = append(searchResults, searchResult)
	}
	if iterationError := queryRows.Err(); iterationError != nil {
		return nil, fmt.Errorf("search rows: %w", iterationError)
	}
	return searchResults, nil
}

func scanSearchResult(
	scanRow rowScanner,
	totalCandidates *int,
	includeRetrievalMetadata bool,
) (*SearchResult, error) {
	catalogItem := &models.CatalogItem{}
	var itemSource string
	var itemType string
	var itemStatus string
	var relevanceRank float64
	var headline string
	var facilitaContributed bool

	scanDestinations := []any{
		&catalogItem.ID, &catalogItem.ExternalID, &itemSource, &itemType,
		&catalogItem.Title, &catalogItem.Description, &catalogItem.ShortDesc,
		&catalogItem.Organization, &catalogItem.URL, &catalogItem.ImageURL,
		&catalogItem.TargetAudience, &catalogItem.Bairros, &catalogItem.Modalidade,
		&itemStatus, &catalogItem.Tags, &catalogItem.SourceData,
		&catalogItem.ValidFrom, &catalogItem.ValidUntil, &catalogItem.SourceUpdatedAt,
		&catalogItem.CreatedAt, &catalogItem.UpdatedAt,
		&relevanceRank, &headline,
	}
	if includeRetrievalMetadata {
		scanDestinations = append(scanDestinations, &facilitaContributed)
	}
	if totalCandidates != nil {
		scanDestinations = append(scanDestinations, totalCandidates)
	}
	if scanError := scanRow.Scan(scanDestinations...); scanError != nil {
		return nil, fmt.Errorf("search scan: %w", scanError)
	}

	catalogItem.Source = models.ItemSource(itemSource)
	catalogItem.Type = models.ItemType(itemType)
	catalogItem.Status = models.ItemStatus(itemStatus)
	return &SearchResult{
		Item:                catalogItem,
		Rank:                relevanceRank,
		Headline:            headline,
		FacilitaContributed: facilitaContributed,
	}, nil
}
