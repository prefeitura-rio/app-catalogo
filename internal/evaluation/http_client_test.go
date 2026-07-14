package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

const validSearchID = "550e8400-e29b-41d4-a716-446655440000"

func TestHTTPClientBuildsPublicSearchRequestAndValidatesResponse(t *testing.T) {
	trueValue := true
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %s", request.Method)
		}
		if request.URL.RawQuery != "" {
			t.Errorf("raw query leaked into URL: %q", request.URL.RawQuery)
		}
		if authorization := request.Header.Get("Authorization"); authorization != "" {
			t.Errorf("Authorization header leaked: %q", authorization)
		}
		if accept := request.Header.Get("Accept"); accept != "application/json" {
			t.Errorf("Accept = %q", accept)
		}
		if contentType := request.Header.Get("Content-Type"); contentType != "application/json" {
			t.Errorf("Content-Type = %q", contentType)
		}
		var requestBody models.SearchRequestBody
		if decodeError := json.NewDecoder(request.Body).Decode(&requestBody); decodeError != nil {
			t.Errorf("decode request body: %v", decodeError)
		}
		page := models.DefaultSearchPage
		perPage := 20
		wantRequestBody := models.SearchRequestBody{
			Q:                 `"assistente social" OR saúde -estágio`,
			Types:             []models.ItemType{models.TypeCourse, models.TypeJob},
			Page:              &page,
			PerPage:           &perPage,
			Modalidade:        "hibrido",
			Bairro:            "Rio Comprido",
			Orgao:             "SMAS",
			Turno:             "noturno",
			RegimeContratacao: "clt",
			ModeloTrabalho:    "remoto",
			PCD:               &trueValue,
			CanalAtendimento:  "digital",
			Tema:              "Saúde",
			Segmento:          "Comércio",
		}
		if !reflect.DeepEqual(requestBody, wantRequestBody) {
			t.Errorf("request body = %#v, want %#v", requestBody, wantRequestBody)
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		fmt.Fprint(responseWriter, validPublicSearchResponseJSON(20, `[{"source":"jobs","source_id":"job-1"},{"source":"courses","source_id":"course-2"}]`, 2))
	}))
	defer server.Close()

	clock := newSequenceClock(
		time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC),
		time.Date(2026, time.July, 10, 12, 0, 0, int(125*time.Millisecond), time.UTC),
	)
	client, clientError := NewHTTPClient(HTTPClientConfig{
		Endpoint:       server.URL + "/api/public/search",
		RequestTimeout: time.Second,
		CandidateLimit: 20,
		Now:            clock.Now,
	})
	if clientError != nil {
		t.Fatalf("NewHTTPClient() error = %v", clientError)
	}

	observation, searchError := client.Search(context.Background(), Query{
		QueryID: "jobs.assistant",
		Text:    `"assistente social" OR saúde -estágio`,
		Types:   []models.ItemType{models.TypeCourse, models.TypeJob},
		Filters: models.SearchFilters{
			Modalidade:        "hibrido",
			Bairro:            "Rio Comprido",
			Orgao:             "SMAS",
			Turno:             "noturno",
			RegimeContratacao: "clt",
			ModeloTrabalho:    "remoto",
			PCD:               &trueValue,
			CanalAtendimento:  "digital",
			Tema:              "Saúde",
			Segmento:          "Comércio",
		},
	})
	if searchError != nil {
		t.Fatalf("Search() error = %v", searchError)
	}
	if observation.SearchID != validSearchID || observation.RankerVersion != testRankerVersion() {
		t.Errorf("observation contract fields = %+v", observation)
	}
	if observation.CatalogRevision != "catalog-revision-1" || observation.EffectivePipeline != models.SearchPipelineLexical || observation.Degraded {
		t.Errorf("observation provenance = %+v", observation)
	}
	if observation.RankerDescriptorHash != testRankerDescriptorHash() {
		t.Errorf("ranker descriptor hash = %q", observation.RankerDescriptorHash)
	}
	if wantDocuments := []DocumentKey{testDocument("jobs", "job-1"), testDocument("courses", "course-2")}; !reflect.DeepEqual(observation.Documents, wantDocuments) {
		t.Errorf("Documents = %v, want %v", observation.Documents, wantDocuments)
	}
	if observation.Latency != 125*time.Millisecond {
		t.Errorf("Latency = %s", observation.Latency)
	}
}

