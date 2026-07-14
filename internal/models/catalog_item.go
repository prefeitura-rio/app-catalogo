package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
)

type ItemSource string
type ItemType string
type ItemStatus string

const (
	// SalesForceObjectTypeSourceDataKey scopes catalog rows to the Salesforce
	// object whose complete snapshots are allowed to reconcile them.
	SalesForceObjectTypeSourceDataKey = "_catalog_object_type"

	SourceSalesForce ItemSource = "salesforce"
	SourceCourses    ItemSource = "courses"
	SourceJobs       ItemSource = "jobs"
	SourceMEI        ItemSource = "mei"
	SourceAppGoAPI   ItemSource = "app-go-api" // fonte composta: courses + jobs + mei
	SourceTypesense  ItemSource = "typesense"  // temporário: Carta de Serviços até migração para SalesForce

	TypeService        ItemType = "service"
	TypeCourse         ItemType = "course"
	TypeJob            ItemType = "job"
	TypeMEIOpportunity ItemType = "mei_opportunity"

	StatusActive   ItemStatus = "active"
	StatusInactive ItemStatus = "inactive"
	StatusDraft    ItemStatus = "draft"
)

// EmbeddingMetadata identifies the complete contract used to generate and
// compare catalog embeddings. Persisting this data prevents vectors produced
// by incompatible models, dimensions, tasks, or document formats from being
// treated as interchangeable.
type EmbeddingMetadata struct {
	Model            string `json:"model"`
	Version          string `json:"version"`
	Dimensions       int    `json:"dimensions"`
	DocumentTaskType string `json:"document_task_type"`
	QueryTaskType    string `json:"query_task_type"`
	DocumentVersion  string `json:"document_version"`
}

type CatalogItem struct {
	ID              uuid.UUID       `json:"id" format:"uuid"`
	ExternalID      string          `json:"external_id"`
	Source          ItemSource      `json:"source"`
	Type            ItemType        `json:"type"`
	Title           string          `json:"title"`
	Description     string          `json:"description,omitempty"`
	ShortDesc       string          `json:"short_desc,omitempty"`
	Organization    string          `json:"organization,omitempty"`
	URL             string          `json:"url,omitempty" format:"uri"`
	ImageURL        string          `json:"image_url,omitempty" format:"uri"`
	TargetAudience  json.RawMessage `json:"target_audience,omitempty" swaggertype:"object"`
	Bairros         []string        `json:"bairros,omitempty"`
	Modalidade      string          `json:"modalidade,omitempty"`
	Status          ItemStatus      `json:"status"`
	Tags            []string        `json:"tags,omitempty"`
	SourceData      json.RawMessage `json:"source_data,omitempty" swaggertype:"object"`
	ValidFrom       *time.Time      `json:"valid_from,omitempty" format:"date-time"`
	ValidUntil      *time.Time      `json:"valid_until,omitempty" format:"date-time"`
	SourceUpdatedAt *time.Time      `json:"source_updated_at,omitempty" format:"date-time"`
	CreatedAt       time.Time       `json:"created_at" format:"date-time"`
	UpdatedAt       time.Time       `json:"updated_at" format:"date-time"`
}

// TargetAudienceData contém os critérios de elegibilidade de um item.
type TargetAudienceData struct {
	Escolaridade []string `json:"escolaridade,omitempty"`
	Renda        string   `json:"renda,omitempty"`
	Deficiencia  []string `json:"deficiencia,omitempty"`
	Etnia        []string `json:"etnia,omitempty"`
	FaixaEtaria  []string `json:"faixa_etaria,omitempty"`
	Genero       []string `json:"genero,omitempty"`
	PCD          *bool    `json:"pcd,omitempty"`
}

func (i *CatalogItem) ParseTargetAudience() (*TargetAudienceData, error) {
	if i == nil {
		return nil, errors.New("parse target audience: catalog item is nil")
	}
	if len(i.TargetAudience) == 0 {
		return &TargetAudienceData{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(i.TargetAudience))
	decoder.DisallowUnknownFields()
	var ta *TargetAudienceData
	if decodeError := decoder.Decode(&ta); decodeError != nil {
		return nil, fmt.Errorf("parse target audience: %w", decodeError)
	}
	if ta == nil {
		return nil, errors.New("parse target audience: expected one JSON object")
	}
	var trailingJSON any
	if trailingError := decoder.Decode(&trailingJSON); !errors.Is(trailingError, io.EOF) {
		return nil, errors.New("parse target audience: expected one JSON object")
	}
	return ta, nil
}
