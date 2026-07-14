package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestLoadJSONLValidatesCanonicalDatasetAndHash(t *testing.T) {
	encodedDataset := strings.Join([]string{
		`{"query_id":"jobs.admin","query":"  assistente   administrativo ","types":["service","job","service"],"filters":{"modelo_trabalho":"HÍBRIDO","pcd":false},"slices":["intent:job","temporal:holdout","intent:job"],"qrels":[{"entity_id":"job-admin","grade":3,"documents":[{"source":"jobs","source_id":"job-123"}]},{"entity_id":"job-assistant","grade":1,"documents":[{"source":"jobs","source_id":"job-456"}]}]}`,
		`{"query_id":"services.iptu","query":"iptu","qrels":[{"entity_id":"iptu-service","grade":3,"documents":[{"source":"salesforce","source_id":"service-1"},{"source":"typesense","source_id":"service-1"}]}]}`,
	}, "\n") + "\n"

	dataset, failures, loadError := LoadJSONL(strings.NewReader(encodedDataset), DatasetLoadOptions{})
	if loadError != nil {
		t.Fatalf("LoadJSONL() error = %v", loadError)
	}
	if len(failures) != 0 {
		t.Fatalf("failures = %+v", failures)
	}
	expectedHashBytes := sha256.Sum256([]byte(encodedDataset))
	if expectedHash := hex.EncodeToString(expectedHashBytes[:]); dataset.Hash != expectedHash {
		t.Errorf("Hash = %q, want %q", dataset.Hash, expectedHash)
	}
	if len(dataset.Queries) != 2 {
		t.Fatalf("query count = %d", len(dataset.Queries))
	}
	firstQuery := dataset.Queries[0]
	if firstQuery.Text != "assistente administrativo" {
		t.Errorf("Text = %q", firstQuery.Text)
	}
	if wantTypes := []models.ItemType{models.TypeJob, models.TypeService}; !reflect.DeepEqual(firstQuery.Types, wantTypes) {
		t.Errorf("Types = %v, want %v", firstQuery.Types, wantTypes)
	}
	if wantSlices := []string{"intent:job", "temporal:holdout"}; !reflect.DeepEqual(firstQuery.Slices, wantSlices) {
		t.Errorf("Slices = %v, want %v", firstQuery.Slices, wantSlices)
	}
	if firstQuery.Filters.ModeloTrabalho != "hibrido" || firstQuery.Filters.PCD == nil || *firstQuery.Filters.PCD {
		t.Errorf("Filters were not canonicalized: %+v", firstQuery.Filters)
	}
}

