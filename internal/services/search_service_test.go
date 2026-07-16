package services

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
)

func TestBuildResponseIncludesSlugFromSourceData(t *testing.T) {
	sourceData := json.RawMessage(`{"slug":"analista-de-dados-42","titulo":"Analista de dados"}`)
	result := &repository.SearchResult{
		Item: &models.CatalogItem{
			ID:         uuid.New(),
			Type:       models.TypeJob,
			Source:     models.SourceJobs,
			Title:      "Analista de dados",
			SourceData: sourceData,
		},
		Rank: 1.2,
	}

	resp := (&SearchService{}).buildResponse(
		[]*repository.SearchResult{result},
		1,
		&models.SearchRequest{Page: 1, PerPage: 10},
	)

	if got := resp.Items[0].Slug; got != "analista-de-dados-42" {
		t.Fatalf("slug = %q, esperava analista-de-dados-42", got)
	}
}
