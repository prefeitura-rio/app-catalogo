package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
)

// Testes de contrato das integrações com o app-go-api e o app-rmi (DEV-331).
//
// Cada fixture em testdata/contract é uma resposta real anonimizada (ou, no RMI,
// sintética no formato real) de um endpoint consumido pela catalogo. O teste
// confere três coisas contra o docs/openapi-v3.json do serviço de origem:
//  1. a fixture é válida pelo schema da resposta e pelo schema do item;
//  2. todo campo usado pelo sync está declarado no schema do item;
//  3. o cliente da catalogo decodifica a fixture sem perder nenhum desses campos.
//
// Por padrão usa o snapshot versionado em testdata/openapi; as variáveis
// CONTRACT_OPENAPI_APP_GO_API e CONTRACT_OPENAPI_APP_RMI apontam para um
// openapi-v3.json baixado na hora (workflow .github/workflows/contract.yml).

type contractService struct {
	name     string
	envVar   string
	snapshot string
}

var (
	appGoAPIContract = contractService{
		name:     "app-go-api",
		envVar:   "CONTRACT_OPENAPI_APP_GO_API",
		snapshot: filepath.Join("testdata", "openapi", "app-go-api.openapi-v3.json"),
	}
	rmiContract = contractService{
		name:     "app-rmi",
		envVar:   "CONTRACT_OPENAPI_APP_RMI",
		snapshot: filepath.Join("testdata", "openapi", "app-rmi.openapi-v3.json"),
	}
)

type contractEndpoint struct {
	name    string
	service contractService
	fixture string
	// path é a chave do endpoint em paths do OpenAPI.
	path string
	// itemsAt leva do corpo da resposta à lista de itens; vazio quando o corpo é o item.
	itemsAt    []string
	itemSchema string
	// usedFields são os campos do item lidos pelo sync, em notação de ponto;
	// "[]" percorre uma lista ("categorias[].nome").
	usedFields []string
	// undocumented são campos usados que o publicador devolve mas não declara
	// no OpenAPI: a fixture precisa trazê-los, o schema não.
	undocumented []string
}

var (
	coursesContract = contractEndpoint{
		name:       "app-go-api cursos",
		service:    appGoAPIContract,
		fixture:    "appgoapi_courses.json",
		path:       "/api/public/courses",
		itemsAt:    []string{"data", "courses"},
		itemSchema: "models.Curso",
		usedFields: []string{
			"id", "title", "description", "target_audience", "organization",
			"modalidade", "turno", "theme", "categorias[].nome", "link_inscricao",
			"cover_image", "has_certificate", "is_visible", "status",
			"data_limite_inscricoes", "updated_at",
		},
		undocumented: []string{"categorias[].nome"},
	}
	jobsContract = contractEndpoint{
		name:       "app-go-api vagas",
		service:    appGoAPIContract,
		fixture:    "appgoapi_jobs.json",
		path:       "/api/public/empregabilidade/vagas",
		itemsAt:    []string{"data"},
		itemSchema: "empregabilidade.Vaga",
		usedFields: []string{
			"id", "titulo", "descricao", "bairro", "acessibilidade_pcd", "status",
			"contratante.nome_fantasia", "contratante.url_logo",
			"regime_contratacao.descricao", "modelo_trabalho.descricao",
			"orgao_parceiro.name", "updated_at",
		},
	}
	meiContract = contractEndpoint{
		name:       "app-go-api MEI",
		service:    appGoAPIContract,
		fixture:    "appgoapi_mei.json",
		path:       "/api/public/oportunidades-mei",
		itemsAt:    []string{"data"},
		itemSchema: "models.OportunidadeMEI",
		usedFields: []string{
			"id", "titulo", "descricao_servico", "orgao_id", "cnae_ids", "bairro",
			"forma_pagamento", "data_expiracao", "cover_image", "status", "updated_at",
		},
	}
	citizenContract = contractEndpoint{
		name:       "app-rmi cidadão",
		service:    rmiContract,
		fixture:    "rmi_citizen.json",
		path:       "/citizen/{cpf}",
		itemSchema: "models.Citizen",
		usedFields: []string{
			"escolaridade", "renda_familiar", "deficiencia", "raca", "genero",
			"nascimento.data", "endereco.principal.bairro",
			"endereco.principal.municipio", "endereco.principal.estado",
			"endereco.principal.cep",
		},
	}
	allContracts = []contractEndpoint{coursesContract, jobsContract, meiContract, citizenContract}
)

