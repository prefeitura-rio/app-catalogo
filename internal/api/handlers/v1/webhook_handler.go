package v1

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/services"
)

type WebhookHandler struct {
	sfSyncSvc     *services.SalesForceSyncService
	webhookSecret string
}

func NewWebhookHandler(sfSyncSvc *services.SalesForceSyncService, webhookSecret string) *WebhookHandler {
	return &WebhookHandler{sfSyncSvc: sfSyncSvc, webhookSecret: webhookSecret}
}

type sfWebhookPayload struct {
	Event struct {
		Type    string `json:"type"`
		Created string `json:"created"`
	} `json:"event"`
	SObject struct {
		ID   string `json:"Id"`
		Type string `json:"type"`
	} `json:"sobject"`
}

// SalesForce godoc
// @Summary Webhook do SalesForce (Change Data Capture)
// @Tags webhooks
// @Accept json
// @Produce json
// @Success 200 {object} map[string]string
// @Router /api/webhooks/salesforce [post]
func (h *WebhookHandler) SalesForce(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "falha ao ler body"})
		return
	}

	if h.webhookSecret != "" {
		sig := c.GetHeader("X-Salesforce-Signature")
		if !h.validateHMAC(body, sig) {
			log.Warn().Str("sig", sig).Msg("webhook: assinatura inválida")
			c.JSON(http.StatusUnauthorized, gin.H{"error": "assinatura inválida"})
			return
		}
	}

	var payload sfWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "payload inválido"})
		return
	}

	if payload.SObject.ID == "" {
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}

	go func() {
		ctx := c.Request.Context()
		if err := h.sfSyncSvc.SyncRecord(ctx, payload.SObject.ID); err != nil {
			log.Error().Err(err).Str("id", payload.SObject.ID).Msg("webhook: falha ao sincronizar registro")
		} else {
			log.Info().Str("id", payload.SObject.ID).Msg("webhook: registro sincronizado")
		}
	}()

	c.JSON(http.StatusOK, gin.H{"status": "queued"})
}

func (h *WebhookHandler) validateHMAC(body []byte, signature string) bool {
	mac := hmac.New(sha256.New, []byte(h.webhookSecret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
