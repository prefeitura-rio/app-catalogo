package clients

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

const (
	geminiEmbeddingModel = "gemini-embedding-001"
	embeddingDimensions  = 3072
	embeddingBatchSize   = 10 // máximo por chamada à API
)

// GeminiEmbeddingClient gera embeddings usando o modelo gemini-embedding-001.
type GeminiEmbeddingClient struct {
	client *genai.Client
}

// NewGeminiEmbeddingClient cria o cliente Gemini.
// Se apiKey for vazio, usa a variável de ambiente GOOGLE_API_KEY.
func NewGeminiEmbeddingClient(ctx context.Context, apiKey string) (*GeminiEmbeddingClient, error) {
	var cfg *genai.ClientConfig
	if apiKey != "" {
		cfg = &genai.ClientConfig{APIKey: apiKey}
	}
	client, err := genai.NewClient(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("gemini: falha ao criar cliente: %w", err)
	}
	return &GeminiEmbeddingClient{client: client}, nil
}

// EmbedDocuments gera embeddings para documentos (indexação).
// Usa task type RETRIEVAL_DOCUMENT para otimizar recuperação.
// Processa em batches de embeddingBatchSize para respeitar limites da API.
func (c *GeminiEmbeddingClient) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	all := make([][]float32, 0, len(texts))

	for i := 0; i < len(texts); i += embeddingBatchSize {
		end := min(i+embeddingBatchSize, len(texts))
		batch := texts[i:end]

		contents := make([]*genai.Content, len(batch))
		for j, t := range batch {
			contents[j] = genai.NewContentFromText(t, genai.RoleUser)
		}

		result, err := c.client.Models.EmbedContent(ctx, geminiEmbeddingModel, contents,
			&genai.EmbedContentConfig{
				TaskType: "RETRIEVAL_DOCUMENT",
			},
		)
		if err != nil {
			return nil, fmt.Errorf("gemini: embed batch %d: %w", i/embeddingBatchSize, err)
		}

		for _, emb := range result.Embeddings {
			all = append(all, emb.Values)
		}
	}

	return all, nil
}

// EmbedQuery gera embedding para uma query de busca.
// Usa task type RETRIEVAL_QUERY para maximizar similaridade com documentos indexados.
func (c *GeminiEmbeddingClient) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	contents := []*genai.Content{
		genai.NewContentFromText(query, genai.RoleUser),
	}

	result, err := c.client.Models.EmbedContent(ctx, geminiEmbeddingModel, contents,
		&genai.EmbedContentConfig{
			TaskType: "RETRIEVAL_QUERY",
		},
	)
	if err != nil {
		return nil, fmt.Errorf("gemini: embed query: %w", err)
	}
	if len(result.Embeddings) == 0 {
		return nil, fmt.Errorf("gemini: resposta sem embeddings")
	}
	return result.Embeddings[0].Values, nil
}

// VectorLiteral converte um slice de float32 no formato literal do pgvector: "[f1,f2,...,fn]".
func VectorLiteral(v []float32) string {
	if len(v) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%.8g", f)
	}
	b.WriteByte(']')
	return b.String()
}
