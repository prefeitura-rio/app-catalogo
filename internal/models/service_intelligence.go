package models

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
)

const (
	MinimumPublicSuggestionQueryRunes = 2
	MaximumPublicSuggestionQueryRunes = 256
	MaximumPublicSuggestions          = 8
	MaximumPublicServiceRelations     = 8
	MaximumSearchSummaryCandidates    = 10
	MaximumSearchSummaryQueryRunes    = 256
	MaximumSearchSummarySegments      = 20
)

var ErrCatalogRevisionMismatch = errors.New("catalog revision mismatch")

type PublicSuggestionRequest struct {
	Query string `json:"query" binding:"required"`
}

func (request *PublicSuggestionRequest) Normalize() error {
	if request == nil {
		return errors.New("suggestion request is required")
	}
	request.Query = strings.Join(strings.Fields(norm.NFC.String(request.Query)), " ")
	if containsControlRune(request.Query) {
		return errors.New("suggestion query contains a control character")
	}
	queryLength := len([]rune(request.Query))
	if queryLength > MaximumPublicSuggestionQueryRunes {
		return fmt.Errorf("suggestion query exceeds %d runes", MaximumPublicSuggestionQueryRunes)
	}
	return nil
}

type PublicServiceSuggestion struct {
	Title string `json:"title"`
	Slug  string `json:"slug"`
	URL   string `json:"url" format:"uri"`
}

type PublicSuggestionResponse struct {
	Suggestions []PublicServiceSuggestion `json:"suggestions" validate:"max=8"`
}

type PublicServiceRelation struct {
	ID           uuid.UUID `json:"id" format:"uuid"`
	Slug         string    `json:"slug"`
	Title        string    `json:"title"`
	ShortDesc    string    `json:"short_desc"`
	URL          string    `json:"url,omitempty" format:"uri"`
	Organization string    `json:"organization,omitempty"`
	Reason       string    `json:"reason,omitempty"`
}

type PublicServiceJourney struct {
	ServiceSlug string                  `json:"service_slug"`
	Theme       string                  `json:"theme,omitempty"`
	NextSteps   []PublicServiceRelation `json:"next_steps" validate:"max=8"`
}

type PublicServiceCluster struct {
	Theme    string                  `json:"theme,omitempty"`
	Services []PublicServiceRelation `json:"services" validate:"max=8"`
}

type PublicServiceRelationsResponse struct {
	CatalogRevision string                  `json:"catalog_revision"`
	Recommendations []PublicServiceRelation `json:"recommendations" validate:"max=8"`
	Journey         PublicServiceJourney    `json:"journey"`
	Cluster         PublicServiceCluster    `json:"cluster"`
}

type SearchSummaryRequest struct {
	Query           string      `json:"query" binding:"required"`
	CatalogRevision string      `json:"catalog_revision" binding:"required"`
	CandidateIDs    []uuid.UUID `json:"candidate_ids" binding:"required" validate:"min=1,max=10"`
}

func (request *SearchSummaryRequest) Normalize() error {
	if request == nil {
		return errors.New("summary request is required")
	}
	request.Query = strings.Join(strings.Fields(norm.NFC.String(request.Query)), " ")
	if containsControlRune(request.Query) {
		return errors.New("summary query contains a control character")
	}
	if queryLength := len([]rune(request.Query)); queryLength < 1 || queryLength > MaximumSearchSummaryQueryRunes {
		return fmt.Errorf("summary query length is invalid")
	}
	request.CatalogRevision = strings.TrimSpace(request.CatalogRevision)
	if !strings.HasPrefix(request.CatalogRevision, "catalog-v2:") || len([]rune(request.CatalogRevision)) > 256 ||
		containsControlRune(request.CatalogRevision) {
		return errors.New("catalog revision is invalid")
	}
	if len(request.CandidateIDs) < 1 || len(request.CandidateIDs) > MaximumSearchSummaryCandidates {
		return fmt.Errorf("summary candidates must contain between 1 and %d IDs", MaximumSearchSummaryCandidates)
	}
	seenCandidateIDs := make(map[uuid.UUID]struct{}, len(request.CandidateIDs))
	for _, candidateID := range request.CandidateIDs {
		if candidateID == uuid.Nil {
			return errors.New("summary candidate ID must not be nil")
		}
		if _, duplicateCandidate := seenCandidateIDs[candidateID]; duplicateCandidate {
			return errors.New("summary candidate IDs must be unique")
		}
		seenCandidateIDs[candidateID] = struct{}{}
	}
	return nil
}

func containsControlRune(candidate string) bool {
	return strings.ContainsFunc(candidate, unicode.IsControl)
}

type SearchSummarySegment struct {
	Text string `json:"text"`
	Slug string `json:"slug,omitempty"`
	URL  string `json:"url,omitempty" format:"uri"`
}

type SearchSummaryResponse struct {
	Query     string                 `json:"query"`
	Segments  []SearchSummarySegment `json:"segments" validate:"max=20"`
	Generated bool                   `json:"generated"`
}

func PublicServiceURL(category string, slug string) string {
	categorySegment := strings.TrimSpace(removeCombiningMarks(norm.NFD.String(category)))
	categorySegment = strings.ToLower(categorySegment)
	return "/servicos/categoria/" + categorySegment + "/" + slug
}

func removeCombiningMarks(candidate string) string {
	return strings.Map(func(character rune) rune {
		if unicode.Is(unicode.Mn, character) {
			return -1
		}
		return character
	}, candidate)
}
