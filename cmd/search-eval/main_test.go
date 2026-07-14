package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prefeitura-rio/app-catalogo/internal/evaluation"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestRunEvaluatesDatasetWithoutLeakingRawQuery(t *testing.T) {
	rawQuery := "private citizen query saúde"
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.RawQuery != "" {
			t.Errorf("request target = %s %s", request.Method, request.URL.String())
		}
		var requestBody models.SearchRequestBody
		if decodeError := json.NewDecoder(request.Body).Decode(&requestBody); decodeError != nil || requestBody.Q != rawQuery {
			t.Errorf("request body = %+v, error = %v", requestBody, decodeError)
		}
		if request.Header.Get("Authorization") != "" {
			t.Error("evaluation request must not carry authorization")
		}
		fmt.Fprint(responseWriter, successfulSearchResponse(5))
	}))
	defer server.Close()

	datasetPath := filepath.Join(t.TempDir(), "search-evaluation.jsonl")
	encodedDataset := fmt.Sprintf(`{"query_id":"private.intent","query":%q,"slices":["intent:service"],"qrels":[{"entity_id":"service","grade":3,"documents":[{"source":"salesforce","source_id":"service-1"}]}]}`+"\n", rawQuery)
	if writeError := os.WriteFile(datasetPath, []byte(encodedDataset), 0o600); writeError != nil {
		t.Fatalf("write dataset: %v", writeError)
	}
	qualityPolicyPath, encodedQualityPolicy := writeTestQualityPolicy(t, filepath.Dir(datasetPath),
		`{"id":"overall-recall","scope":"overall","metric":"recall","cutoff":1,"operator":"gte","value":1}`)

	var standardOutput bytes.Buffer
	var standardError bytes.Buffer
	exitCode := run([]string{
		"-dataset", datasetPath,
		"-endpoint", server.URL + "/api/public/search",
		"-output", "-",
		"-timeout", "1s",
		"-concurrency", "1",
		"-candidate-limit", "5",
		"-cutoffs", "1,3",
		"-latency-quantiles", "0.5",
		"-run-timestamp", "2026-07-10T12:00:00Z",
		"-quality-policy", qualityPolicyPath,
	}, &standardOutput, &standardError)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr=%s", exitCode, standardError.String())
	}
	if strings.Contains(standardOutput.String(), rawQuery) {
		t.Fatalf("report leaked raw query: %s", standardOutput.String())
	}
	var report evaluation.Report
	if decodeError := json.Unmarshal(standardOutput.Bytes(), &report); decodeError != nil {
		t.Fatalf("decode report: %v", decodeError)
	}
	if !report.Complete || report.RunTimestamp != "2026-07-10T12:00:00Z" {
		t.Errorf("report = %+v", report)
	}
	if report.Overall.MetricsAtCutoff["1"].Recall != 1 {
		t.Errorf("metrics = %+v", report.Overall.MetricsAtCutoff)
	}
	policyDigest := sha256.Sum256(encodedQualityPolicy)
	if report.QualityPolicy == nil || report.QualityPolicy.ID != "example-non-production" ||
		report.QualityPolicy.SHA256 != fmt.Sprintf("%x", policyDigest) ||
		report.QualityGate == nil || !report.QualityGate.Passed || report.RunConfiguration.RequestTimeout != "1s" ||
		report.RunConfiguration.MaximumQualityPolicyBytes != evaluation.DefaultMaximumQualityPolicyBytes {
		t.Errorf("report provenance = %+v", report)
	}
}

