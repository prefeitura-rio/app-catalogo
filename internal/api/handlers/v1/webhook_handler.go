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
	"strings"

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
		Type    string `json:"type" example:"updated"`
		Created string `json:"created" example:"2026-04-08T10:00:00Z"`
	} `json:"event"`
	SObject struct {
		ID   string `json:"Id" example:"a001Q000003ABCDE"`
		Slug string `json:"Slug" example:"iptu-cobranca"`
		Type string `json:"type" example:"Servico__c"`
	} `json:"sobject"`
}

// SalesForce godoc
// @Summary      Webhook SalesForce (Carta de Serviços)
// @Description  Recebe notificações de criação/atualização. Valida HMAC-SHA256 via X-Salesforce-Signature. Usa sobject.Slug (ou Id como fallback) como identificador do serviço na API CloudHub. Sem SALESFORCE_WEBHOOK_SECRET retorna 401. Payload > 64KiB retorna 413.
// @Tags         webhooks
// @Accept       json
// @Produce      json
// @Param        X-Salesforce-Signature  header  string                 true   "HMAC-SHA256 do body em hex"
// @Param        payload                 body    sfWebhookPayload        true   "Payload do evento"
// @Success      200  {object}  map[string]string
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      413  {object}  map[string]string
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

	sig := strings.ToLower(strings.TrimSpace(c.GetHeader("X-Salesforce-Signature")))
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

	slug := payload.SObject.Slug
	if slug == "" {
		slug = payload.SObject.ID
	}
	if slug == "" {
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}

	syncCtx := context.WithoutCancel(c.Request.Context())
	go func() {
		if err := h.sfSyncSvc.SyncRecord(syncCtx, slug); err != nil {
			log.Error().Err(err).Str("slug", slug).Msg("webhook: falha ao sincronizar registro")
		} else {
			log.Info().Str("slug", slug).Msg("webhook: registro sincronizado")
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
