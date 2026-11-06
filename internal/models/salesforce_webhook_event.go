package models

import "time"

// WebhookEventStatus é o estado de um evento do webhook SalesForce na fila durável.
type WebhookEventStatus string

const (
	// WebhookEventPending aguarda processamento (ou novo processamento após falha).
	WebhookEventPending WebhookEventStatus = "pending"
	// WebhookEventCompleted foi sincronizado (ou superado por um evento posterior do mesmo slug).
	WebhookEventCompleted WebhookEventStatus = "completed"
)

// SalesForceWebhookEvent é um evento do webhook SalesForce gravado antes do processamento.
type SalesForceWebhookEvent struct {
	ID            int64
	EventKey      string
	Slug          string
	EventType     string
	Status        WebhookEventStatus
	Attempts      int
	NextAttemptAt time.Time
	ReceivedAt    time.Time
	// ClaimedAt é o horário do banco em que esta tentativa assumiu o evento.
	// Eventos pendentes do mesmo slug recebidos até ele são cobertos pela sincronização.
	ClaimedAt time.Time
}

// WebhookQueueStats resume a fila do webhook SalesForce para as métricas de acúmulo.
type WebhookQueueStats struct {
	// Pending é quantos eventos estão pendentes (novos, em tentativa ou à espera da próxima).
	Pending int64
	// Failing é quantos pendentes já falharam ao menos uma vez (last_error preenchido).
	Failing int64
	// OldestPendingAge é há quanto tempo o pendente mais antigo foi recebido; 0 sem pendentes.
	OldestPendingAge time.Duration
}
