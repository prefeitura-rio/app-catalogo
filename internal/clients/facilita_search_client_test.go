package clients

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testFacilitaInternalAPIKey = "test-facilita-internal-api-key-000000000000"

const validFacilitaCandidateResponsePrefix = `{"schema_version":"facilita-service-candidates/v2","provenance":{"catalog_revision":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","retrieval_version":"facilita-bm25-faiss-rrf/v2","query_expansion_version":"facilita-query-expansion/v2-aabbccddeeff","ranker_version":"facilita-local-hybrid-reranker/v2-aabbccddeeff"},"candidates":`

func TestFacilitaSearchClientUsesProtectedPostBodyContract(t *testing.T) {
	t.Parallel()

	var requestObserved atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestObserved.Store(true)
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/search/candidates" || request.URL.RawQuery != "" {
			t.Errorf("request target = %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("x-facilita-internal-key") != testFacilitaInternalAPIKey {
			t.Error("internal API key header is missing")
		}
		if len(request.Header.Get("x-facilita-client-id")) != 43 {
			t.Errorf("client identifier = %q", request.Header.Get("x-facilita-client-id"))
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"schema_version":"facilita-service-candidates/v2","provenance":{"catalog_revision":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","retrieval_version":"facilita-bm25-faiss-rrf/v2","query_expansion_version":"facilita-query-expansion/v2-aabbccddeeff","ranker_version":"facilita-local-hybrid-reranker/v2-aabbccddeeff"},"candidates":[{"slug":"iptu","rank":1},{"slug":"cartao-familia-carioca","rank":2}]}`))
	}))
	defer server.Close()

	client, clientError := NewFacilitaSearchClient(server.URL, testFacilitaInternalAPIKey, time.Second)
	if clientError != nil {
		t.Fatalf("NewFacilitaSearchClient: %v", clientError)
	}
	candidateBatch, candidateError := client.SearchCandidates(context.Background(), "  segunda   via iptu  ", 2)
	if candidateError != nil {
		t.Fatalf("SearchCandidates: %v", candidateError)
	}
	if !requestObserved.Load() || len(candidateBatch.Candidates) != 2 || candidateBatch.Candidates[0].Slug != "iptu" || candidateBatch.Candidates[1].Rank != 2 {
		t.Fatalf("candidate batch = %#v, request observed = %t", candidateBatch, requestObserved.Load())
	}
	if candidateBatch.Provenance.CatalogRevision == "" || candidateBatch.Provenance.RankerVersion == "" {
		t.Fatalf("candidate provenance = %#v", candidateBatch.Provenance)
	}
}

func TestFacilitaSearchClientRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		baseURL string
		apiKey  string
		timeout time.Duration
	}{
		{name: "remote HTTP", baseURL: "http://example.com", apiKey: testFacilitaInternalAPIKey, timeout: time.Second},
		{name: "unapproved cluster HTTP", baseURL: "http://other.svc.cluster.local:8080", apiKey: testFacilitaInternalAPIKey, timeout: time.Second},
		{name: "short credential", baseURL: "https://example.com", apiKey: "too-short", timeout: time.Second},
		{name: "spaced credential", baseURL: "https://example.com", apiKey: strings.Repeat("a", 31) + " ", timeout: time.Second},
		{name: "nonpositive timeout", baseURL: "https://example.com", apiKey: testFacilitaInternalAPIKey},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, clientError := NewFacilitaSearchClient(testCase.baseURL, testCase.apiKey, testCase.timeout); clientError == nil {
				t.Fatal("unsafe Facilita client configuration was accepted")
			}
		})
	}
}

func TestFacilitaSearchClientRejectsInvalidResponses(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
	}{
		{name: "unknown schema", body: `{"schema_version":"v3","provenance":{},"candidates":[]}`},
		{name: "unknown field", body: `{"schema_version":"facilita-service-candidates/v2","provenance":{},"candidates":[],"secret":"no"}`},
		{name: "missing provenance", body: `{"schema_version":"facilita-service-candidates/v2","candidates":[]}`},
		{name: "invalid catalog revision", body: `{"schema_version":"facilita-service-candidates/v2","provenance":{"catalog_revision":"latest","retrieval_version":"v2","query_expansion_version":"v2","ranker_version":"v2"},"candidates":[]}`},
		{name: "invalid slug", body: validFacilitaCandidateResponsePrefix + `[{"slug":"IPTU","rank":1}]}`},
		{name: "noncontiguous rank", body: validFacilitaCandidateResponsePrefix + `[{"slug":"iptu","rank":2}]}`},
		{name: "duplicate slug", body: validFacilitaCandidateResponsePrefix + `[{"slug":"iptu","rank":1},{"slug":"iptu","rank":2}]}`},
		{name: "excess candidates", body: validFacilitaCandidateResponsePrefix + `[{"slug":"iptu","rank":1},{"slug":"iss","rank":2},{"slug":"ipva","rank":3}]}`},
		{name: "trailing JSON", body: `{"schema_version":"facilita-service-candidates/v2","provenance":{},"candidates":[]} {}`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
				responseWriter.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(responseWriter, testCase.body)
			}))
			defer server.Close()
			client, clientError := NewFacilitaSearchClient(server.URL, testFacilitaInternalAPIKey, time.Second)
			if clientError != nil {
				t.Fatalf("NewFacilitaSearchClient: %v", clientError)
			}
			if _, candidateError := client.SearchCandidates(context.Background(), "iptu", 2); candidateError == nil {
				t.Fatal("invalid Facilita response was accepted")
			}
		})
	}
}

func TestFacilitaSearchClientRejectsRedirects(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Location", "https://example.com")
		responseWriter.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, clientError := NewFacilitaSearchClient(server.URL, testFacilitaInternalAPIKey, time.Second)
	if clientError != nil {
		t.Fatalf("NewFacilitaSearchClient: %v", clientError)
	}
	if _, candidateError := client.SearchCandidates(context.Background(), "iptu", 1); candidateError == nil {
		t.Fatal("redirect response was accepted")
	}
}
