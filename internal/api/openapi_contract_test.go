package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	v1 "github.com/prefeitura-rio/app-catalogo/internal/api/handlers/v1"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

const (
	generatedSearchRequestSchemaReference  = "#/components/schemas/models.SearchRequestBody"
	generatedSearchResponseSchemaReference = "#/components/schemas/models.SearchResponse"
	generatedItemTypeSchemaReference       = "#/components/schemas/models.ItemType"
	generatedSearchFacetsSchemaReference   = "#/components/schemas/models.SearchFacets"
	generatedSearchFacetValueReference     = "#/components/schemas/models.SearchFacetValue"
	generatedRequestIDHeaderReference      = "#/components/headers/X-Request-ID"
	generatedSyncStatusResponseReference   = "#/components/schemas/models.SyncStatusResponse"
	generatedTargetAudienceReference       = "#/components/schemas/models.TargetAudienceData"
	generatedRecommendationErrorReference  = "#/components/schemas/models.RecommendationErrorResponse"
	generatedPublicServiceDetailReference  = "#/components/schemas/models.PublicServiceDetail"
	generatedPublicServiceListReference    = "#/components/schemas/models.PublicServiceListResponse"
	generatedServiceCategoriesReference    = "#/components/schemas/models.PublicServiceCategoryResponse"
	generatedServiceSubcategoriesReference = "#/components/schemas/models.PublicServiceSubcategoryResponse"
	generatedSuggestionRequestReference    = "#/components/schemas/models.PublicSuggestionRequest"
	generatedSuggestionResponseReference   = "#/components/schemas/models.PublicSuggestionResponse"
	generatedServiceRelationsReference     = "#/components/schemas/models.PublicServiceRelationsResponse"
	generatedSummaryRequestReference       = "#/components/schemas/models.SearchSummaryRequest"
	generatedSummaryResponseReference      = "#/components/schemas/models.SearchSummaryResponse"
	generatedNonBlankPattern               = `\S`
	generatedCatalogHTTPURLPattern         = `^[Hh][Tt][Tt][Pp][Ss]?://[^/?#@\s]+($|[/?#]($|.*\S$))`
	generatedSalesForceSignaturePattern    = `^[0-9A-Fa-f]{64}$`
)

type generatedOpenAPIDocument struct {
	Paths      map[string]generatedOpenAPIPath `json:"paths"`
	Components struct {
		Schemas map[string]generatedOpenAPISchema `json:"schemas"`
		Headers map[string]generatedOpenAPIHeader `json:"headers"`
	} `json:"components"`
}

type generatedOpenAPIHeader struct {
	Description string                   `json:"description"`
	Schema      generatedOpenAPIProperty `json:"schema"`
}

type generatedOpenAPIPath struct {
	Get     generatedOpenAPIOperation `json:"get"`
	Post    generatedOpenAPIOperation `json:"post"`
	Put     generatedOpenAPIOperation `json:"put"`
	Patch   generatedOpenAPIOperation `json:"patch"`
	Delete  generatedOpenAPIOperation `json:"delete"`
	Options generatedOpenAPIOperation `json:"options"`
	Head    generatedOpenAPIOperation `json:"head"`
	Trace   generatedOpenAPIOperation `json:"trace"`
}

type generatedOpenAPIOperation struct {
	Security    []map[string][]string               `json:"security"`
	Parameters  []generatedOpenAPIParameter         `json:"parameters"`
	RequestBody generatedOpenAPIRequestBody         `json:"requestBody"`
	Responses   map[string]generatedOpenAPIResponse `json:"responses"`
}

type generatedOpenAPIParameter struct {
	Name    string                   `json:"name"`
	Style   string                   `json:"style"`
	Explode *bool                    `json:"explode"`
	Schema  generatedOpenAPIProperty `json:"schema"`
}

type generatedOpenAPIRequestBody struct {
	Required         bool                                 `json:"required"`
	Content          map[string]generatedOpenAPIMediaType `json:"content"`
	MaximumBodyBytes *int64                               `json:"x-max-body-bytes"`
}

type generatedOpenAPIResponse struct {
	Content map[string]generatedOpenAPIMediaType `json:"content"`
	Headers map[string]generatedOpenAPIProperty  `json:"headers"`
}

type generatedOpenAPIMediaType struct {
	Schema generatedOpenAPIProperty `json:"schema"`
}

type generatedOpenAPISchema struct {
	Type                         string                              `json:"type"`
	Enum                         []string                            `json:"enum"`
	Required                     []string                            `json:"required"`
	AdditionalProperties         *bool                               `json:"additionalProperties"`
	Properties                   map[string]generatedOpenAPIProperty `json:"properties"`
	MaximumJSONBytes             *int                                `json:"x-max-json-bytes"`
	MaximumSearchProjectionBytes *int                                `json:"x-max-search-projection-bytes"`
}

type generatedOpenAPIProperty struct {
	Type                 string                              `json:"type"`
	Format               string                              `json:"format"`
	Reference            string                              `json:"$ref"`
	Pattern              string                              `json:"pattern"`
	Minimum              *int                                `json:"minimum"`
	Maximum              *int                                `json:"maximum"`
	MaximumText          *int                                `json:"maxLength"`
	MinimumText          *int                                `json:"minLength"`
	MaximumItems         *int                                `json:"maxItems"`
	MinimumItems         *int                                `json:"minItems"`
	MaximumJSONBytes     *int                                `json:"x-max-json-bytes"`
	Default              *int                                `json:"default"`
	UniqueItems          bool                                `json:"uniqueItems"`
	Nullable             bool                                `json:"nullable"`
	Enum                 []string                            `json:"enum"`
	Items                *generatedOpenAPIProperty           `json:"items"`
	Required             []string                            `json:"required"`
	Properties           map[string]generatedOpenAPIProperty `json:"properties"`
	AdditionalProperties json.RawMessage                     `json:"additionalProperties"`
}

