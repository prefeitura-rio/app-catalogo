package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

func TestLoadQualityPolicyBindsExactArtifactIdentityAndNormalizesOrder(t *testing.T) {
	t.Parallel()

	encodedPolicy := []byte(testQualityPolicyDocument(`
    {"id":"z-latency","scope":"overall","metric":"latency_ms","quantile":0.95,"operator":"lte","value":100},
    {"id":"a-recall","scope":"overall","metric":"recall","cutoff":10,"operator":"gte","value":0.8}`,
		`["lexical_reranked","lexical"]`))
	policy := mustLoadQualityPolicy(t, encodedPolicy, QualityPolicyLoadOptions{})

	expectedDigest := sha256.Sum256(encodedPolicy)
	if policy.Reference() != (QualityPolicyReference{
		SchemaVersion: QualityPolicySchemaVersion,
		ID:            "example-non-production",
		Version:       "test-v1",
		SHA256:        hex.EncodeToString(expectedDigest[:]),
	}) {
		t.Fatalf("policy reference = %+v", policy.Reference())
	}
	if assertionIDs := []string{policy.assertions[0].ID, policy.assertions[1].ID}; !reflect.DeepEqual(assertionIDs, []string{"a-recall", "z-latency"}) {
		t.Errorf("assertion order = %v", assertionIDs)
	}
	if !reflect.DeepEqual(policy.requirements.AllowedPipelines, []models.SearchPipeline{
		models.SearchPipelineLexical,
		models.SearchPipelineLexicalReranked,
	}) {
		t.Errorf("allowed pipelines = %v", policy.requirements.AllowedPipelines)
	}

	whitespaceVariant := append(bytes.Clone(encodedPolicy), ' ')
	variantPolicy := mustLoadQualityPolicy(t, whitespaceVariant, QualityPolicyLoadOptions{})
	if variantPolicy.Reference().SHA256 == policy.Reference().SHA256 {
		t.Fatal("different exact artifacts produced the same policy digest")
	}
}

func TestLoadQualityPolicyRejectsDuplicateKeys(t *testing.T) {
	t.Parallel()

	encodedPolicy := []byte(`{
  "schema_version":"search-quality-policy/v1",
  "id":"example-non-production",
  "version":"test-v1",
  "requirements":{
    "complete":true,
    "catalog_revision":"catalog-revision-1",
    "allowed_pipelines":["lexical"],
    "degraded_responses":0,
    "degraded_responses":0
  },
  "assertions":[{"id":"recall","scope":"overall","metric":"recall","cutoff":1,"operator":"gte","value":0.5}]
}`)
	_, loadError := LoadQualityPolicy(bytes.NewReader(encodedPolicy), QualityPolicyLoadOptions{})
	if loadError == nil || !strings.Contains(loadError.Error(), "duplicate JSON key") {
		t.Fatalf("LoadQualityPolicy() error = %v", loadError)
	}
}

func TestLoadQualityPolicyRejectsOversizedArtifactBeforeDecoding(t *testing.T) {
	t.Parallel()

	encodedPolicy := []byte(testQualityPolicyDocument(
		`{"id":"recall","scope":"overall","metric":"recall","cutoff":1,"operator":"gte","value":0.5}`,
		`["lexical"]`,
	))
	_, loadError := LoadQualityPolicy(bytes.NewReader(encodedPolicy), QualityPolicyLoadOptions{
		MaximumBytes: int64(len(encodedPolicy) - 1),
	})
	if loadError == nil || !strings.Contains(loadError.Error(), "byte bound") {
		t.Fatalf("LoadQualityPolicy() error = %v", loadError)
	}
	_, loadError = LoadQualityPolicy(bytes.NewReader(encodedPolicy), QualityPolicyLoadOptions{
		MaximumBytes: DefaultMaximumQualityPolicyBytes + 1,
	})
	if loadError == nil || !strings.Contains(loadError.Error(), "supported maximum") {
		t.Fatalf("LoadQualityPolicy() oversized bound error = %v", loadError)
	}
}

