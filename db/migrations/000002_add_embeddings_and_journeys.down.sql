-- +goose NO TRANSACTION
DROP TABLE IF EXISTS catalog_item_journeys;
DROP INDEX IF EXISTS idx_catalog_items_no_embedding;
DROP INDEX IF EXISTS idx_catalog_items_embedding;
ALTER TABLE catalog_items DROP COLUMN IF EXISTS embedding;
-- Nota: ALTER TYPE ... DROP VALUE não existe no PostgreSQL.
-- Os valores 'typesense' e 'app-go-api' adicionados ao enum item_source
-- não podem ser revertidos sem recriar o tipo.
