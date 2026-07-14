package query

import (
	"strings"
	"testing"
)

func TestExpandAddsSynonymsAsORBranches(t *testing.T) {
	testCases := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "acronym",
			query: "cras",
			want:  "cras OR centro referência assistência social",
		},
		{
			name:  "accent-insensitive phrase",
			query: "bilhete único",
			want:  "bilhete único OR passagem ônibus metrô transporte",
		},
		{
			name:  "common accent typo",
			query: "alvara",
			want:  "alvara OR alvará funcionamento licença comercial",
		},
		{
			name:  "specific animal vaccination rule",
			query: "vacina para pet",
			want:  "vacina para pet OR vacinação animal pet",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if expandedQuery := Expand(testCase.query); expandedQuery != testCase.want {
				t.Errorf("Expand(%q) = %q, want %q", testCase.query, expandedQuery, testCase.want)
			}
		})
	}
}

func TestExpandPreservesExplicitWebsearchExpressionsWithoutBroadening(t *testing.T) {
	t.Parallel()

	for _, searchQuery := range []string{
		`"cartão sus"`,
		"iptu -dívida",
		"iptu OR itbi",
	} {
		if expandedQuery := Expand(searchQuery); expandedQuery != searchQuery {
			t.Fatalf("Expand(%q) = %q, want unchanged explicit expression", searchQuery, expandedQuery)
		}
	}
}

func TestExpandDoesNotMatchSubstringsOrAntiPatterns(t *testing.T) {
	testCases := []struct {
		name  string
		query string
	}{
		{name: "sus inside sustentabilidade", query: "sustentabilidade"},
		{name: "upa inside ocupacao", query: "ocupação profissional"},
		{name: "rg inside largo", query: "Largo do Machado"},
		{name: "mei inside meio", query: "meio ambiente"},
		{name: "caps inside capstone", query: "curso capstone"},
		{name: "sms with message anti-pattern", query: "mensagem por sms"},
		{name: "rg with address anti-pattern", query: "rg do endereço"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if expandedQuery := Expand(testCase.query); expandedQuery != testCase.query {
				t.Errorf("Expand(%q) = %q, want unchanged query", testCase.query, expandedQuery)
			}
		})
	}
}

func TestExpandDeduplicatesEquivalentRules(t *testing.T) {
	expandedQuery := Expand("cadunico")
	wantExpansion := "cadastro único benefício"

	if count := strings.Count(expandedQuery, wantExpansion); count != 1 {
		t.Fatalf("expansion count = %d, want 1: %q", count, expandedQuery)
	}
	if !strings.HasPrefix(expandedQuery, "cadunico OR ") {
		t.Fatalf("original query is not an independent OR branch: %q", expandedQuery)
	}
}

func TestExpandCanonicalizesWhitespaceWithoutGeneratingSynonyms(t *testing.T) {
	if expandedQuery := Expand("  curso   de   gastronomia  "); expandedQuery != "curso de gastronomia" {
		t.Fatalf("Expand() = %q", expandedQuery)
	}
}
