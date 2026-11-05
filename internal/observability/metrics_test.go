package observability

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func request(r http.Handler, path, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

type logLine struct {
	Level     string `json:"level"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	RequestID string `json:"request_id"`
	IP        string `json:"ip"`
	Message   string `json:"message"`
}

func TestRequestLogger(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = previous })

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("request_id", "req-1"); c.Next() })
	r.Use(RequestLogger())
	r.GET("/status/:code", func(c *gin.Context) {
		switch c.Param("code") {
		case "500":
			c.Status(http.StatusInternalServerError)
		case "404":
			c.Status(http.StatusNotFound)
		default:
			c.Status(http.StatusOK)
		}
	})

	cases := []struct {
		code  string
		level string
		want  int
	}{
		{"200", "info", http.StatusOK},
		{"404", "warn", http.StatusNotFound},
		{"500", "error", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		buf.Reset()
		request(r, "/status/"+tc.code+"?cpf=nao-logar", "192.0.2.20:5555")

		var line logLine
		if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
			t.Fatalf("log não é JSON (%v): %q", err, buf.String())
		}
		if line.Level != tc.level || line.Status != tc.want {
			t.Fatalf("status %s: nível/status esperados %s/%d, obtidos %s/%d", tc.code, tc.level, tc.want, line.Level, line.Status)
		}
		if line.Method != http.MethodGet || line.Path != "/status/"+tc.code || line.RequestID != "req-1" || line.IP != "192.0.2.20" || line.Message != "request" {
			t.Fatalf("campos inesperados: %+v", line)
		}
		if strings.Contains(buf.String(), "nao-logar") {
			t.Fatal("a query string não deve ir para o log")
		}
	}
}

func TestMetricsHandler(t *testing.T) {
	SyncItemsTotal.WithLabelValues("teste", "ok").Inc()
	HTTPClientRetriesTotal.WithLabelValues("teste").Inc()
	CatalogItemsGauge.WithLabelValues("service", "teste").Set(3)
	HTTPClientCircuitState.WithLabelValues("teste").Set(2)

	r := gin.New()
	r.GET("/metrics", MetricsHandler())

	w := request(r, "/metrics", "")
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d", w.Code)
	}
	body, err := io.ReadAll(w.Body)
	if err != nil {
		t.Fatalf("corpo: %v", err)
	}
	for _, want := range []string{
		`catalogo_sync_items_total{source="teste",status="ok"}`,
		`catalogo_http_client_retries_total{client="teste"}`,
		`catalogo_items_total{source="teste",type="service"} 3`,
		`catalogo_http_client_circuit_state{client="teste"} 2`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("métrica %q ausente em /metrics", want)
		}
	}
}
