package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestExtractUserContext_HeimdallRoles(t *testing.T) {
	keys := newTestKeys(t)
	jwks := newJWKSServer(t, keys)
	verifier := newVerifier(t, jwks.URL)

	// Token sem role admin no JWT: se o Heimdall conceder, deve passar.
	tokenNoJWTAdmin := sign(t, jwt.SigningMethodRS256, testRSAKid, keys.rsa, claimsFor(regularRole))

	t.Run("Heimdall go:admin concede ADMIN", func(t *testing.T) {
		heimdall := newHeimdallServer(t, http.StatusOK, []string{"go:admin"})
		r := newTestRouterWithHeimdall(verifier, heimdall.URL)
		if w := do(r, "/admin", tokenNoJWTAdmin); w.Code != http.StatusOK {
			t.Fatalf("/admin: esperado 200, obtido %d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("Heimdall superadmin concede ADMIN", func(t *testing.T) {
		heimdall := newHeimdallServer(t, http.StatusOK, []string{"superadmin"})
		r := newTestRouterWithHeimdall(verifier, heimdall.URL)
		if w := do(r, "/admin", tokenNoJWTAdmin); w.Code != http.StatusOK {
			t.Fatalf("/admin: esperado 200, obtido %d", w.Code)
		}
	})

	t.Run("Heimdall sem admin nega mesmo com go:admin no JWT", func(t *testing.T) {
		heimdall := newHeimdallServer(t, http.StatusOK, []string{"user"})
		r := newTestRouterWithHeimdall(verifier, heimdall.URL)
		adminJWT := sign(t, jwt.SigningMethodRS256, testRSAKid, keys.rsa, claimsFor(adminRole))
		if w := do(r, "/admin", adminJWT); w.Code != http.StatusForbidden {
			t.Fatalf("/admin: esperado 403 (JWT admin ignorado com Heimdall), obtido %d", w.Code)
		}
	})

	t.Run("Heimdall fora deixa USER (fail-safe)", func(t *testing.T) {
		heimdall := newHeimdallServer(t, http.StatusServiceUnavailable, nil)
		r := newTestRouterWithHeimdall(verifier, heimdall.URL)
		adminJWT := sign(t, jwt.SigningMethodRS256, testRSAKid, keys.rsa, claimsFor(adminRole))
		if w := do(r, "/admin", adminJWT); w.Code != http.StatusForbidden {
			t.Fatalf("/admin: esperado 403 com Heimdall fora, obtido %d", w.Code)
		}
	})

	t.Run("cidadão autenticado segue ok com Heimdall sem admin", func(t *testing.T) {
		heimdall := newHeimdallServer(t, http.StatusOK, []string{"carioca-rio"})
		r := newTestRouterWithHeimdall(verifier, heimdall.URL)
		if w := do(r, "/auth", tokenNoJWTAdmin); w.Code != http.StatusOK {
			t.Fatalf("/auth: esperado 200, obtido %d", w.Code)
		}
	})
}

func newHeimdallServer(t *testing.T, status int, roles []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/me" {
			http.NotFound(w, r)
			return
		}
		if auth := r.Header.Get("Authorization"); auth == "" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte(`{"error":"unavailable"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(heimdallUserInfo{
			ID:    1,
			CPF:   testCPF,
			Roles: roles,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}
