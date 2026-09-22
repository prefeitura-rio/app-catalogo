package models

import "time"

// CartaTheme é um tema da Carta de Serviços (navegação local).
type CartaTheme struct {
	Slug              string     `json:"slug"`
	Name              string     `json:"name"`
	SubthemesCount    int        `json:"subthemesCount"`
	PublishedServices int        `json:"publishedServices"`
	CreatedAt         time.Time  `json:"-"`
	UpdatedAt         time.Time  `json:"-"`
	DeletedAt         *time.Time `json:"-"`
}

// CartaSubtheme é um subtema da Carta de Serviços (navegação local).
type CartaSubtheme struct {
	Slug              string     `json:"slug"`
	ThemeSlug         string     `json:"themeSlug,omitempty"`
	Name              string     `json:"name"`
	PublishedServices int        `json:"publishedServices"`
	CreatedAt         time.Time  `json:"-"`
	UpdatedAt         time.Time  `json:"-"`
	DeletedAt         *time.Time `json:"-"`
}

// CartaMeta espelha a paginação da API CloudHub.
type CartaMeta struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

// CartaServiceSummary é o item de listagem local de serviços por subtema.
type CartaServiceSummary struct {
	Slug                        string   `json:"slug"`
	Name                        string   `json:"name"`
	ArticleStatus               string   `json:"articleStatus,omitempty"`
	CreatedDate                 string   `json:"createdDate,omitempty"`
	LastModifiedDate            string   `json:"lastModifiedDate,omitempty"`
	LastPublishedDate           string   `json:"lastPublishedDate,omitempty"`
	ResponsibleOrgUnit          string   `json:"responsibleOrgUnit,omitempty"`
	ResponsibleOrgUnitShortName string   `json:"responsibleOrgUnitShortName,omitempty"`
	Summary                     string   `json:"summary,omitempty"`
	IsFree                      bool     `json:"isFree,omitempty"`
	ThemeSlug                   string   `json:"themeSlug,omitempty"`
	SubthemeSlug                string   `json:"subthemeSlug,omitempty"`
}
