-- +goose Up
-- app-catalogo: schema inicial
-- Extensões necessárias para FTS pt_BR, fuzzy search e vetores semânticos (v2)

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS unaccent;
-- vector (pgvector) precisa da imagem pgvector/pgvector:pg16 no docker
-- Em prod: CREATE EXTENSION IF NOT EXISTS vector;

-- Tipos enum
CREATE TYPE item_source AS ENUM (
    'salesforce',
    'courses',
    'jobs',
    'mei'
);

CREATE TYPE item_type AS ENUM (
    'service',
    'course',
    'job',
    'mei_opportunity'
);

-- Wrapper IMMUTABLE para unaccent (necessário em expressões de trigger e índices)
CREATE OR REPLACE FUNCTION immutable_unaccent(text)
RETURNS text AS $$
  SELECT unaccent($1)
$$ LANGUAGE sql IMMUTABLE PARALLEL SAFE;

-- Tabela central do catálogo
CREATE TABLE catalog_items (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    external_id     VARCHAR(255) NOT NULL,
    source          item_source NOT NULL,
    type            item_type NOT NULL,

    title           TEXT NOT NULL,
    description     TEXT,
    short_desc      TEXT,
    organization    VARCHAR(500),
    url             TEXT,
    image_url       TEXT,

    -- Campos de elegibilidade para scoring de recomendação
    target_audience JSONB NOT NULL DEFAULT '{}',
    bairros         TEXT[] NOT NULL DEFAULT '{}',
    modalidade      VARCHAR(100),
    status          VARCHAR(50) NOT NULL DEFAULT 'active',
    tags            TEXT[] NOT NULL DEFAULT '{}',

    -- Dados raw da fonte (preserva tudo para evolução futura)
    source_data     JSONB NOT NULL DEFAULT '{}',

    valid_from      TIMESTAMP WITH TIME ZONE,
    valid_until     TIMESTAMP WITH TIME ZONE,
    source_updated_at TIMESTAMP WITH TIME ZONE,

    -- FTS: atualizado via trigger (GENERATED ALWAYS AS não suporta unaccent em todos os ambientes)
    search_vector   TSVECTOR,

    -- Placeholder para embeddings semânticos (v2 — pgvector)
    -- embedding vector(1536),

    created_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMP WITH TIME ZONE,

    CONSTRAINT uq_catalog_items_source_external UNIQUE (source, external_id)
);

-- Trigger para manter search_vector atualizado
CREATE OR REPLACE FUNCTION update_search_vector()
RETURNS TRIGGER AS $$
BEGIN
    NEW.search_vector :=
        setweight(to_tsvector('portuguese', immutable_unaccent(coalesce(NEW.title, ''))), 'A') ||
        setweight(to_tsvector('portuguese', immutable_unaccent(coalesce(NEW.short_desc, ''))), 'B') ||
        setweight(to_tsvector('portuguese', immutable_unaccent(coalesce(NEW.description, ''))), 'C') ||
        setweight(to_tsvector('portuguese', immutable_unaccent(array_to_string(coalesce(NEW.tags, '{}'), ' '))), 'D');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_catalog_items_search_vector
    BEFORE INSERT OR UPDATE ON catalog_items
    FOR EACH ROW EXECUTE FUNCTION update_search_vector();

CREATE INDEX idx_catalog_items_fts
    ON catalog_items USING GIN(search_vector);

