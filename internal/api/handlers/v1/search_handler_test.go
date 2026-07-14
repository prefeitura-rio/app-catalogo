package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/repository"
	"github.com/prefeitura-rio/app-catalogo/internal/services"
)

type deadlineSearchRepository struct{}

type oversizedSearchRepository struct{}

func (deadlineSearchRepository) Search(
	context.Context,
	*models.SearchRequest,
) ([]*repository.SearchResult, int, error) {
	return nil, 0, context.DeadlineExceeded
}

func (deadlineSearchRepository) SearchRanked(
	context.Context,
	*models.SearchRequest,
	repository.RankedSearchOptions,
) ([]*repository.SearchResult, int, error) {
	return nil, 0, context.DeadlineExceeded
}

func (oversizedSearchRepository) Search(
	context.Context,
	*models.SearchRequest,
) ([]*repository.SearchResult, int, error) {
	searchResults := make([]*repository.SearchResult, models.MaxSearchPerPage)
	for resultIndex := range searchResults {
		maximumURL := "https://example.test/" + strings.Repeat("u", models.MaximumCatalogURLRunes-len("https://example.test/"))
		searchResults[resultIndex] = &repository.SearchResult{Item: &models.CatalogItem{
			ExternalID: fmt.Sprintf("oversized-%03d", resultIndex),
			Source:     models.SourceTypesense,
			Type:       models.TypeService,
			Title:      strings.Repeat("t", models.MaximumCatalogTitleRunes),
			ShortDesc:  strings.Repeat("d", models.MaximumCatalogTextRunes),
			URL:        maximumURL,
			ImageURL:   maximumURL,
			Status:     models.StatusActive,
		}}
	}
	return searchResults, len(searchResults), nil
}

func (oversizedSearchRepository) SearchRanked(
	context.Context,
	*models.SearchRequest,
	repository.RankedSearchOptions,
) ([]*repository.SearchResult, int, error) {
	return nil, 0, errors.New("ranked search is not expected")
}

func TestSearchRejectsInvalidQueryBeforeCallingService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tooLongQuery := strings.Repeat("á", models.MaxSearchQueryRunes+1)
	testCases := []struct {
		name       string
		queryValue url.Values
	}{
		{name: "page is not an integer", queryValue: url.Values{"page": {"abc"}}},
		{name: "page is zero", queryValue: url.Values{"page": {"0"}}},
		{name: "page is negative", queryValue: url.Values{"page": {"-1"}}},
		{name: "page exceeds limit", queryValue: url.Values{"page": {"1001"}}},
		{name: "per page is not an integer", queryValue: url.Values{"per_page": {"abc"}}},
		{name: "per page is zero", queryValue: url.Values{"per_page": {"0"}}},
		{name: "per page exceeds limit", queryValue: url.Values{"per_page": {"101"}}},
		{name: "query exceeds rune limit", queryValue: url.Values{"q": {tooLongQuery}}},
		{name: "unknown item type", queryValue: url.Values{"types": {"service,event"}}},
		{name: "unknown modalidade", queryValue: url.Values{"modalidade": {"teletransporte"}}},
		{name: "unknown turno", queryValue: url.Values{"turno": {"madrugada"}}},
		{name: "unknown hiring regime", queryValue: url.Values{"regime_contratacao": {"freelance"}}},
		{name: "unknown work model", queryValue: url.Values{"modelo_trabalho": {"itinerante"}}},
		{name: "unsupported salary range", queryValue: url.Values{"faixa_salarial": {"2-4sm"}}},
		{name: "unsupported free-course filter", queryValue: url.Values{"gratuito": {"true"}}},
		{name: "unknown service channel", queryValue: url.Values{"canal_atendimento": {"fax"}}},
		{name: "empty boolean", queryValue: url.Values{"gratuito": {""}}},
		{name: "numeric boolean", queryValue: url.Values{"gratuito": {"1"}}},
		{name: "unknown boolean", queryValue: url.Values{"pcd": {"sim"}}},
	}

	handler := NewSearchHandler(nil)
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			context, recorder := searchTestContext(testCase.queryValue)

			handler.Search(context)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), `"error"`) {
				t.Fatalf("response does not contain an error: %s", recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), `"log_id":"test-request-id"`) {
				t.Fatalf("response does not contain the correlation ID: %s", recorder.Body.String())
			}
		})
	}
}