func contractSpecPath(service contractService) string {
	if path := strings.TrimSpace(os.Getenv(service.envVar)); path != "" {
		return path
	}
	return service.snapshot
}

func loadContractSpec(t *testing.T, service contractService) *openapi3.T {
	t.Helper()
	path := contractSpecPath(service)
	doc, err := openapi3.NewLoader().LoadFromFile(path)
	if err != nil {
		t.Fatalf("%s: falha ao carregar o OpenAPI %s: %v", service.name, path, err)
	}
	return doc
}

func readContractFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "contract", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return body
}

// withoutNulls remove as chaves com null: o swag não marca campos como
// nullable, e a catalogo trata null como campo ausente.
func withoutNulls(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if child != nil {
				out[key] = withoutNulls(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, child := range typed {
			out[index] = withoutNulls(child)
		}
		return out
	default:
		return value
	}
}

func contractItems(body any, itemsAt []string) ([]map[string]any, error) {
	current := body
	for _, key := range itemsAt {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("resposta sem o objeto que leva a %q", key)
		}
		current = object[key]
	}
	var raw []any
	if len(itemsAt) == 0 {
		raw = []any{current}
	} else {
		list, ok := current.([]any)
		if !ok {
			return nil, fmt.Errorf("resposta sem a lista de itens em %s", strings.Join(itemsAt, "."))
		}
		raw = list
	}
	if len(raw) == 0 {
		return nil, errors.New("fixture sem itens")
	}
	items := make([]map[string]any, 0, len(raw))
	for index, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("item %d não é um objeto", index)
		}
		items = append(items, item)
	}
	return items, nil
}

func schemaProperty(schema *openapi3.Schema, name string) *openapi3.SchemaRef {
	if schema == nil {
		return nil
	}
	if property, ok := schema.Properties[name]; ok && property != nil {
		return property
	}
	for _, member := range schema.AllOf {
		if member == nil {
			continue
		}
		if property := schemaProperty(member.Value, name); property != nil {
			return property
		}
	}
	return nil
}

// schemaDeclaresField diz se o campo (notação de usedFields) está declarado no schema.
func schemaDeclaresField(schema *openapi3.Schema, field string) bool {
	current := schema
	for _, segment := range strings.Split(field, ".") {
		name, isList := strings.CutSuffix(segment, "[]")
		property := schemaProperty(current, name)
		if property == nil || property.Value == nil {
			return false
		}
		current = property.Value
		if isList {
			if current.Items == nil || current.Items.Value == nil {
				return false
			}
			current = current.Items.Value
		}
	}
	return true
}

