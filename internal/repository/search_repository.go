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

	// Typesense é fonte temporária da Carta; a busca pública usa SalesForce/CloudHub.
	clauses = append(clauses, fmt.Sprintf("ci.source <> '%s'", models.SourceTypesense))

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

// Ordenações da busca. Cada página é uma query própria com LIMIT/OFFSET, então a
// ordem precisa ser total: sem desempate único, linhas empatadas (ex.: o mesmo
// created_at gravado por lote no UpsertBatch, ou o mesmo rank) podem trocar de
// posição entre páginas, repetindo itens e omitindo outros. ci.id (PK) desempata.
const (
	searchOrderByRecency = "ORDER BY ci.created_at DESC, ci.id"
	searchOrderByRank    = "ORDER BY rank DESC, ci.id"
	hybridOrderByRRF     = "ORDER BY r.rrf_score DESC, ci.id"
)

// searchOrderBy devolve o ORDER BY de Search: por relevância quando há texto de
// busca, por recência quando não há.
func searchOrderBy(hasQuery bool) string {
	if hasQuery {
		return searchOrderByRank
	}
	return searchOrderByRecency
}

// Search executa busca FTS melhorada com ts_rank_cd + boost de similaridade de título.
// Usado como fallback quando embedding não está disponível.
func (r *SearchRepository) Search(ctx context.Context, req *models.SearchRequest) ([]*SearchResult, int, error) {
	filterSQL, filterArgs, _ := buildFilterClauses(req, 2) // $1 reservado para a query

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
		orderBy = searchOrderBy(true)
		queryArg = req.Q
	} else {
		rankExpr = "1.0"
		headlineExpr = "''"
		tsCondition = ""
		orderBy = searchOrderBy(false)
	}

	// Quando não há query, os args dos filtros começam em $1 (não há $1 reservado para a query).
	// Quando há query, filtros começam em $2 (buildFilterClauses já foi chamado com startIdx=2).
	var countArgs []interface{}
	if hasQuery {
		countArgs = append([]interface{}{queryArg}, filterArgs...)
	} else {
		// Reconstrói filtros a partir de $1 já que não há queryArg
		filterSQL, filterArgs, _ = buildFilterClauses(req, 1)
		baseWhere = `
			ci.status = 'active'
			AND ci.deleted_at IS NULL
			AND (ci.valid_until IS NULL OR ci.valid_until > NOW())
			` + filterSQL
		countArgs = filterArgs
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

	results, err := r.scanResults(ctx, mainSQL, mainArgs)
	if err != nil {
		return nil, 0, err
	}
	return results, total, nil
}

// hybridFTSRank é o rank FTS das buscas híbridas: ts_rank_cd com pesos
// {D,C,B,A} sobre a query expandida ($1) + similaridade de título com a query
// crua ($2).
const hybridFTSRank = `ts_rank_cd('{0.05,0.1,0.3,1.0}', ci.search_vector,
					websearch_to_tsquery('portuguese', unaccent($1)), 32)
				+ similarity(unaccent(lower(ci.title)), unaccent(lower($2))) * 0.3`

// hybridFTSCTE devolve a CTE fts das buscas híbridas: as 50 linhas de maior rank
// FTS. Cada página é uma query própria com LIMIT/OFFSET, então o conjunto que
// entra no RRF precisa ser o mesmo em toda página: o ORDER BY antes do LIMIT 50
// escolhe as de maior rank (sem ele, o LIMIT pega 50 correspondências quaisquer)
// e ci.id desempata o ROW_NUMBER(), cuja posição pesa no RRF.
func hybridFTSCTE(where string) string {
	return fmt.Sprintf(`
		fts AS (
			SELECT
				ci.id,
				%[1]s AS score,
				ts_headline('portuguese',
					ci.title || ' ' || COALESCE(ci.short_desc, ''),
					websearch_to_tsquery('portuguese', unaccent($1)),
					'StartSel=<mark>,StopSel=</mark>,MaxFragments=2,MaxWords=15,MinWords=5'
				) AS headline,
				ROW_NUMBER() OVER (
					ORDER BY %[1]s DESC, ci.id
				) AS rn
			FROM catalog_items ci
			WHERE ci.search_vector @@ websearch_to_tsquery('portuguese', unaccent($1))
			  AND %[2]s
			ORDER BY rn
			LIMIT 50
		)`, hybridFTSRank, where)
}

