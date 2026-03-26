package v1

import (
	"net/http"

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
// @Summary Busca global no catálogo
// @Description Busca unificada por serviços, cursos, vagas e MEI.
// @Tags search
// @Accept json
// @Produce json
// @Param body body models.SearchRequest false "Parâmetros de busca"
// @Success 200 {object} models.SearchResponse
// @Router /api/v1/search [post]
// @Router /api/public/search [post]
func (h *SearchHandler) Search(c *gin.Context) {
	var req models.SearchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Body vazio é válido — retorna todos os itens
		req = models.SearchRequest{}
	}

	resp, err := h.searchSvc.Search(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "falha na busca"})
		return
	}

	c.JSON(http.StatusOK, resp)
}