func TestDocumentedNonProductionPolicyExampleLoads(t *testing.T) {
	t.Parallel()

	examplePath := filepath.Join("..", "..", "docs", "search-quality-policy.example.non-production.json")
	exampleFile, openError := os.Open(examplePath)
	if openError != nil {
		t.Fatalf("open policy example: %v", openError)
	}
	policy, loadError := LoadQualityPolicy(exampleFile, QualityPolicyLoadOptions{})
	closeError := exampleFile.Close()
	if loadError != nil || closeError != nil {
		t.Fatalf("load policy example: %v", errors.Join(loadError, closeError))
	}
	policyReference := policy.Reference()
	if policyReference.ID != "example-non-production" || policyReference.Version != "illustrative-only-v1" {
		t.Fatalf("documented policy identity = %s@%s", policyReference.ID, policyReference.Version)
	}
}

func TestLoadQualityPolicyStrictlyValidatesVersionedCompleteContract(t *testing.T) {
	t.Parallel()

	validPolicy := testQualityPolicyDocument(
		`{"id":"recall","scope":"overall","metric":"recall","cutoff":1,"operator":"gte","value":0.5}`,
		`["lexical"]`,
	)
	testCases := map[string]string{
		"unknown field":       strings.Replace(validPolicy, `"version":"test-v1"`, `"version":"test-v1","unknown":true`, 1),
		"wrong schema":        strings.Replace(validPolicy, QualityPolicySchemaVersion, "search-quality-policy/v2", 1),
		"unversioned catalog": strings.Replace(validPolicy, "catalog-revision-1", "unversioned", 1),
		"incomplete report":   strings.Replace(validPolicy, `"complete":true`, `"complete":false`, 1),
		"missing requirement": strings.Replace(validPolicy, ",\n    \"degraded_responses\":0", "", 1),
		"degraded allowed":    strings.Replace(validPolicy, `"degraded_responses":0`, `"degraded_responses":1`, 1),
		"invalid pipeline":    strings.Replace(validPolicy, `"lexical"`, `"unknown"`, 1),
		"duplicate assertion id": testQualityPolicyDocument(`
    {"id":"recall","scope":"overall","metric":"recall","cutoff":1,"operator":"gte","value":0.5},
    {"id":"recall","scope":"overall","metric":"mrr","cutoff":1,"operator":"gte","value":0.5}`,
			`["lexical"]`),
	}
	for testName, encodedPolicy := range testCases {
		encodedPolicy := encodedPolicy
		t.Run(testName, func(t *testing.T) {
			t.Parallel()
			if _, loadError := LoadQualityPolicy(strings.NewReader(encodedPolicy), QualityPolicyLoadOptions{}); loadError == nil {
				t.Fatal("LoadQualityPolicy() accepted invalid policy")
			}
		})
	}
}

func TestEvaluateQualityGatePassesEverySupportedMetricAndSlice(t *testing.T) {
	t.Parallel()

	policy := mustLoadQualityPolicy(t, []byte(testQualityPolicyDocument(`
    {"id":"slice-recall","scope":"slice","slice":"intent:service","metric":"recall","cutoff":10,"operator":"gte","value":0.6},
    {"id":"overall-zero-result","scope":"overall","metric":"zero_result_rate","operator":"lte","value":0.25},
    {"id":"overall-recall","scope":"overall","metric":"recall","cutoff":10,"operator":"gte","value":0.8},
    {"id":"overall-ndcg","scope":"overall","metric":"ndcg","cutoff":10,"operator":"gte","value":0.6},
    {"id":"overall-mrr","scope":"overall","metric":"mrr","cutoff":10,"operator":"gte","value":0.7},
    {"id":"overall-latency","scope":"overall","metric":"latency_ms","quantile":0.95,"operator":"lte","value":90},
    {"id":"overall-judged-rate","scope":"overall","metric":"judged_rate","cutoff":10,"operator":"gte","value":0.5},
    {"id":"overall-failure-rate","scope":"overall","metric":"failure_rate","operator":"lte","value":0.25}`,
		`["lexical_reranked","lexical"]`)), QualityPolicyLoadOptions{})
	report := passingQualityGateReport()

	qualityGate := EvaluateQualityGate(report, policy)
	if !qualityGate.Passed || qualityGate.FailedChecks != 0 {
		t.Fatalf("quality gate = %+v", qualityGate)
	}
	for _, requirement := range qualityGate.Requirements {
		if !requirement.Passed || requirement.Code != "passed" {
			t.Errorf("requirement = %+v", requirement)
		}
	}
	for _, assertion := range qualityGate.Assertions {
		if !assertion.Passed || assertion.Code != "passed" || assertion.Actual == nil {
			t.Errorf("assertion = %+v", assertion)
		}
	}
}

