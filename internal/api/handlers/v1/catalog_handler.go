package v1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

const (
	defaultPublicServicePage    = 1
	defaultPublicServicePerPage = 20
	maximumPublicServicePerPage = 100
	maximumPublicCategoryRunes  = 500
)

type publicServiceRepository interface {
	GetPublicServiceBySlug(context.Context, string) (*repository.PublicServiceResolution, error)
	ListPublicServiceCategories(context.Context) (*repository.PublicServiceCategorySnapshot, error)
	ListPublicServiceSubcategories(context.Context, string) (*repository.PublicServiceSubcategorySnapshot, error)
	ListPublicServices(context.Context, string, string, int, int) (*repository.PublicServiceListSnapshot, error)
}

type CatalogHandler struct {
	repository publicServiceRepository
}

func NewCatalogHandler(catalogRepository publicServiceRepository) *CatalogHandler {
	return &CatalogHandler{repository: catalogRepository}
}

// GetPublicServiceBySlug godoc
// @Summary      Public service detail by canonical or historical slug
// @Description  Returns an allowlisted detail for an active service. Historical slugs redirect to the canonical path.
// @Tags         catálogo
// @Produce      json
// @Param        slug  path  string  true  "Canonical or historical service slug"
// @Success      200  {object}  models.PublicServiceDetail
// @Success      308
// @Failure      400  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/public/services/{slug} [get]
func (handler *CatalogHandler) GetPublicServiceBySlug(ginContext *gin.Context) {
	requestID := ginContext.GetString("request_id")
	requestedSlug := strings.TrimSpace(ginContext.Param("slug"))
	if !models.ValidPublicServiceSlug(requestedSlug) {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": "slug inválido", "log_id": requestID})
		return
	}

	resolution, repositoryError := handler.repository.GetPublicServiceBySlug(ginContext.Request.Context(), requestedSlug)
	if repositoryError != nil {
		if errors.Is(repositoryError, pgx.ErrNoRows) {
			ginContext.JSON(http.StatusNotFound, gin.H{"error": "serviço não encontrado", "log_id": requestID})
			return
		}
		log.Error().Err(repositoryError).Str("request_id", requestID).Str("service_slug", requestedSlug).Msg("public service detail: database failure")
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao buscar serviço", "log_id": requestID})
		return
	}
	if resolution.CanonicalSlug != requestedSlug {
		ginContext.Header("Location", "/api/public/services/"+url.PathEscape(resolution.CanonicalSlug))
		ginContext.Status(http.StatusPermanentRedirect)
		ginContext.Writer.WriteHeaderNow()
		return
	}
	serviceDetail, detailError := models.NewPublicServiceDetail(resolution.Item)
	if detailError != nil {
		log.Error().Err(detailError).Str("request_id", requestID).Str("service_slug", requestedSlug).Msg("public service detail: invalid catalog projection")
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao projetar serviço", "log_id": requestID})
		return
	}
	ginContext.JSON(http.StatusOK, serviceDetail)
}

// ListPublicServiceCategories godoc
// @Summary      Public service categories
// @Description  Lists non-empty categories from active, currently eligible services.
// @Tags         catálogo
// @Produce      json
// @Success      200  {object}  models.PublicServiceCategoryResponse
// @Failure      500  {object}  map[string]string
// @Router       /api/public/service-categories [get]
func (handler *CatalogHandler) ListPublicServiceCategories(ginContext *gin.Context) {
	requestID := ginContext.GetString("request_id")
	categorySnapshot, repositoryError := handler.repository.ListPublicServiceCategories(ginContext.Request.Context())
	if repositoryError != nil {
		log.Error().Err(repositoryError).Str("request_id", requestID).Msg("public service categories: database failure")
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao buscar categorias", "log_id": requestID})
		return
	}
	ginContext.JSON(http.StatusOK, models.PublicServiceCategoryResponse{
		CatalogRevision: categorySnapshot.CatalogRevision,
		Categories:      categorySnapshot.Categories,
	})
}

