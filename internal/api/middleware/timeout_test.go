package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestTimeoutPropagatesDeadlineWithoutDetachedGinContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(Timeout(time.Millisecond))
	router.GET("/deadline", func(requestContext *gin.Context) {
		<-requestContext.Request.Context().Done()
	})

	responseRecorder := httptest.NewRecorder()
	router.ServeHTTP(responseRecorder, httptest.NewRequest(http.MethodGet, "/deadline", nil))

	if responseRecorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusGatewayTimeout)
	}
}

func TestTimeoutKeepsHandlerPanicsOnRecoveryMiddlewareGoroutine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(gin.Recovery(), Timeout(time.Second))
	router.GET("/panic", func(_ *gin.Context) {
		panic("test panic")
	})

	responseRecorder := httptest.NewRecorder()
	router.ServeHTTP(responseRecorder, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if responseRecorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", responseRecorder.Code, http.StatusInternalServerError)
	}
}