func TestRunQualityPolicyFailureIsFatalDespiteContinuation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(responseWriter, successfulSearchResponse(5))
	}))
	defer server.Close()
	temporaryDirectory := t.TempDir()
	datasetPath := filepath.Join(temporaryDirectory, "search-evaluation.jsonl")
	encodedDataset := `{"query_id":"quality.failure","query":"private unmatched query","qrels":[{"entity_id":"different-service","grade":3,"documents":[{"source":"salesforce","source_id":"different-service"}]}]}` + "\n"
	if writeError := os.WriteFile(datasetPath, []byte(encodedDataset), 0o600); writeError != nil {
		t.Fatalf("write dataset: %v", writeError)
	}
	qualityPolicyPath, _ := writeTestQualityPolicy(t, temporaryDirectory,
		`{"id":"overall-recall","scope":"overall","metric":"recall","cutoff":1,"operator":"gte","value":1}`)

	var standardOutput bytes.Buffer
	var standardError bytes.Buffer
	exitCode := run([]string{
		"-dataset", datasetPath,
		"-endpoint", server.URL + "/api/public/search",
		"-output", "-",
		"-candidate-limit", "5",
		"-cutoffs", "1",
		"-latency-quantiles", "0.5",
		"-continue-on-error=true",
		"-quality-policy", qualityPolicyPath,
	}, &standardOutput, &standardError)
	if exitCode == 0 {
		t.Fatalf("run() accepted failing quality policy: %s", standardOutput.String())
	}
	if !strings.Contains(standardError.String(), "search quality gate failed") {
		t.Fatalf("stderr = %s", standardError.String())
	}
	var report evaluation.Report
	if decodeError := json.Unmarshal(standardOutput.Bytes(), &report); decodeError != nil {
		t.Fatalf("decode report: %v", decodeError)
	}
	if report.QualityGate == nil || report.QualityGate.Passed || report.QualityGate.FailedChecks != 1 {
		t.Fatalf("quality gate = %+v", report.QualityGate)
	}
}

func writeTestQualityPolicy(t *testing.T, directory, assertion string) (string, []byte) {
	t.Helper()
	encodedPolicy := []byte(fmt.Sprintf(`{
  "schema_version": "search-quality-policy/v1",
  "id": "example-non-production",
  "version": "test-v1",
  "requirements": {
    "complete": true,
    "catalog_revision": "catalog-revision-1",
    "allowed_pipelines": ["lexical"],
    "degraded_responses": 0
  },
  "assertions": [%s]
}
`, assertion))
	policyPath := filepath.Join(directory, "quality-policy.json")
	if writeError := os.WriteFile(policyPath, encodedPolicy, 0o600); writeError != nil {
		t.Fatalf("write quality policy: %v", writeError)
	}
	return policyPath, encodedPolicy
}