// hybridSemanticCTE devolve uma CTE com os 50 vizinhos mais próximos do vetor
// vectorParam (ex.: "$3").
func hybridSemanticCTE(name, vectorParam, where string) string {
	return fmt.Sprintf(`
		%[1]s AS (
			SELECT
				ci.id,
				1.0 - (ci.embedding <=> %[2]s::vector) AS score,
				ROW_NUMBER() OVER (ORDER BY ci.embedding <=> %[2]s::vector) AS rn
			FROM catalog_items ci
			WHERE ci.embedding IS NOT NULL
			  AND %[3]s
			ORDER BY ci.embedding <=> %[2]s::vector
			LIMIT 50
		)`, name, vectorParam, where)
}

// hybridSearchCTEs monta as CTEs de SearchHybrid: fts + sem fundidas por RRF.
// Args: $1=expandedQuery, $2=rawQuery(trigram), $3=vector, depois os filtros.
func hybridSearchCTEs(where string) string {
	return `
		WITH` + hybridFTSCTE(where) + `,` + hybridSemanticCTE("sem", "$3", where) + `,
		rrf AS (
			SELECT
				COALESCE(f.id, s.id)     AS id,
				COALESCE(f.headline, '') AS headline,
				COALESCE(1.0 / (60.0 + f.rn), 0.0) * 1.0   -- peso FTS
				+ COALESCE(1.0 / (60.0 + s.rn), 0.0) * 2.0 AS rrf_score  -- peso semântico
			FROM fts f
			FULL OUTER JOIN sem s ON f.id = s.id
		)`
}

// hydeSearchCTEs monta as CTEs de SearchHybridWithHyDE: fts + sem + hyde
// fundidas por RRF. Args: $1=expandedQ, $2=rawQ(trigram), $3=queryVec,
// $4=hydeVec, depois os filtros.
func hydeSearchCTEs(where string) string {
	return `
		WITH` + hybridFTSCTE(where) + `,` + hybridSemanticCTE("sem", "$3", where) + `,` +
		hybridSemanticCTE("hyde", "$4", where) + `,
		rrf AS (
			SELECT
				COALESCE(f.id, s.id, h.id) AS id,
				COALESCE(f.headline, '')   AS headline,
				COALESCE(1.0 / (60.0 + f.rn), 0.0) * 1.0   -- FTS
				+ COALESCE(1.0 / (60.0 + s.rn), 0.0) * 2.0  -- query vec
				+ COALESCE(1.0 / (60.0 + h.rn), 0.0) * 1.5  -- hyde vec
				AS rrf_score
			FROM fts f
			FULL OUTER JOIN sem s ON f.id = s.id
			FULL OUTER JOIN hyde h ON COALESCE(f.id, s.id) = h.id
		)`
}

// hybridPageSQL devolve a página da lista RRF montada por ctes. COUNT(*) OVER ()
// é calculado antes do LIMIT/OFFSET: é o tamanho da lista inteira, que vira o
// total da resposta.
func hybridPageSQL(ctes, limitClause string) string {
	return ctes + fmt.Sprintf(`
		SELECT
			ci.id, ci.external_id, ci.source, ci.type,
			ci.title, ci.description, ci.short_desc,
			ci.organization, ci.url, ci.image_url,
			ci.target_audience, ci.bairros, ci.modalidade,
			ci.status, ci.tags, ci.source_data,
			ci.valid_from, ci.valid_until, ci.source_updated_at,
			ci.created_at, ci.updated_at,
			r.rrf_score AS rank,
			r.headline,
			COUNT(*) OVER () AS rrf_total
		FROM rrf r
		JOIN catalog_items ci ON ci.id = r.id
		%s
		%s
	`, hybridOrderByRRF, limitClause)
}

// hybridCountSQL conta a lista RRF montada por ctes.
func hybridCountSQL(ctes string) string {
	return ctes + `
		SELECT COUNT(*) FROM rrf
	`
}

// searchHybridPage executa a página da busca híbrida e devolve os resultados e o
// total, que é o tamanho da lista RRF (o que a paginação alcança), não a
// contagem do FTS: essa pode passar do tamanho da lista (no máximo 50 por
// fonte) ou ser zero com itens semânticos na página. args termina com limit e
// offset. Uma página além do fim não traz linha com o total, então a lista é
// contada à parte.
func (r *SearchRepository) searchHybridPage(ctx context.Context, ctes, limitClause string, args []interface{}, offset int) ([]*SearchResult, int, error) {
	results, total, err := r.queryResults(ctx, hybridPageSQL(ctes, limitClause), args, true)
	if err != nil {
		return nil, 0, err
	}
	if len(results) > 0 || offset == 0 {
		return results, total, nil
	}
	countArgs := args[:len(args)-2] // sem limit e offset
	if err := r.db.QueryRow(ctx, hybridCountSQL(ctes), countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("hybrid search count: %w", err)
	}
	return results, total, nil
}

