package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	maximumRerankerRequestBytes  = 512 << 10
	maximumRerankerResponseBytes = 1 << 20
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
	rerankURL  string
	httpClient *http.Client
}

type rerankerRequest struct {
	Query     string             `json:"query"`
	Documents []rerankerDocument `json:"documents"`
}

type rerankerDocument struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type RerankerResult struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
}

func NewRerankerClient(baseURL string, timeout time.Duration) (*RerankerClient, error) {
	parsedBaseURL, baseURLError := validateServiceBaseURL(baseURL, true, true)
	if baseURLError != nil {
		return nil, fmt.Errorf("reranker: invalid base URL: %w", baseURLError)
	}
	if timeout <= 0 {
		return nil, errors.New("reranker: timeout must be positive")
	}
	rerankURL, joinError := url.JoinPath(parsedBaseURL.String(), "rerank")
	if joinError != nil {
		return nil, errors.New("reranker: build endpoint URL")
	}
	return &RerankerClient{
		rerankURL: rerankURL,
		httpClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Rerank envia query + documentos ao sidecar e retorna os resultados ordenados por score.
// Transport and contract failures are returned so the caller can observe them
// while preserving the fused order as a graceful fallback.
func (c *RerankerClient) Rerank(ctx context.Context, query string, docs []RerankerDocument) ([]RerankerResult, error) {
	reqDocs := make([]rerankerDocument, len(docs))
	for i, d := range docs {
		reqDocs[i] = rerankerDocument{ID: d.ID, Text: d.Text}
	}

	body, err := json.Marshal(rerankerRequest{Query: query, Documents: reqDocs})
	if err != nil {
		return nil, fmt.Errorf("reranker: marshal: %w", err)
	}
	if len(body) > maximumRerankerRequestBytes {
		return nil, errors.New("reranker: request exceeds size limit")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rerankURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("reranker: criar requisição: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reranker: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reranker: status %d", resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, maximumRerankerResponseBytes+1)
	responseBody, readError := io.ReadAll(limitedBody)
	if readError != nil {
		return nil, fmt.Errorf("reranker: read response: %w", readError)
	}
	if len(responseBody) > maximumRerankerResponseBytes {
		return nil, fmt.Errorf("reranker: response exceeds size limit")
	}
	responseDecoder := json.NewDecoder(bytes.NewReader(responseBody))
	var results []RerankerResult
	if err := responseDecoder.Decode(&results); err != nil {
		return nil, fmt.Errorf("reranker: decode: %w", err)
	}
	var trailingJSON any
	if trailingError := responseDecoder.Decode(&trailingJSON); !errors.Is(trailingError, io.EOF) {
		return nil, fmt.Errorf("reranker: trailing JSON")
	}
	return results, nil
}

// RerankerDocument é o payload de entrada do reranker.
type RerankerDocument struct {
	ID   string
	Text string
}
