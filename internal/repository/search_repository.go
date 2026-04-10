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
	Item     *models.CatalogItem
	Rank     float64
	Headline string
}

// buildFilterClauses constrói as cláusulas WHERE dinâmicas para os filtros do SearchRequest.
// startIdx é o índice do primeiro parâmetro posicional ($N) a ser usado.
// Retorna (cláusulas AND, args, próximo índice livre).
func buildFilterClauses(req *models.SearchRequest, startIdx int) (string, []interface{}, int) {
	var clauses []string
	var args []interface{}
	idx := startIdx

	if len(req.Types) > 0 {
		typeStrs := make([]string, len(req.Types))
		for i, t := range req.Types {
			typeStrs[i] = string(t)
		}
		clauses = append(clauses, fmt.Sprintf("ci.type = ANY($%d::item_type[])", idx))
		args = append(args, typeStrs)
		idx++
	}
	if req.Filters.Bairro != "" {
		clauses = append(clauses, fmt.Sprintf("$%d = ANY(ci.bairros)", idx))
		args = append(args, req.Filters.Bairro)
		idx++
	}
	if req.Filters.Orgao != "" {
		clauses = append(clauses, fmt.Sprintf("ci.organization ILIKE $%d", idx))
		args = append(args, "%"+req.Filters.Orgao+"%")
		idx++
	}
	if req.Filters.Modalidade != "" {
		clauses = append(clauses, fmt.Sprintf("ci.modalidade ILIKE $%d", idx))
		args = append(args, "%"+req.Filters.Modalidade+"%")
		idx++
	}
	if req.Filters.Tema != "" {
		clauses = append(clauses, fmt.Sprintf("$%d = ANY(ci.tags)", idx))
		args = append(args, req.Filters.Tema)
		idx++
	}
	if req.Filters.CanalAtendimento != "" {
		clauses = append(clauses, fmt.Sprintf("ci.modalidade ILIKE $%d", idx))
		args = append(args, "%"+req.Filters.CanalAtendimento+"%")
		idx++
	}
	if req.Filters.Segmento != "" {
		clauses = append(clauses, fmt.Sprintf("$%d = ANY(ci.tags)", idx))
		args = append(args, req.Filters.Segmento)
		idx++
	}

	sql := ""
	if len(clauses) > 0 {
		sql = "AND " + strings.Join(clauses, " AND ")
	}
	return sql, args, idx
}

