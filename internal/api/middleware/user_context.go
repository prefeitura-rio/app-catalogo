package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode"

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

type JWTClaimsVerifier interface {
	Verify(context.Context, string) (*VerifiedJWTClaims, error)
}

const userTokenHeader = "X-Auth-Request-Token"

// NewUserContextMiddleware authenticates the proxy-injected token before any
// citizen identity or role is added to the request context.
func NewUserContextMiddleware(verifier JWTClaimsVerifier, roleClientID string) (gin.HandlerFunc, error) {
	if verifier == nil {
		return nil, errors.New("JWT claims verifier is required")
	}
	roleClientID = strings.TrimSpace(roleClientID)
	if roleClientID == "" || strings.IndexFunc(roleClientID, unicode.IsControl) >= 0 {
		return nil, errors.New("JWT role client ID is required")
	}
	return func(c *gin.Context) {
		encodedTokens := c.Request.Header.Values(userTokenHeader)
		if len(encodedTokens) == 0 {
			c.Next()
			return
		}
		if len(encodedTokens) != 1 || strings.TrimSpace(encodedTokens[0]) == "" {
			rejectInvalidUserToken(c)
			return
		}
		claims, verificationError := verifier.Verify(c.Request.Context(), encodedTokens[0])
		if verificationError != nil || claims == nil {
			rejectInvalidUserToken(c)
			return
		}

		cpf, validCPF := normalizeCPF(claims.PreferredUsername)
		if !validCPF {
			rejectInvalidUserToken(c)
			return
		}
		c.Set(UserCPFKey, cpf)
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
		if applicationAccess, hasApplicationAccess := claims.ResourceAccess[roleClientID]; hasApplicationAccess {
			c.Set(UserRolesKey, applicationAccess.Roles)
			for _, roleName := range applicationAccess.Roles {
				if roleName == "go:admin" || roleName == "admin" {
					role = "ADMIN"
					break
				}
			}
		}
		c.Set(UserRoleKey, role)

		c.Next()
	}, nil
}

func rejectInvalidUserToken(context *gin.Context) {
	context.JSON(http.StatusUnauthorized, gin.H{
		"error":  "token de autenticação inválido",
		"log_id": context.GetString("request_id"),
	})
	context.Abort()
}

func normalizeCPF(rawCPF string) (string, bool) {
	canonicalCPF := strings.NewReplacer(".", "", "-", "").Replace(strings.TrimSpace(rawCPF))
	if len(canonicalCPF) != 11 {
		return "", false
	}
	for _, character := range canonicalCPF {
		if !unicode.IsDigit(character) || character > unicode.MaxASCII {
			return "", false
		}
	}
	return canonicalCPF, true
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
			c.JSON(401, gin.H{
				"error":  "autenticação necessária",
				"log_id": c.GetString("request_id"),
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !IsAuthenticated(c) {
			c.JSON(401, gin.H{
				"error":  "autenticação necessária",
				"log_id": c.GetString("request_id"),
			})
			c.Abort()
			return
		}
		if !IsAdmin(c) {
			c.JSON(403, gin.H{
				"error":  "acesso negado",
				"log_id": c.GetString("request_id"),
			})
			c.Abort()
			return
		}
		c.Next()
	}
}
