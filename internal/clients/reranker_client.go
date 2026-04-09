package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// RerankerClient chama um sidecar de cross-encoder para reranking de resultados.
// É opcional — se não configurado, o chamador usa o score RRF diretamente.
//
// Protocolo esperado do sidecar:
//
//	POST /rerank
//	Body: {"query": "...", "documents": [{"id": "uuid", "text": "title. short_desc"}]}
//	→    [{"id": "uuid", "score": 0.95}, ...]  (ordenado por score decrescente)
//
// Protocolo esperado (compatível com sidecar Python cross-encoder):
type RerankerClient struct {
	baseURL    string
	httpClient *http.Client
}

type rerankerRequest struct {
	Query     string              `json:"query"`
	Documents []rerankerDocument  `json:"documents"`
}

type rerankerDocument struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type RerankerResult struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
}

func NewRerankerClient(baseURL string, timeout time.Duration) *RerankerClient {
	return &RerankerClient{
		baseURL: baseURL,
		httpClient: &http.Client{Timeout: timeout},
	}
}

// Rerank envia query + documentos ao sidecar e retorna os resultados ordenados por score.
// A função retorna nil, nil se o sidecar estiver indisponível (erro de rede),
// permitindo fallback gracioso no chamador.
func (c *RerankerClient) Rerank(ctx context.Context, query string, docs []RerankerDocument) ([]RerankerResult, error) {
	reqDocs := make([]rerankerDocument, len(docs))
	for i, d := range docs {
		reqDocs[i] = rerankerDocument{ID: d.ID, Text: d.Text}
	}

	body, err := json.Marshal(rerankerRequest{Query: query, Documents: reqDocs})
	if err != nil {
		return nil, fmt.Errorf("reranker: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("reranker: criar requisição: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil // indisponível — fallback gracioso
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reranker: status %d", resp.StatusCode)
	}

	var results []RerankerResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("reranker: decode: %w", err)
	}
	return results, nil
}

// RerankerDocument é o payload de entrada do reranker.
type RerankerDocument struct {
	ID   string
	Text string
}