// fieldValues devolve os valores não nulos do campo no item, percorrendo listas.
func fieldValues(value any, segments []string) []any {
	if len(segments) == 0 {
		if value == nil {
			return nil
		}
		return []any{value}
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	name, isList := strings.CutSuffix(segments[0], "[]")
	child := object[name]
	if !isList {
		return fieldValues(child, segments[1:])
	}
	list, ok := child.([]any)
	if !ok {
		return nil
	}
	var values []any
	for _, element := range list {
		values = append(values, fieldValues(element, segments[1:])...)
	}
	return values
}

// checkContract valida a fixture contra o OpenAPI e devolve todas as violações.
func checkContract(doc *openapi3.T, endpoint contractEndpoint, body []byte) error {
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("fixture não é JSON: %w", err)
	}
	payload := withoutNulls(decoded)

	pathItem := doc.Paths.Value(endpoint.path)
	if pathItem == nil || pathItem.Get == nil {
		return fmt.Errorf("GET %s não existe no OpenAPI", endpoint.path)
	}
	response := pathItem.Get.Responses.Status(http.StatusOK)
	if response == nil || response.Value == nil {
		return fmt.Errorf("GET %s sem resposta 200 no OpenAPI", endpoint.path)
	}
	media := response.Value.Content.Get("application/json")
	if media == nil || media.Schema == nil || media.Schema.Value == nil {
		return fmt.Errorf("GET %s sem schema application/json na resposta 200", endpoint.path)
	}

	var problems []error
	if err := media.Schema.Value.VisitJSON(payload, openapi3.MultiErrors(), openapi3.EnableFormatValidation()); err != nil {
		problems = append(problems, fmt.Errorf("resposta fora do schema de GET %s: %w", endpoint.path, err))
	}

	itemSchema := doc.Components.Schemas[endpoint.itemSchema]
	if itemSchema == nil || itemSchema.Value == nil {
		return errors.Join(append(problems, fmt.Errorf("schema %s não existe no OpenAPI", endpoint.itemSchema))...)
	}
	for _, field := range endpoint.usedFields {
		if slices.Contains(endpoint.undocumented, field) {
			continue
		}
		if !schemaDeclaresField(itemSchema.Value, field) {
			problems = append(problems, fmt.Errorf("campo %q usado pelo sync não está declarado em %s", field, endpoint.itemSchema))
		}
	}

	items, err := contractItems(payload, endpoint.itemsAt)
	if err != nil {
		return errors.Join(append(problems, err)...)
	}
	for index, item := range items {
		if err := itemSchema.Value.VisitJSON(item, openapi3.MultiErrors(), openapi3.EnableFormatValidation()); err != nil {
			problems = append(problems, fmt.Errorf("item %d fora do schema %s: %w", index, endpoint.itemSchema, err))
		}
		for _, field := range endpoint.usedFields {
			if len(fieldValues(item, strings.Split(field, "."))) == 0 {
				problems = append(problems, fmt.Errorf("item %d sem o campo %q usado pelo sync", index, field))
			}
		}
	}
	return errors.Join(problems...)
}

func TestContrato_FixturesValidasPeloOpenAPI(t *testing.T) {
	t.Parallel()

	specs := map[string]*openapi3.T{
		appGoAPIContract.name: loadContractSpec(t, appGoAPIContract),
		rmiContract.name:      loadContractSpec(t, rmiContract),
	}
	for _, endpoint := range allContracts {
		t.Run(endpoint.name, func(t *testing.T) {
			t.Parallel()
			body := readContractFixture(t, endpoint.fixture)
			if err := checkContract(specs[endpoint.service.name], endpoint, body); err != nil {
				t.Fatalf("contrato de %s (%s) quebrado:\n%v", endpoint.name, contractSpecPath(endpoint.service), err)
			}
		})
	}
}

type fieldKind int

const (
	fieldText fieldKind = iota
	fieldTimestamp
	fieldDate
)

type fieldExtractor[T any] struct {
	kind fieldKind
	get  func(T) string
}

func text[T any](get func(T) string) fieldExtractor[T] {
	return fieldExtractor[T]{kind: fieldText, get: get}
}

func timestamp[T any](get func(T) *time.Time) fieldExtractor[T] {
	return fieldExtractor[T]{kind: fieldTimestamp, get: func(item T) string {
		value := get(item)
		if value == nil || value.IsZero() {
			return ""
		}
		return value.UTC().Format(time.RFC3339Nano)
	}}
}

func normalizeRaw(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	case []any:
		parts := make([]string, len(typed))
		for index, element := range typed {
			parts[index] = normalizeRaw(element)
		}
		return strings.Join(parts, ",")
	default:
		encoded, _ := json.Marshal(typed)
		return string(encoded)
	}
}

