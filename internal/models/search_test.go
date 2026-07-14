package models

import (
	"reflect"
	"strings"
	"testing"
)

func TestSearchRequestNormalizeCanonicalizesDeterministically(t *testing.T) {
	request := SearchRequest{
		Q:         "  curso   gratuito  ",
		ExpandedQ: "  curso OR capacitação  ",
		Types:     []ItemType{TypeService, ItemType(" COURSE "), TypeService, TypeJob},
		Filters: SearchFilters{
			Modalidade:        " HÍBRIDO ",
			Bairro:            "  Rio   Comprido ",
			Orgao:             "  Secretaria   Municipal ",
			Turno:             " NOTURNO ",
			RegimeContratacao: " CLT ",
			ModeloTrabalho:    " REMOTO ",
			FaixaSalarial:     " ATÉ-2SM ",
			CanalAtendimento:  " TELEFONE ",
			Tema:              "  Saúde   pública ",
			Segmento:          "  Comércio   local ",
		},
	}

	request.Normalize()

	if request.Page != DefaultSearchPage || request.PerPage != DefaultSearchPerPage {
		t.Fatalf("unexpected defaults: page=%d per_page=%d", request.Page, request.PerPage)
	}
	if request.Q != "curso gratuito" {
		t.Errorf("Q = %q", request.Q)
	}
	if request.ExpandedQ != "curso OR capacitação" {
		t.Errorf("ExpandedQ = %q", request.ExpandedQ)
	}
	wantTypes := []ItemType{TypeCourse, TypeJob, TypeService}
	if !reflect.DeepEqual(request.Types, wantTypes) {
		t.Errorf("Types = %v, want %v", request.Types, wantTypes)
	}
	if request.Filters.Modalidade != "hibrido" || request.Filters.Turno != "noturno" {
		t.Errorf("enum filters were not canonicalized: %+v", request.Filters)
	}
	if request.Filters.Bairro != "Rio Comprido" || request.Filters.Tema != "Saúde pública" {
		t.Errorf("free-text filters were not canonicalized: %+v", request.Filters)
	}
}

func TestSearchRequestNormalizePreservesExplicitInvalidPagination(t *testing.T) {
	request := SearchRequest{Page: -1, PerPage: MaxSearchPerPage + 1}

	request.Normalize()

	if request.Page != -1 || request.PerPage != MaxSearchPerPage+1 {
		t.Fatalf("invalid pagination was silently replaced: %+v", request)
	}
}

func TestSearchRequestValidateRejectsInvalidValues(t *testing.T) {
	testCases := []struct {
		name    string
		request SearchRequest
	}{
		{name: "negative page", request: SearchRequest{Page: -1, PerPage: DefaultSearchPerPage}},
		{name: "page over limit", request: SearchRequest{Page: MaxSearchPage + 1, PerPage: DefaultSearchPerPage}},
		{name: "per page over limit", request: SearchRequest{Page: DefaultSearchPage, PerPage: MaxSearchPerPage + 1}},
		{name: "query over rune limit", request: SearchRequest{Q: strings.Repeat("á", MaxSearchQueryRunes+1), Page: DefaultSearchPage, PerPage: DefaultSearchPerPage}},
		{name: "filter over rune limit", request: SearchRequest{Filters: SearchFilters{Bairro: strings.Repeat("á", MaxSearchFilterRunes+1)}, Page: DefaultSearchPage, PerPage: DefaultSearchPerPage}},
		{name: "invalid UTF-8 filter", request: SearchRequest{Filters: SearchFilters{Orgao: string([]byte{0xff})}, Page: DefaultSearchPage, PerPage: DefaultSearchPerPage}},
		{name: "unknown item type", request: SearchRequest{Types: []ItemType{"event"}, Page: DefaultSearchPage, PerPage: DefaultSearchPerPage}},
		{name: "unknown modalidade", request: SearchRequest{Filters: SearchFilters{Modalidade: "teletransporte"}, Page: DefaultSearchPage, PerPage: DefaultSearchPerPage}},
		{name: "unknown turno", request: SearchRequest{Filters: SearchFilters{Turno: "madrugada"}, Page: DefaultSearchPage, PerPage: DefaultSearchPerPage}},
		{name: "unknown hiring regime", request: SearchRequest{Filters: SearchFilters{RegimeContratacao: "freelance"}, Page: DefaultSearchPage, PerPage: DefaultSearchPerPage}},
		{name: "unknown work model", request: SearchRequest{Filters: SearchFilters{ModeloTrabalho: "itinerante"}, Page: DefaultSearchPage, PerPage: DefaultSearchPerPage}},
		{name: "unsupported salary range", request: SearchRequest{Filters: SearchFilters{FaixaSalarial: "2-4sm"}, Page: DefaultSearchPage, PerPage: DefaultSearchPerPage}},
		{name: "unsupported free-course filter", request: SearchRequest{Filters: SearchFilters{Gratuito: boolPointer(true)}, Page: DefaultSearchPage, PerPage: DefaultSearchPerPage}},
		{name: "unknown service channel", request: SearchRequest{Filters: SearchFilters{CanalAtendimento: "fax"}, Page: DefaultSearchPage, PerPage: DefaultSearchPerPage}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.request.Validate(); err == nil {
				t.Fatal("Validate() accepted an invalid request")
			}
		})
	}
}

