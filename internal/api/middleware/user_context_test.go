package middleware

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuer  = "https://auth.example.test/realms/teste"
	testCPF     = "22222222222"
	testRSAKid  = "rsa-1"
	testECKid   = "ec-1"
	adminRole   = "go:admin"
	regularRole = "user"
)

type testKeys struct {
	rsa *rsa.PrivateKey
	ec  *ecdsa.PrivateKey
}

func newTestKeys(t *testing.T) testKeys {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ec: %v", err)
	}
	return testKeys{rsa: rk, ec: ek}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func jwksJSON(t *testing.T, k testKeys) []byte {
	t.Helper()
	size := 32
	x := make([]byte, size)
	y := make([]byte, size)
	k.ec.X.FillBytes(x)
	k.ec.Y.FillBytes(y)
	doc := map[string]any{"keys": []map[string]string{
		{"kid": testRSAKid, "kty": "RSA", "use": "sig", "alg": "RS256", "n": b64(k.rsa.N.Bytes()), "e": b64(big.NewInt(int64(k.rsa.E)).Bytes())},
		{"kid": testECKid, "kty": "EC", "use": "sig", "crv": "P-256", "x": b64(x), "y": b64(y)},
		{"kid": "enc-1", "kty": "RSA", "use": "enc", "n": b64(k.rsa.N.Bytes()), "e": "AQAB"},
	}}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("jwks: %v", err)
	}
	return out
}

type jwksServer struct {
	*httptest.Server
	hits   atomic.Int32
	status atomic.Int32
}

func newJWKSServer(t *testing.T, k testKeys) *jwksServer {
	t.Helper()
	body := jwksJSON(t, k)
	s := &jwksServer{}
	s.status.Store(http.StatusOK)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.hits.Add(1)
		code := int(s.status.Load())
		w.WriteHeader(code)
		if code == http.StatusOK {
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func newVerifier(t *testing.T, jwksURL string) *JWKSVerifier {
	t.Helper()
	v, err := NewJWKSVerifier(testIssuer, jwksURL)
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	return v
}

func claimsFor(role string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":                testIssuer,
		"sub":                "00000000-0000-0000-0000-000000000001",
		"preferred_username": testCPF,
		"iat":                now.Unix(),
		"exp":                now.Add(5 * time.Minute).Unix(),
		"resource_access":    map[string]any{"superapp": map[string]any{"roles": []string{role}}},
	}
}

func sign(t *testing.T, method jwt.SigningMethod, kid string, key any, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

// forgeNone reproduz o token do relato: alg none, payload livre e assinatura qualquer.
func forgeNone(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT", "kid": testRSAKid})
	payload, _ := json.Marshal(claims)
	return b64(header) + "." + b64(payload) + ".assinatura-qualquer"
}

func newTestRouter(verifier TokenVerifier) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ExtractUserContext(verifier))
	r.GET("/auth", RequireAuth(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"cpf": GetUserCPF(c)})
	})
	r.GET("/admin", RequireAdmin(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	r.GET("/public", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"cpf": GetUserCPF(c), "admin": IsAdmin(c)})
	})
	return r
}