// Search executa busca FTS melhorada com ts_rank_cd + boost de similaridade de título.
// Usado como fallback quando embedding não está disponível.
func (r *SearchRepository) Search(ctx context.Context, req *models.SearchRequest) ([]*SearchResult, int, error) {
	filterSQL, filterArgs, nextIdx := buildFilterClauses(req, 2) // $1 reservado para a query

	baseWhere := `
		ci.status = 'active'
		AND ci.deleted_at IS NULL
		AND (ci.valid_until IS NULL OR ci.valid_until > NOW())
		` + filterSQL

	var rankExpr, headlineExpr, tsCondition, orderBy string
	var queryArg interface{}

	hasQuery := req.Q != ""

	if hasQuery {
		// ts_rank_cd com pesos {D,C,B,A} = {0.05, 0.1, 0.3, 2.0}
		rankExpr = `
			ts_rank_cd('{0.05,0.1,0.3,1.0}', ci.search_vector,
				websearch_to_tsquery('portuguese', unaccent($1)), 32)
			+ similarity(unaccent(lower(ci.title)), unaccent(lower($1))) * 0.3
		`
		headlineExpr = `
			ts_headline('portuguese',
				ci.title || ' ' || COALESCE(ci.short_desc, ''),
				websearch_to_tsquery('portuguese', unaccent($1)),
				'StartSel=<mark>,StopSel=</mark>,MaxFragments=2,MaxWords=15,MinWords=5'
			)
		`
		tsCondition = "AND ci.search_vector @@ websearch_to_tsquery('portuguese', unaccent($1))"
		orderBy = "ORDER BY rank DESC"
		queryArg = req.Q
	} else {
		rankExpr = "1.0"
		headlineExpr = "''"
		tsCondition = ""
		orderBy = "ORDER BY ci.created_at DESC"
	}

	// Quando não há query, os args dos filtros começam em $1 (não há $1 reservado para a query).
	// Quando há query, filtros começam em $2 (buildFilterClauses já foi chamado com startIdx=2).
	var countArgs []interface{}
	if hasQuery {
		countArgs = append([]interface{}{queryArg}, filterArgs...)
	} else {
		// Reconstrói filtros a partir de $1 já que não há queryArg
		filterSQL, filterArgs, nextIdx = buildFilterClauses(req, 1)
		baseWhere = `
			ci.status = 'active'
			AND ci.deleted_at IS NULL
			AND (ci.valid_until IS NULL OR ci.valid_until > NOW())
			` + filterSQL
		countArgs = filterArgs
		_ = nextIdx
	}

	countSQL := fmt.Sprintf(`
		SELECT COUNT(*) FROM catalog_items ci
		WHERE %s %s
	`, baseWhere, tsCondition)

	var total int
	if err := r.db.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("search count: %w", err)
	}
	if total == 0 {
		return []*SearchResult{}, 0, nil
	}

	offset := (req.Page - 1) * req.PerPage
	mainArgs := append(countArgs, req.PerPage, offset)
	limitArgIdx := len(mainArgs) - 1 // índice do LIMIT (1-based)
	limitClause := fmt.Sprintf("LIMIT $%d OFFSET $%d", limitArgIdx, limitArgIdx+1)

	mainSQL := fmt.Sprintf(`
		SELECT
			ci.id, ci.external_id, ci.source, ci.type,
			ci.title, ci.description, ci.short_desc,
			ci.organization, ci.url, ci.image_url,
			ci.target_audience, ci.bairros, ci.modalidade,
			ci.status, ci.tags, ci.source_data,
			ci.valid_from, ci.valid_until, ci.source_updated_at,
			ci.created_at, ci.updated_at,
			(%s) AS rank,
			(%s) AS headline
		FROM catalog_items ci
		WHERE %s %s
		%s
		%s
	`, rankExpr, headlineExpr, baseWhere, tsCondition, orderBy, limitClause)

	return r.scanResults(ctx, mainSQL, mainArgs)
}

