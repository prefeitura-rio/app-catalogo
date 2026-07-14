package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

type fakeEmbeddingItemRepository struct {
	claim               *repository.EmbeddingClaim
	claimError          error
	claimedMetadata     models.EmbeddingMetadata
	claimedLimit        int
	claimedTimeout      time.Duration
	completions         []repository.EmbeddingCompletion
	completionAccepted  bool
	completionError     error
	releasedClaimTokens []uuid.UUID
}

func (fakeRepository *fakeEmbeddingItemRepository) ClaimItemsForEmbedding(
	_ context.Context,
	metadata models.EmbeddingMetadata,
	limit int,
	claimTimeout time.Duration,
) (*repository.EmbeddingClaim, error) {
	fakeRepository.claimedMetadata = metadata
	fakeRepository.claimedLimit = limit
	fakeRepository.claimedTimeout = claimTimeout
	return fakeRepository.claim, fakeRepository.claimError
}

func (fakeRepository *fakeEmbeddingItemRepository) CompleteEmbedding(
	_ context.Context,
	completion repository.EmbeddingCompletion,
) (bool, error) {
	fakeRepository.completions = append(fakeRepository.completions, completion)
	return fakeRepository.completionAccepted, fakeRepository.completionError
}

func (fakeRepository *fakeEmbeddingItemRepository) ReleaseEmbeddingClaim(
	_ context.Context,
	claimToken uuid.UUID,
) error {
	fakeRepository.releasedClaimTokens = append(fakeRepository.releasedClaimTokens, claimToken)
	return nil
}

type fakeDocumentEmbedder struct {
	metadata       clients.EmbeddingMetadata
	documentTexts  [][]string
	embeddings     [][]float32
	embeddingError error
	embedFunction  func(context.Context, []string) ([][]float32, error)
}

func (fakeEmbedder *fakeDocumentEmbedder) EmbedDocuments(
	embeddingContext context.Context,
	documentTexts []string,
) ([][]float32, error) {
	copiedDocumentTexts := append([]string(nil), documentTexts...)
	fakeEmbedder.documentTexts = append(fakeEmbedder.documentTexts, copiedDocumentTexts)
	if fakeEmbedder.embedFunction != nil {
		return fakeEmbedder.embedFunction(embeddingContext, documentTexts)
	}
	return fakeEmbedder.embeddings, fakeEmbedder.embeddingError
}

func (fakeEmbedder *fakeDocumentEmbedder) Metadata() clients.EmbeddingMetadata {
	return fakeEmbedder.metadata
}

func TestBackfillPassPersistsVectorAndMetadataAtomically(t *testing.T) {
	claimToken := uuid.New()
	catalogItem := &models.CatalogItem{
		ID:           uuid.New(),
		Type:         models.TypeCourse,
		Title:        "Introdução à informática",
		ShortDesc:    "Curso gratuito",
		Description:  "Aprenda habilidades digitais.",
		Organization: "Secretaria de Trabalho",
		Tags:         []string{"Digital", "Qualificação"},
	}
	metadata := testEmbeddingMetadata()
	fakeRepository := &fakeEmbeddingItemRepository{
		claim: &repository.EmbeddingClaim{
			Token: claimToken,
			Items: []*models.CatalogItem{catalogItem},
		},
		completionAccepted: true,
	}
	fakeEmbedder := &fakeDocumentEmbedder{
		metadata:   metadata,
		embeddings: [][]float32{{0.25, 0.5, 0.75}},
	}
	service := NewEmbeddingService(fakeRepository, fakeEmbedder, defaultEmbeddingRequestTimeout)

	processedCount := service.BackfillPass(context.Background())

	if processedCount != 1 {
		t.Fatalf("processed count = %d, expected 1", processedCount)
	}
	if fakeRepository.claimedMetadata != metadata {
		t.Fatalf("claimed metadata = %#v, expected %#v", fakeRepository.claimedMetadata, metadata)
	}
	if fakeRepository.claimedLimit != embeddingBackfillBatchSize {
		t.Fatalf("claimed limit = %d, expected %d", fakeRepository.claimedLimit, embeddingBackfillBatchSize)
	}
	if fakeRepository.claimedTimeout != embeddingClaimTimeout {
		t.Fatalf("claim timeout = %s, expected %s", fakeRepository.claimedTimeout, embeddingClaimTimeout)
	}
	if len(fakeRepository.completions) != 1 {
		t.Fatalf("completion count = %d, expected 1", len(fakeRepository.completions))
	}

	completion := fakeRepository.completions[0]
	if completion.ItemID != catalogItem.ID || completion.ClaimToken != claimToken {
		t.Fatalf("completion identity = (%s, %s), expected (%s, %s)", completion.ItemID, completion.ClaimToken, catalogItem.ID, claimToken)
	}
	if completion.VectorLiteral != "[0.25,0.5,0.75]" {
		t.Fatalf("vector literal = %q, expected [0.25,0.5,0.75]", completion.VectorLiteral)
	}
	if completion.Metadata != metadata {
		t.Fatalf("completion metadata = %#v, expected %#v", completion.Metadata, metadata)
	}
	if len(completion.SourceHash) != sha256HexLength {
		t.Fatalf("source hash length = %d, expected %d", len(completion.SourceHash), sha256HexLength)
	}
	if len(fakeEmbedder.documentTexts) != 1 || len(fakeEmbedder.documentTexts[0]) != 1 {
		t.Fatalf("document batches = %#v, expected one document", fakeEmbedder.documentTexts)
	}
	expectedDocument := buildEmbeddingDocument(catalogItem, metadata.DocumentVersion)
	if fakeEmbedder.documentTexts[0][0] != expectedDocument.Text || completion.SourceHash != expectedDocument.SourceHash {
		t.Fatalf("persisted document provenance does not match canonical document")
	}
	if len(fakeRepository.releasedClaimTokens) != 1 || fakeRepository.releasedClaimTokens[0] != claimToken {
		t.Fatalf("released claim tokens = %v, expected %s", fakeRepository.releasedClaimTokens, claimToken)
	}
}

