package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaximumPublicSearchResponseBytes = 512 << 10

	MaximumCatalogExternalIDRunes       = 255
	MaximumCatalogTitleRunes            = 500
	MaximumCatalogDescriptionRunes      = 16_000
	MaximumCatalogTextRunes             = 2_000
	MaximumCatalogOrganizationRunes     = 500
	MaximumCatalogURLRunes              = 2_048
	MaximumCatalogModalityRunes         = 100
	MaximumCatalogArrayItems            = 50
	MaximumCatalogArrayEntryRunes       = 500
	MaximumCatalogPublicScalarRunes     = 500
	MaximumCatalogSourceDataBytes       = 64 << 10
	MaximumCatalogTargetAudienceBytes   = 16 << 10
	MaximumCatalogSearchProjectionBytes = 8 << 10
)

var validCatalogItemSources = map[ItemSource]struct{}{
	SourceSalesForce: {},
	SourceCourses:    {},
	SourceJobs:       {},
	SourceMEI:        {},
	SourceAppGoAPI:   {},
	SourceTypesense:  {},
}

var validCatalogItemTypes = map[ItemType]struct{}{
	TypeService:        {},
	TypeCourse:         {},
	TypeJob:            {},
	TypeMEIOpportunity: {},
}

var validCatalogItemStatuses = map[ItemStatus]struct{}{
	StatusActive:   {},
	StatusInactive: {},
	StatusDraft:    {},
}

var publicCatalogSourceDataFields = []string{
	"canonical_id",
	"id",
	"slug",
	"tema_especifico",
	"tema_geral",
}

