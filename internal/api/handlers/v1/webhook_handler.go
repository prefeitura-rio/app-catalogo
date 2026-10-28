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
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

const (
	maximumSalesForceWebhookBodyBytes int64 = 64 << 10
	// maximumSalesForceWebhookSlugChars é o limite da coluna slug de
	// salesforce_webhook_events: acima disso o evento nunca seria gravado.
	maximumSalesForceWebhookSlugChars = 255
)

// salesForceWebhookQueue grava o evento antes da resposta e o processa com novas
// tentativas (services.SalesForceWebhookService).
type salesForceWebhookQueue interface {
	Enqueue(ctx context.Context, body []byte, slug, eventType, eventCreated string) (*models.SalesForceWebhookEvent, bool, error)
	Process(ctx context.Context, ev *models.SalesForceWebhookEvent) error
}

type WebhookHandler struct {
	events        salesForceWebhookQueue
	webhookSecret string
}

func NewWebhookHandler(events salesForceWebhookQueue, webhookSecret string) *WebhookHandler {
	return &WebhookHandler{events: events, webhookSecret: webhookSecret}
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
// @Description  Recebe notificações de criação/atualização. Valida HMAC-SHA256 via X-Salesforce-Signature. Usa sobject.Slug (ou Id como fallback) como identificador do serviço na API CloudHub. O evento é gravado antes da resposta (queued) e reprocessado com backoff até a sincronização dar certo; reentrega do mesmo body (com event.created) responde duplicate. Sem SALESFORCE_WEBHOOK_SECRET retorna 401. Payload > 64KiB retorna 413. Falha ao gravar o evento retorna 503 (reenviar).
// @Tags         webhooks
// @Accept       json
// @Produce      json
// @Param        X-Salesforce-Signature  header  string                 true   "HMAC-SHA256 do body em hex"
// @Param        payload                 body    sfWebhookPayload        true   "Payload do evento"
// @Success      200  {object}  map[string]string
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      413  {object}  map[string]string
// @Failure      503  {object}  map[string]string
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
	if utf8.RuneCountInString(slug) > maximumSalesForceWebhookSlugChars {
		c.JSON(http.StatusBadRequest, gin.H{"error": "identificador do serviço excede o limite permitido"})
		return
	}

	if h.events == nil {
		log.Error().Msg("webhook: fila de eventos não configurada")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "fila de eventos indisponível; reenvie o evento"})
		return
	}

	// O evento é gravado antes da resposta: se o processamento falhar ou o pod
	// reiniciar, ele continua pendente e é retomado pelo reprocessador.
	ev, created, err := h.events.Enqueue(c.Request.Context(), body, slug, payload.Event.Type, payload.Event.Created)
	if err != nil {
		log.Error().Err(err).Str("slug", slug).Msg("webhook: falha ao gravar evento")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "falha ao registrar o evento; reenvie o evento"})
		return
	}
	if !created {
		c.JSON(http.StatusOK, gin.H{"status": "duplicate"})
		return
	}

	processCtx := context.WithoutCancel(c.Request.Context())
	go func() {
		// Process registra a falha e agenda a próxima tentativa; o erro já foi logado.
		_ = h.events.Process(processCtx, ev)
	}()

	c.JSON(http.StatusOK, gin.H{"status": "queued"})
}

func (h *WebhookHandler) validateHMAC(body []byte, signature string) bool {
	mac := hmac.New(sha256.New, []byte(h.webhookSecret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