func TestGeneratedOpenAPIContract(t *testing.T) {
	contractPath := filepath.Join("..", "..", "docs", "openapi-v3.json")
	encodedContract, readError := os.ReadFile(contractPath)
	if readError != nil {
		t.Fatalf("read generated OpenAPI contract: %v", readError)
	}

	var contract generatedOpenAPIDocument
	if decodeError := json.Unmarshal(encodedContract, &contract); decodeError != nil {
		t.Fatalf("decode generated OpenAPI contract: %v", decodeError)
	}

	assertGeneratedPathParity(t, contract.Paths)
	assertGeneratedSecurityParity(t, contract.Paths)
	assertGeneratedRequestIDContract(t, contract)
	assertGeneratedMetricsContract(t, contract.Paths["/metrics"])
	assertGeneratedRecommendationOperations(t, contract.Paths, contract.Components.Schemas)
	assertGeneratedPublicServiceOperations(t, contract.Paths)
	assertGeneratedServiceIntelligenceOperations(t, contract)
	assertGeneratedSyncStatusContract(t, contract.Paths["/api/v1/admin/sync/status"], contract.Components.Schemas)
	assertGeneratedSalesForceWebhookContract(
		t,
		contract.Paths["/api/webhooks/salesforce"],
		contract.Components.Schemas["v1.sfWebhookPayload"],
	)

	for _, searchPath := range []string{"/api/public/search", "/api/v1/search"} {
		pathContract, pathExists := contract.Paths[searchPath]
		if !pathExists || len(pathContract.Get.Responses) == 0 {
			t.Fatalf("%s must preserve its compatible GET operation", searchPath)
		}
		expectedGetResponseStatuses := []string{"200", "400", "429", "500", "504"}
		expectedPostResponseStatuses := []string{"200", "400", "413", "415", "429", "500", "504"}
		if searchPath == "/api/v1/search" {
			expectedGetResponseStatuses = append(expectedGetResponseStatuses, "401")
			expectedPostResponseStatuses = append(expectedPostResponseStatuses, "401")
			slices.Sort(expectedGetResponseStatuses)
			slices.Sort(expectedPostResponseStatuses)
		}
		getResponseStatuses := sortedGeneratedOpenAPIKeys(pathContract.Get.Responses)
		if !slices.Equal(getResponseStatuses, expectedGetResponseStatuses) {
			t.Fatalf("%s GET response statuses = %v, want %v", searchPath, getResponseStatuses, expectedGetResponseStatuses)
		}
		assertGeneratedSearchGETParameters(t, searchPath, pathContract.Get.Parameters)
		requestMediaType, requestMediaTypeExists := pathContract.Post.RequestBody.Content["application/json"]
		if !requestMediaTypeExists || !pathContract.Post.RequestBody.Required {
			t.Fatalf("%s POST must require an application/json body", searchPath)
		}
		if requestMediaType.Schema.Reference != generatedSearchRequestSchemaReference {
			t.Fatalf("%s POST request schema = %q", searchPath, requestMediaType.Schema.Reference)
		}
		responseStatuses := sortedGeneratedOpenAPIKeys(pathContract.Post.Responses)
		if !slices.Equal(responseStatuses, expectedPostResponseStatuses) {
			t.Fatalf("%s POST response statuses = %v, want %v", searchPath, responseStatuses, expectedPostResponseStatuses)
		}
		successMediaType := pathContract.Post.Responses["200"].Content["application/json"]
		if successMediaType.Schema.Reference != generatedSearchResponseSchemaReference {
			t.Fatalf("%s POST response schema = %q", searchPath, successMediaType.Schema.Reference)
		}
	}

	assertGeneratedSearchRequestSchema(t, contract.Components.Schemas["models.SearchRequestBody"])
	assertGeneratedSearchResponseSchema(t, contract.Components.Schemas["models.SearchResponse"])
	assertGeneratedSearchSourceSchemas(
		t,
		contract.Components.Schemas["models.SearchSources"],
		contract.Components.Schemas["models.SearchSourceDiagnostic"],
		contract.Components.Schemas["models.SearchExternalRetrieverDescriptor"],
		contract.Components.Schemas["models.SearchSourceStatus"],
		contract.Components.Schemas["models.SearchSourceFailure"],
	)
	assertGeneratedRankerDescriptorSchema(
		t,
		contract.Components.Schemas["models.SearchRankerDescriptor"],
		contract.Components.Schemas["models.SearchRetrievalWeights"],
		contract.Components.Schemas["models.EmbeddingMetadata"],
	)
	assertGeneratedSearchFacetsSchema(
		t,
		contract.Components.Schemas["models.SearchFacets"],
		contract.Components.Schemas["models.SearchFacetValue"],
		contract.Components.Schemas["models.SearchFacetScope"],
	)
	assertGeneratedSearchItemSchema(t, contract.Components.Schemas["models.SearchItem"])
	assertGeneratedNonSearchSchemas(t, contract.Components.Schemas)
}

func assertGeneratedPublicServiceOperations(t *testing.T, paths map[string]generatedOpenAPIPath) {
	t.Helper()
	expectedOperations := map[string]struct {
		statuses          []string
		responseReference string
	}{
		"/api/public/service-categories": {
			statuses:          []string{"200", "500"},
			responseReference: generatedServiceCategoriesReference,
		},
		"/api/public/service-categories/{category}/subcategories": {
			statuses:          []string{"200", "400", "500"},
			responseReference: generatedServiceSubcategoriesReference,
		},
		"/api/public/services": {
			statuses:          []string{"200", "400", "500"},
			responseReference: generatedPublicServiceListReference,
		},
		"/api/public/services/{slug}": {
			statuses:          []string{"200", "308", "400", "404", "500"},
			responseReference: generatedPublicServiceDetailReference,
		},
	}
	for operationPath, expectation := range expectedOperations {
		operation := paths[operationPath].Get
		actualStatuses := sortedGeneratedOpenAPIKeys(operation.Responses)
		if !slices.Equal(actualStatuses, expectation.statuses) {
			t.Fatalf("%s response statuses = %v, want %v", operationPath, actualStatuses, expectation.statuses)
		}
		responseSchema := operation.Responses["200"].Content["application/json"].Schema
		if responseSchema.Reference != expectation.responseReference {
			t.Fatalf("%s response schema = %q, want %q", operationPath, responseSchema.Reference, expectation.responseReference)
		}
	}

	browseParameters := paths["/api/public/services"].Get.Parameters
	if len(browseParameters) != 4 {
		t.Fatalf("public service browse parameters = %v", browseParameters)
	}
	pageParameters := map[string]generatedOpenAPIProperty{}
	for _, parameter := range browseParameters {
		pageParameters[parameter.Name] = parameter.Schema
	}
	if pageParameters["page"].Minimum == nil || *pageParameters["page"].Minimum != 1 ||
		pageParameters["per_page"].Minimum == nil || *pageParameters["per_page"].Minimum != 1 ||
		pageParameters["per_page"].Maximum == nil || *pageParameters["per_page"].Maximum != 100 {
		t.Fatalf("public service pagination contract = %+v", pageParameters)
	}
}

func assertGeneratedServiceIntelligenceOperations(t *testing.T, contract generatedOpenAPIDocument) {
	t.Helper()
	relationsOperation := contract.Paths["/api/public/services/{slug}/relations"].Get
	if actualStatuses := sortedGeneratedOpenAPIKeys(relationsOperation.Responses); !slices.Equal(
		actualStatuses, []string{"200", "308", "400", "404", "500"},
	) {
		t.Fatalf("public relations response statuses = %v", actualStatuses)
	}
	if responseReference := relationsOperation.Responses["200"].Content["application/json"].Schema.Reference; responseReference != generatedServiceRelationsReference {
		t.Fatalf("public relations response schema = %q", responseReference)
	}

	for _, operationExpectation := range []struct {
		path              string
		requestReference  string
		responseReference string
		statuses          []string
		maximumBodyBytes  int64
	}{
		{
			path: "/api/public/suggest", requestReference: generatedSuggestionRequestReference,
			responseReference: generatedSuggestionResponseReference,
			statuses:          []string{"200", "400", "413", "415", "500"}, maximumBodyBytes: 4096,
		},
		{
			path: "/api/public/search-summary", requestReference: generatedSummaryRequestReference,
			responseReference: generatedSummaryResponseReference,
			statuses:          []string{"200", "400", "409", "413", "415", "500", "504"}, maximumBodyBytes: 16384,
		},
	} {
		operation := contract.Paths[operationExpectation.path].Post
		if actualStatuses := sortedGeneratedOpenAPIKeys(operation.Responses); !slices.Equal(actualStatuses, operationExpectation.statuses) {
			t.Fatalf("%s response statuses = %v, want %v", operationExpectation.path, actualStatuses, operationExpectation.statuses)
		}
		mediaType := operation.RequestBody.Content["application/json"]
		if !operation.RequestBody.Required || mediaType.Schema.Reference != operationExpectation.requestReference ||
			operation.RequestBody.MaximumBodyBytes == nil || *operation.RequestBody.MaximumBodyBytes != operationExpectation.maximumBodyBytes {
			t.Fatalf("%s request contract = %+v", operationExpectation.path, operation.RequestBody)
		}
		if responseReference := operation.Responses["200"].Content["application/json"].Schema.Reference; responseReference != operationExpectation.responseReference {
			t.Fatalf("%s response schema = %q", operationExpectation.path, responseReference)
		}
	}

	assertGeneratedSchemaContract(
		t, "models.PublicSuggestionRequest", contract.Components.Schemas["models.PublicSuggestionRequest"],
		[]string{"query"}, nil,
	)
	suggestionQuery := contract.Components.Schemas["models.PublicSuggestionRequest"].Properties["query"]
	if suggestionQuery.MaximumText == nil || *suggestionQuery.MaximumText != models.MaximumPublicSuggestionQueryRunes {
		t.Fatalf("suggestion query bound = %+v", suggestionQuery)
	}
	summaryRequest := contract.Components.Schemas["models.SearchSummaryRequest"]
	assertGeneratedSchemaContract(
		t, "models.SearchSummaryRequest", summaryRequest,
		[]string{"candidate_ids", "catalog_revision", "query"}, nil,
	)
	candidateIDs := summaryRequest.Properties["candidate_ids"]
	if candidateIDs.MinimumItems == nil || *candidateIDs.MinimumItems != 1 ||
		candidateIDs.MaximumItems == nil || *candidateIDs.MaximumItems != models.MaximumSearchSummaryCandidates ||
		!candidateIDs.UniqueItems || candidateIDs.Items == nil || candidateIDs.Items.Format != "uuid" {
		t.Fatalf("summary candidate ID contract = %+v", candidateIDs)
	}
}