func TestDecodePublicSearchResponseRejectsContractViolations(t *testing.T) {
	validResponse := validPublicSearchResponseJSON(10, `[{"source":"salesforce","source_id":"source-1"}]`, 1)
	firstCanonicalEntityID := testCanonicalEntityID(1)
	testCases := []struct {
		name         string
		responseBody string
		wantCode     string
	}{
		{name: "invalid JSON", responseBody: `{`, wantCode: "invalid_json"},
		{name: "trailing JSON", responseBody: validResponse + `{}`, wantCode: "trailing_json"},
		{name: "missing search ID", responseBody: strings.Replace(validResponse, fmt.Sprintf(`"search_id":%q,`, validSearchID), "", 1), wantCode: "invalid_search_id"},
		{name: "invalid search ID", responseBody: strings.Replace(validResponse, validSearchID, "not-a-uuid", 1), wantCode: "invalid_search_id"},
		{name: "missing ranker version", responseBody: strings.Replace(validResponse, fmt.Sprintf(`"ranker_version":%q,`, testRankerVersion()), "", 1), wantCode: "invalid_ranker_version"},
		{name: "non-canonical ranker version", responseBody: strings.Replace(validResponse, testRankerVersion(), "ranker version", 1), wantCode: "invalid_ranker_version"},
		{name: "missing ranker descriptor", responseBody: removeJSONObjectField(validResponse, "ranker_descriptor"), wantCode: "missing_ranker_descriptor"},
		{name: "descriptor mismatch", responseBody: strings.Replace(validResponse, testRankerVersion(), "ranker-v1-deadbeefdead", 1), wantCode: "ranker_descriptor_mismatch"},
		{name: "missing deduplication descriptor", responseBody: removeNestedJSONObjectField(validResponse, "ranker_descriptor", "deduplication_version"), wantCode: "invalid_ranker_descriptor"},
		{name: "invalid semantic distance", responseBody: replaceNestedJSONNumberField(validResponse, "ranker_descriptor", "maximum_semantic_distance", 0), wantCode: "invalid_ranker_descriptor"},
		{name: "missing catalog revision", responseBody: strings.Replace(validResponse, `"catalog_revision":"catalog-revision-1",`, "", 1), wantCode: "invalid_catalog_revision"},
		{name: "invalid effective pipeline", responseBody: strings.Replace(validResponse, `"effective_pipeline":"lexical"`, `"effective_pipeline":"unknown"`, 1), wantCode: "invalid_effective_pipeline"},
		{name: "missing degraded state", responseBody: strings.Replace(validResponse, `"degraded":false,`, "", 1), wantCode: "missing_degraded"},
		{name: "invalid pagination", responseBody: strings.Replace(validResponse, `"page":1`, `"page":2`, 1), wantCode: "invalid_pagination"},
		{name: "missing facets", responseBody: removeJSONObjectField(validResponse, "facets"), wantCode: "missing_facets"},
		{name: "invalid facet version", responseBody: strings.Replace(validResponse, models.SearchFacetVersion, "catalog-facets-v0", 1), wantCode: "invalid_facets"},
		{name: "facet count above total", responseBody: strings.Replace(validResponse, `"types":[]`, `"types":[{"value":"service","label":"Service","count":2}]`, 1), wantCode: "invalid_facets"},
		{name: "facet values outside binary tie order", responseBody: strings.Replace(validResponse, `"types":[]`, `"types":[{"value":"service","label":"Service","count":1},{"value":"course","label":"Course","count":1}]`, 1), wantCode: "invalid_facets"},
		{name: "unavailable facets with values", responseBody: strings.Replace(strings.Replace(validResponse, `"scope":"retrieval_candidates"`, `"scope":"unavailable"`, 1), `"types":[]`, `"types":[{"value":"service","label":"Service","count":1}]`, 1), wantCode: "invalid_facets"},
		{name: "missing items", responseBody: removeJSONObjectField(validResponse, "items"), wantCode: "missing_items"},
		{name: "total below items", responseBody: strings.Replace(validResponse, `"total":1`, `"total":0`, 1), wantCode: "invalid_total"},
		{name: "incomplete first page", responseBody: validPublicSearchResponseJSON(10, `[{"source":"salesforce","source_id":"source-1"}]`, 2), wantCode: "incomplete_first_page"},
		{name: "invalid source", responseBody: strings.Replace(validResponse, `"source":"salesforce"`, `"source":"unknown"`, 1), wantCode: "invalid_source"},
		{name: "missing source ID", responseBody: strings.Replace(validResponse, `,"source_id":"source-1"`, "", 1), wantCode: "missing_source_id"},
		{name: "missing canonical ID", responseBody: strings.Replace(validResponse, fmt.Sprintf(`"canonical_id":%q,`, firstCanonicalEntityID), "", 1), wantCode: "invalid_canonical_id"},
		{name: "invalid canonical ID", responseBody: strings.Replace(validResponse, firstCanonicalEntityID, "entity-v1:invalid", 1), wantCode: "invalid_canonical_id"},
		{name: "duplicate document", responseBody: validPublicSearchResponseJSON(10, `[{"source":"salesforce","source_id":"same"},{"source":"salesforce","source_id":"same"}]`, 2), wantCode: "duplicate_document"},
		{name: "duplicate canonical entity", responseBody: strings.Replace(
			validPublicSearchResponseJSON(10, `[{"source":"salesforce","source_id":"one"},{"source":"typesense","source_id":"two"}]`, 2),
			testCanonicalEntityID(2),
			testCanonicalEntityID(1),
			1,
		), wantCode: "duplicate_canonical_entity"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, decodeError := decodePublicSearchResponse([]byte(testCase.responseBody), 10)
			var contractError *SearchFailureError
			if !errors.As(decodeError, &contractError) {
				t.Fatalf("error = %v, want SearchFailureError", decodeError)
			}
			if contractError.Stage != FailureStageContract || contractError.Code != testCase.wantCode {
				t.Fatalf("error = %+v, want code %q", contractError, testCase.wantCode)
			}
		})
	}
}

