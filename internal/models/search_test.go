package models

import "testing"

func TestSearchRequest_Normalize(t *testing.T) {
	cases := []struct {
		input       SearchRequest
		wantPage    int
		wantPerPage int
	}{
		{SearchRequest{Page: 0, PerPage: 0}, 1, 10},
		{SearchRequest{Page: -1, PerPage: -5}, 1, 10},
		{SearchRequest{Page: 3, PerPage: 50}, 3, 50},
		{SearchRequest{Page: 1, PerPage: 1}, 1, 1},
		{SearchRequest{Page: 1, PerPage: 100}, 1, 100},
		// Acima do máximo do contrato (openapi.yaml: maximum 100) limita a 100,
		// em vez de cair no default (DEV-16).
		{SearchRequest{Page: 1, PerPage: 101}, 1, 100},
		{SearchRequest{Page: 1, PerPage: 999}, 1, 100},
		{SearchRequest{Page: 1, PerPage: 9999}, 1, 100},
	}

	for _, tc := range cases {
		req := tc.input
		req.Normalize()
		if req.Page != tc.wantPage {
			t.Errorf("Page: input %d → got %d, want %d", tc.input.Page, req.Page, tc.wantPage)
		}
		if req.PerPage != tc.wantPerPage {
			t.Errorf("PerPage: input %d → got %d, want %d", tc.input.PerPage, req.PerPage, tc.wantPerPage)
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
	item := &CatalogItem{TargetAudience: []byte(`not json`)}
	ta, err := item.ParseTargetAudience()
	// JSON inválido não deve retornar erro — retorna struct vazia (fail-safe)
	if err != nil {
		t.Fatalf("esperava sem erro para JSON inválido, got %v", err)
	}
	if ta == nil {
		t.Fatal("target audience não deve ser nil mesmo com JSON inválido")
	}
}

func TestParseFaixaSalarial(t *testing.T) {
	valid := map[string]string{
		"":            "",
		"ate-2sm":     FaixaSalarialAte2SM,
		" 2-4sm ":     FaixaSalarial2a4SM,
		"ACIMA-4SM":   FaixaSalarialAcima4SM,
		"   ":         "",
		"Ate-2sm\t":   FaixaSalarialAte2SM,
		"acima-4sm  ": FaixaSalarialAcima4SM,
	}
	for in, want := range valid {
		got, err := ParseFaixaSalarial(in)
		if err != nil || got != want {
			t.Errorf("ParseFaixaSalarial(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	for _, in := range []string{"VALOR-INEXISTENTE-XYZ", "ate-3sm", "2-4", "ate 2sm"} {
		if _, err := ParseFaixaSalarial(in); err != ErrInvalidFaixaSalarial {
			t.Errorf("ParseFaixaSalarial(%q) err = %v; want ErrInvalidFaixaSalarial", in, err)
		}
	}
}

func TestFaixaSalarialBounds_EmSalariosMinimos(t *testing.T) {
	cases := []struct {
		faixa    string
		min, max float64
	}{
		{FaixaSalarialAte2SM, 0, 3242},
		{FaixaSalarial2a4SM, 3242, 6484},
		{FaixaSalarialAcima4SM, 6484, 0},
	}
	for _, tc := range cases {
		min, max, ok := FaixaSalarialBounds(tc.faixa)
		if !ok || min != tc.min || max != tc.max {
			t.Errorf("FaixaSalarialBounds(%q) = (%v, %v, %v); want (%v, %v, true)", tc.faixa, min, max, ok, tc.min, tc.max)
		}
	}
	if _, _, ok := FaixaSalarialBounds("qualquer"); ok {
		t.Error("faixa desconhecida não pode ter intervalo")
	}
}

func TestNormalizeTurno(t *testing.T) {
	cases := map[string]string{
		"":                      "",
		"  ":                    "",
		"matutino":              "MANHA",
		"Manhã":                 "MANHA",
		"MANHA":                 "MANHA",
		"vespertino":            "TARDE",
		"tarde":                 "TARDE",
		"noturno":               "NOITE",
		" NOITE ":               "NOITE",
		"integral":              "INTEGRAL",
		"livre":                 "LIVRE",
		"VALOR-INEXISTENTE-XYZ": "VALOR-INEXISTENTE-XYZ",
	}
	for in, want := range cases {
		if got := NormalizeTurno(in); got != want {
			t.Errorf("NormalizeTurno(%q) = %q; want %q", in, got, want)
		}
	}
}
