package v1

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/services"
)

type RecommendationHandler struct {
	recomSvc   *services.RecommendationService
	citizenSvc *services.CitizenProfileService
}

func NewRecommendationHandler(recomSvc *services.RecommendationService, citizenSvc *services.CitizenProfileService) *RecommendationHandler {
	return &RecommendationHandler{recomSvc: recomSvc, citizenSvc: citizenSvc}
}

// Authenticated godoc
// @Summary      Recomendações personalizadas
// @Description  Recomendações baseadas no perfil do cidadão autenticado (escolaridade, renda, localização, acessibilidade, faixa etária).
// @Tags         recomendações
// @Security     BearerAuth
// @Produce      json
// @Param        types    query  []string  false  "Tipos: service, course, job, mei_opportunity"  collectionFormat(multi)
// @Param        limit    query  int       false  "Máximo de itens, max 50 (default: 10)"
// @Param        context  query  string    false  "Contexto: homepage, after_search, profile"
// @Success      200  {object}  models.RecommendationResponse
// @Failure      401  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/recommendations [get]
func (h *RecommendationHandler) Authenticated(c *gin.Context) {
	cpf := middleware.GetUserCPF(c)
	if cpf == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "autenticação necessária"})
		return
	}

	req := h.parseRequest(c)

	profile, err := h.citizenSvc.GetOrSync(c.Request.Context(), cpf)
	if err != nil || profile == nil {
		// Sem perfil: retornar recomendação anônima
		resp, err := h.recomSvc.RecommendAnonymous(c.Request.Context(), req)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "falha nas recomendações"})
			return
		}
		c.JSON(http.StatusOK, resp)
		return
	}

	resp, err := h.recomSvc.Recommend(c.Request.Context(), profile, req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "falha nas recomendações"})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// Anonymous godoc
// @Summary      Recomendações anônimas
// @Description  Recomendações sem autenticação, com scoring neutro. cluster_hint aceita bairro ou faixa etária para leve personalização.
// @Tags         recomendações
// @Produce      json
// @Param        cluster_hint  query  string    false  "Bairro ou faixa etária (ex: Tijuca, 25-34)"
// @Param        types         query  []string  false  "Tipos: service, course, job, mei_opportunity"  collectionFormat(multi)
// @Param        limit         query  int       false  "Máximo de itens, max 50 (default: 10)"
// @Param        context       query  string    false  "Contexto: homepage, after_search, profile"
// @Success      200  {object}  models.RecommendationResponse
// @Failure      500  {object}  map[string]string
// @Router       /api/public/recommendations [get]
func (h *RecommendationHandler) Anonymous(c *gin.Context) {
	req := h.parseRequest(c)
	req.ClusterHint = c.Query("cluster_hint")

	resp, err := h.recomSvc.RecommendAnonymous(c.Request.Context(), req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "falha nas recomendações"})
		return
	}

	c.JSON(http.StatusOK, resp)
}

func (h *RecommendationHandler) parseRequest(c *gin.Context) *models.RecommendationRequest {
	req := &models.RecommendationRequest{
		Context: models.RecommendationContext(c.DefaultQuery("context", string(models.ContextHomepage))),
	}

	// Tipos
	for _, t := range c.QueryArray("types") {
		req.Types = append(req.Types, models.ItemType(t))
	}

	// Limit
	if limitStr := c.Query("limit"); limitStr != "" {
		var limit int
		if _, err := fmt.Sscanf(limitStr, "%d", &limit); err == nil {
			req.Limit = limit
		}
	}

	req.Normalize()
	return req
}
