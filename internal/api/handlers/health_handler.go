package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

type HealthHandler struct {
	db *pgxpool.Pool
}

func NewHealthHandler(db *pgxpool.Pool) *HealthHandler {
	return &HealthHandler{db: db}
}

// Health godoc
// @Summary      Liveness probe
// @Description  Sempre retorna 200. Usado pelo Kubernetes para liveness check.
// @Tags         infra
// @Produce      json
// @Success      200  {object}  map[string]string
// @Router       /health [get]
func (h *HealthHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Ready godoc
// @Summary      Readiness probe
// @Description  Verifica conectividade com o banco de dados.
// @Tags         infra
// @Produce      json
// @Success      200  {object}  map[string]string
// @Failure      503  {object}  map[string]string
// @Router       /ready [get]
func (h *HealthHandler) Ready(c *gin.Context) {
	if err := h.db.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "not ready",
			"error":  "banco de dados indisponível",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}
