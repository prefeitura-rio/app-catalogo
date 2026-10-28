-- +goose Up
-- Fila durável dos eventos do webhook SalesForce (Carta de Serviços).
-- O evento é gravado antes da resposta ao remetente; o processamento que falha
-- (ou que morre com o pod) fica 'pending' e é retomado quando next_attempt_at vence.
-- event_key = SHA-256 (hex) do body assinado: reentrega idêntica não cria outra linha.

CREATE TABLE IF NOT EXISTS salesforce_webhook_events (
    id               BIGSERIAL PRIMARY KEY,
    event_key        CHAR(64)     NOT NULL,
    slug             VARCHAR(255) NOT NULL,
    event_type       VARCHAR(64)  NOT NULL DEFAULT '',
    status           VARCHAR(16)  NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'completed')),
    attempts         INTEGER      NOT NULL DEFAULT 0,
    last_error       TEXT,
    next_attempt_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    received_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    processed_at     TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_salesforce_webhook_events_event_key UNIQUE (event_key)
);

CREATE INDEX IF NOT EXISTS idx_salesforce_webhook_events_due
    ON salesforce_webhook_events (next_attempt_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_salesforce_webhook_events_slug_pending
    ON salesforce_webhook_events (slug, received_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_salesforce_webhook_events_processed
    ON salesforce_webhook_events (processed_at)
    WHERE status = 'completed';

-- +goose Down
DROP TABLE IF EXISTS salesforce_webhook_events;
