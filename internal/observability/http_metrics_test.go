package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func newMetricsRouter() *gin.Engine {
	r := gin.New()
	r.Use(HTTPMetrics())
	r.Use(gin.Recovery())
	r.GET("/api/public/catalog/:id", func(c *gin.Context) {
		switch c.Param("id") {
		case "falha":
			c.Status(http.StatusInternalServerError)
		case "panic":
			panic("falha inesperada")
		default:
			c.Status(http.StatusOK)
		}
	})
	return r
}

func serve(r http.Handler, method, path string) int {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w.Code
}

func TestHTTPMetrics_CountsByRouteTemplateAndStatus(t *testing.T) {
	r := newMetricsRouter()
	const route = "/api/public/catalog/:id"

	okBefore := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues(http.MethodGet, route, "200"))
	errBefore := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues(http.MethodGet, route, "500"))

	serve(r, http.MethodGet, "/api/public/catalog/a")
	serve(r, http.MethodGet, "/api/public/catalog/b")
	serve(r, http.MethodGet, "/api/public/catalog/falha")
	if code := serve(r, http.MethodGet, "/api/public/catalog/panic"); code != http.StatusInternalServerError {
		t.Fatalf("panic: status %d, esperado 500", code)
	}

	if got := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues(http.MethodGet, route, "200")) - okBefore; got != 2 {
		t.Errorf("200 na rota modelo: %v, esperado 2", got)
	}
	// O 500 do handler e o do panic recuperado (middleware antes do Recovery).
	if got := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues(http.MethodGet, route, "500")) - errBefore; got != 2 {
		t.Errorf("500 na rota modelo: %v, esperado 2", got)
	}
	// O caminho cru nunca vira rótulo.
	if got := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues(http.MethodGet, "/api/public/catalog/a", "200")); got != 0 {
		t.Errorf("caminho cru virou rótulo de rota: %v", got)
	}
}

func TestHTTPMetrics_ObservesLatencyByRoute(t *testing.T) {
	r := newMetricsRouter()
	const route = "/api/public/catalog/:id"
	before := testutil.CollectAndCount(HTTPRequestDuration)

	serve(r, http.MethodGet, "/api/public/catalog/a")

	if got := testutil.CollectAndCount(HTTPRequestDuration); got < before || got == 0 {
		t.Fatalf("histograma sem série depois da requisição: %d", got)
	}
	w := httptest.NewRecorder()
	NewMetricsServer("").Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	want := `catalogo_http_request_duration_seconds_bucket{method="GET",route="` + route + `",le="1"}`
	if body := w.Body.String(); !strings.Contains(body, want) {
		t.Errorf("/metrics sem %s", want)
	}
}

// Rota inexistente e método inventado pelo cliente não criam séries novas.
func TestHTTPMetrics_BoundsClientControlledLabels(t *testing.T) {
	r := newMetricsRouter()
	unmatchedBefore := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues(http.MethodGet, unmatchedRoute, "404"))
	otherBefore := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues("OTHER", unmatchedRoute, "404"))

	if code := serve(r, http.MethodGet, "/qualquer/caminho/123"); code != http.StatusNotFound {
		t.Fatalf("rota inexistente: status %d", code)
	}
	serve(r, "INVENTADO", "/outro")

	if got := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues(http.MethodGet, unmatchedRoute, "404")) - unmatchedBefore; got != 1 {
		t.Errorf("rota inexistente: %v em route=%q, esperado 1", got, unmatchedRoute)
	}
	if got := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues("OTHER", unmatchedRoute, "404")) - otherBefore; got != 1 {
		t.Errorf("método inventado: %v em method=OTHER, esperado 1", got)
	}
	if got := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues("INVENTADO", unmatchedRoute, "404")); got != 0 {
		t.Errorf("método do cliente virou rótulo: %v", got)
	}
}
