package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prefeitura-rio/app-catalogo/internal/models"
)

// maxWebhookErrorLength limita o last_error gravado por evento.
const maxWebhookErrorLength = 1000

const webhookEventColumns = `id, event_key, slug, event_type, status, attempts, next_attempt_at, received_at, NOW()`

// SalesForceWebhookRepository persiste a fila durável de eventos do webhook SalesForce.
type SalesForceWebhookRepository struct {
	db *pgxpool.Pool
}

func NewSalesForceWebhookRepository(db *pgxpool.Pool) *SalesForceWebhookRepository {
	return &SalesForceWebhookRepository{db: db}
}

func scanWebhookEvent(row pgx.Row) (*models.SalesForceWebhookEvent, error) {
	var (
		ev     models.SalesForceWebhookEvent
		status string
	)
	if err := row.Scan(
		&ev.ID, &ev.EventKey, &ev.Slug, &ev.EventType, &status,
		&ev.Attempts, &ev.NextAttemptAt, &ev.ReceivedAt, &ev.ClaimedAt,
	); err != nil {
		return nil, err
	}
	ev.Status = models.WebhookEventStatus(status)
	return &ev, nil
}

// Enqueue grava o evento já assumido pela primeira tentativa (attempts = 1, lease até
// NOW() + lease). Devolve created = false, sem erro, quando event_key já existe.
func (r *SalesForceWebhookRepository) Enqueue(
	ctx context.Context,
	eventKey, slug, eventType string,
	lease time.Duration,
) (*models.SalesForceWebhookEvent, bool, error) {
	row := r.db.QueryRow(ctx, `
		INSERT INTO salesforce_webhook_events (event_key, slug, event_type, attempts, next_attempt_at)
		VALUES ($1, $2, $3, 1, NOW() + make_interval(secs => $4::double precision))
		ON CONFLICT (event_key) DO NOTHING
		RETURNING `+webhookEventColumns,
		eventKey, slug, eventType, lease.Seconds(),
	)
	ev, err := scanWebhookEvent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return ev, true, nil
}

// ClaimDue assume até limit eventos pendentes vencidos (inclusive os de lease expirado,
// de um processamento que morreu), incrementando attempts e renovando o lease.
// SKIP LOCKED deixa várias réplicas reprocessarem sem pegar o mesmo evento.
func (r *SalesForceWebhookRepository) ClaimDue(
	ctx context.Context,
	limit int,
	lease time.Duration,
) ([]*models.SalesForceWebhookEvent, error) {
	rows, err := r.db.Query(ctx, `
		UPDATE salesforce_webhook_events e
		SET attempts = e.attempts + 1,
		    next_attempt_at = NOW() + make_interval(secs => $2::double precision),
		    updated_at = NOW()
		WHERE e.id IN (
			SELECT id FROM salesforce_webhook_events
			WHERE status = 'pending' AND next_attempt_at <= NOW()
			ORDER BY next_attempt_at, id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING e.id, e.event_key, e.slug, e.event_type, e.status, e.attempts,
		          e.next_attempt_at, e.received_at, NOW()
	`, limit, lease.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*models.SalesForceWebhookEvent
	for rows.Next() {
		ev, err := scanWebhookEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

// Complete marca o evento como concluído e, com ele, os pendentes do mesmo slug
// recebidos até o início desta tentativa (a sincronização leu o estado atual do
// registro, que já os inclui). Devolve quantos eventos foram concluídos; 0 quando o
// evento já estava concluído (reprocessamento repetido não tem efeito).
func (r *SalesForceWebhookRepository) Complete(ctx context.Context, ev *models.SalesForceWebhookEvent) (int64, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE salesforce_webhook_events
		SET status = 'completed', processed_at = NOW(), last_error = NULL, updated_at = NOW()
		WHERE status = 'pending'
		  AND (id = $1 OR (slug = $2 AND received_at <= $3))
	`, ev.ID, ev.Slug, ev.ClaimedAt)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Fail registra a falha da tentativa e agenda a próxima para NOW() + retryIn.
// Só vale para a tentativa que assumiu o evento (attempts igual): uma tentativa
// atrasada não sobrescreve outra nem reabre evento concluído.
func (r *SalesForceWebhookRepository) Fail(
	ctx context.Context,
	ev *models.SalesForceWebhookEvent,
	errMsg string,
	retryIn time.Duration,
) error {
	if len(errMsg) > maxWebhookErrorLength {
		errMsg = strings.ToValidUTF8(errMsg[:maxWebhookErrorLength], "")
	}
	_, err := r.db.Exec(ctx, `
		UPDATE salesforce_webhook_events
		SET last_error = $3,
		    next_attempt_at = NOW() + make_interval(secs => $4::double precision),
		    updated_at = NOW()
		WHERE id = $1 AND status = 'pending' AND attempts = $2
	`, ev.ID, ev.Attempts, errMsg, retryIn.Seconds())
	return err
}

// DeleteCompletedBefore remove eventos concluídos antes de before (retenção).
func (r *SalesForceWebhookRepository) DeleteCompletedBefore(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.db.Exec(ctx, `
		DELETE FROM salesforce_webhook_events
		WHERE status = 'completed' AND processed_at < $1
	`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
