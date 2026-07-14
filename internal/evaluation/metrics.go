package evaluation

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"
)

// CutoffMetrics contains macro-averaged quality scores at one rank cutoff.
type CutoffMetrics struct {
	Recall        float64 `json:"recall"`
	MRR           float64 `json:"mrr"`
	NDCG          float64 `json:"ndcg"`
	JudgedRate    float64 `json:"judged_rate"`
	JudgedQueries int     `json:"judged_queries"`
}

// AggregateMetrics summarizes successful and failed queries in one slice.
type AggregateMetrics struct {
	Queries             int                      `json:"queries"`
	SuccessfulQueries   int                      `json:"successful_queries"`
	FailedQueries       int                      `json:"failed_queries"`
	ZeroResultRate      float64                  `json:"zero_result_rate"`
	ZeroResultQueries   int                      `json:"zero_result_queries"`
	MetricsAtCutoff     map[string]CutoffMetrics `json:"metrics_at_cutoff"`
	LatencyMilliseconds map[string]float64       `json:"latency_ms"`
	LatencySamples      int                      `json:"latency_samples"`
}

type queryMetricValues struct {
	Recall     float64
	MRR        float64
	NDCG       float64
	JudgedRate float64
	Judged     bool
}

// NormalizeCutoffs validates, deduplicates, and sorts ranking cutoffs.
func NormalizeCutoffs(cutoffs []int, candidateLimit int) ([]int, error) {
	if candidateLimit < 1 {
		return nil, fmt.Errorf("candidate limit must be positive")
	}
	uniqueCutoffs := make(map[int]struct{}, len(cutoffs))
	for _, cutoff := range cutoffs {
		if cutoff < 1 || cutoff > candidateLimit {
			return nil, fmt.Errorf("cutoff must be between 1 and the candidate limit")
		}
		uniqueCutoffs[cutoff] = struct{}{}
	}
	if len(uniqueCutoffs) == 0 {
		return nil, fmt.Errorf("at least one cutoff is required")
	}
	normalizedCutoffs := make([]int, 0, len(uniqueCutoffs))
	for cutoff := range uniqueCutoffs {
		normalizedCutoffs = append(normalizedCutoffs, cutoff)
	}
	slices.Sort(normalizedCutoffs)
	return normalizedCutoffs, nil
}

// NormalizeQuantiles validates, deduplicates, and sorts latency quantiles.
func NormalizeQuantiles(quantiles []float64) ([]float64, error) {
	uniqueQuantiles := make(map[float64]struct{}, len(quantiles))
	for _, quantile := range quantiles {
		if math.IsNaN(quantile) || math.IsInf(quantile, 0) || quantile < 0 || quantile > 1 {
			return nil, fmt.Errorf("latency quantiles must be finite values between zero and one")
		}
		uniqueQuantiles[quantile] = struct{}{}
	}
	if len(uniqueQuantiles) == 0 {
		return nil, fmt.Errorf("at least one latency quantile is required")
	}
	normalizedQuantiles := make([]float64, 0, len(uniqueQuantiles))
	for quantile := range uniqueQuantiles {
		normalizedQuantiles = append(normalizedQuantiles, quantile)
	}
	slices.Sort(normalizedQuantiles)
	return normalizedQuantiles, nil
}

func calculateQueryMetrics(documents []DocumentKey, qrels []EntityJudgment, cutoff int) queryMetricValues {
	documentJudgments := make(map[DocumentKey]EntityJudgment)
	relevantEntityCount := 0
	maximumGrade := 0
	idealGrades := make([]int, 0, len(qrels))
	for _, entityJudgment := range qrels {
		for _, document := range entityJudgment.Documents {
			documentJudgments[document] = entityJudgment
		}
		if entityJudgment.Grade > 0 {
			relevantEntityCount++
			idealGrades = append(idealGrades, entityJudgment.Grade)
		}
		maximumGrade = max(maximumGrade, entityJudgment.Grade)
	}
	if relevantEntityCount == 0 {
		return queryMetricValues{}
	}

	topDocuments := documents[:min(cutoff, len(documents))]
	seenEntityIDs := make(map[string]struct{})
	retrievedRelevantEntities := 0
	judgedDocuments := 0
	reciprocalRank := 0.0
	discountedGain := 0.0
	for resultIndex, document := range topDocuments {
		entityJudgment, judged := documentJudgments[document]
		if !judged {
			continue
		}
		judgedDocuments++
		if _, entityAlreadySeen := seenEntityIDs[entityJudgment.EntityID]; entityAlreadySeen {
			continue
		}
		seenEntityIDs[entityJudgment.EntityID] = struct{}{}
		if entityJudgment.Grade <= 0 {
			continue
		}
		retrievedRelevantEntities++
		if reciprocalRank == 0 {
			reciprocalRank = 1 / float64(resultIndex+1)
		}
		discountedGain += stableExponentialGain(entityJudgment.Grade, maximumGrade) / math.Log2(float64(resultIndex+2))
	}

	slices.Sort(idealGrades)
	slices.Reverse(idealGrades)
	idealGain := discountedCumulativeGainForGrades(idealGrades, cutoff, maximumGrade)
	normalizedGain := 0.0
	if idealGain > 0 {
		normalizedGain = discountedGain / idealGain
	}

	judgedRate := float64(judgedDocuments) / float64(cutoff)
	return queryMetricValues{
		Recall:     float64(retrievedRelevantEntities) / float64(relevantEntityCount),
		MRR:        reciprocalRank,
		NDCG:       normalizedGain,
		JudgedRate: judgedRate,
		Judged:     true,
	}
}

func discountedCumulativeGainForGrades(relevanceGrades []int, cutoff, maximumGrade int) float64 {
	discountedGain := 0.0
	for gradeIndex, relevanceGrade := range relevanceGrades[:min(cutoff, len(relevanceGrades))] {
		discountedGain += stableExponentialGain(relevanceGrade, maximumGrade) / math.Log2(float64(gradeIndex+2))
	}
	return discountedGain
}

func stableExponentialGain(relevanceGrade, maximumGrade int) float64 {
	if relevanceGrade <= 0 || maximumGrade <= 0 {
		return 0
	}
	maximumGradeScale := math.Exp2(-float64(maximumGrade))
	return math.Exp2(float64(relevanceGrade-maximumGrade)) - maximumGradeScale
}

func latencyQuantiles(latencies []time.Duration, quantiles []float64) map[string]float64 {
	quantileValues := make(map[string]float64, len(quantiles))
	if len(latencies) == 0 {
		for _, quantile := range quantiles {
			quantileValues[quantileLabel(quantile)] = 0
		}
		return quantileValues
	}

	sortedLatencies := slices.Clone(latencies)
	slices.Sort(sortedLatencies)
	for _, quantile := range quantiles {
		nearestRank := int(math.Ceil(quantile*float64(len(sortedLatencies)))) - 1
		nearestRank = max(0, min(nearestRank, len(sortedLatencies)-1))
		quantileValues[quantileLabel(quantile)] = float64(sortedLatencies[nearestRank]) / float64(time.Millisecond)
	}
	return quantileValues
}

func quantileLabel(quantile float64) string {
	return "p" + strconv.FormatFloat(quantile*100, 'f', -1, 64)
}
