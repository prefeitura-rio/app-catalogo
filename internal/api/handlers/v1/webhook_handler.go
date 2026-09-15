package v1

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/services"
)

const maximumSalesForceWebhookBodyBytes int64 = 64 << 10

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
// @Summary      Webhook SalesForce (Change Data Capture)
// @Description  Recebe notificações de criação/atualização da Carta de Serviços. Valida assinatura HMAC-SHA256 via header X-Salesforce-Signature.
// @Tags         webhooks
// @Accept       json
// @Produce      json
// @Param        X-Salesforce-Signature  header  string                 false  "HMAC-SHA256 do body em hex"
// @Param        payload                 body    sfWebhookPayload        true   "Payload do evento"
// @Success      200  {object}  map[string]string
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Router       /api/webhooks/salesforce [post]
func (h *WebhookHandler) SalesForce(c *gin.Context) {
	if h.webhookSecret == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "webhook não configurado"})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maximumSalesForceWebhookBodyBytes)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "payload excede o limite permitido"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "falha ao ler body"})
		return
	}

	sig := c.GetHeader("X-Salesforce-Signature")
	if !h.validateHMAC(body, sig) {
		log.Warn().Msg("webhook: assinatura inválida")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "assinatura inválida"})
		return
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

	externalID := payload.SObject.ID
	syncCtx := context.WithoutCancel(c.Request.Context())
	go func() {
		if err := h.sfSyncSvc.SyncRecord(syncCtx, externalID); err != nil {
			log.Error().Err(err).Str("id", externalID).Msg("webhook: falha ao sincronizar registro")
		} else {
			log.Info().Str("id", externalID).Msg("webhook: registro sincronizado")
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
