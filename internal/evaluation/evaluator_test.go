package evaluation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEvaluateBuildsDeterministicOverallAndSliceReport(t *testing.T) {
	runTimestamp := time.Date(2026, time.July, 10, 15, 4, 5, 123, time.FixedZone("BRT", -3*60*60))
	dataset := Dataset{
		Hash: "dataset-sha256",
		Queries: []Query{
			{QueryID: "query.a", Text: "first private query", Slices: []string{"intent:service", "temporal:holdout"}, Qrels: []EntityJudgment{
				testJudgment("entity-a", 3, testDocument("salesforce", "a")),
				testJudgment("entity-b", 1, testDocument("salesforce", "b")),
			}},
			{QueryID: "query.b", Text: "second private query", Slices: []string{"intent:job"}, Qrels: []EntityJudgment{
				testJudgment("job", 2, testDocument("jobs", "job")),
			}},
			{QueryID: "query.c", Text: "third private query", Slices: []string{"intent:service"}, Qrels: []EntityJudgment{
				testJudgment("course", 1, testDocument("courses", "course")),
			}},
		},
	}
	searcher := fixedSearcher{
		observations: map[string]SearchObservation{
			"query.a": testObservation([]DocumentKey{testDocument("salesforce", "x"), testDocument("salesforce", "a"), testDocument("salesforce", "b")}, "ranker-v1", 30*time.Millisecond),
			"query.b": testObservation([]DocumentKey{testDocument("jobs", "job")}, "ranker-v1", 10*time.Millisecond),
			"query.c": testObservation(nil, "ranker-v1", 20*time.Millisecond),
		},
	}
	config := EvaluatorConfig{
		Endpoint:                "http://localhost:8080/api/public/search",
		Concurrency:             3,
		CandidateLimit:          10,
		Cutoffs:                 []int{3, 1},
		LatencyQuantiles:        []float64{0.95, 0.5},
		RequestTimeout:          2 * time.Second,
		MaximumResponseBytes:    4096,
		MaximumDatasetLineBytes: 2048,
		QualityPolicy: &QualityPolicyReference{
			SchemaVersion: QualityPolicySchemaVersion,
			ID:            "search-quality",
			Version:       "v1",
			SHA256:        strings.Repeat("a", 64),
		},
		RunTimestamp: func() time.Time { return runTimestamp },
	}

	firstReport, evaluationError := Evaluate(context.Background(), dataset, searcher, config)
	if evaluationError != nil {
		t.Fatalf("Evaluate() error = %v", evaluationError)
	}
	secondReport, evaluationError := Evaluate(context.Background(), dataset, searcher, config)
	if evaluationError != nil {
		t.Fatalf("second Evaluate() error = %v", evaluationError)
	}
	firstJSON, marshalError := MarshalReport(firstReport)
	if marshalError != nil {
		t.Fatalf("MarshalReport() error = %v", marshalError)
	}
	secondJSON, marshalError := MarshalReport(secondReport)
	if marshalError != nil {
		t.Fatalf("second MarshalReport() error = %v", marshalError)
	}
	if !reflect.DeepEqual(firstJSON, secondJSON) {
		t.Fatalf("reports differ:\n%s\n%s", firstJSON, secondJSON)
	}
	if firstReport.RunTimestamp != "2026-07-10T18:04:05.000000123Z" {
		t.Errorf("RunTimestamp = %q", firstReport.RunTimestamp)
	}
	if wantVersions := []string{"ranker-v1"}; !reflect.DeepEqual(firstReport.RankerVersions, wantVersions) {
		t.Errorf("RankerVersions = %v, want %v", firstReport.RankerVersions, wantVersions)
	}
	if !reflect.DeepEqual(firstReport.CatalogRevisions, []string{"catalog-revision-1"}) ||
		!reflect.DeepEqual(firstReport.EffectivePipelines, []string{"lexical"}) ||
		!reflect.DeepEqual(firstReport.RankerDescriptorHashes, []string{"descriptor-hash-1"}) {
		t.Errorf("runtime provenance = %+v", firstReport)
	}
	if firstReport.RunConfiguration.Transport != "http-post-json" || firstReport.RunConfiguration.CandidateLimit != 10 {
		t.Errorf("run configuration = %+v", firstReport.RunConfiguration)
	}
	if firstReport.QualityPolicy == nil || firstReport.QualityPolicy.SHA256 != strings.Repeat("a", 64) {
		t.Errorf("quality policy = %+v", firstReport.QualityPolicy)
	}
	if firstReport.Overall.Queries != 3 || firstReport.Overall.SuccessfulQueries != 3 || firstReport.Overall.ZeroResultQueries != 1 {
		t.Errorf("Overall = %+v", firstReport.Overall)
	}
	if firstReport.Overall.LatencyMilliseconds["p50"] != 20 || firstReport.Overall.LatencyMilliseconds["p95"] != 30 {
		t.Errorf("latency = %v", firstReport.Overall.LatencyMilliseconds)
	}
	assertClose(t, firstReport.Overall.MetricsAtCutoff["1"].Recall, 1.0/3.0)
	assertClose(t, firstReport.Overall.MetricsAtCutoff["1"].JudgedRate, 1.0/3.0)
	if serviceSlice := firstReport.BySlice["intent:service"]; serviceSlice.Queries != 2 || serviceSlice.ZeroResultQueries != 1 {
		t.Errorf("intent:service = %+v", serviceSlice)
	}
	encodedReport := string(firstJSON)
	for _, rawQuery := range []string{"first private query", "second private query", "third private query"} {
		if strings.Contains(encodedReport, rawQuery) {
			t.Fatalf("report leaked raw query %q", rawQuery)
		}
	}
}

