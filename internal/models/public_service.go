package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const MaximumPublicServiceSlugRunes = 200

var publicServiceSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type PublicServiceAction struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url" format:"uri"`
	Order       int    `json:"order"`
}

type PublicServiceDetail struct {
	ID                    uuid.UUID             `json:"id" format:"uuid"`
	SourceID              string                `json:"source_id"`
	Slug                  string                `json:"slug"`
	HistoricalSlugs       []string              `json:"historical_slugs"`
	Title                 string                `json:"title"`
	Summary               string                `json:"summary,omitempty"`
	Description           string                `json:"description,omitempty"`
	Author                string                `json:"author,omitempty"`
	Cost                  string                `json:"cost,omitempty"`
	Category              string                `json:"category,omitempty"`
	Subcategory           string                `json:"subcategory,omitempty"`
	ManagingOrganizations []string              `json:"managing_organizations"`
	TargetAudiences       []string              `json:"target_audiences"`
	ServiceTime           string                `json:"service_time,omitempty"`
	RequestInstructions   string                `json:"request_instructions,omitempty"`
	RequestOutcome        string                `json:"request_outcome,omitempty"`
	Exclusions            string                `json:"exclusions,omitempty"`
	DigitalChannels       []string              `json:"digital_channels"`
	InPersonChannels      []string              `json:"in_person_channels"`
	RequiredDocuments     []string              `json:"required_documents"`
	RelatedLegislation    []string              `json:"related_legislation"`
	Actions               []PublicServiceAction `json:"actions"`
	IsFree                bool                  `json:"is_free"`
	Featured              bool                  `json:"featured"`
	PublishedAt           *time.Time            `json:"published_at,omitempty" format:"date-time"`
	SourceUpdatedAt       *time.Time            `json:"source_updated_at,omitempty" format:"date-time"`
}

type PublicServiceSummary struct {
	ID           uuid.UUID `json:"id" format:"uuid"`
	Slug         string    `json:"slug"`
	Title        string    `json:"title"`
	Summary      string    `json:"summary,omitempty"`
	Organization string    `json:"organization,omitempty"`
	Category     string    `json:"category,omitempty"`
	Subcategory  string    `json:"subcategory,omitempty"`
	Modality     string    `json:"modality,omitempty"`
	URL          string    `json:"url,omitempty" format:"uri"`
}

type PublicServiceCategory struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type PublicServiceSubcategory struct {
	Category string `json:"category"`
	Name     string `json:"name"`
	Count    int    `json:"count"`
}

type PublicServiceCategoryResponse struct {
	CatalogRevision string                  `json:"catalog_revision"`
	Categories      []PublicServiceCategory `json:"categories"`
}

type PublicServiceSubcategoryResponse struct {
	CatalogRevision string                     `json:"catalog_revision"`
	Category        string                     `json:"category"`
	Subcategories   []PublicServiceSubcategory `json:"subcategories"`
}

type PublicServiceListResponse struct {
	CatalogRevision string                 `json:"catalog_revision"`
	Items           []PublicServiceSummary `json:"items"`
	Page            int                    `json:"page"`
	PerPage         int                    `json:"per_page"`
	Total           int                    `json:"total"`
}

type publicServiceSourceData struct {
	Slug                  string                `json:"slug"`
	SlugHistory           []string              `json:"slug_history"`
	Author                string                `json:"autor"`
	Cost                  string                `json:"custo_servico"`
	Category              string                `json:"tema_geral"`
	Subcategory           string                `json:"sub_categoria"`
	ManagingOrganizations []string              `json:"orgao_gestor"`
	TargetAudiences       []string              `json:"publico_especifico"`
	ServiceTime           string                `json:"tempo_atendimento"`
	RequestInstructions   string                `json:"instrucoes_solicitante"`
	RequestOutcome        string                `json:"resultado_solicitacao"`
	Exclusions            string                `json:"servico_nao_cobre"`
	DigitalChannels       []string              `json:"canais_digitais"`
	InPersonChannels      []string              `json:"canais_presenciais"`
	RequiredDocuments     []string              `json:"documentos_necessarios"`
	RelatedLegislation    []string              `json:"legislacao_relacionada"`
	Buttons               []publicServiceButton `json:"buttons"`
	IsFree                *bool                 `json:"is_free"`
	Featured              bool                  `json:"fixar_destaque"`
	PublishedAtUnix       *int64                `json:"published_at"`
}

