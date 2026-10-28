package v1

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

const testWebhookSecret = "segredo-de-teste-do-webhook"

// recordingWebhookQueue registra o que o handler pediu à fila.
type recordingWebhookQueue struct {
	mu          sync.Mutex
	enqueueErr  error
	duplicate   bool
	enqueued    []string
	processed   chan *models.SalesForceWebhookEvent
	lastBody    []byte
	lastEvtType string
	lastCreated string
}

func newRecordingWebhookQueue() *recordingWebhookQueue {
	return &recordingWebhookQueue{processed: make(chan *models.SalesForceWebhookEvent, 4)}
}

func (q *recordingWebhookQueue) Enqueue(_ context.Context, body []byte, slug, eventType, eventCreated string) (*models.SalesForceWebhookEvent, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.enqueued = append(q.enqueued, slug)
	q.lastBody = append([]byte(nil), body...)
	q.lastEvtType = eventType
	q.lastCreated = eventCreated
	if q.enqueueErr != nil {
		return nil, false, q.enqueueErr
	}
	if q.duplicate {
		return nil, false, nil
	}
	return &models.SalesForceWebhookEvent{ID: int64(len(q.enqueued)), Slug: slug, Attempts: 1}, true, nil
}

func (q *recordingWebhookQueue) Process(_ context.Context, ev *models.SalesForceWebhookEvent) error {
	q.processed <- ev
	return nil
}

func (q *recordingWebhookQueue) enqueuedCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.enqueued)
}

