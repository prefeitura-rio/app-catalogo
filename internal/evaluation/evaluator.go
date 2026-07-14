package evaluation

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EvaluatorConfig controls bounded execution and deterministic reporting.
type EvaluatorConfig struct {
	Endpoint                  string
	Concurrency               int
	CandidateLimit            int
	Cutoffs                   []int
	LatencyQuantiles          []float64
	ContinueOnError           bool
	RequestTimeout            time.Duration
	MaximumResponseBytes      int64
	MaximumDatasetLineBytes   int
	MaximumQualityPolicyBytes int64
	QualityPolicy             *QualityPolicyReference
	RunTimestamp              func() time.Time
}

// RunConfiguration records every execution bound that can change a report.
type RunConfiguration struct {
	Transport                 string    `json:"transport"`
	RequestTimeout            string    `json:"request_timeout"`
	Concurrency               int       `json:"concurrency"`
	CandidateLimit            int       `json:"candidate_limit"`
	Cutoffs                   []int     `json:"cutoffs"`
	LatencyQuantiles          []float64 `json:"latency_quantiles"`
	ContinueOnError           bool      `json:"continue_on_error"`
	MaximumResponseBytes      int64     `json:"maximum_response_bytes"`
	MaximumDatasetLineBytes   int       `json:"maximum_dataset_line_bytes"`
	MaximumQualityPolicyBytes int64     `json:"maximum_quality_policy_bytes"`
}

// Report is the privacy-safe, deterministic evaluation artifact.
type Report struct {
	SchemaVersion          string                      `json:"schema_version"`
	RunTimestamp           string                      `json:"run_timestamp"`
	Complete               bool                        `json:"complete"`
	Endpoint               string                      `json:"endpoint"`
	DatasetHash            string                      `json:"dataset_hash"`
	RunConfiguration       RunConfiguration            `json:"run_configuration"`
	QualityPolicy          *QualityPolicyReference     `json:"quality_policy,omitempty"`
	QualityGate            *QualityGateResult          `json:"quality_gate,omitempty"`
	RankerVersions         []string                    `json:"ranker_versions"`
	RankerDescriptorHashes []string                    `json:"ranker_descriptor_hashes"`
	CatalogRevisions       []string                    `json:"catalog_revisions"`
	EffectivePipelines     []string                    `json:"effective_pipelines"`
	DegradedResponses      int                         `json:"degraded_responses"`
	Failures               []Failure                   `json:"failures"`
	Overall                AggregateMetrics            `json:"overall"`
	BySlice                map[string]AggregateMetrics `json:"by_slice"`
}

type queryOutcome struct {
	observation *SearchObservation
	failure     *Failure
}

// Evaluate retrieves all valid queries with bounded concurrency and aggregates them.
func Evaluate(searchContext context.Context, dataset Dataset, searcher Searcher, config EvaluatorConfig) (Report, error) {
	if searcher == nil {
		return Report{}, errors.New("searcher is required")
	}
	if len(dataset.Queries) == 0 {
		return Report{}, errors.New("evaluation dataset contains no queries")
	}
	if strings.TrimSpace(config.Endpoint) == "" {
		return Report{}, errors.New("report endpoint is required")
	}
	if config.Concurrency < 1 {
		return Report{}, errors.New("evaluation concurrency must be positive")
	}
	if policyError := ValidateQualityPolicyReference(config.QualityPolicy); policyError != nil {
		return Report{}, policyError
	}
	if config.MaximumQualityPolicyBytes <= 0 {
		config.MaximumQualityPolicyBytes = DefaultMaximumQualityPolicyBytes
	}
	cutoffs, cutoffError := NormalizeCutoffs(config.Cutoffs, config.CandidateLimit)
	if cutoffError != nil {
		return Report{}, cutoffError
	}
	quantiles, quantileError := NormalizeQuantiles(config.LatencyQuantiles)
	if quantileError != nil {
		return Report{}, quantileError
	}
	runTimestamp := config.RunTimestamp
	if runTimestamp == nil {
		runTimestamp = time.Now
	}

	outcomes := make([]queryOutcome, len(dataset.Queries))
	queryIndexes := make(chan int)
	workerCount := min(config.Concurrency, len(dataset.Queries))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for queryIndex := range queryIndexes {
				query := dataset.Queries[queryIndex]
				observation, searchError := searcher.Search(searchContext, query)
				if searchError != nil {
					failure := failureFromSearchError(query.QueryID, searchError)
					outcomes[queryIndex].failure = &failure
					continue
				}
				observation.QueryID = query.QueryID
				outcomes[queryIndex].observation = &observation
			}
		}()
	}
	for queryIndex := range dataset.Queries {
		queryIndexes <- queryIndex
	}
	close(queryIndexes)
	workers.Wait()

	report := buildReport(dataset, outcomes, cutoffs, quantiles, config, runTimestamp())
	if contextError := searchContext.Err(); contextError != nil {
		return report, fmt.Errorf("evaluation context ended: %w", contextError)
	}
	if len(report.Failures) > 0 && !config.ContinueOnError {
		return report, &FailuresError{Count: len(report.Failures)}
	}
	return report, nil
}

