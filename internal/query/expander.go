// Package query fornece pré-processamento de queries de busca.
package query

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ExpansionVersion changes whenever synonym semantics or rules change.
const ExpansionVersion = "synonyms-v2"

// synonymRule defines a deterministic query expansion rule.
// Each pattern entry is a token sequence that must occur in the query.
type synonymRule struct {
	Pattern      []string
	AntiPatterns []string
	Expansion    string
}

// synonymRules cobre termos coloquiais, siglas e abreviações comuns nos serviços
// públicos do Rio de Janeiro. A ordem importa: regras mais específicas primeiro.
var synonymRules = []synonymRule{
	// Siglas de equipamentos de assistência social
	{Pattern: []string{"cras"}, Expansion: "centro referência assistência social"},
	{Pattern: []string{"creas"}, Expansion: "centro referência especializado assistência social"},
	{Pattern: []string{"caps"}, Expansion: "centro atenção psicossocial saúde mental"},
	{Pattern: []string{"suas"}, Expansion: "sistema único assistência social"},
	{Pattern: []string{"cad unico"}, Expansion: "cadastro único benefício"},
	{Pattern: []string{"cadunico"}, Expansion: "cadastro único benefício"},
	{Pattern: []string{"cadúnico"}, Expansion: "cadastro único benefício"},

	// Programas de renda
	{Pattern: []string{"bolsa familia"}, Expansion: "transferência renda benefício assistência"},
	{Pattern: []string{"bpc"}, AntiPatterns: []string{"banco"}, Expansion: "benefício prestação continuada loas assistência"},
	{Pattern: []string{"loas"}, Expansion: "benefício prestação continuada assistência social"},
	{Pattern: []string{"auxilio brasil"}, Expansion: "transferência renda benefício social"},

	// Saúde
	{Pattern: []string{"ubs"}, Expansion: "unidade básica saúde posto médico"},
	{Pattern: []string{"upa"}, Expansion: "unidade pronto atendimento urgência emergência"},
	{Pattern: []string{"sus"}, Expansion: "sistema único saúde cartão"},
	{Pattern: []string{"sms"}, AntiPatterns: []string{"mensagem", "telefone"}, Expansion: "secretaria saúde"},
	{Pattern: []string{"vacina", "cachorro"}, Expansion: "vacinação animal pet"},
	{Pattern: []string{"vacina", "gato"}, Expansion: "vacinação animal pet"},
	{Pattern: []string{"vacina", "animal"}, Expansion: "vacinação animal pet"},
	{Pattern: []string{"vacina", "pet"}, Expansion: "vacinação animal pet"},
	{Pattern: []string{"vacina"}, AntiPatterns: []string{"animal", "cachorro", "gato", "pet"}, Expansion: "vacinação imunização"},

	// Documentação
	{Pattern: []string{"rg"}, AntiPatterns: []string{"endereço", "bairro"}, Expansion: "registro geral identidade documento"},
	{Pattern: []string{"cpf"}, Expansion: "cadastro pessoa física documento"},
	{Pattern: []string{"ctps"}, Expansion: "carteira trabalho previdência social emprego"},
	{Pattern: []string{"segunda via"}, Expansion: "reemissão documento substituição"},

	// Habitação e urbanismo
	{Pattern: []string{"iptu"}, Expansion: "imposto predial territorial urbano imóvel"},
	{Pattern: []string{"habite-se"}, Expansion: "habite se alvará construção regularização"},
	{Pattern: []string{"itbi"}, Expansion: "imposto transmissão bens imóveis transferência"},

	// Transporte e mobilidade
	{Pattern: []string{"riocard"}, Expansion: "cartão passagem transporte público ônibus"},
	{Pattern: []string{"bilhete unico"}, Expansion: "passagem ônibus metrô transporte"},
	{Pattern: []string{"cartao idoso"}, Expansion: "passe livre gratuidade idoso transporte"},

	// Trabalho e emprego
	{Pattern: []string{"sine"}, Expansion: "sistema nacional emprego vaga trabalho"},
	{Pattern: []string{"mei"}, AntiPatterns: []string{"meio"}, Expansion: "microempreendedor individual empresa"},
	{Pattern: []string{"cnpj"}, Expansion: "cadastro nacional pessoa jurídica empresa"},
	{Pattern: []string{"seguro desemprego"}, Expansion: "benefício desempregado seguro"},

	// Educação
	{Pattern: []string{"creche"}, Expansion: "educação infantil berçário criança"},
	{Pattern: []string{"ciep"}, Expansion: "escola pública educação ensino"},

	// Animais
	{Pattern: []string{"sisbicho"}, Expansion: "cadastro animal pet registro"},

	// Termos coloquiais / buscas frequentes
	{Pattern: []string{"nota carioca"}, Expansion: "nota fiscal eletrônica imposto"},
	{Pattern: []string{"alvara"}, Expansion: "alvará funcionamento licença comercial"},
	{Pattern: []string{"esic"}, Expansion: "acesso informação transparência ouvidoria"},
	{Pattern: []string{"156"}, Expansion: "central atendimento prefeitura solicitação"},
}

