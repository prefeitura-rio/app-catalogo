-- +goose NO TRANSACTION
-- +goose Up
-- Migration: busca semântica (pgvector) + citizen journeys + novos item_source

-- 1. Novos valores no enum item_source
ALTER TYPE item_source ADD VALUE IF NOT EXISTS 'typesense';
ALTER TYPE item_source ADD VALUE IF NOT EXISTS 'app-go-api';

-- 2. Habilitar pgvector (disponível na imagem pgvector/pgvector:pg16)
CREATE EXTENSION IF NOT EXISTS vector;

-- 3. Coluna de embedding semântico (gemini-embedding-001, OutputDimensionality=1536)
--    1536 dims: abaixo do limite HNSW do pgvector (2000) e suficiente para retrieval
ALTER TABLE catalog_items
    ADD COLUMN IF NOT EXISTS embedding vector(1536);

-- 4. Índice HNSW para busca por similaridade de cosseno
--    m=16 e ef_construction=64 são bons defaults para catálogos de até ~100k itens
CREATE INDEX IF NOT EXISTS idx_catalog_items_embedding
    ON catalog_items USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);

-- 5. Índice parcial para backfill periódico (itens ainda sem embedding)
CREATE INDEX IF NOT EXISTS idx_catalog_items_no_embedding
    ON catalog_items (updated_at DESC)
    WHERE embedding IS NULL AND deleted_at IS NULL;

-- 6. Tabela de jornadas do cidadão
--    Grafo curado editorialmente: serviço A frequentemente leva ao serviço B.
--    from_source/to_source são TEXT (não enum) para facilitar manutenção editorial.
CREATE TABLE IF NOT EXISTS catalog_item_journeys (
    id               SERIAL PRIMARY KEY,
    from_external_id TEXT    NOT NULL,
    from_source      TEXT    NOT NULL,
    to_external_id   TEXT    NOT NULL,
    to_source        TEXT    NOT NULL,
    journey_type     TEXT    NOT NULL DEFAULT 'related',  -- related | sequence | prerequisite | alternative
    weight           FLOAT   NOT NULL DEFAULT 1.0,
    created_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_journey UNIQUE (from_external_id, from_source, to_external_id, to_source)
);

CREATE INDEX IF NOT EXISTS idx_journeys_from
    ON catalog_item_journeys (from_external_id, from_source);

-- 7. Seed inicial de jornadas (slugs do Typesense — prefrio_services_base)
--    Atualize os slugs conforme os valores reais da coleção.
INSERT INTO catalog_item_journeys
    (from_external_id, from_source, to_external_id, to_source, journey_type, weight)
VALUES
    -- Nascimento / Maternidade
    ('atendimento-em-maternidades',    'typesense', 'cartao-do-recem-nascido',             'typesense', 'sequence',     1.0),
    ('atendimento-em-maternidades',    'typesense', 'vacinacao-infantil',                   'typesense', 'sequence',     1.0),
    ('registro-civil-nascimento',      'typesense', 'cartao-sus',                           'typesense', 'sequence',     0.9),
    ('registro-civil-nascimento',      'typesense', 'bolsa-familia',                        'typesense', 'related',      0.8),

    -- Assistência Social / Renda
    ('bolsa-familia',                  'typesense', 'cadastro-unico',                       'typesense', 'prerequisite', 1.0),
    ('bpc-loas',                       'typesense', 'cadastro-unico',                       'typesense', 'prerequisite', 1.0),
    ('bpc-loas',                       'typesense', 'passe-livre-idoso',                    'typesense', 'related',      0.8),
    ('cadastro-unico',                 'typesense', 'bolsa-familia',                        'typesense', 'related',      0.9),
    ('cadastro-unico',                 'typesense', 'bpc-loas',                             'typesense', 'related',      0.8),
    ('cadastro-unico',                 'typesense', 'minha-casa-minha-vida',                'typesense', 'related',      0.7),

    -- Saúde
    ('unidade-basica-de-saude',        'typesense', 'cartao-sus',                           'typesense', 'prerequisite', 1.0),
    ('vacinacao',                      'typesense', 'cartao-sus',                           'typesense', 'prerequisite', 0.9),
    ('caps',                           'typesense', 'cras',                                 'typesense', 'related',      0.7),

    -- Habitação
    ('minha-casa-minha-vida',          'typesense', 'cadastro-unico',                       'typesense', 'prerequisite', 1.0),
    ('minha-casa-minha-vida',          'typesense', 'regularizacao-fundiaria',              'typesense', 'related',      0.7),

    -- Emprego e qualificação
    ('sine-emprego',                   'typesense', 'requalificacao-profissional',           'typesense', 'related',      0.9),
    ('sine-emprego',                   'typesense', 'seguro-desemprego',                    'typesense', 'related',      0.8),
    ('microempreendedor-individual',   'typesense', 'mei-registro',                         'typesense', 'related',      1.0),
    ('microempreendedor-individual',   'typesense', 'sine-emprego',                         'typesense', 'alternative',  0.6),

    -- Idosos
    ('passe-livre-idoso',              'typesense', 'bpc-loas',                             'typesense', 'related',      0.7),
    ('passe-livre-idoso',              'typesense', 'unidade-basica-de-saude',              'typesense', 'related',      0.6),

    -- Animais de companhia
    ('cadastro-animal-sisbicho',       'typesense', 'castracao-animal',                     'typesense', 'sequence',     1.0),
    ('cadastro-animal-sisbicho',       'typesense', 'atendimento-clinico-animal',           'typesense', 'related',      0.8),

    -- Documentação / Identidade
    ('segunda-via-rg',                 'typesense', 'cpf-cadastro',                         'typesense', 'related',      0.7),
    ('carteira-de-trabalho',           'typesense', 'sine-emprego',                         'typesense', 'sequence',     0.9)
ON CONFLICT (from_external_id, from_source, to_external_id, to_source) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS catalog_item_journeys;
DROP INDEX IF EXISTS idx_catalog_items_no_embedding;
DROP INDEX IF EXISTS idx_catalog_items_embedding;
ALTER TABLE catalog_items DROP COLUMN IF EXISTS embedding;