func buildReport(
	dataset Dataset,
	outcomes []queryOutcome,
	cutoffs []int,
	quantiles []float64,
	config EvaluatorConfig,
	runTimestamp time.Time,
) Report {
	failures := make([]Failure, 0)
	rankerVersionSet := make(map[string]struct{})
	rankerDescriptorHashSet := make(map[string]struct{})
	catalogRevisionSet := make(map[string]struct{})
	effectivePipelineSet := make(map[string]struct{})
	degradedResponses := 0
	missingProvenance := false
	for _, outcome := range outcomes {
		if outcome.failure != nil {
			failures = append(failures, *outcome.failure)
			continue
		}
		if outcome.observation != nil {
			if outcome.observation.RankerVersion != "" {
				rankerVersionSet[outcome.observation.RankerVersion] = struct{}{}
			}
			if outcome.observation.RankerDescriptorHash != "" {
				rankerDescriptorHashSet[outcome.observation.RankerDescriptorHash] = struct{}{}
			}
			if outcome.observation.CatalogRevision != "" {
				catalogRevisionSet[outcome.observation.CatalogRevision] = struct{}{}
			}
			if outcome.observation.EffectivePipeline.Valid() {
				effectivePipelineSet[string(outcome.observation.EffectivePipeline)] = struct{}{}
			}
			if outcome.observation.Degraded {
				degradedResponses++
			}
			if outcome.observation.RankerVersion == "" || outcome.observation.RankerDescriptorHash == "" ||
				outcome.observation.CatalogRevision == "" || !outcome.observation.EffectivePipeline.Valid() {
				missingProvenance = true
			}
		}
	}
	slices.SortFunc(failures, compareFailures)

	rankerVersions := make([]string, 0, len(rankerVersionSet))
	for rankerVersion := range rankerVersionSet {
		rankerVersions = append(rankerVersions, rankerVersion)
	}
	slices.Sort(rankerVersions)
	if len(rankerVersions) > 1 {
		failures = append(failures, Failure{
			Stage:   FailureStageContract,
			Code:    "mixed_ranker_version",
			Message: "successful responses contain multiple ranker versions",
		})
		slices.SortFunc(failures, compareFailures)
	}
	rankerDescriptorHashes := sortedSetValues(rankerDescriptorHashSet)
	if len(rankerDescriptorHashes) > 1 {
		failures = append(failures, Failure{
			Stage:   FailureStageContract,
			Code:    "mixed_ranker_descriptor",
			Message: "successful responses contain multiple ranker descriptors",
		})
	}
	catalogRevisions := sortedSetValues(catalogRevisionSet)
	if len(catalogRevisions) > 1 {
		failures = append(failures, Failure{
			Stage:   FailureStageContract,
			Code:    "mixed_catalog_revision",
			Message: "successful responses contain multiple catalog revisions",
		})
	}
	if degradedResponses > 0 {
		failures = append(failures, Failure{
			Stage:   FailureStageContract,
			Code:    "degraded_responses",
			Message: "successful responses include degraded ranking executions",
		})
	}
	if missingProvenance {
		failures = append(failures, Failure{
			Stage:   FailureStageContract,
			Code:    "missing_runtime_provenance",
			Message: "successful responses are missing runtime provenance",
		})
	}
	slices.SortFunc(failures, compareFailures)
	effectivePipelines := sortedSetValues(effectivePipelineSet)

	allQueryIndexes := make([]int, len(dataset.Queries))
	for queryIndex := range dataset.Queries {
		allQueryIndexes[queryIndex] = queryIndex
	}
	bySliceIndexes := make(map[string][]int)
	for queryIndex, query := range dataset.Queries {
		for _, sliceName := range query.Slices {
			bySliceIndexes[sliceName] = append(bySliceIndexes[sliceName], queryIndex)
		}
	}
	bySlice := make(map[string]AggregateMetrics, len(bySliceIndexes))
	for sliceName, queryIndexes := range bySliceIndexes {
		bySlice[sliceName] = aggregateMetrics(dataset, outcomes, queryIndexes, cutoffs, quantiles)
	}

	return Report{
		SchemaVersion: ReportSchemaVersion,
		RunTimestamp:  runTimestamp.UTC().Format(time.RFC3339Nano),
		Complete:      len(failures) == 0,
		Endpoint:      config.Endpoint,
		DatasetHash:   dataset.Hash,
		RunConfiguration: RunConfiguration{
			Transport:                 "http-post-json",
			RequestTimeout:            config.RequestTimeout.String(),
			Concurrency:               config.Concurrency,
			CandidateLimit:            config.CandidateLimit,
			Cutoffs:                   cutoffs,
			LatencyQuantiles:          quantiles,
			ContinueOnError:           config.ContinueOnError,
			MaximumResponseBytes:      config.MaximumResponseBytes,
			MaximumDatasetLineBytes:   config.MaximumDatasetLineBytes,
			MaximumQualityPolicyBytes: config.MaximumQualityPolicyBytes,
		},
		QualityPolicy:          cloneQualityPolicy(config.QualityPolicy),
		RankerVersions:         rankerVersions,
		RankerDescriptorHashes: rankerDescriptorHashes,
		CatalogRevisions:       catalogRevisions,
		EffectivePipelines:     effectivePipelines,
		DegradedResponses:      degradedResponses,
		Failures:               failures,
		Overall:                aggregateMetrics(dataset, outcomes, allQueryIndexes, cutoffs, quantiles),
		BySlice:                bySlice,
	}
}

