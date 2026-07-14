package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestValidateCatalogItemAcceptsBoundedItemsFromEverySource(t *testing.T) {
	t.Parallel()

	itemTypesBySource := map[ItemSource]ItemType{
		SourceSalesForce: TypeService,
		SourceCourses:    TypeCourse,
		SourceJobs:       TypeJob,
		SourceMEI:        TypeMEIOpportunity,
		SourceAppGoAPI:   TypeService,
		SourceTypesense:  TypeService,
	}
	for itemSource, itemType := range itemTypesBySource {
		catalogItem := validCatalogItemFixture()
		catalogItem.Source = itemSource
		catalogItem.Type = itemType
		if validationError := ValidateCatalogItem(catalogItem); validationError != nil {
			t.Fatalf("source %q rejected: %v", itemSource, validationError)
		}
	}
}

func TestValidateCatalogItemRejectsUnsafeOrUnboundedFields(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		mutate func(*CatalogItem)
	}{
		{name: "nil item"},
		{name: "invalid source", mutate: func(item *CatalogItem) { item.Source = "unknown" }},
		{name: "invalid type", mutate: func(item *CatalogItem) { item.Type = "unknown" }},
		{name: "invalid status", mutate: func(item *CatalogItem) { item.Status = "unknown" }},
		{name: "empty title", mutate: func(item *CatalogItem) { item.Title = "  " }},
		{name: "oversized title", mutate: func(item *CatalogItem) { item.Title = strings.Repeat("á", MaximumCatalogTitleRunes+1) }},
		{name: "oversized description", mutate: func(item *CatalogItem) { item.Description = strings.Repeat("a", MaximumCatalogDescriptionRunes+1) }},
		{name: "control character", mutate: func(item *CatalogItem) { item.ShortDesc = "unsafe\x00text" }},
		{name: "relative URL", mutate: func(item *CatalogItem) { item.URL = "/services/item" }},
		{name: "URL with surrounding whitespace", mutate: func(item *CatalogItem) { item.URL = " https://example.test/item" }},
		{name: "credential URL", mutate: func(item *CatalogItem) { item.URL = "https://user:secret@example.test/item" }},
		{name: "unsupported URL scheme", mutate: func(item *CatalogItem) { item.ImageURL = "data:image/png;base64,AAAA" }},
		{name: "too many tags", mutate: func(item *CatalogItem) { item.Tags = make([]string, MaximumCatalogArrayItems+1) }},
		{name: "empty neighborhood", mutate: func(item *CatalogItem) { item.Bairros = []string{""} }},
		{name: "oversized array entry", mutate: func(item *CatalogItem) { item.Tags = []string{strings.Repeat("x", MaximumCatalogArrayEntryRunes+1)} }},
		{name: "source data array", mutate: func(item *CatalogItem) { item.SourceData = json.RawMessage(`[]`) }},
		{name: "source data public non-string", mutate: func(item *CatalogItem) { item.SourceData = json.RawMessage(`{"slug":123}`) }},
		{name: "oversized source data", mutate: func(item *CatalogItem) {
			item.SourceData = json.RawMessage(`{"padding":"` + strings.Repeat("x", MaximumCatalogSourceDataBytes) + `"}`)
		}},
		{name: "target audience array", mutate: func(item *CatalogItem) { item.TargetAudience = json.RawMessage(`[]`) }},
		{name: "target audience wrong scalar type", mutate: func(item *CatalogItem) { item.TargetAudience = json.RawMessage(`{"renda":42}`) }},
		{name: "target audience unknown field", mutate: func(item *CatalogItem) { item.TargetAudience = json.RawMessage(`{"unknown":true}`) }},
		{name: "target audience oversized array", mutate: func(item *CatalogItem) {
			escolaridade := make([]string, MaximumCatalogArrayItems+1)
			for valueIndex := range escolaridade {
				escolaridade[valueIndex] = "x"
			}
			encodedAudience, encodeError := json.Marshal(TargetAudienceData{Escolaridade: escolaridade})
			if encodeError != nil {
				panic(encodeError)
			}
			item.TargetAudience = encodedAudience
		}},
		{name: "invalid validity window", mutate: func(item *CatalogItem) {
			validFrom := time.Date(2026, time.July, 12, 0, 0, 0, 0, time.UTC)
			validUntil := validFrom
			item.ValidFrom = &validFrom
			item.ValidUntil = &validUntil
		}},
		{name: "oversized aggregate projection", mutate: func(item *CatalogItem) {
			item.Tags = make([]string, MaximumCatalogArrayItems)
			for tagIndex := range item.Tags {
				item.Tags[tagIndex] = strings.Repeat("x", MaximumCatalogArrayEntryRunes)
			}
		}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var catalogItem *CatalogItem
			if testCase.mutate != nil {
				catalogItem = validCatalogItemFixture()
				testCase.mutate(catalogItem)
			}
			if validationError := ValidateCatalogItem(catalogItem); validationError == nil {
				t.Fatal("ValidateCatalogItem accepted an unsafe item")
			}
		})
	}
}

func validCatalogItemFixture() *CatalogItem {
	return &CatalogItem{
		ExternalID:     "catalog-item-1",
		Source:         SourceTypesense,
		Type:           TypeService,
		Title:          "Serviço municipal",
		Description:    "Descrição pública do serviço",
		ShortDesc:      "Resumo do serviço",
		Organization:   "Secretaria Municipal",
		URL:            "https://example.test/services/catalog-item-1",
		ImageURL:       "https://example.test/images/catalog-item-1.png",
		Bairros:        []string{"Centro"},
		Modalidade:     "digital",
		Status:         StatusActive,
		Tags:           []string{"serviço"},
		TargetAudience: json.RawMessage(`{"pcd":false}`),
		SourceData: json.RawMessage(
			`{"id":"catalog-item-1","slug":"catalog-item-1","tema_geral":"Cidadania"}`,
		),
	}
}
