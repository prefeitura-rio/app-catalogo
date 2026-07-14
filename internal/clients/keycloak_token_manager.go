package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	maximumKeycloakTokenResponseBytes int64 = 64 << 10
	maximumServiceCredentialBytes           = 16 << 10
	defaultKeycloakTokenLifetime            = 5 * time.Minute
	maximumKeycloakTokenLifetime            = 24 * time.Hour
	keycloakTokenRefreshLead                = 30 * time.Second
)

// KeycloakTokenManager obtains and renews service-account client_credentials tokens.
type KeycloakTokenManager struct {
	tokenURL     string
	clientID     string
	clientSecret string
	httpClient   *http.Client
	now          func() time.Time

	mutex        sync.RWMutex
	refreshMutex sync.Mutex
	token        string
	expiresAt    time.Time
}

func NewKeycloakTokenManager(
	keycloakURL string,
	realm string,
	clientID string,
	clientSecret string,
) (*KeycloakTokenManager, error) {
	parsedBaseURL, baseURLError := validateServiceBaseURL(keycloakURL, true, true)
	if baseURLError != nil {
		return nil, fmt.Errorf("keycloak: invalid base URL: %w", baseURLError)
	}
	canonicalRealm := strings.TrimSpace(realm)
	if canonicalRealm == "" || canonicalRealm == "." || canonicalRealm == ".." ||
		url.PathEscape(canonicalRealm) != canonicalRealm {
		return nil, errors.New("keycloak: realm must be one safe URL path segment")
	}
	if credentialError := validateServiceCredential(clientID, maximumServiceCredentialBytes); credentialError != nil {
		return nil, errors.New("keycloak: client ID is missing or invalid")
	}
	if credentialError := validateServiceCredential(clientSecret, maximumServiceCredentialBytes); credentialError != nil {
		return nil, errors.New("keycloak: client secret is missing or invalid")
	}
	tokenURL, joinError := url.JoinPath(
		parsedBaseURL.String(),
		"realms",
		canonicalRealm,
		"protocol",
		"openid-connect",
		"token",
	)
	if joinError != nil {
		return nil, errors.New("keycloak: build token URL")
	}
	return &KeycloakTokenManager{
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   noRedirectHTTPClient(10 * time.Second),
		now:          time.Now,
	}, nil
}

type keycloakTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

func (manager *KeycloakTokenManager) fetchToken(ctx context.Context) error {
	formValues := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {manager.clientID},
		"client_secret": {manager.clientSecret},
	}
	request, requestError := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		manager.tokenURL,
		strings.NewReader(formValues.Encode()),
	)
	if requestError != nil {
		return fmt.Errorf("keycloak: create token request: %w", requestError)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, requestError := manager.httpClient.Do(request)
	if requestError != nil {
		return fmt.Errorf("keycloak: token request failed: %w", requestError)
	}
	defer response.Body.Close()
	encodedBody, readError := readBoundedHTTPBody(response.Body, maximumKeycloakTokenResponseBytes)
	if readError != nil {
		return fmt.Errorf("keycloak: invalid token response size: %w", readError)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("keycloak: token endpoint returned status %d", response.StatusCode)
	}

	var tokenResponse keycloakTokenResponse
	if decodeError := json.Unmarshal(encodedBody, &tokenResponse); decodeError != nil {
		return errors.New("keycloak: token endpoint returned invalid JSON")
	}
	if credentialError := validateServiceCredential(tokenResponse.AccessToken, maximumServiceCredentialBytes); credentialError != nil {
		return errors.New("keycloak: token endpoint returned an invalid access token")
	}
	if tokenResponse.ExpiresIn <= 0 {
		tokenResponse.ExpiresIn = int(defaultKeycloakTokenLifetime / time.Second)
	}
	if tokenResponse.ExpiresIn > int(maximumKeycloakTokenLifetime/time.Second) {
		return errors.New("keycloak: token endpoint returned an invalid lifetime")
	}
	tokenLifetime := time.Duration(tokenResponse.ExpiresIn) * time.Second
	refreshLead := min(keycloakTokenRefreshLead, tokenLifetime/2)

	manager.mutex.Lock()
	manager.token = tokenResponse.AccessToken
	manager.expiresAt = manager.now().Add(tokenLifetime - refreshLead)
	manager.mutex.Unlock()
	return nil
}

// GetToken returns a valid token, collapsing concurrent refreshes.
func (manager *KeycloakTokenManager) GetToken(ctx context.Context) (string, error) {
	if token, valid := manager.cachedToken(); valid {
		return token, nil
	}
	manager.refreshMutex.Lock()
	defer manager.refreshMutex.Unlock()
	if token, valid := manager.cachedToken(); valid {
		return token, nil
	}
	if fetchError := manager.fetchToken(ctx); fetchError != nil {
		return "", fetchError
	}
	token, valid := manager.cachedToken()
	if !valid {
		return "", errors.New("keycloak: refreshed token is not usable")
	}
	return token, nil
}

func (manager *KeycloakTokenManager) cachedToken() (string, bool) {
	manager.mutex.RLock()
	defer manager.mutex.RUnlock()
	return manager.token, manager.token != "" && manager.now().Before(manager.expiresAt)
}

// BearerToken returns the Authorization header value without exposing it in errors.
func (manager *KeycloakTokenManager) BearerToken(ctx context.Context) (string, error) {
	token, tokenError := manager.GetToken(ctx)
	if tokenError != nil {
		return "", tokenError
	}
	return "Bearer " + token, nil
}