func assertGeneratedPathParity(t *testing.T, paths map[string]generatedOpenAPIPath) {
	t.Helper()
	expectedMethods := map[string][]string{
		"/api/public/catalog/{id}":                                {"GET"},
		"/api/public/recommendations":                             {"GET"},
		"/api/public/search":                                      {"GET", "POST"},
		"/api/public/search-summary":                              {"POST"},
		"/api/public/service-categories":                          {"GET"},
		"/api/public/service-categories/{category}/subcategories": {"GET"},
		"/api/public/services":                                    {"GET"},
		"/api/public/services/{slug}":                             {"GET"},
		"/api/public/services/{slug}/relations":                   {"GET"},
		"/api/public/suggest":                                     {"POST"},
		"/api/v1/admin/sync/status":                               {"GET"},
		"/api/v1/admin/sync/trigger":                              {"POST"},
		"/api/v1/catalog/{id}":                                    {"GET"},
		"/api/v1/recommendations":                                 {"GET"},
		"/api/v1/search":                                          {"GET", "POST"},
		"/api/webhooks/salesforce":                                {"POST"},
		"/health":                                                 {"GET"},
		"/metrics":                                                {"GET"},
		"/ready":                                                  {"GET"},
	}
	actualPaths := sortedGeneratedOpenAPIKeys(paths)
	expectedPaths := sortedGeneratedOpenAPIKeys(expectedMethods)
	if !slices.Equal(actualPaths, expectedPaths) {
		t.Fatalf("OpenAPI paths = %v, want %v", actualPaths, expectedPaths)
	}
	for path, expectedPathMethods := range expectedMethods {
		actualMethods := sortedGeneratedOpenAPIKeys(generatedOperations(paths[path]))
		if !slices.Equal(actualMethods, expectedPathMethods) {
			t.Fatalf("OpenAPI methods for %s = %v, want %v", path, actualMethods, expectedPathMethods)
		}
	}
}

func generatedOperations(path generatedOpenAPIPath) map[string]generatedOpenAPIOperation {
	operations := map[string]generatedOpenAPIOperation{
		"DELETE":  path.Delete,
		"GET":     path.Get,
		"HEAD":    path.Head,
		"OPTIONS": path.Options,
		"PATCH":   path.Patch,
		"POST":    path.Post,
		"PUT":     path.Put,
		"TRACE":   path.Trace,
	}
	for method, operation := range operations {
		if len(operation.Responses) == 0 {
			delete(operations, method)
		}
	}
	return operations
}

func assertGeneratedSecurityParity(t *testing.T, paths map[string]generatedOpenAPIPath) {
	t.Helper()
	authenticatedOperations := map[string]struct{}{
		"GET /api/v1/admin/sync/status":   {},
		"POST /api/v1/admin/sync/trigger": {},
		"GET /api/v1/catalog/{id}":        {},
		"GET /api/v1/recommendations":     {},
		"GET /api/v1/search":              {},
		"POST /api/v1/search":             {},
	}
	for path, pathContract := range paths {
		for method, operation := range generatedOperations(pathContract) {
			operationName := method + " " + path
			_, authenticationRequired := authenticatedOperations[operationName]
			if !authenticationRequired {
				if len(operation.Security) != 0 {
					t.Fatalf("%s security = %v, want public operation", operationName, operation.Security)
				}
				continue
			}
			if len(operation.Security) != 1 {
				t.Fatalf("%s security = %v, want BearerAuth", operationName, operation.Security)
			}
			bearerScopes, bearerSecurityExists := operation.Security[0]["BearerAuth"]
			if !bearerSecurityExists || len(bearerScopes) != 0 {
				t.Fatalf("%s security = %v, want BearerAuth", operationName, operation.Security)
			}
		}
	}
}

func assertGeneratedRequestIDContract(t *testing.T, contract generatedOpenAPIDocument) {
	t.Helper()
	requestIDHeader, headerExists := contract.Components.Headers["X-Request-ID"]
	if !headerExists || requestIDHeader.Description == "" ||
		requestIDHeader.Schema.Type != "string" || requestIDHeader.Schema.Format != "uuid" {
		t.Fatalf("X-Request-ID component = %+v", requestIDHeader)
	}
	for path, pathContract := range contract.Paths {
		for method, operation := range generatedOperations(pathContract) {
			for responseStatus, response := range operation.Responses {
				requestIDReference := response.Headers["X-Request-ID"]
				if requestIDReference.Reference != generatedRequestIDHeaderReference {
					t.Fatalf(
						"%s %s response %s X-Request-ID = %+v",
						method,
						path,
						responseStatus,
						requestIDReference,
					)
				}
			}
		}
	}
}

func assertGeneratedMetricsContract(t *testing.T, metricsPath generatedOpenAPIPath) {
	t.Helper()
	metricsResponse := metricsPath.Get.Responses["200"]
	metricsMediaType, mediaTypeExists := metricsResponse.Content["text/plain"]
	if !mediaTypeExists || metricsMediaType.Schema.Type != "string" {
		t.Fatalf("metrics response = %+v", metricsResponse)
	}
}

func assertGeneratedRecommendationOperations(
	t *testing.T,
	paths map[string]generatedOpenAPIPath,
	schemas map[string]generatedOpenAPISchema,
) {
	t.Helper()
	expectedContexts := []string{
		string(models.ContextHomepage),
		string(models.ContextAfterSearch),
		string(models.ContextProfile),
	}
	if contextSchema := schemas["models.RecommendationContext"]; contextSchema.Type != "string" ||
		!slices.Equal(contextSchema.Enum, expectedContexts) {
		t.Fatalf("recommendation context schema = %+v", contextSchema)
	}
	errorSchema := schemas["models.RecommendationErrorResponse"]
	assertGeneratedClosedRequiredObject(
		t,
		"recommendation error response",
		errorSchema,
		[]string{"error", "log_id"},
	)
	if errorSchema.Properties["error"].Type != "string" ||
		errorSchema.Properties["log_id"].Type != "string" ||
		errorSchema.Properties["log_id"].Format != "uuid" {
		t.Fatalf("recommendation error response fields = %+v", errorSchema.Properties)
	}

	expectedStatusesByPath := map[string][]string{
		"/api/public/recommendations": {"200", "400", "500"},
		"/api/v1/recommendations":     {"200", "400", "401", "500"},
	}
	for recommendationPath, expectedStatuses := range expectedStatusesByPath {
		operation := paths[recommendationPath].Get
		if actualStatuses := sortedGeneratedOpenAPIKeys(operation.Responses); !slices.Equal(actualStatuses, expectedStatuses) {
			t.Fatalf(
				"%s response statuses = %v, want %v",
				recommendationPath,
				actualStatuses,
				expectedStatuses,
			)
		}
		parametersByName := make(map[string]generatedOpenAPIParameter, len(operation.Parameters))
		for _, parameter := range operation.Parameters {
			parametersByName[parameter.Name] = parameter
		}
		if actualParameterNames := sortedGeneratedOpenAPIKeys(parametersByName); !slices.Equal(
			actualParameterNames,
			[]string{"context", "limit", "types"},
		) {
			t.Fatalf("%s parameters = %v", recommendationPath, actualParameterNames)
		}
		contextParameter, contextParameterExists := parametersByName["context"]
		if !contextParameterExists || contextParameter.Schema.Type != "string" ||
			!slices.Equal(contextParameter.Schema.Enum, expectedContexts) {
			t.Fatalf("%s context parameter = %+v", recommendationPath, contextParameter)
		}
		typesParameter, typesParameterExists := parametersByName["types"]
		if !typesParameterExists || typesParameter.Schema.Type != "array" ||
			typesParameter.Schema.Items == nil || typesParameter.Schema.Items.Type != "string" ||
			!slices.Equal(typesParameter.Schema.Items.Enum, []string{
				string(models.TypeService),
				string(models.TypeCourse),
				string(models.TypeJob),
				string(models.TypeMEIOpportunity),
			}) ||
			(typesParameter.Style != "" && typesParameter.Style != "form") ||
			(typesParameter.Explode != nil && !*typesParameter.Explode) {
			t.Fatalf("%s types parameter = %+v", recommendationPath, typesParameter)
		}
		limitParameter, limitParameterExists := parametersByName["limit"]
		if !limitParameterExists {
			t.Fatalf("%s limit parameter is missing", recommendationPath)
		}
		assertGeneratedIntegerConstraints(
			t,
			recommendationPath+" limit",
			limitParameter.Schema,
			1,
			models.MaximumRecommendationItems,
			models.DefaultRecommendationLimit,
		)
		for responseStatus, response := range operation.Responses {
			responseSchema := response.Content["application/json"].Schema
			if responseStatus == "200" {
				if responseSchema.Reference != "#/components/schemas/models.RecommendationResponse" {
					t.Fatalf("%s success schema = %+v", recommendationPath, responseSchema)
				}
				continue
			}
			if responseSchema.Reference != generatedRecommendationErrorReference {
				t.Fatalf(
					"%s response %s schema = %+v",
					recommendationPath,
					responseStatus,
					responseSchema,
				)
			}
		}
	}
}

