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

func TestSalesForceClientQueriesAndCachesToken(t *testing.T) {
	var tokenRequests atomic.Int32
	var queryRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/services/oauth2/token":
			tokenRequests.Add(1)
			_, _ = responseWriter.Write([]byte(`{"access_token":"salesforce-token","expires_in":300}`))
		case "/services/data/v62.0/query":
			queryRequests.Add(1)
			if request.Header.Get("Authorization") != "Bearer salesforce-token" {
				t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
			}
			_, _ = responseWriter.Write([]byte(`{"done":true,"records":[{"Id":"record-1"}]}`))
		default:
			http.NotFound(responseWriter, request)
		}
	}))
	defer server.Close()

	client, clientError := NewSalesForceClient(server.URL, "client-id", "client-secret")
	if clientError != nil {
		t.Fatalf("create Salesforce client: %v", clientError)
	}
	for range 2 {
		records, queryError := client.Query(context.Background(), "SELECT Id FROM Service__c")
		if queryError != nil || len(records) != 1 || records[0]["Id"] != "record-1" {
			t.Fatalf("Query returned records=%v error=%v", records, queryError)
		}
	}
	if tokenRequests.Load() != 1 || queryRequests.Load() != 2 {
		t.Fatalf("requests token=%d query=%d, want token=1 query=2", tokenRequests.Load(), queryRequests.Load())
	}
}

func TestSalesForceClientRenewsOnlyOnceAfterUnauthorized(t *testing.T) {
	const sensitiveBody = "sensitive-upstream-record"
	var tokenRequests atomic.Int32
	var queryRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/services/oauth2/token":
			tokenNumber := tokenRequests.Add(1)
			_, _ = fmt.Fprintf(responseWriter, `{"access_token":"token-%d","expires_in":300}`, tokenNumber)
		case "/services/data/v62.0/query":
			queryRequests.Add(1)
			responseWriter.WriteHeader(http.StatusUnauthorized)
			_, _ = responseWriter.Write([]byte(sensitiveBody))
		default:
			http.NotFound(responseWriter, request)
		}
	}))
	defer server.Close()

	client, clientError := NewSalesForceClient(server.URL, "client-id", "client-secret")
	if clientError != nil {
		t.Fatalf("create Salesforce client: %v", clientError)
	}
	_, queryError := client.Query(context.Background(), "SELECT Id FROM Service__c")
	if queryError == nil {
		t.Fatal("Query accepted repeated unauthorized responses")
	}
	if tokenRequests.Load() != 2 || queryRequests.Load() != 2 {
		t.Fatalf("requests token=%d query=%d, want token=2 query=2", tokenRequests.Load(), queryRequests.Load())
	}
	if strings.Contains(queryError.Error(), sensitiveBody) || strings.Contains(queryError.Error(), "token-") {
		t.Fatalf("Query leaked upstream data in error: %v", queryError)
	}
}

func TestSalesForceClientCollapsesConcurrentUnauthorizedRefreshes(t *testing.T) {
	var tokenRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/services/oauth2/token":
			tokenNumber := tokenRequests.Add(1)
			_, _ = fmt.Fprintf(responseWriter, `{"access_token":"token-%d","expires_in":300}`, tokenNumber)
		case "/services/data/v62.0/query":
			if request.Header.Get("Authorization") == "Bearer token-1" {
				responseWriter.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = responseWriter.Write([]byte(`{"done":true,"records":[]}`))
		default:
			http.NotFound(responseWriter, request)
		}
	}))
	defer server.Close()

	client, clientError := NewSalesForceClient(server.URL, "client-id", "client-secret")
	if clientError != nil {
		t.Fatalf("create Salesforce client: %v", clientError)
	}
	queryErrors := make(chan error, 8)
	for range 8 {
		go func() {
			_, queryError := client.Query(context.Background(), "SELECT Id FROM Service__c")
			queryErrors <- queryError
		}()
	}
	for range 8 {
		if queryError := <-queryErrors; queryError != nil {
			t.Fatalf("concurrent Query returned an error: %v", queryError)
		}
	}
	if tokenRequests.Load() != 2 {
		t.Fatalf("token requests = %d, want 2", tokenRequests.Load())
	}
}

