-- +goose Up
-- Support deterministic exact, slug, trigram, and compatible KNN candidate pools.
-- +goose StatementBegin
DO $$
DECLARE
    installed_vector_version INTEGER[];
BEGIN
    SELECT string_to_array(extversion, '.')::INTEGER[]
    INTO installed_vector_version
    FROM pg_extension
    WHERE extname = 'vector';

    IF installed_vector_version IS NULL OR installed_vector_version < ARRAY[0, 8, 0] THEN
        RAISE EXCEPTION 'pgvector 0.8.0 or newer is required for filtered iterative HNSW scans';
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX idx_catalog_items_active_title_trigram
    ON catalog_items USING gist (immutable_unaccent(lower(title)) gist_trgm_ops)
    WHERE status = 'active' AND deleted_at IS NULL;

CREATE INDEX idx_catalog_items_active_external_id
    ON catalog_items (lower(external_id))
    WHERE status = 'active' AND deleted_at IS NULL;

CREATE INDEX idx_catalog_items_active_source_slug
    ON catalog_items (lower((source_data->>'slug')))
    WHERE status = 'active' AND deleted_at IS NULL;

-- Keep incompatible and legacy vectors out of the ANN graph used by search.
-- Besides reducing write amplification, the partial predicate gives PostgreSQL
-- an index whose cardinality matches the filtered KNN query closely enough to
-- avoid a sequential scan when embedding provenance columns are correlated.
CREATE INDEX idx_catalog_items_search_embedding
    ON catalog_items USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64)
    WHERE status = 'active'
      AND deleted_at IS NULL
      AND embedding IS NOT NULL
      AND embedding_model IS NOT NULL
      AND embedding_model_version IS NOT NULL
      AND embedding_dimensions IS NOT NULL
      AND embedding_task_type IS NOT NULL
      AND embedding_document_version IS NOT NULL
      AND embedding_source_hash IS NOT NULL
      AND embedding_generated_at IS NOT NULL;

-- Migration 000002 created an unfiltered HNSW index before embedding
-- provenance existed. The partial index above fully replaces it for search.
DROP INDEX IF EXISTS idx_catalog_items_embedding;

-- +goose Down
CREATE INDEX IF NOT EXISTS idx_catalog_items_embedding
    ON catalog_items USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);
DROP INDEX IF EXISTS idx_catalog_items_search_embedding;
DROP INDEX IF EXISTS idx_catalog_items_active_source_slug;
DROP INDEX IF EXISTS idx_catalog_items_active_external_id;
DROP INDEX IF EXISTS idx_catalog_items_active_title_trigram;
