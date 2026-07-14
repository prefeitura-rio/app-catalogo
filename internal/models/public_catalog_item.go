package models

import (
	"time"

	"github.com/google/uuid"
)

// PublicCatalogItem is the allowlisted catalog detail exposed to anonymous
// callers. Raw source payloads and internal targeting data intentionally stay
// behind authenticated administrative boundaries.
type PublicCatalogItem struct {
	ID              uuid.UUID  `json:"id" format:"uuid"`
	SourceID        string     `json:"source_id"`
	Source          ItemSource `json:"source"`
	Type            ItemType   `json:"type"`
	Title           string     `json:"title"`
	Description     string     `json:"description,omitempty"`
	ShortDesc       string     `json:"short_desc,omitempty"`
	Organization    string     `json:"organization,omitempty"`
	URL             string     `json:"url,omitempty" format:"uri"`
	ImageURL        string     `json:"image_url,omitempty" format:"uri"`
	Bairros         []string   `json:"bairros,omitempty"`
	Modalidade      string     `json:"modalidade,omitempty"`
	Tags            []string   `json:"tags,omitempty"`
	ValidFrom       *time.Time `json:"valid_from,omitempty" format:"date-time"`
	ValidUntil      *time.Time `json:"valid_until,omitempty" format:"date-time"`
	SourceUpdatedAt *time.Time `json:"source_updated_at,omitempty" format:"date-time"`
}

func NewPublicCatalogItem(catalogItem *CatalogItem) *PublicCatalogItem {
	if catalogItem == nil {
		return nil
	}
	return &PublicCatalogItem{
		ID:              catalogItem.ID,
		SourceID:        catalogItem.ExternalID,
		Source:          catalogItem.Source,
		Type:            catalogItem.Type,
		Title:           catalogItem.Title,
		Description:     catalogItem.Description,
		ShortDesc:       catalogItem.ShortDesc,
		Organization:    catalogItem.Organization,
		URL:             catalogItem.URL,
		ImageURL:        catalogItem.ImageURL,
		Bairros:         catalogItem.Bairros,
		Modalidade:      catalogItem.Modalidade,
		Tags:            catalogItem.Tags,
		ValidFrom:       catalogItem.ValidFrom,
		ValidUntil:      catalogItem.ValidUntil,
		SourceUpdatedAt: catalogItem.SourceUpdatedAt,
	}
}
