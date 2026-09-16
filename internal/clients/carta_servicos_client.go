package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maximumCartaServicosResponseBytes int64 = 8 << 20 // 8 MiB
	cartaServicosDefaultPerPage             = 100
	cartaServicosMaxPerPage                 = 100
)

// CartaServicosClient consome a API MuleSoft CloudHub da Carta de Serviços.
type CartaServicosClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewCartaServicosClient(baseURL string) *CartaServicosClient {
	return &CartaServicosClient{
		baseURL:    normalizeCartaServicosBaseURL(baseURL),
		httpClient: noRedirectHTTPClient(30 * time.Second),
	}
}

func normalizeCartaServicosBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		trimmed := strings.TrimRight(raw, "/")
		if trimmed == "" {
			return ""
		}
		if trimmed == "/api" || strings.HasSuffix(trimmed, "/api") || strings.Contains(trimmed, "/api/") {
			return trimmed
		}
		return trimmed + "/api"
	}

	path := strings.TrimRight(u.Path, "/")
	switch {
	case path == "":
		u.Path = "/api"
	case path == "/api", strings.HasPrefix(path, "/api/"):
		u.Path = path
	default:
		u.Path = path + "/api"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// --- DTOs -------------------------------------------------------------------

type CartaMeta struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

type CartaTheme struct {
	Slug              string `json:"slug"`
	Name              string `json:"name"`
	SubthemesCount    int    `json:"subthemesCount"`
	PublishedServices int    `json:"publishedServices"`
}

type CartaSubtheme struct {
	Slug              string `json:"slug"`
	Name              string `json:"name"`
	PublishedServices int    `json:"publishedServices"`
}

// CartaServiceListItem é o item retornado por GET /subthemes/{slug}/services.
type CartaServiceListItem struct {
	Slug                         string   `json:"slug"`
	Name                         string   `json:"name"`
	ServiceCatalogID             string   `json:"serviceCatalogId"`
	ArticleID                    string   `json:"articleId"`
	ArticleVersion               int      `json:"articleVersion"`
	ArticleStatus                string   `json:"articleStatus"`
	CreatedDate                  string   `json:"createdDate"`
	LastModifiedDate             string   `json:"lastModifiedDate"`
	LastPublishedDate            string   `json:"lastPublishedDate"`
	ResponsibleOrgUnit           string   `json:"responsibleOrgUnit"`
	ResponsibleOrgUnitShortName  string   `json:"responsibleOrgUnitShortName"`
	Summary                      string   `json:"summary"`
	ServiceDeadline              *string  `json:"serviceDeadline"`
	ServiceDeadlineNotes         *string  `json:"serviceDeadlineNotes"`
	Cost                         *string  `json:"cost"`
	CostValue                    *float64 `json:"costValue"`
	CostNotes                    *string  `json:"costNotes"`
	IsFree                       bool     `json:"isFree"`
	FixarDestaque                bool     `json:"fixarDestaque"`
	AllowTicketSubmission        bool     `json:"allowTicketSubmission"`
	AllowsAnonymity              bool     `json:"allowsAnonymity"`
}

// EffectiveModifiedAt retorna o maior timestamp entre lastModified e lastPublished.
func (s CartaServiceListItem) EffectiveModifiedAt() time.Time {
	return maxTime(parseCartaTime(s.LastModifiedDate), parseCartaTime(s.LastPublishedDate))
}

type CartaServiceDetail struct {
	Slug                        string              `json:"slug"`
	Name                        string              `json:"name"`
	ServiceCatalogID            string              `json:"serviceCatalogId"`
	ArticleID                   string              `json:"articleId"`
	ArticleVersion              int                 `json:"articleVersion"`
	ArticleStatus               string              `json:"articleStatus"`
	CreatedDate                 string              `json:"createdDate"`
	LastModifiedDate            string              `json:"lastModifiedDate"`
	LastPublishedDate           string              `json:"lastPublishedDate"`
	SlugHistory                 []string            `json:"slugHistory"`
	ThemeSlug                   string              `json:"themeSlug"`
	ThemeName                   string              `json:"themeName"`
	SubthemeSlug                string              `json:"subthemeSlug"`
	SubthemeName                string              `json:"subthemeName"`
	ResponsibleOrgUnit          string              `json:"responsibleOrgUnit"`
	ResponsibleOrgUnitShortName string              `json:"responsibleOrgUnitShortName"`
	Flags                       CartaServiceFlags   `json:"flags"`
	Info                        CartaServiceInfo    `json:"info"`
	HowToRequest                CartaHowToRequest   `json:"howToRequest"`
	Channels                    []CartaChannel      `json:"channels"`
	Buttons                     []CartaButton       `json:"buttons"`
	Legislation                 []CartaLegislation  `json:"legislation"`
	ServicePoints               []json.RawMessage   `json:"servicePoints"`
}

type CartaServiceFlags struct {
	AllowsAnonymity       bool `json:"allowsAnonymity"`
	AllowTicketSubmission bool `json:"allowTicketSubmission"`
}

type CartaServiceInfo struct {
	Summary              string   `json:"summary"`
	FullDescription      string   `json:"fullDescription"`
	TargetAudience       []string `json:"targetAudience"`
	Cost                 *string  `json:"cost"`
	CostValue            *float64 `json:"costValue"`
	CostNotes            *string  `json:"costNotes"`
	IsFree               bool     `json:"isFree"`
	ServiceDeadline      *string  `json:"serviceDeadline"`
	ServiceDeadlineNotes *string  `json:"serviceDeadlineNotes"`
}

type CartaHowToRequest struct {
	Instructions  string   `json:"instructions"`
	ServiceResult string   `json:"serviceResult"`
	RequiredDocs  []string `json:"requiredDocs"`
	Exclusions    *string  `json:"exclusions"`
}

type CartaChannel struct {
	Order       int     `json:"order"`
	Type        string  `json:"type"`
	Endereco    string  `json:"endereco,omitempty"`
	Titulo      string  `json:"titulo,omitempty"`
	MapURL      string  `json:"mapUrl,omitempty"`
	Value       string  `json:"value,omitempty"`
	ChannelType string  `json:"channelType,omitempty"`
	WhatsappLink string `json:"whatsappLink,omitempty"`
	Enabled     bool    `json:"enabled"`
}

type CartaButton struct {
	Order       int    `json:"order"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Enabled     bool   `json:"enabled"`
}

type CartaLegislation struct {
	Order  int    `json:"order"`
	Titulo string `json:"titulo"`
}

// ServiceRedirectError indica que o slug solicitado foi movido (301).
type ServiceRedirectError struct {
	FromSlug string
	ToSlug   string
}

func (e *ServiceRedirectError) Error() string {
	return fmt.Sprintf("carta-servicos: slug %q redireciona para %q", e.FromSlug, e.ToSlug)
}

// --- Endpoints --------------------------------------------------------------

type cartaPagedResponse[T any] struct {
	Meta CartaMeta `json:"meta"`
	Data []T       `json:"data"`
}

type cartaServiceDetailResponse struct {
	Data CartaServiceDetail `json:"data"`
}

type cartaRedirectBody struct {
	Redirect string `json:"redirect"`
}

// ListThemes retorna uma página de temas.
func (c *CartaServicosClient) ListThemes(ctx context.Context, page, perPage int, includeEmpty bool) ([]CartaTheme, CartaMeta, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(clampPerPage(perPage)))
	if includeEmpty {
		q.Set("include_empty", "true")
	}
	var resp cartaPagedResponse[CartaTheme]
	if err := c.getJSON(ctx, "/themes", q, &resp); err != nil {
		return nil, CartaMeta{}, err
	}
	return resp.Data, resp.Meta, nil
}

// ListAllThemes pagina todos os temas.
func (c *CartaServicosClient) ListAllThemes(ctx context.Context, includeEmpty bool) ([]CartaTheme, error) {
	var all []CartaTheme
	page := 1
	for {
		items, meta, err := c.ListThemes(ctx, page, cartaServicosDefaultPerPage, includeEmpty)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if page >= meta.TotalPages || len(items) == 0 {
			break
		}
		page++
	}
	return all, nil
}

// ListSubthemes retorna uma página de subtemas de um tema.
func (c *CartaServicosClient) ListSubthemes(ctx context.Context, themeSlug string, page, perPage int) ([]CartaSubtheme, CartaMeta, error) {
	themeSlug = strings.TrimSpace(themeSlug)
	if themeSlug == "" {
		return nil, CartaMeta{}, fmt.Errorf("carta-servicos: theme slug vazio")
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(clampPerPage(perPage)))
	var resp cartaPagedResponse[CartaSubtheme]
	if err := c.getJSON(ctx, "/themes/"+url.PathEscape(themeSlug)+"/subthemes", q, &resp); err != nil {
		return nil, CartaMeta{}, err
	}
	return resp.Data, resp.Meta, nil
}

// ListAllSubthemes pagina todos os subtemas de um tema.
func (c *CartaServicosClient) ListAllSubthemes(ctx context.Context, themeSlug string) ([]CartaSubtheme, error) {
	var all []CartaSubtheme
	page := 1
	for {
		items, meta, err := c.ListSubthemes(ctx, themeSlug, page, cartaServicosDefaultPerPage)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if page >= meta.TotalPages || len(items) == 0 {
			break
		}
		page++
	}
	return all, nil
}

// ListServicesBySubtheme retorna uma página de serviços de um subtema.
func (c *CartaServicosClient) ListServicesBySubtheme(ctx context.Context, subthemeSlug string, page, perPage int) ([]CartaServiceListItem, CartaMeta, error) {
	subthemeSlug = strings.TrimSpace(subthemeSlug)
	if subthemeSlug == "" {
		return nil, CartaMeta{}, fmt.Errorf("carta-servicos: subtheme slug vazio")
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(clampPerPage(perPage)))
	var resp cartaPagedResponse[CartaServiceListItem]
	if err := c.getJSON(ctx, "/subthemes/"+url.PathEscape(subthemeSlug)+"/services", q, &resp); err != nil {
		return nil, CartaMeta{}, err
	}
	return resp.Data, resp.Meta, nil
}

// ListAllServicesBySubtheme pagina todos os serviços de um subtema.
func (c *CartaServicosClient) ListAllServicesBySubtheme(ctx context.Context, subthemeSlug string) ([]CartaServiceListItem, error) {
	var all []CartaServiceListItem
	page := 1
	for {
		items, meta, err := c.ListServicesBySubtheme(ctx, subthemeSlug, page, cartaServicosDefaultPerPage)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if page >= meta.TotalPages || len(items) == 0 {
			break
		}
		page++
	}
	return all, nil
}

// GetService retorna o detalhe de um serviço pelo slug.
// Em caso de 301, retorna *ServiceRedirectError com o slug de destino.
func (c *CartaServicosClient) GetService(ctx context.Context, slug string) (*CartaServiceDetail, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return nil, fmt.Errorf("carta-servicos: service slug vazio")
	}

	reqURL := c.baseURL + "/services/" + url.PathEscape(slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("carta-servicos: criar request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("carta-servicos: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readBoundedHTTPBody(resp.Body, maximumCartaServicosResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("carta-servicos: ler resposta: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var wrapper cartaServiceDetailResponse
		if err := json.Unmarshal(body, &wrapper); err != nil {
			return nil, fmt.Errorf("carta-servicos: decodificar detalhe: %w", err)
		}
		if wrapper.Data.Slug == "" {
			return nil, fmt.Errorf("carta-servicos: detalhe sem slug")
		}
		return &wrapper.Data, nil

	case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		toSlug := extractRedirectSlug(resp.Header.Get("Location"), body)
		if toSlug == "" {
			return nil, fmt.Errorf("carta-servicos: redirect sem slug de destino (status %d)", resp.StatusCode)
		}
		return nil, &ServiceRedirectError{FromSlug: slug, ToSlug: toSlug}

	case http.StatusNotFound:
		return nil, fmt.Errorf("carta-servicos: serviço %q não encontrado", slug)

	default:
		return nil, fmt.Errorf("carta-servicos: GET /services/%s status %d", slug, resp.StatusCode)
	}
}

// GetServiceCanonical resolve redirects e retorna o detalhe no slug canônico.
func (c *CartaServicosClient) GetServiceCanonical(ctx context.Context, slug string) (*CartaServiceDetail, string, error) {
	detail, err := c.GetService(ctx, slug)
	if err == nil {
		return detail, detail.Slug, nil
	}
	var redir *ServiceRedirectError
	if !asServiceRedirect(err, &redir) {
		return nil, "", err
	}
	detail, err = c.GetService(ctx, redir.ToSlug)
	if err != nil {
		return nil, "", err
	}
	return detail, detail.Slug, nil
}

func asServiceRedirect(err error, target **ServiceRedirectError) bool {
	if err == nil {
		return false
	}
	re, ok := err.(*ServiceRedirectError)
	if !ok {
		return false
	}
	*target = re
	return true
}

func extractRedirectSlug(location string, body []byte) string {
	var redirectBody cartaRedirectBody
	if len(body) > 0 {
		_ = json.Unmarshal(body, &redirectBody)
		if slug := strings.TrimSpace(redirectBody.Redirect); slug != "" {
			return strings.Trim(slug, "/")
		}
	}
	location = strings.TrimSpace(location)
	if location == "" {
		return ""
	}
	// Location pode ser path relativo: /api/services/novo-slug
	if u, err := url.Parse(location); err == nil {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 1 {
			return parts[len(parts)-1]
		}
	}
	return ""
}

func (c *CartaServicosClient) getJSON(ctx context.Context, path string, query url.Values, dest any) error {
	reqURL := c.baseURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("carta-servicos: criar request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("carta-servicos: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readBoundedHTTPBody(resp.Body, maximumCartaServicosResponseBytes)
	if err != nil {
		return fmt.Errorf("carta-servicos: ler resposta: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("carta-servicos: %s status %d", path, resp.StatusCode)
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return fmt.Errorf("carta-servicos: decodificar %s: %w", path, err)
	}
	return nil
}

func clampPerPage(perPage int) int {
	if perPage <= 0 {
		return cartaServicosDefaultPerPage
	}
	if perPage > cartaServicosMaxPerPage {
		return cartaServicosMaxPerPage
	}
	return perPage
}

func parseCartaTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
