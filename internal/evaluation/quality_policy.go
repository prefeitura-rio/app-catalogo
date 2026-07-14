package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

const (
	QualityPolicySchemaVersion                = "search-quality-policy/v1"
	QualityGateResultSchemaVersion            = "search-quality-gate-result/v1"
	DefaultMaximumQualityPolicyBytes    int64 = 1 << 20
	maximumQualityPolicyAssertions            = 256
	maximumQualityPolicyIdentifierBytes       = 128
)

// QualityAssertionScope selects the complete evaluation or one named slice.
type QualityAssertionScope string

// QualityMetric identifies an aggregate report value accepted by the gate.
type QualityMetric string

// QualityComparisonOperator identifies a threshold comparison.
type QualityComparisonOperator string

const (
	QualityAssertionScopeOverall QualityAssertionScope = "overall"
	QualityAssertionScopeSlice   QualityAssertionScope = "slice"

	QualityMetricRecall         QualityMetric = "recall"
	QualityMetricMRR            QualityMetric = "mrr"
	QualityMetricNDCG           QualityMetric = "ndcg"
	QualityMetricJudgedRate     QualityMetric = "judged_rate"
	QualityMetricZeroResultRate QualityMetric = "zero_result_rate"
	QualityMetricFailureRate    QualityMetric = "failure_rate"
	QualityMetricLatencyMS      QualityMetric = "latency_ms"

	QualityComparisonGreaterThanOrEqual QualityComparisonOperator = "gte"
	QualityComparisonLessThanOrEqual    QualityComparisonOperator = "lte"

	qualityGateCodePassed                   = "passed"
	qualityGateCodeReportIncomplete         = "report_incomplete"
	qualityGateCodeCatalogRevisionMismatch  = "catalog_revision_mismatch"
	qualityGateCodePipelineNotAllowed       = "pipeline_not_allowed"
	qualityGateCodeDegradedResponsesPresent = "degraded_responses_present"
	qualityGateCodeMetricMissing            = "metric_missing"
	qualityGateCodeSliceMissing             = "slice_missing"
	qualityGateCodeMetricNonFinite          = "metric_non_finite"
	qualityGateCodeThresholdNotMet          = "threshold_not_met"
)

// QualityPolicyLoadOptions controls the fixed artifact read bound.
type QualityPolicyLoadOptions struct {
	MaximumBytes int64
}

// QualityPolicyReference binds a report to the exact reviewed policy bytes.
type QualityPolicyReference struct {
	SchemaVersion string `json:"schema_version"`
	ID            string `json:"id"`
	Version       string `json:"version"`
	SHA256        string `json:"sha256"`
}

type qualityPolicyRequirements struct {
	Complete          bool
	CatalogRevision   string
	AllowedPipelines  []models.SearchPipeline
	DegradedResponses int
}

type qualityAssertion struct {
	ID       string
	Scope    QualityAssertionScope
	Slice    string
	Metric   QualityMetric
	Cutoff   *int
	Quantile *float64
	Operator QualityComparisonOperator
	Value    float64
}

// QualityPolicy is an opaque, strictly loaded executable acceptance artifact.
type QualityPolicy struct {
	schemaVersion string
	id            string
	version       string
	requirements  qualityPolicyRequirements
	assertions    []qualityAssertion
	reference     QualityPolicyReference
}

// Reference returns the immutable identity calculated from the loaded artifact.
func (policy QualityPolicy) Reference() QualityPolicyReference {
	return policy.reference
}

// QualityRequirementResult records one fail-closed runtime requirement.
type QualityRequirementResult struct {
	Requirement string `json:"requirement"`
	Passed      bool   `json:"passed"`
	Code        string `json:"code"`
}

// QualityAssertionResult records one bounded aggregate threshold decision.
type QualityAssertionResult struct {
	AssertionID string                    `json:"assertion_id"`
	Scope       QualityAssertionScope     `json:"scope"`
	Slice       string                    `json:"slice,omitempty"`
	Metric      QualityMetric             `json:"metric"`
	Cutoff      *int                      `json:"cutoff,omitempty"`
	Quantile    *float64                  `json:"quantile,omitempty"`
	Operator    QualityComparisonOperator `json:"operator"`
	Expected    float64                   `json:"expected"`
	Actual      *float64                  `json:"actual,omitempty"`
	Passed      bool                      `json:"passed"`
	Code        string                    `json:"code"`
}

