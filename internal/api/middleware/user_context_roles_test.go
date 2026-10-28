package middleware

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestNormalizeCPF(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"22222222222", "22222222222", true},
		{"222.222.222-22", "22222222222", true},
		{"  222.222.222-22  ", "22222222222", true},
		{"", "", false},
		{"2222222222", "", false},
		{"222222222222", "", false},
		{"2222222222a", "", false},
		{"222 222 222 22", "", false},
		{"usuario.servico", "", false},
		{"٢٢٢٢٢٢٢٢٢٢٢", "", false}, // dígitos não ASCII
	}
	for _, tc := range cases {
		got, ok := normalizeCPF(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("normalizeCPF(%q) = (%q, %v), esperado (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestExtractUserContext_CPFFromToken(t *testing.T) {
	keys := newTestKeys(t)
	srv := newJWKSServer(t, keys)
	r := newTestRouter(newVerifier(t, srv.URL))

	t.Run("CPF formatado é normalizado", func(t *testing.T) {
		c := claimsFor(regularRole)
		c["preferred_username"] = "222.222.222-22"
		w := do(r, "/auth", sign(t, jwt.SigningMethodRS256, testRSAKid, keys.rsa, c))
		if w.Code != http.StatusOK {
			t.Fatalf("esperado 200, obtido %d", w.Code)
		}
		var body struct {
			CPF string `json:"cpf"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.CPF != testCPF {
			t.Fatalf("CPF normalizado esperado, obtido %q (%v)", body.CPF, err)
		}
	})

	for name, username := range map[string]string{
		"CPF curto":        "2222222222",
		"CPF com letras":   "2222222222x",
		"username não CPF": "usuario.servico",
		"username vazio":   "",
	} {
		t.Run(name+" não autentica cidadão", func(t *testing.T) {
			c := claimsFor(regularRole)
			c["preferred_username"] = username
			token := sign(t, jwt.SigningMethodRS256, testRSAKid, keys.rsa, c)
			if w := do(r, "/auth", token); w.Code != http.StatusUnauthorized {
				t.Fatalf("/auth: esperado 401, obtido %d", w.Code)
			}
			if w := do(r, "/admin", token); w.Code != http.StatusForbidden {
				t.Fatalf("/admin usuário comum: esperado 403, obtido %d", w.Code)
			}
		})
	}
}

func TestExtractUserContext_AdminRoles(t *testing.T) {
	keys := newTestKeys(t)
	srv := newJWKSServer(t, keys)
	r := newTestRouter(newVerifier(t, srv.URL))

	withAccess := func(access map[string]any) string {
		c := claimsFor(regularRole)
		if access == nil {
			delete(c, "resource_access")
		} else {
			c["resource_access"] = access
		}
		return sign(t, jwt.SigningMethodRS256, testRSAKid, keys.rsa, c)
	}
	roles := func(client string, rs ...string) map[string]any {
		return map[string]any{client: map[string]any{"roles": rs}}
	}

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"go:admin no superapp", withAccess(roles("superapp", "go:admin")), http.StatusOK},
		{"admin no superapp", withAccess(roles("superapp", "admin")), http.StatusOK},
		{"admin entre outros papéis", withAccess(roles("superapp", "user", "admin")), http.StatusOK},
		{"papel comum", withAccess(roles("superapp", "user")), http.StatusForbidden},
		{"sem papéis", withAccess(roles("superapp")), http.StatusForbidden},
		{"sem resource_access", withAccess(nil), http.StatusForbidden},
		{"admin em outro client", withAccess(roles("outro-client", "admin", "go:admin")), http.StatusForbidden},
		{"papel parecido", withAccess(roles("superapp", "ADMIN", "go:admins", "superadmin")), http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w := do(r, "/admin", tc.token); w.Code != tc.want {
				t.Fatalf("/admin: esperado %d, obtido %d", tc.want, w.Code)
			}
			// O papel não muda a autenticação de cidadão: todos têm CPF válido.
			if w := do(r, "/auth", tc.token); w.Code != http.StatusOK {
				t.Fatalf("/auth: esperado 200, obtido %d", w.Code)
			}
		})
	}
}