func signWebhook(body []byte) string {
	mac := hmac.New(sha256.New, []byte(testWebhookSecret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func postWebhook(t *testing.T, h *WebhookHandler, body []byte, signature string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/webhooks/salesforce", h.SalesForce)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/salesforce", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if signature != "" {
		req.Header.Set("X-Salesforce-Signature", signature)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func responseStatus(t *testing.T, w *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("resposta não é JSON: %q", w.Body.String())
	}
	return out
}

var webhookBody = []byte(`{"event":{"type":"updated","created":"2026-10-28T12:00:00Z"},"sobject":{"Id":"a001Q000003ABCDE","Slug":"servico-teste","type":"Servico__c"}}`)

func TestWebhook_ValidEventIsPersistedBeforeResponseAndProcessed(t *testing.T) {
	q := newRecordingWebhookQueue()
	h := NewWebhookHandler(q, testWebhookSecret)

	w := postWebhook(t, h, webhookBody, signWebhook(webhookBody))
	if w.Code != http.StatusOK {
		t.Fatalf("status: esperava 200, got %d (%s)", w.Code, w.Body.String())
	}
	if got := responseStatus(t, w)["status"]; got != "queued" {
		t.Errorf("status: esperava queued, got %q", got)
	}
	if q.enqueuedCount() != 1 || q.enqueued[0] != "servico-teste" {
		t.Fatalf("esperava o evento gravado com o slug, got %v", q.enqueued)
	}
	if !bytes.Equal(q.lastBody, webhookBody) || q.lastEvtType != "updated" || q.lastCreated != "2026-10-28T12:00:00Z" {
		t.Errorf("body/tipo/criação repassados errados: %q %q %q", q.lastBody, q.lastEvtType, q.lastCreated)
	}

	select {
	case ev := <-q.processed:
		if ev.Slug != "servico-teste" {
			t.Errorf("processou slug errado: %q", ev.Slug)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("o evento gravado deveria ser processado")
	}
}

func TestWebhook_UsesIDWhenSlugIsMissing(t *testing.T) {
	q := newRecordingWebhookQueue()
	h := NewWebhookHandler(q, testWebhookSecret)
	body := []byte(`{"event":{"type":"created"},"sobject":{"Id":"a001Q000003ABCDE"}}`)

	w := postWebhook(t, h, body, signWebhook(body))
	if w.Code != http.StatusOK || q.enqueuedCount() != 1 || q.enqueued[0] != "a001Q000003ABCDE" {
		t.Fatalf("esperava o Id como slug: status=%d enqueued=%v", w.Code, q.enqueued)
	}
}

func TestWebhook_EnqueueFailureReturns503SoSenderRetries(t *testing.T) {
	q := newRecordingWebhookQueue()
	q.enqueueErr = errors.New("banco indisponível")
	h := NewWebhookHandler(q, testWebhookSecret)

	w := postWebhook(t, h, webhookBody, signWebhook(webhookBody))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: esperava 503, got %d", w.Code)
	}
	select {
	case <-q.processed:
		t.Fatal("evento não gravado não deveria ser processado")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWebhook_DuplicateDeliveryIsAcknowledgedWithoutProcessing(t *testing.T) {
	q := newRecordingWebhookQueue()
	q.duplicate = true
	h := NewWebhookHandler(q, testWebhookSecret)

	w := postWebhook(t, h, webhookBody, signWebhook(webhookBody))
	if w.Code != http.StatusOK {
		t.Fatalf("status: esperava 200, got %d", w.Code)
	}
	if got := responseStatus(t, w)["status"]; got != "duplicate" {
		t.Errorf("status: esperava duplicate, got %q", got)
	}
	select {
	case <-q.processed:
		t.Fatal("reentrega não deveria ser processada de novo")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWebhook_InvalidOrMissingSignatureIsNotPersisted(t *testing.T) {
	q := newRecordingWebhookQueue()
	h := NewWebhookHandler(q, testWebhookSecret)

	tampered := append([]byte(nil), webhookBody...)
	tampered[len(tampered)-3] = 'X'

	for name, sig := range map[string]string{
		"sem assinatura":      "",
		"assinatura de outro": signWebhook([]byte(`{}`)),
		"body adulterado":     signWebhook(webhookBody),
		"assinatura não hex":  "zz",
		"segredo diferente":   hex.EncodeToString(hmac.New(sha256.New, []byte("outro")).Sum(nil)),
	} {
		body := webhookBody
		if name == "body adulterado" {
			body = tampered
		}
		w := postWebhook(t, h, body, sig)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: esperava 401, got %d", name, w.Code)
		}
	}
	if q.enqueuedCount() != 0 {
		t.Fatalf("nenhum evento sem assinatura válida deveria ser gravado, got %v", q.enqueued)
	}
}

func TestWebhook_WithoutSecretRejectsAndDoesNotPersist(t *testing.T) {
	q := newRecordingWebhookQueue()
	h := NewWebhookHandler(q, "")

	w := postWebhook(t, h, webhookBody, signWebhook(webhookBody))
	if w.Code != http.StatusUnauthorized || q.enqueuedCount() != 0 {
		t.Fatalf("sem segredo: status=%d enqueued=%v", w.Code, q.enqueued)
	}
}

func TestWebhook_InvalidJSONAndEmptySlugAreNotPersisted(t *testing.T) {
	q := newRecordingWebhookQueue()
	h := NewWebhookHandler(q, testWebhookSecret)

	invalid := []byte(`{"sobject":`)
	if w := postWebhook(t, h, invalid, signWebhook(invalid)); w.Code != http.StatusBadRequest {
		t.Errorf("JSON inválido: esperava 400, got %d", w.Code)
	}

	empty := []byte(`{"event":{"type":"updated"},"sobject":{}}`)
	w := postWebhook(t, h, empty, signWebhook(empty))
	if w.Code != http.StatusOK || responseStatus(t, w)["status"] != "ignored" {
		t.Errorf("slug vazio: esperava 200 ignored, got %d %s", w.Code, w.Body.String())
	}
	if q.enqueuedCount() != 0 {
		t.Fatalf("nada deveria ser gravado, got %v", q.enqueued)
	}
}

func TestWebhook_OversizedPayloadIsRejected(t *testing.T) {
	q := newRecordingWebhookQueue()
	h := NewWebhookHandler(q, testWebhookSecret)
	big := bytes.Repeat([]byte("a"), int(maximumSalesForceWebhookBodyBytes)+1)

	if w := postWebhook(t, h, big, signWebhook(big)); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("esperava 413, got %d", w.Code)
	}
	if q.enqueuedCount() != 0 {
		t.Fatal("payload grande não deveria ser gravado")
	}
}

func TestWebhook_NilQueueReturns503(t *testing.T) {
	h := NewWebhookHandler(nil, testWebhookSecret)
	if w := postWebhook(t, h, webhookBody, signWebhook(webhookBody)); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("sem fila: esperava 503, got %d", w.Code)
	}
}

func TestWebhook_OversizedSlugIsRejectedWith400(t *testing.T) {
	q := newRecordingWebhookQueue()
	h := NewWebhookHandler(q, testWebhookSecret)

	long := strings.Repeat("ã", maximumSalesForceWebhookSlugChars+1)
	body := []byte(`{"event":{"type":"updated","created":"2026-10-28T12:00:00Z"},"sobject":{"Slug":"` + long + `"}}`)
	if w := postWebhook(t, h, body, signWebhook(body)); w.Code != http.StatusBadRequest {
		t.Fatalf("slug acima do limite: esperava 400 (não 503, que faria o remetente reenviar sem fim), got %d", w.Code)
	}
	if q.enqueuedCount() != 0 {
		t.Fatal("slug acima do limite não deveria ser gravado")
	}

	// No limite (em caracteres, não bytes) é aceito.
	atLimit := strings.Repeat("ã", maximumSalesForceWebhookSlugChars)
	body = []byte(`{"event":{"type":"updated","created":"2026-10-28T12:00:00Z"},"sobject":{"Slug":"` + atLimit + `"}}`)
	if w := postWebhook(t, h, body, signWebhook(body)); w.Code != http.StatusOK || q.enqueuedCount() != 1 {
		t.Fatalf("slug no limite: status=%d enqueued=%d", w.Code, q.enqueuedCount())
	}
}