func expectedField(raw []any, kind fieldKind) string {
	parts := make([]string, len(raw))
	for index, value := range raw {
		parts[index] = normalizeRaw(value)
	}
	joined := strings.Join(parts, ",")
	switch kind {
	case fieldTimestamp:
		parsed, err := time.Parse(time.RFC3339Nano, joined)
		if err != nil {
			return "data inválida: " + joined
		}
		return parsed.UTC().Format(time.RFC3339Nano)
	case fieldDate:
		if len(joined) < len(time.DateOnly) {
			return "data inválida: " + joined
		}
		return joined[:len(time.DateOnly)]
	default:
		return joined
	}
}

// decodedFieldMismatches compara cada campo usado pelo sync entre a fixture e o
// item decodificado pelo cliente; devolve as divergências.
func decodedFieldMismatches[T any](endpoint contractEndpoint, body []byte, decoded []T, extractors map[string]fieldExtractor[T]) []string {
	var mismatches []string
	checked := make([]string, 0, len(extractors))
	for field := range extractors {
		checked = append(checked, field)
	}
	sort.Strings(checked)
	used := slices.Clone(endpoint.usedFields)
	sort.Strings(used)
	if !slices.Equal(checked, used) {
		mismatches = append(mismatches, fmt.Sprintf("campos conferidos %v diferentes dos usados pelo sync %v", checked, used))
	}

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return append(mismatches, "fixture não é JSON: "+err.Error())
	}
	items, err := contractItems(withoutNulls(payload), endpoint.itemsAt)
	if err != nil {
		return append(mismatches, err.Error())
	}
	if len(items) != len(decoded) {
		return append(mismatches, fmt.Sprintf("fixture com %d itens, cliente decodificou %d", len(items), len(decoded)))
	}
	for index, item := range items {
		for _, field := range checked {
			extractor := extractors[field]
			raw := fieldValues(item, strings.Split(field, "."))
			want := expectedField(raw, extractor.kind)
			got := extractor.get(decoded[index])
			if len(raw) == 0 || got != want {
				mismatches = append(mismatches, fmt.Sprintf("item %d campo %q: fixture=%q decodificado=%q", index, field, want, got))
			}
		}
	}
	return mismatches
}

var courseExtractors = map[string]fieldExtractor[Course]{
	"id":                     text(func(c Course) string { return string(c.ID) }),
	"title":                  text(func(c Course) string { return c.Title }),
	"description":            text(func(c Course) string { return c.Description }),
	"target_audience":        text(func(c Course) string { return c.TargetAudience }),
	"organization":           text(func(c Course) string { return c.Organization }),
	"modalidade":             text(func(c Course) string { return c.Modalidade }),
	"turno":                  text(func(c Course) string { return c.Turno }),
	"theme":                  text(func(c Course) string { return c.Theme }),
	"link_inscricao":         text(func(c Course) string { return c.URL }),
	"cover_image":            text(func(c Course) string { return c.ImageURL }),
	"has_certificate":        text(func(c Course) string { return strconv.FormatBool(c.HasCertificate) }),
	"is_visible":             text(func(c Course) string { return strconv.FormatBool(c.IsVisible) }),
	"status":                 text(func(c Course) string { return c.Status }),
	"data_limite_inscricoes": timestamp(func(c Course) *time.Time { return c.DataLimiteInscr }),
	"updated_at":             timestamp(func(c Course) *time.Time { return &c.UpdatedAt }),
	"categorias[].nome": text(func(c Course) string {
		names := make([]string, len(c.Categorias))
		for index, categoria := range c.Categorias {
			names[index] = categoria.Nome
		}
		return strings.Join(names, ",")
	}),
}

var jobExtractors = map[string]fieldExtractor[Job]{
	"id":                           text(func(j Job) string { return j.ID }),
	"titulo":                       text(func(j Job) string { return j.Title }),
	"descricao":                    text(func(j Job) string { return j.Description }),
	"bairro":                       text(func(j Job) string { return j.Bairro }),
	"acessibilidade_pcd":           text(func(j Job) string { return j.AcessibilidadePCD }),
	"status":                       text(func(j Job) string { return j.Status }),
	"contratante.nome_fantasia":    text(func(j Job) string { return j.Contratante.NomeFantasia }),
	"contratante.url_logo":         text(func(j Job) string { return j.Contratante.URLLogo }),
	"regime_contratacao.descricao": text(func(j Job) string { return j.RegimeContratacao.Descricao }),
	"modelo_trabalho.descricao":    text(func(j Job) string { return j.ModeloTrabalho.Descricao }),
	"orgao_parceiro.name": text(func(j Job) string {
		if j.OrgaoParceiro == nil {
			return ""
		}
		return j.OrgaoParceiro.Name
	}),
	"updated_at": timestamp(func(j Job) *time.Time { return &j.UpdatedAt }),
}

