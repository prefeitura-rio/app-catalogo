-- +goose Up
-- Preserve legacy vectors while recording provenance for every newly generated
-- embedding. Rows with legacy vectors and NULL metadata are regenerated lazily.
ALTER TABLE catalog_items
    ADD COLUMN embedding_model TEXT,
    ADD COLUMN embedding_model_version TEXT,
    ADD COLUMN embedding_dimensions INTEGER,
    ADD COLUMN embedding_task_type TEXT,
    ADD COLUMN embedding_document_version TEXT,
    ADD COLUMN embedding_source_hash TEXT,
    ADD COLUMN embedding_generated_at TIMESTAMP WITH TIME ZONE,
    ADD COLUMN embedding_claim_token UUID,
    ADD COLUMN embedding_claimed_at TIMESTAMP WITH TIME ZONE;

ALTER TABLE catalog_items
    ADD CONSTRAINT catalog_items_embedding_metadata_consistent CHECK (
        (
            embedding_model IS NULL
            AND embedding_model_version IS NULL
            AND embedding_dimensions IS NULL
            AND embedding_task_type IS NULL
            AND embedding_document_version IS NULL
            AND embedding_source_hash IS NULL
            AND embedding_generated_at IS NULL
        )
        OR
        (
            embedding IS NOT NULL
            AND embedding_model IS NOT NULL
            AND embedding_model <> ''
            AND embedding_model_version IS NOT NULL
            AND embedding_model_version <> ''
            AND embedding_dimensions IS NOT NULL
            AND embedding_dimensions > 0
            AND embedding_task_type IS NOT NULL
            AND embedding_task_type <> ''
            AND embedding_document_version IS NOT NULL
            AND embedding_document_version <> ''
            AND embedding_source_hash IS NOT NULL
            AND embedding_source_hash ~ '^[0-9a-f]{64}$'
            AND embedding_generated_at IS NOT NULL
        )
    ),
    ADD CONSTRAINT catalog_items_embedding_dimension_matches CHECK (
        embedding_dimensions IS NULL
        OR embedding_dimensions = vector_dims(embedding)
    ),
    ADD CONSTRAINT catalog_items_embedding_claim_consistent CHECK (
        (embedding_claim_token IS NULL AND embedding_claimed_at IS NULL)
        OR
        (embedding_claim_token IS NOT NULL AND embedding_claimed_at IS NOT NULL)
    );

CREATE INDEX idx_catalog_items_embedding_work
    ON catalog_items (updated_at, id)
    WHERE deleted_at IS NULL AND status = 'active';

-- Embedding claims and completions are maintenance state, not source changes.
-- Keep the domain updated_at timestamp stable when only lifecycle columns move.
DROP TRIGGER IF EXISTS trg_catalog_items_updated_at ON catalog_items;
CREATE TRIGGER trg_catalog_items_updated_at
    BEFORE UPDATE OF
        external_id,
        source,
        type,
        title,
        description,
        short_desc,
        organization,
        url,
        image_url,
        target_audience,
        bairros,
        modalidade,
        status,
        tags,
        source_data,
        valid_from,
        valid_until,
        source_updated_at,
        deleted_at
    ON catalog_items
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION invalidate_catalog_item_embedding()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF ROW(
        NEW.type,
        NEW.title,
        NEW.description,
        NEW.short_desc,
        NEW.organization,
        NEW.tags
    ) IS DISTINCT FROM ROW(
        OLD.type,
        OLD.title,
        OLD.description,
        OLD.short_desc,
        OLD.organization,
        OLD.tags
    ) THEN
        NEW.embedding := NULL;
        NEW.embedding_model := NULL;
        NEW.embedding_model_version := NULL;
        NEW.embedding_dimensions := NULL;
        NEW.embedding_task_type := NULL;
        NEW.embedding_document_version := NULL;
        NEW.embedding_source_hash := NULL;
        NEW.embedding_generated_at := NULL;
        NEW.embedding_claim_token := NULL;
        NEW.embedding_claimed_at := NULL;
    END IF;

    IF ROW(
        NEW.status,
        NEW.deleted_at,
        NEW.valid_from,
        NEW.valid_until
    ) IS DISTINCT FROM ROW(
        OLD.status,
        OLD.deleted_at,
        OLD.valid_from,
        OLD.valid_until
    ) THEN
        NEW.embedding_claim_token := NULL;
        NEW.embedding_claimed_at := NULL;
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_catalog_items_invalidate_embedding
    BEFORE UPDATE OF
        type,
        title,
        description,
        short_desc,
        organization,
        tags,
        status,
        deleted_at,
        valid_from,
        valid_until
    ON catalog_items
    FOR EACH ROW
    EXECUTE FUNCTION invalidate_catalog_item_embedding();

-- +goose Down
DROP TRIGGER IF EXISTS trg_catalog_items_invalidate_embedding ON catalog_items;
DROP FUNCTION IF EXISTS invalidate_catalog_item_embedding();
DROP INDEX IF EXISTS idx_catalog_items_embedding_work;

DROP TRIGGER IF EXISTS trg_catalog_items_updated_at ON catalog_items;
CREATE TRIGGER trg_catalog_items_updated_at
    BEFORE UPDATE ON catalog_items
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

ALTER TABLE catalog_items
    DROP CONSTRAINT IF EXISTS catalog_items_embedding_claim_consistent,
    DROP CONSTRAINT IF EXISTS catalog_items_embedding_dimension_matches,
    DROP CONSTRAINT IF EXISTS catalog_items_embedding_metadata_consistent,
    DROP COLUMN IF EXISTS embedding_claimed_at,
    DROP COLUMN IF EXISTS embedding_claim_token,
    DROP COLUMN IF EXISTS embedding_generated_at,
    DROP COLUMN IF EXISTS embedding_source_hash,
    DROP COLUMN IF EXISTS embedding_document_version,
    DROP COLUMN IF EXISTS embedding_task_type,
    DROP COLUMN IF EXISTS embedding_dimensions,
    DROP COLUMN IF EXISTS embedding_model_version,
    DROP COLUMN IF EXISTS embedding_model;