func TestEvaluateRedactsUnderlyingFailureAndHonorsContinueFlag(t *testing.T) {
	rawQuery := "cpf 123456789 private query"
	dataset := Dataset{
		Hash: "hash",
		Queries: []Query{{
			QueryID: "private.query",
			Text:    rawQuery,
			Qrels: []EntityJudgment{
				testJudgment("private-entity", 1, testDocument("salesforce", "source-1")),
			},
		}},
	}
	searcher := fixedSearcher{errors: map[string]error{
		"private.query": errors.New("GET http://localhost/search?q=" + rawQuery),
	}}
	baseConfig := EvaluatorConfig{
		Endpoint:         "http://localhost:8080/api/public/search",
		Concurrency:      1,
		CandidateLimit:   10,
		Cutoffs:          []int{1},
		LatencyQuantiles: []float64{0.5},
		RunTimestamp:     func() time.Time { return time.Unix(0, 0) },
	}

	failedReport, evaluationError := Evaluate(context.Background(), dataset, searcher, baseConfig)
	var failuresError *FailuresError
	if !errors.As(evaluationError, &failuresError) {
		t.Fatalf("Evaluate() error = %v, want FailuresError", evaluationError)
	}
	if failedReport.Complete || failedReport.Overall.FailedQueries != 1 {
		t.Errorf("failed report = %+v", failedReport)
	}

	baseConfig.ContinueOnError = true
	continuedReport, evaluationError := Evaluate(context.Background(), dataset, searcher, baseConfig)
	if evaluationError != nil {
		t.Fatalf("continued Evaluate() error = %v", evaluationError)
	}
	encodedReport, marshalError := MarshalReport(continuedReport)
	if marshalError != nil {
		t.Fatalf("MarshalReport() error = %v", marshalError)
	}
	if strings.Contains(string(encodedReport), rawQuery) {
		t.Fatalf("report leaked raw query: %s", encodedReport)
	}
	if !strings.Contains(string(encodedReport), `"code": "request_failed"`) {
		t.Fatalf("report does not contain stable failure code: %s", encodedReport)
	}
}