// SearchHybrid executa busca híbrida: RRF entre FTS melhorado e busca vetorial.
// queryEmbedding é o embedding da query (gerado pelo GeminiEmbeddingClient).
// Pesos RRF: FTS=1.0, semântico=2.0, k=60.
func (r *SearchRepository) SearchHybrid(ctx context.Context, req *models.SearchRequest, queryEmbedding string) ([]*SearchResult, int, error) {
	// Args do count: $1=expandedQuery, $2...$N=filtros
	// Args do main:  $1=expandedQuery, $2=rawQuery(trigram), $3=vector, $4...$N=filtros, $N+1=limit, $N+2=offset
	filterSQL, filterArgs, nextIdx := buildFilterClauses(req, 4) // FTS=$1, trigram=$2, vector=$3

	baseWhere := `
		ci.status = 'active'
		AND ci.deleted_at IS NULL
		AND (ci.valid_until IS NULL OR ci.valid_until > NOW())
		` + filterSQL

	// Count: conta apenas pelo FTS (aproximação padrão de search engines)
	countFilterSQL, countFilterArgs, countNextIdx := buildFilterClauses(req, 2)
	_ = countNextIdx
	countWhere := `
		ci.status = 'active'
		AND ci.deleted_at IS NULL
		AND (ci.valid_until IS NULL OR ci.valid_until > NOW())
		` + countFilterSQL

	countArgs := make([]interface{}, 0, 1+len(countFilterArgs))
	countArgs = append(countArgs, req.Q)
	countArgs = append(countArgs, countFilterArgs...)

	countSQL := fmt.Sprintf(`
		SELECT COUNT(*) FROM catalog_items ci
		WHERE %s
		  AND ci.search_vector @@ websearch_to_tsquery('portuguese', unaccent($1))
	`, countWhere)

	var total int
	if err := r.db.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("hybrid search count: %w", err)
	}

	offset := (req.Page - 1) * req.PerPage

	mainArgs := make([]interface{}, 0, 3+len(filterArgs)+2)
	mainArgs = append(mainArgs, req.Q, req.Q, queryEmbedding)
	mainArgs = append(mainArgs, filterArgs...)
	mainArgs = append(mainArgs, req.PerPage, offset)

	limitClause := fmt.Sprintf("LIMIT $%d OFFSET $%d", nextIdx, nextIdx+1)

	mainSQL := fmt.Sprintf(`
		WITH
		fts AS (
			SELECT
				ci.id,
				ts_rank_cd('{0.05,0.1,0.3,1.0}', ci.search_vector,
					websearch_to_tsquery('portuguese', unaccent($1)), 32)
				+ similarity(unaccent(lower(ci.title)), unaccent(lower($2))) * 0.3 AS score,
				ts_headline('portuguese',
					ci.title || ' ' || COALESCE(ci.short_desc, ''),
					websearch_to_tsquery('portuguese', unaccent($1)),
					'StartSel=<mark>,StopSel=</mark>,MaxFragments=2,MaxWords=15,MinWords=5'
				) AS headline,
				ROW_NUMBER() OVER (
					ORDER BY
						ts_rank_cd('{0.05,0.1,0.3,1.0}', ci.search_vector,
							websearch_to_tsquery('portuguese', unaccent($1)), 32)
						+ similarity(unaccent(lower(ci.title)), unaccent(lower($2))) * 0.3 DESC
				) AS rn
			FROM catalog_items ci
			WHERE ci.search_vector @@ websearch_to_tsquery('portuguese', unaccent($1))
			  AND %s
			LIMIT 50
		),
		sem AS (
			SELECT
				ci.id,
				1.0 - (ci.embedding <=> $3::vector) AS score,
				ROW_NUMBER() OVER (ORDER BY ci.embedding <=> $3::vector) AS rn
			FROM catalog_items ci
			WHERE ci.embedding IS NOT NULL
			  AND %s
			ORDER BY ci.embedding <=> $3::vector
			LIMIT 50
		),
		rrf AS (
			SELECT
				COALESCE(f.id, s.id)     AS id,
				COALESCE(f.headline, '') AS headline,
				COALESCE(1.0 / (60.0 + f.rn), 0.0) * 1.0   -- peso FTS
				+ COALESCE(1.0 / (60.0 + s.rn), 0.0) * 2.0 AS rrf_score  -- peso semântico
			FROM fts f
			FULL OUTER JOIN sem s ON f.id = s.id
		)
		SELECT
			ci.id, ci.external_id, ci.source, ci.type,
			ci.title, ci.description, ci.short_desc,
			ci.organization, ci.url, ci.image_url,
			ci.target_audience, ci.bairros, ci.modalidade,
			ci.status, ci.tags, ci.source_data,
			ci.valid_from, ci.valid_until, ci.source_updated_at,
			ci.created_at, ci.updated_at,
			r.rrf_score AS rank,
			r.headline
		FROM rrf r
		JOIN catalog_items ci ON ci.id = r.id
		ORDER BY r.rrf_score DESC
		%s
	`, baseWhere, baseWhere, limitClause)

	results, _, err := r.scanResults(ctx, mainSQL, mainArgs)
	if err != nil {
		return nil, 0, err
	}
	return results, total, nil
}

func (r *SearchRepository) scanResults(ctx context.Context, sql string, args []interface{}) ([]*SearchResult, int, error) {
	rows, err := r.db.Query(ctx, sql, args...)
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

		if err := rows.Scan(
			&item.ID, &item.ExternalID, &source, &itemType,
			&item.Title, &item.Description, &item.ShortDesc,
			&item.Organization, &item.URL, &item.ImageURL,
			&item.TargetAudience, &item.Bairros, &item.Modalidade,
			&status, &item.Tags, &item.SourceData,
			&item.ValidFrom, &item.ValidUntil, &item.SourceUpdatedAt,
			&item.CreatedAt, &item.UpdatedAt,
			&rank, &headline,
		); err != nil {
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
	return results, len(results), rows.Err()
}
