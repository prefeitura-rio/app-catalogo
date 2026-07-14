-- +goose Up
-- Bind search and recommendation cache snapshots to committed catalog content.
CREATE TABLE catalog_state (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0)
);

INSERT INTO catalog_state (singleton, revision)
VALUES (TRUE, 1);

-- +goose StatementBegin
CREATE FUNCTION bump_catalog_revision()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    -- Claim ownership changes only coordinate embedding workers. The vector and
    -- its provenance still participate because they alter semantic retrieval.
    IF TG_OP = 'UPDATE'
       AND (
           to_jsonb(OLD) - ARRAY['embedding_claim_token', 'embedding_claimed_at']::TEXT[]
       ) IS NOT DISTINCT FROM (
           to_jsonb(NEW) - ARRAY['embedding_claim_token', 'embedding_claimed_at']::TEXT[]
       ) THEN
        RETURN NULL;
    END IF;

    -- A synchronization transaction may touch many rows. One revision change
    -- identifies the complete atomic commit without repeatedly rewriting the
    -- singleton state row.
    IF current_setting('app_catalogo.catalog_revision_bumped', TRUE) = '1' THEN
        RETURN NULL;
    END IF;

    UPDATE catalog_state
    SET revision = revision + 1
    WHERE singleton = TRUE;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'catalog_state singleton row is missing';
    END IF;

    PERFORM set_config('app_catalogo.catalog_revision_bumped', '1', TRUE);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER trg_catalog_items_revision
    AFTER INSERT OR UPDATE OR DELETE ON catalog_items
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    EXECUTE FUNCTION bump_catalog_revision();

-- +goose Down
DROP TRIGGER IF EXISTS trg_catalog_items_revision ON catalog_items;
DROP FUNCTION IF EXISTS bump_catalog_revision();
DROP TABLE IF EXISTS catalog_state;
