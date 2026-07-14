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

func TestKeycloakTokenManagerCachesBoundedToken(t *testing.T) {
	var tokenRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		tokenRequests.Add(1)
		if request.URL.Path != "/realms/rio/protocol/openid-connect/token" {
			t.Errorf("token path = %q", request.URL.Path)
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"access_token":"service-token","expires_in":300}`))
	}))
	defer server.Close()

	manager, managerError := NewKeycloakTokenManager(server.URL, "rio", "catalog-client", "client-secret")
	if managerError != nil {
		t.Fatalf("create Keycloak token manager: %v", managerError)
	}
	for range 2 {
		token, tokenError := manager.GetToken(context.Background())
		if tokenError != nil || token != "service-token" {
			t.Fatalf("GetToken returned token=%q error=%v", token, tokenError)
		}
	}
	if tokenRequests.Load() != 1 {
		t.Fatalf("token requests = %d, want 1", tokenRequests.Load())
	}
}

func TestKeycloakTokenManagerBoundsResponsesWithoutLeakingBodies(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		status int
		body   string
		secret string
	}{
		{name: "status body", status: http.StatusUnauthorized, body: "upstream-secret-body", secret: "upstream-secret-body"},
		{name: "oversized body", status: http.StatusOK, body: strings.Repeat("x", int(maximumKeycloakTokenResponseBytes+1)), secret: strings.Repeat("x", 64)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
				responseWriter.WriteHeader(testCase.status)
				_, _ = responseWriter.Write([]byte(testCase.body))
			}))
			defer server.Close()
			manager, managerError := NewKeycloakTokenManager(server.URL, "rio", "catalog-client", "client-secret")
			if managerError != nil {
				t.Fatalf("create Keycloak token manager: %v", managerError)
			}
			_, tokenError := manager.GetToken(context.Background())
			if tokenError == nil || strings.Contains(tokenError.Error(), testCase.secret) {
				t.Fatalf("unsafe token error = %v", tokenError)
			}
		})
	}
}

func TestKeycloakTokenManagerRejectsRedirects(t *testing.T) {
	var redirectedRequests atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests.Add(1)
	}))
	defer redirectTarget.Close()
	redirectSource := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		http.Redirect(responseWriter, request, redirectTarget.URL, http.StatusTemporaryRedirect)
	}))
	defer redirectSource.Close()

	manager, managerError := NewKeycloakTokenManager(redirectSource.URL, "rio", "catalog-client", "client-secret")
	if managerError != nil {
		t.Fatalf("create Keycloak token manager: %v", managerError)
	}
	if _, tokenError := manager.GetToken(context.Background()); tokenError == nil {
		t.Fatal("Keycloak token manager accepted a redirect response")
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("Keycloak client followed %d redirect(s)", redirectedRequests.Load())
	}
}

func TestNewKeycloakTokenManagerRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	for testIndex, testCase := range []struct {
		baseURL      string
		realm        string
		clientID     string
		clientSecret string
	}{
		{baseURL: "relative", realm: "rio", clientID: "client", clientSecret: "secret"},
		{baseURL: "ftp://identity.example", realm: "rio", clientID: "client", clientSecret: "secret"},
		{baseURL: "http://identity.example", realm: "rio", clientID: "client", clientSecret: "secret"},
		{baseURL: "https://user:secret@identity.example", realm: "rio", clientID: "client", clientSecret: "secret"},
		{baseURL: "https://identity.example?debug=true", realm: "rio", clientID: "client", clientSecret: "secret"},
		{baseURL: "https://identity.example", realm: "../admin", clientID: "client", clientSecret: "secret"},
		{baseURL: "https://identity.example", realm: "rio", clientID: "", clientSecret: "secret"},
		{baseURL: "https://identity.example", realm: "rio", clientID: "client", clientSecret: ""},
	} {
		t.Run(fmt.Sprintf("case-%d", testIndex), func(t *testing.T) {
			if _, managerError := NewKeycloakTokenManager(
				testCase.baseURL,
				testCase.realm,
				testCase.clientID,
				testCase.clientSecret,
			); managerError == nil {
				t.Fatal("NewKeycloakTokenManager accepted unsafe configuration")
			}
		})
	}
}

func TestKeycloakTokenManagerRefreshesConcurrentCallersOnce(t *testing.T) {
	var tokenRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		tokenRequests.Add(1)
		time.Sleep(10 * time.Millisecond)
		_, _ = responseWriter.Write([]byte(`{"access_token":"shared-token","expires_in":300}`))
	}))
	defer server.Close()
	manager, managerError := NewKeycloakTokenManager(server.URL, "rio", "catalog-client", "client-secret")
	if managerError != nil {
		t.Fatalf("create Keycloak token manager: %v", managerError)
	}

	errorChannel := make(chan error, 8)
	for range 8 {
		go func() {
			_, tokenError := manager.GetToken(context.Background())
			errorChannel <- tokenError
		}()
	}
	for range 8 {
		if tokenError := <-errorChannel; tokenError != nil {
			t.Fatalf("concurrent GetToken returned an error: %v", tokenError)
		}
	}
	if tokenRequests.Load() != 1 {
		t.Fatalf("concurrent token requests = %d, want 1", tokenRequests.Load())
	}
}
