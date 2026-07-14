package v1

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
)

const (
	MaximumSalesForceWebhookBodyBytes   int64 = 64 << 10
	SalesForceWebhookSignatureHexLength       = sha256.Size * 2
)

type SalesForceRecordSyncer interface {
	SyncRecord(ctx context.Context, externalID string) error
}

type SalesForceWebhookSyncHook func(ctx context.Context) error

type WebhookHandler struct {
	recordSyncer  SalesForceRecordSyncer
	webhookSecret string
	syncHooks     []SalesForceWebhookSyncHook
}

func NewWebhookHandler(
	recordSyncer SalesForceRecordSyncer,
	webhookSecret string,
	syncHooks ...SalesForceWebhookSyncHook,
) (*WebhookHandler, error) {
	if strings.TrimSpace(webhookSecret) == "" {
		return nil, errors.New("salesforce webhook secret is required")
	}
	if recordSyncer == nil {
		return nil, errors.New("salesforce webhook record syncer is required")
	}
	for hookIndex, syncHook := range syncHooks {
		if syncHook == nil {
			return nil, fmt.Errorf("salesforce webhook sync hook %d is nil", hookIndex)
		}
	}
	return &WebhookHandler{
		recordSyncer:  recordSyncer,
		webhookSecret: webhookSecret,
		syncHooks:     append([]SalesForceWebhookSyncHook(nil), syncHooks...),
	}, nil
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
// @Param        X-Salesforce-Signature  header  string                 true   "HMAC-SHA256 do body em hex"
// @Param        payload                 body    sfWebhookPayload        true   "Payload do evento"
// @Success      200  {object}  map[string]string
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      413  {object}  map[string]string
// @Failure      502  {object}  map[string]string
// @Failure      504  {object}  map[string]string
// @Router       /api/webhooks/salesforce [post]
func (h *WebhookHandler) SalesForce(c *gin.Context) {
	requestID := c.GetString("request_id")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaximumSalesForceWebhookBodyBytes)
	body, readError := io.ReadAll(c.Request.Body)
	if readError != nil {
		var maximumBytesError *http.MaxBytesError
		if errors.As(readError, &maximumBytesError) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error":  "payload excede o limite permitido",
				"log_id": requestID,
			})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "falha ao ler payload",
			"log_id": requestID,
		})
		return
	}

	if !h.validateHMAC(body, c.GetHeader("X-Salesforce-Signature")) {
		log.Warn().Str("request_id", requestID).Msg("webhook: assinatura inválida")
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":  "assinatura inválida",
			"log_id": requestID,
		})
		return
	}

	var payload sfWebhookPayload
	if unmarshalError := json.Unmarshal(body, &payload); unmarshalError != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "payload inválido",
			"log_id": requestID,
		})
		return
	}

	if strings.TrimSpace(payload.SObject.ID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "payload não contém o identificador do registro",
			"log_id": requestID,
		})
		return
	}

	if syncError := h.recordSyncer.SyncRecord(c.Request.Context(), payload.SObject.ID); syncError != nil {
		statusCode := http.StatusBadGateway
		if errors.Is(syncError, context.DeadlineExceeded) || errors.Is(c.Request.Context().Err(), context.DeadlineExceeded) {
			statusCode = http.StatusGatewayTimeout
		}
		log.Error().
			Str("error_type", fmt.Sprintf("%T", syncError)).
			Str("request_id", requestID).
			Msg("webhook: falha ao sincronizar registro")
		c.JSON(statusCode, gin.H{
			"error":  "falha ao sincronizar registro",
			"log_id": requestID,
		})
		return
	}
	for _, syncHook := range h.syncHooks {
		if hookError := syncHook(c.Request.Context()); hookError != nil {
			log.Error().
				Str("error_type", fmt.Sprintf("%T", hookError)).
				Str("request_id", requestID).
				Msg("webhook: registro sincronizado, mas hook pós-sync falhou")
			c.JSON(http.StatusBadGateway, gin.H{
				"error":  "registro sincronizado, mas a atualização de cache falhou",
				"log_id": requestID,
			})
			return
		}
	}

	log.Info().Str("request_id", requestID).Msg("webhook: registro sincronizado")
	c.JSON(http.StatusOK, gin.H{"status": "processed"})
}

func (h *WebhookHandler) validateHMAC(body []byte, signature string) bool {
	providedSignature, decodeError := hex.DecodeString(signature)
	if decodeError != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(h.webhookSecret))
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), providedSignature)
}