// QualityGateResult is the deterministic, privacy-safe serialized gate result.
type QualityGateResult struct {
	SchemaVersion string                     `json:"schema_version"`
	Passed        bool                       `json:"passed"`
	FailedChecks  int                        `json:"failed_checks"`
	Requirements  []QualityRequirementResult `json:"requirements"`
	Assertions    []QualityAssertionResult   `json:"assertions"`
}

// QualityGateError reports that a serialized evaluation failed its policy.
type QualityGateError struct {
	FailedChecks int
}

func (qualityError *QualityGateError) Error() string {
	return fmt.Sprintf("search quality gate failed %d check(s)", qualityError.FailedChecks)
}

type qualityPolicyDocument struct {
	SchemaVersion string                             `json:"schema_version"`
	ID            string                             `json:"id"`
	Version       string                             `json:"version"`
	Requirements  *qualityPolicyRequirementsDocument `json:"requirements"`
	Assertions    []qualityAssertionDocument         `json:"assertions"`
}

type qualityPolicyRequirementsDocument struct {
	Complete          *bool                    `json:"complete"`
	CatalogRevision   *string                  `json:"catalog_revision"`
	AllowedPipelines  *[]models.SearchPipeline `json:"allowed_pipelines"`
	DegradedResponses *int                     `json:"degraded_responses"`
}

type qualityAssertionDocument struct {
	ID       string                    `json:"id"`
	Scope    QualityAssertionScope     `json:"scope"`
	Slice    string                    `json:"slice,omitempty"`
	Metric   QualityMetric             `json:"metric"`
	Cutoff   *int                      `json:"cutoff,omitempty"`
	Quantile *float64                  `json:"quantile,omitempty"`
	Operator QualityComparisonOperator `json:"operator"`
	Value    *float64                  `json:"value"`
}

// LoadQualityPolicy strictly decodes and hashes one bounded policy artifact.
func LoadQualityPolicy(policyReader io.Reader, options QualityPolicyLoadOptions) (QualityPolicy, error) {
	if policyReader == nil {
		return QualityPolicy{}, errors.New("quality policy reader is required")
	}
	maximumBytes := options.MaximumBytes
	if maximumBytes <= 0 {
		maximumBytes = DefaultMaximumQualityPolicyBytes
	}
	if maximumBytes > DefaultMaximumQualityPolicyBytes {
		return QualityPolicy{}, errors.New("quality policy byte bound exceeds the supported maximum")
	}
	encodedPolicy, readError := io.ReadAll(io.LimitReader(policyReader, maximumBytes+1))
	if readError != nil {
		return QualityPolicy{}, fmt.Errorf("read quality policy: %w", readError)
	}
	if int64(len(encodedPolicy)) > maximumBytes {
		return QualityPolicy{}, fmt.Errorf("quality policy exceeds the configured byte bound")
	}
	if len(bytes.TrimSpace(encodedPolicy)) == 0 {
		return QualityPolicy{}, errors.New("quality policy is empty")
	}
	if !utf8.Valid(encodedPolicy) {
		return QualityPolicy{}, errors.New("quality policy is not valid UTF-8")
	}
	if structureError := validateJSONStructure(encodedPolicy); structureError != nil {
		if errors.Is(structureError, errDuplicateJSONKey) {
			return QualityPolicy{}, errors.New("quality policy contains a duplicate JSON key")
		}
		return QualityPolicy{}, errors.New("quality policy is not one valid JSON object")
	}

	policyDecoder := json.NewDecoder(bytes.NewReader(encodedPolicy))
	policyDecoder.DisallowUnknownFields()
	var document qualityPolicyDocument
	if decodeError := policyDecoder.Decode(&document); decodeError != nil {
		return QualityPolicy{}, fmt.Errorf("quality policy violates its schema: %w", decodeError)
	}
	var trailingValue any
	if trailingError := policyDecoder.Decode(&trailingValue); !errors.Is(trailingError, io.EOF) {
		return QualityPolicy{}, errors.New("quality policy contains trailing JSON")
	}

	policy, validationError := validateQualityPolicyDocument(document)
	if validationError != nil {
		return QualityPolicy{}, validationError
	}
	policyDigest := sha256.Sum256(encodedPolicy)
	policy.reference = QualityPolicyReference{
		SchemaVersion: policy.schemaVersion,
		ID:            policy.id,
		Version:       policy.version,
		SHA256:        hex.EncodeToString(policyDigest[:]),
	}
	return policy, nil
}