var meiExtractors = map[string]fieldExtractor[MEIOpportunity]{
	"id":                text(func(m MEIOpportunity) string { return string(m.ID) }),
	"titulo":            text(func(m MEIOpportunity) string { return m.Title }),
	"descricao_servico": text(func(m MEIOpportunity) string { return m.Description }),
	"orgao_id":          text(func(m MEIOpportunity) string { return m.OrgaoID }),
	"cnae_ids":          text(func(m MEIOpportunity) string { return strings.Join(m.CNAEIDs, ",") }),
	"bairro":            text(func(m MEIOpportunity) string { return m.Bairro }),
	"forma_pagamento":   text(func(m MEIOpportunity) string { return m.FormaPagamento }),
	"data_expiracao":    timestamp(func(m MEIOpportunity) *time.Time { return m.DataExpiracao }),
	"cover_image":       text(func(m MEIOpportunity) string { return m.ImageURL }),
	"status":            text(func(m MEIOpportunity) string { return m.Status }),
	"updated_at":        timestamp(func(m MEIOpportunity) *time.Time { return &m.UpdatedAt }),
}

var citizenExtractors = map[string]fieldExtractor[CitizenData]{
	"escolaridade":                 text(func(c CitizenData) string { return c.Escolaridade }),
	"renda_familiar":               text(func(c CitizenData) string { return c.RendaFamiliar }),
	"deficiencia":                  text(func(c CitizenData) string { return c.Deficiencia }),
	"raca":                         text(func(c CitizenData) string { return c.Etnia }),
	"genero":                       text(func(c CitizenData) string { return c.Genero }),
	"nascimento.data":              {kind: fieldDate, get: func(c CitizenData) string { return c.BirthDate }},
	"endereco.principal.bairro":    text(func(c CitizenData) string { return c.Address.Bairro }),
	"endereco.principal.municipio": text(func(c CitizenData) string { return c.Address.Cidade }),
	"endereco.principal.estado":    text(func(c CitizenData) string { return c.Address.Estado }),
	"endereco.principal.cep":       text(func(c CitizenData) string { return c.Address.CEP }),
}

func serveFixture(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

func decodeCoursesFixture(t *testing.T, body []byte) ([]Course, int) {
	t.Helper()
	client := newTestAppGoAPIClient(t, withKeycloakToken(serveFixture(body)))
	courses, total, err := client.GetCourses(context.Background(), 1, time.Time{})
	if err != nil {
		t.Fatalf("GetCourses: %v", err)
	}
	return courses, total
}

func decodeJobsFixture(t *testing.T, body []byte) ([]Job, int) {
	t.Helper()
	client := newTestAppGoAPIClient(t, withKeycloakToken(serveFixture(body)))
	jobs, total, err := client.GetJobs(context.Background(), 1, time.Time{})
	if err != nil {
		t.Fatalf("GetJobs: %v", err)
	}
	return jobs, total
}

func decodeMEIFixture(t *testing.T, body []byte) ([]MEIOpportunity, int) {
	t.Helper()
	client := newTestAppGoAPIClient(t, withKeycloakToken(serveFixture(body)))
	items, total, err := client.GetMEIOpportunities(context.Background(), 1, time.Time{})
	if err != nil {
		t.Fatalf("GetMEIOpportunities: %v", err)
	}
	return items, total
}

func decodeCitizenFixture(t *testing.T, body []byte) []CitizenData {
	t.Helper()
	client, _ := newTestRMIClient(t, serveFixture(body), fastResilience(5))
	citizen, err := client.GetCitizen(context.Background(), syntheticCPF)
	if err != nil {
		t.Fatalf("GetCitizen: %v", err)
	}
	if citizen == nil {
		t.Fatal("GetCitizen devolveu nil para uma resposta 200")
	}
	return []CitizenData{*citizen}
}

func fixtureTotal(t *testing.T, body []byte, path ...string) int {
	t.Helper()
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("fixture não é JSON: %v", err)
	}
	values := fieldValues(payload, path)
	if len(values) != 1 {
		t.Fatalf("fixture sem %s", strings.Join(path, "."))
	}
	total, ok := values[0].(float64)
	if !ok {
		t.Fatalf("%s não é número: %v", strings.Join(path, "."), values[0])
	}
	return int(total)
}

