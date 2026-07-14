package v1

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

const maximumSearchSummaryBodyBytes = 16 << 10

type searchSummaryProvider interface {
	Generate(context.Context, *models.SearchSummaryRequest) (*models.SearchSummaryResponse, error)
}

type SearchSummaryHandler struct {
	service searchSummaryProvider
}

func NewSearchSummaryHandler(service searchSummaryProvider) *SearchSummaryHandler {
	return &SearchSummaryHandler{service: service}
}

// Generate godoc
// @Summary      Grounded search-result summary
// @Description  Rehydrates candidates from one catalog revision and optionally generates a Gemini summary with allowlisted citations.
// @Tags         busca
// @Accept       json
// @Produce      json
// @Param        request  body  models.SearchSummaryRequest  true  "Search summary context"
// @Success      200  {object}  models.SearchSummaryResponse
// @Failure      400  {object}  map[string]string
// @Failure      409  {object}  map[string]string
// @Failure      413  {object}  map[string]string
// @Failure      415  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Failure      504  {object}  map[string]string
// @Router       /api/public/search-summary [post]
func (handler *SearchSummaryHandler) Generate(ginContext *gin.Context) {
	requestID := ginContext.GetString("request_id")
	ginContext.Header("Cache-Control", "private, no-store, max-age=0")
	var summaryRequest models.SearchSummaryRequest
	if decodeError := decodeStrictJSON(ginContext, maximumSearchSummaryBodyBytes, &summaryRequest); decodeError != nil {
		ginContext.JSON(strictJSONErrorStatus(decodeError), gin.H{"error": "contexto do resumo inválido", "log_id": requestID})
		return
	}
	if normalizationError := summaryRequest.Normalize(); normalizationError != nil {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": "contexto do resumo inválido", "log_id": requestID})
		return
	}
	summaryResponse, summaryError := handler.service.Generate(ginContext.Request.Context(), &summaryRequest)
	if summaryError != nil {
		switch {
		case errors.Is(summaryError, models.ErrCatalogRevisionMismatch):
			ginContext.JSON(http.StatusConflict, gin.H{"error": "o catálogo mudou; refaça a busca", "log_id": requestID})
		case errors.Is(summaryError, context.Canceled), errors.Is(summaryError, context.DeadlineExceeded):
			ginContext.JSON(http.StatusGatewayTimeout, gin.H{"error": "tempo limite do resumo excedido", "log_id": requestID})
		default:
			log.Error().Err(summaryError).Str("request_id", requestID).Msg("search summary failed")
			ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao gerar resumo", "log_id": requestID})
		}
		return
	}
	ginContext.JSON(http.StatusOK, summaryResponse)
}
