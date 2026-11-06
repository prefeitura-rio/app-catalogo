package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maximumRMIResponseBytes int64 = 1 << 20

var ErrRMINotConfigured = errors.New("rmi: URL base não configurada (RMI_BASE_URL)")

// RMIClient consome a API do app-rmi para dados do cidadão.
type RMIClient struct {
	baseURL      string
	tokenManager *KeycloakTokenManager
	httpClient   *http.Client
	resilience   *resilientCaller
}

func NewRMIClient(baseURL string, tokenManager *KeycloakTokenManager) *RMIClient {
	return &RMIClient{
		baseURL:      baseURL,
		tokenManager: tokenManager,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		resilience:   newResilientCaller("rmi", defaultRMIResilience),
	}
}

// Configured informa se há URL base para chamar o app-rmi.
func (c *RMIClient) Configured() bool {
	return strings.TrimSpace(c.baseURL) != ""
}

// CircuitState retorna o estado atual do circuit breaker do cliente.
func (c *RMIClient) CircuitState() CircuitState {
	return c.resilience.breaker.State()
}

// CitizenData são os dados do cidadão que a catalogo usa, extraídos da resposta
// do app-rmi (rmiCitizenResponse). Não é o formato de fio: veja toCitizenData.
type CitizenData struct {
	CPF           string
	DisplayName   string
	BirthDate     string // YYYY-MM-DD
	Escolaridade  string
	RendaFamiliar string
	Deficiencia   string
	Etnia         string
	Genero        string
	Address       CitizenAddress
}

// CitizenAddress é o endereço principal do cidadão.
type CitizenAddress struct {
	Bairro string
	Cidade string
	Estado string
	CEP    string
}

// rmiCitizenResponse espelha o contrato publicado de GET /v1/citizen/{cpf}
// (models.Citizen no docs/openapi-v3.json do app-rmi), só com os campos usados.
// Campos ausentes ou null viram string vazia.
type rmiCitizenResponse struct {
	CPF           string  `json:"cpf"`
	NomeExibicao  *string `json:"nome_exibicao"`
	Escolaridade  *string `json:"escolaridade"`
	RendaFamiliar *string `json:"renda_familiar"`
	Deficiencia   *string `json:"deficiencia"`
	Raca          *string `json:"raca"` // etnia autodeclarada (PUT /citizen/{cpf}/ethnicity)
	Genero        *string `json:"genero"`
	Nascimento    *struct {
		Data *string `json:"data"`
	} `json:"nascimento"`
	Endereco *struct {
		Principal *struct {
			Bairro    *string `json:"bairro"`
			Municipio *string `json:"municipio"`
			Estado    *string `json:"estado"`
			CEP       *string `json:"cep"`
		} `json:"principal"`
	} `json:"endereco"`
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// birthDateFromRMI reduz nascimento.data (timestamp RFC 3339) a YYYY-MM-DD.
// Valor que não começa por uma data válida vira string vazia.
func birthDateFromRMI(value *string) string {
	raw := stringValue(value)
	if len(raw) < len(time.DateOnly) {
		return ""
	}
	date := raw[:len(time.DateOnly)]
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return ""
	}
	return date
}

func (r *rmiCitizenResponse) toCitizenData() *CitizenData {
	citizen := &CitizenData{
		CPF:           r.CPF,
		DisplayName:   stringValue(r.NomeExibicao),
		Escolaridade:  stringValue(r.Escolaridade),
		RendaFamiliar: stringValue(r.RendaFamiliar),
		Deficiencia:   stringValue(r.Deficiencia),
		Etnia:         stringValue(r.Raca),
		Genero:        stringValue(r.Genero),
	}
	if r.Nascimento != nil {
		citizen.BirthDate = birthDateFromRMI(r.Nascimento.Data)
	}
	if r.Endereco != nil && r.Endereco.Principal != nil {
		principal := r.Endereco.Principal
		citizen.Address = CitizenAddress{
			Bairro: stringValue(principal.Bairro),
			Cidade: stringValue(principal.Municipio),
			Estado: stringValue(principal.Estado),
			CEP:    stringValue(principal.CEP),
		}
	}
	return citizen
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
	if !c.Configured() {
		return nil, ErrRMINotConfigured
	}

	authHeader, err := c.tokenManager.BearerToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("rmi: falha ao obter token: %w", err)
	}

	requestURL, joinErr := url.JoinPath(c.baseURL, "v1", "citizen", cpf)
	if joinErr != nil {
		return nil, fmt.Errorf("rmi: URL base inválida: %w", joinErr)
	}

	var citizen *CitizenData
	err = c.resilience.do(ctx, func(attemptCtx context.Context) error {
		result, fetchErr := c.fetchCitizen(attemptCtx, requestURL, authHeader)
		if fetchErr != nil {
			return fetchErr
		}
		citizen = result
		return nil
	})
	if err != nil {
		return nil, err
	}
	return citizen, nil
}

// fetchCitizen faz uma tentativa de GET no RMI. Falhas que valem repetir
// (transporte, timeout, 5xx, 429) voltam marcadas como transitórias.
func (c *RMIClient) fetchCitizen(ctx context.Context, requestURL, authHeader string) (*CitizenData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("rmi: falha ao criar request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// *url.Error traz a URL, que contém o CPF: propaga só a causa.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, markTransient(fmt.Errorf("rmi: falha na requisição: %w", err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	body, err := readBoundedHTTPBody(resp.Body, maximumRMIResponseBytes)
	if err != nil {
		readErr := fmt.Errorf("rmi: falha ao ler resposta: %w", err)
		if errors.Is(err, errHTTPBodyTooLarge) {
			return nil, readErr
		}
		return nil, markTransient(readErr)
	}
	if resp.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("rmi: retornou status %d", resp.StatusCode)
		if isTransientStatus(resp.StatusCode) {
			return nil, markTransient(statusErr)
		}
		return nil, statusErr
	}

	var citizen rmiCitizenResponse
	if err := json.Unmarshal(body, &citizen); err != nil {
		return nil, fmt.Errorf("rmi: falha ao decodificar cidadão: %w", err)
	}

	return citizen.toCitizenData(), nil
}
