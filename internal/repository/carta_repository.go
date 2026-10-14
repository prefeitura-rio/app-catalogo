package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

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

func clampCartaPage(page, perPage int) (int, int) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 100
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

func cartaMeta(page, perPage, total int) models.CartaMeta {
	pages := 0
	if total > 0 {
		pages = int(math.Ceil(float64(total) / float64(perPage)))
	}
	return models.CartaMeta{Page: page, PerPage: perPage, Total: total, TotalPages: pages}
}

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

// SoftDeleteSubthemesNotIn marca subtemas fora da lista como deletados.
func (r *CartaRepository) SoftDeleteSubthemesNotIn(ctx context.Context, keep []string) (int64, error) {
	if len(keep) == 0 {
		tag, err := r.db.Exec(ctx, `
			UPDATE carta_subthemes SET deleted_at = NOW(), updated_at = NOW()
			WHERE deleted_at IS NULL`)
		return tag.RowsAffected(), err
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE carta_subthemes SET deleted_at = NOW(), updated_at = NOW()
		WHERE deleted_at IS NULL AND NOT (slug = ANY($1))`, keep)
	return tag.RowsAffected(), err
}

// RefreshHierarchyCounts recalcula contagens a partir dos serviços ativos locais.
func (r *CartaRepository) RefreshHierarchyCounts(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		UPDATE carta_subthemes s SET
			published_services = (
				SELECT COUNT(*)::INT FROM catalog_items ci
				WHERE ci.source = 'salesforce'
				  AND ci.deleted_at IS NULL
				  AND ci.status = 'active'
				  AND ci.subtheme_slug = s.slug
			),
			updated_at = NOW()
		WHERE s.deleted_at IS NULL`)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `
		UPDATE carta_themes t SET
			published_services = (
				SELECT COUNT(*)::INT FROM catalog_items ci
				WHERE ci.source = 'salesforce'
				  AND ci.deleted_at IS NULL
				  AND ci.status = 'active'
				  AND ci.theme_slug = t.slug
			),
			subthemes_count = (
				SELECT COUNT(*)::INT FROM carta_subthemes s
				WHERE s.theme_slug = t.slug AND s.deleted_at IS NULL
			),
			updated_at = NOW()
		WHERE t.deleted_at IS NULL`)
	return err
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

// ListServicesBySubtheme pagina serviços ativos de um subtema.
func (r *CartaRepository) ListServicesBySubtheme(ctx context.Context, subthemeSlug string, page, perPage int) ([]models.CartaServiceSummary, models.CartaMeta, error) {
	subthemeSlug = strings.TrimSpace(subthemeSlug)
	if subthemeSlug == "" {
		return nil, models.CartaMeta{}, fmt.Errorf("subtheme slug vazio")
	}
	page, perPage = clampCartaPage(page, perPage)

	var total int
	if err := r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM catalog_items
		WHERE source = 'salesforce'
		  AND subtheme_slug = $1
		  AND deleted_at IS NULL
		  AND status = 'active'`, subthemeSlug).Scan(&total); err != nil {
		return nil, models.CartaMeta{}, err
	}

	offset := (page - 1) * perPage
	rows, err := r.db.Query(ctx, `
		SELECT external_id, title, COALESCE(short_desc, ''), COALESCE(organization, ''),
		       status, COALESCE(theme_slug, ''), COALESCE(subtheme_slug, ''),
		       source_data
		FROM catalog_items
		WHERE source = 'salesforce'
		  AND subtheme_slug = $1
		  AND deleted_at IS NULL
		  AND status = 'active'
		ORDER BY title ASC
		LIMIT $2 OFFSET $3`, subthemeSlug, perPage, offset)
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

// GetServiceDetail retorna o source_data do serviço publicado pelo slug (external_id).
// Se o slug for um alias antigo, retorna redirectTo com o slug canônico (data vazio).
func (r *CartaRepository) GetServiceDetail(ctx context.Context, slug string) (data json.RawMessage, redirectTo string, err error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return nil, "", fmt.Errorf("slug vazio")
	}
	var raw []byte
	err = r.db.QueryRow(ctx, `
		SELECT source_data
		FROM catalog_items
		WHERE source = 'salesforce'
		  AND external_id = $1
		  AND deleted_at IS NULL
		  AND status = 'active'
		LIMIT 1`, slug).Scan(&raw)
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