func assertGeneratedSyncStatusContract(
	t *testing.T,
	syncStatusPath generatedOpenAPIPath,
	schemas map[string]generatedOpenAPISchema,
) {
	t.Helper()
	successSchema := syncStatusPath.Get.Responses["200"].Content["application/json"].Schema
	if successSchema.Reference != generatedSyncStatusResponseReference {
		t.Fatalf("sync status response schema = %+v", successSchema)
	}
	wrapperSchema := schemas["models.SyncStatusResponse"]
	assertGeneratedClosedRequiredObject(t, "sync status response", wrapperSchema, []string{"syncs"})
	syncsProperty := wrapperSchema.Properties["syncs"]
	if syncsProperty.Type != "array" || syncsProperty.Items == nil ||
		syncsProperty.Items.Reference != "#/components/schemas/models.SyncStatus" {
		t.Fatalf("sync status collection = %+v", syncsProperty)
	}
}

func assertGeneratedSalesForceWebhookContract(
	t *testing.T,
	webhookPath generatedOpenAPIPath,
	webhookSchema generatedOpenAPISchema,
) {
	t.Helper()
	operation := webhookPath.Post
	parametersByName := make(map[string]generatedOpenAPIParameter, len(operation.Parameters))
	for _, parameter := range operation.Parameters {
		parametersByName[parameter.Name] = parameter
	}
	signatureParameter, signatureExists := parametersByName["X-Salesforce-Signature"]
	expectedSignatureLength := v1.SalesForceWebhookSignatureHexLength
	if !signatureExists || signatureParameter.Schema.Type != "string" ||
		signatureParameter.Schema.MinimumText == nil ||
		*signatureParameter.Schema.MinimumText != expectedSignatureLength ||
		signatureParameter.Schema.MaximumText == nil ||
		*signatureParameter.Schema.MaximumText != expectedSignatureLength ||
		signatureParameter.Schema.Pattern != generatedSalesForceSignaturePattern {
		t.Fatalf("Salesforce signature parameter = %+v", signatureParameter)
	}
	if operation.RequestBody.MaximumBodyBytes == nil ||
		*operation.RequestBody.MaximumBodyBytes != v1.MaximumSalesForceWebhookBodyBytes {
		t.Fatalf("Salesforce request body byte limit = %v", operation.RequestBody.MaximumBodyBytes)
	}

	webhookRequired := append([]string(nil), webhookSchema.Required...)
	slices.Sort(webhookRequired)
	webhookObject := webhookSchema.Properties["sobject"]
	webhookObjectRequired := append([]string(nil), webhookObject.Required...)
	slices.Sort(webhookObjectRequired)
	recordIDProperty := webhookObject.Properties["Id"]
	if !slices.Equal(webhookRequired, []string{"sobject"}) ||
		!slices.Equal(webhookObjectRequired, []string{"Id"}) ||
		recordIDProperty.Type != "string" ||
		recordIDProperty.MinimumText == nil || *recordIDProperty.MinimumText != 1 ||
		recordIDProperty.MaximumText != nil || recordIDProperty.Pattern != generatedNonBlankPattern {
		t.Fatalf("Salesforce webhook boundary = %+v", webhookSchema)
	}
	if webhookSchema.AdditionalProperties != nil && !*webhookSchema.AdditionalProperties {
		t.Fatalf("Salesforce webhook unexpectedly rejects upstream fields: %+v", webhookSchema)
	}
	for _, objectPropertyName := range []string{"event", "sobject"} {
		assertGeneratedPropertyAllowsAdditionalProperties(
			t,
			"Salesforce webhook "+objectPropertyName,
			webhookSchema.Properties[objectPropertyName],
		)
	}
}

func assertGeneratedSearchGETParameters(
	t *testing.T,
	searchPath string,
	parameters []generatedOpenAPIParameter,
) {
	t.Helper()
	parametersByName := make(map[string]generatedOpenAPIParameter, len(parameters))
	for _, parameter := range parameters {
		parametersByName[parameter.Name] = parameter
	}
	for _, parameterName := range []string{"bairro", "orgao", "segmento", "tema"} {
		parameter, parameterExists := parametersByName[parameterName]
		if !parameterExists || parameter.Schema.MaximumText == nil ||
			*parameter.Schema.MaximumText != models.MaxSearchFilterRunes {
			t.Fatalf("%s GET parameter %s = %+v", searchPath, parameterName, parameter)
		}
	}
	typesParameter, typesParameterExists := parametersByName["types"]
	if !typesParameterExists || typesParameter.Schema.Type != "array" ||
		typesParameter.Schema.Items == nil ||
		typesParameter.Schema.Items.Type != "string" ||
		!slices.Equal(typesParameter.Schema.Items.Enum, []string{
			string(models.TypeService),
			string(models.TypeCourse),
			string(models.TypeJob),
			string(models.TypeMEIOpportunity),
		}) ||
		(typesParameter.Style != "" && typesParameter.Style != "form") ||
		(typesParameter.Explode != nil && !*typesParameter.Explode) {
		t.Fatalf("%s GET types serialization = %+v", searchPath, typesParameter)
	}
}