func TestEvaluateQualityGateFailsThresholdAndRuntimeRequirements(t *testing.T) {
	t.Parallel()

	policy := mustLoadQualityPolicy(t, []byte(testQualityPolicyDocument(
		`{"id":"overall-recall","scope":"overall","metric":"recall","cutoff":10,"operator":"gte","value":0.9}`,
		`["lexical"]`,
	)), QualityPolicyLoadOptions{})
	report := passingQualityGateReport()
	report.Complete = false
	report.CatalogRevisions = []string{"unversioned"}
	report.EffectivePipelines = []string{"hybrid"}
	report.DegradedResponses = 1

	qualityGate := EvaluateQualityGate(report, policy)
	if qualityGate.Passed || qualityGate.FailedChecks != 5 {
		t.Fatalf("quality gate = %+v", qualityGate)
	}
	expectedRequirementCodes := []string{
		"report_incomplete",
		"catalog_revision_mismatch",
		"pipeline_not_allowed",
		"degraded_responses_present",
	}
	for requirementIndex, expectedCode := range expectedRequirementCodes {
		if qualityGate.Requirements[requirementIndex].Code != expectedCode {
			t.Errorf("requirement %d = %+v", requirementIndex, qualityGate.Requirements[requirementIndex])
		}
	}
	if qualityGate.Assertions[0].Code != "threshold_not_met" {
		t.Errorf("assertion = %+v", qualityGate.Assertions[0])
	}
}

func TestEvaluateQualityGateReportsMissingMetricAndSlice(t *testing.T) {
	t.Parallel()

	policy := mustLoadQualityPolicy(t, []byte(testQualityPolicyDocument(`
    {"id":"missing-cutoff","scope":"overall","metric":"recall","cutoff":3,"operator":"gte","value":0.5},
    {"id":"missing-quantile","scope":"overall","metric":"latency_ms","quantile":0.99,"operator":"lte","value":100},
    {"id":"missing-slice","scope":"slice","slice":"intent:missing","metric":"recall","cutoff":10,"operator":"gte","value":0.5}`,
		`["lexical"]`)), QualityPolicyLoadOptions{})

	qualityGate := EvaluateQualityGate(passingQualityGateReport(), policy)
	if qualityGate.Passed || qualityGate.FailedChecks != 3 {
		t.Fatalf("quality gate = %+v", qualityGate)
	}
	assertionCodes := map[string]string{}
	for _, assertion := range qualityGate.Assertions {
		assertionCodes[assertion.AssertionID] = assertion.Code
	}
	if !reflect.DeepEqual(assertionCodes, map[string]string{
		"missing-cutoff":   "metric_missing",
		"missing-quantile": "metric_missing",
		"missing-slice":    "slice_missing",
	}) {
		t.Errorf("assertion codes = %v", assertionCodes)
	}
}