func TestDecodePublicSearchResponseUsesCompositeDocumentIdentity(t *testing.T) {
	responseBody := validPublicSearchResponseJSON(10, `[{"source":"salesforce","source_id":"same"},{"source":"typesense","source_id":"same"}]`, 2)

	if _, decodeError := decodePublicSearchResponse([]byte(responseBody), 10); decodeError != nil {
		t.Fatalf("decodePublicSearchResponse() error = %v", decodeError)
	}
}

func TestHTTPClientEnforcesResponseSizeBound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(responseWriter, strings.Repeat("x", 65))
	}))
	defer server.Close()
	client, clientError := NewHTTPClient(HTTPClientConfig{
		Endpoint:             server.URL + "/api/public/search",
		RequestTimeout:       time.Second,
		CandidateLimit:       10,
		MaximumResponseBytes: 64,
	})
	if clientError != nil {
		t.Fatalf("NewHTTPClient() error = %v", clientError)
	}

	_, searchError := client.Search(context.Background(), Query{QueryID: "response.bound", Text: "iptu"})
	var contractError *SearchFailureError
	if !errors.As(searchError, &contractError) || contractError.Code != "response_too_large" {
		t.Fatalf("Search() error = %v", searchError)
	}
}

func TestHTTPClientEnforcesRequestTimeout(t *testing.T) {
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-releaseHandler
	}))
	defer func() {
		close(releaseHandler)
		server.Close()
	}()
	client, clientError := NewHTTPClient(HTTPClientConfig{
		Endpoint:       server.URL + "/api/public/search",
		RequestTimeout: 10 * time.Millisecond,
		CandidateLimit: 10,
	})
	if clientError != nil {
		t.Fatalf("NewHTTPClient() error = %v", clientError)
	}

	_, searchError := client.Search(context.Background(), Query{QueryID: "timeout", Text: "iptu"})
	var transportError *SearchFailureError
	if !errors.As(searchError, &transportError) || transportError.Stage != FailureStageTransport {
		t.Fatalf("Search() error = %v", searchError)
	}
}