func assertGeneratedSearchFacetsSchema(
	t *testing.T,
	facetsSchema generatedOpenAPISchema,
	valueSchema generatedOpenAPISchema,
	scopeSchema generatedOpenAPISchema,
) {
	t.Helper()
	expectedFacetProperties := []string{
		"bairros",
		"modalidades",
		"organizations",
		"scope",
		"types",
		"version",
	}
	facetProperties := sortedGeneratedOpenAPIKeys(facetsSchema.Properties)
	if !slices.Equal(facetProperties, expectedFacetProperties) ||
		facetsSchema.AdditionalProperties == nil ||
		*facetsSchema.AdditionalProperties {
		t.Fatalf("search facets schema = %+v", facetsSchema)
	}
	for _, requiredProperty := range expectedFacetProperties {
		if !slices.Contains(facetsSchema.Required, requiredProperty) {
			t.Fatalf("search facets does not require %s", requiredProperty)
		}
	}
	if !slices.Equal(facetsSchema.Properties["version"].Enum, []string{models.SearchFacetVersion}) ||
		facetsSchema.Properties["scope"].Reference != "#/components/schemas/models.SearchFacetScope" {
		t.Fatalf("search facets provenance = %+v", facetsSchema.Properties)
	}
	for _, collectionProperty := range []string{"types", "modalidades", "bairros", "organizations"} {
		property := facetsSchema.Properties[collectionProperty]
		if property.Type != "array" || property.MaximumItems == nil ||
			*property.MaximumItems != models.MaxSearchFacetValues || property.Items == nil ||
			property.Items.Reference != generatedSearchFacetValueReference {
			t.Fatalf("search facet collection %s = %+v", collectionProperty, property)
		}
	}

	if valueSchema.AdditionalProperties == nil || *valueSchema.AdditionalProperties {
		t.Fatalf("search facet value allows unknown fields: %+v", valueSchema)
	}
	for _, requiredProperty := range []string{"value", "label", "count"} {
		if !slices.Contains(valueSchema.Required, requiredProperty) {
			t.Fatalf("search facet value does not require %s", requiredProperty)
		}
	}
	if valueSchema.Properties["count"].Minimum == nil || *valueSchema.Properties["count"].Minimum != 1 {
		t.Fatalf("search facet count = %+v", valueSchema.Properties["count"])
	}
	if valueSchema.Properties["value"].MaximumText == nil ||
		*valueSchema.Properties["value"].MaximumText != models.MaxSearchFilterRunes ||
		valueSchema.Properties["label"].MaximumText == nil ||
		*valueSchema.Properties["label"].MaximumText != models.MaxSearchFacetLabelRunes {
		t.Fatalf("search facet text bounds = %+v", valueSchema.Properties)
	}
	expectedScopes := []string{"catalog_matches", "retrieval_candidates", "unavailable"}
	if scopeSchema.Type != "string" || !slices.Equal(scopeSchema.Enum, expectedScopes) {
		t.Fatalf("search facet scope = %+v", scopeSchema)
	}
}

func assertGeneratedRankerDescriptorSchema(
	t *testing.T,
	descriptorSchema generatedOpenAPISchema,
	weightsSchema generatedOpenAPISchema,
	embeddingSchema generatedOpenAPISchema,
) {
	t.Helper()
	expectedRequiredProperties := []string{
		"base_version",
		"candidate_pool_size",
		"deduplication_version",
		"hyde_enabled",
		"maximum_semantic_distance",
		"query_expansion_version",
		"reciprocal_rank_k",
		"reranker_enabled",
		"retrieval_version",
		"schema_version",
		"semantic_enabled",
		"semantic_overfetch_factor",
		"trigram_threshold",
		"weights",
	}
	actualRequiredProperties := append([]string(nil), descriptorSchema.Required...)
	slices.Sort(actualRequiredProperties)
	if !slices.Equal(actualRequiredProperties, expectedRequiredProperties) {
		t.Fatalf(
			"ranker descriptor required properties = %v, want %v",
			actualRequiredProperties,
			expectedRequiredProperties,
		)
	}
	expectedPropertyTypes := map[string]string{
		"base_version":              "string",
		"candidate_pool_size":       "integer",
		"deduplication_version":     "string",
		"hyde_candidate_count":      "integer",
		"hyde_determinism_policy":   "string",
		"hyde_enabled":              "boolean",
		"hyde_max_output_tokens":    "integer",
		"hyde_model":                "string",
		"hyde_prompt_sha256":        "string",
		"hyde_prompt_version":       "string",
		"hyde_response_mime_type":   "string",
		"hyde_seed":                 "integer",
		"hyde_temperature":          "number",
		"maximum_semantic_distance": "number",
		"query_expansion_version":   "string",
		"reciprocal_rank_k":         "number",
		"reranker_candidate_limit":  "integer",
		"reranker_enabled":          "boolean",
		"reranker_version":          "string",
		"retrieval_version":         "string",
		"schema_version":            "string",
		"semantic_enabled":          "boolean",
		"semantic_overfetch_factor": "integer",
		"trigram_threshold":         "number",
	}
	expectedReferenceProperties := map[string]string{
		"embedding": "#/components/schemas/models.EmbeddingMetadata",
		"facilita":  "#/components/schemas/models.SearchExternalRetrieverDescriptor",
		"weights":   "#/components/schemas/models.SearchRetrievalWeights",
	}
	expectedPropertyNames := make([]string, 0, len(expectedPropertyTypes)+len(expectedReferenceProperties))
	for propertyName, expectedType := range expectedPropertyTypes {
		expectedPropertyNames = append(expectedPropertyNames, propertyName)
		if descriptorSchema.Properties[propertyName].Type != expectedType {
			t.Fatalf("ranker descriptor property %s = %+v", propertyName, descriptorSchema.Properties[propertyName])
		}
	}
	for propertyName, expectedReference := range expectedReferenceProperties {
		expectedPropertyNames = append(expectedPropertyNames, propertyName)
		if descriptorSchema.Properties[propertyName].Reference != expectedReference {
			t.Fatalf("ranker descriptor property %s = %+v", propertyName, descriptorSchema.Properties[propertyName])
		}
	}
	slices.Sort(expectedPropertyNames)
	if actualPropertyNames := sortedGeneratedOpenAPIKeys(descriptorSchema.Properties); !slices.Equal(actualPropertyNames, expectedPropertyNames) ||
		descriptorSchema.AdditionalProperties == nil || *descriptorSchema.AdditionalProperties {
		t.Fatalf("ranker descriptor schema = %+v", descriptorSchema)
	}
	assertGeneratedClosedRequiredObject(
		t,
		"ranker weights",
		weightsSchema,
		[]string{"exact", "facilita", "full_text", "hyde", "semantic", "trigram"},
	)
	assertGeneratedClosedRequiredObject(
		t,
		"embedding metadata",
		embeddingSchema,
		[]string{"dimensions", "document_task_type", "document_version", "model", "query_task_type", "version"},
	)
}

func assertGeneratedSearchSourceSchemas(
	t *testing.T,
	sourcesSchema generatedOpenAPISchema,
	diagnosticSchema generatedOpenAPISchema,
	provenanceSchema generatedOpenAPISchema,
	statusSchema generatedOpenAPISchema,
	failureSchema generatedOpenAPISchema,
) {
	t.Helper()
	assertGeneratedClosedRequiredObject(t, "search sources", sourcesSchema, []string{"facilita"})
	assertGeneratedClosedRequiredObject(
		t,
		"external retriever provenance",
		provenanceSchema,
		[]string{"catalog_revision", "query_expansion_version", "ranker_version", "retrieval_version", "schema_version"},
	)
	expectedDiagnosticProperties := []string{
		"candidates_received",
		"eligible_contributions",
		"failure",
		"latency_ms",
		"provenance",
		"status",
	}
	if actualProperties := sortedGeneratedOpenAPIKeys(diagnosticSchema.Properties); !slices.Equal(actualProperties, expectedDiagnosticProperties) ||
		diagnosticSchema.AdditionalProperties == nil || *diagnosticSchema.AdditionalProperties {
		t.Fatalf("search source diagnostic schema = %+v", diagnosticSchema)
	}
	requiredDiagnosticProperties := append([]string(nil), diagnosticSchema.Required...)
	slices.Sort(requiredDiagnosticProperties)
	if !slices.Equal(
		requiredDiagnosticProperties,
		[]string{"candidates_received", "eligible_contributions", "latency_ms", "status"},
	) {
		t.Fatalf("search source diagnostic required properties = %v", requiredDiagnosticProperties)
	}
	if !slices.Equal(statusSchema.Enum, []string{"not_applicable", "unavailable", "no_effect", "applied"}) ||
		!slices.Equal(failureSchema.Enum, []string{"timeout", "transport", "rejected", "invalid_contract"}) {
		t.Fatalf("search source enums = status %v failure %v", statusSchema.Enum, failureSchema.Enum)
	}
}

