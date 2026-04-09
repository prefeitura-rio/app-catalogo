package clients

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// TypesenseService representa um documento da coleção prefrio_services_base.
type TypesenseService struct {
	ID                    string          `json:"id"`
	NomeServico           string          `json:"nome_servico"`
	OrgaoGestor           []string        `json:"orgao_gestor"`
	Resumo                string          `json:"resumo"`
	TempoAtendimento      string          `json:"tempo_atendimento"`
	CustoServico          string          `json:"custo_servico"`
	ResultadoSolicitacao  string          `json:"resultado_solicitacao"`
	DescricaoCompleta     string          `json:"descricao_completa"`
	Autor                 string          `json:"autor"`
	DocumentosNecessarios []string        `json:"documentos_necessarios"`
	InstrucoesSolicitante string          `json:"instrucoes_solicitante"`
	CanaisDigitais        []string        `json:"canais_digitais"`
	CanaisPresenciais     []string        `json:"canais_presenciais"`
	ServicoNaoCobre       string          `json:"servico_nao_cobre"`
	LegislacaoRelacionada []string        `json:"legislacao_relacionada"`
	TemaGeral             string          `json:"tema_geral"`
	SubCategoria          string          `json:"sub_categoria"`
	PublicoEspecifico     []string        `json:"publico_especifico"`
	FixarDestaque         bool            `json:"fixar_destaque"`
	AwaitingApproval      bool            `json:"awaiting_approval"`
	PublishedAt           *int64          `json:"published_at"`
	IsFree                *bool           `json:"is_free"`
	Agents                json.RawMessage `json:"agents"`
	ExtraFields           json.RawMessage `json:"extra_fields"`
	Status                int32           `json:"status"`
	CreatedAt             int64           `json:"created_at"`
	LastUpdate            int64           `json:"last_update"`
	SearchContent         string          `json:"search_content"`
	Buttons               json.RawMessage `json:"buttons"`
	Slug                  string          `json:"slug"`
	SlugHistory           []string        `json:"slug_history"`
}

// TypesenseClient é um cliente HTTP para a API do Typesense.
type TypesenseClient struct {
	baseURL    string
	apiKey     string
	collection string
	httpClient *http.Client
}

func NewTypesenseClient(baseURL, apiKey, collection string) *TypesenseClient {
	return &TypesenseClient{
		baseURL:    baseURL,
		apiKey:     apiKey,
		collection: collection,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

// ExportSince exporta documentos via endpoint JSONL do Typesense.
// Filtra apenas documentos publicados (awaiting_approval=false, status>=1).
// Se since não for zero, inclui filtro de delta por last_update.
// fn é chamada para cada documento — retornar erro interrompe o export.
func (c *TypesenseClient) ExportSince(ctx context.Context, since time.Time, fn func(TypesenseService) error) error {
	filter := "awaiting_approval:=false && status:>=1"
	if !since.IsZero() {
		filter += fmt.Sprintf(" && last_update:>%d", since.Unix())
	}

	u := fmt.Sprintf("%s/collections/%s/documents/export?filter_by=%s",
		c.baseURL, c.collection, url.QueryEscape(filter))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("typesense: erro ao criar requisição: %w", err)
	}
	req.Header.Set("X-TYPESENSE-API-KEY", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("typesense: erro na requisição: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("typesense: status inesperado %d para coleção %s", resp.StatusCode, c.collection)
	}

	// O export retorna JSONL — um documento JSON por linha.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // buffer de 1MB por linha

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var svc TypesenseService
		if err := json.Unmarshal(line, &svc); err != nil {
			// Linha malformada: ignora e continua
			continue
		}
		if err := fn(svc); err != nil {
			return err
		}
	}
	return scanner.Err()
}
