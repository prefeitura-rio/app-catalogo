package v1

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/services"
)

type SearchHandler struct {
	searchSvc  *services.SearchService
	citizenSvc *services.CitizenProfileService
}

func NewSearchHandler(searchSvc *services.SearchService, citizenSvc *services.CitizenProfileService) *SearchHandler {
	return &SearchHandler{searchSvc: searchSvc, citizenSvc: citizenSvc}
}

func (h *SearchHandler) Search(c *gin.Context) {
	req := parseSearchQuery(c)

	resp, err := h.searchSvc.Search(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "falha na busca"})
		return
	}

	c.JSON(http.StatusOK, resp)
}

func parseSearchQuery(c *gin.Context) models.SearchRequest {
	req := models.SearchRequest{
		Q:       c.Query("q"),
		Page:    queryInt(c, "page", 1),
		PerPage: queryInt(c, "per_page", 10),
	}

	for _, t := range c.QueryArray("types") {
		req.Types = append(req.Types, models.ItemType(t))
	}

	req.Filters = models.SearchFilters{
		Modalidade:        c.Query("modalidade"),
		Bairro:            c.Query("bairro"),
		Orgao:             c.Query("orgao"),
		Gratuito:          queryBool(c, "gratuito"),
		Turno:             c.Query("turno"),
		RegimeContratacao: c.Query("regime_contratacao"),
		ModeloTrabalho:    c.Query("modelo_trabalho"),
		PCD:               queryBool(c, "pcd"),
		FaixaSalarial:     c.Query("faixa_salarial"),
		CanalAtendimento:  c.Query("canal_atendimento"),
		Tema:              c.Query("tema"),
		Segmento:          c.Query("segmento"),
	}

	return req
}

func queryInt(c *gin.Context, key string, def int) int {
	if v := c.Query(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// queryBool retorna nil quando o parâmetro está ausente (sem filtro),
// e um ponteiro para bool quando presente ("true" ou "false").
func queryBool(c *gin.Context, key string) *bool {
	v := c.Query(key)
	if v == "" {
		return nil
	}
	b := strings.ToLower(v) == "true"
	return &b
}
