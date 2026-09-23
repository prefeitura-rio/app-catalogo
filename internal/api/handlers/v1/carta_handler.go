package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

// cartaLocalStore cobre a navegação local da Carta.
type cartaLocalStore interface {
	ListThemes(ctx context.Context, page, perPage int, includeEmpty bool) ([]models.CartaTheme, models.CartaMeta, error)
	ListSubthemes(ctx context.Context, themeSlug string, page, perPage int) ([]models.CartaSubtheme, models.CartaMeta, error)
	ListServicesBySubtheme(ctx context.Context, subthemeSlug string, page, perPage int) ([]models.CartaServiceSummary, models.CartaMeta, error)
	GetServiceDetail(ctx context.Context, slug string) (json.RawMessage, error)
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

type cartaPagedJSON struct {
	Meta models.CartaMeta `json:"meta"`
	Data any              `json:"data"`
}

type cartaDetailJSON struct {
	Data json.RawMessage `json:"data" swaggertype:"object"`
}

// ListThemes godoc
// @Summary      Lista temas da Carta de Serviços
// @Description  Lista temas ativos a partir da base local (sincronizada do CloudHub).
// @Tags         carta
// @Produce      json
// @Param        page           query  int   false  "Página (1-based)" default(1)
// @Param        per_page       query  int   false  "Itens por página (máx. 100)" default(100)
// @Param        include_empty  query  bool  false  "Inclui temas sem serviços publicados"
// @Success      200  {object}  cartaPagedJSON
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
	c.JSON(http.StatusOK, cartaPagedJSON{Meta: meta, Data: data})
}

// ListSubthemes godoc
// @Summary      Lista subtemas de um tema
// @Description  Lista subtemas a partir da base local.
// @Tags         carta
// @Produce      json
// @Param        slug      path   string  true   "Slug do tema"
// @Param        page      query  int     false  "Página (1-based)" default(1)
// @Param        per_page  query  int     false  "Itens por página (máx. 100)" default(100)
// @Success      200  {object}  cartaPagedJSON
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
	c.JSON(http.StatusOK, cartaPagedJSON{Meta: meta, Data: data})
}

// ListServicesBySubtheme godoc
// @Summary      Lista serviços de um subtema
// @Description  Lista serviços ativos do catálogo local filtrados por subtema.
// @Tags         carta
// @Produce      json
// @Param        slug      path   string  true   "Slug do subtema"
// @Param        page      query  int     false  "Página (1-based)" default(1)
// @Param        per_page  query  int     false  "Itens por página (máx. 100)" default(100)
// @Success      200  {object}  cartaPagedJSON
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
	c.JSON(http.StatusOK, cartaPagedJSON{Meta: meta, Data: data})
}

// GetService godoc
// @Summary      Detalhe de um serviço da Carta
// @Description  Retorna o payload completo do serviço a partir de catalog_items.source_data.
// @Tags         carta
// @Produce      json
// @Param        slug  path  string  true  "Slug do serviço"
// @Success      200  {object}  cartaDetailJSON
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

	raw, err := h.store.GetServiceDetail(c.Request.Context(), slug)
	if err != nil {
		h.storeError(c, err)
		return
	}
	if len(raw) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "serviço não encontrado"})
		return
	}
	c.JSON(http.StatusOK, cartaDetailJSON{Data: raw})
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
	return page, perPage
}
