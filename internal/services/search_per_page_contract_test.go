package services

import (
	"fmt"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

// O contrato de per_page da busca (openapi.yaml, searchPerPage) precisa
// descrever o que SearchRequest.Normalize faz: < 1 cai no default e > máximo
// é limitado ao máximo (DEV-16, DEV-803).
func TestContratoSearchPerPage_DescreveNormalizacao(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatalf("falha ao carregar openapi.yaml: %v", err)
	}
	param := doc.Components.Parameters["searchPerPage"]
	if param == nil || param.Value == nil || param.Value.Schema == nil || param.Value.Schema.Value == nil {
		t.Fatal("openapi.yaml sem components.parameters.searchPerPage com schema")
	}
	schema := param.Value.Schema.Value

	if schema.Min == nil || *schema.Min != 1 {
		t.Errorf("searchPerPage.minimum = %v, want 1", schema.Min)
	}
	if schema.Max == nil || *schema.Max != float64(models.SearchMaxPerPage) {
		t.Errorf("searchPerPage.maximum = %v, want %d", schema.Max, models.SearchMaxPerPage)
	}
	if fmt.Sprint(schema.Default) != fmt.Sprint(models.SearchDefaultPerPage) {
		t.Errorf("searchPerPage.default = %v, want %d", schema.Default, models.SearchDefaultPerPage)
	}

	description := param.Value.Description
	if strings.Contains(description, "fora de [1,100]") {
		t.Errorf("descrição ainda diz que valores acima do máximo caem no default: %q", description)
	}
	if want := fmt.Sprintf("limitados a %d", models.SearchMaxPerPage); !strings.Contains(description, want) {
		t.Errorf("descrição deve dizer que valores acima do máximo são %q: %q", want, description)
	}
	if want := fmt.Sprintf("default (%d)", models.SearchDefaultPerPage); !strings.Contains(description, want) {
		t.Errorf("descrição deve citar o %q para valores < 1: %q", want, description)
	}
}
