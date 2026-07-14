package clients

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maximumTypesenseExportLineBytes = 1 << 20

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
// Exporta todos os status para que despublicações também atualizem o catálogo.
// Se since não for zero, inclui filtro de delta inclusivo por last_update.
// O limite inclusivo reprocessa empates no timestamp de precisão em segundos e
// evita perder documentos que foram atualizados no mesmo instante do cursor.
// fn é chamada para cada documento — retornar erro interrompe o export.
func (c *TypesenseClient) ExportSince(ctx context.Context, since time.Time, fn func(TypesenseService) error) error {
	exportURL := fmt.Sprintf(
		"%s/collections/%s/documents/export",
		strings.TrimRight(c.baseURL, "/"),
		url.PathEscape(c.collection),
	)
	if !since.IsZero() {
		filter := fmt.Sprintf("last_update:>=%d", since.Unix())
		exportURL += "?filter_by=" + url.QueryEscape(filter)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, exportURL, nil)
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
	scanner.Buffer(make([]byte, 64<<10), maximumTypesenseExportLineBytes)

	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var svc TypesenseService
		if err := json.Unmarshal(line, &svc); err != nil {
			return fmt.Errorf("typesense: linha JSONL %d inválida: %w", lineNumber, err)
		}
		if err := fn(svc); err != nil {
			return fmt.Errorf("typesense: processar linha JSONL %d: %w", lineNumber, err)
		}
	}
	if scanError := scanner.Err(); scanError != nil {
		return fmt.Errorf("typesense: ler export JSONL: %w", scanError)
	}
	return nil
}
