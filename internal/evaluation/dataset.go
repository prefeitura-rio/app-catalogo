package evaluation

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

const defaultMaximumDatasetLineBytes = 1 << 20

var stableIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

var errDuplicateJSONKey = errors.New("duplicate JSON key")

// DatasetLoadOptions bounds JSONL input before strict validation.
type DatasetLoadOptions struct {
	MaximumLineBytes int
}

// LoadJSONL validates, canonicalizes, and hashes a JSONL evaluation dataset.
func LoadJSONL(datasetReader io.Reader, options DatasetLoadOptions) (Dataset, []Failure, error) {
	if datasetReader == nil {
		return Dataset{}, nil, errors.New("dataset reader is required")
	}
	maximumLineBytes := options.MaximumLineBytes
	if maximumLineBytes <= 0 {
		maximumLineBytes = defaultMaximumDatasetLineBytes
	}

	datasetHasher := sha256.New()
	datasetScanner := bufio.NewScanner(io.TeeReader(datasetReader, datasetHasher))
	datasetScanner.Buffer(make([]byte, min(maximumLineBytes, bufio.MaxScanTokenSize)), maximumLineBytes)

	dataset := Dataset{}
	failures := make([]Failure, 0)
	seenQueryIDs := make(map[string]struct{})
	lineNumber := 0
	for datasetScanner.Scan() {
		lineNumber++
		query, queryFailure := decodeDatasetQuery(datasetScanner.Bytes(), lineNumber, seenQueryIDs)
		if queryFailure != nil {
			failures = append(failures, *queryFailure)
			continue
		}
		seenQueryIDs[query.QueryID] = struct{}{}
		dataset.Queries = append(dataset.Queries, query)
	}
	dataset.Hash = hex.EncodeToString(datasetHasher.Sum(nil))

	if scanError := datasetScanner.Err(); scanError != nil {
		return dataset, append(failures, Failure{
			Line:    lineNumber + 1,
			Stage:   FailureStageDataset,
			Code:    "read_error",
			Message: "dataset could not be read within the configured line bound",
		}), fmt.Errorf("read evaluation dataset: %w", scanError)
	}
	if len(dataset.Queries) == 0 {
		return dataset, failures, errors.New("evaluation dataset contains no valid queries")
	}
	if len(failures) > 0 {
		return dataset, failures, &FailuresError{Count: len(failures)}
	}
	return dataset, failures, nil
}

