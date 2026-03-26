package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

type SearchRepository struct {
	db *pgxpool.Pool
}

func NewSearchRepository(db *pgxpool.Pool) *SearchRepository {
	return &SearchRepository{db: db}
}

type SearchResult struct {
	Item      *models.CatalogItem
	Rank      float64
	Headline  string
}

// Search executa a busca FTS com filtros dinâmicos e retorna resultados rankeados.
func (r *SearchRepository) Search(ctx context.Context, req *models.SearchRequest) ([]*SearchResult, int, error) {
	args := []interface{}{}
	argIdx := 1

	var whereClauses []string
	whereClauses = append(whereClauses, "ci.status = 'active'")
	whereClauses = append(whereClauses, "ci.deleted_at IS NULL")
	whereClauses = append(whereClauses, "(ci.valid_until IS NULL OR ci.valid_until > NOW())")

	// Filtro por tipo(s)
	if len(req.Types) > 0 {
		typeStrs := make([]string, len(req.Types))
		for i, t := range req.Types {
			typeStrs[i] = string(t)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("ci.type = ANY($%d::item_type[])", argIdx))
		args = append(args, typeStrs)
		argIdx++
	}

	// Filtros comuns
	if req.Filters.Bairro != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("$%d = ANY(ci.bairros)", argIdx))
		args = append(args, req.Filters.Bairro)
		argIdx++
	}
	if req.Filters.Orgao != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("ci.organization ILIKE $%d", argIdx))
		args = append(args, "%"+req.Filters.Orgao+"%")
		argIdx++
	}
	if req.Filters.Modalidade != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("ci.modalidade ILIKE $%d", argIdx))
		args = append(args, "%"+req.Filters.Modalidade+"%")
		argIdx++
	}
	if req.Filters.Tema != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("$%d = ANY(ci.tags)", argIdx))
		args = append(args, req.Filters.Tema)
		argIdx++
	}
	if req.Filters.CanalAtendimento != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("ci.modalidade ILIKE $%d", argIdx))
		args = append(args, "%"+req.Filters.CanalAtendimento+"%")
		argIdx++
	}
	if req.Filters.Segmento != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("$%d = ANY(ci.tags)", argIdx))
		args = append(args, req.Filters.Segmento)
		argIdx++
	}

	// FTS — constrói o select e order by dependendo de ter query ou não
	var rankExpr, headlineExpr, orderBy string
	if req.Q != "" {
		rankExpr = fmt.Sprintf(`,
			ts_rank(ci.search_vector, websearch_to_tsquery('portuguese', unaccent($%d))) AS rank`, argIdx)
		headlineExpr = fmt.Sprintf(`,
			ts_headline('portuguese', ci.title || ' ' || COALESCE(ci.short_desc, ''),
				websearch_to_tsquery('portuguese', unaccent($%d)),
				'StartSel=<mark>,StopSel=</mark>,MaxFragments=2,MaxWords=15,MinWords=5'
			) AS headline`, argIdx)
		whereClauses = append(whereClauses,
			fmt.Sprintf("ci.search_vector @@ websearch_to_tsquery('portuguese', unaccent($%d))", argIdx),
		)
		args = append(args, req.Q)
		argIdx++
		orderBy = "ORDER BY rank DESC"
	} else {
		rankExpr = ", 1.0 AS rank"
		headlineExpr = ", '' AS headline"
		orderBy = "ORDER BY ci.created_at DESC"
	}

	where := "WHERE " + strings.Join(whereClauses, " AND ")

	// COUNT total (sem paginação)
	countSQL := fmt.Sprintf(`
		SELECT COUNT(*) FROM catalog_items ci %s
	`, where)

	var total int
	if err := r.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("search count: %w", err)
	}

	if total == 0 {
		return []*SearchResult{}, 0, nil
	}

	// Paginação
	offset := (req.Page - 1) * req.PerPage
	args = append(args, req.PerPage, offset)
	limitClause := fmt.Sprintf("LIMIT $%d OFFSET $%d", argIdx, argIdx+1)

	selectSQL := fmt.Sprintf(`
		SELECT
			ci.id, ci.external_id, ci.source, ci.type,
			ci.title, ci.description, ci.short_desc,
			ci.organization, ci.url, ci.image_url,
			ci.target_audience, ci.bairros, ci.modalidade,
			ci.status, ci.tags, ci.source_data,
			ci.valid_from, ci.valid_until, ci.source_updated_at,
			ci.created_at, ci.updated_at
			%s
			%s
		FROM catalog_items ci
		%s
		%s
		%s
	`, rankExpr, headlineExpr, where, orderBy, limitClause)

	rows, err := r.db.Query(ctx, selectSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	var results []*SearchResult
	for rows.Next() {
		var item models.CatalogItem
		var source, itemType, status string
		var rank float64
		var headline string

		err := rows.Scan(
			&item.ID, &item.ExternalID, &source, &itemType,
			&item.Title, &item.Description, &item.ShortDesc,
			&item.Organization, &item.URL, &item.ImageURL,
			&item.TargetAudience, &item.Bairros, &item.Modalidade,
			&status, &item.Tags, &item.SourceData,
			&item.ValidFrom, &item.ValidUntil, &item.SourceUpdatedAt,
			&item.CreatedAt, &item.UpdatedAt,
			&rank, &headline,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("search scan: %w", err)
		}
		item.Source = models.ItemSource(source)
		item.Type = models.ItemType(itemType)
		item.Status = models.ItemStatus(status)

		results = append(results, &SearchResult{
			Item:     &item,
			Rank:     rank,
			Headline: headline,
		})
	}

	return results, total, rows.Err()
}
