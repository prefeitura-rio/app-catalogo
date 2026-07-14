package v1

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
)

const testSalesForceWebhookSecret = "salesforce-webhook-test-secret-with-entropy"

type salesForceRecordSyncerStub struct {
	mutex       sync.Mutex
	externalIDs []string
	syncError   error
}

func (syncer *salesForceRecordSyncerStub) SyncRecord(_ context.Context, externalID string) error {
	syncer.mutex.Lock()
	defer syncer.mutex.Unlock()
	syncer.externalIDs = append(syncer.externalIDs, externalID)
	return syncer.syncError
}

func (syncer *salesForceRecordSyncerStub) synchronizedExternalIDs() []string {
	syncer.mutex.Lock()
	defer syncer.mutex.Unlock()
	return append([]string(nil), syncer.externalIDs...)
}

func TestNewWebhookHandlerRejectsMissingSecret(t *testing.T) {
	if _, constructorError := NewWebhookHandler(&salesForceRecordSyncerStub{}, ""); constructorError == nil {
		t.Fatal("NewWebhookHandler accepted a missing HMAC secret")
	}
}

func TestSalesForceWebhookRejectsOversizedBodyBeforeProcessing(t *testing.T) {
	syncer := &salesForceRecordSyncerStub{}
	router := newSalesForceWebhookTestRouter(t, syncer)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/webhooks/salesforce",
		bytes.NewReader(bytes.Repeat([]byte("x"), int(MaximumSalesForceWebhookBodyBytes+1))),
	)
	responseRecorder := httptest.NewRecorder()

	router.ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized webhook status = %d, want %d", responseRecorder.Code, http.StatusRequestEntityTooLarge)
	}
	if synchronizedIDs := syncer.synchronizedExternalIDs(); len(synchronizedIDs) != 0 {
		t.Fatalf("oversized webhook synchronized records: %v", synchronizedIDs)
	}
}