func validateQualityPolicyDocument(document qualityPolicyDocument) (QualityPolicy, error) {
	if document.SchemaVersion != QualityPolicySchemaVersion {
		return QualityPolicy{}, fmt.Errorf("quality policy schema_version must be %q", QualityPolicySchemaVersion)
	}
	if !validBoundedStableIdentifier(document.ID) || !validBoundedStableIdentifier(document.Version) {
		return QualityPolicy{}, errors.New("quality policy id and version must be bounded stable identifiers")
	}
	if document.Requirements == nil || document.Requirements.Complete == nil ||
		document.Requirements.CatalogRevision == nil || document.Requirements.AllowedPipelines == nil ||
		document.Requirements.DegradedResponses == nil {
		return QualityPolicy{}, errors.New("quality policy requirements are incomplete")
	}
	if !*document.Requirements.Complete {
		return QualityPolicy{}, errors.New("quality policy must require a complete report")
	}
	catalogRevision := *document.Requirements.CatalogRevision
	if !validBoundedStableIdentifier(catalogRevision) || catalogRevision == "unversioned" {
		return QualityPolicy{}, errors.New("quality policy must pin a versioned catalog revision")
	}
	if *document.Requirements.DegradedResponses != 0 {
		return QualityPolicy{}, errors.New("quality policy must require zero degraded responses")
	}
	allowedPipelines, pipelineError := normalizeAllowedPipelines(*document.Requirements.AllowedPipelines)
	if pipelineError != nil {
		return QualityPolicy{}, pipelineError
	}
	if len(document.Assertions) == 0 || len(document.Assertions) > maximumQualityPolicyAssertions {
		return QualityPolicy{}, fmt.Errorf("quality policy must contain a bounded non-empty assertion list")
	}

	assertions := make([]qualityAssertion, 0, len(document.Assertions))
	seenAssertionIDs := make(map[string]struct{}, len(document.Assertions))
	for assertionIndex, assertionDocument := range document.Assertions {
		assertion, assertionError := validateQualityAssertion(assertionDocument)
		if assertionError != nil {
			return QualityPolicy{}, fmt.Errorf("quality policy assertion %d: %w", assertionIndex, assertionError)
		}
		if _, duplicate := seenAssertionIDs[assertion.ID]; duplicate {
			return QualityPolicy{}, fmt.Errorf("quality policy contains duplicate assertion id %q", assertion.ID)
		}
		seenAssertionIDs[assertion.ID] = struct{}{}
		assertions = append(assertions, assertion)
	}
	slices.SortFunc(assertions, func(firstAssertion, secondAssertion qualityAssertion) int {
		return strings.Compare(firstAssertion.ID, secondAssertion.ID)
	})

	return QualityPolicy{
		schemaVersion: document.SchemaVersion,
		id:            document.ID,
		version:       document.Version,
		requirements: qualityPolicyRequirements{
			Complete:          true,
			CatalogRevision:   catalogRevision,
			AllowedPipelines:  allowedPipelines,
			DegradedResponses: 0,
		},
		assertions: assertions,
	}, nil
}

func normalizeAllowedPipelines(pipelines []models.SearchPipeline) ([]models.SearchPipeline, error) {
	if len(pipelines) == 0 {
		return nil, errors.New("quality policy must allow at least one pipeline")
	}
	seenPipelines := make(map[models.SearchPipeline]struct{}, len(pipelines))
	for _, pipeline := range pipelines {
		if !pipeline.Valid() {
			return nil, fmt.Errorf("quality policy contains invalid pipeline %q", pipeline)
		}
		if _, duplicate := seenPipelines[pipeline]; duplicate {
			return nil, fmt.Errorf("quality policy contains duplicate pipeline %q", pipeline)
		}
		seenPipelines[pipeline] = struct{}{}
	}
	normalizedPipelines := slices.Clone(pipelines)
	slices.Sort(normalizedPipelines)
	return normalizedPipelines, nil
}

