package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

type PublicServiceResolution struct {
	Item          *models.CatalogItem
	CanonicalSlug string
}

type PublicServiceCategorySnapshot struct {
	CatalogRevision string
	Categories      []models.PublicServiceCategory
}

type PublicServiceSubcategorySnapshot struct {
	CatalogRevision string
	Subcategories   []models.PublicServiceSubcategory
}

type PublicServiceListSnapshot struct {
	CatalogRevision string
	Items           []*models.CatalogItem
	Total           int
}

type PublicServiceRelationsSnapshot struct {
	CatalogRevision string
	CanonicalSlug   string
	Theme           string
	Recommendations []models.PublicServiceRelation
	Journey         []models.PublicServiceRelation
	Cluster         []models.PublicServiceRelation
}

type SearchSummaryCandidateSnapshot struct {
	CatalogRevision string
	Items           []*models.CatalogItem
}

type publicServiceQueryer interface {
	catalogSnapshotQueryer
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (repository *CatalogItemRepository) SuggestPublicServices(
	queryContext context.Context,
	query string,
	limit int,
) ([]models.PublicServiceSuggestion, error) {
	if len([]rune(query)) < models.MinimumPublicSuggestionQueryRunes {
		return []models.PublicServiceSuggestion{}, nil
	}
	if limit < 1 || limit > models.MaximumPublicSuggestions {
		return nil, fmt.Errorf("public service suggestion limit is invalid: %d", limit)
	}
	rows, queryError := repository.db.Query(queryContext, `
		SELECT
			catalog_items.title,
			canonical_alias.slug,
			COALESCE(catalog_items.source_data->>'tema_geral', '')
		FROM catalog_items
		JOIN catalog_item_slug_aliases canonical_alias
		  ON canonical_alias.catalog_item_id = catalog_items.id
		 AND canonical_alias.is_canonical = TRUE
		WHERE catalog_items.type = 'service'
		  AND catalog_items.status = 'active'
		  AND catalog_items.deleted_at IS NULL
		  AND (catalog_items.valid_from IS NULL OR catalog_items.valid_from <= NOW())
		  AND (catalog_items.valid_until IS NULL OR catalog_items.valid_until > NOW())
		  AND COALESCE(catalog_items.source_data->>'tema_geral', '') <> ''
		  AND (
			strpos(immutable_unaccent(lower(catalog_items.title)), immutable_unaccent(lower($1))) > 0
			OR strpos(immutable_unaccent(lower(COALESCE(catalog_items.short_desc, ''))), immutable_unaccent(lower($1))) > 0
			OR catalog_items.search_vector @@ websearch_to_tsquery('portuguese', immutable_unaccent($1))
		  )
		ORDER BY
			CASE
				WHEN immutable_unaccent(lower(catalog_items.title)) = immutable_unaccent(lower($1)) THEN 0
				WHEN starts_with(immutable_unaccent(lower(catalog_items.title)), immutable_unaccent(lower($1))) THEN 1
				WHEN strpos(' ' || immutable_unaccent(lower(catalog_items.title)), ' ' || immutable_unaccent(lower($1))) > 0 THEN 2
				WHEN strpos(immutable_unaccent(lower(catalog_items.title)), immutable_unaccent(lower($1))) > 0 THEN 3
				WHEN strpos(immutable_unaccent(lower(COALESCE(catalog_items.short_desc, ''))), immutable_unaccent(lower($1))) > 0 THEN 4
				ELSE 5
			END,
			similarity(immutable_unaccent(lower(catalog_items.title)), immutable_unaccent(lower($1))) DESC,
			char_length(catalog_items.title) ASC,
			catalog_items.title COLLATE "C" ASC,
			catalog_items.id ASC
		LIMIT $2
	`, query, limit)
	if queryError != nil {
		return nil, fmt.Errorf("public service suggestions: %w", queryError)
	}
	defer rows.Close()

	suggestions := make([]models.PublicServiceSuggestion, 0, limit)
	for rows.Next() {
		var suggestion models.PublicServiceSuggestion
		var category string
		if scanError := rows.Scan(&suggestion.Title, &suggestion.Slug, &category); scanError != nil {
			return nil, fmt.Errorf("public service suggestions: %w", scanError)
		}
		suggestion.URL = models.PublicServiceURL(category, suggestion.Slug)
		suggestions = append(suggestions, suggestion)
	}
	if rowsError := rows.Err(); rowsError != nil {
		return nil, fmt.Errorf("public service suggestions: %w", rowsError)
	}
	return suggestions, nil
}

func (repository *CatalogItemRepository) GetPublicServiceRelations(
	queryContext context.Context,
	requestedSlug string,
	limit int,
) (*PublicServiceRelationsSnapshot, error) {
	if limit < 1 || limit > models.MaximumPublicServiceRelations {
		return nil, fmt.Errorf("public service relations limit is invalid: %d", limit)
	}
	transaction, beginError := repository.db.BeginTx(queryContext, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if beginError != nil {
		return nil, fmt.Errorf("public service relations transaction: %w", beginError)
	}
	defer func() { _ = transaction.Rollback(queryContext) }()

	snapshotVersion, snapshotError := readCatalogSnapshotVersion(queryContext, transaction)
	if snapshotError != nil {
		return nil, fmt.Errorf("public service relations snapshot: %w", snapshotError)
	}
	var originID uuid.UUID
	var canonicalSlug string
	var theme string
	lookupError := transaction.QueryRow(queryContext, `
		SELECT catalog_items.id, canonical_alias.slug,
			COALESCE(catalog_items.source_data->>'tema_geral', '')
		FROM catalog_item_slug_aliases requested_alias
		JOIN catalog_items ON catalog_items.id = requested_alias.catalog_item_id
		JOIN catalog_item_slug_aliases canonical_alias
		  ON canonical_alias.catalog_item_id = catalog_items.id
		 AND canonical_alias.is_canonical = TRUE
		WHERE requested_alias.slug = $1
		  AND catalog_items.type = 'service'
		  AND catalog_items.status = 'active'
		  AND catalog_items.deleted_at IS NULL
		  AND (catalog_items.valid_from IS NULL OR catalog_items.valid_from <= NOW())
		  AND (catalog_items.valid_until IS NULL OR catalog_items.valid_until > NOW())
		ORDER BY requested_alias.is_canonical DESC, catalog_items.updated_at DESC, catalog_items.id ASC
		LIMIT 1
	`, requestedSlug).Scan(&originID, &canonicalSlug, &theme)
	if lookupError != nil {
		return nil, lookupError
	}

	journey, journeyTheme, journeyError := queryPublicJourneyRelations(queryContext, transaction, originID, limit)
	if journeyError != nil {
		return nil, journeyError
	}
	if journeyTheme != "" {
		theme = journeyTheme
	}
	recommendations, recommendationError := queryPublicRecommendedRelations(
		queryContext, transaction, originID, limit,
	)
	if recommendationError != nil {
		return nil, recommendationError
	}
	cluster, clusterError := queryPublicClusterRelations(queryContext, transaction, originID, limit)
	if clusterError != nil {
		return nil, clusterError
	}
	if commitError := transaction.Commit(queryContext); commitError != nil {
		return nil, fmt.Errorf("public service relations commit: %w", commitError)
	}
	return &PublicServiceRelationsSnapshot{
		CatalogRevision: snapshotVersion.Revision,
		CanonicalSlug:   canonicalSlug,
		Theme:           theme,
		Recommendations: recommendations,
		Journey:         journey,
		Cluster:         cluster,
	}, nil
}

func (repository *CatalogItemRepository) GetSearchSummaryCandidates(
	queryContext context.Context,
	catalogRevision string,
	candidateIDs []uuid.UUID,
) (*SearchSummaryCandidateSnapshot, error) {
	transaction, beginError := repository.db.BeginTx(queryContext, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if beginError != nil {
		return nil, fmt.Errorf("search summary candidate transaction: %w", beginError)
	}
	defer func() { _ = transaction.Rollback(queryContext) }()
	snapshotVersion, snapshotError := readCatalogSnapshotVersion(queryContext, transaction)
	if snapshotError != nil {
		return nil, fmt.Errorf("search summary candidate snapshot: %w", snapshotError)
	}
	if snapshotVersion.Revision != catalogRevision {
		return nil, fmt.Errorf("%w: requested %q, current %q", models.ErrCatalogRevisionMismatch, catalogRevision, snapshotVersion.Revision)
	}
	rows, queryError := transaction.Query(queryContext, `
		SELECT catalog_items.id, catalog_items.external_id, catalog_items.source, catalog_items.type, catalog_items.title,
			COALESCE(catalog_items.description, ''), COALESCE(catalog_items.short_desc, ''),
			COALESCE(catalog_items.organization, ''), COALESCE(catalog_items.url, ''), COALESCE(catalog_items.image_url, ''),
			catalog_items.target_audience, catalog_items.bairros,
			COALESCE(catalog_items.modalidade, ''), catalog_items.status, catalog_items.tags, catalog_items.source_data,
			catalog_items.valid_from, catalog_items.valid_until, catalog_items.source_updated_at,
			catalog_items.created_at, catalog_items.updated_at
		FROM unnest($1::uuid[]) WITH ORDINALITY requested(id, ordinal)
		JOIN catalog_items ON catalog_items.id = requested.id
		WHERE catalog_items.status = 'active'
		  AND catalog_items.deleted_at IS NULL
		  AND (catalog_items.valid_from IS NULL OR catalog_items.valid_from <= NOW())
		  AND (catalog_items.valid_until IS NULL OR catalog_items.valid_until > NOW())
		ORDER BY requested.ordinal
	`, candidateIDs)
	if queryError != nil {
		return nil, fmt.Errorf("search summary candidates: %w", queryError)
	}
	defer rows.Close()
	items := make([]*models.CatalogItem, 0, len(candidateIDs))
	for rows.Next() {
		item, scanError := scanCatalogItemFromRows(rows)
		if scanError != nil {
			return nil, fmt.Errorf("search summary candidates: %w", scanError)
		}
		items = append(items, item)
	}
	if rowsError := rows.Err(); rowsError != nil {
		return nil, fmt.Errorf("search summary candidates: %w", rowsError)
	}
	if len(items) != len(candidateIDs) {
		return nil, errors.New("one or more search summary candidates are no longer publicly eligible")
	}
	if commitError := transaction.Commit(queryContext); commitError != nil {
		return nil, fmt.Errorf("search summary candidate commit: %w", commitError)
	}
	return &SearchSummaryCandidateSnapshot{CatalogRevision: snapshotVersion.Revision, Items: items}, nil
}

func queryPublicJourneyRelations(
	queryContext context.Context,
	queryer publicServiceQueryer,
	originID uuid.UUID,
	limit int,
) ([]models.PublicServiceRelation, string, error) {
	rows, queryError := queryer.Query(queryContext, `
		SELECT target.id, target_alias.slug, target.title,
			COALESCE(target.short_desc, ''), COALESCE(target.organization, ''),
			COALESCE(relation.reason, ''), COALESCE(relation.theme, ''),
			COALESCE(target.source_data->>'tema_geral', '')
		FROM catalog_items origin
		JOIN catalog_item_journeys relation
		  ON relation.from_external_id = origin.external_id
		 AND relation.from_source = origin.source::text
		JOIN catalog_items target
		  ON target.external_id = relation.to_external_id
		 AND target.source::text = relation.to_source
		JOIN catalog_item_slug_aliases target_alias
		  ON target_alias.catalog_item_id = target.id
		 AND target_alias.is_canonical = TRUE
		WHERE origin.id = $1
		  AND target.type = 'service'
		  AND target.status = 'active'
		  AND target.deleted_at IS NULL
		  AND (target.valid_from IS NULL OR target.valid_from <= NOW())
		  AND (target.valid_until IS NULL OR target.valid_until > NOW())
		  AND COALESCE(target.source_data->>'tema_geral', '') <> ''
		ORDER BY relation.weight DESC, target.title COLLATE "C" ASC, target.id ASC
		LIMIT $2
	`, originID, limit)
	if queryError != nil {
		return nil, "", fmt.Errorf("public service journey: %w", queryError)
	}
	defer rows.Close()
	relations := make([]models.PublicServiceRelation, 0, limit)
	theme := ""
	for rows.Next() {
		var relation models.PublicServiceRelation
		var relationTheme string
		var relationCategory string
		if scanError := rows.Scan(
			&relation.ID, &relation.Slug, &relation.Title, &relation.ShortDesc,
			&relation.Organization, &relation.Reason, &relationTheme, &relationCategory,
		); scanError != nil {
			return nil, "", fmt.Errorf("public service journey: %w", scanError)
		}
		if theme == "" {
			theme = relationTheme
		}
		relation.URL = models.PublicServiceURL(relationCategory, relation.Slug)
		relations = append(relations, relation)
	}
	if rowsError := rows.Err(); rowsError != nil {
		return nil, "", fmt.Errorf("public service journey: %w", rowsError)
	}
	return relations, theme, nil
}

func queryPublicRecommendedRelations(
	queryContext context.Context,
	queryer publicServiceQueryer,
	originID uuid.UUID,
	limit int,
) ([]models.PublicServiceRelation, error) {
	rows, queryError := queryer.Query(queryContext, `
		SELECT candidate.id, candidate_alias.slug, candidate.title,
			COALESCE(candidate.short_desc, ''), COALESCE(candidate.organization, ''),
			CASE
				WHEN journey.weight IS NOT NULL THEN COALESCE(journey.reason, 'próximo passo na jornada')
				WHEN COALESCE(candidate.source_data->>'sub_categoria', '') <> ''
				 AND candidate.source_data->>'sub_categoria' = origin.source_data->>'sub_categoria'
				THEN 'mesma subcategoria de serviço'
				WHEN COALESCE(candidate.source_data->>'tema_geral', '') <> ''
				 AND candidate.source_data->>'tema_geral' = origin.source_data->>'tema_geral'
				THEN 'mesmo tema de serviço'
				ELSE 'conteúdo semanticamente relacionado'
			END AS reason,
			COALESCE(candidate.source_data->>'tema_geral', '')
		FROM catalog_items origin
		JOIN catalog_items candidate ON candidate.id <> origin.id
		JOIN catalog_item_slug_aliases candidate_alias
		  ON candidate_alias.catalog_item_id = candidate.id
		 AND candidate_alias.is_canonical = TRUE
		LEFT JOIN LATERAL (
			SELECT relation.weight, relation.reason
			FROM catalog_item_journeys relation
			WHERE relation.from_external_id = origin.external_id
			  AND relation.from_source = origin.source::text
			  AND relation.to_external_id = candidate.external_id
			  AND relation.to_source = candidate.source::text
			ORDER BY relation.weight DESC
			LIMIT 1
		) journey ON TRUE
		WHERE origin.id = $1
		  AND candidate.type = 'service'
		  AND candidate.status = 'active'
		  AND candidate.deleted_at IS NULL
		  AND (candidate.valid_from IS NULL OR candidate.valid_from <= NOW())
		  AND (candidate.valid_until IS NULL OR candidate.valid_until > NOW())
		  AND COALESCE(candidate.source_data->>'tema_geral', '') <> ''
		  AND (
			journey.weight IS NOT NULL
			OR (
				COALESCE(origin.source_data->>'tema_geral', '') <> ''
				AND candidate.source_data->>'tema_geral' = origin.source_data->>'tema_geral'
			)
			OR (
				origin.embedding IS NOT NULL AND candidate.embedding IS NOT NULL
				AND candidate.embedding_model = origin.embedding_model
				AND candidate.embedding_model_version = origin.embedding_model_version
				AND candidate.embedding_dimensions = origin.embedding_dimensions
				AND candidate.embedding_task_type = origin.embedding_task_type
				AND candidate.embedding_document_version = origin.embedding_document_version
			)
		  )
		ORDER BY
			COALESCE(journey.weight, 0) DESC,
			(candidate.source_data->>'sub_categoria' = origin.source_data->>'sub_categoria') DESC,
			(candidate.source_data->>'tema_geral' = origin.source_data->>'tema_geral') DESC,
			CASE
				WHEN origin.embedding IS NOT NULL AND candidate.embedding IS NOT NULL
				 AND candidate.embedding_model = origin.embedding_model
				 AND candidate.embedding_model_version = origin.embedding_model_version
				 AND candidate.embedding_dimensions = origin.embedding_dimensions
				 AND candidate.embedding_task_type = origin.embedding_task_type
				 AND candidate.embedding_document_version = origin.embedding_document_version
				THEN candidate.embedding <=> origin.embedding
				ELSE 2
			END ASC,
			candidate.title COLLATE "C" ASC,
			candidate.id ASC
		LIMIT $2
	`, originID, limit)
	if queryError != nil {
		return nil, fmt.Errorf("public service recommendations: %w", queryError)
	}
	defer rows.Close()
	return scanPublicServiceRelations(rows, limit, "public service recommendations")
}

func queryPublicClusterRelations(
	queryContext context.Context,
	queryer publicServiceQueryer,
	originID uuid.UUID,
	limit int,
) ([]models.PublicServiceRelation, error) {
	rows, queryError := queryer.Query(queryContext, `
		SELECT candidate.id, candidate_alias.slug, candidate.title,
			COALESCE(candidate.short_desc, ''), COALESCE(candidate.organization, ''),
			'mesmo tema de serviço', COALESCE(candidate.source_data->>'tema_geral', '')
		FROM catalog_items origin
		JOIN catalog_items candidate
		  ON candidate.id <> origin.id
		 AND COALESCE(origin.source_data->>'tema_geral', '') <> ''
		 AND candidate.source_data->>'tema_geral' = origin.source_data->>'tema_geral'
		JOIN catalog_item_slug_aliases candidate_alias
		  ON candidate_alias.catalog_item_id = candidate.id
		 AND candidate_alias.is_canonical = TRUE
		WHERE origin.id = $1
		  AND candidate.type = 'service'
		  AND candidate.status = 'active'
		  AND candidate.deleted_at IS NULL
		  AND (candidate.valid_from IS NULL OR candidate.valid_from <= NOW())
		  AND (candidate.valid_until IS NULL OR candidate.valid_until > NOW())
		ORDER BY
			(candidate.source_data->>'sub_categoria' = origin.source_data->>'sub_categoria') DESC,
			CASE
				WHEN origin.embedding IS NOT NULL AND candidate.embedding IS NOT NULL
				 AND candidate.embedding_model = origin.embedding_model
				 AND candidate.embedding_model_version = origin.embedding_model_version
				 AND candidate.embedding_dimensions = origin.embedding_dimensions
				 AND candidate.embedding_task_type = origin.embedding_task_type
				 AND candidate.embedding_document_version = origin.embedding_document_version
				THEN candidate.embedding <=> origin.embedding
				ELSE 2
			END ASC,
			candidate.title COLLATE "C" ASC,
			candidate.id ASC
		LIMIT $2
	`, originID, limit)
	if queryError != nil {
		return nil, fmt.Errorf("public service cluster: %w", queryError)
	}
	defer rows.Close()
	return scanPublicServiceRelations(rows, limit, "public service cluster")
}

func scanPublicServiceRelations(
	rows pgx.Rows,
	capacity int,
	operation string,
) ([]models.PublicServiceRelation, error) {
	relations := make([]models.PublicServiceRelation, 0, capacity)
	for rows.Next() {
		var relation models.PublicServiceRelation
		var relationCategory string
		if scanError := rows.Scan(
			&relation.ID, &relation.Slug, &relation.Title, &relation.ShortDesc,
			&relation.Organization, &relation.Reason, &relationCategory,
		); scanError != nil {
			return nil, fmt.Errorf("%s: %w", operation, scanError)
		}
		relation.URL = models.PublicServiceURL(relationCategory, relation.Slug)
		relations = append(relations, relation)
	}
	if rowsError := rows.Err(); rowsError != nil {
		return nil, fmt.Errorf("%s: %w", operation, rowsError)
	}
	return relations, nil
}

func (repository *CatalogItemRepository) GetPublicServiceBySlug(
	queryContext context.Context,
	requestedSlug string,
) (*PublicServiceResolution, error) {
	transaction, beginError := repository.db.BeginTx(queryContext, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if beginError != nil {
		return nil, fmt.Errorf("public service detail transaction: %w", beginError)
	}
	defer func() { _ = transaction.Rollback(queryContext) }()

	var catalogItemID uuid.UUID
	var canonicalSlug string
	lookupError := transaction.QueryRow(queryContext, `
		SELECT catalog_items.id, canonical_alias.slug
		FROM catalog_item_slug_aliases requested_alias
		JOIN catalog_items ON catalog_items.id = requested_alias.catalog_item_id
		JOIN catalog_item_slug_aliases canonical_alias
		  ON canonical_alias.catalog_item_id = catalog_items.id
		 AND canonical_alias.is_canonical = TRUE
		WHERE requested_alias.slug = $1
		  AND catalog_items.type = 'service'
		  AND catalog_items.status = 'active'
		  AND catalog_items.deleted_at IS NULL
		  AND (catalog_items.valid_from IS NULL OR catalog_items.valid_from <= NOW())
		  AND (catalog_items.valid_until IS NULL OR catalog_items.valid_until > NOW())
		ORDER BY
			requested_alias.is_canonical DESC,
			catalog_items.source_updated_at DESC NULLS LAST,
			catalog_items.updated_at DESC,
			catalog_items.id ASC
		LIMIT 1
	`, requestedSlug).Scan(&catalogItemID, &canonicalSlug)
	if lookupError != nil {
		return nil, lookupError
	}

	item, itemError := queryPublicCatalogItemByID(queryContext, transaction, catalogItemID)
	if itemError != nil {
		return nil, itemError
	}
	if commitError := transaction.Commit(queryContext); commitError != nil {
		return nil, fmt.Errorf("public service detail commit: %w", commitError)
	}
	return &PublicServiceResolution{Item: item, CanonicalSlug: canonicalSlug}, nil
}

func (repository *CatalogItemRepository) ListPublicServiceCategories(
	queryContext context.Context,
) (*PublicServiceCategorySnapshot, error) {
	transaction, beginError := repository.db.BeginTx(queryContext, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if beginError != nil {
		return nil, fmt.Errorf("public service categories transaction: %w", beginError)
	}
	defer func() { _ = transaction.Rollback(queryContext) }()

	snapshotVersion, snapshotError := readCatalogSnapshotVersion(queryContext, transaction)
	if snapshotError != nil {
		return nil, snapshotError
	}
	rows, queryError := transaction.Query(queryContext, `
		SELECT (catalog_items.source_data->>'tema_geral') COLLATE "C", COUNT(*)::integer
		FROM catalog_items
		JOIN catalog_item_slug_aliases canonical_alias
		  ON canonical_alias.catalog_item_id = catalog_items.id
		 AND canonical_alias.is_canonical = TRUE
		WHERE catalog_items.type = 'service'
		  AND catalog_items.status = 'active'
		  AND catalog_items.deleted_at IS NULL
		  AND (catalog_items.valid_from IS NULL OR catalog_items.valid_from <= NOW())
		  AND (catalog_items.valid_until IS NULL OR catalog_items.valid_until > NOW())
		  AND COALESCE(catalog_items.source_data->>'tema_geral', '') <> ''
		GROUP BY (catalog_items.source_data->>'tema_geral') COLLATE "C"
		ORDER BY COUNT(*) DESC, (catalog_items.source_data->>'tema_geral') COLLATE "C" ASC
	`)
	if queryError != nil {
		return nil, fmt.Errorf("public service categories: %w", queryError)
	}
	defer rows.Close()
	categories := make([]models.PublicServiceCategory, 0)
	for rows.Next() {
		var category models.PublicServiceCategory
		if scanError := rows.Scan(&category.Name, &category.Count); scanError != nil {
			return nil, fmt.Errorf("public service categories: %w", scanError)
		}
		categories = append(categories, category)
	}
	if rowsError := rows.Err(); rowsError != nil {
		return nil, fmt.Errorf("public service categories: %w", rowsError)
	}
	if commitError := transaction.Commit(queryContext); commitError != nil {
		return nil, fmt.Errorf("public service categories commit: %w", commitError)
	}
	return &PublicServiceCategorySnapshot{CatalogRevision: snapshotVersion.Revision, Categories: categories}, nil
}

func (repository *CatalogItemRepository) ListPublicServiceSubcategories(
	queryContext context.Context,
	category string,
) (*PublicServiceSubcategorySnapshot, error) {
	transaction, beginError := repository.db.BeginTx(queryContext, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if beginError != nil {
		return nil, fmt.Errorf("public service subcategories transaction: %w", beginError)
	}
	defer func() { _ = transaction.Rollback(queryContext) }()

	snapshotVersion, snapshotError := readCatalogSnapshotVersion(queryContext, transaction)
	if snapshotError != nil {
		return nil, snapshotError
	}
	rows, queryError := transaction.Query(queryContext, `
		SELECT
			(catalog_items.source_data->>'tema_geral') COLLATE "C",
			(catalog_items.source_data->>'sub_categoria') COLLATE "C",
			COUNT(*)::integer
		FROM catalog_items
		JOIN catalog_item_slug_aliases canonical_alias
		  ON canonical_alias.catalog_item_id = catalog_items.id
		 AND canonical_alias.is_canonical = TRUE
		WHERE catalog_items.type = 'service'
		  AND catalog_items.status = 'active'
		  AND catalog_items.deleted_at IS NULL
		  AND (catalog_items.valid_from IS NULL OR catalog_items.valid_from <= NOW())
		  AND (catalog_items.valid_until IS NULL OR catalog_items.valid_until > NOW())
		  AND catalog_items.source_data->>'tema_geral' = $1
		  AND COALESCE(catalog_items.source_data->>'sub_categoria', '') <> ''
		GROUP BY
			(catalog_items.source_data->>'tema_geral') COLLATE "C",
			(catalog_items.source_data->>'sub_categoria') COLLATE "C"
		ORDER BY COUNT(*) DESC, (catalog_items.source_data->>'sub_categoria') COLLATE "C" ASC
	`, category)
	if queryError != nil {
		return nil, fmt.Errorf("public service subcategories: %w", queryError)
	}
	defer rows.Close()
	subcategories := make([]models.PublicServiceSubcategory, 0)
	for rows.Next() {
		var subcategory models.PublicServiceSubcategory
		if scanError := rows.Scan(&subcategory.Category, &subcategory.Name, &subcategory.Count); scanError != nil {
			return nil, fmt.Errorf("public service subcategories: %w", scanError)
		}
		subcategories = append(subcategories, subcategory)
	}
	if rowsError := rows.Err(); rowsError != nil {
		return nil, fmt.Errorf("public service subcategories: %w", rowsError)
	}
	if commitError := transaction.Commit(queryContext); commitError != nil {
		return nil, fmt.Errorf("public service subcategories commit: %w", commitError)
	}
	return &PublicServiceSubcategorySnapshot{
		CatalogRevision: snapshotVersion.Revision,
		Subcategories:   subcategories,
	}, nil
}

func (repository *CatalogItemRepository) ListPublicServices(
	queryContext context.Context,
	category string,
	subcategory string,
	page int,
	perPage int,
) (*PublicServiceListSnapshot, error) {
	transaction, beginError := repository.db.BeginTx(queryContext, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if beginError != nil {
		return nil, fmt.Errorf("public service list transaction: %w", beginError)
	}
	defer func() { _ = transaction.Rollback(queryContext) }()

	snapshotVersion, snapshotError := readCatalogSnapshotVersion(queryContext, transaction)
	if snapshotError != nil {
		return nil, snapshotError
	}
	const filters = `
		catalog_items.type = 'service'
		AND catalog_items.status = 'active'
		AND catalog_items.deleted_at IS NULL
		AND (catalog_items.valid_from IS NULL OR catalog_items.valid_from <= NOW())
		AND (catalog_items.valid_until IS NULL OR catalog_items.valid_until > NOW())
		AND ($1 = '' OR catalog_items.source_data->>'tema_geral' = $1)
		AND ($2 = '' OR catalog_items.source_data->>'sub_categoria' = $2)
	`
	var total int
	countError := transaction.QueryRow(queryContext, `
		SELECT COUNT(*)::integer
		FROM catalog_items
		JOIN catalog_item_slug_aliases canonical_alias
		  ON canonical_alias.catalog_item_id = catalog_items.id
		 AND canonical_alias.is_canonical = TRUE
		WHERE `+filters, category, subcategory).Scan(&total)
	if countError != nil {
		return nil, fmt.Errorf("public service list count: %w", countError)
	}
	rows, queryError := transaction.Query(queryContext, `
		SELECT catalog_items.id, catalog_items.external_id, catalog_items.source, catalog_items.type, catalog_items.title,
			COALESCE(catalog_items.description, ''), COALESCE(catalog_items.short_desc, ''),
			COALESCE(catalog_items.organization, ''), COALESCE(catalog_items.url, ''), COALESCE(catalog_items.image_url, ''),
			catalog_items.target_audience, catalog_items.bairros,
			COALESCE(catalog_items.modalidade, ''), catalog_items.status, catalog_items.tags, catalog_items.source_data,
			catalog_items.valid_from, catalog_items.valid_until, catalog_items.source_updated_at,
			catalog_items.created_at, catalog_items.updated_at
		FROM catalog_items
		JOIN catalog_item_slug_aliases canonical_alias
		  ON canonical_alias.catalog_item_id = catalog_items.id
		 AND canonical_alias.is_canonical = TRUE
		WHERE `+filters+`
		ORDER BY catalog_items.title COLLATE "C" ASC, catalog_items.id ASC
		LIMIT $3 OFFSET $4
	`, category, subcategory, perPage, (page-1)*perPage)
	if queryError != nil {
		return nil, fmt.Errorf("public service list: %w", queryError)
	}
	defer rows.Close()
	items := make([]*models.CatalogItem, 0, perPage)
	for rows.Next() {
		item, scanError := scanCatalogItemFromRows(rows)
		if scanError != nil {
			return nil, fmt.Errorf("public service list: %w", scanError)
		}
		items = append(items, item)
	}
	if rowsError := rows.Err(); rowsError != nil {
		return nil, fmt.Errorf("public service list: %w", rowsError)
	}
	if commitError := transaction.Commit(queryContext); commitError != nil {
		return nil, fmt.Errorf("public service list commit: %w", commitError)
	}
	return &PublicServiceListSnapshot{
		CatalogRevision: snapshotVersion.Revision,
		Items:           items,
		Total:           total,
	}, nil
}

func queryPublicCatalogItemByID(
	queryContext context.Context,
	queryer catalogSnapshotQueryer,
	catalogItemID uuid.UUID,
) (*models.CatalogItem, error) {
	row := queryer.QueryRow(queryContext, `
		SELECT id, external_id, source, type, title,
			COALESCE(description, ''), COALESCE(short_desc, ''),
			COALESCE(organization, ''), COALESCE(url, ''), COALESCE(image_url, ''),
			target_audience, bairros,
			COALESCE(modalidade, ''), status, tags, source_data,
			valid_from, valid_until, source_updated_at, created_at, updated_at
		FROM catalog_items
		WHERE id = $1
		  AND type = 'service'
		  AND status = 'active'
		  AND deleted_at IS NULL
		  AND (valid_from IS NULL OR valid_from <= NOW())
		  AND (valid_until IS NULL OR valid_until > NOW())
	`, catalogItemID)
	return scanCatalogItem(row)
}