func sortedSetValues(valueSet map[string]struct{}) []string {
	values := make([]string, 0, len(valueSet))
	for value := range valueSet {
		if value != "" {
			values = append(values, value)
		}
	}
	slices.Sort(values)
	return values
}

// ValidateQualityPolicyReference validates an optional immutable policy identity.
func ValidateQualityPolicyReference(policy *QualityPolicyReference) error {
	if policy == nil {
		return nil
	}
	if policy.SchemaVersion != QualityPolicySchemaVersion {
		return fmt.Errorf("quality policy schema_version must be %q", QualityPolicySchemaVersion)
	}
	if !validBoundedStableIdentifier(policy.ID) || !validBoundedStableIdentifier(policy.Version) {
		return errors.New("quality policy id and version must be bounded stable identifiers")
	}
	if len(policy.SHA256) != sha256.Size*2 {
		return errors.New("quality policy sha256 must be a lowercase hexadecimal digest")
	}
	for _, character := range policy.SHA256 {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return errors.New("quality policy sha256 must be a lowercase hexadecimal digest")
		}
	}
	return nil
}

func cloneQualityPolicy(policy *QualityPolicyReference) *QualityPolicyReference {
	if policy == nil {
		return nil
	}
	policyCopy := *policy
	return &policyCopy
}