func boolPointer(booleanValue bool) *bool {
	return &booleanValue
}

func TestSearchRequestValidateAcceptsMaximumUnicodeQuery(t *testing.T) {
	request := SearchRequest{
		Q:       strings.Repeat("á", MaxSearchQueryRunes),
		Types:   []ItemType{TypeCourse},
		Page:    DefaultSearchPage,
		PerPage: MaxSearchPerPage,
		Filters: SearchFilters{
			Bairro:            strings.Repeat("á", MaxSearchFilterRunes),
			Modalidade:        "hibrido",
			Turno:             "noturno",
			RegimeContratacao: "clt",
			ModeloTrabalho:    "remoto",
			CanalAtendimento:  "digital",
		},
	}

	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() returned an unexpected error: %v", err)
	}
}

func TestSearchRankerDescriptorKeepsHyDEGenerationContractOrderedAndTyped(t *testing.T) {
	t.Parallel()

	descriptorType := reflect.TypeOf(SearchRankerDescriptor{})
	expectedFields := []struct {
		name      string
		fieldType reflect.Type
		jsonName  string
	}{
		{name: "HyDEPromptVersion", fieldType: reflect.TypeOf(""), jsonName: "hyde_prompt_version,omitempty"},
		{name: "HyDEPromptSHA256", fieldType: reflect.TypeOf(""), jsonName: "hyde_prompt_sha256,omitempty"},
		{name: "HyDETemperature", fieldType: reflect.TypeOf((*float32)(nil)), jsonName: "hyde_temperature,omitempty"},
		{name: "HyDESeed", fieldType: reflect.TypeOf((*int32)(nil)), jsonName: "hyde_seed,omitempty"},
		{name: "HyDECandidateCount", fieldType: reflect.TypeOf((*int32)(nil)), jsonName: "hyde_candidate_count,omitempty"},
		{name: "HyDEMaxOutputTokens", fieldType: reflect.TypeOf((*int32)(nil)), jsonName: "hyde_max_output_tokens,omitempty"},
		{name: "HyDEResponseMIMEType", fieldType: reflect.TypeOf(""), jsonName: "hyde_response_mime_type,omitempty"},
		{name: "HyDEDeterminismPolicy", fieldType: reflect.TypeOf(""), jsonName: "hyde_determinism_policy,omitempty"},
	}
	firstField, firstFieldExists := descriptorType.FieldByName(expectedFields[0].name)
	if !firstFieldExists {
		t.Fatalf("descriptor is missing %s", expectedFields[0].name)
	}
	for fieldOffset, expectedField := range expectedFields {
		actualField := descriptorType.Field(firstField.Index[0] + fieldOffset)
		if actualField.Name != expectedField.name || actualField.Type != expectedField.fieldType ||
			actualField.Tag.Get("json") != expectedField.jsonName {
			t.Fatalf(
				"HyDE descriptor field %d = %s %s json:%q, want %s %s json:%q",
				fieldOffset,
				actualField.Name,
				actualField.Type,
				actualField.Tag.Get("json"),
				expectedField.name,
				expectedField.fieldType,
				expectedField.jsonName,
			)
		}
	}
}

func TestCatalogItem_ParseTargetAudience_Empty(t *testing.T) {
	item := &CatalogItem{}
	ta, err := item.ParseTargetAudience()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if ta == nil {
		t.Fatal("target audience não deve ser nil")
	}
	if len(ta.Escolaridade) != 0 {
		t.Errorf("escolaridade deveria estar vazia, got %v", ta.Escolaridade)
	}
}

func TestCatalogItem_ParseTargetAudience_Valid(t *testing.T) {
	item := &CatalogItem{
		TargetAudience: []byte(`{"escolaridade":["medio","superior"],"renda":"ate_3sm"}`),
	}
	ta, err := item.ParseTargetAudience()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if len(ta.Escolaridade) != 2 {
		t.Errorf("esperado 2 escolaridades, got %d", len(ta.Escolaridade))
	}
	if ta.Renda != "ate_3sm" {
		t.Errorf("renda esperada 'ate_3sm', got %q", ta.Renda)
	}
}

func TestCatalogItem_ParseTargetAudience_InvalidJSON(t *testing.T) {
	for _, encodedAudience := range []string{
		`not json`,
		`null`,
		`{"unknown":true}`,
		`{} {}`,
	} {
		item := &CatalogItem{TargetAudience: []byte(encodedAudience)}
		targetAudience, parseError := item.ParseTargetAudience()
		if parseError == nil || targetAudience != nil {
			t.Fatalf("target audience %q returned value=%#v error=%v", encodedAudience, targetAudience, parseError)
		}
	}
}

func TestCatalogItem_ParseTargetAudience_NilReceiver(t *testing.T) {
	var item *CatalogItem
	targetAudience, parseError := item.ParseTargetAudience()
	if parseError == nil || targetAudience != nil {
		t.Fatalf("nil catalog item returned value=%#v error=%v", targetAudience, parseError)
	}
}
