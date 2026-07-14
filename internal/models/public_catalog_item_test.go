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

func TestPublicServiceDetailAllowsOnlyTypedSourceFields(t *testing.T) {
	publicService, serviceError := NewPublicServiceDetail(&CatalogItem{
		ExternalID:  "source-service",
		Type:        TypeService,
		Title:       "Iluminação pública",
		Description: "Descrição pública",
		ShortDesc:   "Resumo público",
		SourceData: json.RawMessage(`{
			"slug":"iluminacao-publica",
			"slug_history":["luz-na-rua"],
			"tema_geral":"Conservação",
			"sub_categoria":"Iluminação",
			"buttons":[
				{"titulo":"Solicitar","url_service":"https://prefeitura.example/solicitar","is_enabled":true,"ordem":1},
				{"titulo":"Desabilitado","url_service":"https://prefeitura.example/ignorar","is_enabled":false}
			],
			"private_upstream_field":"secret"
		}`),
	})
	if serviceError != nil {
		t.Fatalf("build public service detail: %v", serviceError)
	}
	encodedService, encodeError := json.Marshal(publicService)
	if encodeError != nil {
		t.Fatalf("marshal public service detail: %v", encodeError)
	}
	for _, forbiddenField := range []string{"source_data", "private_upstream_field", "Desabilitado"} {
		if strings.Contains(string(encodedService), forbiddenField) {
			t.Fatalf("public service detail leaked %q: %s", forbiddenField, encodedService)
		}
	}
	for _, requiredField := range []string{"iluminacao-publica", "luz-na-rua", "Solicitar", "Conservação"} {
		if !strings.Contains(string(encodedService), requiredField) {
			t.Fatalf("public service detail omitted %q: %s", requiredField, encodedService)
		}
	}
}

func TestValidatePublicServiceSourceDataRejectsUnsafeEnabledAction(t *testing.T) {
	validationError := ValidateCatalogItem(&CatalogItem{
		ExternalID: "unsafe-action",
		Source:     SourceTypesense,
		Type:       TypeService,
		Title:      "Unsafe action",
		Status:     StatusActive,
		SourceData: json.RawMessage(`{
			"slug":"unsafe-action",
			"buttons":[{"titulo":"Executar","url_service":"javascript:alert(1)","is_enabled":true}]
		}`),
	})
	if validationError == nil || !strings.Contains(validationError.Error(), "action URL is unsafe") {
		t.Fatalf("unsafe public service action error = %v", validationError)
	}
}
