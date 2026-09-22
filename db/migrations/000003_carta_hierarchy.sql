-- +goose Up
-- Hierarquia da Carta de Serviços para navegação local (themes → subthemes → services).

CREATE TABLE IF NOT EXISTS carta_themes (
    slug                VARCHAR(255) PRIMARY KEY,
    name                TEXT NOT NULL,
    subthemes_count     INT NOT NULL DEFAULT 0,
    published_services  INT NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_carta_themes_active
    ON carta_themes (name)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS carta_subthemes (
    slug                VARCHAR(255) PRIMARY KEY,
    theme_slug          VARCHAR(255) NOT NULL REFERENCES carta_themes(slug),
    name                TEXT NOT NULL,
    published_services  INT NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_carta_subthemes_theme
    ON carta_subthemes (theme_slug)
    WHERE deleted_at IS NULL;

ALTER TABLE catalog_items
    ADD COLUMN IF NOT EXISTS theme_slug VARCHAR(255),
    ADD COLUMN IF NOT EXISTS subtheme_slug VARCHAR(255);

CREATE INDEX IF NOT EXISTS idx_catalog_items_theme_slug
    ON catalog_items (theme_slug)
    WHERE deleted_at IS NULL AND source = 'salesforce';

CREATE INDEX IF NOT EXISTS idx_catalog_items_subtheme_slug
    ON catalog_items (subtheme_slug)
    WHERE deleted_at IS NULL AND source = 'salesforce';

-- Backfill a partir de source_data já sincronizado.
UPDATE catalog_items
SET
    theme_slug = NULLIF(TRIM(source_data->>'themeSlug'), ''),
    subtheme_slug = NULLIF(TRIM(source_data->>'subthemeSlug'), '')
WHERE source = 'salesforce'
  AND deleted_at IS NULL
  AND (source_data ? 'themeSlug' OR source_data ? 'subthemeSlug');

INSERT INTO carta_themes (slug, name, published_services, subthemes_count)
SELECT
    theme_slug,
    COALESCE(NULLIF(MAX(source_data->>'themeName'), ''), theme_slug),
    COUNT(*)::INT,
    0
FROM catalog_items
WHERE source = 'salesforce'
  AND deleted_at IS NULL
  AND status = 'active'
  AND theme_slug IS NOT NULL
  AND theme_slug <> ''
GROUP BY theme_slug
ON CONFLICT (slug) DO UPDATE SET
    name = EXCLUDED.name,
    published_services = EXCLUDED.published_services,
    updated_at = NOW(),
    deleted_at = NULL;

INSERT INTO carta_subthemes (slug, theme_slug, name, published_services)
SELECT
    ci.subtheme_slug,
    ci.theme_slug,
    COALESCE(NULLIF(MAX(ci.source_data->>'subthemeName'), ''), ci.subtheme_slug),
    COUNT(*)::INT
FROM catalog_items ci
WHERE ci.source = 'salesforce'
  AND ci.deleted_at IS NULL
  AND ci.status = 'active'
  AND ci.subtheme_slug IS NOT NULL
  AND ci.subtheme_slug <> ''
  AND ci.theme_slug IS NOT NULL
  AND ci.theme_slug <> ''
GROUP BY ci.subtheme_slug, ci.theme_slug
ON CONFLICT (slug) DO UPDATE SET
    theme_slug = EXCLUDED.theme_slug,
    name = EXCLUDED.name,
    published_services = EXCLUDED.published_services,
    updated_at = NOW(),
    deleted_at = NULL;

UPDATE carta_themes t
SET subthemes_count = (
    SELECT COUNT(*)::INT
    FROM carta_subthemes s
    WHERE s.theme_slug = t.slug
      AND s.deleted_at IS NULL
),
updated_at = NOW();

-- +goose Down
DROP INDEX IF EXISTS idx_catalog_items_subtheme_slug;
DROP INDEX IF EXISTS idx_catalog_items_theme_slug;

ALTER TABLE catalog_items
    DROP COLUMN IF EXISTS subtheme_slug,
    DROP COLUMN IF EXISTS theme_slug;

DROP INDEX IF EXISTS idx_carta_subthemes_theme;
DROP TABLE IF EXISTS carta_subthemes;

DROP INDEX IF EXISTS idx_carta_themes_active;
DROP TABLE IF EXISTS carta_themes;
