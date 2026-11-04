-- +goose Up
-- Snapshot editorial da home da Carta (GET /themes?include_featured=true).

CREATE TABLE IF NOT EXISTS carta_home_featured (
    slug                   VARCHAR(255) PRIMARY KEY,
    position               INT NOT NULL,
    name                   TEXT NOT NULL,
    summary                TEXT NOT NULL DEFAULT '',
    theme_slug             VARCHAR(255) NOT NULL DEFAULT '',
    theme_name             TEXT NOT NULL DEFAULT '',
    subtheme_slug          VARCHAR(255) NOT NULL DEFAULT '',
    subtheme_name          TEXT NOT NULL DEFAULT '',
    last_published_date    TEXT NOT NULL DEFAULT '',
    autoservice_category   TEXT,
    synced_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT carta_home_featured_position_unique UNIQUE (position)
);

-- +goose Down
DROP TABLE IF EXISTS carta_home_featured;