func TestLoadJSONLRejectsMalformedDataset(t *testing.T) {
	validLine := `{"query_id":"valid.query","query":"iptu","qrels":[{"entity_id":"iptu","grade":3,"documents":[{"source":"salesforce","source_id":"service-1"}]}]}`
	testCases := []struct {
		name         string
		encodedLines []byte
		wantCode     string
	}{
		{name: "invalid UTF-8", encodedLines: append([]byte{0xff, '\n'}, []byte(validLine+"\n")...), wantCode: "invalid_utf8"},
		{name: "invalid JSON", encodedLines: []byte("{\n" + validLine + "\n"), wantCode: "invalid_json"},
		{name: "duplicate JSON key", encodedLines: []byte(`{"query_id":"first","query_id":"second","query":"iptu","qrels":[]}` + "\n" + validLine + "\n"), wantCode: "duplicate_json_key"},
		{name: "unknown field", encodedLines: []byte(`{"query_id":"unknown.field","query":"iptu","unknown":true,"qrels":[]}` + "\n" + validLine + "\n"), wantCode: "invalid_json"},
		{name: "trailing JSON", encodedLines: []byte(`{"query_id":"trailing","query":"iptu","qrels":[]} {}` + "\n" + validLine + "\n"), wantCode: "invalid_json"},
		{name: "invalid query ID", encodedLines: []byte(`{"query_id":"invalid id","query":"iptu","qrels":[]}` + "\n" + validLine + "\n"), wantCode: "invalid_query_id"},
		{name: "duplicate query ID", encodedLines: []byte(validLine + "\n" + validLine + "\n"), wantCode: "duplicate_query_id"},
		{name: "empty query", encodedLines: []byte(`{"query_id":"empty.query","query":"   ","qrels":[]}` + "\n" + validLine + "\n"), wantCode: "empty_query"},
		{name: "invalid type", encodedLines: []byte(`{"query_id":"invalid.type","query":"iptu","types":["event"],"qrels":[]}` + "\n" + validLine + "\n"), wantCode: "invalid_search_request"},
		{name: "invalid filter", encodedLines: []byte(`{"query_id":"invalid.filter","query":"iptu","filters":{"turno":"madrugada"},"qrels":[]}` + "\n" + validLine + "\n"), wantCode: "invalid_search_request"},
		{name: "missing qrels", encodedLines: []byte(`{"query_id":"missing.qrels","query":"iptu"}` + "\n" + validLine + "\n"), wantCode: "missing_qrels"},
		{name: "missing grade", encodedLines: []byte(`{"query_id":"missing.grade","query":"iptu","qrels":[{"entity_id":"ungraded","documents":[{"source":"salesforce","source_id":"service-1"}]},{"entity_id":"positive","grade":3,"documents":[{"source":"salesforce","source_id":"service-2"}]}]}` + "\n" + validLine + "\n"), wantCode: "missing_relevance_grade"},
		{name: "negative grade", encodedLines: []byte(`{"query_id":"negative.grade","query":"iptu","qrels":[{"entity_id":"iptu","grade":-1,"documents":[{"source":"salesforce","source_id":"service-1"}]}]}` + "\n" + validLine + "\n"), wantCode: "invalid_relevance_grade"},
		{name: "grade above scale", encodedLines: []byte(`{"query_id":"high.grade","query":"iptu","qrels":[{"entity_id":"iptu","grade":4,"documents":[{"source":"salesforce","source_id":"service-1"}]}]}` + "\n" + validLine + "\n"), wantCode: "invalid_relevance_grade"},
		{name: "invalid entity", encodedLines: []byte(`{"query_id":"bad.entity","query":"iptu","qrels":[{"entity_id":"invalid entity","grade":3,"documents":[{"source":"salesforce","source_id":"service-1"}]}]}` + "\n" + validLine + "\n"), wantCode: "invalid_entity_id"},
		{name: "duplicate entity", encodedLines: []byte(`{"query_id":"duplicate.entity","query":"iptu","qrels":[{"entity_id":"same","grade":3,"documents":[{"source":"salesforce","source_id":"service-1"}]},{"entity_id":"same","grade":2,"documents":[{"source":"salesforce","source_id":"service-2"}]}]}` + "\n" + validLine + "\n"), wantCode: "duplicate_entity"},
		{name: "missing documents", encodedLines: []byte(`{"query_id":"missing.documents","query":"iptu","qrels":[{"entity_id":"iptu","grade":3,"documents":[]}]}` + "\n" + validLine + "\n"), wantCode: "missing_documents"},
		{name: "invalid source", encodedLines: []byte(`{"query_id":"bad.source","query":"iptu","qrels":[{"entity_id":"iptu","grade":3,"documents":[{"source":"unknown","source_id":"service-1"}]}]}` + "\n" + validLine + "\n"), wantCode: "invalid_source"},
		{name: "non-canonical source ID", encodedLines: []byte(`{"query_id":"bad.source.id","query":"iptu","qrels":[{"entity_id":"iptu","grade":3,"documents":[{"source":"salesforce","source_id":" service-1 "}]}]}` + "\n" + validLine + "\n"), wantCode: "invalid_source_id"},
		{name: "duplicate document", encodedLines: []byte(`{"query_id":"duplicate.document","query":"iptu","qrels":[{"entity_id":"iptu","grade":3,"documents":[{"source":"salesforce","source_id":"service-1"},{"source":"salesforce","source_id":"service-1"}]}]}` + "\n" + validLine + "\n"), wantCode: "duplicate_document"},
		{name: "crossed document", encodedLines: []byte(`{"query_id":"crossed.document","query":"iptu","qrels":[{"entity_id":"first","grade":3,"documents":[{"source":"salesforce","source_id":"service-1"}]},{"entity_id":"second","grade":2,"documents":[{"source":"salesforce","source_id":"service-1"}]}]}` + "\n" + validLine + "\n"), wantCode: "crossed_document"},
		{name: "no positive entity", encodedLines: []byte(`{"query_id":"no.positive","query":"iptu","qrels":[{"entity_id":"irrelevant","grade":0,"documents":[{"source":"salesforce","source_id":"service-1"}]}]}` + "\n" + validLine + "\n"), wantCode: "no_positive_qrels"},
		{name: "invalid slice", encodedLines: []byte(`{"query_id":"bad.slice","query":"iptu","slices":["intent job"],"qrels":[{"entity_id":"iptu","grade":3,"documents":[{"source":"salesforce","source_id":"service-1"}]}]}` + "\n" + validLine + "\n"), wantCode: "invalid_slice"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, failures, loadError := LoadJSONL(bytes.NewReader(testCase.encodedLines), DatasetLoadOptions{})
			if loadError == nil {
				t.Fatal("LoadJSONL() accepted a malformed dataset")
			}
			if !containsFailureCode(failures, testCase.wantCode) {
				t.Fatalf("failures = %+v, want code %q", failures, testCase.wantCode)
			}
		})
	}
}

func TestLoadJSONLRejectsPartialDataset(t *testing.T) {
	encodedDataset := strings.Join([]string{
		`{"query_id":"invalid.query","query":"iptu","types":["event"],"qrels":[]}`,
		`{"query_id":"valid.query","query":"curso","qrels":[{"entity_id":"course","grade":2,"documents":[{"source":"courses","source_id":"course-1"}]}]}`,
	}, "\n") + "\n"

	dataset, failures, loadError := LoadJSONL(strings.NewReader(encodedDataset), DatasetLoadOptions{})
	if loadError == nil {
		t.Fatal("LoadJSONL() accepted a partially invalid dataset")
	}
	if len(dataset.Queries) != 1 || dataset.Queries[0].QueryID != "valid.query" {
		t.Fatalf("Queries = %+v", dataset.Queries)
	}
	if len(failures) != 1 || failures[0].Code != "invalid_search_request" {
		t.Fatalf("Failures = %+v", failures)
	}
}

func TestLoadJSONLEnforcesConfiguredLineBound(t *testing.T) {
	encodedDataset := `{"query_id":"bounded.query","query":"iptu","qrels":[{"entity_id":"iptu","grade":3,"documents":[{"source":"salesforce","source_id":"service-1"}]}]}` + "\n"

	_, failures, loadError := LoadJSONL(strings.NewReader(encodedDataset), DatasetLoadOptions{MaximumLineBytes: 16})
	if loadError == nil {
		t.Fatal("LoadJSONL() accepted a line over the configured bound")
	}
	if !containsFailureCode(failures, "read_error") {
		t.Fatalf("failures = %+v", failures)
	}
}

func containsFailureCode(failures []Failure, code string) bool {
	for _, failure := range failures {
		if failure.Code == code {
			return true
		}
	}
	return false
}