func assertGeneratedClosedRequiredObject(
	t *testing.T,
	schemaName string,
	schema generatedOpenAPISchema,
	expectedProperties []string,
) {
	t.Helper()
	actualProperties := sortedGeneratedOpenAPIKeys(schema.Properties)
	requiredProperties := append([]string(nil), schema.Required...)
	slices.Sort(requiredProperties)
	if !slices.Equal(actualProperties, expectedProperties) ||
		!slices.Equal(requiredProperties, expectedProperties) ||
		schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Fatalf("%s schema = %+v", schemaName, schema)
	}
}

func assertGeneratedSearchItemSchema(t *testing.T, itemSchema generatedOpenAPISchema) {
	t.Helper()
	if !slices.Contains(itemSchema.Required, "canonical_id") ||
		itemSchema.Properties["canonical_id"].Type != "string" ||
		itemSchema.AdditionalProperties == nil || *itemSchema.AdditionalProperties {
		t.Fatalf("search item canonical identity contract = %+v", itemSchema)
	}
	assertGeneratedCatalogProjectionBounds(
		t,
		"models.SearchItem",
		itemSchema,
		map[string]generatedCatalogTextBound{
			"source_id":    {minimum: 1, maximum: models.MaximumCatalogExternalIDRunes, pattern: generatedNonBlankPattern},
			"slug":         {minimum: 1, maximum: models.MaximumCatalogPublicScalarRunes},
			"title":        {minimum: 1, maximum: models.MaximumCatalogTitleRunes, pattern: generatedNonBlankPattern},
			"short_desc":   {minimum: 1, maximum: models.MaximumCatalogTextRunes},
			"organization": {minimum: 1, maximum: models.MaximumCatalogOrganizationRunes},
			"modalidade":   {minimum: 1, maximum: models.MaximumCatalogModalityRunes},
			"url":          {minimum: 1, maximum: models.MaximumCatalogURLRunes, pattern: generatedCatalogHTTPURLPattern, format: "uri"},
			"image_url":    {minimum: 1, maximum: models.MaximumCatalogURLRunes, pattern: generatedCatalogHTTPURLPattern, format: "uri"},
		},
		[]string{"bairros", "tags"},
	)
	metadataProperty := itemSchema.Properties["metadata"]
	if metadataProperty.Type != "object" ||
		!slices.Equal(
			sortedGeneratedOpenAPIKeys(metadataProperty.Properties),
			[]string{"id", "slug", "tema_especifico", "tema_geral"},
		) {
		t.Fatalf("search metadata schema = %+v", metadataProperty)
	}
	assertGeneratedPropertyAdditionalProperties(t, "search metadata", metadataProperty, false)
	for propertyName, metadataField := range metadataProperty.Properties {
		if metadataField.Type != "string" || metadataField.MaximumText == nil ||
			*metadataField.MaximumText != models.MaximumCatalogPublicScalarRunes {
			t.Fatalf("search metadata %s = %+v", propertyName, metadataField)
		}
	}
}

func assertGeneratedNonSearchSchemas(
	t *testing.T,
	schemas map[string]generatedOpenAPISchema,
) {
	t.Helper()
	assertGeneratedSchemaContract(
		t,
		"models.CatalogItem",
		schemas["models.CatalogItem"],
		[]string{"created_at", "external_id", "id", "source", "status", "title", "type", "updated_at"},
		map[string]string{
			"created_at":        "date-time",
			"id":                "uuid",
			"image_url":         "uri",
			"source_updated_at": "date-time",
			"updated_at":        "date-time",
			"url":               "uri",
			"valid_from":        "date-time",
			"valid_until":       "date-time",
		},
	)
	catalogSchema := schemas["models.CatalogItem"]
	assertGeneratedCatalogProjectionBounds(
		t,
		"models.CatalogItem",
		catalogSchema,
		map[string]generatedCatalogTextBound{
			"external_id":  {minimum: 1, maximum: models.MaximumCatalogExternalIDRunes, pattern: generatedNonBlankPattern},
			"title":        {minimum: 1, maximum: models.MaximumCatalogTitleRunes, pattern: generatedNonBlankPattern},
			"description":  {minimum: 1, maximum: models.MaximumCatalogDescriptionRunes},
			"short_desc":   {minimum: 1, maximum: models.MaximumCatalogTextRunes},
			"organization": {minimum: 1, maximum: models.MaximumCatalogOrganizationRunes},
			"modalidade":   {minimum: 1, maximum: models.MaximumCatalogModalityRunes},
			"url":          {minimum: 1, maximum: models.MaximumCatalogURLRunes, pattern: generatedCatalogHTTPURLPattern, format: "uri"},
			"image_url":    {minimum: 1, maximum: models.MaximumCatalogURLRunes, pattern: generatedCatalogHTTPURLPattern, format: "uri"},
		},
		[]string{"bairros", "tags"},
	)
	assertGeneratedCatalogInternalObjectBounds(t, catalogSchema, schemas["models.TargetAudienceData"])
	assertGeneratedSchemaContract(
		t,
		"models.PublicCatalogItem",
		schemas["models.PublicCatalogItem"],
		[]string{"id", "source", "source_id", "title", "type"},
		map[string]string{
			"id":                "uuid",
			"image_url":         "uri",
			"source_updated_at": "date-time",
			"url":               "uri",
			"valid_from":        "date-time",
			"valid_until":       "date-time",
		},
	)
	assertGeneratedCatalogProjectionBounds(
		t,
		"models.PublicCatalogItem",
		schemas["models.PublicCatalogItem"],
		map[string]generatedCatalogTextBound{
			"source_id":    {minimum: 1, maximum: models.MaximumCatalogExternalIDRunes, pattern: generatedNonBlankPattern},
			"title":        {minimum: 1, maximum: models.MaximumCatalogTitleRunes, pattern: generatedNonBlankPattern},
			"description":  {minimum: 1, maximum: models.MaximumCatalogDescriptionRunes},
			"short_desc":   {minimum: 1, maximum: models.MaximumCatalogTextRunes},
			"organization": {minimum: 1, maximum: models.MaximumCatalogOrganizationRunes},
			"modalidade":   {minimum: 1, maximum: models.MaximumCatalogModalityRunes},
			"url":          {minimum: 1, maximum: models.MaximumCatalogURLRunes, pattern: generatedCatalogHTTPURLPattern, format: "uri"},
			"image_url":    {minimum: 1, maximum: models.MaximumCatalogURLRunes, pattern: generatedCatalogHTTPURLPattern, format: "uri"},
		},
		[]string{"bairros", "tags"},
	)
	assertGeneratedSchemaContract(
		t,
		"models.RecommendationResponse",
		schemas["models.RecommendationResponse"],
		[]string{"context", "items", "personalized"},
		nil,
	)
	recommendationItems := schemas["models.RecommendationResponse"].Properties["items"]
	if recommendationItems.Type != "array" || recommendationItems.MaximumItems == nil ||
		*recommendationItems.MaximumItems != models.MaximumRecommendationItems {
		t.Fatalf("recommendation items bound = %+v", recommendationItems)
	}
	assertGeneratedSchemaContract(
		t,
		"models.RankedItem",
		schemas["models.RankedItem"],
		[]string{"id", "score", "source", "title", "type"},
		map[string]string{
			"id":        "uuid",
			"image_url": "uri",
			"url":       "uri",
		},
	)
	assertGeneratedCatalogProjectionBounds(
		t,
		"models.RankedItem",
		schemas["models.RankedItem"],
		map[string]generatedCatalogTextBound{
			"title":        {minimum: 1, maximum: models.MaximumCatalogTitleRunes, pattern: generatedNonBlankPattern},
			"short_desc":   {minimum: 1, maximum: models.MaximumCatalogTextRunes},
			"organization": {minimum: 1, maximum: models.MaximumCatalogOrganizationRunes},
			"modalidade":   {minimum: 1, maximum: models.MaximumCatalogModalityRunes},
			"url":          {minimum: 1, maximum: models.MaximumCatalogURLRunes, pattern: generatedCatalogHTTPURLPattern, format: "uri"},
			"image_url":    {minimum: 1, maximum: models.MaximumCatalogURLRunes, pattern: generatedCatalogHTTPURLPattern, format: "uri"},
		},
		[]string{"bairros", "tags"},
	)
	if scoreProperty := schemas["models.RankedItem"].Properties["score"]; scoreProperty.Minimum != nil || scoreProperty.Maximum != nil {
		t.Fatalf("recommendation score has an unproven bound = %+v", scoreProperty)
	}
	assertGeneratedSchemaContract(
		t,
		"models.SyncStatus",
		schemas["models.SyncStatus"],
		[]string{"items_failed", "items_processed", "last_event_type", "last_started_at", "last_status", "source"},
		map[string]string{
			"last_completed_at": "date-time",
			"last_started_at":   "date-time",
		},
	)
	for _, countPropertyName := range []string{"items_failed", "items_processed"} {
		countProperty := schemas["models.SyncStatus"].Properties[countPropertyName]
		if countProperty.Minimum != nil || countProperty.Maximum != nil {
			t.Fatalf("sync count %s has an unproven bound = %+v", countPropertyName, countProperty)
		}
	}
}

