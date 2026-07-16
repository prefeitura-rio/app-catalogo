package models

import "testing"

func TestSearchRequest_Normalize(t *testing.T) {
	cases := []struct {
		input   SearchRequest
		wantPage    int
		wantPerPage int
	}{
		{SearchRequest{Page: 0, PerPage: 0}, 1, 10},
		{SearchRequest{Page: -1, PerPage: -5}, 1, 10},
		{SearchRequest{Page: 3, PerPage: 50}, 3, 50},
		{SearchRequest{Page: 1, PerPage: 999}, 1, 10},
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