func TestContrato_ClienteDecodificaSemPerderCampos(t *testing.T) {
	t.Parallel()

	t.Run(coursesContract.name, func(t *testing.T) {
		t.Parallel()
		body := readContractFixture(t, coursesContract.fixture)
		courses, total := decodeCoursesFixture(t, body)
		if want := fixtureTotal(t, body, "data", "pagination", "total"); total != want {
			t.Errorf("total=%d, fixture=%d", total, want)
		}
		for _, mismatch := range decodedFieldMismatches(coursesContract, body, courses, courseExtractors) {
			t.Error(mismatch)
		}
	})
	t.Run(jobsContract.name, func(t *testing.T) {
		t.Parallel()
		body := readContractFixture(t, jobsContract.fixture)
		jobs, total := decodeJobsFixture(t, body)
		if want := fixtureTotal(t, body, "meta", "total"); total != want {
			t.Errorf("total=%d, fixture=%d", total, want)
		}
		for _, mismatch := range decodedFieldMismatches(jobsContract, body, jobs, jobExtractors) {
			t.Error(mismatch)
		}
	})
	t.Run(meiContract.name, func(t *testing.T) {
		t.Parallel()
		body := readContractFixture(t, meiContract.fixture)
		items, total := decodeMEIFixture(t, body)
		if want := fixtureTotal(t, body, "meta", "total"); total != want {
			t.Errorf("total=%d, fixture=%d", total, want)
		}
		for _, mismatch := range decodedFieldMismatches(meiContract, body, items, meiExtractors) {
			t.Error(mismatch)
		}
	})
	t.Run(citizenContract.name, func(t *testing.T) {
		t.Parallel()
		body := readContractFixture(t, citizenContract.fixture)
		citizens := decodeCitizenFixture(t, body)
		for _, mismatch := range decodedFieldMismatches(citizenContract, body, citizens, citizenExtractors) {
			t.Error(mismatch)
		}
	})
}

// mutateFixture aplica uma mudança ao primeiro item da fixture e devolve o JSON.
func mutateFixture(t *testing.T, endpoint contractEndpoint, mutate func(item map[string]any)) []byte {
	t.Helper()
	var payload any
	if err := json.Unmarshal(readContractFixture(t, endpoint.fixture), &payload); err != nil {
		t.Fatalf("fixture não é JSON: %v", err)
	}
	items, err := contractItems(payload, endpoint.itemsAt)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	mutate(items[0])
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return body
}

func requireContractError(t *testing.T, err error, fragment string) {
	t.Helper()
	if err == nil {
		t.Fatalf("mudança incompatível passou na validação de contrato (esperava erro com %q)", fragment)
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("erro de contrato não cita %q: %v", fragment, err)
	}
}

func requireMismatch(t *testing.T, mismatches []string, field string) {
	t.Helper()
	for _, mismatch := range mismatches {
		if strings.Contains(mismatch, fmt.Sprintf("campo %q", field)) {
			return
		}
	}
	t.Fatalf("decodificação não acusou a perda do campo %q: %v", field, mismatches)
}