func TestBackfillPassRejectsProviderCountMismatch(t *testing.T) {
	claimToken := uuid.New()
	fakeRepository := &fakeEmbeddingItemRepository{
		claim: &repository.EmbeddingClaim{
			Token: claimToken,
			Items: []*models.CatalogItem{
				{ID: uuid.New(), Title: "First"},
				{ID: uuid.New(), Title: "Second"},
			},
		},
		completionAccepted: true,
	}
	fakeEmbedder := &fakeDocumentEmbedder{
		metadata:   testEmbeddingMetadata(),
		embeddings: [][]float32{{0.25, 0.5, 0.75}},
	}
	service := NewEmbeddingService(fakeRepository, fakeEmbedder, defaultEmbeddingRequestTimeout)

	processedCount := service.BackfillPass(context.Background())

	if processedCount != 0 {
		t.Fatalf("processed count = %d, expected 0", processedCount)
	}
	if len(fakeRepository.completions) != 0 {
		t.Fatalf("completion count = %d, expected 0", len(fakeRepository.completions))
	}
	if len(fakeRepository.releasedClaimTokens) != 1 || fakeRepository.releasedClaimTokens[0] != claimToken {
		t.Fatalf("released claim tokens = %v, expected %s", fakeRepository.releasedClaimTokens, claimToken)
	}
}

func TestBackfillPassDoesNotCountStaleCompletion(t *testing.T) {
	claimToken := uuid.New()
	fakeRepository := &fakeEmbeddingItemRepository{
		claim: &repository.EmbeddingClaim{
			Token: claimToken,
			Items: []*models.CatalogItem{{ID: uuid.New(), Title: "Updated while embedding"}},
		},
		completionAccepted: false,
	}
	fakeEmbedder := &fakeDocumentEmbedder{
		metadata:   testEmbeddingMetadata(),
		embeddings: [][]float32{{0.25, 0.5, 0.75}},
	}
	service := NewEmbeddingService(fakeRepository, fakeEmbedder, defaultEmbeddingRequestTimeout)

	processedCount := service.BackfillPass(context.Background())

	if processedCount != 0 {
		t.Fatalf("processed count = %d, expected 0", processedCount)
	}
	if len(fakeRepository.completions) != 1 {
		t.Fatalf("completion count = %d, expected 1 attempted completion", len(fakeRepository.completions))
	}
}

func TestBackfillPassReturnsZeroWhenClaimFails(t *testing.T) {
	fakeRepository := &fakeEmbeddingItemRepository{claimError: errors.New("database unavailable")}
	fakeEmbedder := &fakeDocumentEmbedder{metadata: testEmbeddingMetadata()}
	service := NewEmbeddingService(fakeRepository, fakeEmbedder, defaultEmbeddingRequestTimeout)

	processedCount := service.BackfillPass(context.Background())

	if processedCount != 0 {
		t.Fatalf("processed count = %d, expected 0", processedCount)
	}
}