func validateQualityAssertion(document qualityAssertionDocument) (qualityAssertion, error) {
	if !validBoundedStableIdentifier(document.ID) {
		return qualityAssertion{}, errors.New("id must be a bounded stable identifier")
	}
	if document.Scope != QualityAssertionScopeOverall && document.Scope != QualityAssertionScopeSlice {
		return qualityAssertion{}, errors.New("scope must be overall or slice")
	}
	if document.Scope == QualityAssertionScopeOverall && document.Slice != "" {
		return qualityAssertion{}, errors.New("overall assertion must not specify slice")
	}
	if document.Scope == QualityAssertionScopeSlice && !validBoundedStableIdentifier(document.Slice) {
		return qualityAssertion{}, errors.New("slice assertion must specify a bounded stable slice")
	}
	if document.Operator != QualityComparisonGreaterThanOrEqual && document.Operator != QualityComparisonLessThanOrEqual {
		return qualityAssertion{}, errors.New("operator must be gte or lte")
	}
	if document.Value == nil || !finite(*document.Value) {
		return qualityAssertion{}, errors.New("value must be finite")
	}
	assertionValue := *document.Value
	if assertionValue == 0 {
		assertionValue = 0
	}

	assertion := qualityAssertion{
		ID:       document.ID,
		Scope:    document.Scope,
		Slice:    document.Slice,
		Metric:   document.Metric,
		Operator: document.Operator,
		Value:    assertionValue,
	}
	switch document.Metric {
	case QualityMetricRecall, QualityMetricMRR, QualityMetricNDCG, QualityMetricJudgedRate:
		if document.Cutoff == nil || *document.Cutoff <= 0 || document.Quantile != nil {
			return qualityAssertion{}, errors.New("ranking metric requires only a positive cutoff")
		}
		if assertion.Value < 0 || assertion.Value > 1 {
			return qualityAssertion{}, errors.New("ranking metric value must be between zero and one")
		}
		cutoff := *document.Cutoff
		assertion.Cutoff = &cutoff
	case QualityMetricZeroResultRate, QualityMetricFailureRate:
		if document.Cutoff != nil || document.Quantile != nil {
			return qualityAssertion{}, errors.New("rate metric must not specify cutoff or quantile")
		}
		if assertion.Value < 0 || assertion.Value > 1 {
			return qualityAssertion{}, errors.New("rate metric value must be between zero and one")
		}
	case QualityMetricLatencyMS:
		if document.Cutoff != nil || document.Quantile == nil ||
			!finite(*document.Quantile) || *document.Quantile < 0 || *document.Quantile > 1 {
			return qualityAssertion{}, errors.New("latency metric requires only a finite quantile between zero and one")
		}
		if assertion.Value < 0 {
			return qualityAssertion{}, errors.New("latency value must be non-negative")
		}
		quantile := *document.Quantile
		if quantile == 0 {
			quantile = 0
		}
		assertion.Quantile = &quantile
	default:
		return qualityAssertion{}, errors.New("metric is unsupported")
	}
	return assertion, nil
}

