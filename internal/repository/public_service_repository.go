package repository

import (
	"context"
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
	defer transaction.Rollback(queryContext)

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
	defer transaction.Rollback(queryContext)

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
	defer transaction.Rollback(queryContext)

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
	defer transaction.Rollback(queryContext)

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