func TestBackfillPassBoundsEachProviderRequestAndReleasesClaim(t *testing.T) {
	claimToken := uuid.New()
	fakeRepository := &fakeEmbeddingItemRepository{
		claim: &repository.EmbeddingClaim{
			Token: claimToken,
			Items: []*models.CatalogItem{{ID: uuid.New(), Title: "Bounded provider request"}},
		},
	}
	fakeEmbedder := &fakeDocumentEmbedder{
		metadata: testEmbeddingMetadata(),
		embedFunction: func(embeddingContext context.Context, _ []string) ([][]float32, error) {
			<-embeddingContext.Done()
			return nil, embeddingContext.Err()
		},
	}
	service := NewEmbeddingService(fakeRepository, fakeEmbedder, time.Millisecond)

	processedCount := service.BackfillPass(context.Background())

	if processedCount != 0 {
		t.Fatalf("processed count = %d, expected 0", processedCount)
	}
	if len(fakeRepository.releasedClaimTokens) != 1 || fakeRepository.releasedClaimTokens[0] != claimToken {
		t.Fatalf("released claim tokens = %v, expected %s", fakeRepository.releasedClaimTokens, claimToken)
	}
}

func TestBuildEmbeddingDocumentIsCanonicalAndUTF8Safe(t *testing.T) {
	invalidTitle := string([]byte{'A', 0xff, 'B'})
	catalogItem := &models.CatalogItem{
		Type:        models.TypeService,
		Title:       invalidTitle,
		Description: strings.Repeat("🚀", maximumEmbeddingDescriptionRunes+20),
		Tags:        []string{" beta ", "alpha", "alpha"},
	}

	document := buildEmbeddingDocument(catalogItem, "catalog-item-v1")

	if !utf8.ValidString(document.Text) {
		t.Fatalf("canonical document is not valid UTF-8: %q", document.Text)
	}
	if !strings.Contains(document.Text, "Título: A�B") {
		t.Fatalf("canonical document did not replace invalid UTF-8: %q", document.Text)
	}
	if !strings.Contains(document.Text, "Categorias: alpha, beta") {
		t.Fatalf("canonical tags are not sorted and deduplicated: %q", document.Text)
	}
	descriptionLine := strings.TrimPrefix(document.Text[strings.Index(document.Text, "Descrição: "):], "Descrição: ")
	if descriptionRuneCount := utf8.RuneCountInString(descriptionLine); descriptionRuneCount != maximumEmbeddingDescriptionRunes {
		t.Fatalf("description rune count = %d, expected %d", descriptionRuneCount, maximumEmbeddingDescriptionRunes)
	}
	if len(document.SourceHash) != sha256HexLength {
		t.Fatalf("source hash length = %d, expected %d", len(document.SourceHash), sha256HexLength)
	}
}

func TestBuildEmbeddingDocumentHashIsStableAndVersioned(t *testing.T) {
	firstItem := &models.CatalogItem{
		Type:      models.TypeJob,
		Title:     "  Analista   de dados ",
		ShortDesc: "Vaga presencial",
		Tags:      []string{"Tecnologia", "Emprego"},
	}
	secondItem := &models.CatalogItem{
		Type:      models.TypeJob,
		Title:     "Analista de dados",
		ShortDesc: "Vaga presencial",
		Tags:      []string{"Emprego", "Tecnologia", "Emprego"},
	}

	firstDocument := buildEmbeddingDocument(firstItem, "catalog-item-v1")
	secondDocument := buildEmbeddingDocument(secondItem, "catalog-item-v1")
	nextVersionDocument := buildEmbeddingDocument(secondItem, "catalog-item-v2")

	if firstDocument != secondDocument {
		t.Fatalf("equivalent source values produced different canonical documents")
	}
	if secondDocument.SourceHash == nextVersionDocument.SourceHash {
		t.Fatalf("document version change did not change source hash")
	}
}

const sha256HexLength = 64

func testEmbeddingMetadata() clients.EmbeddingMetadata {
	return clients.EmbeddingMetadata{
		Model:            "test-embedding-model",
		Version:          "v1",
		Dimensions:       3,
		DocumentTaskType: "RETRIEVAL_DOCUMENT",
		QueryTaskType:    "RETRIEVAL_QUERY",
		DocumentVersion:  "catalog-item-test-v1",
	}
}
