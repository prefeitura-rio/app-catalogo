-- +goose Up
-- Código SICI do órgão responsável (Carta de Serviços) para filtro por igualdade.

ALTER TABLE catalog_items
    ADD COLUMN IF NOT EXISTS sici_code VARCHAR;

CREATE INDEX IF NOT EXISTS idx_catalog_items_sici_code
    ON catalog_items (sici_code)
    WHERE source = 'salesforce'
      AND deleted_at IS NULL
      AND status = 'active';

-- Sem backfill: source_data atual ainda não contém responsibleOrgUnitSiciCode.
-- Após o deploy, rodar FullSync.

-- +goose Down
DROP INDEX IF EXISTS idx_catalog_items_sici_code;

ALTER TABLE catalog_items
    DROP COLUMN IF EXISTS sici_code;
