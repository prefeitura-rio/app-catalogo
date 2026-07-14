package v1

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/datasource"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

// swag import para que models.SyncStatus e models.CatalogItem sejam reconhecidos
var _ = models.SyncStatus{}

type AdminHandler struct {
	repo    *repository.CatalogItemRepository
	manager *datasource.Manager
}

func NewAdminHandler(repo *repository.CatalogItemRepository, manager *datasource.Manager) *AdminHandler {
	return &AdminHandler{repo: repo, manager: manager}
}

// SyncStatus godoc
// @Summary      Status das sincronizações
// @Description  Retorna o último evento de sincronização por fonte de dados. Requer role admin ou go:admin.
// @Tags         admin
// @Security     BearerAuth
// @Produce      json
// @Success      200  {object}  models.SyncStatusResponse
// @Failure      401  {object}  map[string]string
// @Failure      403  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/admin/sync/status [get]
func (h *AdminHandler) SyncStatus(c *gin.Context) {
	statuses, err := h.repo.GetLastSyncEvents(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao buscar status de sync"})
		return
	}
	c.JSON(http.StatusOK, models.NewSyncStatusResponse(statuses))
}

// TriggerSync godoc
// @Summary      Dispara sincronização manual
// @Description  Dispara sync ad-hoc em background. Retorna imediatamente. Requer role admin ou go:admin.
// @Tags         admin
// @Security     BearerAuth
// @Param        source  query  string  false  "Fonte: salesforce, app-go-api, typesense. Vazio = todas."
// @Produce      json
// @Success      202  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      403  {object}  map[string]string
// @Router       /api/v1/admin/sync/trigger [post]
func (h *AdminHandler) TriggerSync(c *gin.Context) {
	source := c.Query("source")

	go func() {
		ctx := context.Background()
		if source == "" {
			h.manager.TriggerAll(ctx)
		} else {
			if !h.manager.TriggerSync(ctx, source) {
				// fonte não encontrada — sem efeito, log já é feito no manager
			}
		}
	}()

	c.JSON(http.StatusAccepted, gin.H{"status": "triggered", "source": source})
}

// GetCatalogItem godoc
// @Summary      Detalhe de item do catálogo
// @Description  Retorna todos os campos de um item incluindo source_data original.
// @Tags         catálogo
// @Security     BearerAuth
// @Produce      json
// @Param        id  path  string  true  "UUID v4 do item"
// @Success      200  {object}  models.CatalogItem
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Router       /api/v1/catalog/{id} [get]
func (h *AdminHandler) GetCatalogItem(c *gin.Context) {
	requestID := c.GetString("request_id")
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido", "log_id": requestID})
		return
	}

	item, err := h.repo.GetByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "item não encontrado", "log_id": requestID})
			return
		}
		log.Error().Err(err).Str("request_id", requestID).Str("catalog_item_id", id.String()).Msg("catalog detail: database failure")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao buscar item", "log_id": requestID})
		return
	}

	c.JSON(http.StatusOK, item)
}

// GetPublicCatalogItem godoc
// @Summary      Detalhe público de item elegível do catálogo
// @Description  Retorna somente campos públicos de um item ativo e vigente.
// @Tags         catálogo
// @Produce      json
// @Param        id  path  string  true  "UUID v4 do item"
// @Success      200  {object}  models.PublicCatalogItem
// @Failure      400  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/public/catalog/{id} [get]
func (h *AdminHandler) GetPublicCatalogItem(c *gin.Context) {
	requestID := c.GetString("request_id")
	id, parseError := uuid.Parse(c.Param("id"))
	if parseError != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido", "log_id": requestID})
		return
	}

	item, repositoryError := h.repo.GetPublicByID(c.Request.Context(), id)
	if repositoryError != nil {
		if errors.Is(repositoryError, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "item não encontrado", "log_id": requestID})
			return
		}
		log.Error().Err(repositoryError).Str("request_id", requestID).Str("catalog_item_id", id.String()).Msg("public catalog detail: database failure")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao buscar item", "log_id": requestID})
		return
	}

	c.JSON(http.StatusOK, models.NewPublicCatalogItem(item))
}
