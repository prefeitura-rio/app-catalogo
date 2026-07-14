package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

const (
	embeddingBackfillBatchSize       = 50
	embeddingGenerationBatchSize     = 10
	embeddingClaimTimeout            = 15 * time.Minute
	embeddingClaimReleaseTimeout     = 5 * time.Second
	defaultEmbeddingRequestTimeout   = 30 * time.Second
	maximumEmbeddingDescriptionRunes = 600
	maximumEmbeddingTags             = 2
)

type embeddingItemRepository interface {
	ClaimItemsForEmbedding(
		ctx context.Context,
		metadata models.EmbeddingMetadata,
		limit int,
		claimTimeout time.Duration,
	) (*repository.EmbeddingClaim, error)
	CompleteEmbedding(
		ctx context.Context,
		completion repository.EmbeddingCompletion,
	) (bool, error)
	ReleaseEmbeddingClaim(ctx context.Context, claimToken uuid.UUID) error
}

type documentEmbedder interface {
	EmbedDocuments(ctx context.Context, documentTexts []string) ([][]float32, error)
	Metadata() clients.EmbeddingMetadata
}

// EmbeddingService generates and stores semantic catalog embeddings outside
// the request path.
type EmbeddingService struct {
	itemRepository   embeddingItemRepository
	documentEmbedder documentEmbedder
	requestTimeout   time.Duration
}

func NewEmbeddingService(
	itemRepository embeddingItemRepository,
	documentEmbedder documentEmbedder,
	requestTimeout time.Duration,
) *EmbeddingService {
	if requestTimeout <= 0 {
		requestTimeout = defaultEmbeddingRequestTimeout
	}
	return &EmbeddingService{
		itemRepository:   itemRepository,
		documentEmbedder: documentEmbedder,
		requestTimeout:   requestTimeout,
	}
}

// BackfillPass claims one bounded work set, generates compatible vectors, and
// writes each vector only while its source claim is still current.
func (service *EmbeddingService) BackfillPass(ctx context.Context) int {
	metadata := service.documentEmbedder.Metadata()
	claim, err := service.itemRepository.ClaimItemsForEmbedding(
		ctx,
		metadata,
		embeddingBackfillBatchSize,
		embeddingClaimTimeout,
	)
	if err != nil {
		log.Error().Err(err).Msg("embedding backfill: failed to claim catalog items")
		return 0
	}
	if len(claim.Items) == 0 {
		return 0
	}
	defer service.releaseEmbeddingClaim(claim.Token)

	processedCount := 0
	for batchStart := 0; batchStart < len(claim.Items); batchStart += embeddingGenerationBatchSize {
		if ctx.Err() != nil {
			return processedCount
		}

		batchEnd := min(batchStart+embeddingGenerationBatchSize, len(claim.Items))
		catalogItems := claim.Items[batchStart:batchEnd]
		documents := make([]embeddingDocument, len(catalogItems))
		documentTexts := make([]string, len(catalogItems))
		for catalogItemIndex, catalogItem := range catalogItems {
			documents[catalogItemIndex] = buildEmbeddingDocument(catalogItem, metadata.DocumentVersion)
			documentTexts[catalogItemIndex] = documents[catalogItemIndex].Text
		}

		embeddingContext, cancelEmbedding := context.WithTimeout(ctx, service.requestTimeout)
		embeddings, err := service.documentEmbedder.EmbedDocuments(embeddingContext, documentTexts)
		cancelEmbedding()
		if err != nil {
			log.Error().Err(err).Int("batch_start", batchStart).Msg("embedding backfill: provider request failed")
			break
		}
		if len(embeddings) != len(catalogItems) {
			log.Error().
				Int("batch_start", batchStart).
				Int("expected_count", len(catalogItems)).
				Int("actual_count", len(embeddings)).
				Msg("embedding backfill: provider returned an invalid embedding count")
			continue
		}

		for catalogItemIndex, catalogItem := range catalogItems {
			embedding := embeddings[catalogItemIndex]
			if len(embedding) != metadata.Dimensions {
				log.Error().
					Str("id", catalogItem.ID.String()).
					Int("expected_dimensions", metadata.Dimensions).
					Int("actual_dimensions", len(embedding)).
					Msg("embedding backfill: provider returned incompatible dimensions")
				continue
			}

			completed, err := service.itemRepository.CompleteEmbedding(ctx, repository.EmbeddingCompletion{
				ItemID:        catalogItem.ID,
				ClaimToken:    claim.Token,
				VectorLiteral: clients.VectorLiteral(embedding),
				SourceHash:    documents[catalogItemIndex].SourceHash,
				Metadata:      metadata,
			})
			if err != nil {
				log.Error().Err(err).Str("id", catalogItem.ID.String()).Msg("embedding backfill: failed to persist vector")
				continue
			}
			if !completed {
				log.Info().Str("id", catalogItem.ID.String()).Msg("embedding backfill: discarded stale vector")
				continue
			}
			processedCount++
		}
	}

	log.Info().
		Int("processed", processedCount).
		Int("claimed", len(claim.Items)).
		Msg("embedding backfill: pass completed")
	return processedCount
}

