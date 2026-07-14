package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/config"
	"github.com/prefeitura-rio/app-catalogo/internal/datasource"
)

func TestNewHTTPServerAppliesTransportResourceBounds(t *testing.T) {
	handler := http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusNoContent)
	})
	settings := config.ServerSettings{
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      7 * time.Second,
		IdleTimeout:       11 * time.Second,
		MaxHeaderBytes:    65536,
	}

	server := newHTTPServer("127.0.0.1:8080", handler, settings)
	if server.Addr != "127.0.0.1:8080" || server.Handler == nil ||
		server.ReadHeaderTimeout != settings.ReadHeaderTimeout || server.ReadTimeout != settings.ReadTimeout ||
		server.WriteTimeout != settings.WriteTimeout || server.IdleTimeout != settings.IdleTimeout ||
		server.MaxHeaderBytes != settings.MaxHeaderBytes {
		t.Fatalf("HTTP server did not apply settings: %#v", server)
	}
	responseRecorder := httptest.NewRecorder()
	server.Handler.ServeHTTP(responseRecorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if responseRecorder.Code != http.StatusNoContent {
		t.Fatalf("configured handler status = %d, want %d", responseRecorder.Code, http.StatusNoContent)
	}
}

func TestRegisterConfiguredTypesenseDataSourceWiresManualTriggerWithoutNetwork(t *testing.T) {
	manager := datasource.NewManager()
	settings := config.TypesenseSettings{
		URL:              "https://typesense.example.test",
		APIKey:           "test-key",
		Collection:       "services",
		BaseServiceURL:   "https://prefeitura.example.test",
		SyncInterval:     15 * time.Minute,
		FullSyncInterval: 12 * time.Hour,
		SyncEnabled:      true,
	}

	if !registerConfiguredTypesenseDataSource(manager, settings, nil) {
		t.Fatal("enabled Typesense settings were not registered")
	}
	if !manager.HasSource("typesense") {
		t.Fatal("manual datasource manager cannot find Typesense after registration")
	}
}

func TestRegisterConfiguredTypesenseDataSourceSkipsIncompleteOrDisabledSettings(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		settings config.TypesenseSettings
	}{
		{
			name: "missing URL",
			settings: config.TypesenseSettings{
				APIKey:      "test-key",
				SyncEnabled: true,
			},
		},
		{
			name: "missing API key",
			settings: config.TypesenseSettings{
				URL:         "https://typesense.example.test",
				SyncEnabled: true,
			},
		},
		{
			name: "disabled",
			settings: config.TypesenseSettings{
				URL:         "https://typesense.example.test",
				APIKey:      "test-key",
				SyncEnabled: false,
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			manager := datasource.NewManager()
			if registerConfiguredTypesenseDataSource(manager, testCase.settings, nil) {
				t.Fatal("invalid Typesense settings were registered")
			}
			if manager.HasSource("typesense") {
				t.Fatal("manual datasource manager contains disabled Typesense")
			}
		})
	}
}
