package evaluation

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

// ReportSchemaVersion identifies the serialized report contract.
const ReportSchemaVersion = "search-evaluation/v4"

const maximumRelevanceGrade = 3

var validItemSources = map[models.ItemSource]struct{}{
	models.SourceSalesForce: {},
	models.SourceCourses:    {},
	models.SourceJobs:       {},
	models.SourceMEI:        {},
	models.SourceAppGoAPI:   {},
	models.SourceTypesense:  {},
}

func isCanonicalSourceID(sourceID string) bool {
	return sourceID != "" && sourceID == strings.TrimSpace(sourceID) &&
		strings.IndexFunc(sourceID, unicode.IsControl) == -1
}

// DocumentKey identifies one source document without using its database UUID.
type DocumentKey struct {
	Source   models.ItemSource `json:"source"`
	SourceID string            `json:"source_id"`
}

// EntityJudgment grades one canonical entity and lists every accepted alias.
type EntityJudgment struct {
	EntityID  string        `json:"entity_id"`
	Grade     int           `json:"grade"`
	Documents []DocumentKey `json:"documents"`
}

// Query is one privacy-sensitive search input and its non-sensitive judgments.
type Query struct {
	QueryID string               `json:"query_id"`
	Text    string               `json:"query"`
	Types   []models.ItemType    `json:"types,omitempty"`
	Filters models.SearchFilters `json:"filters,omitempty"`
	Slices  []string             `json:"slices,omitempty"`
	Qrels   []EntityJudgment     `json:"qrels"`
}

// Dataset contains validated queries and the SHA-256 of the original JSONL bytes.
type Dataset struct {
	Queries []Query
	Hash    string
}

// FailureStage identifies the boundary at which an evaluation record failed.
type FailureStage string

const (
	FailureStageDataset   FailureStage = "dataset"
	FailureStageTransport FailureStage = "transport"
	FailureStageContract  FailureStage = "contract"
)

// Failure is a bounded, privacy-safe failure included in a report.
type Failure struct {
	QueryID string       `json:"query_id,omitempty"`
	Line    int          `json:"line,omitempty"`
	Stage   FailureStage `json:"stage"`
	Code    string       `json:"code"`
	Message string       `json:"message"`
}

// SearchObservation is the validated result list used for offline scoring.
type SearchObservation struct {
	QueryID              string
	Documents            []DocumentKey
	SearchID             string
	RankerVersion        string
	RankerDescriptorHash string
	CatalogRevision      string
	EffectivePipeline    models.SearchPipeline
	Degraded             bool
	Latency              time.Duration
}

// Searcher retrieves one bounded result list for an evaluation query.
type Searcher interface {
	Search(context.Context, Query) (SearchObservation, error)
}

// SearchFailureError carries an internal cause and report-safe classification.
type SearchFailureError struct {
	Stage       FailureStage
	Code        string
	SafeMessage string
	Cause       error
}

func (e *SearchFailureError) Error() string {
	if e.SafeMessage != "" {
		return e.SafeMessage
	}
	return "search evaluation request failed"
}

func (e *SearchFailureError) Unwrap() error {
	return e.Cause
}

// FailuresError makes an incomplete evaluation fail unless continuation was explicit.
type FailuresError struct {
	Count int
}

func (e *FailuresError) Error() string {
	return fmt.Sprintf("search evaluation contains %d failure(s)", e.Count)
}