func TestContrato_MudancaIncompativelFalha(t *testing.T) {
	t.Parallel()

	t.Run("campo renomeado na resposta de cursos", func(t *testing.T) {
		t.Parallel()
		body := mutateFixture(t, coursesContract, func(item map[string]any) {
			item["titulo"] = item["title"]
			delete(item, "title")
		})
		requireContractError(t, checkContract(loadContractSpec(t, appGoAPIContract), coursesContract, body), `campo "title"`)
		courses, _ := decodeCoursesFixture(t, body)
		requireMismatch(t, decodedFieldMismatches(coursesContract, body, courses, courseExtractors), "title")
	})

	t.Run("campo removido da resposta de vagas", func(t *testing.T) {
		t.Parallel()
		body := mutateFixture(t, jobsContract, func(item map[string]any) {
			delete(item["contratante"].(map[string]any), "nome_fantasia")
		})
		requireContractError(t, checkContract(loadContractSpec(t, appGoAPIContract), jobsContract, body), `campo "contratante.nome_fantasia"`)
		jobs, _ := decodeJobsFixture(t, body)
		requireMismatch(t, decodedFieldMismatches(jobsContract, body, jobs, jobExtractors), "contratante.nome_fantasia")
	})

	t.Run("tipo trocado na resposta de MEI", func(t *testing.T) {
		t.Parallel()
		body := mutateFixture(t, meiContract, func(item map[string]any) {
			item["id"] = map[string]any{"valor": 42}
		})
		requireContractError(t, checkContract(loadContractSpec(t, appGoAPIContract), meiContract, body), "models.OportunidadeMEI")
		items, _ := decodeMEIFixture(t, body)
		requireMismatch(t, decodedFieldMismatches(meiContract, body, items, meiExtractors), "id")
	})

	t.Run("endereço do RMI no formato antigo do cliente", func(t *testing.T) {
		t.Parallel()
		body := mutateFixture(t, citizenContract, func(item map[string]any) {
			principal := item["endereco"].(map[string]any)["principal"].(map[string]any)
			item["address"] = map[string]any{"neighborhood": principal["bairro"], "city": principal["municipio"]}
			delete(item, "endereco")
		})
		requireContractError(t, checkContract(loadContractSpec(t, rmiContract), citizenContract, body), `campo "endereco.principal.bairro"`)
		citizens := decodeCitizenFixture(t, body)
		requireMismatch(t, decodedFieldMismatches(citizenContract, body, citizens, citizenExtractors), "endereco.principal.bairro")
	})

	t.Run("campo usado removido do OpenAPI publicado", func(t *testing.T) {
		t.Parallel()
		doc := loadContractSpec(t, appGoAPIContract)
		delete(doc.Components.Schemas[jobsContract.itemSchema].Value.Properties, "titulo")
		requireContractError(t, checkContract(doc, jobsContract, readContractFixture(t, jobsContract.fixture)), `campo "titulo" usado pelo sync não está declarado`)
	})

	t.Run("endpoint removido do OpenAPI publicado", func(t *testing.T) {
		t.Parallel()
		doc := loadContractSpec(t, rmiContract)
		doc.Paths.Delete(citizenContract.path)
		requireContractError(t, checkContract(doc, citizenContract, readContractFixture(t, citizenContract.fixture)), "não existe no OpenAPI")
	})
}

