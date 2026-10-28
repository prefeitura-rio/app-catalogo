package v1

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/observability"
	"github.com/prefeitura-rio/app-catalogo/internal/services"
)

// recommender é o que o handler usa de services.RecommendationService.
type recommender interface {
	Recommend(ctx context.Context, profile *models.CitizenProfile, req *models.RecommendationRequest) (*models.RecommendationResponse, error)
	RecommendAnonymous(ctx context.Context, req *models.RecommendationRequest) (*models.RecommendationResponse, error)
}

// citizenProfileProvider é o que o handler usa de services.CitizenProfileService.
type citizenProfileProvider interface {
	GetOrSync(ctx context.Context, cpf string) (*models.CitizenProfile, error)
}

type RecommendationHandler struct {
	recomSvc   recommender
	citizenSvc citizenProfileProvider
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

	started := time.Now()
	// personalized só vira true quando há perfil e a recomendação personalizada roda.
	personalized := false
	defer func() { observeRecommendation(started, personalized) }()

	req := h.parseRequest(c)

	profileCtx, cancelProfile := profileLookupContext(c.Request.Context())
	profile, err := h.citizenSvc.GetOrSync(profileCtx, cpf)
	cancelProfile()
	if err != nil || profile == nil {
		// Sem perfil (cidadão sem cadastro, RMI fora do ar ou com o circuit
		// breaker aberto): retornar recomendação genérica, nunca 500.
		resp, err := h.recomSvc.RecommendAnonymous(c.Request.Context(), req)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "falha nas recomendações"})
			return
		}
		c.JSON(http.StatusOK, resp)
		return
	}

	personalized = true
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
	started := time.Now()
	defer func() { observeRecommendation(started, false) }()

	req := h.parseRequest(c)
	req.ClusterHint = c.Query("cluster_hint")

	resp, err := h.recomSvc.RecommendAnonymous(c.Request.Context(), req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "falha nas recomendações"})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// observeRecommendation registra a duração da requisição de recomendação,
// inclusive quando ela falha.
func observeRecommendation(started time.Time, personalized bool) {
	observability.RecommendationsDuration.
		WithLabelValues(strconv.FormatBool(personalized)).
		Observe(time.Since(started).Seconds())
}

// profileLookupContext limita a busca do perfil (que pode depender do RMI) à
// metade do prazo restante da requisição, para sobrar tempo de servir a
// recomendação genérica quando o RMI está lento.
func profileLookupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Until(deadline)/2)
}

func (h *RecommendationHandler) parseRequest(c *gin.Context) *models.RecommendationRequest {
	req := &models.RecommendationRequest{
		Context: models.RecommendationContext(c.DefaultQuery("context", string(models.ContextHomepage))),
	}

	// Tipos
	for _, raw := range c.QueryArray("types") {
		for _, t := range strings.Split(raw, ",") {
			if t = strings.TrimSpace(t); t != "" {
				req.Types = append(req.Types, models.ItemType(t))
			}
		}
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
