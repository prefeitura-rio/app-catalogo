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
-- jsonb_typeof = 'array' evita falha quando slugHistory é JSON null (Marshal de slice nil).
-- DISTINCT ON (old_slug) evita 'ON CONFLICT DO UPDATE cannot affect row a second time'.
INSERT INTO carta_service_slug_aliases (old_slug, canonical_slug)
SELECT DISTINCT ON (TRIM(hist.old_slug))
    TRIM(hist.old_slug),
    ci.external_id
FROM catalog_items ci
CROSS JOIN LATERAL jsonb_array_elements_text(ci.source_data->'slugHistory') AS hist(old_slug)
WHERE ci.source = 'salesforce'
  AND ci.deleted_at IS NULL
  AND jsonb_typeof(ci.source_data->'slugHistory') = 'array'
  AND TRIM(hist.old_slug) <> ''
  AND TRIM(hist.old_slug) <> ci.external_id
ORDER BY TRIM(hist.old_slug), ci.updated_at DESC
ON CONFLICT (old_slug) DO UPDATE SET
    canonical_slug = EXCLUDED.canonical_slug,
    updated_at = NOW();

-- Remove aliases em que o próprio canônico aparece como old_slug (ciclo de redirect).
DELETE FROM carta_service_slug_aliases
WHERE old_slug = canonical_slug;

-- Invalida o cursor para o próximo ciclo fazer full sync e regravar flags
-- (activeCategoryConfigId / activeCategoryName) nos serviços já existentes.
DELETE FROM salesforce_sync_cursor
WHERE object_type = 'carta_servicos';

-- +goose Down
DROP INDEX IF EXISTS idx_carta_slug_aliases_canonical;
DROP TABLE IF EXISTS carta_service_slug_aliases;
