package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicCatalogItemExcludesRawAndInternalFields(t *testing.T) {
	publicItem := NewPublicCatalogItem(&CatalogItem{
		ExternalID:     "source-1",
		Title:          "Serviço público",
		TargetAudience: json.RawMessage(`{"renda":"restrita"}`),
		SourceData:     json.RawMessage(`{"private_upstream_field":"secret"}`),
	})
	encodedItem, encodeError := json.Marshal(publicItem)
	if encodeError != nil {
		t.Fatalf("marshal public catalog item: %v", encodeError)
	}
	for _, forbiddenField := range []string{"target_audience", "source_data", "private_upstream_field"} {
		if strings.Contains(string(encodedItem), forbiddenField) {
			t.Fatalf("public catalog item leaked %q: %s", forbiddenField, encodedItem)
		}
	}
	if !strings.Contains(string(encodedItem), `"source_id":"source-1"`) {
		t.Fatalf("public catalog item omitted stable source identifier: %s", encodedItem)
	}
}
