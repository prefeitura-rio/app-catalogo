package middleware

import (
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
)

const (
	UserCPFKey   = "user_cpf"
	UserRoleKey  = "user_role"
	UserRolesKey = "user_roles"
	UserIDKey    = "user_id"
	UserNameKey  = "user_name"
	UserEmailKey = "user_email"

	UserVerifiedKey = "user_verified"
)

type jwtClaims struct {
	jwt.RegisteredClaims
	AuthorizedParty   string `json:"azp"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
	Email             string `json:"email"`
	ResourceAccess    map[string]struct {
		Roles []string `json:"roles"`
	} `json:"resource_access"`
}

// normalizeCPF strips . and - and accepts only an 11-digit CPF.
func normalizeCPF(raw string) (string, bool) {
	canonical := strings.NewReplacer(".", "", "-", "").Replace(strings.TrimSpace(raw))
	if len(canonical) != 11 {
		return "", false
	}
	for _, r := range canonical {
		if !unicode.IsDigit(r) || r > unicode.MaxASCII {
			return "", false
		}
	}
	return canonical, true
}

// ExtractUserContext lê o JWT de X-Auth-Request-Token e só cria identidade e
// papel quando o verifier confere assinatura, emissor, exp, iat e azp. O header pode
// chegar direto do cliente, então nada nele é confiável sem essa verificação.
// Com verifier nil nenhuma requisição é autenticada (falha fechada).
func ExtractUserContext(verifier TokenVerifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("X-Auth-Request-Token")
		if authHeader == "" || verifier == nil {
			c.Next()
			return
		}

		claims, err := verifier.Verify(c.Request.Context(), authHeader)
		if err != nil || claims == nil {
			if err != nil {
				log.Debug().Err(err).Msg("JWT de X-Auth-Request-Token rejeitado")
			}
			c.Next()
			return
		}

		c.Set(UserVerifiedKey, true)
		if claims.PreferredUsername != "" {
			if cpf, ok := normalizeCPF(claims.PreferredUsername); ok {
				c.Set(UserCPFKey, cpf)
			}
		}
		if claims.Subject != "" {
			c.Set(UserIDKey, claims.Subject)
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
	return hasVerifiedToken(c) && GetUserCPF(c) != ""
}

func hasVerifiedToken(c *gin.Context) bool {
	v, exists := c.Get(UserVerifiedKey)
	if !exists {
		return false
	}
	ok, _ := v.(bool)
	return ok
}

func IsAdmin(c *gin.Context) bool {
	if !hasVerifiedToken(c) {
		return false
	}
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
		if !hasVerifiedToken(c) {
			c.JSON(401, gin.H{"error": "autenticação necessária"})
			c.Abort()
			return
		}
		if !IsAdmin(c) {
			c.JSON(403, gin.H{"error": "acesso negado"})
			c.Abort()
			return
		}
		c.Next()
	}
}
