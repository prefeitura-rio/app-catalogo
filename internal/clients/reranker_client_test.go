package clients

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRerankerClientReturnsTransportErrorsForObservableFallback(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	}))
	serverURL := testServer.URL
	testServer.Close()

	rerankerClient, clientError := NewRerankerClient(serverURL, time.Second)
	if clientError != nil {
		t.Fatalf("create reranker client: %v", clientError)
	}
	_, rerankerError := rerankerClient.Rerank(context.Background(), "query", nil)
	if rerankerError == nil || !strings.Contains(rerankerError.Error(), "request") {
		t.Fatalf("error = %v, want an observable transport error", rerankerError)
	}
}

func TestRerankerClientRejectsOversizedResponses(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(responseWriter, `[{"id":"candidate","score":1,"padding":"`)
		_, _ = io.WriteString(responseWriter, strings.Repeat("x", maximumRerankerResponseBytes))
		_, _ = io.WriteString(responseWriter, `"}]`)
	}))
	defer testServer.Close()

	rerankerClient, clientError := NewRerankerClient(testServer.URL, time.Second)
	if clientError != nil {
		t.Fatalf("create reranker client: %v", clientError)
	}
	_, rerankerError := rerankerClient.Rerank(context.Background(), "query", nil)
	if rerankerError == nil || !strings.Contains(rerankerError.Error(), "size limit") {
		t.Fatalf("error = %v, want a bounded response error", rerankerError)
	}
}

func TestNewRerankerClientRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		baseURL string
		timeout time.Duration
	}{
		{baseURL: "relative"},
		{baseURL: "ftp://reranker.example", timeout: time.Second},
		{baseURL: "http://reranker.example", timeout: time.Second},
		{baseURL: "https://user:secret@reranker.example", timeout: time.Second},
		{baseURL: "https://reranker.example?debug=true", timeout: time.Second},
		{baseURL: "https://reranker.example", timeout: 0},
	} {
		if _, clientError := NewRerankerClient(testCase.baseURL, testCase.timeout); clientError == nil {
			t.Fatalf("NewRerankerClient accepted baseURL=%q timeout=%s", testCase.baseURL, testCase.timeout)
		}
	}
}

func TestRerankerClientRejectsOversizedRequests(t *testing.T) {
	t.Parallel()

	rerankerClient, clientError := NewRerankerClient("https://reranker.example", time.Second)
	if clientError != nil {
		t.Fatalf("create reranker client: %v", clientError)
	}
	_, rerankerError := rerankerClient.Rerank(context.Background(), "query", []RerankerDocument{{
		ID:   "candidate",
		Text: strings.Repeat("x", maximumRerankerRequestBytes),
	}})
	if rerankerError == nil || !strings.Contains(rerankerError.Error(), "request exceeds") {
		t.Fatalf("error = %v, want bounded request rejection", rerankerError)
	}
}

func TestRerankerClientNeverFollowsCrossHostRedirects(t *testing.T) {
	for _, redirectStatus := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(redirectStatus), func(t *testing.T) {
			var redirectedRequests atomic.Int32
			redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				redirectedRequests.Add(1)
			}))
			defer redirectTarget.Close()
			redirectSource := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
				http.Redirect(responseWriter, request, redirectTarget.URL, redirectStatus)
			}))
			defer redirectSource.Close()

			rerankerClient, clientError := NewRerankerClient(redirectSource.URL, time.Second)
			if clientError != nil {
				t.Fatalf("create reranker client: %v", clientError)
			}
			_, rerankerError := rerankerClient.Rerank(
				context.Background(),
				"privacy-sensitive-query",
				[]RerankerDocument{{ID: "one", Text: "catalog document"}},
			)
			if rerankerError == nil || !strings.Contains(rerankerError.Error(), "status") {
				t.Fatalf("redirect error = %v", rerankerError)
			}
			if redirectedRequests.Load() != 0 {
				t.Fatalf("reranker followed %d redirected request(s)", redirectedRequests.Load())
			}
		})
	}
}
