package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

const (
	embeddingBackfillBatchSize = 50 // itens por passagem de backfill
	embeddingGenBatchSize      = 10 // itens por chamada à API Gemini
)

// EmbeddingService gera e armazena embeddings semânticos para itens do catálogo.
// É executado como worker periódico — não bloqueia o path de busca.
type EmbeddingService struct {
	itemRepo *repository.CatalogItemRepository
	gemini   *clients.GeminiEmbeddingClient
}

func NewEmbeddingService(
	itemRepo *repository.CatalogItemRepository,
	gemini *clients.GeminiEmbeddingClient,
) *EmbeddingService {
	return &EmbeddingService{itemRepo: itemRepo, gemini: gemini}
}

// BackfillPass processa uma passagem de backfill: busca itens sem embedding,
// gera os vetores e persiste no banco. Retorna o número de itens processados.
func (s *EmbeddingService) BackfillPass(ctx context.Context) int {
	items, err := s.itemRepo.GetItemsWithoutEmbedding(ctx, embeddingBackfillBatchSize)
	if err != nil {
		log.Error().Err(err).Msg("embedding backfill: erro ao buscar itens")
		return 0
	}
	if len(items) == 0 {
		return 0
	}

	processed := 0
	for i := 0; i < len(items); i += embeddingGenBatchSize {
		end := min(i+embeddingGenBatchSize, len(items))
		batch := items[i:end]

		texts := make([]string, len(batch))
		for j, item := range batch {
			texts[j] = buildDocumentText(item)
		}

		embeddings, err := s.gemini.EmbedDocuments(ctx, texts)
		if err != nil {
			log.Error().Err(err).Int("batch_start", i).Msg("embedding backfill: erro na API Gemini")
			time.Sleep(2 * time.Second) // back-off simples em caso de erro
			continue
		}

		for j, item := range batch {
			if j >= len(embeddings) {
				break
			}
			vectorLit := clients.VectorLiteral(embeddings[j])
			if err := s.itemRepo.UpdateEmbedding(ctx, item.ID.String(), vectorLit); err != nil {
				log.Error().Err(err).Str("id", item.ID.String()).Msg("embedding backfill: erro ao salvar")
				continue
			}
			processed++
		}
	}

	log.Info().Int("processed", processed).Int("total", len(items)).Msg("embedding backfill: passagem concluída")
	return processed
}

// buildDocumentText monta o texto de um item para embedding de indexação.
// Formato: "título. categoria. resumo descrição[:600]".
func buildDocumentText(item *models.CatalogItem) string {
	var parts []string
	if item.Title != "" {
		parts = append(parts, item.Title+".")
	}
	if len(item.Tags) > 0 {
		parts = append(parts, fmt.Sprintf("Categoria: %s.", strings.Join(item.Tags[:min(2, len(item.Tags))], ", ")))
	}
	if item.ShortDesc != "" {
		parts = append(parts, item.ShortDesc)
	}
	if item.Description != "" {
		desc := item.Description
		if len(desc) > 600 {
			desc = desc[:600]
		}
		parts = append(parts, desc)
	}
	if item.Organization != "" {
		parts = append(parts, "Órgão: "+item.Organization+".")
	}
	return strings.Join(parts, " ")
}
