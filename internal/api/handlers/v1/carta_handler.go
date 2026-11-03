package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

// cartaLocalStore cobre a navegação local da Carta.
type cartaLocalStore interface {
	ListThemes(ctx context.Context, page, perPage int, includeEmpty bool) ([]models.CartaTheme, models.CartaMeta, error)
	ListSubthemes(ctx context.Context, themeSlug string, page, perPage int) ([]models.CartaSubtheme, models.CartaMeta, error)
	ListServicesBySubtheme(ctx context.Context, subthemeSlug string, page, perPage int) ([]models.CartaServiceSummary, models.CartaMeta, error)
	GetServiceDetail(ctx context.Context, slug string) (data json.RawMessage, redirectTo string, err error)
	ListOrgUnits(ctx context.Context, page, perPage int) ([]models.CartaOrgUnit, models.CartaMeta, error)
	ListServicesBySiciCode(ctx context.Context, siciCode string, page, perPage int) ([]json.RawMessage, models.CartaMeta, error)
	ListAllServices(ctx context.Context, page, perPage int) ([]json.RawMessage, models.CartaMeta, error)
}

// CartaHandler expõe a hierarquia themes → subthemes → services a partir do banco local.
type CartaHandler struct {
	store cartaLocalStore
}

func NewCartaHandler(store *repository.CartaRepository) *CartaHandler {
	h := &CartaHandler{}
	if store != nil {
		h.store = store
	}
	return h
}

func newCartaHandler(store cartaLocalStore) *CartaHandler {
	return &CartaHandler{store: store}
}

// cartaThemesPagedJSON tipa GET /api/public/themes para o OpenAPI.
type cartaThemesPagedJSON struct {
	Meta models.CartaMeta    `json:"meta"`
	Data []models.CartaTheme `json:"data"`
}

// cartaSubthemesPagedJSON tipa GET /api/public/themes/{slug}/subthemes para o OpenAPI.
type cartaSubthemesPagedJSON struct {
	Meta models.CartaMeta       `json:"meta"`
	Data []models.CartaSubtheme `json:"data"`
}

// cartaServicesPagedJSON tipa GET /api/public/subthemes/{slug}/services para o OpenAPI.
type cartaServicesPagedJSON struct {
	Meta models.CartaMeta             `json:"meta"`
	Data []models.CartaServiceSummary `json:"data"`
}

// cartaDetailJSON tipa GET /api/public/services/{slug} para o OpenAPI (shape CloudHub).
type cartaDetailJSON struct {
	Data clients.CartaServiceDetail `json:"data"`
}

// cartaOrgUnitsPagedJSON tipa GET /api/public/org-units para o OpenAPI.
type cartaOrgUnitsPagedJSON struct {
	Meta models.CartaMeta      `json:"meta"`
	Data []models.CartaOrgUnit `json:"data"`
}

// cartaServiceDetailsPagedJSON tipa listagens paginadas de detalhe completo da Carta.
type cartaServiceDetailsPagedJSON struct {
	Meta models.CartaMeta             `json:"meta"`
	Data []clients.CartaServiceDetail `json:"data"`
}

// cartaServiceRedirectJSON é o corpo de 301 quando o slug é um alias antigo.
type cartaServiceRedirectJSON struct {
	Redirect string `json:"redirect" example:"iptu-2026"`
}

// ListThemes godoc
// @Summary      Lista temas da Carta de Serviços
// @Description  Lista temas ativos a partir da base local (sincronizada do CloudHub).
// @Tags         carta
// @Produce      json
// @Param        page           query  int   false  "Página (1-based, máx. 1000)" default(1) maximum(1000)
// @Param        per_page       query  int   false  "Itens por página (máx. 100)" default(100)
// @Param        include_empty  query  bool  false  "Inclui temas sem serviços publicados"
// @Success      200  {object}  cartaThemesPagedJSON
// @Failure      500  {object}  map[string]string
// @Failure      503  {object}  map[string]string
// @Router       /api/public/themes [get]
func (h *CartaHandler) ListThemes(c *gin.Context) {
	if h.unavailable(c) {
		return
	}
	page, perPage := parseCartaPage(c)
	includeEmpty := strings.EqualFold(c.Query("include_empty"), "true")

	data, meta, err := h.store.ListThemes(c.Request.Context(), page, perPage, includeEmpty)
	if err != nil {
		h.storeError(c, err)
		return
	}
	if data == nil {
		data = []models.CartaTheme{}
	}
	c.JSON(http.StatusOK, cartaThemesPagedJSON{Meta: meta, Data: data})
}