type publicServiceButton struct {
	Title       string `json:"titulo"`
	Description string `json:"descricao"`
	URL         string `json:"url_service"`
	Enabled     bool   `json:"is_enabled"`
	Order       int    `json:"ordem"`
}

func ValidatePublicServiceSourceData(catalogItem *CatalogItem) error {
	if catalogItem == nil || catalogItem.Type != TypeService || len(catalogItem.SourceData) == 0 {
		return nil
	}
	serviceSource, decodeError := decodePublicServiceSourceData(catalogItem.SourceData)
	if decodeError != nil {
		return decodeError
	}
	if serviceSource.Slug == "" {
		if len(serviceSource.SlugHistory) > 0 {
			return errors.New("catalog item service slug_history requires a canonical slug")
		}
		return nil
	}
	if !ValidPublicServiceSlug(serviceSource.Slug) {
		return errors.New("catalog item service slug is invalid")
	}
	seenSlugs := map[string]struct{}{serviceSource.Slug: {}}
	for _, historicalSlug := range serviceSource.SlugHistory {
		if !ValidPublicServiceSlug(historicalSlug) {
			return errors.New("catalog item service slug_history contains an invalid slug")
		}
		if _, duplicateSlug := seenSlugs[historicalSlug]; duplicateSlug {
			return errors.New("catalog item service slugs must be unique")
		}
		seenSlugs[historicalSlug] = struct{}{}
	}
	for _, serviceText := range []struct {
		name string
		text string
	}{
		{name: "source_data.autor", text: serviceSource.Author},
		{name: "source_data.custo_servico", text: serviceSource.Cost},
		{name: "source_data.tema_geral", text: serviceSource.Category},
		{name: "source_data.sub_categoria", text: serviceSource.Subcategory},
		{name: "source_data.tempo_atendimento", text: serviceSource.ServiceTime},
		{name: "source_data.instrucoes_solicitante", text: serviceSource.RequestInstructions},
		{name: "source_data.resultado_solicitacao", text: serviceSource.RequestOutcome},
		{name: "source_data.servico_nao_cobre", text: serviceSource.Exclusions},
	} {
		if validationError := validateOptionalCatalogText(serviceText.name, serviceText.text, MaximumCatalogDescriptionRunes); validationError != nil {
			return validationError
		}
	}
	for _, serviceCollection := range []struct {
		name   string
		values []string
	}{
		{name: "source_data.orgao_gestor", values: serviceSource.ManagingOrganizations},
		{name: "source_data.publico_especifico", values: serviceSource.TargetAudiences},
		{name: "source_data.canais_digitais", values: serviceSource.DigitalChannels},
		{name: "source_data.canais_presenciais", values: serviceSource.InPersonChannels},
		{name: "source_data.documentos_necessarios", values: serviceSource.RequiredDocuments},
		{name: "source_data.legislacao_relacionada", values: serviceSource.RelatedLegislation},
	} {
		if validationError := validateCatalogStringArray(serviceCollection.name, serviceCollection.values); validationError != nil {
			return validationError
		}
	}
	for _, button := range serviceSource.Buttons {
		if !button.Enabled {
			continue
		}
		if validationError := validateRequiredCatalogText("source_data.buttons.title", button.Title, MaximumCatalogTextRunes); validationError != nil {
			return validationError
		}
		if validationError := validateOptionalCatalogText("source_data.buttons.description", button.Description, MaximumCatalogTextRunes); validationError != nil {
			return validationError
		}
		if !safePublicServiceActionURL(button.URL) {
			return errors.New("catalog item service action URL is unsafe")
		}
	}
	return nil
}

func ValidPublicServiceSlug(slug string) bool {
	return len(slug) <= MaximumPublicServiceSlugRunes && publicServiceSlugPattern.MatchString(slug)
}

func PublicServiceSlugs(catalogItem *CatalogItem) (canonicalSlug string, historicalSlugs []string, serviceError error) {
	if catalogItem == nil || catalogItem.Type != TypeService || len(catalogItem.SourceData) == 0 {
		return "", nil, nil
	}
	serviceSource, decodeError := decodePublicServiceSourceData(catalogItem.SourceData)
	if decodeError != nil {
		return "", nil, decodeError
	}
	return serviceSource.Slug, append([]string(nil), serviceSource.SlugHistory...), nil
}

