package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newTimeoutRouter(d time.Duration, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.Use(Timeout(d))
	r.GET("/", handler)
	return r
}

func serve(r *gin.Engine, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestTimeout_DeadlineExceededWithoutResponse(t *testing.T) {
	r := newTimeoutRouter(20*time.Millisecond, func(c *gin.Context) {
		<-c.Request.Context().Done()
	})

	w := serve(r, map[string]string{RequestIDHeader: "req-timeout"})
	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("esperado 504, obtido %d", w.Code)
	}
	var body struct {
		Error string `json:"error"`
		LogID string `json:"log_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body.Error == "" || body.LogID != "req-timeout" {
		t.Fatalf("corpo inesperado: %+v", body)
	}
}

func TestTimeout_KeepsCommittedResponse(t *testing.T) {
	r := newTimeoutRouter(20*time.Millisecond, func(c *gin.Context) {
		<-c.Request.Context().Done()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "dependência fora"})
	})

	if w := serve(r, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("resposta já escrita deve ser mantida: esperado 503, obtido %d", w.Code)
	}
}

func TestTimeout_PropagatesDeadlineAndPassesFastHandlers(t *testing.T) {
	r := newTimeoutRouter(time.Minute, func(c *gin.Context) {
		if _, ok := c.Request.Context().Deadline(); !ok {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusNoContent)
	})

	if w := serve(r, nil); w.Code != http.StatusNoContent {
		t.Fatalf("esperado 204 com deadline no contexto, obtido %d", w.Code)
	}
}

func TestTimeout_DisabledForNonPositiveDuration(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		r := newTimeoutRouter(d, func(c *gin.Context) {
			if _, ok := c.Request.Context().Deadline(); ok {
				c.Status(http.StatusInternalServerError)
				return
			}
			c.Status(http.StatusOK)
		})
		if w := serve(r, nil); w.Code != http.StatusOK {
			t.Fatalf("duração %v não deve impor deadline: obtido %d", d, w.Code)
		}
	}
}

func TestTimeout_CanceledByClientIsNotTimeout(t *testing.T) {
	r := newTimeoutRouter(time.Minute, func(c *gin.Context) {
		<-c.Request.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusGatewayTimeout {
		t.Fatal("cancelamento do cliente não é estouro de tempo limite")
	}
}

func TestRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, c.GetString("request_id"))
	})

	t.Run("reaproveita o header do cliente", func(t *testing.T) {
		w := serve(r, map[string]string{RequestIDHeader: "abc-123"})
		if got := w.Header().Get(RequestIDHeader); got != "abc-123" {
			t.Fatalf("header esperado abc-123, obtido %q", got)
		}
		if w.Body.String() != "abc-123" {
			t.Fatalf("contexto esperado abc-123, obtido %q", w.Body.String())
		}
	})

	t.Run("gera um id quando ausente", func(t *testing.T) {
		first := serve(r, nil).Header().Get(RequestIDHeader)
		second := serve(r, nil).Header().Get(RequestIDHeader)
		if len(first) != 36 || len(second) != 36 {
			t.Fatalf("esperado UUID, obtido %q e %q", first, second)
		}
		if first == second {
			t.Fatal("cada requisição deve receber um id próprio")
		}
	})
}