func assertGeneratedSchemaContract(
	t *testing.T,
	schemaName string,
	schema generatedOpenAPISchema,
	expectedRequired []string,
	expectedFormats map[string]string,
) {
	t.Helper()
	actualRequired := append([]string(nil), schema.Required...)
	slices.Sort(actualRequired)
	if !slices.Equal(actualRequired, expectedRequired) {
		t.Fatalf("%s required fields = %v, want %v", schemaName, actualRequired, expectedRequired)
	}
	if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Fatalf("%s must reject unknown response fields: %+v", schemaName, schema)
	}
	for propertyName, expectedFormat := range expectedFormats {
		if actualFormat := schema.Properties[propertyName].Format; actualFormat != expectedFormat {
			t.Fatalf("%s.%s format = %q, want %q", schemaName, propertyName, actualFormat, expectedFormat)
		}
	}
}

type generatedCatalogTextBound struct {
	minimum int
	maximum int
	pattern string
	format  string
}

func assertGeneratedCatalogProjectionBounds(
	t *testing.T,
	schemaName string,
	schema generatedOpenAPISchema,
	textBounds map[string]generatedCatalogTextBound,
	arrayProperties []string,
) {
	t.Helper()
	for propertyName, expectedBound := range textBounds {
		property := schema.Properties[propertyName]
		if property.Type != "string" || property.MinimumText == nil ||
			*property.MinimumText != expectedBound.minimum || property.MaximumText == nil ||
			*property.MaximumText != expectedBound.maximum || property.Pattern != expectedBound.pattern ||
			property.Format != expectedBound.format {
			t.Fatalf("%s.%s bounds = %+v", schemaName, propertyName, property)
		}
	}
	for _, propertyName := range arrayProperties {
		property := schema.Properties[propertyName]
		if property.Type != "array" || property.MinimumItems == nil ||
			*property.MinimumItems != 1 || property.MaximumItems == nil ||
			*property.MaximumItems != models.MaximumCatalogArrayItems || property.Items == nil ||
			property.Items.Type != "string" || property.Items.MinimumText == nil ||
			*property.Items.MinimumText != 1 || property.Items.MaximumText == nil ||
			*property.Items.MaximumText != models.MaximumCatalogArrayEntryRunes ||
			property.Items.Pattern != generatedNonBlankPattern {
			t.Fatalf("%s.%s bounds = %+v", schemaName, propertyName, property)
		}
	}
}

func assertGeneratedCatalogInternalObjectBounds(
	t *testing.T,
	catalogSchema generatedOpenAPISchema,
	targetAudienceSchema generatedOpenAPISchema,
) {
	t.Helper()
	if catalogSchema.MaximumSearchProjectionBytes == nil ||
		*catalogSchema.MaximumSearchProjectionBytes != models.MaximumCatalogSearchProjectionBytes {
		t.Fatalf("catalog search projection byte bound = %v", catalogSchema.MaximumSearchProjectionBytes)
	}
	if targetAudienceProperty := catalogSchema.Properties["target_audience"]; targetAudienceProperty.Reference != generatedTargetAudienceReference {
		t.Fatalf("catalog target audience reference = %+v", targetAudienceProperty)
	}

	if targetAudienceSchema.Type != "object" || targetAudienceSchema.AdditionalProperties == nil ||
		*targetAudienceSchema.AdditionalProperties || targetAudienceSchema.MaximumJSONBytes == nil ||
		*targetAudienceSchema.MaximumJSONBytes != models.MaximumCatalogTargetAudienceBytes ||
		!slices.Equal(
			sortedGeneratedOpenAPIKeys(targetAudienceSchema.Properties),
			[]string{"deficiencia", "escolaridade", "etnia", "faixa_etaria", "genero", "pcd", "renda"},
		) {
		t.Fatalf("target audience schema = %+v", targetAudienceSchema)
	}
	for _, arrayPropertyName := range []string{"deficiencia", "escolaridade", "etnia", "faixa_etaria", "genero"} {
		arrayProperty := targetAudienceSchema.Properties[arrayPropertyName]
		if arrayProperty.Type != "array" || !arrayProperty.Nullable || arrayProperty.MinimumItems != nil ||
			arrayProperty.MaximumItems == nil || *arrayProperty.MaximumItems != models.MaximumCatalogArrayItems ||
			arrayProperty.Items == nil || arrayProperty.Items.Type != "string" ||
			arrayProperty.Items.MinimumText == nil || *arrayProperty.Items.MinimumText != 1 ||
			arrayProperty.Items.MaximumText == nil ||
			*arrayProperty.Items.MaximumText != models.MaximumCatalogArrayEntryRunes ||
			arrayProperty.Items.Pattern != generatedNonBlankPattern {
			t.Fatalf("target audience %s = %+v", arrayPropertyName, arrayProperty)
		}
	}
	rendaProperty := targetAudienceSchema.Properties["renda"]
	if rendaProperty.Type != "string" || !rendaProperty.Nullable || rendaProperty.MinimumText != nil ||
		rendaProperty.MaximumText == nil ||
		*rendaProperty.MaximumText != models.MaximumCatalogPublicScalarRunes {
		t.Fatalf("target audience renda = %+v", rendaProperty)
	}
	pcdProperty := targetAudienceSchema.Properties["pcd"]
	if pcdProperty.Type != "boolean" || !pcdProperty.Nullable {
		t.Fatalf("target audience pcd = %+v", pcdProperty)
	}

	sourceDataProperty := catalogSchema.Properties["source_data"]
	if sourceDataProperty.Type != "object" || sourceDataProperty.MaximumJSONBytes == nil ||
		*sourceDataProperty.MaximumJSONBytes != models.MaximumCatalogSourceDataBytes ||
		!slices.Equal(
			sortedGeneratedOpenAPIKeys(sourceDataProperty.Properties),
			[]string{"_catalog_object_type", "canonical_id", "id", "slug", "tema_especifico", "tema_geral"},
		) {
		t.Fatalf("catalog source data schema = %+v", sourceDataProperty)
	}
	assertGeneratedPropertyAdditionalProperties(t, "catalog source data", sourceDataProperty, true)
	for _, propertyName := range []string{"canonical_id", "id", "slug", "tema_especifico", "tema_geral"} {
		publicProperty := sourceDataProperty.Properties[propertyName]
		if publicProperty.Type != "string" || !publicProperty.Nullable || publicProperty.MaximumText == nil ||
			*publicProperty.MaximumText != models.MaximumCatalogPublicScalarRunes {
			t.Fatalf("catalog source data %s = %+v", propertyName, publicProperty)
		}
	}
	objectTypeProperty := sourceDataProperty.Properties[models.SalesForceObjectTypeSourceDataKey]
	if objectTypeProperty.Type != "string" || objectTypeProperty.MinimumText == nil ||
		*objectTypeProperty.MinimumText != 1 || objectTypeProperty.MaximumText == nil ||
		*objectTypeProperty.MaximumText != models.MaximumCatalogModalityRunes ||
		objectTypeProperty.Pattern != generatedNonBlankPattern {
		t.Fatalf("catalog source object type = %+v", objectTypeProperty)
	}
}

