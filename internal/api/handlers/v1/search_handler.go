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

// Search godoc
// @Summary      Busca no catálogo
// @Description  Busca por serviços, cursos, vagas e oportunidades MEI. Suporta sintaxe websearch_to_tsquery: aspas para frase exata, - para exclusão, OR para alternativas.
// @Tags         busca
// @Produce      json
// @Param        q                  query  string    false  "Texto livre de busca"
// @Param        types              query  []string  false  "Tipos: service, course, job, mei_opportunity"  collectionFormat(multi)
// @Param        page               query  int       false  "Página (default: 1)"
// @Param        per_page           query  int       false  "Itens por página, máximo 100 (default: 10)"
// @Param        modalidade         query  string    false  "Modalidade: presencial, digital, hibrido"
// @Param        bairro             query  string    false  "Bairro do Rio de Janeiro"
// @Param        orgao              query  string    false  "Órgão ou secretaria responsável"
// @Param        gratuito           query  bool      false  "[course] Apenas cursos gratuitos"
// @Param        turno              query  string    false  "[course] Turno: matutino, vespertino, noturno"
// @Param        regime_contratacao query  string    false  "[job] Regime: CLT, PJ, temporario"
// @Param        modelo_trabalho    query  string    false  "[job] Modelo: presencial, remoto, hibrido"
// @Param        pcd                query  bool      false  "[job] Apenas vagas exclusivas para PCD"
// @Param        faixa_salarial     query  string    false  "[job] Faixa salarial: ate-2sm, 2-4sm, acima-4sm"
// @Param        canal_atendimento  query  string    false  "[service] Canal: presencial, digital, telefone"
// @Param        tema               query  string    false  "[service] Tema do serviço"
// @Param        segmento           query  string    false  "[mei_opportunity] Segmento do negócio"
// @Success      200                {object}  models.SearchResponse
// @Failure      500                {object}  map[string]string
// @Router       /api/v1/search [get]
// @Router       /api/public/search [get]
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