func TestEvaluateBoundsConcurrentSearchRequests(t *testing.T) {
	queries := make([]Query, 8)
	for queryIndex := range queries {
		queries[queryIndex] = Query{
			QueryID: "query." + string(rune('a'+queryIndex)),
			Text:    "private query",
			Qrels: []EntityJudgment{
				testJudgment("entity", 1, testDocument("salesforce", "source-1")),
			},
		}
	}
	searcher := &concurrencyTrackingSearcher{
		targetConcurrency: 3,
		release:           make(chan struct{}),
	}
	evaluationContext, cancelEvaluation := context.WithTimeout(context.Background(), time.Second)
	defer cancelEvaluation()

	_, evaluationError := Evaluate(evaluationContext, Dataset{Hash: "hash", Queries: queries}, searcher, EvaluatorConfig{
		Endpoint:         "http://localhost:8080/api/public/search",
		Concurrency:      3,
		CandidateLimit:   10,
		Cutoffs:          []int{1},
		LatencyQuantiles: []float64{0.5},
		RunTimestamp:     func() time.Time { return time.Unix(0, 0) },
	})
	if evaluationError != nil {
		t.Fatalf("Evaluate() error = %v", evaluationError)
	}
	if searcher.maximumConcurrent != 3 {
		t.Fatalf("maximum concurrent searches = %d, want 3", searcher.maximumConcurrent)
	}
}

func TestEvaluateRejectsMixedRankerVersionsByDefault(t *testing.T) {
	dataset := Dataset{
		Hash: "hash",
		Queries: []Query{
			{QueryID: "query.a", Text: "first", Qrels: []EntityJudgment{testJudgment("a", 1, testDocument("salesforce", "a"))}},
			{QueryID: "query.b", Text: "second", Qrels: []EntityJudgment{testJudgment("b", 1, testDocument("jobs", "b"))}},
		},
	}
	searcher := fixedSearcher{observations: map[string]SearchObservation{
		"query.a": testObservation([]DocumentKey{testDocument("salesforce", "a")}, "ranker-a", time.Millisecond),
		"query.b": testObservation([]DocumentKey{testDocument("jobs", "b")}, "ranker-b", time.Millisecond),
	}}
	config := EvaluatorConfig{
		Endpoint:         "http://localhost:8080/api/public/search",
		Concurrency:      2,
		CandidateLimit:   10,
		Cutoffs:          []int{1},
		LatencyQuantiles: []float64{0.5},
		RunTimestamp:     func() time.Time { return time.Unix(0, 0) },
	}

	report, evaluationError := Evaluate(context.Background(), dataset, searcher, config)
	var failuresError *FailuresError
	if !errors.As(evaluationError, &failuresError) {
		t.Fatalf("Evaluate() error = %v", evaluationError)
	}
	if report.Complete || !containsFailureCode(report.Failures, "mixed_ranker_version") {
		t.Fatalf("report = %+v", report)
	}
	config.ContinueOnError = true
	continuedReport, evaluationError := Evaluate(context.Background(), dataset, searcher, config)
	if evaluationError != nil || continuedReport.Complete {
		t.Fatalf("continued report = %+v, error = %v", continuedReport, evaluationError)
	}
}

func TestEvaluateRejectsDegradedResponsesAndMixedCatalogRevisions(t *testing.T) {
	t.Parallel()

	dataset := Dataset{
		Hash: "hash",
		Queries: []Query{
			{QueryID: "query.a", Text: "first", Qrels: []EntityJudgment{testJudgment("a", 1, testDocument("salesforce", "a"))}},
			{QueryID: "query.b", Text: "second", Qrels: []EntityJudgment{testJudgment("b", 1, testDocument("jobs", "b"))}},
		},
	}
	firstObservation := testObservation([]DocumentKey{testDocument("salesforce", "a")}, "ranker-v1", time.Millisecond)
	firstObservation.Degraded = true
	secondObservation := testObservation([]DocumentKey{testDocument("jobs", "b")}, "ranker-v1", time.Millisecond)
	secondObservation.CatalogRevision = "catalog-revision-2"
	searcher := fixedSearcher{observations: map[string]SearchObservation{
		"query.a": firstObservation,
		"query.b": secondObservation,
	}}

	report, evaluationError := Evaluate(context.Background(), dataset, searcher, EvaluatorConfig{
		Endpoint:         "http://localhost:8080/api/public/search",
		Concurrency:      2,
		CandidateLimit:   10,
		Cutoffs:          []int{1},
		LatencyQuantiles: []float64{0.5},
		RunTimestamp:     func() time.Time { return time.Unix(0, 0) },
	})
	var failuresError *FailuresError
	if !errors.As(evaluationError, &failuresError) {
		t.Fatalf("Evaluate() error = %v, want FailuresError", evaluationError)
	}
	for _, failureCode := range []string{"degraded_responses", "mixed_catalog_revision"} {
		if !containsFailureCode(report.Failures, failureCode) {
			t.Errorf("report does not contain %q: %+v", failureCode, report.Failures)
		}
	}
	if report.DegradedResponses != 1 || !reflect.DeepEqual(report.CatalogRevisions, []string{"catalog-revision-1", "catalog-revision-2"}) {
		t.Errorf("provenance = %+v", report)
	}
}

