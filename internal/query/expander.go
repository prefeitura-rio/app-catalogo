// Package query fornece pré-processamento de queries de busca.
package query

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// synonymRule define uma regra de expansão de query.
// Todos os tokens de Pattern devem estar presentes na query normalizada.
// Se qualquer token de AntiPatterns estiver presente, a regra não se aplica.
type synonymRule struct {
	Pattern     []string
	AntiPatterns []string
	Expansion   string
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
	{Pattern: []string{"vacina"}, AntiPatterns: []string{"animal", "cachorro", "gato", "pet"}, Expansion: "vacinação imunização"},
	{Pattern: []string{"vacina", "cachorro"}, Expansion: "vacinação animal pet"},

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

// Expand expande uma query adicionando sinônimos relevantes.
// A query original é preservada — os termos são apenas concatenados.
// Retorna a query inalterada se nenhuma regra se aplicar.
func Expand(query string) string {
	normalized := normalizeForMatch(query)

	var additions []string
	for _, rule := range synonymRules {
		if matchesPattern(normalized, rule.Pattern) &&
			!hasAntiPattern(normalized, rule.AntiPatterns) {
			additions = append(additions, rule.Expansion)
		}
	}

	if len(additions) == 0 {
		return query
	}
	return query + " " + strings.Join(additions, " ")
}

// normalizeForMatch converte para lowercase e remove acentos para comparação.
func normalizeForMatch(s string) string {
	// Normalização Unicode NFD para decompor caracteres acentuados
	t := norm.NFD.String(strings.ToLower(s))
	// Remove marcas diacríticas (categoria Mn)
	var b strings.Builder
	for _, r := range t {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func matchesPattern(normalized string, pattern []string) bool {
	for _, p := range pattern {
		if !strings.Contains(normalized, p) {
			return false
		}
	}
	return len(pattern) > 0
}

func hasAntiPattern(normalized string, antiPatterns []string) bool {
	for _, ap := range antiPatterns {
		if strings.Contains(normalized, ap) {
			return true
		}
	}
	return false
}
