package models

import (
	"time"

	"github.com/google/uuid"
)

// PublicCatalogItem is the allowlisted catalog detail exposed to anonymous
// callers. Raw source payloads and internal targeting data stay behind
// authenticated boundaries.
type PublicCatalogItem struct {
	ID           uuid.UUID  `json:"id"`
	ExternalID   string     `json:"external_id"`
	Source       ItemSource `json:"source"`
	Type         ItemType   `json:"type"`
	Title        string     `json:"title"`
	Description  string     `json:"description,omitempty"`
	ShortDesc    string     `json:"short_desc,omitempty"`
	Organization string     `json:"organization,omitempty"`
	URL          string     `json:"url,omitempty"`
	ImageURL     string     `json:"image_url,omitempty"`
	Bairros      []string   `json:"bairros,omitempty"`
	Modalidade   string     `json:"modalidade,omitempty"`
	Status       ItemStatus `json:"status"`
	Tags         []string   `json:"tags,omitempty"`
	ValidFrom    *time.Time `json:"valid_from,omitempty"`
	ValidUntil   *time.Time `json:"valid_until,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// ToPublicCatalogItem maps a CatalogItem to the public-safe DTO.
func ToPublicCatalogItem(item *CatalogItem) PublicCatalogItem {
	if item == nil {
		return PublicCatalogItem{}
	}
	return PublicCatalogItem{
		ID:           item.ID,
		ExternalID:   item.ExternalID,
		Source:       item.Source,
		Type:         item.Type,
		Title:        item.Title,
		Description:  item.Description,
		ShortDesc:    item.ShortDesc,
		Organization: item.Organization,
		URL:          item.URL,
		ImageURL:     item.ImageURL,
		Bairros:      item.Bairros,
		Modalidade:   item.Modalidade,
		Status:       item.Status,
		Tags:         item.Tags,
		ValidFrom:    item.ValidFrom,
		ValidUntil:   item.ValidUntil,
		CreatedAt:    item.CreatedAt,
		UpdatedAt:    item.UpdatedAt,
	}
}