func validBoundedStableIdentifier(identifier string) bool {
	return len(identifier) <= maximumQualityPolicyIdentifierBytes && stableIdentifierPattern.MatchString(identifier)
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// EvaluateQualityGate deterministically evaluates provenance and aggregate assertions.
func EvaluateQualityGate(report Report, policy QualityPolicy) QualityGateResult {
	requirementResults := []QualityRequirementResult{
		qualityRequirementResult("complete", report.Complete == policy.requirements.Complete, qualityGateCodeReportIncomplete),
		qualityRequirementResult(
			"catalog_revision",
			len(report.CatalogRevisions) == 1 &&
				report.CatalogRevisions[0] == policy.requirements.CatalogRevision &&
				report.CatalogRevisions[0] != "unversioned",
			qualityGateCodeCatalogRevisionMismatch,
		),
		qualityRequirementResult(
			"allowed_pipelines",
			effectivePipelinesAllowed(report.EffectivePipelines, policy.requirements.AllowedPipelines),
			qualityGateCodePipelineNotAllowed,
		),
		qualityRequirementResult(
			"degraded_responses",
			report.DegradedResponses == policy.requirements.DegradedResponses,
			qualityGateCodeDegradedResponsesPresent,
		),
	}

	assertionResults := make([]QualityAssertionResult, 0, len(policy.assertions))
	failedChecks := 0
	for _, requirementResult := range requirementResults {
		if !requirementResult.Passed {
			failedChecks++
		}
	}
	for _, assertion := range policy.assertions {
		assertionResult := evaluateQualityAssertion(report, assertion)
		if !assertionResult.Passed {
			failedChecks++
		}
		assertionResults = append(assertionResults, assertionResult)
	}

	return QualityGateResult{
		SchemaVersion: QualityGateResultSchemaVersion,
		Passed:        failedChecks == 0,
		FailedChecks:  failedChecks,
		Requirements:  requirementResults,
		Assertions:    assertionResults,
	}
}

func qualityRequirementResult(requirement string, passed bool, failureCode string) QualityRequirementResult {
	code := failureCode
	if passed {
		code = qualityGateCodePassed
	}
	return QualityRequirementResult{Requirement: requirement, Passed: passed, Code: code}
}

func effectivePipelinesAllowed(effectivePipelines []string, allowedPipelines []models.SearchPipeline) bool {
	if len(effectivePipelines) == 0 {
		return false
	}
	allowedPipelineSet := make(map[string]struct{}, len(allowedPipelines))
	for _, allowedPipeline := range allowedPipelines {
		allowedPipelineSet[string(allowedPipeline)] = struct{}{}
	}
	for _, effectivePipeline := range effectivePipelines {
		if _, allowed := allowedPipelineSet[effectivePipeline]; !allowed {
			return false
		}
	}
	return true
}

func evaluateQualityAssertion(report Report, assertion qualityAssertion) QualityAssertionResult {
	assertionResult := QualityAssertionResult{
		AssertionID: assertion.ID,
		Scope:       assertion.Scope,
		Slice:       assertion.Slice,
		Metric:      assertion.Metric,
		Cutoff:      cloneIntPointer(assertion.Cutoff),
		Quantile:    cloneFloatPointer(assertion.Quantile),
		Operator:    assertion.Operator,
		Expected:    assertion.Value,
		Code:        qualityGateCodeMetricMissing,
	}

	aggregate := report.Overall
	if assertion.Scope == QualityAssertionScopeSlice {
		var slicePresent bool
		aggregate, slicePresent = report.BySlice[assertion.Slice]
		if !slicePresent {
			assertionResult.Code = qualityGateCodeSliceMissing
			return assertionResult
		}
	}
	actual, present := qualityMetricValue(aggregate, assertion)
	if !present {
		return assertionResult
	}
	if !finite(actual) {
		assertionResult.Code = qualityGateCodeMetricNonFinite
		return assertionResult
	}
	assertionResult.Actual = &actual
	assertionResult.Passed = compareQualityMetric(actual, assertion.Operator, assertion.Value)
	if assertionResult.Passed {
		assertionResult.Code = qualityGateCodePassed
	} else {
		assertionResult.Code = qualityGateCodeThresholdNotMet
	}
	return assertionResult
}

func qualityMetricValue(aggregate AggregateMetrics, assertion qualityAssertion) (float64, bool) {
	switch assertion.Metric {
	case QualityMetricRecall, QualityMetricMRR, QualityMetricNDCG, QualityMetricJudgedRate:
		if assertion.Cutoff == nil {
			return 0, false
		}
		cutoffMetrics, present := aggregate.MetricsAtCutoff[strconv.Itoa(*assertion.Cutoff)]
		if !present {
			return 0, false
		}
		switch assertion.Metric {
		case QualityMetricRecall:
			return cutoffMetrics.Recall, true
		case QualityMetricMRR:
			return cutoffMetrics.MRR, true
		case QualityMetricNDCG:
			return cutoffMetrics.NDCG, true
		default:
			return cutoffMetrics.JudgedRate, true
		}
	case QualityMetricZeroResultRate:
		return aggregate.ZeroResultRate, true
	case QualityMetricFailureRate:
		if aggregate.Queries <= 0 {
			return 0, false
		}
		return float64(aggregate.FailedQueries) / float64(aggregate.Queries), true
	case QualityMetricLatencyMS:
		if assertion.Quantile == nil {
			return 0, false
		}
		latency, present := aggregate.LatencyMilliseconds[quantileLabel(*assertion.Quantile)]
		return latency, present
	default:
		return 0, false
	}
}

func compareQualityMetric(actual float64, operator QualityComparisonOperator, expected float64) bool {
	switch operator {
	case QualityComparisonGreaterThanOrEqual:
		return actual >= expected
	case QualityComparisonLessThanOrEqual:
		return actual <= expected
	default:
		return false
	}
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	valueCopy := *value
	return &valueCopy
}

func cloneFloatPointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	valueCopy := *value
	return &valueCopy
}
