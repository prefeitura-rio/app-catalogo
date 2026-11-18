package services

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func searchItemMetadataSchema(t *testing.T) *openapi3.Schema {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatalf("falha ao carregar openapi.yaml: %v", err)
	}
	searchItem := doc.Components.Schemas["SearchItem"]
	if searchItem == nil || searchItem.Value == nil {
		t.Fatal("openapi.yaml sem components.schemas.SearchItem")
	}
	metadata := searchItem.Value.Properties["metadata"]
	if metadata == nil || metadata.Value == nil {
		t.Fatal("openapi.yaml sem SearchItem.properties.metadata")
	}
	return metadata.Value
}

func TestContratoSearchItemMetadata_DocumentaSubconjuntoSanitizado(t *testing.T) {
	schema := searchItemMetadataSchema(t)
	description := schema.Description

	if strings.Contains(description, "Payload original") || strings.Contains(description, "schema livre") {
		t.Fatalf("metadata ainda descrito como payload original de source_data: %q", description)
	}
	if !strings.Contains(description, "sanitizado") {
		t.Fatalf("metadata deve ser descrito como subconjunto sanitizado de source_data: %q", description)
	}

	allowedByAnyType := map[string]bool{}
	for _, itemType := range models.ItemTypes {
		for _, field := range searchMetadataAllowedFields(itemType) {
			allowedByAnyType[field] = true
			if !strings.Contains(description, "`"+field+"`") {
				t.Errorf("chave %q permitida para %q não está documentada em SearchItem.metadata", field, itemType)
			}
		}
	}

	notKeys := map[string]bool{"source_data": true, "type": true, "service": true, "metadata": true}
	for _, match := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(description, -1) {
		token := match[1]
		if notKeys[token] {
			continue
		}
		if !allowedByAnyType[token] {
			t.Errorf("SearchItem.metadata documenta a chave %q, que sanitizedSearchMetadata não devolve", token)
		}
	}
}

func TestContratoSearchItemMetadata_ExemploEhRespostaReal(t *testing.T) {
	schema := searchItemMetadataSchema(t)
	if len(schema.Examples) == 0 {
		t.Fatal("SearchItem.metadata sem exemplo")
	}
	for i, example := range schema.Examples {
		raw, err := json.Marshal(example)
		if err != nil {
			t.Fatalf("exemplo %d: %v", i, err)
		}
		got := sanitizedSearchMetadata(models.TypeService, raw)
		var want, gotFields map[string]any
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatalf("exemplo %d não é objeto: %v", i, err)
		}
		if err := json.Unmarshal(got, &gotFields); err != nil {
			t.Fatalf("exemplo %d: sanitizedSearchMetadata devolveu %q: %v", i, got, err)
		}
		if !reflect.DeepEqual(want, gotFields) {
			t.Errorf("exemplo %d traz chaves que a busca descarta: exemplo=%v resposta=%v", i, want, gotFields)
		}
	}
}
