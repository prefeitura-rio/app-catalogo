package middleware

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	UserCPFKey   = "user_cpf"
	UserRoleKey  = "user_role"
	UserRolesKey = "user_roles"
	UserIDKey    = "user_id"
	UserNameKey  = "user_name"
	UserEmailKey = "user_email"
)

type jwtClaims struct {
	PreferredUsername string `json:"preferred_username"`
	Sub               string `json:"sub"`
	Name              string `json:"name"`
	Email             string `json:"email"`
	ResourceAccess    map[string]struct {
		Roles []string `json:"roles"`
	} `json:"resource_access"`
}

func decodeJWT(token string) *jwtClaims {
	token = strings.TrimPrefix(token, "Bearer ")
	token = strings.TrimSpace(token)

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}

	payload := parts[1]
	if l := len(payload) % 4; l > 0 {
		payload += strings.Repeat("=", 4-l)
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}

	var claims jwtClaims
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil
	}

	return &claims
}

// ExtractUserContext decodifica o JWT injetado pelo Istio (X-Auth-Request-Token).
// A assinatura já foi validada pelo Istio — não re-validamos aqui.
func ExtractUserContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("X-Auth-Request-Token")
		if authHeader == "" {
			authHeader = c.GetHeader("Authorization")
		}

		if authHeader == "" {
			c.Next()
			return
		}

		claims := decodeJWT(authHeader)
		if claims == nil {
			c.Next()
			return
		}

		if claims.PreferredUsername != "" {
			cpf := strings.NewReplacer(".", "", "-", "").Replace(claims.PreferredUsername)
			c.Set(UserCPFKey, cpf)
		}
		if claims.Sub != "" {
			c.Set(UserIDKey, claims.Sub)
		}
		if claims.Name != "" {
			c.Set(UserNameKey, claims.Name)
		}
		if claims.Email != "" {
			c.Set(UserEmailKey, claims.Email)
		}

		role := "USER"
		if superappAccess, ok := claims.ResourceAccess["superapp"]; ok {
			c.Set(UserRolesKey, superappAccess.Roles)
			for _, r := range superappAccess.Roles {
				if r == "go:admin" || r == "admin" {
					role = "ADMIN"
					break
				}
			}
		}
		c.Set(UserRoleKey, role)

		c.Next()
	}
}

func GetUserCPF(c *gin.Context) string {
	if v, exists := c.Get(UserCPFKey); exists {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func IsAuthenticated(c *gin.Context) bool {
	return GetUserCPF(c) != ""
}

func IsAdmin(c *gin.Context) bool {
	if v, exists := c.Get(UserRoleKey); exists {
		if s, ok := v.(string); ok {
			return s == "ADMIN"
		}
	}
	return false
}

func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !IsAuthenticated(c) {
			c.JSON(401, gin.H{"error": "autenticação necessária"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !IsAdmin(c) {
			c.JSON(403, gin.H{"error": "acesso negado"})
			c.Abort()
			return
		}
		c.Next()
	}
}
