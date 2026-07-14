package clients

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTypesenseExportSinceUsesInclusiveUpstreamCursor(t *testing.T) {
	cursor := time.Date(2026, time.July, 11, 12, 30, 0, 0, time.UTC)
	requestFilter := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestFilter <- request.URL.Query().Get("filter_by")
		responseWriter.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = responseWriter.Write([]byte(`{"id":"service-1","nome_servico":"Serviço","last_update":1783773000}` + "\n"))
	}))
	defer server.Close()

	client := NewTypesenseClient(server.URL, "test-key", "services")
	var exportedIDs []string
	if exportError := client.ExportSince(context.Background(), cursor, func(service TypesenseService) error {
		exportedIDs = append(exportedIDs, service.ID)
		return nil
	}); exportError != nil {
		t.Fatalf("ExportSince returned error: %v", exportError)
	}

	filter := <-requestFilter
	wantCursorFilter := fmt.Sprintf("last_update:>=%d", cursor.Unix())
	if !strings.Contains(filter, wantCursorFilter) {
		t.Fatalf("filter %q does not contain inclusive cursor %q", filter, wantCursorFilter)
	}
	if strings.Contains(filter, "status") || strings.Contains(filter, "awaiting_approval") {
		t.Fatalf("filter %q would hide source status transitions", filter)
	}
	if len(exportedIDs) != 1 || exportedIDs[0] != "service-1" {
		t.Fatalf("exported ids = %v, want [service-1]", exportedIDs)
	}
}

func TestTypesenseExportSinceFailsOnMalformedJSONL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = responseWriter.Write([]byte("{not-json}\n"))
	}))
	defer server.Close()

	client := NewTypesenseClient(server.URL, "test-key", "services")
	callbackCalled := false
	exportError := client.ExportSince(context.Background(), time.Time{}, func(TypesenseService) error {
		callbackCalled = true
		return nil
	})

	if exportError == nil || !strings.Contains(exportError.Error(), "linha JSONL 1 inválida") {
		t.Fatalf("ExportSince error = %v, want malformed JSONL context", exportError)
	}
	if callbackCalled {
		t.Fatal("callback was called for malformed JSONL")
	}
}

func TestTypesenseExportSincePropagatesCallbackFailureWithLineNumber(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = responseWriter.Write([]byte(`{"id":"service-1","last_update":1}` + "\n"))
	}))
	defer server.Close()

	client := NewTypesenseClient(server.URL, "test-key", "services")
	exportError := client.ExportSince(context.Background(), time.Time{}, func(TypesenseService) error {
		return fmt.Errorf("persist failed")
	})

	if exportError == nil || !strings.Contains(exportError.Error(), "processar linha JSONL 1: persist failed") {
		t.Fatalf("ExportSince error = %v, want callback context", exportError)
	}
}