func assertGeneratedPropertyAdditionalProperties(
	t *testing.T,
	propertyName string,
	property generatedOpenAPIProperty,
	expected bool,
) {
	t.Helper()
	var actual bool
	if len(property.AdditionalProperties) == 0 ||
		json.Unmarshal(property.AdditionalProperties, &actual) != nil || actual != expected {
		t.Fatalf("%s additionalProperties = %s, want %t", propertyName, property.AdditionalProperties, expected)
	}
}

func assertGeneratedPropertyAllowsAdditionalProperties(
	t *testing.T,
	propertyName string,
	property generatedOpenAPIProperty,
) {
	t.Helper()
	if len(property.AdditionalProperties) == 0 {
		return
	}
	var allowsAdditionalProperties bool
	if json.Unmarshal(property.AdditionalProperties, &allowsAdditionalProperties) != nil ||
		!allowsAdditionalProperties {
		t.Fatalf("%s unexpectedly rejects additional properties: %s", propertyName, property.AdditionalProperties)
	}
}

func assertGeneratedSearchResponseSchema(t *testing.T, responseSchema generatedOpenAPISchema) {
	t.Helper()
	if responseSchema.AdditionalProperties == nil || *responseSchema.AdditionalProperties {
		t.Fatalf("search response allows unknown fields: %+v", responseSchema)
	}
	expectedRequiredProperties := []string{
		"catalog_revision",
		"degraded",
		"effective_pipeline",
		"facets",
		"items",
		"page",
		"per_page",
		"ranker_descriptor",
		"ranker_version",
		"search_id",
		"sources",
		"total",
	}
	requiredProperties := append([]string(nil), responseSchema.Required...)
	slices.Sort(requiredProperties)
	if !slices.Equal(requiredProperties, expectedRequiredProperties) {
		t.Fatalf("search response required properties = %v, want %v", requiredProperties, expectedRequiredProperties)
	}
	if responseSchema.Properties["ranker_descriptor"].Reference != "#/components/schemas/models.SearchRankerDescriptor" {
		t.Fatalf("ranker descriptor property = %+v", responseSchema.Properties["ranker_descriptor"])
	}
	if responseSchema.Properties["effective_pipeline"].Reference != "#/components/schemas/models.SearchPipeline" {
		t.Fatalf("effective pipeline property = %+v", responseSchema.Properties["effective_pipeline"])
	}
	if responseSchema.Properties["facets"].Reference != generatedSearchFacetsSchemaReference {
		t.Fatalf("facets property = %+v", responseSchema.Properties["facets"])
	}
	if responseSchema.Properties["sources"].Reference != "#/components/schemas/models.SearchSources" {
		t.Fatalf("search sources property = %+v", responseSchema.Properties["sources"])
	}
	if responseSchema.Properties["catalog_revision"].Type != "string" || responseSchema.Properties["degraded"].Type != "boolean" {
		t.Fatalf("search provenance properties = %+v", responseSchema.Properties)
	}
}

func assertGeneratedSearchRequestSchema(t *testing.T, requestSchema generatedOpenAPISchema) {
	t.Helper()
	if requestSchema.Type != "object" {
		t.Fatalf("search request schema type = %q", requestSchema.Type)
	}
	if requestSchema.AdditionalProperties == nil || *requestSchema.AdditionalProperties {
		t.Fatal("search request schema must reject additional properties")
	}
	if len(requestSchema.Required) != 0 {
		t.Fatalf("search request required properties = %v, want none", requestSchema.Required)
	}

	expectedProperties := []string{
		"bairro",
		"canal_atendimento",
		"modalidade",
		"modelo_trabalho",
		"orgao",
		"page",
		"pcd",
		"per_page",
		"q",
		"regime_contratacao",
		"segmento",
		"tema",
		"turno",
		"types",
	}
	propertyNames := sortedGeneratedOpenAPIKeys(requestSchema.Properties)
	if !slices.Equal(propertyNames, expectedProperties) {
		t.Fatalf("search request properties = %v, want %v", propertyNames, expectedProperties)
	}

	assertGeneratedTextLimit(t, "q", requestSchema.Properties["q"], 256)
	for _, textProperty := range []string{"bairro", "orgao", "segmento", "tema"} {
		assertGeneratedTextLimit(t, textProperty, requestSchema.Properties[textProperty], 100)
	}
	assertGeneratedIntegerConstraints(t, "page", requestSchema.Properties["page"], 1, models.MaxSearchPage, 1)
	assertGeneratedIntegerConstraints(t, "per_page", requestSchema.Properties["per_page"], 1, 100, 10)
	assertGeneratedEnum(t, "modalidade", requestSchema.Properties["modalidade"], []string{"presencial", "digital", "hibrido"})
	assertGeneratedEnum(t, "turno", requestSchema.Properties["turno"], []string{"matutino", "vespertino", "noturno"})
	assertGeneratedEnum(t, "regime_contratacao", requestSchema.Properties["regime_contratacao"], []string{"clt", "pj", "temporario"})
	assertGeneratedEnum(t, "modelo_trabalho", requestSchema.Properties["modelo_trabalho"], []string{"presencial", "remoto", "hibrido"})
	assertGeneratedEnum(t, "canal_atendimento", requestSchema.Properties["canal_atendimento"], []string{"presencial", "digital", "telefone"})

	typesProperty := requestSchema.Properties["types"]
	if typesProperty.Type != "array" || !typesProperty.UniqueItems || typesProperty.Items == nil || typesProperty.Items.Reference != generatedItemTypeSchemaReference {
		t.Fatalf("types property = %+v", typesProperty)
	}
	if requestSchema.Properties["pcd"].Type != "boolean" {
		t.Fatalf("pcd property type = %q", requestSchema.Properties["pcd"].Type)
	}
}

func assertGeneratedTextLimit(t *testing.T, propertyName string, property generatedOpenAPIProperty, maximumText int) {
	t.Helper()
	if property.Type != "string" || property.MaximumText == nil || *property.MaximumText != maximumText {
		t.Fatalf("%s property = %+v", propertyName, property)
	}
}

func assertGeneratedIntegerConstraints(
	t *testing.T,
	propertyName string,
	property generatedOpenAPIProperty,
	minimum int,
	maximum int,
	defaultValue int,
) {
	t.Helper()
	if property.Type != "integer" || property.Minimum == nil || *property.Minimum != minimum || property.Default == nil || *property.Default != defaultValue {
		t.Fatalf("%s property = %+v", propertyName, property)
	}
	if maximum == 0 {
		if property.Maximum != nil {
			t.Fatalf("%s unexpected maximum = %d", propertyName, *property.Maximum)
		}
		return
	}
	if property.Maximum == nil || *property.Maximum != maximum {
		t.Fatalf("%s maximum = %v, want %d", propertyName, property.Maximum, maximum)
	}
}

func assertGeneratedEnum(t *testing.T, propertyName string, property generatedOpenAPIProperty, expectedValues []string) {
	t.Helper()
	if property.Type != "string" || !slices.Equal(property.Enum, expectedValues) {
		t.Fatalf("%s property = %+v, want enum %v", propertyName, property, expectedValues)
	}
}

func sortedGeneratedOpenAPIKeys[Value any](entries map[string]Value) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