// errorBody devolve um corpo de erro no formato que o OpenAPI declara para o status.
func errorBody(t *testing.T, doc *openapi3.T, path string, status int, fallbackSchema string, body map[string]any) []byte {
	t.Helper()
	schema := doc.Components.Schemas[fallbackSchema]
	if response := doc.Paths.Value(path).Get.Responses.Status(status); response != nil && response.Value != nil {
		if media := response.Value.Content.Get("application/json"); media != nil && media.Schema != nil {
			schema = media.Schema
		}
	}
	if schema == nil || schema.Value == nil {
		t.Fatalf("OpenAPI sem schema de erro para %d em %s", status, path)
	}
	if err := schema.Value.VisitJSON(body); err != nil {
		t.Fatalf("corpo de erro %d fora do contrato: %v", status, err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return encoded
}

func TestContrato_ErrosTratadosPeloClienteAppGoAPI(t *testing.T) {
	t.Parallel()

	doc := loadContractSpec(t, appGoAPIContract)
	for _, tc := range []struct {
		status    int
		calls     int32
		transient bool
	}{
		{status: http.StatusUnauthorized, calls: 1},
		{status: http.StatusNotFound, calls: 1},
		{status: http.StatusInternalServerError, calls: 3, transient: true},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			t.Parallel()
			body := errorBody(t, doc, coursesContract.path, tc.status, "models.ErrorResponse",
				map[string]any{"code": tc.status, "message": http.StatusText(tc.status)})
			client, calls := newResilienceTestAppGoAPIClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write(body)
			}, fastResilience(5))

			courses, total, err := client.GetCourses(context.Background(), 1, time.Time{})
			if err == nil {
				t.Fatalf("status %d deve virar erro, got courses=%v total=%d", tc.status, courses, total)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("status %d", tc.status)) {
				t.Fatalf("erro não cita o status %d: %v", tc.status, err)
			}
			if isTransient(err) != tc.transient {
				t.Fatalf("status %d: transitório=%v, want %v", tc.status, isTransient(err), tc.transient)
			}
			if calls.Load() != tc.calls {
				t.Fatalf("status %d: %d chamadas, want %d", tc.status, calls.Load(), tc.calls)
			}
			if client.CircuitState() != CircuitClosed {
				t.Fatalf("status %d não deve abrir o circuito abaixo do limite, got %s", tc.status, client.CircuitState())
			}
		})
	}
}

func TestContrato_ErrosTratadosPeloClienteRMI(t *testing.T) {
	t.Parallel()

	doc := loadContractSpec(t, rmiContract)
	for _, tc := range []struct {
		status    int
		calls     int32
		wantErr   bool
		transient bool
	}{
		{status: http.StatusUnauthorized, calls: 1, wantErr: true},
		{status: http.StatusNotFound, calls: 1},
		{status: http.StatusInternalServerError, calls: 3, wantErr: true, transient: true},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			t.Parallel()
			body := errorBody(t, doc, citizenContract.path, tc.status, "handlers.ErrorResponse",
				map[string]any{"error": http.StatusText(tc.status)})
			client, calls := newTestRMIClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write(body)
			}, fastResilience(5))

			citizen, err := client.GetCitizen(context.Background(), syntheticCPF)
			if citizen != nil {
				t.Fatalf("status %d não deve devolver cidadão: %+v", tc.status, citizen)
			}
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("status %d deve voltar (nil, nil), got %v", tc.status, err)
				}
			} else {
				if err == nil {
					t.Fatalf("status %d deve virar erro", tc.status)
				}
				if strings.Contains(err.Error(), syntheticCPF) {
					t.Fatalf("erro expõe o CPF: %v", err)
				}
				if isTransient(err) != tc.transient {
					t.Fatalf("status %d: transitório=%v, want %v", tc.status, isTransient(err), tc.transient)
				}
			}
			if calls.Load() != tc.calls {
				t.Fatalf("status %d: %d chamadas, want %d", tc.status, calls.Load(), tc.calls)
			}
		})
	}
}

func TestRMIClient_CamposAusentesOuNulosViramVazio(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
	}{
		{"só cpf", `{"cpf":"` + syntheticCPF + `"}`},
		{"objetos nulos", `{"cpf":"` + syntheticCPF + `","nascimento":null,"endereco":null,"raca":null}`},
		{"endereço sem principal", `{"cpf":"` + syntheticCPF + `","endereco":{"indicador":false,"principal":null}}`},
		{"nascimento inválido", `{"cpf":"` + syntheticCPF + `","nascimento":{"data":"15/03/1995"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			citizens := decodeCitizenFixture(t, []byte(tc.body))
			citizen := citizens[0]
			if citizen.CPF != syntheticCPF {
				t.Fatalf("CPF=%q", citizen.CPF)
			}
			if citizen.BirthDate != "" || citizen.Etnia != "" || citizen.Address != (CitizenAddress{}) {
				t.Fatalf("esperava campos vazios, got %+v", citizen)
			}
		})
	}
}
