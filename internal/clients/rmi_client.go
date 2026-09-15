package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const maximumRMIResponseBytes int64 = 1 << 20

// RMIClient consome a API do app-rmi para dados do cidadão.
type RMIClient struct {
	baseURL      string
	tokenManager *KeycloakTokenManager
	httpClient   *http.Client
}

func NewRMIClient(baseURL string, tokenManager *KeycloakTokenManager) *RMIClient {
	return &RMIClient{
		baseURL:      baseURL,
		tokenManager: tokenManager,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

// CitizenData representa os dados do cidadão retornados pelo app-rmi.
type CitizenData struct {
	CPF           string `json:"cpf"`
	DisplayName   string `json:"display_name"`
	BirthDate     string `json:"birth_date"`
	Escolaridade  string `json:"escolaridade"`
	RendaFamiliar string `json:"renda_familiar"`
	Deficiencia   string `json:"deficiencia"`
	Etnia         string `json:"etnia"`
	Genero        string `json:"genero"`
	Address       struct {
		Bairro string `json:"neighborhood"`
		Cidade string `json:"city"`
		Estado string `json:"state"`
		CEP    string `json:"cep"`
	} `json:"address"`
}

func isCanonicalCPF(cpf string) bool {
	if len(cpf) != 11 {
		return false
	}
	for _, character := range cpf {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// GetCitizen busca os dados de um cidadão pelo CPF.
// Usa service account token — requer permissão adequada no Keycloak.
func (c *RMIClient) GetCitizen(ctx context.Context, cpf string) (*CitizenData, error) {
	if !isCanonicalCPF(cpf) {
		return nil, fmt.Errorf("rmi: CPF inválido")
	}

	authHeader, err := c.tokenManager.BearerToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("rmi: falha ao obter token: %w", err)
	}

	requestURL, joinErr := url.JoinPath(c.baseURL, "v1", "citizen", cpf)
	if joinErr != nil {
		return nil, fmt.Errorf("rmi: URL base inválida: %w", joinErr)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("rmi: falha ao criar request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rmi: falha na requisição: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	body, err := readBoundedHTTPBody(resp.Body, maximumRMIResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("rmi: falha ao ler resposta: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rmi: retornou status %d", resp.StatusCode)
	}

	var citizen CitizenData
	if err := json.Unmarshal(body, &citizen); err != nil {
		return nil, fmt.Errorf("rmi: falha ao decodificar cidadão: %w", err)
	}

	return &citizen, nil
}
