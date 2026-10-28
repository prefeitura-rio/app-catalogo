package middleware

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	maximumJWKSResponseBytes int64 = 256 << 10
	defaultJWKSRefreshTTL          = 10 * time.Minute
	defaultJWKSMinRefresh          = 1 * time.Minute
	defaultJWKSFetchTimeout        = 5 * time.Second
	defaultJWTLeeway               = 30 * time.Second
)

// allowedJWTMethods lista os métodos de assinatura assimétricos aceitos. `none` e HMAC
// (HS*) ficam de fora: o serviço não tem segredo compartilhado com o emissor.
var allowedJWTMethods = []string{
	"RS256", "RS384", "RS512",
	"PS256", "PS384", "PS512",
	"ES256", "ES384", "ES512",
}

var (
	ErrTokenVerifierNotConfigured = errors.New("verificação de JWT não configurada")
	ErrJWKSKeyNotFound            = errors.New("chave do JWT não encontrada no JWKS")
	ErrUnauthorizedParty          = errors.New("azp do JWT não está entre os clients autorizados")
)

// TokenVerifier confere a assinatura e as claims de registro de um JWT e
// devolve as claims somente quando o token é válido.
type TokenVerifier interface {
	Verify(ctx context.Context, rawToken string) (*jwtClaims, error)
}

// JWKSVerifier valida JWTs emitidos pelo Keycloak usando as chaves públicas
// publicadas no endpoint JWKS do realm, com cache em memória.
type JWKSVerifier struct {
	issuer            string
	jwksURL           string
	authorizedParties map[string]struct{}
	httpClient        *http.Client
	refreshTTL        time.Duration
	minRefresh        time.Duration
	leeway            time.Duration
	now               func() time.Time

	mu        sync.RWMutex
	keys      map[string]crypto.PublicKey
	fetchedAt time.Time
	lastTry   time.Time
	refreshMu sync.Mutex
}

// NewJWKSVerifier cria o verificador. issuer, jwksURL e ao menos um client
// autorizado (claim azp) são obrigatórios; faltando algum, nenhum token é aceito.
func NewJWKSVerifier(issuer, jwksURL string, authorizedParties []string) (*JWKSVerifier, error) {
	issuer = strings.TrimSpace(issuer)
	jwksURL = strings.TrimSpace(jwksURL)
	parties := make(map[string]struct{}, len(authorizedParties))
	for _, p := range authorizedParties {
		if p = strings.TrimSpace(p); p != "" {
			parties[p] = struct{}{}
		}
	}
	if issuer == "" || jwksURL == "" || len(parties) == 0 {
		return nil, ErrTokenVerifierNotConfigured
	}
	return &JWKSVerifier{
		issuer:            issuer,
		jwksURL:           jwksURL,
		authorizedParties: parties,
		httpClient:        &http.Client{Timeout: defaultJWKSFetchTimeout},
		refreshTTL:        defaultJWKSRefreshTTL,
		minRefresh:        defaultJWKSMinRefresh,
		leeway:            defaultJWTLeeway,
		now:               time.Now,
		keys:              map[string]crypto.PublicKey{},
	}, nil
}

// Verify confere método, assinatura, emissor, exp, iat e azp do token.
func (v *JWKSVerifier) Verify(ctx context.Context, rawToken string) (*jwtClaims, error) {
	rawToken = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rawToken), "Bearer "))
	if rawToken == "" {
		return nil, errors.New("token vazio")
	}

	parser := jwt.NewParser(
		jwt.WithValidMethods(allowedJWTMethods),
		jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(v.leeway),
		jwt.WithTimeFunc(v.now),
	)

	claims := &jwtClaims{}
	_, err := parser.ParseWithClaims(rawToken, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return v.key(ctx, kid)
	})
	if err != nil {
		return nil, err
	}
	// O azp identifica o client do realm para o qual o token foi emitido; sem
	// essa conferência qualquer token válido do realm autenticaria aqui.
	if _, ok := v.authorizedParties[claims.AuthorizedParty]; !ok {
		return nil, ErrUnauthorizedParty
	}
	return claims, nil
}