type catalogSearchProjection struct {
	ExternalID     string            `json:"source_id"`
	Title          string            `json:"title"`
	ShortDesc      string            `json:"short_desc,omitempty"`
	Organization   string            `json:"organization,omitempty"`
	URL            string            `json:"url,omitempty"`
	ImageURL       string            `json:"image_url,omitempty"`
	Modalidade     string            `json:"modalidade,omitempty"`
	Bairros        []string          `json:"bairros,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
	SourceMetadata map[string]string `json:"metadata,omitempty"`
}

// ValidateCatalogItem enforces the resource and public-transport invariants
// shared by every ingestion source before the item can reach PostgreSQL.
func ValidateCatalogItem(catalogItem *CatalogItem) error {
	if catalogItem == nil {
		return errors.New("catalog item cannot be nil")
	}
	if _, validSource := validCatalogItemSources[catalogItem.Source]; !validSource {
		return errors.New("catalog item source is invalid")
	}
	if _, validType := validCatalogItemTypes[catalogItem.Type]; !validType {
		return errors.New("catalog item type is invalid")
	}
	if _, validStatus := validCatalogItemStatuses[catalogItem.Status]; !validStatus {
		return errors.New("catalog item status is invalid")
	}
	if validationError := validateRequiredCatalogText(
		"external_id",
		catalogItem.ExternalID,
		MaximumCatalogExternalIDRunes,
	); validationError != nil {
		return validationError
	}
	if validationError := validateRequiredCatalogText(
		"title",
		catalogItem.Title,
		MaximumCatalogTitleRunes,
	); validationError != nil {
		return validationError
	}
	for _, textField := range []struct {
		name         string
		text         string
		maximumRunes int
	}{
		{name: "description", text: catalogItem.Description, maximumRunes: MaximumCatalogDescriptionRunes},
		{name: "short_desc", text: catalogItem.ShortDesc, maximumRunes: MaximumCatalogTextRunes},
		{name: "organization", text: catalogItem.Organization, maximumRunes: MaximumCatalogOrganizationRunes},
		{name: "modalidade", text: catalogItem.Modalidade, maximumRunes: MaximumCatalogModalityRunes},
	} {
		if validationError := validateOptionalCatalogText(
			textField.name,
			textField.text,
			textField.maximumRunes,
		); validationError != nil {
			return validationError
		}
	}
	for _, urlField := range []struct {
		name   string
		rawURL string
	}{
		{name: "url", rawURL: catalogItem.URL},
		{name: "image_url", rawURL: catalogItem.ImageURL},
	} {
		if validationError := validateOptionalCatalogHTTPURL(urlField.name, urlField.rawURL); validationError != nil {
			return validationError
		}
	}
	if validationError := validateCatalogStringArray("bairros", catalogItem.Bairros); validationError != nil {
		return validationError
	}
	if validationError := validateCatalogStringArray("tags", catalogItem.Tags); validationError != nil {
		return validationError
	}
	if validationError := validateCatalogTargetAudience(catalogItem); validationError != nil {
		return validationError
	}
	if catalogItem.ValidFrom != nil && catalogItem.ValidUntil != nil &&
		!catalogItem.ValidFrom.Before(*catalogItem.ValidUntil) {
		return errors.New("catalog item valid_from must be earlier than valid_until")
	}

	publicSourceMetadata, sourceDataError := validateCatalogSourceData(catalogItem.SourceData)
	if sourceDataError != nil {
		return sourceDataError
	}
	if serviceSourceError := ValidatePublicServiceSourceData(catalogItem); serviceSourceError != nil {
		return serviceSourceError
	}
	encodedProjection, encodeError := json.Marshal(catalogSearchProjection{
		ExternalID:     catalogItem.ExternalID,
		Title:          catalogItem.Title,
		ShortDesc:      catalogItem.ShortDesc,
		Organization:   catalogItem.Organization,
		URL:            catalogItem.URL,
		ImageURL:       catalogItem.ImageURL,
		Modalidade:     catalogItem.Modalidade,
		Bairros:        catalogItem.Bairros,
		Tags:           catalogItem.Tags,
		SourceMetadata: publicSourceMetadata,
	})
	if encodeError != nil {
		return errors.New("catalog item public projection is not serializable")
	}
	if len(encodedProjection) > MaximumCatalogSearchProjectionBytes {
		return errors.New("catalog item public projection exceeds its byte limit")
	}
	return nil
}

func validateRequiredCatalogText(fieldName string, fieldText string, maximumRunes int) error {
	if strings.TrimSpace(fieldText) == "" {
		return fmt.Errorf("catalog item %s cannot be empty", fieldName)
	}
	return validateOptionalCatalogText(fieldName, fieldText, maximumRunes)
}

func validateOptionalCatalogText(fieldName string, fieldText string, maximumRunes int) error {
	if !utf8.ValidString(fieldText) {
		return fmt.Errorf("catalog item %s must be valid UTF-8", fieldName)
	}
	if utf8.RuneCountInString(fieldText) > maximumRunes {
		return fmt.Errorf("catalog item %s exceeds its rune limit", fieldName)
	}
	if strings.IndexFunc(fieldText, isUnsafeCatalogControl) >= 0 {
		return fmt.Errorf("catalog item %s contains control characters", fieldName)
	}
	return nil
}

func validateOptionalCatalogHTTPURL(fieldName string, rawURL string) error {
	if rawURL == "" {
		return nil
	}
	if validationError := validateOptionalCatalogText(fieldName, rawURL, MaximumCatalogURLRunes); validationError != nil {
		return validationError
	}
	if strings.TrimSpace(rawURL) != rawURL {
		return fmt.Errorf("catalog item %s must not contain surrounding whitespace", fieldName)
	}
	parsedURL, parseError := url.Parse(rawURL)
	if parseError != nil || parsedURL.Host == "" ||
		(parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.User != nil {
		return fmt.Errorf("catalog item %s must be an absolute credential-free HTTP(S) URL", fieldName)
	}
	return nil
}

func isUnsafeCatalogControl(character rune) bool {
	return unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t'
}

func validateCatalogStringArray(fieldName string, fieldValues []string) error {
	if len(fieldValues) > MaximumCatalogArrayItems {
		return fmt.Errorf("catalog item %s exceeds its item limit", fieldName)
	}
	for valueIndex, fieldValue := range fieldValues {
		if validationError := validateRequiredCatalogText(
			fmt.Sprintf("%s[%d]", fieldName, valueIndex),
			fieldValue,
			MaximumCatalogArrayEntryRunes,
		); validationError != nil {
			return validationError
		}
	}
	return nil
}

func validateCatalogJSONObject(fieldName string, encodedObject json.RawMessage, maximumBytes int) error {
	if len(encodedObject) == 0 {
		return nil
	}
	if len(encodedObject) > maximumBytes {
		return fmt.Errorf("catalog item %s exceeds its byte limit", fieldName)
	}
	decoder := json.NewDecoder(bytes.NewReader(encodedObject))
	var objectFields map[string]json.RawMessage
	if decodeError := decoder.Decode(&objectFields); decodeError != nil || objectFields == nil {
		return fmt.Errorf("catalog item %s must be a JSON object", fieldName)
	}
	var trailingJSON any
	if trailingError := decoder.Decode(&trailingJSON); !errors.Is(trailingError, io.EOF) {
		return fmt.Errorf("catalog item %s must contain one JSON object", fieldName)
	}
	return nil
}

func validateCatalogSourceData(encodedSourceData json.RawMessage) (map[string]string, error) {
	if validationError := validateCatalogJSONObject(
		"source_data",
		encodedSourceData,
		MaximumCatalogSourceDataBytes,
	); validationError != nil {
		return nil, validationError
	}
	if len(encodedSourceData) == 0 {
		return nil, nil
	}

	var sourceFields map[string]json.RawMessage
	if unmarshalError := json.Unmarshal(encodedSourceData, &sourceFields); unmarshalError != nil {
		return nil, errors.New("catalog item source_data must be a JSON object")
	}
	publicMetadata := make(map[string]string)
	for _, publicField := range publicCatalogSourceDataFields {
		encodedField, fieldExists := sourceFields[publicField]
		if !fieldExists || bytes.Equal(bytes.TrimSpace(encodedField), []byte("null")) {
			continue
		}
		var fieldText string
		if decodeError := json.Unmarshal(encodedField, &fieldText); decodeError != nil {
			return nil, fmt.Errorf("catalog item source_data.%s must be a string", publicField)
		}
		if validationError := validateOptionalCatalogText(
			"source_data."+publicField,
			fieldText,
			MaximumCatalogPublicScalarRunes,
		); validationError != nil {
			return nil, validationError
		}
		publicMetadata[publicField] = fieldText
	}
	if encodedObjectType, fieldExists := sourceFields[SalesForceObjectTypeSourceDataKey]; fieldExists {
		var objectType string
		if decodeError := json.Unmarshal(encodedObjectType, &objectType); decodeError != nil {
			return nil, fmt.Errorf("catalog item source_data.%s must be a string", SalesForceObjectTypeSourceDataKey)
		}
		if validationError := validateRequiredCatalogText(
			"source_data."+SalesForceObjectTypeSourceDataKey,
			objectType,
			MaximumCatalogModalityRunes,
		); validationError != nil {
			return nil, validationError
		}
	}
	if len(publicMetadata) == 0 {
		return nil, nil
	}
	return publicMetadata, nil
}

func validateCatalogTargetAudience(catalogItem *CatalogItem) error {
	if validationError := validateCatalogJSONObject(
		"target_audience",
		catalogItem.TargetAudience,
		MaximumCatalogTargetAudienceBytes,
	); validationError != nil {
		return validationError
	}
	targetAudience, parseError := catalogItem.ParseTargetAudience()
	if parseError != nil {
		return fmt.Errorf("catalog item target_audience is invalid: %w", parseError)
	}
	if validationError := validateOptionalCatalogText(
		"target_audience.renda",
		targetAudience.Renda,
		MaximumCatalogPublicScalarRunes,
	); validationError != nil {
		return validationError
	}
	for _, audienceField := range []struct {
		name   string
		values []string
	}{
		{name: "target_audience.escolaridade", values: targetAudience.Escolaridade},
		{name: "target_audience.deficiencia", values: targetAudience.Deficiencia},
		{name: "target_audience.etnia", values: targetAudience.Etnia},
		{name: "target_audience.faixa_etaria", values: targetAudience.FaixaEtaria},
		{name: "target_audience.genero", values: targetAudience.Genero},
	} {
		if validationError := validateCatalogStringArray(audienceField.name, audienceField.values); validationError != nil {
			return validationError
		}
	}
	return nil
}