// SearchHybrid executa busca híbrida: RRF entre FTS melhorado e busca vetorial.
// queryEmbedding é o embedding da query (gerado pelo GeminiEmbeddingClient).
// Pesos RRF: FTS=1.0, semântico=2.0, k=60.
func (r *SearchRepository) SearchHybrid(ctx context.Context, req *models.SearchRequest, queryEmbedding string) ([]*SearchResult, int, error) {
	// Args: $1=expandedQuery, $2=rawQuery(trigram), $3=vector, $4...$N=filtros, $N+1=limit, $N+2=offset
	filterSQL, filterArgs, nextIdx := buildFilterClauses(req, 4) // FTS=$1, trigram=$2, vector=$3

	baseWhere := `
		ci.status = 'active'
		AND ci.deleted_at IS NULL
		AND (ci.valid_until IS NULL OR ci.valid_until > NOW())
		` + filterSQL

	offset := (req.Page - 1) * req.PerPage

	mainArgs := make([]interface{}, 0, 3+len(filterArgs)+2)
	mainArgs = append(mainArgs, req.Q, req.Q, queryEmbedding)
	mainArgs = append(mainArgs, filterArgs...)
	mainArgs = append(mainArgs, req.PerPage, offset)

	limitClause := fmt.Sprintf("LIMIT $%d OFFSET $%d", nextIdx, nextIdx+1)

	return r.searchHybridPage(ctx, hybridSearchCTEs(baseWhere), limitClause, mainArgs, offset)
}

// SearchHybridWithHyDE executa busca com 3 listas no RRF: FTS + query_vec + hyde_vec.
// hydeEmbedding é o embedding do documento hipotético gerado pelo HyDE.
// Pesos: FTS=1.0, query_vec=2.0, hyde_vec=1.5
func (r *SearchRepository) SearchHybridWithHyDE(ctx context.Context, req *models.SearchRequest, queryEmbedding, hydeEmbedding string) ([]*SearchResult, int, error) {
	// Args: $1=expandedQ, $2=rawQ(trigram), $3=queryVec, $4=hydeVec, $5...$N=filtros, $N+1=limit, $N+2=offset
	filterSQL, filterArgs, nextIdx := buildFilterClauses(req, 5)

	baseWhere := `
		ci.status = 'active'
		AND ci.deleted_at IS NULL
		AND (ci.valid_until IS NULL OR ci.valid_until > NOW())
		` + filterSQL

	offset := (req.Page - 1) * req.PerPage
	mainArgs := make([]interface{}, 0, 4+len(filterArgs)+2)
	mainArgs = append(mainArgs, req.Q, req.Q, queryEmbedding, hydeEmbedding)
	mainArgs = append(mainArgs, filterArgs...)
	mainArgs = append(mainArgs, req.PerPage, offset)

	limitClause := fmt.Sprintf("LIMIT $%d OFFSET $%d", nextIdx, nextIdx+1)

	return r.searchHybridPage(ctx, hydeSearchCTEs(baseWhere), limitClause, mainArgs, offset)
}

func (r *SearchRepository) scanResults(ctx context.Context, sql string, args []interface{}) ([]*SearchResult, error) {
	results, _, err := r.queryResults(ctx, sql, args, false)
	return results, err
}

// queryResults executa sql e lê os resultados. Com withTotal, cada linha traz
// depois do headline uma coluna com o total da lista (COUNT(*) OVER ()), que é
// devolvida; sem linhas, o total é zero.
func (r *SearchRepository) queryResults(ctx context.Context, sql string, args []interface{}, withTotal bool) ([]*SearchResult, int, error) {
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	var results []*SearchResult
	var total int
	for rows.Next() {
		var item models.CatalogItem
		var source, itemType, status string
		var rank float64
		var headline string
		var rowTotal int64

		dest := []interface{}{
			&item.ID, &item.ExternalID, &source, &itemType,
			&item.Title, &item.Description, &item.ShortDesc,
			&item.Organization, &item.URL, &item.ImageURL,
			&item.TargetAudience, &item.Bairros, &item.Modalidade,
			&status, &item.Tags, &item.SourceData,
			&item.ValidFrom, &item.ValidUntil, &item.SourceUpdatedAt,
			&item.CreatedAt, &item.UpdatedAt,
			&rank, &headline,
		}
		if withTotal {
			dest = append(dest, &rowTotal)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, 0, fmt.Errorf("search scan: %w", err)
		}
		if withTotal {
			total = int(rowTotal)
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
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return results, total, nil
}
