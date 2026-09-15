package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func Timeout(duration time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if duration <= 0 {
			c.Next()
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), duration)
		defer cancel()

		c.Request = c.Request.WithContext(ctx)
		c.Next()

		// Gin contexts and response writers are not safe to use from a detached
		// handler goroutine. Downstream I/O receives the deadline through the
		// request context; emit a timeout only when the handler returned without
		// already committing a response.
		if ctx.Err() == context.DeadlineExceeded && !c.Writer.Written() {
			c.AbortWithStatusJSON(http.StatusGatewayTimeout, gin.H{
				"error":  "tempo limite da requisição excedido",
				"log_id": c.GetString("request_id"),
			})
		}
	}
}
