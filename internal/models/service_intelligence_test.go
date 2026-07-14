package models

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPublicSuggestionRequestNormalizesUnicodeAndWhitespace(t *testing.T) {
	request := &PublicSuggestionRequest{Query: "  IPTU\t e\u0301  2026  "}

	if normalizeError := request.Normalize(); normalizeError != nil {
		t.Fatalf("Normalize returned error: %v", normalizeError)
	}
	if request.Query != "IPTU é 2026" {
		t.Fatalf("normalized query = %q", request.Query)
	}
}

func TestPublicSuggestionRequestRejectsOversizedQuery(t *testing.T) {
	request := &PublicSuggestionRequest{Query: strings.Repeat("a", MaximumPublicSuggestionQueryRunes+1)}

	if normalizeError := request.Normalize(); normalizeError == nil {
		t.Fatal("Normalize accepted an oversized query")
	}
}

func TestSearchSummaryRequestRejectsDuplicateCandidateIDs(t *testing.T) {
	candidateID := uuid.MustParse("018f2f2f-4f68-7a68-8000-000000000001")
	request := &SearchSummaryRequest{
		Query: "IPTU", CatalogRevision: "catalog-v2:1:eligible",
		CandidateIDs: []uuid.UUID{candidateID, candidateID},
	}

	if normalizeError := request.Normalize(); normalizeError == nil {
		t.Fatal("Normalize accepted duplicate candidate IDs")
	}
}

func TestPublicServiceURLMatchesSuperAppCategoryRoute(t *testing.T) {
	serviceURL := PublicServiceURL("Saúde e Proteção Animal", "castracao-gratuita")

	if serviceURL != "/servicos/categoria/saude e protecao animal/castracao-gratuita" {
		t.Fatalf("service URL = %q", serviceURL)
	}
}