func TestParseSearchQueryCanonicalizesTypesAndFilters(t *testing.T) {
	queryValues := url.Values{
		"q":                  {"  curso   gratuito  "},
		"types":              {"SERVICE, course", "job", "service"},
		"page":               {"2"},
		"per_page":           {"25"},
		"modalidade":         {"HÍBRIDO"},
		"bairro":             {"  Rio   Comprido "},
		"orgao":              {" Secretaria   Municipal "},
		"turno":              {"NOTURNO"},
		"regime_contratacao": {"CLT"},
		"modelo_trabalho":    {"REMOTO"},
		"pcd":                {"TRUE"},
		"canal_atendimento":  {"TELEFONE"},
		"tema":               {" Saúde   pública "},
		"segmento":           {" Comércio   local "},
	}
	context, _ := searchTestContext(queryValues)

	request, err := parseSearchQuery(context)
	if err != nil {
		t.Fatalf("parseSearchQuery() error = %v", err)
	}

	if request.Q != "curso gratuito" {
		t.Errorf("Q = %q", request.Q)
	}
	if request.ExpandedQ != "" {
		t.Errorf("ExpandedQ must remain separate and empty at the HTTP boundary, got %q", request.ExpandedQ)
	}
	wantTypes := []models.ItemType{models.TypeCourse, models.TypeJob, models.TypeService}
	if !reflect.DeepEqual(request.Types, wantTypes) {
		t.Errorf("Types = %v, want %v", request.Types, wantTypes)
	}
	if request.Page != 2 || request.PerPage != 25 {
		t.Errorf("pagination = (%d, %d)", request.Page, request.PerPage)
	}
	if request.Filters.Modalidade != "hibrido" || request.Filters.RegimeContratacao != "clt" {
		t.Errorf("enum filters were not canonicalized: %+v", request.Filters)
	}
	if request.Filters.Bairro != "Rio Comprido" || request.Filters.Segmento != "Comércio local" {
		t.Errorf("free-text filters were not canonicalized: %+v", request.Filters)
	}
	if request.Filters.PCD == nil || !*request.Filters.PCD {
		t.Errorf("pcd = %v, want explicit true", request.Filters.PCD)
	}
}

func TestParseSearchQueryDistinguishesAbsentBooleanFromFalse(t *testing.T) {
	absentContext, _ := searchTestContext(nil)
	absentRequest, err := parseSearchQuery(absentContext)
	if err != nil {
		t.Fatalf("parse absent boolean: %v", err)
	}
	if absentRequest.Filters.Gratuito != nil || absentRequest.Filters.PCD != nil {
		t.Fatalf("absent booleans must be nil: %+v", absentRequest.Filters)
	}

	falseContext, _ := searchTestContext(url.Values{"pcd": {"false"}})
	falseRequest, err := parseSearchQuery(falseContext)
	if err != nil {
		t.Fatalf("parse false boolean: %v", err)
	}
	if falseRequest.Filters.PCD == nil || *falseRequest.Filters.PCD {
		t.Fatalf("pcd must be explicit false: %v", falseRequest.Filters.PCD)
	}
}

func TestParseSearchJSONCanonicalizesFlatBody(t *testing.T) {
	page := 2
	perPage := 25
	pcd := true
	requestBody := models.SearchRequestBody{
		Q:                "  curso   gratuito  ",
		Types:            []models.ItemType{models.TypeJob, models.TypeService, models.TypeJob},
		Page:             &page,
		PerPage:          &perPage,
		Modalidade:       "HÍBRIDO",
		Bairro:           "  Rio   Comprido ",
		PCD:              &pcd,
		CanalAtendimento: "TELEFONE",
	}
	requestContext, _ := searchJSONTestContext(t, requestBody)

	searchRequest, parseError := parseSearchJSON(requestContext)
	if parseError != nil {
		t.Fatalf("parseSearchJSON() error = %v", parseError)
	}
	if searchRequest.Q != "curso gratuito" || searchRequest.Page != page || searchRequest.PerPage != perPage {
		t.Fatalf("parsed request = %+v", searchRequest)
	}
	if !reflect.DeepEqual(searchRequest.Types, []models.ItemType{models.TypeJob, models.TypeService}) {
		t.Fatalf("types = %v", searchRequest.Types)
	}
	if searchRequest.Filters.Modalidade != "hibrido" || searchRequest.Filters.Bairro != "Rio Comprido" {
		t.Fatalf("filters = %+v", searchRequest.Filters)
	}
}