func (service *EmbeddingService) releaseEmbeddingClaim(claimToken uuid.UUID) {
	releaseContext, cancelRelease := context.WithTimeout(context.Background(), embeddingClaimReleaseTimeout)
	defer cancelRelease()
	if err := service.itemRepository.ReleaseEmbeddingClaim(releaseContext, claimToken); err != nil {
		log.Error().Err(err).Str("claim_token", claimToken.String()).Msg("embedding backfill: failed to release claim")
	}
}

type embeddingDocument struct {
	Text       string
	SourceHash string
}

// buildEmbeddingDocument produces stable, valid UTF-8 input. The source hash
// covers both the canonical text and its format version.
func buildEmbeddingDocument(catalogItem *models.CatalogItem, documentVersion string) embeddingDocument {
	documentLines := make([]string, 0, 6)
	documentLines = appendEmbeddingDocumentField(documentLines, "Tipo", string(catalogItem.Type))
	documentLines = appendEmbeddingDocumentField(documentLines, "Título", catalogItem.Title)

	canonicalTags := canonicalizeEmbeddingTags(catalogItem.Tags)
	if len(canonicalTags) > 0 {
		documentLines = append(documentLines, "Categorias: "+strings.Join(canonicalTags, ", "))
	}

	documentLines = appendEmbeddingDocumentField(documentLines, "Resumo", catalogItem.ShortDesc)
	normalizedDescription := normalizeEmbeddingText(catalogItem.Description)
	documentLines = appendEmbeddingDocumentField(
		documentLines,
		"Descrição",
		truncateEmbeddingText(normalizedDescription, maximumEmbeddingDescriptionRunes),
	)
	documentLines = appendEmbeddingDocumentField(documentLines, "Órgão", catalogItem.Organization)

	if len(documentLines) == 0 {
		documentLines = append(documentLines, "Título: item sem descrição")
	}
	documentText := strings.Join(documentLines, "\n")
	sourceDigest := sha256.Sum256([]byte(documentVersion + "\x00" + documentText))

	return embeddingDocument{
		Text:       documentText,
		SourceHash: hex.EncodeToString(sourceDigest[:]),
	}
}

func appendEmbeddingDocumentField(documentLines []string, label string, sourceText string) []string {
	normalizedText := normalizeEmbeddingText(sourceText)
	if normalizedText == "" {
		return documentLines
	}
	return append(documentLines, label+": "+normalizedText)
}

func canonicalizeEmbeddingTags(sourceTags []string) []string {
	uniqueTags := make(map[string]struct{}, len(sourceTags))
	for _, sourceTag := range sourceTags {
		normalizedTag := normalizeEmbeddingText(sourceTag)
		if normalizedTag != "" {
			uniqueTags[normalizedTag] = struct{}{}
		}
	}

	canonicalTags := make([]string, 0, len(uniqueTags))
	for canonicalTag := range uniqueTags {
		canonicalTags = append(canonicalTags, canonicalTag)
	}
	sort.Strings(canonicalTags)
	if len(canonicalTags) > maximumEmbeddingTags {
		canonicalTags = canonicalTags[:maximumEmbeddingTags]
	}
	return canonicalTags
}

func normalizeEmbeddingText(sourceText string) string {
	validText := strings.ToValidUTF8(sourceText, "�")
	return strings.Join(strings.Fields(validText), " ")
}

func truncateEmbeddingText(sourceText string, maximumRunes int) string {
	if maximumRunes <= 0 {
		return ""
	}
	textRunes := []rune(sourceText)
	if len(textRunes) <= maximumRunes {
		return sourceText
	}
	return string(textRunes[:maximumRunes])
}
