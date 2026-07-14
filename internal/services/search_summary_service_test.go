package services

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

type searchSummaryRepositoryStub struct {
	snapshot *repository.SearchSummaryCandidateSnapshot
	err      error
}

type blockingGroundedSummaryGenerator struct {
	started chan struct{}
	release chan struct{}
}

func (generator *blockingGroundedSummaryGenerator) GenerateGroundedSummary(
	context.Context,
	string,
	[]clients.GroundedSummaryCandidate,
) ([]clients.GeneratedSummarySegment, error) {
	close(generator.started)
	<-generator.release
	candidateIndex := 0
	return []clients.GeneratedSummarySegment{{Text: "Resumo concluído.", CandidateIndex: &candidateIndex}}, nil
}

func (stub *searchSummaryRepositoryStub) GetSearchSummaryCandidates(
	context.Context,
	string,
	[]uuid.UUID,
) (*repository.SearchSummaryCandidateSnapshot, error) {
	return stub.snapshot, stub.err
}

type groundedSummaryGeneratorStub struct {
	segments   []clients.GeneratedSummarySegment
	err        error
	candidates []clients.GroundedSummaryCandidate
}

func (stub *groundedSummaryGeneratorStub) GenerateGroundedSummary(
	_ context.Context,
	_ string,
	candidates []clients.GroundedSummaryCandidate,
) ([]clients.GeneratedSummarySegment, error) {
	stub.candidates = candidates
	return stub.segments, stub.err
}

func TestSearchSummaryServiceMapsOnlyTrustedCatalogCitation(t *testing.T) {
	t.Parallel()

	candidateID := uuid.MustParse("018f2f2f-4f68-7a68-8000-000000000001")
	candidateIndex := 0
	repositoryStub := &searchSummaryRepositoryStub{snapshot: &repository.SearchSummaryCandidateSnapshot{
		CatalogRevision: "catalog-v2:1:eligible",
		Items: []*models.CatalogItem{{
			ID: candidateID, Type: models.TypeService, Title: "IPTU", ShortDesc: "Consulte débitos.",
			URL: "https://untrusted-model.example", SourceData: json.RawMessage(`{"slug":"iptu","tema_geral":"Fazenda"}`),
		}},
	}}
	generatorStub := &groundedSummaryGeneratorStub{segments: []clients.GeneratedSummarySegment{{
		Text: "Consulte seus débitos de IPTU.", CandidateIndex: &candidateIndex,
	}}}
	service := NewSearchSummaryService(repositoryStub, generatorStub, nil, time.Second, time.Minute, 1)
	request := &models.SearchSummaryRequest{
		Query: "IPTU", CatalogRevision: repositoryStub.snapshot.CatalogRevision,
		CandidateIDs: []uuid.UUID{candidateID},
	}

	response, generationError := service.Generate(context.Background(), request)
	if generationError != nil {
		t.Fatalf("Generate returned error: %v", generationError)
	}
	if !response.Generated || len(response.Segments) != 1 {
		t.Fatalf("response = %#v", response)
	}
	segment := response.Segments[0]
	if segment.Slug != "iptu" || segment.URL != "/servicos/categoria/fazenda/iptu" {
		t.Fatalf("grounded segment = %#v", segment)
	}
	if len(generatorStub.candidates) != 1 || generatorStub.candidates[0].Title != "IPTU" {
		t.Fatalf("generator candidates = %#v", generatorStub.candidates)
	}
}

func TestSearchSummaryServiceDegradesWhenGeminiIsUnavailable(t *testing.T) {
	t.Parallel()

	candidateID := uuid.MustParse("018f2f2f-4f68-7a68-8000-000000000002")
	repositoryStub := &searchSummaryRepositoryStub{snapshot: &repository.SearchSummaryCandidateSnapshot{
		CatalogRevision: "catalog-v2:2:eligible",
		Items:           []*models.CatalogItem{{ID: candidateID, Type: models.TypeJob}},
	}}
	service := NewSearchSummaryService(repositoryStub, nil, nil, time.Second, time.Minute, 1)

	response, generationError := service.Generate(context.Background(), &models.SearchSummaryRequest{
		Query: "emprego", CatalogRevision: repositoryStub.snapshot.CatalogRevision,
		CandidateIDs: []uuid.UUID{candidateID},
	})
	if generationError != nil {
		t.Fatalf("Generate returned error: %v", generationError)
	}
	if response.Generated || len(response.Segments) != 0 {
		t.Fatalf("fallback response = %#v", response)
	}
}

func TestSearchSummaryServicePropagatesCatalogRevisionMismatch(t *testing.T) {
	t.Parallel()

	repositoryStub := &searchSummaryRepositoryStub{err: models.ErrCatalogRevisionMismatch}
	service := NewSearchSummaryService(repositoryStub, nil, nil, time.Second, time.Minute, 1)
	_, generationError := service.Generate(context.Background(), &models.SearchSummaryRequest{
		Query: "IPTU", CatalogRevision: "catalog-v2:stale",
		CandidateIDs: []uuid.UUID{uuid.MustParse("018f2f2f-4f68-7a68-8000-000000000003")},
	})
	if !errors.Is(generationError, models.ErrCatalogRevisionMismatch) {
		t.Fatalf("generation error = %v", generationError)
	}
}

func TestSearchSummaryServiceRejectsExcessConcurrentProviderWork(t *testing.T) {
	candidateID := uuid.MustParse("018f2f2f-4f68-7a68-8000-000000000004")
	repositoryStub := &searchSummaryRepositoryStub{snapshot: &repository.SearchSummaryCandidateSnapshot{
		CatalogRevision: "catalog-v2:4:eligible",
		Items:           []*models.CatalogItem{{ID: candidateID, Type: models.TypeJob, Title: "Vaga"}},
	}}
	generator := &blockingGroundedSummaryGenerator{started: make(chan struct{}), release: make(chan struct{})}
	service := NewSearchSummaryService(repositoryStub, generator, nil, time.Second, time.Minute, 1)
	firstResponse := make(chan *models.SearchSummaryResponse, 1)
	firstError := make(chan error, 1)
	go func() {
		response, generationError := service.Generate(context.Background(), &models.SearchSummaryRequest{
			Query: "primeira busca", CatalogRevision: repositoryStub.snapshot.CatalogRevision,
			CandidateIDs: []uuid.UUID{candidateID},
		})
		firstResponse <- response
		firstError <- generationError
	}()
	<-generator.started

	secondResponse, secondError := service.Generate(context.Background(), &models.SearchSummaryRequest{
		Query: "segunda busca", CatalogRevision: repositoryStub.snapshot.CatalogRevision,
		CandidateIDs: []uuid.UUID{candidateID},
	})
	if secondError != nil || secondResponse.Generated {
		t.Fatalf("capacity fallback response = %#v error=%v", secondResponse, secondError)
	}
	close(generator.release)
	if generationError := <-firstError; generationError != nil {
		t.Fatalf("first generation error = %v", generationError)
	}
	if response := <-firstResponse; response == nil || !response.Generated {
		t.Fatalf("first generation response = %#v", response)
	}
}