func decodeDatasetQuery(encodedQuery []byte, lineNumber int, seenQueryIDs map[string]struct{}) (Query, *Failure) {
	if len(strings.TrimSpace(string(encodedQuery))) == 0 {
		return Query{}, datasetFailure(lineNumber, "", "empty_line", "dataset line is empty")
	}
	if !utf8.Valid(encodedQuery) {
		return Query{}, datasetFailure(lineNumber, "", "invalid_utf8", "dataset line is not valid UTF-8")
	}
	if structureError := validateJSONStructure(encodedQuery); structureError != nil {
		if errors.Is(structureError, errDuplicateJSONKey) {
			return Query{}, datasetFailure(lineNumber, "", "duplicate_json_key", "dataset line contains a duplicate JSON key")
		}
		return Query{}, datasetFailure(lineNumber, "", "invalid_json", "dataset line is not a valid query object")
	}

	queryDecoder := json.NewDecoder(strings.NewReader(string(encodedQuery)))
	queryDecoder.DisallowUnknownFields()
	var query Query
	if decodeError := queryDecoder.Decode(&query); decodeError != nil {
		return Query{}, datasetFailure(lineNumber, "", "invalid_json", "dataset line is not a valid query object")
	}
	var requiredFields struct {
		Qrels []struct {
			Grade *json.RawMessage `json:"grade"`
		} `json:"qrels"`
	}
	if decodeError := json.Unmarshal(encodedQuery, &requiredFields); decodeError != nil {
		return Query{}, datasetFailure(lineNumber, "", "invalid_json", "dataset line is not a valid query object")
	}
	var trailingJSON any
	if trailingError := queryDecoder.Decode(&trailingJSON); !errors.Is(trailingError, io.EOF) {
		return Query{}, datasetFailure(lineNumber, query.QueryID, "trailing_json", "dataset line contains more than one JSON value")
	}

	query.QueryID = strings.TrimSpace(query.QueryID)
	if !stableIdentifierPattern.MatchString(query.QueryID) {
		return Query{}, datasetFailure(lineNumber, "", "invalid_query_id", "query_id is not a stable identifier")
	}
	if _, duplicate := seenQueryIDs[query.QueryID]; duplicate {
		return Query{}, datasetFailure(lineNumber, query.QueryID, "duplicate_query_id", "query_id is duplicated")
	}

	searchRequest := models.SearchRequest{
		Q:       query.Text,
		Types:   query.Types,
		Filters: query.Filters,
		Page:    models.DefaultSearchPage,
		PerPage: models.DefaultSearchPerPage,
	}
	searchRequest.Normalize()
	if searchRequest.Q == "" {
		return Query{}, datasetFailure(lineNumber, query.QueryID, "empty_query", "query text is empty")
	}
	if validationError := searchRequest.Validate(); validationError != nil {
		return Query{}, datasetFailure(lineNumber, query.QueryID, "invalid_search_request", "query parameters violate the public search contract")
	}
	query.Text = searchRequest.Q
	query.Types = searchRequest.Types
	query.Filters = searchRequest.Filters

	if len(query.Qrels) == 0 {
		return Query{}, datasetFailure(lineNumber, query.QueryID, "missing_qrels", "qrels must contain entity judgments")
	}
	seenEntityIDs := make(map[string]struct{}, len(query.Qrels))
	seenDocuments := make(map[DocumentKey]string)
	positiveEntities := 0
	for entityIndex := range query.Qrels {
		entityJudgment := &query.Qrels[entityIndex]
		if requiredFields.Qrels[entityIndex].Grade == nil {
			return Query{}, datasetFailure(lineNumber, query.QueryID, "missing_relevance_grade", "qrel entity has no grade")
		}
		entityJudgment.EntityID = strings.TrimSpace(entityJudgment.EntityID)
		if !stableIdentifierPattern.MatchString(entityJudgment.EntityID) {
			return Query{}, datasetFailure(lineNumber, query.QueryID, "invalid_entity_id", "qrels contain an invalid entity_id")
		}
		if _, duplicate := seenEntityIDs[entityJudgment.EntityID]; duplicate {
			return Query{}, datasetFailure(lineNumber, query.QueryID, "duplicate_entity", "qrels contain a duplicate entity_id")
		}
		seenEntityIDs[entityJudgment.EntityID] = struct{}{}
		if entityJudgment.Grade < 0 || entityJudgment.Grade > maximumRelevanceGrade {
			return Query{}, datasetFailure(lineNumber, query.QueryID, "invalid_relevance_grade", "qrel grade is outside the supported scale")
		}
		if entityJudgment.Grade > 0 {
			positiveEntities++
		}
		if len(entityJudgment.Documents) == 0 {
			return Query{}, datasetFailure(lineNumber, query.QueryID, "missing_documents", "qrel entity has no document aliases")
		}
		for documentIndex := range entityJudgment.Documents {
			document := &entityJudgment.Documents[documentIndex]
			if _, validSource := validItemSources[document.Source]; !validSource {
				return Query{}, datasetFailure(lineNumber, query.QueryID, "invalid_source", "qrel document has an invalid source")
			}
			if !isCanonicalSourceID(document.SourceID) {
				return Query{}, datasetFailure(lineNumber, query.QueryID, "invalid_source_id", "qrel document has a non-canonical source_id")
			}
			if ownerEntityID, duplicate := seenDocuments[*document]; duplicate {
				failureCode := "duplicate_document"
				failureMessage := "qrel document is repeated within an entity"
				if ownerEntityID != entityJudgment.EntityID {
					failureCode = "crossed_document"
					failureMessage = "qrel document belongs to more than one entity"
				}
				return Query{}, datasetFailure(lineNumber, query.QueryID, failureCode, failureMessage)
			}
			seenDocuments[*document] = entityJudgment.EntityID
		}
		slices.SortFunc(entityJudgment.Documents, compareDocumentKeys)
	}
	if positiveEntities == 0 {
		return Query{}, datasetFailure(lineNumber, query.QueryID, "no_positive_qrels", "query has no positively graded entity")
	}
	slices.SortFunc(query.Qrels, compareEntityJudgments)

	canonicalSlices := make(map[string]struct{}, len(query.Slices))
	for _, sliceName := range query.Slices {
		sliceName = strings.TrimSpace(sliceName)
		if !stableIdentifierPattern.MatchString(sliceName) {
			return Query{}, datasetFailure(lineNumber, query.QueryID, "invalid_slice", "slice is not a stable identifier")
		}
		canonicalSlices[sliceName] = struct{}{}
	}
	query.Slices = query.Slices[:0]
	for sliceName := range canonicalSlices {
		query.Slices = append(query.Slices, sliceName)
	}
	slices.Sort(query.Slices)

	return query, nil
}

func compareDocumentKeys(firstDocument, secondDocument DocumentKey) int {
	if firstDocument.Source != secondDocument.Source {
		return strings.Compare(string(firstDocument.Source), string(secondDocument.Source))
	}
	return strings.Compare(firstDocument.SourceID, secondDocument.SourceID)
}

func compareEntityJudgments(firstJudgment, secondJudgment EntityJudgment) int {
	return strings.Compare(firstJudgment.EntityID, secondJudgment.EntityID)
}

func validateJSONStructure(encodedJSON []byte) error {
	structureDecoder := json.NewDecoder(strings.NewReader(string(encodedJSON)))
	if validationError := validateJSONValue(structureDecoder); validationError != nil {
		return validationError
	}
	var trailingJSON any
	if trailingError := structureDecoder.Decode(&trailingJSON); !errors.Is(trailingError, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func validateJSONValue(structureDecoder *json.Decoder) error {
	valueToken, tokenError := structureDecoder.Token()
	if tokenError != nil {
		return tokenError
	}
	delimiter, isDelimiter := valueToken.(json.Delim)
	if !isDelimiter {
		return nil
	}

	switch delimiter {
	case '{':
		seenKeys := make(map[string]struct{})
		for structureDecoder.More() {
			keyToken, keyError := structureDecoder.Token()
			if keyError != nil {
				return keyError
			}
			key, isString := keyToken.(string)
			if !isString {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := seenKeys[key]; duplicate {
				return errDuplicateJSONKey
			}
			seenKeys[key] = struct{}{}
			if valueError := validateJSONValue(structureDecoder); valueError != nil {
				return valueError
			}
		}
		closingToken, closingError := structureDecoder.Token()
		if closingError != nil || closingToken != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for structureDecoder.More() {
			if valueError := validateJSONValue(structureDecoder); valueError != nil {
				return valueError
			}
		}
		closingToken, closingError := structureDecoder.Token()
		if closingError != nil || closingToken != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func datasetFailure(lineNumber int, queryID, code, message string) *Failure {
	return &Failure{
		QueryID: queryID,
		Line:    lineNumber,
		Stage:   FailureStageDataset,
		Code:    code,
		Message: message,
	}
}
