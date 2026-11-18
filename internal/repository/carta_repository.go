package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

// CartaRepository lê/escreve a hierarquia local da Carta de Serviços.
type CartaRepository struct {
	db *pgxpool.Pool
}

func NewCartaRepository(db *pgxpool.Pool) *CartaRepository {
	return &CartaRepository{db: db}
}

const CartaMaxPage = 1000

// ClampCartaPage normaliza page/per_page da Carta (page máx. 1000, per_page máx. 100).
func ClampCartaPage(page, perPage int) (int, int) {
	if page < 1 {
		page = 1
	}
	if page > CartaMaxPage {
		page = CartaMaxPage
	}
	if perPage < 1 {
		perPage = 100
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

func clampCartaPage(page, perPage int) (int, int) {
	return ClampCartaPage(page, perPage)
}

func cartaMeta(page, perPage, total int) models.CartaMeta {
	pages := 0
	if total > 0 {
		pages = int(math.Ceil(float64(total) / float64(perPage)))
	}
	return models.CartaMeta{Page: page, PerPage: perPage, Total: total, TotalPages: pages}
}

// cartaPublishedSQL é a condição de um serviço publicado da Carta: da fonte
// SalesForce, não removido, ativo e vigente (inForceSQL: já começou e ainda não
// terminou). Toda leitura pública da Carta e as contagens da hierarquia a usam,
// para que listas, detalhe e contagens mostrem os mesmos serviços (DEV-622).
// prefix é o alias da tabela com o ponto ("ci.") ou "" sem alias.
func cartaPublishedSQL(prefix string) string {
	return prefix + "source = 'salesforce'" +
		" AND " + prefix + "deleted_at IS NULL" +
		" AND " + prefix + "status = 'active'" +
		" AND " + inForceSQL(prefix)
}

var (
	refreshSubthemeCountsSQL = `
		UPDATE carta_subthemes s SET
			published_services = (
				SELECT COUNT(*)::INT FROM catalog_items ci
				WHERE ` + cartaPublishedSQL("ci.") + `
				  AND ci.subtheme_slug = s.slug
			),
			updated_at = NOW()
		WHERE s.deleted_at IS NULL`

	refreshThemeCountsSQL = `
		UPDATE carta_themes t SET
			published_services = (
				SELECT COUNT(*)::INT FROM catalog_items ci
				WHERE ` + cartaPublishedSQL("ci.") + `
				  AND ci.theme_slug = t.slug
			),
			subthemes_count = (
				SELECT COUNT(*)::INT FROM carta_subthemes s
				WHERE s.theme_slug = t.slug AND s.deleted_at IS NULL
			),
			updated_at = NOW()
		WHERE t.deleted_at IS NULL`

	countServicesBySubthemeSQL = `
		SELECT COUNT(*) FROM catalog_items
		WHERE ` + cartaPublishedSQL("") + `
		  AND subtheme_slug = $1`

	listServicesBySubthemeSQL = `
		SELECT external_id, title, COALESCE(short_desc, ''), COALESCE(organization, ''),
		       status, COALESCE(theme_slug, ''), COALESCE(subtheme_slug, ''),
		       source_data
		FROM catalog_items
		WHERE ` + cartaPublishedSQL("") + `
		  AND subtheme_slug = $1
		ORDER BY title ASC
		LIMIT $2 OFFSET $3`

	// Uma única agregação (CTE) + COUNT(*) OVER() evita o GROUP BY duplicado.
	listOrgUnitsSQL = `
		WITH orgs AS (
			SELECT
				sici_code,
				COALESCE(NULLIF(MAX(organization), ''), sici_code) AS responsible_org_unit,
				COALESCE(MAX(source_data->>'responsibleOrgUnitShortName'), '') AS short_name,
				COUNT(*)::INT AS published_services
			FROM catalog_items
			WHERE ` + cartaPublishedSQL("") + `
			  AND sici_code IS NOT NULL
			  AND sici_code <> ''
			GROUP BY sici_code
		)
		SELECT
			sici_code,
			responsible_org_unit,
			short_name,
			published_services,
			COUNT(*) OVER()::INT AS total
		FROM orgs
		ORDER BY responsible_org_unit ASC
		LIMIT $1 OFFSET $2`

	countOrgUnitsSQL = `
		SELECT COUNT(*) FROM (
			SELECT sici_code
			FROM catalog_items
			WHERE ` + cartaPublishedSQL("") + `
			  AND sici_code IS NOT NULL
			  AND sici_code <> ''
			GROUP BY sici_code
		) AS orgs`

	countServicesBySiciCodeSQL = `
		SELECT COUNT(*) FROM catalog_items
		WHERE ` + cartaPublishedSQL("") + `
		  AND sici_code = $1`

	listServicesBySiciCodeSQL = `
		SELECT source_data
		FROM catalog_items
		WHERE ` + cartaPublishedSQL("") + `
		  AND sici_code = $1
		ORDER BY title ASC
		LIMIT $2 OFFSET $3`

	countAllServicesSQL = `
		SELECT COUNT(*) FROM catalog_items
		WHERE ` + cartaPublishedSQL("")

	listAllServicesSQL = `
		SELECT source_data
		FROM catalog_items
		WHERE ` + cartaPublishedSQL("") + `
		ORDER BY sici_code NULLS FIRST, title ASC
		LIMIT $1 OFFSET $2`

	getServiceDetailSQL = `
		SELECT source_data
		FROM catalog_items
		WHERE ` + cartaPublishedSQL("") + `
		  AND external_id = $1
		LIMIT 1`
)

// UpsertTheme grava/atualiza um tema e limpa soft-delete.
func (r *CartaRepository) UpsertTheme(ctx context.Context, theme *models.CartaTheme) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO carta_themes (slug, name, subthemes_count, published_services)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (slug) DO UPDATE SET
			name = EXCLUDED.name,
			subthemes_count = EXCLUDED.subthemes_count,
			published_services = EXCLUDED.published_services,
			updated_at = NOW(),
			deleted_at = NULL
	`, theme.Slug, theme.Name, theme.SubthemesCount, theme.PublishedServices)
	return err
}

// UpsertSubtheme grava/atualiza um subtema.
func (r *CartaRepository) UpsertSubtheme(ctx context.Context, st *models.CartaSubtheme) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO carta_subthemes (slug, theme_slug, name, published_services)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (slug) DO UPDATE SET
			theme_slug = EXCLUDED.theme_slug,
			name = EXCLUDED.name,
			published_services = EXCLUDED.published_services,
			updated_at = NOW(),
			deleted_at = NULL
	`, st.Slug, st.ThemeSlug, st.Name, st.PublishedServices)
	return err
}

