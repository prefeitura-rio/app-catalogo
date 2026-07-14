package clients

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAppGoAPIClientRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(strings.Repeat("x", int(maximumAppGoAPIResponseBytes+1))))
	}))
	defer server.Close()

	client := newAuthenticatedAppGoAPIClientForTest(server.URL)
	var destination map[string]any
	requestError := client.doGet(context.Background(), "/oversized", &destination)

	if requestError == nil || !strings.Contains(requestError.Error(), "resposta excede o limite") {
		t.Fatalf("doGet error = %v, want response size failure", requestError)
	}
}

func TestAppGoAPIClientDoesNotExposeUpstreamErrorBody(t *testing.T) {
	const sensitiveBody = "upstream-sensitive-detail"
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		http.Error(responseWriter, sensitiveBody, http.StatusInternalServerError)
	}))
	defer server.Close()

	client := newAuthenticatedAppGoAPIClientForTest(server.URL)
	var destination map[string]any
	requestError := client.doGet(context.Background(), "/failure", &destination)

	if requestError == nil || !strings.Contains(requestError.Error(), "status 500") {
		t.Fatalf("doGet error = %v, want status context", requestError)
	}
	if strings.Contains(requestError.Error(), sensitiveBody) {
		t.Fatalf("doGet error exposed upstream body: %v", requestError)
	}
}

func TestAppGoAPIClientSendsServiceBearerToken(t *testing.T) {
	authorizationHeader := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		authorizationHeader <- request.Header.Get("Authorization")
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := newAuthenticatedAppGoAPIClientForTest(server.URL)
	var destination map[string]bool
	if requestError := client.doGet(context.Background(), "/success", &destination); requestError != nil {
		t.Fatalf("doGet returned error: %v", requestError)
	}
	if !destination["ok"] {
		t.Fatalf("decoded destination = %v, want ok=true", destination)
	}
	if authorization := <-authorizationHeader; authorization != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want Bearer test-token", authorization)
	}
}

func TestAppGoAPIClientRequiresExplicitPaginationEvidence(t *testing.T) {
	testCases := []struct {
		name         string
		responseBody string
		wantError    string
	}{
		{
			name:         "missing total",
			responseBody: `{"data":{"courses":[],"pagination":{"page":1}}}`,
			wantError:    "omitted pagination total",
		},
		{
			name:         "missing page",
			responseBody: `{"data":{"courses":[],"pagination":{"total":0}}}`,
			wantError:    "omitted pagination page",
		},
		{
			name:         "wrong page",
			responseBody: `{"data":{"courses":[],"pagination":{"total":0,"page":2}}}`,
			wantError:    "does not match requested page 1",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
				responseWriter.Header().Set("Content-Type", "application/json")
				_, _ = responseWriter.Write([]byte(testCase.responseBody))
			}))
			defer server.Close()

			client := newAuthenticatedAppGoAPIClientForTest(server.URL)
			_, _, requestError := client.GetCourses(context.Background(), 1, time.Time{})
			if requestError == nil || !strings.Contains(requestError.Error(), testCase.wantError) {
				t.Fatalf("GetCourses error = %v, want %q", requestError, testCase.wantError)
			}
		})
	}
}

func TestAppGoAPIClientAcceptsExplicitEmptyPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"data":{"courses":[],"pagination":{"total":0,"page":1}}}`))
	}))
	defer server.Close()

	client := newAuthenticatedAppGoAPIClientForTest(server.URL)
	courses, total, requestError := client.GetCourses(context.Background(), 1, time.Time{})
	if requestError != nil {
		t.Fatalf("GetCourses returned error: %v", requestError)
	}
	if len(courses) != 0 || total != 0 {
		t.Fatalf("explicit empty page = %d courses, total %d; want 0, 0", len(courses), total)
	}
}

func newAuthenticatedAppGoAPIClientForTest(baseURL string) *AppGoAPIClient {
	tokenManager := &KeycloakTokenManager{
		token:     "test-token",
		expiresAt: time.Now().Add(time.Hour),
		now:       time.Now,
	}
	return NewAppGoAPIClient(baseURL, tokenManager)
}
