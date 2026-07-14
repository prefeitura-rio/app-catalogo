package evaluation

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func TestCalculateQueryMetricsUsesGradedQrelsAtCutoff(t *testing.T) {
	qrels := []EntityJudgment{
		testJudgment("entity-a", 3, testDocument("salesforce", "a")),
		testJudgment("entity-b", 2, testDocument("jobs", "b")),
		testJudgment("entity-c", 1, testDocument("courses", "c")),
	}
	documents := []DocumentKey{
		testDocument("salesforce", "unjudged"),
		testDocument("jobs", "b"),
		testDocument("salesforce", "a"),
	}

	metricsAtTwo := calculateQueryMetrics(documents, qrels, 2)
	if !metricsAtTwo.Judged {
		t.Fatal("metrics must be marked judged")
	}
	assertClose(t, metricsAtTwo.Recall, 1.0/3.0)
	assertClose(t, metricsAtTwo.MRR, 0.5)
	assertClose(t, metricsAtTwo.JudgedRate, 0.5)
	wantNDCGAtTwo := (3.0 / math.Log2(3)) / (7.0 + 3.0/math.Log2(3))
	assertClose(t, metricsAtTwo.NDCG, wantNDCGAtTwo)

	metricsAtThree := calculateQueryMetrics(documents, qrels, 3)
	assertClose(t, metricsAtThree.Recall, 2.0/3.0)
	assertClose(t, metricsAtThree.MRR, 0.5)
	assertClose(t, metricsAtThree.JudgedRate, 2.0/3.0)
	wantNDCGAtThree := (3.0/math.Log2(3) + 7.0/math.Log2(4)) /
		(7.0 + 3.0/math.Log2(3) + 1.0/math.Log2(4))
	assertClose(t, metricsAtThree.NDCG, wantNDCGAtThree)
}

func TestCalculateQueryMetricsExcludesQueryWithoutPositiveQrel(t *testing.T) {
	queryMetrics := calculateQueryMetrics(
		[]DocumentKey{testDocument("salesforce", "source-1")},
		[]EntityJudgment{testJudgment("irrelevant", 0, testDocument("salesforce", "source-1"))},
		1,
	)
	if queryMetrics.Judged {
		t.Fatalf("queryMetrics = %+v", queryMetrics)
	}
}

func TestCalculateQueryMetricsCreditsEntityAliasOnlyOnce(t *testing.T) {
	qrels := []EntityJudgment{
		testJudgment("service-a", 3,
			testDocument("salesforce", "current-a"),
			testDocument("typesense", "legacy-a"),
		),
		testJudgment("service-b", 2, testDocument("salesforce", "current-b")),
	}
	documents := []DocumentKey{
		testDocument("typesense", "legacy-a"),
		testDocument("salesforce", "current-a"),
		testDocument("salesforce", "current-b"),
	}

	queryMetrics := calculateQueryMetrics(documents, qrels, 3)
	assertClose(t, queryMetrics.Recall, 1)
	assertClose(t, queryMetrics.MRR, 1)
	assertClose(t, queryMetrics.JudgedRate, 1)
	wantNDCG := (7.0 + 3.0/math.Log2(4)) / (7.0 + 3.0/math.Log2(3))
	assertClose(t, queryMetrics.NDCG, wantNDCG)
}

func TestCalculateQueryMetricsCountsZeroGradeDocumentAsJudged(t *testing.T) {
	qrels := []EntityJudgment{
		testJudgment("relevant", 3, testDocument("salesforce", "relevant")),
		testJudgment("reviewed-negative", 0, testDocument("salesforce", "negative")),
	}
	documents := []DocumentKey{
		testDocument("salesforce", "negative"),
		testDocument("salesforce", "unjudged"),
	}

	queryMetrics := calculateQueryMetrics(documents, qrels, 2)
	assertClose(t, queryMetrics.Recall, 0)
	assertClose(t, queryMetrics.MRR, 0)
	assertClose(t, queryMetrics.NDCG, 0)
	assertClose(t, queryMetrics.JudgedRate, 0.5)
}

func TestNormalizeCutoffsAndQuantilesAreSortedAndUnique(t *testing.T) {
	cutoffs, cutoffError := NormalizeCutoffs([]int{10, 1, 3, 3}, 10)
	if cutoffError != nil {
		t.Fatalf("NormalizeCutoffs() error = %v", cutoffError)
	}
	if wantCutoffs := []int{1, 3, 10}; !reflect.DeepEqual(cutoffs, wantCutoffs) {
		t.Errorf("cutoffs = %v, want %v", cutoffs, wantCutoffs)
	}

	quantiles, quantileError := NormalizeQuantiles([]float64{0.95, 0.5, 0.5, 1})
	if quantileError != nil {
		t.Fatalf("NormalizeQuantiles() error = %v", quantileError)
	}
	if wantQuantiles := []float64{0.5, 0.95, 1}; !reflect.DeepEqual(quantiles, wantQuantiles) {
		t.Errorf("quantiles = %v, want %v", quantiles, wantQuantiles)
	}
}

func TestLatencyQuantilesUseNearestRank(t *testing.T) {
	quantiles := latencyQuantiles(
		[]time.Duration{40 * time.Millisecond, 10 * time.Millisecond, 30 * time.Millisecond, 20 * time.Millisecond},
		[]float64{0, 0.5, 0.95, 1},
	)
	wantQuantiles := map[string]float64{"p0": 10, "p50": 20, "p95": 40, "p100": 40}
	if !reflect.DeepEqual(quantiles, wantQuantiles) {
		t.Errorf("quantiles = %v, want %v", quantiles, wantQuantiles)
	}
}

func assertClose(t *testing.T, actual, expected float64) {
	t.Helper()
	if math.Abs(actual-expected) > 1e-12 {
		t.Errorf("actual = %.15f, expected %.15f", actual, expected)
	}
}
