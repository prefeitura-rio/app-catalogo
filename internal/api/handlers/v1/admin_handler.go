package v1

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/prefeitura-rio/app-catalogo/internal/datasource"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

type AdminHandler struct {
	repo    *repository.CatalogItemRepository
	manager *datasource.Manager
}

func NewAdminHandler(repo *repository.CatalogItemRepository, manager *datasource.Manager) *AdminHandler {
	return &AdminHandler{repo: repo, manager: manager}
}

// SyncStatus godoc
// @Summary Status das últimas sincronizações por fonte
// @Tags admin
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/admin/sync/status [get]
func (h *AdminHandler) SyncStatus(c *gin.Context) {
	statuses, err := h.repo.GetLastSyncEvents(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao buscar status de sync"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"syncs": statuses})
}

// TriggerSync godoc
// @Summary Dispara sincronização manual de qualquer fonte registrada
// @Tags admin
// @Security BearerAuth
// @Param source query string false "Nome da fonte (ex: salesforce, app-go-api). Vazio = todas."
// @Produce json
// @Success 202 {object} map[string]string
// @Router /api/v1/admin/sync/trigger [post]
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
// @Summary Busca item do catálogo por ID
// @Tags catalog
// @Produce json
// @Param id path string true "UUID do item"
// @Success 200 {object} models.CatalogItem
// @Router /api/v1/catalog/{id} [get]
func (h *AdminHandler) GetCatalogItem(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID inválido"})
		return
	}

	item, err := h.repo.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "item não encontrado"})
		return
	}

	c.JSON(http.StatusOK, item)
}