// ListSubthemes godoc
// @Summary      Lista subtemas de um tema
// @Description  Lista subtemas a partir da base local.
// @Tags         carta
// @Produce      json
// @Param        slug      path   string  true   "Slug do tema"
// @Param        page      query  int     false  "Página (1-based, máx. 1000)" default(1) maximum(1000)
// @Param        per_page  query  int     false  "Itens por página (máx. 100)" default(100)
// @Success      200  {object}  cartaSubthemesPagedJSON
// @Failure      400  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Failure      503  {object}  map[string]string
// @Router       /api/public/themes/{slug}/subthemes [get]
func (h *CartaHandler) ListSubthemes(c *gin.Context) {
	if h.unavailable(c) {
		return
	}
	slug := strings.TrimSpace(c.Param("slug"))
	if slug == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slug do tema obrigatório"})
		return
	}
	page, perPage := parseCartaPage(c)

	data, meta, err := h.store.ListSubthemes(c.Request.Context(), slug, page, perPage)
	if err != nil {
		h.storeError(c, err)
		return
	}
	if data == nil {
		data = []models.CartaSubtheme{}
	}
	c.JSON(http.StatusOK, cartaSubthemesPagedJSON{Meta: meta, Data: data})
}

// ListServicesBySubtheme godoc
// @Summary      Lista serviços de um subtema
// @Description  Lista serviços ativos do catálogo local filtrados por subtema. O resumo inclui responsibleOrgUnit, responsibleOrgUnitShortName e responsibleOrgUnitSiciCode (este último é sempre presente no JSON e null quando o órgão não tem SICI).
// @Tags         carta
// @Produce      json
// @Param        slug      path   string  true   "Slug do subtema"
// @Param        page      query  int     false  "Página (1-based, máx. 1000)" default(1) maximum(1000)
// @Param        per_page  query  int     false  "Itens por página (máx. 100)" default(100)
// @Success      200  {object}  cartaServicesPagedJSON
// @Failure      400  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Failure      503  {object}  map[string]string
// @Router       /api/public/subthemes/{slug}/services [get]
func (h *CartaHandler) ListServicesBySubtheme(c *gin.Context) {
	if h.unavailable(c) {
		return
	}
	slug := strings.TrimSpace(c.Param("slug"))
	if slug == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slug do subtema obrigatório"})
		return
	}
	page, perPage := parseCartaPage(c)

	data, meta, err := h.store.ListServicesBySubtheme(c.Request.Context(), slug, page, perPage)
	if err != nil {
		h.storeError(c, err)
		return
	}
	if data == nil {
		data = []models.CartaServiceSummary{}
	}
	c.JSON(http.StatusOK, cartaServicesPagedJSON{Meta: meta, Data: data})
}

// ListOrgUnits godoc
// @Summary      Lista órgãos da Carta de Serviços
// @Description  Lista órgãos/secretarias com ao menos 1 serviço publicado e código SICI preenchido.
// @Tags         carta
// @Produce      json
// @Param        page      query  int  false  "Página (1-based, máx. 1000)" default(1) maximum(1000)
// @Param        per_page  query  int  false  "Itens por página (máx. 100)" default(100)
// @Success      200  {object}  cartaOrgUnitsPagedJSON
// @Failure      500  {object}  map[string]string
// @Failure      503  {object}  map[string]string
// @Router       /api/public/org-units [get]
func (h *CartaHandler) ListOrgUnits(c *gin.Context) {
	if h.unavailable(c) {
		return
	}
	page, perPage := parseCartaPage(c)

	data, meta, err := h.store.ListOrgUnits(c.Request.Context(), page, perPage)
	if err != nil {
		h.storeError(c, err)
		return
	}
	if data == nil {
		data = []models.CartaOrgUnit{}
	}
	c.JSON(http.StatusOK, cartaOrgUnitsPagedJSON{Meta: meta, Data: data})
}

// ListServicesByOrgUnit godoc
// @Summary      Lista serviços de um órgão da Carta
// @Description  Retorna o detalhe completo dos serviços publicados filtrados por responsibleOrgUnitSiciCode (igualdade de texto).
// @Tags         carta
// @Produce      json
// @Param        siciCode  path   string  true   "Código SICI do órgão"
// @Param        page      query  int     false  "Página (1-based, máx. 1000)" default(1) maximum(1000)
// @Param        per_page  query  int     false  "Itens por página (máx. 100)" default(100)
// @Success      200  {object}  cartaServiceDetailsPagedJSON
// @Failure      400  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Failure      503  {object}  map[string]string
// @Router       /api/public/org-units/{siciCode}/services [get]
func (h *CartaHandler) ListServicesByOrgUnit(c *gin.Context) {
	if h.unavailable(c) {
		return
	}
	siciCode := strings.TrimSpace(c.Param("siciCode"))
	if siciCode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "código SICI obrigatório"})
		return
	}
	page, perPage := parseCartaPage(c)

	raws, meta, err := h.store.ListServicesBySiciCode(c.Request.Context(), siciCode, page, perPage)
	if err != nil {
		h.storeError(c, err)
		return
	}
	data, ok := unmarshalCartaServiceDetails(c, raws)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, cartaServiceDetailsPagedJSON{Meta: meta, Data: data})
}

