package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// KeycloakTokenManager obtém e renova tokens de service account via client_credentials.
type KeycloakTokenManager struct {
	keycloakURL  string
	realm        string
	clientID     string
	clientSecret string
	httpClient   *http.Client

	mu        sync.RWMutex
	token     string
	expiresAt time.Time
}

func NewKeycloakTokenManager(keycloakURL, realm, clientID, clientSecret string) *KeycloakTokenManager {
	return &KeycloakTokenManager{
		keycloakURL:  keycloakURL,
		realm:        realm,
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

type kcTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

func (m *KeycloakTokenManager) fetchToken(ctx context.Context) error {
	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", m.keycloakURL, m.realm)

	data := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {m.clientID},
		"client_secret": {m.clientSecret},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("keycloak: falha ao criar request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: falha na requisição: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("keycloak: retornou %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp kcTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return fmt.Errorf("keycloak: falha ao decodificar token: %w", err)
	}

	expiry := tokenResp.ExpiresIn
	if expiry <= 0 {
		expiry = 300
	}

	m.mu.Lock()
	m.token = tokenResp.AccessToken
	m.expiresAt = time.Now().Add(time.Duration(expiry-30) * time.Second)
	m.mu.Unlock()

	return nil
}

// GetToken retorna um token válido, renovando se necessário.
func (m *KeycloakTokenManager) GetToken(ctx context.Context) (string, error) {
	m.mu.RLock()
	token := m.token
	valid := time.Now().Before(m.expiresAt)
	m.mu.RUnlock()

	if token != "" && valid {
		return token, nil
	}

	if err := m.fetchToken(ctx); err != nil {
		return "", err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.token, nil
}

// BearerToken retorna "Bearer <token>".
func (m *KeycloakTokenManager) BearerToken(ctx context.Context) (string, error) {
	token, err := m.GetToken(ctx)
	if err != nil {
		return "", err
	}
	return "Bearer " + token, nil
}
