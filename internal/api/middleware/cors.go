package middleware

import (
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// CORSAllowedOriginsEnv é a variável de ambiente com a allowlist de origens
// (separadas por vírgula). Vazia por padrão: nenhuma origem cross-origin é permitida.
const CORSAllowedOriginsEnv = "CORS_ALLOWED_ORIGINS"

// ParseAllowedOrigins converte uma lista separada por vírgula em origens normalizadas.
// O wildcard "*" é ignorado deliberadamente.
func ParseAllowedOrigins(raw string) []string {
	var origins []string
	for _, o := range strings.Split(raw, ",") {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o == "" || o == "*" {
			continue
		}
		origins = append(origins, o)
	}
	return origins
}

// CORS usa a allowlist de CORS_ALLOWED_ORIGINS (padrão: vazia).
func CORS() gin.HandlerFunc {
	return CORSWithOrigins(ParseAllowedOrigins(os.Getenv(CORSAllowedOriginsEnv)))
}

// CORSWithOrigins permite apenas as origens informadas.
func CORSWithOrigins(allowed []string) gin.HandlerFunc {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		allowedSet[o] = struct{}{}
	}

	return func(c *gin.Context) {
		c.Writer.Header().Add("Vary", "Origin")

		if origin := c.Request.Header.Get("Origin"); origin != "" {
			if _, ok := allowedSet[origin]; ok {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization, X-Auth-Request-Token, X-Request-ID")
				c.Header("Access-Control-Expose-Headers", "X-Request-ID")
			}
		}

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}