func aggregateMetrics(
	dataset Dataset,
	outcomes []queryOutcome,
	queryIndexes []int,
	cutoffs []int,
	quantiles []float64,
) AggregateMetrics {
	metricSums := make(map[int]queryMetricValues, len(cutoffs))
	latencies := make([]time.Duration, 0, len(queryIndexes))
	aggregate := AggregateMetrics{
		Queries:         len(queryIndexes),
		MetricsAtCutoff: make(map[string]CutoffMetrics, len(cutoffs)),
	}
	for _, queryIndex := range queryIndexes {
		outcome := outcomes[queryIndex]
		if outcome.failure != nil || outcome.observation == nil {
			aggregate.FailedQueries++
			continue
		}
		aggregate.SuccessfulQueries++
		if len(outcome.observation.Documents) == 0 {
			aggregate.ZeroResultQueries++
		}
		latency := outcome.observation.Latency
		if latency < 0 {
			latency = 0
		}
		latencies = append(latencies, latency)
		for _, cutoff := range cutoffs {
			queryMetrics := calculateQueryMetrics(outcome.observation.Documents, dataset.Queries[queryIndex].Qrels, cutoff)
			if !queryMetrics.Judged {
				continue
			}
			metricSum := metricSums[cutoff]
			metricSum.Recall += queryMetrics.Recall
			metricSum.MRR += queryMetrics.MRR
			metricSum.NDCG += queryMetrics.NDCG
			metricSum.JudgedRate += queryMetrics.JudgedRate
			metricSums[cutoff] = metricSum
		}
	}
	if aggregate.SuccessfulQueries > 0 {
		aggregate.ZeroResultRate = float64(aggregate.ZeroResultQueries) / float64(aggregate.SuccessfulQueries)
	}
	judgedQueries := countJudgedQueries(dataset, outcomes, queryIndexes)
	for _, cutoff := range cutoffs {
		metricSum := metricSums[cutoff]
		cutoffMetrics := CutoffMetrics{JudgedQueries: judgedQueries}
		if judgedQueries > 0 {
			cutoffMetrics.Recall = metricSum.Recall / float64(judgedQueries)
			cutoffMetrics.MRR = metricSum.MRR / float64(judgedQueries)
			cutoffMetrics.NDCG = metricSum.NDCG / float64(judgedQueries)
			cutoffMetrics.JudgedRate = metricSum.JudgedRate / float64(judgedQueries)
		}
		aggregate.MetricsAtCutoff[strconv.Itoa(cutoff)] = cutoffMetrics
	}
	aggregate.LatencyMilliseconds = latencyQuantiles(latencies, quantiles)
	aggregate.LatencySamples = len(latencies)
	return aggregate
}

func countJudgedQueries(dataset Dataset, outcomes []queryOutcome, queryIndexes []int) int {
	judgedQueries := 0
	for _, queryIndex := range queryIndexes {
		if outcomes[queryIndex].observation == nil || outcomes[queryIndex].failure != nil {
			continue
		}
		for _, entityJudgment := range dataset.Queries[queryIndex].Qrels {
			if entityJudgment.Grade > 0 {
				judgedQueries++
				break
			}
		}
	}
	return judgedQueries
}

func failureFromSearchError(queryID string, searchError error) Failure {
	failure := Failure{
		QueryID: queryID,
		Stage:   FailureStageTransport,
		Code:    "request_failed",
		Message: "search request failed",
	}
	var typedFailure *SearchFailureError
	if !errors.As(searchError, &typedFailure) {
		return failure
	}
	if typedFailure.Stage != FailureStageTransport && typedFailure.Stage != FailureStageContract {
		return failure
	}
	if !stableIdentifierPattern.MatchString(typedFailure.Code) {
		return failure
	}
	failure.Stage = typedFailure.Stage
	failure.Code = typedFailure.Code
	if typedFailure.Stage == FailureStageContract {
		failure.Message = "search response violated the public contract"
	}
	return failure
}

func compareFailures(firstFailure, secondFailure Failure) int {
	if firstFailure.Line != secondFailure.Line {
		return cmp.Compare(firstFailure.Line, secondFailure.Line)
	}
	if firstFailure.QueryID != secondFailure.QueryID {
		return strings.Compare(firstFailure.QueryID, secondFailure.QueryID)
	}
	if firstFailure.Stage != secondFailure.Stage {
		return strings.Compare(string(firstFailure.Stage), string(secondFailure.Stage))
	}
	return strings.Compare(firstFailure.Code, secondFailure.Code)
}

// MarshalReport emits stable indented JSON with a trailing newline.
func MarshalReport(report Report) ([]byte, error) {
	encodedReport, encodeError := json.MarshalIndent(report, "", "  ")
	if encodeError != nil {
		return nil, fmt.Errorf("encode evaluation report: %w", encodeError)
	}
	return append(encodedReport, '\n'), nil
}
