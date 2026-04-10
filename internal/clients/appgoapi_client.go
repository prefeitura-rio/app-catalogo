package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AppGoAPIClient consome a API pública do app-go-api.
type AppGoAPIClient struct {
	baseURL      string
	tokenManager *KeycloakTokenManager
	httpClient   *http.Client
}

func NewAppGoAPIClient(baseURL string, tokenManager *KeycloakTokenManager) *AppGoAPIClient {
	return &AppGoAPIClient{
		baseURL:      baseURL,
		tokenManager: tokenManager,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// flexString aceita string ou número JSON.
// Números são convertidos para sua representação em string (ex: 81 → "81").
// Outros tipos (objeto, null) resultam em string vazia.
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	// Tenta string primeiro
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*s = flexString(str)
		return nil
	}
	// Tenta número e converte para string (IDs inteiros da API)
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*s = flexString(n.String())
		return nil
	}
	*s = ""
	return nil
}

// Course representa um curso do app-go-api.
// Estrutura real: GET /api/public/courses → {"data": {"courses": [...], "pagination": {...}}}
type Course struct {
	ID          flexString `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Organization string    `json:"organization"`
	Modalidade  string     `json:"modalidade"`
	Turno       string     `json:"turno"`
	Theme       string     `json:"theme"`
	URL         string     `json:"link_inscricao"`
	ImageURL    string     `json:"cover_image"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type coursesPageResponse struct {
	Data struct {
		Courses    []Course `json:"courses"`
		Pagination struct {
			Total int `json:"total"`
			Page  int `json:"page"`
		} `json:"pagination"`
	} `json:"data"`
}

// Job representa uma vaga de emprego do app-go-api.
// Estrutura: GET /api/public/empregabilidade/vagas → {"data": [...], "total": N, ...}
type Job struct {
	ID                string     `json:"id"`
	Title             string     `json:"title"`
	Description       string     `json:"description"`
	Company           string     `json:"company"`
	Bairro            flexString `json:"bairro"`
	RegimeContratacao flexString `json:"regime_contratacao"`
	ModeloTrabalho    flexString `json:"modelo_trabalho"`
	FaixaSalarial     flexString `json:"faixa_salarial"`
	PCD               bool       `json:"pcd"`
	URL               string     `json:"url"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// MEIOpportunity representa uma oportunidade MEI.
// Estrutura real: GET /api/public/oportunidades-mei → {"data": [...], "meta": {"total": N, ...}}
type MEIOpportunity struct {
	ID          flexString `json:"id"`
	Title       string     `json:"titulo"`           // campo real é "titulo"
	Description string     `json:"descricao_servico"` // campo real é "descricao_servico"
	Organization string    `json:"orgao_id"`
	Segmento    string     `json:"outras_informacoes"`
	ImageURL    string     `json:"cover_image"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type meiPageResponse struct {
	Data []MEIOpportunity `json:"data"`
	Meta struct {
		Total    int `json:"total"`
		Page     int `json:"page"`
		PageSize int `json:"page_size"`
	} `json:"meta"`
}

type paginatedResponse[T any] struct {
	Data    []T `json:"data"`
	Total   int `json:"total"`
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
}

func (c *AppGoAPIClient) doGet(ctx context.Context, path string, dest interface{}) error {
	authHeader, err := c.tokenManager.BearerToken(ctx)
	if err != nil {
		return fmt.Errorf("appgoapi: falha ao obter token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("appgoapi: falha ao criar request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("appgoapi: falha na requisição: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("appgoapi: retornou %d: %s", resp.StatusCode, string(body))
	}

	return json.Unmarshal(body, dest)
}

// GetCourses retorna cursos paginados.
func (c *AppGoAPIClient) GetCourses(ctx context.Context, page int, updatedSince time.Time) ([]Course, int, error) {
	path := fmt.Sprintf("/api/public/courses?page=%d&per_page=100", page)
	if !updatedSince.IsZero() {
		path += "&updated_since=" + updatedSince.UTC().Format(time.RFC3339)
	}

	var resp coursesPageResponse
	if err := c.doGet(ctx, path, &resp); err != nil {
		return nil, 0, err
	}
	return resp.Data.Courses, resp.Data.Pagination.Total, nil
}

// GetJobs retorna vagas de emprego paginadas.
func (c *AppGoAPIClient) GetJobs(ctx context.Context, page int, updatedSince time.Time) ([]Job, int, error) {
	path := fmt.Sprintf("/api/public/empregabilidade/vagas?page=%d&per_page=100", page)
	if !updatedSince.IsZero() {
		path += "&updated_since=" + updatedSince.UTC().Format(time.RFC3339)
	}

	var resp paginatedResponse[Job]
	if err := c.doGet(ctx, path, &resp); err != nil {
		return nil, 0, err
	}
	return resp.Data, resp.Total, nil
}

// GetMEIOpportunities retorna oportunidades MEI paginadas.
func (c *AppGoAPIClient) GetMEIOpportunities(ctx context.Context, page int, updatedSince time.Time) ([]MEIOpportunity, int, error) {
	path := fmt.Sprintf("/api/public/oportunidades-mei?page=%d&per_page=100", page)
	if !updatedSince.IsZero() {
		path += "&updated_since=" + updatedSince.UTC().Format(time.RFC3339)
	}

	var resp meiPageResponse
	if err := c.doGet(ctx, path, &resp); err != nil {
		return nil, 0, err
	}
	return resp.Data, resp.Meta.Total, nil
}
