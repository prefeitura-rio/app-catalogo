package middleware

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
)

func newCORSRouter(h gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(h)
	r.GET("/test", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	return r
}

func doCORS(r *gin.Engine, method, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/test", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if method == http.MethodOptions {
		req.Header.Set("Access-Control-Request-Method", "GET")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCORS_AllowedOrigin(t *testing.T) {
	r := newCORSRouter(CORSWithOrigins([]string{"https://example.com"}))
	w := doCORS(r, http.MethodGet, "https://example.com")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Errorf("Allow-Origin = %q, want https://example.com", got)
	}
}

func TestCORS_DeniedOrigin(t *testing.T) {
	r := newCORSRouter(CORSWithOrigins([]string{"https://example.com"}))
	w := doCORS(r, http.MethodGet, "https://evil.com")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want empty", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Errorf("Allow-Methods = %q, want empty", got)
	}
}

func TestCORS_NoOriginHeader(t *testing.T) {
	r := newCORSRouter(CORSWithOrigins([]string{"https://example.com"}))
	w := doCORS(r, http.MethodGet, "")

	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("got %d %q, want 200 ok", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want empty", got)
	}
}

func TestCORS_PreflightAllowedOrigin(t *testing.T) {
	r := newCORSRouter(CORSWithOrigins([]string{"https://example.com"}))
	w := doCORS(r, http.MethodOptions, "https://example.com")

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Errorf("Allow-Origin = %q, want https://example.com", got)
	}
	if w.Header().Get("Access-Control-Allow-Methods") == "" || w.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Error("expected Allow-Methods and Allow-Headers on allowed preflight")
	}
}

func TestCORS_PreflightDeniedOrigin(t *testing.T) {
	r := newCORSRouter(CORSWithOrigins([]string{"https://example.com"}))
	w := doCORS(r, http.MethodOptions, "https://evil.com")

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want empty", got)
	}
}

func TestCORS_DefaultEmptyAllowlist(t *testing.T) {
	t.Setenv(CORSAllowedOriginsEnv, "")
	w := doCORS(newCORSRouter(CORS()), http.MethodGet, "https://example.com")

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want empty by default", got)
	}
}

func TestCORS_EnvAllowlist(t *testing.T) {
	t.Setenv(CORSAllowedOriginsEnv, " https://a.com/ , https://b.com,*")
	if got := ParseAllowedOrigins(" https://a.com/ , https://b.com,*"); !reflect.DeepEqual(got, []string{"https://a.com", "https://b.com"}) {
		t.Fatalf("ParseAllowedOrigins = %v", got)
	}
	w := doCORS(newCORSRouter(CORS()), http.MethodGet, "https://b.com")
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://b.com" {
		t.Errorf("Allow-Origin = %q, want https://b.com", got)
	}
}