func TestNewHTTPClientRequiresTLSOutsideLoopback(t *testing.T) {
	if _, clientError := NewHTTPClient(HTTPClientConfig{
		Endpoint:       "http://example.com/api/public/search",
		CandidateLimit: 10,
	}); clientError == nil {
		t.Fatal("NewHTTPClient() accepted plain HTTP for a non-loopback host")
	}
	if _, clientError := NewHTTPClient(HTTPClientConfig{
		Endpoint:       "https://example.com/api/public/search",
		CandidateLimit: 10,
	}); clientError != nil {
		t.Fatalf("NewHTTPClient() rejected HTTPS endpoint: %v", clientError)
	}
}

func TestNewHTTPClientBlocksRedirectsFromInjectedClient(t *testing.T) {
	var redirectedRequests atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		redirectedRequests.Add(1)
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer redirectTarget.Close()
	redirectSource := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		http.Redirect(responseWriter, request, redirectTarget.URL, http.StatusFound)
	}))
	defer redirectSource.Close()
	injectedClient := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return nil }}
	client, clientError := NewHTTPClient(HTTPClientConfig{
		Endpoint:       redirectSource.URL + "/api/public/search",
		CandidateLimit: 10,
		Client:         injectedClient,
	})
	if clientError != nil {
		t.Fatalf("NewHTTPClient() error = %v", clientError)
	}

	_, searchError := client.Search(context.Background(), Query{QueryID: "redirect", Text: "iptu"})
	var transportError *SearchFailureError
	if !errors.As(searchError, &transportError) || transportError.Code != "http_status" {
		t.Fatalf("Search() error = %v", searchError)
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("injected client followed redirect %d time(s)", redirectedRequests.Load())
	}
}

func validPublicSearchResponseJSON(candidateLimit int, itemsJSON string, total int) string {
	var searchItems []map[string]any
	if decodeError := json.Unmarshal([]byte(itemsJSON), &searchItems); decodeError != nil {
		panic(decodeError)
	}
	for searchItemIndex := range searchItems {
		if _, canonicalIDExists := searchItems[searchItemIndex]["canonical_id"]; !canonicalIDExists {
			searchItems[searchItemIndex]["canonical_id"] = testCanonicalEntityID(searchItemIndex + 1)
		}
	}
	encodedItems, encodeItemsError := json.Marshal(searchItems)
	if encodeItemsError != nil {
		panic(encodeItemsError)
	}
	encodedDescriptor, encodeError := json.Marshal(testRankerDescriptor())
	if encodeError != nil {
		panic(encodeError)
	}
	return fmt.Sprintf(
		`{"search_id":%q,"ranker_version":%q,"ranker_descriptor":%s,"catalog_revision":"catalog-revision-1","effective_pipeline":"lexical","degraded":false,"total":%d,"page":1,"per_page":%d,"facets":{"version":%q,"scope":"retrieval_candidates","types":[],"modalidades":[],"bairros":[],"organizations":[]},"items":%s}`,
		validSearchID,
		testRankerVersion(),
		encodedDescriptor,
		total,
		candidateLimit,
		models.SearchFacetVersion,
		encodedItems,
	)
}

func testCanonicalEntityID(entityIndex int) string {
	return fmt.Sprintf("entity-v1:%064x", entityIndex)
}