// ListPublicServiceSubcategories godoc
// @Summary      Public service subcategories
// @Description  Lists non-empty subcategories for one exact public category name.
// @Tags         catálogo
// @Produce      json
// @Param        category  path  string  true  "Exact category name"
// @Success      200  {object}  models.PublicServiceSubcategoryResponse
// @Failure      400  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/public/service-categories/{category}/subcategories [get]
func (handler *CatalogHandler) ListPublicServiceSubcategories(ginContext *gin.Context) {
	requestID := ginContext.GetString("request_id")
	category := strings.TrimSpace(ginContext.Param("category"))
	if category == "" || len([]rune(category)) > maximumPublicCategoryRunes {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": "categoria inválida", "log_id": requestID})
		return
	}
	subcategorySnapshot, repositoryError := handler.repository.ListPublicServiceSubcategories(ginContext.Request.Context(), category)
	if repositoryError != nil {
		log.Error().Err(repositoryError).Str("request_id", requestID).Str("service_category", category).Msg("public service subcategories: database failure")
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao buscar subcategorias", "log_id": requestID})
		return
	}
	ginContext.JSON(http.StatusOK, models.PublicServiceSubcategoryResponse{
		CatalogRevision: subcategorySnapshot.CatalogRevision,
		Category:        category,
		Subcategories:   subcategorySnapshot.Subcategories,
	})
}

// ListPublicServices godoc
// @Summary      Public services browse
// @Description  Lists active, currently eligible services, optionally filtered by exact category and subcategory names.
// @Tags         catálogo
// @Produce      json
// @Param        category     query  string  false  "Exact category name"
// @Param        subcategory  query  string  false  "Exact subcategory name"
// @Param        page         query  int     false  "Page" minimum(1) default(1)
// @Param        per_page     query  int     false  "Items per page" minimum(1) maximum(100) default(20)
// @Success      200  {object}  models.PublicServiceListResponse
// @Failure      400  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/public/services [get]
func (handler *CatalogHandler) ListPublicServices(ginContext *gin.Context) {
	requestID := ginContext.GetString("request_id")
	category := strings.TrimSpace(ginContext.Query("category"))
	subcategory := strings.TrimSpace(ginContext.Query("subcategory"))
	if len([]rune(category)) > maximumPublicCategoryRunes || len([]rune(subcategory)) > maximumPublicCategoryRunes {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": "filtro de categoria inválido", "log_id": requestID})
		return
	}
	page, pageError := boundedPositiveQueryInteger(ginContext, "page", defaultPublicServicePage, 0)
	perPage, perPageError := boundedPositiveQueryInteger(ginContext, "per_page", defaultPublicServicePerPage, maximumPublicServicePerPage)
	if pageError != nil || perPageError != nil {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": "paginação inválida", "log_id": requestID})
		return
	}

	serviceSnapshot, repositoryError := handler.repository.ListPublicServices(
		ginContext.Request.Context(), category, subcategory, page, perPage,
	)
	if repositoryError != nil {
		log.Error().Err(repositoryError).Str("request_id", requestID).Msg("public service list: database failure")
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao buscar serviços", "log_id": requestID})
		return
	}
	services := make([]models.PublicServiceSummary, 0, len(serviceSnapshot.Items))
	for _, catalogItem := range serviceSnapshot.Items {
		serviceSummary, summaryError := models.NewPublicServiceSummary(catalogItem)
		if summaryError != nil {
			log.Error().Err(summaryError).Str("request_id", requestID).Str("catalog_item_id", catalogItem.ID.String()).Msg("public service list: invalid catalog projection")
			ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao projetar serviços", "log_id": requestID})
			return
		}
		services = append(services, *serviceSummary)
	}
	ginContext.JSON(http.StatusOK, models.PublicServiceListResponse{
		CatalogRevision: serviceSnapshot.CatalogRevision,
		Items:           services,
		Page:            page,
		PerPage:         perPage,
		Total:           serviceSnapshot.Total,
	})
}

func boundedPositiveQueryInteger(
	ginContext *gin.Context,
	parameterName string,
	defaultValue int,
	maximumValue int,
) (int, error) {
	rawValue := ginContext.Query(parameterName)
	if rawValue == "" {
		return defaultValue, nil
	}
	parsedValue, parseError := strconv.Atoi(rawValue)
	if parseError != nil || parsedValue < 1 || (maximumValue > 0 && parsedValue > maximumValue) {
		return 0, fmt.Errorf("%s is outside its supported range", parameterName)
	}
	return parsedValue, nil
}
