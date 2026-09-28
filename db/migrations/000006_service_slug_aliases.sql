-- +goose Up
-- Materialize canonical and historical service slugs without exposing source_data
-- as a public lookup contract.
CREATE TABLE IF NOT EXISTS catalog_item_slug_aliases (
    catalog_item_id UUID NOT NULL REFERENCES catalog_items(id) ON DELETE CASCADE,
    slug            TEXT NOT NULL,
    is_canonical    BOOLEAN NOT NULL,
    created_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (catalog_item_id, slug),
    CONSTRAINT catalog_item_slug_aliases_slug_format
        CHECK (slug ~ '^[a-z0-9][a-z0-9-]*$')
);

CREATE INDEX IF NOT EXISTS idx_catalog_item_slug_aliases_lookup
    ON catalog_item_slug_aliases (slug, is_canonical DESC, catalog_item_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_catalog_item_slug_aliases_one_canonical
    ON catalog_item_slug_aliases (catalog_item_id)
    WHERE is_canonical;

INSERT INTO catalog_item_slug_aliases (catalog_item_id, slug, is_canonical)
SELECT id, source_data->>'slug', TRUE
FROM catalog_items
WHERE type = 'service'
  AND source_data->>'slug' ~ '^[a-z0-9][a-z0-9-]*$'
ON CONFLICT (catalog_item_id, slug) DO UPDATE
SET is_canonical = TRUE;

INSERT INTO catalog_item_slug_aliases (catalog_item_id, slug, is_canonical)
SELECT catalog_items.id, historical_slug.slug, FALSE
FROM catalog_items
CROSS JOIN LATERAL jsonb_array_elements_text(
    CASE
        WHEN jsonb_typeof(catalog_items.source_data->'slug_history') = 'array'
            THEN catalog_items.source_data->'slug_history'
        ELSE '[]'::jsonb
    END
) AS historical_slug(slug)
WHERE catalog_items.type = 'service'
  AND historical_slug.slug ~ '^[a-z0-9][a-z0-9-]*$'
  AND historical_slug.slug IS DISTINCT FROM catalog_items.source_data->>'slug'
ON CONFLICT (catalog_item_id, slug) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS catalog_item_slug_aliases;
