package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	heimdallRequestTimeout        = 3 * time.Second
	maximumHeimdallResponseBytes  = 1 << 20 // 1 MiB
	heimdallUsersMePath           = "/api/v1/users/me"
)

// heimdallUserInfo espelha GET {HEIMDALL_BASE_URL}/api/v1/users/me.
type heimdallUserInfo struct {
	ID          int      `json:"id"`
	CPF         string   `json:"cpf"`
	DisplayName string   `json:"display_name"`
	Groups      []string `json:"groups"`
	Roles       []string `json:"roles"`
}

var heimdallHTTPClient = &http.Client{Timeout: heimdallRequestTimeout}

// fetchHeimdallRoles consulta o Heimdall pelas roles reais do usuário.
// authHeader é o valor de X-Auth-Request-Token (com ou sem prefixo Bearer).
func fetchHeimdallRoles(ctx context.Context, baseURL, authHeader string) ([]string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("heimdall: base URL vazia")
	}
	authHeader = strings.TrimSpace(authHeader)
	if authHeader == "" {
		return nil, fmt.Errorf("heimdall: token vazio")
	}
	if !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		authHeader = "Bearer " + authHeader
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+heimdallUsersMePath, nil)
	if err != nil {
		return nil, fmt.Errorf("heimdall: criar request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Accept", "application/json")

	resp, err := heimdallHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("heimdall: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maximumHeimdallResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("heimdall: ler resposta: %w", err)
	}
	if len(body) > maximumHeimdallResponseBytes {
		return nil, fmt.Errorf("heimdall: resposta maior que %d bytes", maximumHeimdallResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("heimdall: status %d", resp.StatusCode)
	}

	var info heimdallUserInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("heimdall: decode: %w", err)
	}
	if info.Roles == nil {
		return []string{}, nil
	}
	return info.Roles, nil
}

// isHeimdallAdminRole reconhece os papéis de admin que o Heimdall devolve
// (mesmo conjunto do app-go-api).
func isHeimdallAdminRole(role string) bool {
	switch role {
	case "go:admin", "admin", "superadmin":
		return true
	default:
		return false
	}
}

// isJWTAdminRole é o fallback local quando HEIMDALL_BASE_URL está vazio.
// Só aceita papéis no client superapp do JWT (não confiar em superadmin no token).
func isJWTAdminRole(role string) bool {
	return role == "go:admin" || role == "admin"
}