func NewPublicServiceDetail(catalogItem *CatalogItem) (*PublicServiceDetail, error) {
	if catalogItem == nil {
		return nil, errors.New("public service detail requires a catalog item")
	}
	serviceSource, decodeError := decodePublicServiceSourceData(catalogItem.SourceData)
	if decodeError != nil {
		return nil, decodeError
	}
	actions := make([]PublicServiceAction, 0, len(serviceSource.Buttons))
	for _, button := range serviceSource.Buttons {
		if !button.Enabled || !safePublicServiceActionURL(button.URL) {
			continue
		}
		actions = append(actions, PublicServiceAction{
			Title: button.Title, Description: button.Description, URL: button.URL, Order: button.Order,
		})
	}
	return &PublicServiceDetail{
		ID:                    catalogItem.ID,
		SourceID:              catalogItem.ExternalID,
		Slug:                  serviceSource.Slug,
		HistoricalSlugs:       nonNilStrings(serviceSource.SlugHistory),
		Title:                 catalogItem.Title,
		Summary:               catalogItem.ShortDesc,
		Description:           catalogItem.Description,
		Author:                serviceSource.Author,
		Cost:                  serviceSource.Cost,
		Category:              serviceSource.Category,
		Subcategory:           serviceSource.Subcategory,
		ManagingOrganizations: nonNilStrings(serviceSource.ManagingOrganizations),
		TargetAudiences:       nonNilStrings(serviceSource.TargetAudiences),
		ServiceTime:           serviceSource.ServiceTime,
		RequestInstructions:   serviceSource.RequestInstructions,
		RequestOutcome:        serviceSource.RequestOutcome,
		Exclusions:            serviceSource.Exclusions,
		DigitalChannels:       nonNilStrings(serviceSource.DigitalChannels),
		InPersonChannels:      nonNilStrings(serviceSource.InPersonChannels),
		RequiredDocuments:     nonNilStrings(serviceSource.RequiredDocuments),
		RelatedLegislation:    nonNilStrings(serviceSource.RelatedLegislation),
		Actions:               actions,
		IsFree:                serviceSource.IsFree != nil && *serviceSource.IsFree,
		Featured:              serviceSource.Featured,
		PublishedAt:           unixTimestamp(serviceSource.PublishedAtUnix),
		SourceUpdatedAt:       catalogItem.SourceUpdatedAt,
	}, nil
}

func NewPublicServiceSummary(catalogItem *CatalogItem) (*PublicServiceSummary, error) {
	detail, detailError := NewPublicServiceDetail(catalogItem)
	if detailError != nil {
		return nil, detailError
	}
	return &PublicServiceSummary{
		ID:           detail.ID,
		Slug:         detail.Slug,
		Title:        detail.Title,
		Summary:      detail.Summary,
		Organization: catalogItem.Organization,
		Category:     detail.Category,
		Subcategory:  detail.Subcategory,
		Modality:     catalogItem.Modalidade,
		URL:          catalogItem.URL,
	}, nil
}

func decodePublicServiceSourceData(encodedSourceData json.RawMessage) (publicServiceSourceData, error) {
	if len(encodedSourceData) == 0 {
		return publicServiceSourceData{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(encodedSourceData))
	var serviceSource publicServiceSourceData
	if decodeError := decoder.Decode(&serviceSource); decodeError != nil {
		return publicServiceSourceData{}, errors.New("catalog item service source_data is invalid")
	}
	var trailingJSON any
	if trailingError := decoder.Decode(&trailingJSON); !errors.Is(trailingError, io.EOF) {
		return publicServiceSourceData{}, errors.New("catalog item service source_data must contain one JSON object")
	}
	return serviceSource, nil
}

func safePublicServiceActionURL(rawURL string) bool {
	if strings.TrimSpace(rawURL) != rawURL || rawURL == "" {
		return false
	}
	parsedURL, parseError := url.Parse(rawURL)
	if parseError != nil || parsedURL.User != nil || parsedURL.Fragment != "" {
		return false
	}
	if parsedURL.IsAbs() {
		return (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") && parsedURL.Host != ""
	}
	return strings.HasPrefix(parsedURL.Path, "/") && !strings.HasPrefix(parsedURL.Path, "//")
}

func nonNilStrings(stringsCandidate []string) []string {
	if stringsCandidate == nil {
		return []string{}
	}
	return stringsCandidate
}

func unixTimestamp(unixSeconds *int64) *time.Time {
	if unixSeconds == nil || *unixSeconds <= 0 {
		return nil
	}
	timestamp := time.Unix(*unixSeconds, 0).UTC()
	return &timestamp
}
