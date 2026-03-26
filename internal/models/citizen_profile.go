package models

import "time"

// CitizenProfile é o snapshot do perfil do cidadão para personalização de recomendações.
// CPF nunca é armazenado — apenas cpf_hash (SHA-256 + salt).
type CitizenProfile struct {
	ID              string     `json:"id"`
	CPFHash         string     `json:"-"` // nunca serializar o hash
	Bairro          string     `json:"bairro,omitempty"`
	Cidade          string     `json:"cidade,omitempty"`
	Estado          string     `json:"estado,omitempty"`
	CEP             string     `json:"cep,omitempty"`
	Escolaridade    string     `json:"escolaridade,omitempty"`
	RendaFamiliar   string     `json:"renda_familiar,omitempty"`
	Deficiencia     string     `json:"deficiencia,omitempty"`
	Etnia           string     `json:"etnia,omitempty"`
	Genero          string     `json:"genero,omitempty"`
	FaixaEtaria     string     `json:"faixa_etaria,omitempty"`
	ClusterID       *int       `json:"cluster_id,omitempty"`
	LastSyncedAt    time.Time  `json:"last_synced_at"`
}

// DemographicCluster representa um cluster de cidadãos para recomendação anônima.
type DemographicCluster struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	TopItemIDs  []string `json:"top_item_ids,omitempty"`
}