func TestSalesForceClientBoundsQueryResponsesWithoutLeakingBodies(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		status int
		body   string
		secret string
	}{
		{name: "status body", status: http.StatusInternalServerError, body: "sensitive-record-body", secret: "sensitive-record-body"},
		{name: "oversized body", status: http.StatusOK, body: strings.Repeat("x", maximumSalesForceQueryResponseBytes+1), secret: strings.Repeat("x", 64)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/services/oauth2/token" {
					_, _ = responseWriter.Write([]byte(`{"access_token":"salesforce-token","expires_in":300}`))
					return
				}
				responseWriter.WriteHeader(testCase.status)
				_, _ = responseWriter.Write([]byte(testCase.body))
			}))
			defer server.Close()
			client, clientError := NewSalesForceClient(server.URL, "client-id", "client-secret")
			if clientError != nil {
				t.Fatalf("create Salesforce client: %v", clientError)
			}

			_, queryError := client.Query(context.Background(), "SELECT Id FROM Service__c")
			if queryError == nil || strings.Contains(queryError.Error(), testCase.secret) {
				t.Fatalf("unsafe query error = %v", queryError)
			}
		})
	}
}

func TestSalesForceClientRejectsCrossOriginPagination(t *testing.T) {
	var crossOriginRequests atomic.Int32
	crossOriginServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		crossOriginRequests.Add(1)
	}))
	defer crossOriginServer.Close()

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/services/oauth2/token" {
			_, _ = responseWriter.Write([]byte(`{"access_token":"salesforce-token","expires_in":300}`))
			return
		}
		_, _ = fmt.Fprintf(
			responseWriter,
			`{"done":false,"nextRecordsUrl":%q,"records":[]}`,
			crossOriginServer.URL+"/services/data/v62.0/query/next",
		)
	}))
	defer server.Close()

	client, clientError := NewSalesForceClient(server.URL, "client-id", "client-secret")
	if clientError != nil {
		t.Fatalf("create Salesforce client: %v", clientError)
	}
	if _, queryError := client.Query(context.Background(), "SELECT Id FROM Service__c"); queryError == nil {
		t.Fatal("Query accepted cross-origin pagination")
	}
	if crossOriginRequests.Load() != 0 {
		t.Fatalf("Salesforce client sent bearer token to %d cross-origin request(s)", crossOriginRequests.Load())
	}
}

func TestSalesForceClientRejectsRedirects(t *testing.T) {
	var redirectedRequests atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests.Add(1)
	}))
	defer redirectTarget.Close()
	redirectSource := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/services/oauth2/token" {
			_, _ = responseWriter.Write([]byte(`{"access_token":"salesforce-token","expires_in":300}`))
			return
		}
		http.Redirect(responseWriter, request, redirectTarget.URL, http.StatusTemporaryRedirect)
	}))
	defer redirectSource.Close()

	client, clientError := NewSalesForceClient(redirectSource.URL, "client-id", "client-secret")
	if clientError != nil {
		t.Fatalf("create Salesforce client: %v", clientError)
	}
	if _, queryError := client.Query(context.Background(), "SELECT Id FROM Service__c"); queryError == nil {
		t.Fatal("Salesforce client accepted a redirect response")
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("Salesforce client followed %d redirect(s)", redirectedRequests.Load())
	}
}

func TestNewSalesForceClientRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()
	for testIndex, testCase := range []struct {
		instanceURL  string
		clientID     string
		clientSecret string
	}{
		{instanceURL: "relative", clientID: "client", clientSecret: "secret"},
		{instanceURL: "ftp://salesforce.example", clientID: "client", clientSecret: "secret"},
		{instanceURL: "http://salesforce.example", clientID: "client", clientSecret: "secret"},
		{instanceURL: "https://user:secret@salesforce.example", clientID: "client", clientSecret: "secret"},
		{instanceURL: "https://salesforce.example/path", clientID: "client", clientSecret: "secret"},
		{instanceURL: "https://salesforce.example?debug=true", clientID: "client", clientSecret: "secret"},
		{instanceURL: "https://salesforce.example", clientID: "", clientSecret: "secret"},
		{instanceURL: "https://salesforce.example", clientID: "client", clientSecret: ""},
	} {
		t.Run(fmt.Sprintf("case-%d", testIndex), func(t *testing.T) {
			if _, clientError := NewSalesForceClient(
				testCase.instanceURL,
				testCase.clientID,
				testCase.clientSecret,
			); clientError == nil {
				t.Fatal("NewSalesForceClient accepted unsafe configuration")
			}
		})
	}
}

func TestSalesForceObjectTypeValidationRejectsSOQLInjection(t *testing.T) {
	client := &SalesForceClient{}
	for _, objectType := range []string{"", "1Service__c", "Service__c WHERE Id != null", "Service-Record"} {
		t.Run(objectType, func(t *testing.T) {
			if _, queryError := client.QueryAll(context.Background(), objectType); queryError == nil {
				t.Fatal("QueryAll accepted an unsafe object type")
			}
			if _, queryError := client.QueryModifiedSince(context.Background(), objectType, time.Time{}); queryError == nil {
				t.Fatal("QueryModifiedSince accepted an unsafe object type")
			}
		})
	}
}