func TestSalesForceWebhookRejectsInvalidSignatureBeforeProcessing(t *testing.T) {
	syncer := &salesForceRecordSyncerStub{}
	router := newSalesForceWebhookTestRouter(t, syncer)
	body := []byte(`{"sobject":{"Id":"a001Q000003ABCDEF0"}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/webhooks/salesforce", bytes.NewReader(body))
	request.Header.Set("X-Salesforce-Signature", strings.Repeat("0", sha256.Size*2))
	responseRecorder := httptest.NewRecorder()

	router.ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("invalid signature status = %d, want %d", responseRecorder.Code, http.StatusUnauthorized)
	}
	if synchronizedIDs := syncer.synchronizedExternalIDs(); len(synchronizedIDs) != 0 {
		t.Fatalf("invalid signature synchronized records: %v", synchronizedIDs)
	}
	if !strings.Contains(responseRecorder.Body.String(), `"log_id"`) {
		t.Fatalf("invalid signature response omitted log_id: %s", responseRecorder.Body.String())
	}
}

func TestSalesForceWebhookRejectsMalformedSignatureLengthsAndEncoding(t *testing.T) {
	body := []byte(`{"sobject":{"Id":"a001Q000003ABCDEF0"}}`)
	testCases := []struct {
		name      string
		signature string
	}{
		{name: "missing", signature: ""},
		{name: "short", signature: strings.Repeat("0", SalesForceWebhookSignatureHexLength-1)},
		{name: "long", signature: strings.Repeat("0", SalesForceWebhookSignatureHexLength+1)},
		{name: "non hexadecimal", signature: strings.Repeat("z", SalesForceWebhookSignatureHexLength)},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			syncer := &salesForceRecordSyncerStub{}
			router := newSalesForceWebhookTestRouter(t, syncer)
			request := httptest.NewRequest(http.MethodPost, "/api/webhooks/salesforce", bytes.NewReader(body))
			request.Header.Set("X-Salesforce-Signature", testCase.signature)
			responseRecorder := httptest.NewRecorder()

			router.ServeHTTP(responseRecorder, request)

			if responseRecorder.Code != http.StatusUnauthorized {
				t.Fatalf("malformed signature status = %d, want %d", responseRecorder.Code, http.StatusUnauthorized)
			}
			if synchronizedIDs := syncer.synchronizedExternalIDs(); len(synchronizedIDs) != 0 {
				t.Fatalf("malformed signature synchronized records: %v", synchronizedIDs)
			}
		})
	}
}

func TestSalesForceWebhookRejectsBlankRecordIdentifier(t *testing.T) {
	syncer := &salesForceRecordSyncerStub{}
	router := newSalesForceWebhookTestRouter(t, syncer)
	body := []byte(`{"sobject":{"Id":" \t\n"}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/webhooks/salesforce", bytes.NewReader(body))
	request.Header.Set("X-Salesforce-Signature", strings.ToUpper(salesForceWebhookSignature(body)))
	responseRecorder := httptest.NewRecorder()

	router.ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusBadRequest {
		t.Fatalf("blank record identifier status = %d, want %d", responseRecorder.Code, http.StatusBadRequest)
	}
	if synchronizedIDs := syncer.synchronizedExternalIDs(); len(synchronizedIDs) != 0 {
		t.Fatalf("blank record identifier synchronized records: %v", synchronizedIDs)
	}
}

func TestSalesForceWebhookSynchronizesValidSignedPayloadBeforeResponding(t *testing.T) {
	syncer := &salesForceRecordSyncerStub{}
	router := newSalesForceWebhookTestRouter(t, syncer)
	body := []byte(`{"event":{"type":"updated"},"sobject":{"Id":"a001Q000003ABCDEF0","type":"Service__c"}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/webhooks/salesforce", bytes.NewReader(body))
	request.Header.Set("X-Salesforce-Signature", salesForceWebhookSignature(body))
	responseRecorder := httptest.NewRecorder()

	router.ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("valid webhook status = %d, want %d: %s", responseRecorder.Code, http.StatusOK, responseRecorder.Body.String())
	}
	if responseRecorder.Body.String() != `{"status":"processed"}` {
		t.Fatalf("valid webhook body = %s", responseRecorder.Body.String())
	}
	synchronizedIDs := syncer.synchronizedExternalIDs()
	if len(synchronizedIDs) != 1 || synchronizedIDs[0] != "a001Q000003ABCDEF0" {
		t.Fatalf("synchronized records = %v", synchronizedIDs)
	}
}

func TestSalesForceWebhookRunsPostSyncHookBeforeResponding(t *testing.T) {
	syncer := &salesForceRecordSyncerStub{}
	hookCalled := false
	router := newSalesForceWebhookTestRouterWithHooks(t, syncer, func(context.Context) error {
		hookCalled = true
		return nil
	})
	body := []byte(`{"event":{"type":"updated"},"sobject":{"Id":"a001Q000003ABCDEF0"}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/webhooks/salesforce", bytes.NewReader(body))
	request.Header.Set("X-Salesforce-Signature", salesForceWebhookSignature(body))
	responseRecorder := httptest.NewRecorder()

	router.ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("valid webhook status = %d, want %d: %s", responseRecorder.Code, http.StatusOK, responseRecorder.Body.String())
	}
	if !hookCalled {
		t.Fatal("post-sync hook was not called")
	}
}

func TestSalesForceWebhookReportsPostSyncHookFailure(t *testing.T) {
	syncer := &salesForceRecordSyncerStub{}
	router := newSalesForceWebhookTestRouterWithHooks(t, syncer, func(context.Context) error {
		return errors.New("redis unavailable")
	})
	body := []byte(`{"event":{"type":"updated"},"sobject":{"Id":"a001Q000003ABCDEF0"}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/webhooks/salesforce", bytes.NewReader(body))
	request.Header.Set("X-Salesforce-Signature", salesForceWebhookSignature(body))
	responseRecorder := httptest.NewRecorder()

	router.ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusBadGateway {
		t.Fatalf("hook failure status = %d, want %d: %s", responseRecorder.Code, http.StatusBadGateway, responseRecorder.Body.String())
	}
	if !strings.Contains(responseRecorder.Body.String(), `"log_id"`) {
		t.Fatalf("hook failure response omitted log_id: %s", responseRecorder.Body.String())
	}
	if synchronizedIDs := syncer.synchronizedExternalIDs(); len(synchronizedIDs) != 1 {
		t.Fatalf("hook failure synchronized records = %v, want one", synchronizedIDs)
	}
}

func newSalesForceWebhookTestRouter(
	testingContext *testing.T,
	syncer SalesForceRecordSyncer,
) *gin.Engine {
	return newSalesForceWebhookTestRouterWithHooks(testingContext, syncer)
}

func newSalesForceWebhookTestRouterWithHooks(
	testingContext *testing.T,
	syncer SalesForceRecordSyncer,
	syncHooks ...SalesForceWebhookSyncHook,
) *gin.Engine {
	testingContext.Helper()
	gin.SetMode(gin.TestMode)
	handler, constructorError := NewWebhookHandler(syncer, testSalesForceWebhookSecret, syncHooks...)
	if constructorError != nil {
		testingContext.Fatalf("create webhook handler: %v", constructorError)
	}
	router := gin.New()
	router.Use(middleware.RequestID())
	router.POST("/api/webhooks/salesforce", handler.SalesForce)
	return router
}

func salesForceWebhookSignature(body []byte) string {
	messageAuthenticationCode := hmac.New(sha256.New, []byte(testSalesForceWebhookSecret))
	_, _ = messageAuthenticationCode.Write(body)
	return hex.EncodeToString(messageAuthenticationCode.Sum(nil))
}
