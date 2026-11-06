package observability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestRateLimitMiddleware(t *testing.T) {
	r := gin.New()
	r.Use(RateLimitMiddleware(2))
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	const ip = "192.0.2.10:1234"
	if w := request(r, "/", ip); w.Code != http.StatusOK {
		t.Fatalf("1ª requisição: esperado 200, obtido %d", w.Code)
	}

	w := request(r, "/", ip)
	if w.Code != http.StatusOK {
		t.Fatalf("2ª requisição: esperado 200, obtido %d", w.Code)
	}
	if got := w.Header().Get("X-RateLimit-Limit"); got != "2" {
		t.Fatalf("X-RateLimit-Limit esperado 2, obtido %q", got)
	}
	if got := w.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Fatalf("X-RateLimit-Remaining esperado 0, obtido %q", got)
	}

	for i := 0; i < 2; i++ {
		w = request(r, "/", ip)
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("acima do limite: esperado 429, obtido %d", w.Code)
		}
		if got := w.Header().Get("X-RateLimit-Remaining"); got != "0" {
			t.Fatalf("X-RateLimit-Remaining não pode ficar negativo, obtido %q", got)
		}
	}

	if w := request(r, "/", "198.51.100.7:4321"); w.Code != http.StatusOK {
		t.Fatalf("outro IP tem contador próprio: esperado 200, obtido %d", w.Code)
	}
}

func TestRateLimitMiddleware_LimiterIsPerInstance(t *testing.T) {
	newRouter := func() *gin.Engine {
		r := gin.New()
		r.Use(RateLimitMiddleware(1))
		r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
		return r
	}
	a, b := newRouter(), newRouter()
	const ip = "203.0.113.5:1"
	request(a, "/", ip)
	if w := request(a, "/", ip); w.Code != http.StatusTooManyRequests {
		t.Fatalf("esperado 429 no mesmo limitador, obtido %d", w.Code)
	}
	if w := request(b, "/", ip); w.Code != http.StatusOK {
		t.Fatalf("outro limitador não compartilha contador: esperado 200, obtido %d", w.Code)
	}
}

type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

func TestRateLimiter_RemovesExpiredEntries(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	l := newRateLimiter(1000, clock.now)

	for i := 0; i < 50; i++ {
		l.hit(fmt.Sprintf("2001:db8::%x", i))
	}
	if got := l.size(); got != 50 {
		t.Fatalf("esperadas 50 entradas na janela, obtidas %d", got)
	}

	// Passada a janela, a próxima requisição varre as entradas vencidas: só
	// fica a do IP que acabou de chegar.
	clock.advance(time.Minute + time.Second)
	if got := l.hit("2001:db8::ffff"); got != 1 {
		t.Fatalf("IP novo deve começar em 1, obtido %d", got)
	}
	if got := l.size(); got != 1 {
		t.Fatalf("entradas vencidas deviam ser removidas: esperada 1, obtidas %d", got)
	}
}

func TestRateLimiter_KeepsLiveEntriesOnSweep(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	l := newRateLimiter(1000, clock.now)

	l.hit("192.0.2.1")
	clock.advance(40 * time.Second)
	l.hit("192.0.2.2") // janela até 12:01:40
	l.hit("192.0.2.2")

	clock.advance(30 * time.Second) // 12:01:10: varre; 192.0.2.1 venceu, 192.0.2.2 não
	if got := l.hit("192.0.2.2"); got != 3 {
		t.Fatalf("a varredura não pode zerar contador em janela vigente: esperado 3, obtido %d", got)
	}
	if got := l.size(); got != 1 {
		t.Fatalf("esperada só a entrada vigente, obtidas %d", got)
	}
}

func TestRateLimiter_BoundedByMaxEntries(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	const maxEntries = 100
	l := newRateLimiter(maxEntries, clock.now)

	// Rotação de IPs dentro da mesma janela, sem nenhuma entrada vencida.
	for i := 0; i < 10*maxEntries; i++ {
		if got := l.hit(fmt.Sprintf("2001:db8:1::%x", i)); got != 1 {
			t.Fatalf("IP novo deve começar em 1, obtido %d", got)
		}
		if got := l.size(); got > maxEntries {
			t.Fatalf("mapa passou do teto: %d > %d", got, maxEntries)
		}
	}
	if got := l.size(); got != maxEntries {
		t.Fatalf("esperado o mapa cheio no teto (%d), obtido %d", maxEntries, got)
	}
}

func TestRateLimiter_ConcurrentHits(t *testing.T) {
	l := newRateLimiter(1000, time.Now)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				l.hit(fmt.Sprintf("198.51.100.%d", (g*500+i)%2000))
			}
		}(g)
	}
	wg.Wait()
	if got := l.size(); got > 1000 {
		t.Fatalf("mapa passou do teto sob concorrência: %d", got)
	}
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

func TestMetricsServer(t *testing.T) {
	HTTPClientRetriesTotal.WithLabelValues("teste").Inc()
	HTTPClientCircuitState.WithLabelValues("teste").Set(2)

	h := NewMetricsServer("").Handler

	w := request(h, "/metrics", "")
	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d", w.Code)
	}
	body, err := io.ReadAll(w.Body)
	if err != nil {
		t.Fatalf("corpo: %v", err)
	}
	for _, want := range []string{
		`catalogo_http_client_retries_total{client="teste"}`,
		`catalogo_http_client_circuit_state{client="teste"} 2`,
		"go_goroutines",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("métrica %q ausente em /metrics", want)
		}
	}
	// Métricas registradas que nada alimentava foram removidas (DEV-369).
	for _, gone := range []string{"catalogo_items_total", "catalogo_sync_items_total"} {
		if strings.Contains(string(body), gone) {
			t.Errorf("/metrics ainda expõe %q", gone)
		}
	}

	if w := request(h, "/health", ""); w.Code != http.StatusNotFound {
		t.Errorf("/health no servidor de métricas: esperado 404, obtido %d", w.Code)
	}
}
