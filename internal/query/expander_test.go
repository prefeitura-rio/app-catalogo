package query

import (
	"reflect"
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"vazia", "", ""},
		{"só espaços", "   ", ""},
		{"só pontuação", "?!", "?!"},
		{"sem sinônimo", "praia limpa", "praia limpa"},
		{"espaços normalizados", "  praia   limpa ", "praia limpa"},
		{"sigla simples", "cras", "cras OR centro referência assistência social"},
		{"caixa e acento ignorados no match", "CRAS perto", "CRAS perto OR centro referência assistência social"},
		{"padrão de duas palavras", "Bolsa Família", "Bolsa Família OR transferência renda benefício assistência"},
		{"padrão com várias entradas", "vacina para cachorro", "vacina para cachorro OR vacinação animal pet"},
		{"anti-padrão bloqueia", "bpc banco", "bpc banco"},
		{"anti-padrão por palavra inteira", "mei", "mei OR microempreendedor individual empresa"},
		{"anti-padrão meio", "meio mei", "meio mei"},
		{"número", "156", "156 OR central atendimento prefeitura solicitação"},
		{"expansões repetidas aparecem uma vez", "cadunico cad unico", "cadunico cad unico OR cadastro único benefício"},
		{"várias regras", "iptu e cpf", "iptu e cpf OR cadastro pessoa física documento OR imposto predial territorial urbano imóvel"},
		{"sigla dentro de palavra não casa", "crasso", "crasso"},
		{"aspas preservam a sintaxe", `"cras" centro`, `"cras" centro`},
		{"OR explícito preserva a sintaxe", "cras OR creas", "cras OR creas"},
		{"exclusão preserva a sintaxe", "cras -creas", "cras -creas"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Expand(tc.query); got != tc.want {
				t.Fatalf("Expand(%q) = %q, esperado %q", tc.query, got, tc.want)
			}
		})
	}
}

func TestExpand_VaccineRulesAreExclusive(t *testing.T) {
	human := Expand("vacina gripe")
	if !strings.Contains(human, "vacinação imunização") || strings.Contains(human, "animal pet") {
		t.Fatalf("vacina humana expandida errado: %q", human)
	}
	pet := Expand("vacina gato")
	if strings.Contains(pet, "imunização") || !strings.Contains(pet, "vacinação animal pet") {
		t.Fatalf("vacina animal expandida errado: %q", pet)
	}
}

func TestExpand_OriginalQueryIsFirstBranch(t *testing.T) {
	got := Expand("segunda via rg")
	if !strings.HasPrefix(got, "segunda via rg OR ") {
		t.Fatalf("a busca original deve ser o primeiro ramo: %q", got)
	}
}

func TestTokenizeForMatch(t *testing.T) {
	got := tokenizeForMatch("Habite-se: CadÚnico, 2ª via!")
	want := []string{"habite", "se", "cadunico", "2ª", "via"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokenizeForMatch = %q, esperado %q", got, want)
	}
	if tokenizeForMatch("  -- ") != nil {
		t.Fatal("texto sem letras deve gerar nenhum token")
	}
}

func TestContainsTokenSequence(t *testing.T) {
	tokens := []string{"bilhete", "unico", "carioca"}
	cases := []struct {
		seq  []string
		want bool
	}{
		{[]string{"bilhete", "unico"}, true},
		{[]string{"unico", "carioca"}, true},
		{[]string{"carioca"}, true},
		{[]string{"unico", "bilhete"}, false},
		{nil, false},
		{[]string{"bilhete", "unico", "carioca", "rio"}, false},
	}
	for _, tc := range cases {
		if got := containsTokenSequence(tokens, tc.seq); got != tc.want {
			t.Errorf("containsTokenSequence(%q) = %v, esperado %v", tc.seq, got, tc.want)
		}
	}
	if matchesPattern(tokens, nil) {
		t.Fatal("padrão vazio não deve casar")
	}
}
