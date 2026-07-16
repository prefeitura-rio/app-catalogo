package datasource

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
)

func TestMapJobPreservesSlugInSourceData(t *testing.T) {
	job := clients.Job{
		ID:          "42",
		Slug:        "analista-de-dados-42",
		Title:       "Analista de dados",
		Description: "Vaga para analista de dados",
		UpdatedAt:   time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC),
	}

	item := mapJob(job)

	var sourceData struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(item.SourceData, &sourceData); err != nil {
		t.Fatalf("source_data inválido: %v", err)
	}
	if sourceData.Slug != job.Slug {
		t.Fatalf("slug em source_data = %q, esperava %q", sourceData.Slug, job.Slug)
	}
}