// Expand preserves the original websearch expression as one branch and appends
// every matching synonym expansion as an OR branch. Unquoted terms inside an
// expansion may still combine with AND, but can never become mandatory for the
// original query branch.
func Expand(query string) string {
	canonicalQuery := strings.Join(strings.Fields(strings.TrimSpace(query)), " ")
	if hasExplicitWebsearchSyntax(canonicalQuery) {
		return canonicalQuery
	}
	queryTokens := tokenizeForMatch(canonicalQuery)
	if len(queryTokens) == 0 {
		return canonicalQuery
	}

	expansions := make([]string, 0)
	seenExpansions := make(map[string]struct{})
	for _, rule := range synonymRules {
		if !matchesPattern(queryTokens, rule.Pattern) || hasAntiPattern(queryTokens, rule.AntiPatterns) {
			continue
		}
		if _, duplicate := seenExpansions[rule.Expansion]; duplicate {
			continue
		}
		seenExpansions[rule.Expansion] = struct{}{}
		expansions = append(expansions, rule.Expansion)
	}

	if len(expansions) == 0 {
		return canonicalQuery
	}
	return canonicalQuery + " OR " + strings.Join(expansions, " OR ")
}

func hasExplicitWebsearchSyntax(searchQuery string) bool {
	if strings.Contains(searchQuery, `"`) {
		return true
	}
	for _, queryField := range strings.Fields(searchQuery) {
		if queryField == "OR" || strings.HasPrefix(queryField, "-") {
			return true
		}
	}
	return false
}

func tokenizeForMatch(searchText string) []string {
	normalizedText := norm.NFD.String(strings.ToLower(searchText))
	var tokens []string
	var currentToken strings.Builder

	for _, character := range normalizedText {
		if unicode.Is(unicode.Mn, character) {
			continue
		}
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			currentToken.WriteRune(character)
			continue
		}
		if currentToken.Len() > 0 {
			tokens = append(tokens, currentToken.String())
			currentToken.Reset()
		}
	}
	if currentToken.Len() > 0 {
		tokens = append(tokens, currentToken.String())
	}
	return tokens
}

func matchesPattern(queryTokens []string, pattern []string) bool {
	if len(pattern) == 0 {
		return false
	}
	for _, patternEntry := range pattern {
		if !containsTokenSequence(queryTokens, tokenizeForMatch(patternEntry)) {
			return false
		}
	}
	return true
}

func hasAntiPattern(queryTokens []string, antiPatterns []string) bool {
	for _, antiPattern := range antiPatterns {
		if containsTokenSequence(queryTokens, tokenizeForMatch(antiPattern)) {
			return true
		}
	}
	return false
}

func containsTokenSequence(tokens, sequence []string) bool {
	if len(sequence) == 0 || len(sequence) > len(tokens) {
		return false
	}
	for startIndex := 0; startIndex <= len(tokens)-len(sequence); startIndex++ {
		matches := true
		for sequenceIndex, sequenceToken := range sequence {
			if tokens[startIndex+sequenceIndex] != sequenceToken {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}