func testRankerDescriptor() models.SearchRankerDescriptor {
	return models.SearchRankerDescriptor{
		SchemaVersion:           "search-ranker/v1",
		BaseVersion:             "ranker-v1",
		RetrievalVersion:        "postgres-weighted-rrf-v2",
		QueryExpansionVersion:   "synonyms-v1",
		DeduplicationVersion:    "canonical-entity-v1",
		CandidatePoolSize:       40,
		TrigramThreshold:        0.18,
		MaximumSemanticDistance: 1,
		ReciprocalRankK:         60,
		Weights: models.SearchRetrievalWeights{
			Exact:    3,
			FullText: 1,
			Trigram:  1,
			Semantic: 1,
			HyDE:     0.5,
		},
	}
}

func testRankerDescriptorHash() string {
	encodedDescriptor, encodeError := json.Marshal(testRankerDescriptor())
	if encodeError != nil {
		panic(encodeError)
	}
	descriptorDigest := sha256.Sum256(encodedDescriptor)
	return fmt.Sprintf("%x", descriptorDigest)
}

func testRankerVersion() string {
	return testRankerDescriptor().BaseVersion + "-" + testRankerDescriptorHash()[:12]
}

func removeJSONObjectField(encodedObject string, fieldName string) string {
	var objectFields map[string]json.RawMessage
	if decodeError := json.Unmarshal([]byte(encodedObject), &objectFields); decodeError != nil {
		panic(decodeError)
	}
	delete(objectFields, fieldName)
	reencodedObject, encodeError := json.Marshal(objectFields)
	if encodeError != nil {
		panic(encodeError)
	}
	return string(reencodedObject)
}

func removeNestedJSONObjectField(encodedObject string, objectName string, fieldName string) string {
	var objectFields map[string]json.RawMessage
	if decodeError := json.Unmarshal([]byte(encodedObject), &objectFields); decodeError != nil {
		panic(decodeError)
	}
	var nestedFields map[string]json.RawMessage
	if decodeError := json.Unmarshal(objectFields[objectName], &nestedFields); decodeError != nil {
		panic(decodeError)
	}
	delete(nestedFields, fieldName)
	encodedNestedFields, encodeError := json.Marshal(nestedFields)
	if encodeError != nil {
		panic(encodeError)
	}
	objectFields[objectName] = encodedNestedFields
	reencodedObject, encodeError := json.Marshal(objectFields)
	if encodeError != nil {
		panic(encodeError)
	}
	return string(reencodedObject)
}

func replaceNestedJSONNumberField(
	encodedObject string,
	objectName string,
	fieldName string,
	replacement float64,
) string {
	var objectFields map[string]json.RawMessage
	if decodeError := json.Unmarshal([]byte(encodedObject), &objectFields); decodeError != nil {
		panic(decodeError)
	}
	var nestedFields map[string]json.RawMessage
	if decodeError := json.Unmarshal(objectFields[objectName], &nestedFields); decodeError != nil {
		panic(decodeError)
	}
	encodedReplacement, encodeError := json.Marshal(replacement)
	if encodeError != nil {
		panic(encodeError)
	}
	nestedFields[fieldName] = encodedReplacement
	encodedNestedFields, encodeError := json.Marshal(nestedFields)
	if encodeError != nil {
		panic(encodeError)
	}
	objectFields[objectName] = encodedNestedFields
	reencodedObject, encodeError := json.Marshal(objectFields)
	if encodeError != nil {
		panic(encodeError)
	}
	return string(reencodedObject)
}

type sequenceClock struct {
	mutex sync.Mutex
	times []time.Time
	index int
}

func newSequenceClock(times ...time.Time) *sequenceClock {
	return &sequenceClock{times: times}
}

func (clock *sequenceClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	if clock.index >= len(clock.times) {
		return clock.times[len(clock.times)-1]
	}
	currentTime := clock.times[clock.index]
	clock.index++
	return currentTime
}