func successfulSearchResponse(candidateLimit int) string {
	descriptor := models.SearchRankerDescriptor{
		SchemaVersion:           "search-ranker/v1",
		BaseVersion:             "ranker-v1",
		RetrievalVersion:        "postgres-weighted-rrf-v1",
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
	encodedDescriptor, encodeError := json.Marshal(descriptor)
	if encodeError != nil {
		panic(encodeError)
	}
	descriptorDigest := sha256.Sum256(encodedDescriptor)
	canonicalEntityDigest := sha256.Sum256([]byte("service"))
	return fmt.Sprintf(
		`{"search_id":"550e8400-e29b-41d4-a716-446655440000","ranker_version":"ranker-v1-%x","ranker_descriptor":%s,"catalog_revision":"catalog-revision-1","effective_pipeline":"lexical","degraded":false,"total":1,"page":1,"per_page":%d,"facets":{"version":"catalog-facets-v1","scope":"retrieval_candidates","types":[],"modalidades":[],"bairros":[],"organizations":[]},"items":[{"canonical_id":"entity-v1:%x","source":"salesforce","source_id":"service-1"}]}`,
		descriptorDigest[:6],
		encodedDescriptor,
		candidateLimit,
		canonicalEntityDigest,
	)
}

func TestRunRejectsMalformedDatasetWithoutLeakingLine(t *testing.T) {
	rawQuery := "private malformed query"
	var networkRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		networkRequests.Add(1)
		responseWriter.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	datasetPath := filepath.Join(t.TempDir(), "malformed.jsonl")
	encodedDataset := `{"query_id":"invalid id","query":"` + rawQuery + `","qrels":[]}` + "\n" +
		`{"query_id":"valid.query","query":"iptu","qrels":[{"entity_id":"iptu","grade":3,"documents":[{"source":"salesforce","source_id":"service-1"}]}]}` + "\n"
	if writeError := os.WriteFile(datasetPath, []byte(encodedDataset), 0o600); writeError != nil {
		t.Fatalf("write dataset: %v", writeError)
	}

	var standardOutput bytes.Buffer
	var standardError bytes.Buffer
	exitCode := run([]string{
		"-dataset", datasetPath,
		"-endpoint", server.URL + "/api/public/search",
		"-continue-on-error=true",
	}, &standardOutput, &standardError)
	if exitCode == 0 {
		t.Fatal("run() accepted malformed dataset")
	}
	if standardOutput.Len() != 0 {
		t.Fatalf("unexpected stdout: %s", standardOutput.String())
	}
	if strings.Contains(standardError.String(), rawQuery) {
		t.Fatalf("stderr leaked raw query: %s", standardError.String())
	}
	if networkRequests.Load() != 0 {
		t.Fatalf("invalid dataset triggered %d network request(s)", networkRequests.Load())
	}
}

func TestWriteReportAtomicallyCreatesPrivateFile(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "report.json")
	encodedReport := []byte("{\"complete\":true}\n")

	if writeError := writeReport(reportPath, encodedReport, &bytes.Buffer{}); writeError != nil {
		t.Fatalf("writeReport() error = %v", writeError)
	}
	writtenReport, readError := os.ReadFile(reportPath)
	if readError != nil {
		t.Fatalf("read report: %v", readError)
	}
	if !bytes.Equal(writtenReport, encodedReport) {
		t.Errorf("report = %q", writtenReport)
	}
	reportInfo, statError := os.Stat(reportPath)
	if statError != nil {
		t.Fatalf("stat report: %v", statError)
	}
	if permissions := reportInfo.Mode().Perm(); permissions != 0o600 {
		t.Errorf("permissions = %o, want 600", permissions)
	}
}

func TestRunRequiresExplicitContinuationForResponseFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(responseWriter, `{"search_id":"550e8400-e29b-41d4-a716-446655440000","ranker_version":"ranker-v1","total":1,"page":1,"per_page":5,"items":[{}]}`)
	}))
	defer server.Close()
	datasetPath := filepath.Join(t.TempDir(), "search-evaluation.jsonl")
	if writeError := os.WriteFile(datasetPath, []byte(`{"query_id":"contract.failure","query":"iptu","qrels":[{"entity_id":"service","grade":1,"documents":[{"source":"salesforce","source_id":"service-1"}]}]}`+"\n"), 0o600); writeError != nil {
		t.Fatalf("write dataset: %v", writeError)
	}
	baseArguments := []string{
		"-dataset", datasetPath,
		"-endpoint", server.URL + "/api/public/search",
		"-output", "-",
		"-candidate-limit", "5",
		"-cutoffs", "1",
		"-latency-quantiles", "0.5",
	}

	var failedOutput bytes.Buffer
	var failedError bytes.Buffer
	if exitCode := run(baseArguments, &failedOutput, &failedError); exitCode == 0 {
		t.Fatalf("run() succeeded without explicit continuation: %s", failedOutput.String())
	}
	if !strings.Contains(failedOutput.String(), `"complete": false`) {
		t.Fatalf("failed report is missing: %s", failedOutput.String())
	}

	var continuedOutput bytes.Buffer
	var continuedError bytes.Buffer
	continuedArguments := append(append([]string{}, baseArguments...), "-continue-on-error=true")
	if exitCode := run(continuedArguments, &continuedOutput, &continuedError); exitCode != 0 {
		t.Fatalf("continued run exit code = %d, stderr=%s", exitCode, continuedError.String())
	}
	if !strings.Contains(continuedOutput.String(), `"complete": false`) {
		t.Fatalf("continued report is missing failure status: %s", continuedOutput.String())
	}
}
