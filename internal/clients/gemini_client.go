package clients

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

const (
	geminiEmbeddingModel = "gemini-embedding-001"
	embeddingBatchSize   = 10 // máximo por chamada à API
)

// embeddingDims é a dimensionalidade dos embeddings gerados.
// 1536: abaixo do limite HNSW do pgvector (2000) e suficiente para retrieval semântico.
var embeddingDims int32 = 1536

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

		// Filtra textos vazios — API Gemini rejeita conteúdo sem texto
		var contents []*genai.Content
		for _, t := range batch {
			if t == "" {
				contents = append(contents, genai.NewContentFromText("item sem descrição", genai.RoleUser))
			} else {
				contents = append(contents, genai.NewContentFromText(t, genai.RoleUser))
			}
		}

		result, err := c.client.Models.EmbedContent(ctx, geminiEmbeddingModel, contents,
			&genai.EmbedContentConfig{
				TaskType:             "RETRIEVAL_DOCUMENT",
				OutputDimensionality: &embeddingDims,
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
			TaskType:             "RETRIEVAL_QUERY",
			OutputDimensionality: &embeddingDims,
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

// hydePromptTemplate é o prompt para geração do documento hipotético.
// Instrui o modelo a produzir texto que se pareça com um item real do catálogo.
const hydePromptTemplate = `Você é um assistente de busca de serviços públicos do Rio de Janeiro.
Um cidadão pesquisou por: "%s"

Escreva 2 a 3 frases descrevendo um serviço público municipal relevante para essa pesquisa, como se fosse o resumo de um item do catálogo oficial. Use linguagem direta e termos que aparecem em documentos de serviços públicos (ex: "o cidadão pode solicitar", "basta comparecer", "disponível online"). Não mencione "hipotético" nem cite a pesquisa.`

// GenerateHyDE gera um documento hipotético (Hypothetical Document Embedding).
// O texto gerado descreve como seria um item real do catálogo que responde à query.
// O embedding desse texto é mais próximo dos documentos relevantes do que o embedding da própria query.
func (c *GeminiEmbeddingClient) GenerateHyDE(ctx context.Context, query string) (string, error) {
	prompt := fmt.Sprintf(hydePromptTemplate, query)
	contents := []*genai.Content{
		genai.NewContentFromText(prompt, genai.RoleUser),
	}

	result, err := c.client.Models.GenerateContent(ctx, "gemini-3.1-flash-lite-preview", contents,
		&genai.GenerateContentConfig{
			MaxOutputTokens: 150,
		},
	)
	if err != nil {
		return "", fmt.Errorf("gemini: hyde generation: %w", err)
	}
	if len(result.Candidates) == 0 || result.Candidates[0].Content == nil {
		return "", fmt.Errorf("gemini: hyde: resposta sem candidatos")
	}
	text := result.Text()
	if text == "" {
		return "", fmt.Errorf("gemini: hyde: texto vazio")
	}
	return strings.TrimSpace(text), nil
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
