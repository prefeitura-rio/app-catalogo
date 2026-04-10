package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type ItemSource string
type ItemType string
type ItemStatus string

const (
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

type CatalogItem struct {
	ID           uuid.UUID       `json:"id"`
	ExternalID   string          `json:"external_id"`
	Source       ItemSource      `json:"source"`
	Type         ItemType        `json:"type"`
	Title        string          `json:"title"`
	Description  string          `json:"description,omitempty"`
	ShortDesc    string          `json:"short_desc,omitempty"`
	Organization string          `json:"organization,omitempty"`
	URL          string          `json:"url,omitempty"`
	ImageURL     string          `json:"image_url,omitempty"`
	TargetAudience json.RawMessage `json:"target_audience,omitempty" swaggertype:"object"`
	Bairros      []string        `json:"bairros,omitempty"`
	Modalidade   string          `json:"modalidade,omitempty"`
	Status       ItemStatus      `json:"status"`
	Tags         []string        `json:"tags,omitempty"`
	SourceData   json.RawMessage `json:"source_data,omitempty" swaggertype:"object"`
	ValidFrom    *time.Time      `json:"valid_from,omitempty"`
	ValidUntil   *time.Time      `json:"valid_until,omitempty"`
	SourceUpdatedAt *time.Time   `json:"source_updated_at,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// TargetAudienceData contém os critérios de elegibilidade de um item.
type TargetAudienceData struct {
	Escolaridade []string `json:"escolaridade,omitempty"`
	Renda        string   `json:"renda,omitempty"`
	Deficiencia  []string `json:"deficiencia,omitempty"`
	Etnia        []string `json:"etnia,omitempty"`
	FaixaEtaria  []string `json:"faixa_etaria,omitempty"`
	Genero       []string `json:"genero,omitempty"`
}

func (i *CatalogItem) ParseTargetAudience() (*TargetAudienceData, error) {
	if len(i.TargetAudience) == 0 {
		return &TargetAudienceData{}, nil
	}
	var ta TargetAudienceData
	if err := json.Unmarshal(i.TargetAudience, &ta); err != nil {
		return &TargetAudienceData{}, nil
	}
	return &ta, nil
}