func do(r *gin.Engine, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("X-Auth-Request-Token", token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestExtractUserContext_RejectsForgedTokens(t *testing.T) {
	keys := newTestKeys(t)
	srv := newJWKSServer(t, keys)
	r := newTestRouter(newVerifier(t, srv.URL))

	attacker, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}

	expired := claimsFor(adminRole)
	expired["iat"] = time.Now().Add(-2 * time.Hour).Unix()
	expired["exp"] = time.Now().Add(-1 * time.Hour).Unix()

	wrongIssuer := claimsFor(adminRole)
	wrongIssuer["iss"] = "https://evil.example.test/realms/teste"

	noExp := claimsFor(adminRole)
	delete(noExp, "exp")

	cases := map[string]string{
		"alg none (relato)":           forgeNone(t, claimsFor(adminRole)),
		"alg none Bearer":             "Bearer " + forgeNone(t, claimsFor(adminRole)),
		"HS256 com segredo qualquer":  sign(t, jwt.SigningMethodHS256, testRSAKid, []byte("segredo-qualquer"), claimsFor(adminRole)),
		"RS256 com chave do atacante": sign(t, jwt.SigningMethodRS256, testRSAKid, attacker, claimsFor(adminRole)),
		"kid desconhecido":            sign(t, jwt.SigningMethodRS256, "outro-kid", attacker, claimsFor(adminRole)),
		"sem kid":                     sign(t, jwt.SigningMethodRS256, "", keys.rsa, claimsFor(adminRole)),
		"kid de chave de cifragem":    sign(t, jwt.SigningMethodRS256, "enc-1", keys.rsa, claimsFor(adminRole)),
		"expirado":                    sign(t, jwt.SigningMethodRS256, testRSAKid, keys.rsa, expired),
		"emissor errado":              sign(t, jwt.SigningMethodRS256, testRSAKid, keys.rsa, wrongIssuer),
		"sem exp":                     sign(t, jwt.SigningMethodRS256, testRSAKid, keys.rsa, noExp),
		"lixo":                        "a.b.c",
	}

	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if w := do(r, "/admin", token); w.Code != http.StatusUnauthorized {
				t.Fatalf("/admin: esperado 401, obtido %d", w.Code)
			}
			if w := do(r, "/auth", token); w.Code != http.StatusUnauthorized {
				t.Fatalf("/auth: esperado 401, obtido %d", w.Code)
			}
			w := do(r, "/public", token)
			if w.Code != http.StatusOK {
				t.Fatalf("/public: esperado 200, obtido %d", w.Code)
			}
			var body struct {
				CPF   string `json:"cpf"`
				Admin bool   `json:"admin"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("json: %v", err)
			}
			if body.CPF != "" || body.Admin {
				t.Fatalf("token inválido gerou identidade: %+v", body)
			}
		})
	}
}

func TestExtractUserContext_AcceptsSignedTokens(t *testing.T) {
	keys := newTestKeys(t)
	srv := newJWKSServer(t, keys)
	r := newTestRouter(newVerifier(t, srv.URL))

	user := sign(t, jwt.SigningMethodRS256, testRSAKid, keys.rsa, claimsFor(regularRole))
	w := do(r, "/auth", user)
	if w.Code != http.StatusOK {
		t.Fatalf("/auth usuário: esperado 200, obtido %d", w.Code)
	}
	var body struct {
		CPF string `json:"cpf"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.CPF != testCPF {
		t.Fatalf("CPF esperado no contexto, obtido %q (%v)", body.CPF, err)
	}
	if w := do(r, "/admin", user); w.Code != http.StatusForbidden {
		t.Fatalf("/admin usuário comum: esperado 403, obtido %d", w.Code)
	}

	admin := sign(t, jwt.SigningMethodRS256, testRSAKid, keys.rsa, claimsFor(adminRole))
	if w := do(r, "/admin", admin); w.Code != http.StatusOK {
		t.Fatalf("/admin admin RS256: esperado 200, obtido %d", w.Code)
	}

	adminEC := sign(t, jwt.SigningMethodES256, testECKid, keys.ec, claimsFor(adminRole))
	if w := do(r, "/admin", "Bearer "+adminEC); w.Code != http.StatusOK {
		t.Fatalf("/admin admin ES256: esperado 200, obtido %d", w.Code)
	}

	if hits := srv.hits.Load(); hits != 1 {
		t.Fatalf("JWKS deveria ser buscado uma vez e reutilizado do cache, buscado %d", hits)
	}
}

func TestExtractUserContext_NoHeader(t *testing.T) {
	keys := newTestKeys(t)
	srv := newJWKSServer(t, keys)
	r := newTestRouter(newVerifier(t, srv.URL))

	if w := do(r, "/admin", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("/admin sem token: esperado 401, obtido %d", w.Code)
	}
	if w := do(r, "/auth", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("/auth sem token: esperado 401, obtido %d", w.Code)
	}
	if w := do(r, "/public", ""); w.Code != http.StatusOK {
		t.Fatalf("/public sem token: esperado 200, obtido %d", w.Code)
	}
}

func TestExtractUserContext_FailsClosed(t *testing.T) {
	keys := newTestKeys(t)
	valid := sign(t, jwt.SigningMethodRS256, testRSAKid, keys.rsa, claimsFor(adminRole))

	t.Run("sem verificador configurado", func(t *testing.T) {
		r := newTestRouter(nil)
		if w := do(r, "/admin", valid); w.Code != http.StatusUnauthorized {
			t.Fatalf("esperado 401, obtido %d", w.Code)
		}
		if w := do(r, "/auth", valid); w.Code != http.StatusUnauthorized {
			t.Fatalf("esperado 401, obtido %d", w.Code)
		}
	})

	t.Run("JWKS indisponível", func(t *testing.T) {
		srv := newJWKSServer(t, keys)
		srv.status.Store(http.StatusInternalServerError)
		r := newTestRouter(newVerifier(t, srv.URL))
		if w := do(r, "/admin", valid); w.Code != http.StatusUnauthorized {
			t.Fatalf("esperado 401, obtido %d", w.Code)
		}
	})

	t.Run("configuração vazia", func(t *testing.T) {
		if _, err := NewJWKSVerifier("", ""); err == nil {
			t.Fatal("esperado erro sem emissor/JWKS")
		}
	})
}

func TestJWKSVerifier_UnknownKidDoesNotHammerJWKS(t *testing.T) {
	keys := newTestKeys(t)
	srv := newJWKSServer(t, keys)
	r := newTestRouter(newVerifier(t, srv.URL))

	for i := 0; i < 20; i++ {
		tok := sign(t, jwt.SigningMethodRS256, "kid-aleatorio", keys.rsa, claimsFor(adminRole))
		if w := do(r, "/admin", tok); w.Code != http.StatusUnauthorized {
			t.Fatalf("esperado 401, obtido %d", w.Code)
		}
	}
	if hits := srv.hits.Load(); hits != 1 {
		t.Fatalf("kids desconhecidos não devem refazer a busca do JWKS antes do intervalo mínimo; buscas: %d", hits)
	}
}

func TestJWKSVerifier_KeepsKnownKeyWhenJWKSFails(t *testing.T) {
	keys := newTestKeys(t)
	srv := newJWKSServer(t, keys)
	v := newVerifier(t, srv.URL)
	now := time.Now()
	v.now = func() time.Time { return now }
	r := newTestRouter(v)

	longLived := claimsFor(adminRole)
	longLived["exp"] = now.Add(time.Hour).Unix()
	valid := sign(t, jwt.SigningMethodRS256, testRSAKid, keys.rsa, longLived)
	if w := do(r, "/admin", valid); w.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d", w.Code)
	}

	srv.status.Store(http.StatusServiceUnavailable)
	now = now.Add(defaultJWKSRefreshTTL + time.Second)
	if w := do(r, "/admin", valid); w.Code != http.StatusOK {
		t.Fatalf("chave já conhecida deve continuar válida com o JWKS fora: obtido %d", w.Code)
	}
}