func TestSearchJSONRejectsMalformedUnknownAndOversizedBodies(t *testing.T) {
	handler := NewSearchHandler(nil)
	testBodies := []struct {
		name       string
		body       []byte
		wantStatus int
	}{
		{name: "wrong query type", body: []byte(`{"q":true}`), wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: []byte(`{"q":"iptu","debug":true}`), wantStatus: http.StatusBadRequest},
		{name: "unsupported free-course filter", body: []byte(`{"gratuito":true}`), wantStatus: http.StatusBadRequest},
		{name: "unsupported salary-range filter", body: []byte(`{"faixa_salarial":"2-4sm"}`), wantStatus: http.StatusBadRequest},
		{name: "explicit zero page", body: []byte(`{"page":0}`), wantStatus: http.StatusBadRequest},
		{name: "explicit zero per page", body: []byte(`{"per_page":0}`), wantStatus: http.StatusBadRequest},
		{name: "multiple objects", body: []byte(`{} {}`), wantStatus: http.StatusBadRequest},
		{name: "oversized", body: bytes.Repeat([]byte(" "), maximumSearchRequestBodyBytes+1), wantStatus: http.StatusRequestEntityTooLarge},
	}

	for _, testBody := range testBodies {
		t.Run(testBody.name, func(t *testing.T) {
			requestContext, recorder := rawSearchJSONTestContext(testBody.body)

			handler.SearchJSON(requestContext)

			if recorder.Code != testBody.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, testBody.wantStatus, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), `"log_id":"test-request-id"`) {
				t.Fatalf("response does not contain correlation ID: %s", recorder.Body.String())
			}
		})
	}
}

func TestSearchJSONRequiresApplicationJSONMediaType(t *testing.T) {
	handler := NewSearchHandler(nil)
	for _, contentType := range []string{"", "text/plain", "application/problem+json", "application/json, text/plain"} {
		t.Run(contentType, func(t *testing.T) {
			requestContext, recorder := rawSearchJSONTestContextWithMediaType([]byte(`{}`), contentType)

			handler.SearchJSON(requestContext)

			if recorder.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusUnsupportedMediaType, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), `"log_id":"test-request-id"`) {
				t.Fatalf("response does not contain correlation ID: %s", recorder.Body.String())
			}
		})
	}
}

func TestParseSearchJSONAllowsMediaTypeParameters(t *testing.T) {
	requestContext, _ := rawSearchJSONTestContextWithMediaType([]byte(`{}`), "Application/JSON; Charset=UTF-8")

	if _, parseError := parseSearchJSON(requestContext); parseError != nil {
		t.Fatalf("parseSearchJSON() rejected application/json parameters: %v", parseError)
	}
}

func TestSearchRejectsResponseAboveBFFByteLimit(t *testing.T) {
	searchService := services.NewSearchService(
		oversizedSearchRepository{},
		nil,
		0,
		nil,
		nil,
		services.DefaultSearchRuntimeConfig(),
	)
	handler := NewSearchHandler(searchService)
	requestContext, recorder := rawSearchJSONTestContext([]byte(`{"per_page":100}`))

	handler.SearchJSON(requestContext)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	}
	if recorder.Body.Len() >= models.MaximumPublicSearchResponseBytes ||
		!strings.Contains(recorder.Body.String(), `"log_id":"test-request-id"`) {
		t.Fatalf("unsafe oversized response body: %s", recorder.Body.String())
	}
}

func TestSearchMapsDownstreamDeadlineToGatewayTimeout(t *testing.T) {
	searchService := services.NewSearchService(
		deadlineSearchRepository{},
		nil,
		0,
		nil,
		nil,
		services.DefaultSearchRuntimeConfig(),
	)
	handler := NewSearchHandler(searchService)
	requestContext, recorder := searchTestContext(nil)

	handler.Search(requestContext)

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusGatewayTimeout, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"log_id":"test-request-id"`) {
		t.Fatalf("response does not contain the correlation ID: %s", recorder.Body.String())
	}
}

func searchTestContext(queryValues url.Values) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("request_id", "test-request-id")
	request := httptest.NewRequest(http.MethodGet, "/api/public/search?"+queryValues.Encode(), nil)
	context.Request = request
	return context, recorder
}

func searchJSONTestContext(t *testing.T, requestBody models.SearchRequestBody) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	encodedBody, encodeError := json.Marshal(requestBody)
	if encodeError != nil {
		t.Fatalf("encode search JSON fixture: %v", encodeError)
	}
	return rawSearchJSONTestContext(encodedBody)
}

func rawSearchJSONTestContext(requestBody []byte) (*gin.Context, *httptest.ResponseRecorder) {
	return rawSearchJSONTestContextWithMediaType(requestBody, "application/json")
}

func rawSearchJSONTestContextWithMediaType(
	requestBody []byte,
	contentType string,
) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	requestContext, _ := gin.CreateTestContext(recorder)
	requestContext.Set("request_id", "test-request-id")
	requestContext.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/public/search",
		bytes.NewReader(requestBody),
	)
	if contentType != "" {
		requestContext.Request.Header.Set("Content-Type", contentType)
	}
	return requestContext, recorder
}