// ReplaceHomeFeatured substitui o snapshot editorial da home numa transação (delete + copy).
func (r *CartaRepository) ReplaceHomeFeatured(ctx context.Context, items []models.CartaHomeFeatured) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM carta_home_featured`); err != nil {
		return err
	}

	if len(items) > 0 {
		syncedAt := time.Now().UTC()
		rows := make([][]any, len(items))
		for i, item := range items {
			rows[i] = []any{
				item.Slug, item.Position, item.Name, item.Summary,
				item.ThemeSlug, item.ThemeName, item.SubthemeSlug, item.SubthemeName,
				item.LastPublishedDate, item.AutoserviceCategory, syncedAt,
			}
		}
		_, err = tx.CopyFrom(
			ctx,
			pgx.Identifier{"carta_home_featured"},
			[]string{
				"slug", "position", "name", "summary",
				"theme_slug", "theme_name", "subtheme_slug", "subtheme_name",
				"last_published_date", "autoservice_category", "synced_at",
			},
			pgx.CopyFromRows(rows),
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// ListHomeFeatured retorna os destaques editoriais ordenados por position (desempate por slug).
func (r *CartaRepository) ListHomeFeatured(ctx context.Context) ([]models.CartaHomeFeatured, error) {
	rows, err := r.db.Query(ctx, `
		SELECT slug, position, name, summary,
		       theme_slug, theme_name, subtheme_slug, subtheme_name,
		       last_published_date, autoservice_category, synced_at
		FROM carta_home_featured
		ORDER BY position ASC, slug ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]models.CartaHomeFeatured, 0)
	for rows.Next() {
		var item models.CartaHomeFeatured
		if err := rows.Scan(
			&item.Slug, &item.Position, &item.Name, &item.Summary,
			&item.ThemeSlug, &item.ThemeName, &item.SubthemeSlug, &item.SubthemeName,
			&item.LastPublishedDate, &item.AutoserviceCategory, &item.SyncedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// SoftDeleteThemesNotIn marca temas fora da lista como deletados.
func (r *CartaRepository) SoftDeleteThemesNotIn(ctx context.Context, keep []string) (int64, error) {
	if len(keep) == 0 {
		tag, err := r.db.Exec(ctx, `
			UPDATE carta_themes SET deleted_at = NOW(), updated_at = NOW()
			WHERE deleted_at IS NULL`)
		return tag.RowsAffected(), err
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE carta_themes SET deleted_at = NOW(), updated_at = NOW()
		WHERE deleted_at IS NULL AND NOT (slug = ANY($1))`, keep)
	return tag.RowsAffected(), err
}

// PropagateHierarchyLabels atualiza theme/subtheme (slug + nome) em catalog_items e
// carta_home_featured a partir da hierarquia CloudHub. Cobre renomes e mudanças de
// vínculo sem depender do LastModified dos serviços.
func (r *CartaRepository) PropagateHierarchyLabels(ctx context.Context, labels []models.CartaHierarchyLabel) (int64, error) {
	var updated int64
	for _, label := range labels {
		if len(label.ServiceSlugs) == 0 {
			continue
		}
		themeSlug := strings.TrimSpace(label.ThemeSlug)
		subthemeSlug := strings.TrimSpace(label.SubthemeSlug)
		themeName := strings.TrimSpace(label.ThemeName)
		subthemeName := strings.TrimSpace(label.SubthemeName)
		if themeSlug == "" || subthemeSlug == "" {
			continue
		}
		if themeName == "" {
			themeName = themeSlug
		}
		if subthemeName == "" {
			subthemeName = subthemeSlug
		}

		tag, err := r.db.Exec(ctx, `
			UPDATE catalog_items
			SET
				theme_slug = $1,
				subtheme_slug = $2,
				source_data = CASE
					WHEN source_data IS NULL OR source_data = 'null'::jsonb THEN source_data
					ELSE jsonb_set(jsonb_set(jsonb_set(jsonb_set(
						source_data,
						'{themeSlug}', to_jsonb($1::text), true),
						'{themeName}', to_jsonb($3::text), true),
						'{subthemeSlug}', to_jsonb($2::text), true),
						'{subthemeName}', to_jsonb($4::text), true)
				END,
				tags = ARRAY_REMOVE(ARRAY[
					NULLIF($3::text, ''),
					NULLIF($4::text, ''),
					COALESCE(
						NULLIF(source_data#>>'{info,cost}', ''),
						CASE
							WHEN COALESCE((source_data#>>'{info,isFree}')::boolean, false) THEN 'Gratuito'
							ELSE NULL
						END
					),
					NULLIF(source_data->>'responsibleOrgUnitShortName', '')
				], NULL),
				updated_at = NOW()
			WHERE source = 'salesforce'
			  AND deleted_at IS NULL
			  AND external_id = ANY($5)
			  AND (
				theme_slug IS DISTINCT FROM $1
				OR subtheme_slug IS DISTINCT FROM $2
				OR COALESCE(source_data->>'themeSlug', '') IS DISTINCT FROM $1
				OR COALESCE(source_data->>'themeName', '') IS DISTINCT FROM $3
				OR COALESCE(source_data->>'subthemeSlug', '') IS DISTINCT FROM $2
				OR COALESCE(source_data->>'subthemeName', '') IS DISTINCT FROM $4
			  )`,
			themeSlug, subthemeSlug, themeName, subthemeName, label.ServiceSlugs,
		)
		if err != nil {
			return updated, fmt.Errorf("propagar hierarquia no subtema %q: %w", subthemeSlug, err)
		}
		updated += tag.RowsAffected()

		_, err = r.db.Exec(ctx, `
			UPDATE carta_home_featured
			SET
				theme_slug = $1,
				theme_name = $3,
				subtheme_slug = $2,
				subtheme_name = $4
			WHERE slug = ANY($5)
			  AND (
				theme_slug IS DISTINCT FROM $1
				OR subtheme_slug IS DISTINCT FROM $2
				OR theme_name IS DISTINCT FROM $3
				OR subtheme_name IS DISTINCT FROM $4
			  )`,
			themeSlug, subthemeSlug, themeName, subthemeName, label.ServiceSlugs,
		)
		if err != nil {
			return updated, fmt.Errorf("propagar hierarquia no home_featured do subtema %q: %w", subthemeSlug, err)
		}
	}
	return updated, nil
}

// SoftDeleteSubthemesNotInTheme marca subtemas do tema fora da lista keep como deletados.
// Escopo por tema evita apagar subtemas de outros temas quando a listagem de um tema falha parcialmente.
func (r *CartaRepository) SoftDeleteSubthemesNotInTheme(ctx context.Context, themeSlug string, keep []string) (int64, error) {
	themeSlug = strings.TrimSpace(themeSlug)
	if themeSlug == "" {
		return 0, fmt.Errorf("theme slug vazio")
	}
	if len(keep) == 0 {
		tag, err := r.db.Exec(ctx, `
			UPDATE carta_subthemes SET deleted_at = NOW(), updated_at = NOW()
			WHERE deleted_at IS NULL AND theme_slug = $1`, themeSlug)
		return tag.RowsAffected(), err
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE carta_subthemes SET deleted_at = NOW(), updated_at = NOW()
		WHERE deleted_at IS NULL AND theme_slug = $1 AND NOT (slug = ANY($2))`, themeSlug, keep)
	return tag.RowsAffected(), err
}

// RefreshHierarchyCounts recria subtemas/temas órfãos a partir de catalog_items e
// recalcula contagens com os mesmos critérios das listas públicas (cartaPublishedSQL).
func (r *CartaRepository) RefreshHierarchyCounts(ctx context.Context) error {
	if err := r.healHierarchyFromCatalogItems(ctx); err != nil {
		return err
	}
	if _, err := r.db.Exec(ctx, refreshSubthemeCountsSQL); err != nil {
		return err
	}
	_, err := r.db.Exec(ctx, refreshThemeCountsSQL)
	return err
}

// healHierarchyFromCatalogItems garante linhas ativas em carta_themes/carta_subthemes
// para todo par (theme_slug, subtheme_slug) presente em serviços Salesforce publicados e vigentes.
func (r *CartaRepository) healHierarchyFromCatalogItems(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO carta_themes (slug, name, subthemes_count, published_services)
		SELECT
			ci.theme_slug,
			COALESCE(
				NULLIF(MAX(ci.source_data->>'themeName'), ''),
				ci.theme_slug
			),
			0,
			0
		FROM catalog_items ci
		WHERE `+cartaPublishedSQL("ci.")+`
		  AND ci.theme_slug IS NOT NULL
		  AND ci.theme_slug <> ''
		GROUP BY ci.theme_slug
		ON CONFLICT (slug) DO UPDATE SET
			deleted_at = NULL,
			updated_at = NOW()`)
	if err != nil {
		return fmt.Errorf("heal temas a partir de catalog_items: %w", err)
	}

	_, err = r.db.Exec(ctx, `
		INSERT INTO carta_subthemes (slug, theme_slug, name, published_services)
		SELECT
			sub.subtheme_slug,
			sub.theme_slug,
			sub.subtheme_name,
			0
		FROM (
			SELECT DISTINCT ON (ci.subtheme_slug)
				ci.subtheme_slug,
				ci.theme_slug,
				COALESCE(
					NULLIF(ci.source_data->>'subthemeName', ''),
					ci.subtheme_slug
				) AS subtheme_name
			FROM catalog_items ci
			WHERE `+cartaPublishedSQL("ci.")+`
			  AND ci.subtheme_slug IS NOT NULL
			  AND ci.subtheme_slug <> ''
			  AND ci.theme_slug IS NOT NULL
			  AND ci.theme_slug <> ''
			ORDER BY ci.subtheme_slug, ci.updated_at DESC NULLS LAST, ci.id DESC
		) sub
		ON CONFLICT (slug) DO UPDATE SET
			theme_slug = EXCLUDED.theme_slug,
			name = EXCLUDED.name,
			deleted_at = NULL,
			updated_at = NOW()`)
	if err != nil {
		return fmt.Errorf("heal subtemas a partir de catalog_items: %w", err)
	}
	return nil
}

// ListThemes pagina temas ativos.
func (r *CartaRepository) ListThemes(ctx context.Context, page, perPage int, includeEmpty bool) ([]models.CartaTheme, models.CartaMeta, error) {
	page, perPage = clampCartaPage(page, perPage)
	where := `deleted_at IS NULL`
	if !includeEmpty {
		where += ` AND published_services > 0`
	}

	var total int
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM carta_themes WHERE `+where).Scan(&total); err != nil {
		return nil, models.CartaMeta{}, err
	}

	offset := (page - 1) * perPage
	rows, err := r.db.Query(ctx, `
		SELECT slug, name, subthemes_count, published_services
		FROM carta_themes
		WHERE `+where+`
		ORDER BY name ASC
		LIMIT $1 OFFSET $2`, perPage, offset)
	if err != nil {
		return nil, models.CartaMeta{}, err
	}
	defer rows.Close()

	out := make([]models.CartaTheme, 0, perPage)
	for rows.Next() {
		var t models.CartaTheme
		if err := rows.Scan(&t.Slug, &t.Name, &t.SubthemesCount, &t.PublishedServices); err != nil {
			return nil, models.CartaMeta{}, err
		}
		out = append(out, t)
	}
	return out, cartaMeta(page, perPage, total), rows.Err()
}

// ListSubthemes pagina subtemas de um tema.
func (r *CartaRepository) ListSubthemes(ctx context.Context, themeSlug string, page, perPage int) ([]models.CartaSubtheme, models.CartaMeta, error) {
	themeSlug = strings.TrimSpace(themeSlug)
	if themeSlug == "" {
		return nil, models.CartaMeta{}, fmt.Errorf("theme slug vazio")
	}
	page, perPage = clampCartaPage(page, perPage)

	var total int
	if err := r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM carta_subthemes
		WHERE theme_slug = $1 AND deleted_at IS NULL`, themeSlug).Scan(&total); err != nil {
		return nil, models.CartaMeta{}, err
	}

	offset := (page - 1) * perPage
	rows, err := r.db.Query(ctx, `
		SELECT slug, theme_slug, name, published_services
		FROM carta_subthemes
		WHERE theme_slug = $1 AND deleted_at IS NULL
		ORDER BY name ASC
		LIMIT $2 OFFSET $3`, themeSlug, perPage, offset)
	if err != nil {
		return nil, models.CartaMeta{}, err
	}
	defer rows.Close()

	out := make([]models.CartaSubtheme, 0, perPage)
	for rows.Next() {
		var s models.CartaSubtheme
		if err := rows.Scan(&s.Slug, &s.ThemeSlug, &s.Name, &s.PublishedServices); err != nil {
			return nil, models.CartaMeta{}, err
		}
		out = append(out, s)
	}
	return out, cartaMeta(page, perPage, total), rows.Err()
}

// ListServicesBySubtheme pagina serviços publicados e vigentes de um subtema.
func (r *CartaRepository) ListServicesBySubtheme(ctx context.Context, subthemeSlug string, page, perPage int) ([]models.CartaServiceSummary, models.CartaMeta, error) {
	subthemeSlug = strings.TrimSpace(subthemeSlug)
	if subthemeSlug == "" {
		return nil, models.CartaMeta{}, fmt.Errorf("subtheme slug vazio")
	}
	page, perPage = clampCartaPage(page, perPage)

	var total int
	if err := r.db.QueryRow(ctx, countServicesBySubthemeSQL, subthemeSlug).Scan(&total); err != nil {
		return nil, models.CartaMeta{}, err
	}

	offset := (page - 1) * perPage
	rows, err := r.db.Query(ctx, listServicesBySubthemeSQL, subthemeSlug, perPage, offset)
	if err != nil {
		return nil, models.CartaMeta{}, err
	}
	defer rows.Close()

	out := make([]models.CartaServiceSummary, 0, perPage)
	for rows.Next() {
		var (
			sum        models.CartaServiceSummary
			status     string
			sourceData []byte
		)
		if err := rows.Scan(
			&sum.Slug, &sum.Name, &sum.Summary, &sum.ResponsibleOrgUnit,
			&status, &sum.ThemeSlug, &sum.SubthemeSlug, &sourceData,
		); err != nil {
			return nil, models.CartaMeta{}, err
		}
		sum.ArticleStatus = status
		enrichCartaServiceSummary(&sum, sourceData)
		out = append(out, sum)
	}
	return out, cartaMeta(page, perPage, total), rows.Err()
}

// enrichCartaServiceSummary preenche campos derivados do payload CloudHub em source_data.
func enrichCartaServiceSummary(sum *models.CartaServiceSummary, sourceData []byte) {
	if sum == nil || len(sourceData) == 0 {
		return
	}
	var detail map[string]any
	if err := json.Unmarshal(sourceData, &detail); err != nil || detail == nil {
		return
	}
	if v, ok := detail["serviceCatalogId"].(string); ok {
		sum.ServiceCatalogID = v
	}
	if v, ok := detail["articleId"].(string); ok {
		sum.ArticleID = v
	}
	if v, ok := detail["articleStatus"].(string); ok && v != "" {
		sum.ArticleStatus = v
	}
	if v, ok := detail["createdDate"].(string); ok {
		sum.CreatedDate = v
	}
	if v, ok := detail["lastModifiedDate"].(string); ok {
		sum.LastModifiedDate = v
	}
	if v, ok := detail["lastPublishedDate"].(string); ok {
		sum.LastPublishedDate = v
	}
	if v, ok := detail["responsibleOrgUnitShortName"].(string); ok {
		sum.ResponsibleOrgUnitShortName = v
	}
	if v, ok := detail["responsibleOrgUnitSiciCode"].(string); ok {
		code := v
		sum.ResponsibleOrgUnitSiciCode = &code
	}
	info, _ := detail["info"].(map[string]any)
	if info == nil {
		return
	}
	if v, ok := info["summary"].(string); ok && v != "" {
		sum.Summary = v
	}
	if v, ok := info["isFree"].(bool); ok {
		sum.IsFree = v
	}
}

// ListOrgUnits pagina órgãos com ao menos 1 serviço publicado e vigente e
// sici_code preenchido.
func (r *CartaRepository) ListOrgUnits(ctx context.Context, page, perPage int) ([]models.CartaOrgUnit, models.CartaMeta, error) {
	page, perPage = clampCartaPage(page, perPage)
	offset := (page - 1) * perPage

	rows, err := r.db.Query(ctx, listOrgUnitsSQL, perPage, offset)
	if err != nil {
		return nil, models.CartaMeta{}, err
	}
	defer rows.Close()

	out := make([]models.CartaOrgUnit, 0, perPage)
	total := 0
	for rows.Next() {
		var u models.CartaOrgUnit
		if err := rows.Scan(
			&u.ResponsibleOrgUnitSiciCode,
			&u.ResponsibleOrgUnit,
			&u.ResponsibleOrgUnitShortName,
			&u.PublishedServices,
			&total,
		); err != nil {
			return nil, models.CartaMeta{}, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, models.CartaMeta{}, err
	}
	// Página vazia (OFFSET além do fim) não traz COUNT(*) OVER(); consulta só o total.
	if len(out) == 0 {
		if err := r.db.QueryRow(ctx, countOrgUnitsSQL).Scan(&total); err != nil {
			return nil, models.CartaMeta{}, err
		}
	}
	return out, cartaMeta(page, perPage, total), nil
}

// ListServicesBySiciCode pagina o detalhe completo (source_data) dos serviços
// publicados e vigentes de um órgão.
// Código em branco retorna erro; código sem serviço retorna slice vazio.
func (r *CartaRepository) ListServicesBySiciCode(ctx context.Context, siciCode string, page, perPage int) ([]json.RawMessage, models.CartaMeta, error) {
	siciCode = strings.TrimSpace(siciCode)
	if siciCode == "" {
		return nil, models.CartaMeta{}, fmt.Errorf("sici code vazio")
	}
	page, perPage = clampCartaPage(page, perPage)

	var total int
	if err := r.db.QueryRow(ctx, countServicesBySiciCodeSQL, siciCode).Scan(&total); err != nil {
		return nil, models.CartaMeta{}, err
	}

	offset := (page - 1) * perPage
	rows, err := r.db.Query(ctx, listServicesBySiciCodeSQL, siciCode, perPage, offset)
	if err != nil {
		return nil, models.CartaMeta{}, err
	}
	defer rows.Close()

	out := make([]json.RawMessage, 0, perPage)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, models.CartaMeta{}, err
		}
		out = append(out, json.RawMessage(raw))
	}
	return out, cartaMeta(page, perPage, total), rows.Err()
}

// ListAllServices pagina o detalhe completo de todos os serviços publicados e
// vigentes da Carta.
// Serviços sem sici_code entram aqui; ordem por sici_code, depois title.
func (r *CartaRepository) ListAllServices(ctx context.Context, page, perPage int) ([]json.RawMessage, models.CartaMeta, error) {
	page, perPage = clampCartaPage(page, perPage)

	var total int
	if err := r.db.QueryRow(ctx, countAllServicesSQL).Scan(&total); err != nil {
		return nil, models.CartaMeta{}, err
	}

	offset := (page - 1) * perPage
	rows, err := r.db.Query(ctx, listAllServicesSQL, perPage, offset)
	if err != nil {
		return nil, models.CartaMeta{}, err
	}
	defer rows.Close()

	out := make([]json.RawMessage, 0, perPage)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, models.CartaMeta{}, err
		}
		out = append(out, json.RawMessage(raw))
	}
	return out, cartaMeta(page, perPage, total), rows.Err()
}

// UpsertSlugAlias grava/atualiza o mapeamento slug antigo → canônico.
// Também remove qualquer alias em que o canônico figure como old_slug (evita ciclo).
func (r *CartaRepository) UpsertSlugAlias(ctx context.Context, oldSlug, canonicalSlug string) error {
	oldSlug = strings.TrimSpace(oldSlug)
	canonicalSlug = strings.TrimSpace(canonicalSlug)
	if oldSlug == "" || canonicalSlug == "" || oldSlug == canonicalSlug {
		return nil
	}
	_, err := r.db.Exec(ctx, `
		INSERT INTO carta_service_slug_aliases (old_slug, canonical_slug)
		VALUES ($1, $2)
		ON CONFLICT (old_slug) DO UPDATE SET
			canonical_slug = EXCLUDED.canonical_slug,
			updated_at = NOW()
	`, oldSlug, canonicalSlug)
	if err != nil {
		return err
	}
	return r.DeleteSlugAlias(ctx, canonicalSlug)
}

// UpsertSlugAliases grava vários aliases apontando para o mesmo slug canônico.
func (r *CartaRepository) UpsertSlugAliases(ctx context.Context, canonicalSlug string, oldSlugs []string) error {
	canonicalSlug = strings.TrimSpace(canonicalSlug)
	if canonicalSlug == "" {
		return nil
	}
	for _, old := range oldSlugs {
		if err := r.UpsertSlugAlias(ctx, old, canonicalSlug); err != nil {
			return err
		}
	}
	// Garante limpeza mesmo quando slugHistory está vazio (só promove o canônico).
	return r.DeleteSlugAlias(ctx, canonicalSlug)
}

// DeleteSlugAlias remove um mapeamento cujo old_slug deixou de ser alias
// (ex.: o slug passou a ser o canônico do serviço).
func (r *CartaRepository) DeleteSlugAlias(ctx context.Context, oldSlug string) error {
	oldSlug = strings.TrimSpace(oldSlug)
	if oldSlug == "" {
		return nil
	}
	_, err := r.db.Exec(ctx, `
		DELETE FROM carta_service_slug_aliases WHERE old_slug = $1
	`, oldSlug)
	return err
}

// ResolveSlugAlias retorna o slug canônico para um slug antigo, se existir.
func (r *CartaRepository) ResolveSlugAlias(ctx context.Context, oldSlug string) (string, error) {
	oldSlug = strings.TrimSpace(oldSlug)
	if oldSlug == "" {
		return "", nil
	}
	var canonical string
	err := r.db.QueryRow(ctx, `
		SELECT canonical_slug
		FROM carta_service_slug_aliases
		WHERE old_slug = $1
		LIMIT 1`, oldSlug).Scan(&canonical)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return canonical, nil
}

// GetServiceDetail retorna o source_data do serviço publicado e vigente pelo
// slug (external_id); fora da vigência, responde como serviço inexistente.
// Se o slug for um alias antigo, retorna redirectTo com o slug canônico (data vazio).
func (r *CartaRepository) GetServiceDetail(ctx context.Context, slug string) (data json.RawMessage, redirectTo string, err error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return nil, "", fmt.Errorf("slug vazio")
	}
	var raw []byte
	err = r.db.QueryRow(ctx, getServiceDetailSQL, slug).Scan(&raw)
	if err == nil {
		return json.RawMessage(raw), "", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, "", err
	}

	canonical, aliasErr := r.ResolveSlugAlias(ctx, slug)
	if aliasErr != nil {
		return nil, "", aliasErr
	}
	if canonical != "" && canonical != slug {
		return nil, canonical, nil
	}
	return nil, "", nil
}