func TestEvaluateQualityGateRejectsNonFiniteMetricWithSerializableSafeResult(t *testing.T) {
	t.Parallel()

	policy := mustLoadQualityPolicy(t, []byte(testQualityPolicyDocument(
		`{"id":"overall-recall","scope":"overall","metric":"recall","cutoff":10,"operator":"gte","value":0.5}`,
		`["lexical"]`,
	)), QualityPolicyLoadOptions{})
	report := passingQualityGateReport()
	cutoffMetrics := report.Overall.MetricsAtCutoff["10"]
	cutoffMetrics.Recall = math.NaN()
	report.Overall.MetricsAtCutoff["10"] = cutoffMetrics

	qualityGate := EvaluateQualityGate(report, policy)
	assertion := qualityGate.Assertions[0]
	if qualityGate.Passed || assertion.Code != "metric_non_finite" || assertion.Actual != nil {
		t.Fatalf("quality gate = %+v", qualityGate)
	}
	encodedGate, encodeError := json.Marshal(qualityGate)
	if encodeError != nil {
		t.Fatalf("json.Marshal() error = %v", encodeError)
	}
	for _, prohibitedContent := range []string{"private query", "private document", "NaN"} {
		if bytes.Contains(encodedGate, []byte(prohibitedContent)) {
			t.Fatalf("quality gate leaked prohibited content %q: %s", prohibitedContent, encodedGate)
		}
	}
}

func TestEvaluateQualityGateIsDeterministic(t *testing.T) {
	t.Parallel()

	policy := mustLoadQualityPolicy(t, []byte(testQualityPolicyDocument(`
    {"id":"z-zero-result","scope":"overall","metric":"zero_result_rate","operator":"lte","value":0.25},
    {"id":"a-recall","scope":"overall","metric":"recall","cutoff":10,"operator":"gte","value":0.8}`,
		`["lexical"]`)), QualityPolicyLoadOptions{})
	firstGate := EvaluateQualityGate(passingQualityGateReport(), policy)
	secondGate := EvaluateQualityGate(passingQualityGateReport(), policy)
	firstJSON, firstError := json.Marshal(firstGate)
	secondJSON, secondError := json.Marshal(secondGate)
	if firstError != nil || secondError != nil {
		t.Fatalf("marshal errors = %v, %v", firstError, secondError)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("quality gates differ:\n%s\n%s", firstJSON, secondJSON)
	}
	if assertionIDs := []string{firstGate.Assertions[0].AssertionID, firstGate.Assertions[1].AssertionID}; !reflect.DeepEqual(assertionIDs, []string{"a-recall", "z-zero-result"}) {
		t.Errorf("assertion order = %v", assertionIDs)
	}
}

func testQualityPolicyDocument(assertions, allowedPipelines string) string {
	return `{
  "schema_version":"search-quality-policy/v1",
  "id":"example-non-production",
  "version":"test-v1",
  "requirements":{
    "complete":true,
    "catalog_revision":"catalog-revision-1",
    "allowed_pipelines":` + allowedPipelines + `,
    "degraded_responses":0
  },
  "assertions":[` + assertions + `]
}`
}

func mustLoadQualityPolicy(t *testing.T, encodedPolicy []byte, options QualityPolicyLoadOptions) QualityPolicy {
	t.Helper()
	policy, loadError := LoadQualityPolicy(bytes.NewReader(encodedPolicy), options)
	if loadError != nil {
		t.Fatalf("LoadQualityPolicy() error = %v", loadError)
	}
	return policy
}

func passingQualityGateReport() Report {
	overallMetrics := AggregateMetrics{
		Queries:           4,
		SuccessfulQueries: 3,
		FailedQueries:     1,
		ZeroResultRate:    0.25,
		MetricsAtCutoff: map[string]CutoffMetrics{
			"10": {Recall: 0.8, MRR: 0.7, NDCG: 0.6, JudgedRate: 0.5},
		},
		LatencyMilliseconds: map[string]float64{"p95": 90},
	}
	return Report{
		Complete:           true,
		CatalogRevisions:   []string{"catalog-revision-1"},
		EffectivePipelines: []string{"lexical"},
		Overall:            overallMetrics,
		BySlice: map[string]AggregateMetrics{
			"intent:service": {
				Queries:           2,
				SuccessfulQueries: 2,
				MetricsAtCutoff: map[string]CutoffMetrics{
					"10": {Recall: 0.6},
				},
				LatencyMilliseconds: map[string]float64{"p95": 80},
			},
		},
	}
}
