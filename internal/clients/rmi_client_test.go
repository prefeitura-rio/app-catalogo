package clients

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// CPF sintético (não pertence a ninguém): só precisa ter 11 dígitos.
const syntheticCPF = "00000000191"

func newTestRMIClient(t *testing.T, handler http.HandlerFunc, cfg ResilienceConfig) (*RMIClient, *atomic.Int32) {
	t.Helper()
	var citizenCalls atomic.Int32
	srv := httptest.NewServer(withKeycloakToken(func(w http.ResponseWriter, r *http.Request) {
		citizenCalls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	client := NewRMIClient(srv.URL, NewKeycloakTokenManager(srv.URL, "test-realm", "client", "secret"))
	client.resilience = newResilientCaller(uniqueClientName(t), cfg)
	return client, &citizenCalls
}

func writeCitizen(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"cpf": syntheticCPF, "escolaridade": "medio"})
}

func TestRMIClient_RepeteEm503EDevolveCidadao(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	client, calls := newTestRMIClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeCitizen(w)
	}, fastResilience(5))

	citizen, err := client.GetCitizen(context.Background(), syntheticCPF)
	if err != nil {
		t.Fatalf("GetCitizen: %v", err)
	}
	if citizen == nil || citizen.Escolaridade != "medio" {
		t.Fatalf("cidadão inesperado: %+v", citizen)
	}
	if calls.Load() != 3 {
		t.Fatalf("esperava 3 chamadas ao RMI, got %d", calls.Load())
	}
	if got := retriesMetric(client.resilience.client); got != 2 {
		t.Fatalf("retries=%v, want 2", got)
	}
}

func TestRMIClient_4xxE404NaoRepetem(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		status int
	}{
		{"400", http.StatusBadRequest},
		{"403", http.StatusForbidden},
		{"404", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, calls := newTestRMIClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}, fastResilience(1))

			citizen, err := client.GetCitizen(context.Background(), syntheticCPF)
			if tc.status == http.StatusNotFound {
				if err != nil || citizen != nil {
					t.Fatalf("404 deve voltar (nil, nil), got (%v, %v)", citizen, err)
				}
			} else if err == nil {
				t.Fatalf("esperava erro para status %d", tc.status)
			}
			if calls.Load() != 1 {
				t.Fatalf("status %d não deve repetir, got %d chamadas", tc.status, calls.Load())
			}
			if client.CircuitState() != CircuitClosed {
				t.Fatalf("status %d não deve abrir o circuito", tc.status)
			}
		})
	}
}

func TestRMIClient_CircuitoAbreERecusaSemChamarORMI(t *testing.T) {
	t.Parallel()

	client, calls := newTestRMIClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}, fastResilience(3))

	// 3 tentativas com 502 → 3 falhas consecutivas → circuito aberto.
	if _, err := client.GetCitizen(context.Background(), syntheticCPF); err == nil {
		t.Fatal("esperava erro com o RMI respondendo 502")
	}
	if client.CircuitState() != CircuitOpen {
		t.Fatalf("esperava circuito aberto, got %s", client.CircuitState())
	}
	callsWhenOpened := calls.Load()

	for i := 0; i < 5; i++ {
		_, err := client.GetCitizen(context.Background(), syntheticCPF)
		if !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("esperava ErrCircuitOpen, got %v", err)
		}
	}
	if calls.Load() != callsWhenOpened {
		t.Fatalf("com o circuito aberto o RMI não pode ser chamado: %d → %d", callsWhenOpened, calls.Load())
	}
}

func TestRMIClient_ErroDeTransporteNaoExpoeCPF(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(withKeycloakToken(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	tokenManager := NewKeycloakTokenManager(srv.URL, "test-realm", "client", "secret")

	// Porta fechada: a conexão é recusada em toda tentativa.
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	client := NewRMIClient(closedURL, tokenManager)
	client.resilience = newResilientCaller(uniqueClientName(t), fastResilience(5))

	_, err := client.GetCitizen(context.Background(), syntheticCPF)
	if err == nil {
		t.Fatal("esperava erro de transporte")
	}
	if strings.Contains(err.Error(), syntheticCPF) {
		t.Fatalf("erro expõe o CPF: %v", err)
	}
	if !isTransient(err) {
		t.Fatalf("erro de transporte deve ser transitório: %v", err)
	}
}