func TestEvaluateRejectsInvalidQualityPolicyBeforeSearching(t *testing.T) {
	t.Parallel()

	searcher := &countingSearcher{}
	_, evaluationError := Evaluate(context.Background(), Dataset{
		Hash: "hash",
		Queries: []Query{{
			QueryID: "query.a",
			Text:    "first",
			Qrels:   []EntityJudgment{testJudgment("a", 1, testDocument("salesforce", "a"))},
		}},
	}, searcher, EvaluatorConfig{
		Endpoint:         "http://localhost:8080/api/public/search",
		Concurrency:      1,
		CandidateLimit:   10,
		Cutoffs:          []int{1},
		LatencyQuantiles: []float64{0.5},
		QualityPolicy: &QualityPolicyReference{
			SchemaVersion: QualityPolicySchemaVersion,
			ID:            "policy",
			Version:       "v1",
			SHA256:        "invalid",
		},
	})
	if evaluationError == nil || searcher.calls != 0 {
		t.Fatalf("Evaluate() error = %v, search calls = %d", evaluationError, searcher.calls)
	}
}

type countingSearcher struct {
	calls int
}

func (searcher *countingSearcher) Search(context.Context, Query) (SearchObservation, error) {
	searcher.calls++
	return SearchObservation{}, nil
}

type fixedSearcher struct {
	observations map[string]SearchObservation
	errors       map[string]error
}

func (searcher fixedSearcher) Search(_ context.Context, query Query) (SearchObservation, error) {
	if searchError := searcher.errors[query.QueryID]; searchError != nil {
		return SearchObservation{}, searchError
	}
	return searcher.observations[query.QueryID], nil
}

type concurrencyTrackingSearcher struct {
	mutex             sync.Mutex
	closeRelease      sync.Once
	active            int
	maximumConcurrent int
	targetConcurrency int
	release           chan struct{}
}

func (searcher *concurrencyTrackingSearcher) Search(searchContext context.Context, query Query) (SearchObservation, error) {
	searcher.mutex.Lock()
	searcher.active++
	searcher.maximumConcurrent = max(searcher.maximumConcurrent, searcher.active)
	if searcher.active == searcher.targetConcurrency {
		searcher.closeRelease.Do(searcher.releaseWorkers)
	}
	searcher.mutex.Unlock()
	defer searcher.finishSearch()

	select {
	case <-searcher.release:
	case <-searchContext.Done():
		return SearchObservation{}, searchContext.Err()
	}

	return SearchObservation{
		QueryID:              query.QueryID,
		Documents:            []DocumentKey{testDocument("salesforce", "source-1")},
		SearchID:             validSearchID,
		RankerVersion:        "ranker-v1",
		RankerDescriptorHash: "descriptor-hash-1",
		CatalogRevision:      "catalog-revision-1",
		EffectivePipeline:    "lexical",
		Latency:              time.Millisecond,
	}, nil
}

func testObservation(documents []DocumentKey, rankerVersion string, latency time.Duration) SearchObservation {
	return SearchObservation{
		Documents:            documents,
		SearchID:             validSearchID,
		RankerVersion:        rankerVersion,
		RankerDescriptorHash: "descriptor-hash-1",
		CatalogRevision:      "catalog-revision-1",
		EffectivePipeline:    "lexical",
		Latency:              latency,
	}
}

func (searcher *concurrencyTrackingSearcher) releaseWorkers() {
	close(searcher.release)
}

func (searcher *concurrencyTrackingSearcher) finishSearch() {
	searcher.mutex.Lock()
	defer searcher.mutex.Unlock()
	searcher.active--
}