CREATE INDEX idx_catalog_items_source_type_status
    ON catalog_items(source, type, status)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_catalog_items_tags
    ON catalog_items USING GIN(tags)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_catalog_items_bairros
    ON catalog_items USING GIN(bairros)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_catalog_items_source_updated
    ON catalog_items(source_updated_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_catalog_items_status_active
    ON catalog_items(type, created_at DESC)
    WHERE status = 'active' AND deleted_at IS NULL;

-- Trigger para atualizar updated_at automaticamente
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_catalog_items_updated_at
    BEFORE UPDATE ON catalog_items
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- Perfil do cidadão (snapshot para personalização)
-- CPF nunca é armazenado — apenas cpf_hash (SHA-256 + salt)
CREATE TABLE citizen_profiles (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cpf_hash        VARCHAR(64) NOT NULL,

    bairro          VARCHAR(255),
    cidade          VARCHAR(100),
    estado          VARCHAR(2),
    cep             VARCHAR(8),
    escolaridade    VARCHAR(100),
    renda_familiar  VARCHAR(100),
    deficiencia     TEXT,
    etnia           VARCHAR(100),
    genero          VARCHAR(100),
    faixa_etaria    VARCHAR(50),

    -- Cluster demográfico calculado pelo serviço
    cluster_id      INTEGER,
    cluster_updated_at TIMESTAMP WITH TIME ZONE,

    last_synced_at  TIMESTAMP WITH TIME ZONE NOT NULL,
    created_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_citizen_profiles_cpf_hash UNIQUE (cpf_hash)
);

CREATE INDEX idx_citizen_profiles_cpf_hash ON citizen_profiles(cpf_hash);
CREATE INDEX idx_citizen_profiles_cluster ON citizen_profiles(cluster_id);
CREATE INDEX idx_citizen_profiles_bairro ON citizen_profiles(bairro, cidade);
CREATE INDEX idx_citizen_profiles_stale ON citizen_profiles(last_synced_at ASC);

CREATE TRIGGER trg_citizen_profiles_updated_at
    BEFORE UPDATE ON citizen_profiles
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- Clusters demográficos para recomendação anônima
CREATE TABLE demographic_clusters (
    id              SERIAL PRIMARY KEY,
    name            VARCHAR(100) NOT NULL,
    description     TEXT,
    criteria        JSONB NOT NULL DEFAULT '{}',
    top_item_ids    UUID[] NOT NULL DEFAULT '{}',
    created_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

CREATE TRIGGER trg_demographic_clusters_updated_at
    BEFORE UPDATE ON demographic_clusters
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- Audit log de sincronizações
CREATE TABLE sync_events (
    id              BIGSERIAL PRIMARY KEY,
    source          item_source NOT NULL,
    event_type      VARCHAR(50) NOT NULL,
    status          VARCHAR(50) NOT NULL,
    items_processed INTEGER NOT NULL DEFAULT 0,
    items_failed    INTEGER NOT NULL DEFAULT 0,
    error_message   TEXT,
    duration_ms     INTEGER,
    started_at      TIMESTAMP WITH TIME ZONE NOT NULL,
    completed_at    TIMESTAMP WITH TIME ZONE,
    metadata        JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_sync_events_source_started ON sync_events(source, started_at DESC);
CREATE INDEX idx_sync_events_status ON sync_events(status, started_at DESC);

-- Cursor de sincronização incremental do SalesForce
CREATE TABLE salesforce_sync_cursor (
    id              SERIAL PRIMARY KEY,
    object_type     VARCHAR(100) NOT NULL,
    last_sync_at    TIMESTAMP WITH TIME ZONE,
    last_delta_token TEXT,
    created_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_salesforce_sync_cursor_object UNIQUE (object_type)
);

CREATE TRIGGER trg_salesforce_sync_cursor_updated_at
    BEFORE UPDATE ON salesforce_sync_cursor
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- +goose Down
DROP TABLE IF EXISTS salesforce_sync_cursor;
DROP TABLE IF EXISTS sync_events;
DROP TABLE IF EXISTS demographic_clusters;
DROP TABLE IF EXISTS citizen_profiles;
DROP TABLE IF EXISTS catalog_items;
DROP FUNCTION IF EXISTS update_updated_at;
DROP FUNCTION IF EXISTS immutable_unaccent;
DROP FUNCTION IF EXISTS update_search_vector;
DROP TYPE IF EXISTS item_type;
DROP TYPE IF EXISTS item_source;
DROP EXTENSION IF EXISTS unaccent;
DROP EXTENSION IF EXISTS pg_trgm;
DROP EXTENSION IF EXISTS "uuid-ossp";
