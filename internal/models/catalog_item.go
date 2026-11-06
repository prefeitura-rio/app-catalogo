package models

import (
	"encoding/json"
	"errors"
	"strings"
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
	SourceTypesense  ItemSource = "typesense"  // legado: Carta via Typesense, fonte desligada; só restam linhas antigas no banco

	TypeService        ItemType = "service"
	TypeCourse         ItemType = "course"
	TypeJob            ItemType = "job"
	TypeMEIOpportunity ItemType = "mei_opportunity"

	StatusActive   ItemStatus = "active"
	StatusInactive ItemStatus = "inactive"
	StatusDraft    ItemStatus = "draft"
)

// ItemTypes são os valores do enum item_type do banco, na ordem do contrato.
var ItemTypes = []ItemType{TypeService, TypeCourse, TypeJob, TypeMEIOpportunity}

// ErrInvalidItemType indica um valor de `types` fora de ItemTypes.
var ErrInvalidItemType = errors.New("types inválido: valores aceitos são " + joinItemTypes(ItemTypes))

func joinItemTypes(types []ItemType) string {
	strs := make([]string, len(types))
	for i, t := range types {
		strs[i] = string(t)
	}
	return strings.Join(strs, ", ")
}

// Valid informa se t é um dos valores do enum item_type (comparação exata).
func (t ItemType) Valid() bool {
	for _, known := range ItemTypes {
		if t == known {
			return true
		}
	}
	return false
}

// ParseItemTypes lê os valores do parâmetro `types` (repetido e/ou separado por
// vírgula), ignorando entradas vazias. Valor fora do enum devolve
// ErrInvalidItemType, para que a entrada do cliente nunca chegue ao cast
// `::item_type[]` do Postgres.
func ParseItemTypes(raws []string) ([]ItemType, error) {
	var types []ItemType
	for _, raw := range raws {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			t := ItemType(s)
			if !t.Valid() {
				return nil, ErrInvalidItemType
			}
			types = append(types, t)
		}
	}
	return types, nil
}

type CatalogItem struct {
	ID              uuid.UUID       `json:"id"`
	ExternalID      string          `json:"external_id"`
	Source          ItemSource      `json:"source"`
	Type            ItemType        `json:"type"`
	Title           string          `json:"title"`
	Description     string          `json:"description,omitempty"`
	ShortDesc       string          `json:"short_desc,omitempty"`
	Organization    string          `json:"organization,omitempty"`
	URL             string          `json:"url,omitempty"`
	ImageURL        string          `json:"image_url,omitempty"`
	TargetAudience  json.RawMessage `json:"target_audience,omitempty" swaggertype:"object"`
	Bairros         []string        `json:"bairros,omitempty"`
	Modalidade      string          `json:"modalidade,omitempty"`
	Status          ItemStatus      `json:"status"`
	Tags            []string        `json:"tags,omitempty"`
	SourceData      json.RawMessage `json:"source_data,omitempty" swaggertype:"object"`
	ThemeSlug       string          `json:"theme_slug,omitempty"`
	SubthemeSlug    string          `json:"subtheme_slug,omitempty"`
	SiciCode        string          `json:"sici_code,omitempty"`
	ValidFrom       *time.Time      `json:"valid_from,omitempty"`
	ValidUntil      *time.Time      `json:"valid_until,omitempty"`
	SourceUpdatedAt *time.Time      `json:"source_updated_at,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// TargetAudienceData contém os critérios de elegibilidade de um item.
type TargetAudienceData struct {
	Escolaridade []string `json:"escolaridade,omitempty"`
	Renda        string   `json:"renda,omitempty"`
	Deficiencia  []string `json:"deficiencia,omitempty"`
	Etnia        []string `json:"etnia,omitempty"`
	FaixaEtaria  []string `json:"faixa_etaria,omitempty"`
	Genero       []string `json:"genero,omitempty"`
	// Outros preserva públicos não classificados (ex.: "Publico_em_geral", "MEI").
	Outros []string `json:"outros,omitempty"`
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
