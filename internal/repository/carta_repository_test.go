package repository

import (
	"testing"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestClampCartaPage(t *testing.T) {
	cases := []struct {
		page, perPage     int
		wantPage, wantPer int
	}{
		{0, 0, 1, 100},
		{-1, -5, 1, 100},
		{2, 50, 2, 50},
		{1, 101, 1, 100},
		{3, 1, 3, 1},
	}
	for _, tc := range cases {
		page, per := clampCartaPage(tc.page, tc.perPage)
		if page != tc.wantPage || per != tc.wantPer {
			t.Fatalf("clamp(%d,%d)=(%d,%d) want (%d,%d)", tc.page, tc.perPage, page, per, tc.wantPage, tc.wantPer)
		}
	}
}

func TestCartaMeta(t *testing.T) {
	m := cartaMeta(1, 10, 0)
	if m.TotalPages != 0 || m.Total != 0 {
		t.Fatalf("empty: %+v", m)
	}
	m = cartaMeta(2, 10, 25)
	if m.Page != 2 || m.PerPage != 10 || m.Total != 25 || m.TotalPages != 3 {
		t.Fatalf("paged: %+v", m)
	}
	m = cartaMeta(1, 100, 100)
	if m.TotalPages != 1 {
		t.Fatalf("exact page: %+v", m)
	}
}

func TestEnrichCartaServiceSummary(t *testing.T) {
	sum := models.CartaServiceSummary{
		Slug:          "iptu",
		Name:          "IPTU",
		Summary:       "fallback",
		ArticleStatus: "active",
	}
	enrichCartaServiceSummary(&sum, []byte(`{
		"articleStatus": "Online",
		"createdDate": "2024-01-01T00:00:00.000Z",
		"lastModifiedDate": "2024-02-01T00:00:00.000Z",
		"lastPublishedDate": "2024-02-02T00:00:00.000Z",
		"responsibleOrgUnitShortName": "SMFP",
		"info": {"summary": "<p>Resumo</p>", "isFree": true}
	}`))
	if sum.ArticleStatus != "Online" {
		t.Fatalf("articleStatus=%s", sum.ArticleStatus)
	}
	if sum.Summary != "<p>Resumo</p>" || !sum.IsFree {
		t.Fatalf("info: summary=%q free=%v", sum.Summary, sum.IsFree)
	}
	if sum.CreatedDate == "" || sum.LastModifiedDate == "" || sum.LastPublishedDate == "" {
		t.Fatalf("dates missing: %+v", sum)
	}
	if sum.ResponsibleOrgUnitShortName != "SMFP" {
		t.Fatalf("org short=%s", sum.ResponsibleOrgUnitShortName)
	}

	// invalid JSON / nil: no panic, keeps prior values
	before := sum.Summary
	enrichCartaServiceSummary(&sum, []byte(`{`))
	enrichCartaServiceSummary(&sum, nil)
	enrichCartaServiceSummary(nil, []byte(`{}`))
	if sum.Summary != before {
		t.Fatalf("summary changed on bad input: %q", sum.Summary)
	}
}
