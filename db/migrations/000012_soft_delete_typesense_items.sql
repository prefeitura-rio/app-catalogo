-- +goose Up
-- Retira da busca/recomendação os serviços ingeridos via Typesense
-- (fonte temporária da Carta), sem apagar o histórico físico.
UPDATE catalog_items
SET
    deleted_at = NOW(),
    status = 'inactive',
    updated_at = NOW()
WHERE source = 'typesense'
  AND deleted_at IS NULL;

-- +goose Down
UPDATE catalog_items
SET
    deleted_at = NULL,
    status = 'active',
    updated_at = NOW()
WHERE source = 'typesense'
  AND deleted_at IS NOT NULL
  AND status = 'inactive';
