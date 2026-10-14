-- +goose Up
-- Mapeia slugs antigos da Carta de Serviços para o slug canônico atual.
-- Usado por GET /api/public/services/{slug} para responder 301 local sem chamar o CloudHub.

CREATE TABLE IF NOT EXISTS carta_service_slug_aliases (
    old_slug        VARCHAR(255) PRIMARY KEY,
    canonical_slug  VARCHAR(255) NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_carta_slug_aliases_canonical
    ON carta_service_slug_aliases (canonical_slug);

-- Backfill a partir de slugHistory já gravado em source_data.
INSERT INTO carta_service_slug_aliases (old_slug, canonical_slug)
SELECT DISTINCT
    TRIM(hist.old_slug),
    ci.external_id
FROM catalog_items ci
CROSS JOIN LATERAL jsonb_array_elements_text(
    COALESCE(ci.source_data->'slugHistory', '[]'::jsonb)
) AS hist(old_slug)
WHERE ci.source = 'salesforce'
  AND ci.deleted_at IS NULL
  AND TRIM(hist.old_slug) <> ''
  AND TRIM(hist.old_slug) <> ci.external_id
ON CONFLICT (old_slug) DO UPDATE SET
    canonical_slug = EXCLUDED.canonical_slug,
    updated_at = NOW();

-- +goose Down
DROP INDEX IF EXISTS idx_carta_slug_aliases_canonical;
DROP TABLE IF EXISTS carta_service_slug_aliases;
