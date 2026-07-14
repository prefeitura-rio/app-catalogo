package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prefeitura-rio/app-catalogo/internal/evaluation"
)

const (
	defaultEndpoint                = "http://localhost:8080/api/public/search"
	defaultRequestTimeout          = 5 * time.Second
	defaultConcurrency             = 4
	defaultCandidateLimit          = 100
	defaultCutoffs                 = "1,3,5,10"
	defaultLatencyQuantiles        = "0.5,0.95,0.99"
	defaultMaximumResponseBytes    = 4 << 20
	defaultMaximumDatasetLineBytes = 1 << 20
)

type commandConfig struct {
	datasetPath               string
	endpoint                  string
	outputPath                string
	requestTimeout            time.Duration
	concurrency               int
	candidateLimit            int
	cutoffs                   []int
	latencyQuantiles          []float64
	continueOnError           bool
	runTimestamp              func() time.Time
	maximumResponseBytes      int64
	maximumDatasetLineBytes   int
	maximumQualityPolicyBytes int64
	qualityPolicy             *evaluation.QualityPolicy
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, standardOutput, standardError io.Writer) int {
	config, configError := parseCommandConfig(arguments, standardError)
	if configError != nil {
		if errors.Is(configError, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(standardError, "search evaluation configuration failed: %v\n", configError)
		return 2
	}

	datasetFile, openError := os.Open(config.datasetPath)
	if openError != nil {
		fmt.Fprintf(standardError, "search evaluation dataset could not be opened: %v\n", openError)
		return 1
	}
	dataset, _, loadError := evaluation.LoadJSONL(datasetFile, evaluation.DatasetLoadOptions{
		MaximumLineBytes: config.maximumDatasetLineBytes,
	})
	closeError := datasetFile.Close()
	if loadError != nil {
		fmt.Fprintf(standardError, "search evaluation dataset validation failed: %v\n", errors.Join(loadError, closeError))
		return 1
	}
	if closeError != nil {
		fmt.Fprintf(standardError, "search evaluation dataset could not be closed: %v\n", closeError)
		return 1
	}

	defaultTransport, isHTTPTransport := http.DefaultTransport.(*http.Transport)
	if !isHTTPTransport {
		fmt.Fprintln(standardError, "search evaluation HTTP transport configuration failed")
		return 2
	}
	httpTransport := defaultTransport.Clone()
	httpTransport.MaxConnsPerHost = config.concurrency
	httpTransport.MaxIdleConnsPerHost = config.concurrency
	httpClient, clientError := evaluation.NewHTTPClient(evaluation.HTTPClientConfig{
		Endpoint:             config.endpoint,
		RequestTimeout:       config.requestTimeout,
		CandidateLimit:       config.candidateLimit,
		MaximumResponseBytes: config.maximumResponseBytes,
		Client: &http.Client{
			Transport:     httpTransport,
			CheckRedirect: rejectRedirects,
		},
	})
	if clientError != nil {
		fmt.Fprintf(standardError, "search evaluation HTTP client configuration failed: %v\n", clientError)
		return 2
	}

	evaluationContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	var qualityPolicyReference *evaluation.QualityPolicyReference
	if config.qualityPolicy != nil {
		policyReference := config.qualityPolicy.Reference()
		qualityPolicyReference = &policyReference
	}
	report, evaluationError := evaluation.Evaluate(evaluationContext, dataset, httpClient, evaluation.EvaluatorConfig{
		Endpoint:                  httpClient.Endpoint(),
		Concurrency:               config.concurrency,
		CandidateLimit:            config.candidateLimit,
		Cutoffs:                   config.cutoffs,
		LatencyQuantiles:          config.latencyQuantiles,
		ContinueOnError:           config.continueOnError,
		RequestTimeout:            config.requestTimeout,
		MaximumResponseBytes:      config.maximumResponseBytes,
		MaximumDatasetLineBytes:   config.maximumDatasetLineBytes,
		MaximumQualityPolicyBytes: config.maximumQualityPolicyBytes,
		QualityPolicy:             qualityPolicyReference,
		RunTimestamp:              config.runTimestamp,
	})
	var qualityGateError error
	if report.SchemaVersion != "" && config.qualityPolicy != nil {
		qualityGate := evaluation.EvaluateQualityGate(report, *config.qualityPolicy)
		report.QualityGate = &qualityGate
		if !qualityGate.Passed {
			qualityGateError = &evaluation.QualityGateError{FailedChecks: qualityGate.FailedChecks}
		}
	}
	if report.SchemaVersion != "" {
		encodedReport, encodeError := evaluation.MarshalReport(report)
		if encodeError != nil {
			fmt.Fprintf(standardError, "search evaluation report encoding failed: %v\n", encodeError)
			return 1
		}
		if writeError := writeReport(config.outputPath, encodedReport, standardOutput); writeError != nil {
			fmt.Fprintf(standardError, "search evaluation report write failed: %v\n", writeError)
			return 1
		}
	}
	if runError := errors.Join(evaluationError, qualityGateError); runError != nil {
		fmt.Fprintf(standardError, "search evaluation failed: %v\n", runError)
		return 1
	}
	return 0
}

func parseCommandConfig(arguments []string, standardError io.Writer) (commandConfig, error) {
	flagSet := flag.NewFlagSet("search-eval", flag.ContinueOnError)
	flagSet.SetOutput(standardError)
	datasetPath := flagSet.String("dataset", "", "path to the JSONL evaluation dataset")
	endpoint := flagSet.String("endpoint", defaultEndpoint, "public app-catalogo search URL")
	outputPath := flagSet.String("output", "-", "report path, or - for stdout")
	requestTimeout := flagSet.Duration("timeout", defaultRequestTimeout, "timeout for each HTTP request")
	concurrency := flagSet.Int("concurrency", defaultConcurrency, "maximum concurrent HTTP requests")
	candidateLimit := flagSet.Int("candidate-limit", defaultCandidateLimit, "number of results requested per query")
	cutoffsText := flagSet.String("cutoffs", defaultCutoffs, "comma-separated ranking cutoffs")
	quantilesText := flagSet.String("latency-quantiles", defaultLatencyQuantiles, "comma-separated latency quantiles")
	continueOnError := flagSet.Bool("continue-on-error", false, "allow HTTP failures to produce a successful incomplete run")
	runTimestampText := flagSet.String("run-timestamp", "", "RFC3339 timestamp override for reproducible reports")
	maximumResponseBytes := flagSet.Int64("max-response-bytes", defaultMaximumResponseBytes, "maximum HTTP response body size")
	maximumDatasetLineBytes := flagSet.Int("max-dataset-line-bytes", defaultMaximumDatasetLineBytes, "maximum JSONL line size")
	qualityPolicyPath := flagSet.String("quality-policy", "", "path to a versioned search quality policy JSON artifact")
	if parseError := flagSet.Parse(arguments); parseError != nil {
		return commandConfig{}, parseError
	}
	if flagSet.NArg() != 0 {
		return commandConfig{}, errors.New("positional arguments are not supported")
	}
	if strings.TrimSpace(*datasetPath) == "" {
		return commandConfig{}, errors.New("-dataset is required")
	}
	if strings.TrimSpace(*outputPath) == "" {
		return commandConfig{}, errors.New("-output must not be empty")
	}
	if *requestTimeout <= 0 || *concurrency < 1 || *maximumResponseBytes < 1 || *maximumDatasetLineBytes < 1 {
		return commandConfig{}, errors.New("timeout, concurrency, and byte bounds must be positive")
	}
	cutoffs, cutoffError := parseIntegerList(*cutoffsText)
	if cutoffError != nil {
		return commandConfig{}, fmt.Errorf("parse -cutoffs: %w", cutoffError)
	}
	if _, cutoffError = evaluation.NormalizeCutoffs(cutoffs, *candidateLimit); cutoffError != nil {
		return commandConfig{}, cutoffError
	}
	latencyQuantiles, quantileError := parseFloatList(*quantilesText)
	if quantileError != nil {
		return commandConfig{}, fmt.Errorf("parse -latency-quantiles: %w", quantileError)
	}
	if _, quantileError = evaluation.NormalizeQuantiles(latencyQuantiles); quantileError != nil {
		return commandConfig{}, quantileError
	}
	var qualityPolicy *evaluation.QualityPolicy
	if strings.TrimSpace(*qualityPolicyPath) != "" {
		loadedPolicy, policyError := loadQualityPolicy(*qualityPolicyPath, evaluation.DefaultMaximumQualityPolicyBytes)
		if policyError != nil {
			return commandConfig{}, fmt.Errorf("load -quality-policy: %w", policyError)
		}
		qualityPolicy = &loadedPolicy
	}

	runTimestamp := time.Now
	if strings.TrimSpace(*runTimestampText) != "" {
		parsedTimestamp, timestampError := time.Parse(time.RFC3339Nano, strings.TrimSpace(*runTimestampText))
		if timestampError != nil {
			return commandConfig{}, errors.New("-run-timestamp must be RFC3339")
		}
		runTimestamp = fixedTimestampClock(parsedTimestamp)
	}
	return commandConfig{
		datasetPath:               *datasetPath,
		endpoint:                  *endpoint,
		outputPath:                *outputPath,
		requestTimeout:            *requestTimeout,
		concurrency:               *concurrency,
		candidateLimit:            *candidateLimit,
		cutoffs:                   cutoffs,
		latencyQuantiles:          latencyQuantiles,
		continueOnError:           *continueOnError,
		runTimestamp:              runTimestamp,
		maximumResponseBytes:      *maximumResponseBytes,
		maximumDatasetLineBytes:   *maximumDatasetLineBytes,
		maximumQualityPolicyBytes: evaluation.DefaultMaximumQualityPolicyBytes,
		qualityPolicy:             qualityPolicy,
	}, nil
}

func loadQualityPolicy(policyPath string, maximumBytes int64) (evaluation.QualityPolicy, error) {
	policyFile, openError := os.Open(policyPath)
	if openError != nil {
		return evaluation.QualityPolicy{}, openError
	}
	policy, loadError := evaluation.LoadQualityPolicy(policyFile, evaluation.QualityPolicyLoadOptions{
		MaximumBytes: maximumBytes,
	})
	closeError := policyFile.Close()
	if loadError != nil || closeError != nil {
		return evaluation.QualityPolicy{}, errors.Join(loadError, closeError)
	}
	return policy, nil
}

func parseIntegerList(encodedValues string) ([]int, error) {
	values := strings.Split(encodedValues, ",")
	parsedValues := make([]int, 0, len(values))
	for _, encodedValue := range values {
		parsedValue, parseError := strconv.Atoi(strings.TrimSpace(encodedValue))
		if parseError != nil {
			return nil, errors.New("list contains a non-integer value")
		}
		parsedValues = append(parsedValues, parsedValue)
	}
	return parsedValues, nil
}

func parseFloatList(encodedValues string) ([]float64, error) {
	values := strings.Split(encodedValues, ",")
	parsedValues := make([]float64, 0, len(values))
	for _, encodedValue := range values {
		parsedValue, parseError := strconv.ParseFloat(strings.TrimSpace(encodedValue), 64)
		if parseError != nil {
			return nil, errors.New("list contains a non-numeric value")
		}
		parsedValues = append(parsedValues, parsedValue)
	}
	return parsedValues, nil
}

func writeReport(outputPath string, encodedReport []byte, standardOutput io.Writer) error {
	if outputPath == "-" {
		_, writeError := standardOutput.Write(encodedReport)
		return writeError
	}

	reportDirectory := filepath.Dir(outputPath)
	temporaryReport, createError := os.CreateTemp(reportDirectory, ".search-evaluation-*.json")
	if createError != nil {
		return createError
	}
	temporaryPath := temporaryReport.Name()
	if chmodError := temporaryReport.Chmod(0o600); chmodError != nil {
		return errors.Join(chmodError, cleanupTemporaryReport(temporaryReport, temporaryPath))
	}
	if _, writeError := temporaryReport.Write(encodedReport); writeError != nil {
		return errors.Join(writeError, cleanupTemporaryReport(temporaryReport, temporaryPath))
	}
	if syncError := temporaryReport.Sync(); syncError != nil {
		return errors.Join(syncError, cleanupTemporaryReport(temporaryReport, temporaryPath))
	}
	if closeError := temporaryReport.Close(); closeError != nil {
		return errors.Join(closeError, os.Remove(temporaryPath))
	}
	if renameError := os.Rename(temporaryPath, outputPath); renameError != nil {
		return errors.Join(renameError, os.Remove(temporaryPath))
	}
	return nil
}

func rejectRedirects(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func fixedTimestampClock(timestamp time.Time) func() time.Time {
	return func() time.Time { return timestamp }
}

func cleanupTemporaryReport(temporaryReport *os.File, temporaryPath string) error {
	return errors.Join(temporaryReport.Close(), os.Remove(temporaryPath))
}