func (v *JWKSVerifier) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	if kid == "" {
		return nil, errors.New("JWT sem kid")
	}

	v.mu.RLock()
	k, ok := v.keys[kid]
	fresh := !v.fetchedAt.IsZero() && v.now().Sub(v.fetchedAt) < v.refreshTTL
	v.mu.RUnlock()
	if ok && fresh {
		return k, nil
	}

	if err := v.refresh(ctx); err != nil {
		// Com o JWKS indisponível, uma chave já conhecida continua válida.
		if ok {
			return k, nil
		}
		return nil, err
	}

	v.mu.RLock()
	defer v.mu.RUnlock()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, ErrJWKSKeyNotFound
}

// refresh busca o JWKS, limitado a uma tentativa por minRefresh para que kids
// desconhecidos não virem um ataque de amplificação contra o Keycloak.
func (v *JWKSVerifier) refresh(ctx context.Context) error {
	v.refreshMu.Lock()
	defer v.refreshMu.Unlock()

	v.mu.RLock()
	lastTry := v.lastTry
	fetchedAt := v.fetchedAt
	v.mu.RUnlock()

	now := v.now()
	if !lastTry.IsZero() && now.Sub(lastTry) < v.minRefresh {
		if fetchedAt.IsZero() || !fetchedAt.Equal(lastTry) {
			return errors.New("JWKS indisponível (aguardando nova tentativa)")
		}
		return nil
	}

	v.mu.Lock()
	v.lastTry = now
	v.mu.Unlock()

	keys, err := v.fetch(ctx)
	if err != nil {
		return err
	}

	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = now
	v.mu.Unlock()
	return nil
}

type jwksDocument struct {
	Keys []jwkKey `json:"keys"`
}

type jwkKey struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Use string `json:"use"`
	Crv string `json:"crv"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (v *JWKSVerifier) fetch(ctx context.Context) (map[string]crypto.PublicKey, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultJWKSFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return nil, fmt.Errorf("jwks: falha ao criar request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwks: falha na requisição: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: retornou status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maximumJWKSResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("jwks: falha ao ler resposta: %w", err)
	}
	if int64(len(body)) > maximumJWKSResponseBytes {
		return nil, errors.New("jwks: resposta excede o limite")
	}

	var doc jwksDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("jwks: falha ao decodificar: %w", err)
	}

	keys := make(map[string]crypto.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kid == "" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		pub, err := k.publicKey()
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("jwks: nenhuma chave de assinatura utilizável")
	}
	return keys, nil
}

func (k jwkKey) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err := decodeB64URLInt(k.N)
		if err != nil {
			return nil, err
		}
		e, err := decodeB64URLInt(k.E)
		if err != nil {
			return nil, err
		}
		if !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
			return nil, errors.New("jwk: expoente RSA inválido")
		}
		if n.BitLen() < 2048 {
			return nil, errors.New("jwk: chave RSA menor que 2048 bits")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("jwk: curva %q não suportada", k.Crv)
		}
		size := (curve.Params().BitSize + 7) / 8
		x, err := decodeB64URLInt(k.X)
		if err != nil {
			return nil, err
		}
		y, err := decodeB64URLInt(k.Y)
		if err != nil {
			return nil, err
		}
		if len(x.Bytes()) > size || len(y.Bytes()) > size {
			return nil, errors.New("jwk: coordenada EC inválida")
		}
		point := make([]byte, 1+2*size)
		point[0] = 4
		x.FillBytes(point[1 : 1+size])
		y.FillBytes(point[1+size:])
		// ParseUncompressedPublicKey rejeita pontos fora da curva.
		return ecdsa.ParseUncompressedPublicKey(curve, point)
	default:
		return nil, fmt.Errorf("jwk: kty %q não suportado", k.Kty)
	}
}

func decodeB64URLInt(s string) (*big.Int, error) {
	if s == "" {
		return nil, errors.New("jwk: campo vazio")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return nil, fmt.Errorf("jwk: base64 inválido: %w", err)
	}
	return new(big.Int).SetBytes(b), nil
}