// ListAllServices godoc
// @Summary      Lista todos os serviços da Carta
// @Description  Retorna o detalhe completo de todos os serviços publicados, ordenados por sici_code e title. Serviços sem código SICI entram nesta lista.
// @Tags         carta
// @Produce      json
// @Param        page      query  int  false  "Página (1-based, máx. 1000)" default(1) maximum(1000)
// @Param        per_page  query  int  false  "Itens por página (máx. 100)" default(100)
// @Success      200  {object}  cartaServiceDetailsPagedJSON
// @Failure      500  {object}  map[string]string
// @Failure      503  {object}  map[string]string
// @Router       /api/public/services [get]
func (h *CartaHandler) ListAllServices(c *gin.Context) {
	if h.unavailable(c) {
		return
	}
	page, perPage := parseCartaPage(c)

	raws, meta, err := h.store.ListAllServices(c.Request.Context(), page, perPage)
	if err != nil {
		h.storeError(c, err)
		return
	}
	data, ok := unmarshalCartaServiceDetails(c, raws)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, cartaServiceDetailsPagedJSON{Meta: meta, Data: data})
}

// GetService godoc
// @Summary      Detalhe de um serviço da Carta
// @Description  Retorna o payload completo do serviço publicado a partir de catalog_items.source_data. Slugs antigos respondem 301 com {"redirect":"<slug-canonico>"} (sem header Location).
// @Tags         carta
// @Produce      json
// @Param        slug  path  string  true  "Slug do serviço"
// @Success      200  {object}  cartaDetailJSON
// @Success      301  {object}  cartaServiceRedirectJSON
// @Failure      400  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Failure      503  {object}  map[string]string
// @Router       /api/public/services/{slug} [get]
func (h *CartaHandler) GetService(c *gin.Context) {
	if h.unavailable(c) {
		return
	}
	slug := strings.TrimSpace(c.Param("slug"))
	if slug == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slug do serviço obrigatório"})
		return
	}

	raw, redirectTo, err := h.store.GetServiceDetail(c.Request.Context(), slug)
	if err != nil {
		h.storeError(c, err)
		return
	}
	if redirectTo != "" {
		// 301 com corpo JSON; sem Location para o client não consumir o redirect HTTP.
		c.JSON(http.StatusMovedPermanently, cartaServiceRedirectJSON{Redirect: redirectTo})
		return
	}
	if len(raw) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "serviço não encontrado"})
		return
	}
	var detail clients.CartaServiceDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		log.Error().Err(err).Str("slug", slug).Msg("carta: source_data inválido")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao consultar carta de serviços"})
		return
	}
	c.JSON(http.StatusOK, cartaDetailJSON{Data: detail})
}

func (h *CartaHandler) unavailable(c *gin.Context) bool {
	if h == nil || h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "carta de serviços não disponível"})
		return true
	}
	return false
}

func (h *CartaHandler) storeError(c *gin.Context, err error) {
	log.Error().Err(err).Str("path", c.FullPath()).Msg("carta: falha na consulta local")
	c.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao consultar carta de serviços"})
}

func unmarshalCartaServiceDetails(c *gin.Context, raws []json.RawMessage) ([]clients.CartaServiceDetail, bool) {
	data := make([]clients.CartaServiceDetail, 0, len(raws))
	for _, raw := range raws {
		var detail clients.CartaServiceDetail
		if err := json.Unmarshal(raw, &detail); err != nil {
			log.Error().Err(err).Str("path", c.FullPath()).Msg("carta: source_data inválido")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao consultar carta de serviços"})
			return nil, false
		}
		data = append(data, detail)
	}
	return data, true
}

func parseCartaPage(c *gin.Context) (page, perPage int) {
	page = 1
	perPage = 100
	if v := c.Query("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}
	if v := c.Query("per_page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			perPage = n
		}
	}
	return repository.ClampCartaPage(page, perPage)
}
