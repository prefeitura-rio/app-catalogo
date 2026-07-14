package v1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

type RecommendationHandler struct {
	recomSvc   RecommendationProvider
	citizenSvc CitizenProfileProvider
}

// RecommendationProvider exposes the recommendation operations used by HTTP handlers.
type RecommendationProvider interface {
	Recommend(context.Context, *models.CitizenProfile, *models.RecommendationRequest) (*models.RecommendationResponse, error)
	RecommendAnonymous(context.Context, *models.RecommendationRequest) (*models.RecommendationResponse, error)
}

// CitizenProfileProvider resolves the authenticated profile used for personalization.
type CitizenProfileProvider interface {
	GetOrSync(context.Context, string) (*models.CitizenProfile, error)
}

func NewRecommendationHandler(recomSvc RecommendationProvider, citizenSvc CitizenProfileProvider) *RecommendationHandler {
	return &RecommendationHandler{recomSvc: recomSvc, citizenSvc: citizenSvc}
}

// Authenticated godoc
// @Summary      Recomendações personalizadas
// @Description  Recomendações baseadas no perfil do cidadão autenticado (escolaridade, renda, localização, acessibilidade, faixa etária).
// @Tags         recomendações
// @Security     BearerAuth
// @Produce      json
// @Param        types    query  []string  false  "Tipos: service, course, job, mei_opportunity"  collectionFormat(multi)  Enums(service,course,job,mei_opportunity)
// @Param        limit    query  int       false  "Máximo de itens"  minimum(1)  maximum(50)  default(10)
// @Param        context  query  string    false  "Contexto da recomendação"  Enums(homepage,after_search,profile)
// @Success      200  {object}  models.RecommendationResponse
// @Failure      400  {object}  models.RecommendationErrorResponse
// @Failure      401  {object}  models.RecommendationErrorResponse
// @Failure      500  {object}  models.RecommendationErrorResponse
// @Router       /api/v1/recommendations [get]
func (h *RecommendationHandler) Authenticated(c *gin.Context) {
	cpf := middleware.GetUserCPF(c)
	if cpf == "" {
		writeRecommendationError(c, http.StatusUnauthorized, "autenticação necessária")
		return
	}

	req, requestError := h.parseRequest(c)
	if requestError != nil {
		rejectInvalidRecommendationRequest(c, requestError)
		return
	}

	profile, profileError := h.citizenSvc.GetOrSync(c.Request.Context(), cpf)
	if profileError != nil {
		log.Warn().
			Err(profileError).
			Str("request_id", c.GetString("request_id")).
			Msg("recommendation handler: citizen profile unavailable; using anonymous ranking")
	}
	if profileError != nil || profile == nil {
		// Sem perfil: retornar recomendação anônima
		resp, recommendationError := h.recomSvc.RecommendAnonymous(c.Request.Context(), req)
		if recommendationError != nil {
			logRecommendationFailure(c, recommendationError, "authenticated fallback")
			writeRecommendationError(c, http.StatusInternalServerError, "falha nas recomendações")
			return
		}
		c.JSON(http.StatusOK, resp)
		return
	}

	resp, recommendationError := h.recomSvc.Recommend(c.Request.Context(), profile, req)
	if recommendationError != nil {
		logRecommendationFailure(c, recommendationError, "authenticated")
		writeRecommendationError(c, http.StatusInternalServerError, "falha nas recomendações")
		return
	}

	c.JSON(http.StatusOK, resp)
}

// Anonymous godoc
// @Summary      Recomendações anônimas
// @Description  Recomendações sem autenticação, com scoring neutro.
// @Tags         recomendações
// @Produce      json
// @Param        types         query  []string  false  "Tipos: service, course, job, mei_opportunity"  collectionFormat(multi)  Enums(service,course,job,mei_opportunity)
// @Param        limit         query  int       false  "Máximo de itens"  minimum(1)  maximum(50)  default(10)
// @Param        context       query  string    false  "Contexto da recomendação"  Enums(homepage,after_search,profile)
// @Success      200  {object}  models.RecommendationResponse
// @Failure      400  {object}  models.RecommendationErrorResponse
// @Failure      500  {object}  models.RecommendationErrorResponse
// @Router       /api/public/recommendations [get]
func (h *RecommendationHandler) Anonymous(c *gin.Context) {
	req, requestError := h.parseRequest(c)
	if requestError != nil {
		rejectInvalidRecommendationRequest(c, requestError)
		return
	}
	resp, recommendationError := h.recomSvc.RecommendAnonymous(c.Request.Context(), req)
	if recommendationError != nil {
		logRecommendationFailure(c, recommendationError, "anonymous")
		writeRecommendationError(c, http.StatusInternalServerError, "falha nas recomendações")
		return
	}

	c.JSON(http.StatusOK, resp)
}

func (h *RecommendationHandler) parseRequest(c *gin.Context) (*models.RecommendationRequest, error) {
	req := &models.RecommendationRequest{
		Context: models.RecommendationContext(c.DefaultQuery("context", string(models.ContextHomepage))),
		Limit:   models.DefaultRecommendationLimit,
	}

	// Tipos
	for _, raw := range c.QueryArray("types") {
		for _, t := range strings.Split(raw, ",") {
			if t = strings.TrimSpace(t); t != "" {
				req.Types = append(req.Types, models.ItemType(t))
			}
		}
	}

	if encodedLimit, limitSupplied := c.GetQuery("limit"); limitSupplied {
		limit, parseError := strconv.Atoi(encodedLimit)
		if parseError != nil || limit < 1 || limit > models.MaximumRecommendationItems {
			return nil, fmt.Errorf("%w: %q", models.ErrInvalidRecommendationLimit, encodedLimit)
		}
		req.Limit = limit
	}

	if normalizeError := req.Normalize(); normalizeError != nil {
		return nil, normalizeError
	}
	return req, nil
}

func rejectInvalidRecommendationRequest(c *gin.Context, requestError error) {
	errorMessage := "parâmetros de recomendação inválidos"
	switch {
	case errors.Is(requestError, models.ErrInvalidRecommendationContext):
		errorMessage = "contexto de recomendação inválido"
	case errors.Is(requestError, models.ErrInvalidRecommendationItemType):
		errorMessage = "tipo de item de recomendação inválido"
	case errors.Is(requestError, models.ErrInvalidRecommendationLimit):
		errorMessage = "limite de recomendações inválido"
	}
	writeRecommendationError(c, http.StatusBadRequest, errorMessage)
}

func writeRecommendationError(c *gin.Context, statusCode int, errorMessage string) {
	c.JSON(statusCode, models.RecommendationErrorResponse{
		Error: errorMessage,
		LogID: c.GetString("request_id"),
	})
}

func logRecommendationFailure(c *gin.Context, recommendationError error, operation string) {
	log.Error().
		Err(recommendationError).
		Str("request_id", c.GetString("request_id")).
		Str("operation", operation).
		Msg("recommendation handler: recommendation failed")
}
