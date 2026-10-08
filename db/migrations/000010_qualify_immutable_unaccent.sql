-- +goose Up
-- Qualifica public.unaccent e fixa o search_path em immutable_unaccent (hardening que o
-- staging recebeu via 000004 original). Como 000003–000008 agora são no-op, esta migration
-- leva o prod ao mesmo estado final do staging. Idempotente: no staging é um no-op efetivo.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION immutable_unaccent(text)
RETURNS text AS $$
  SELECT public.unaccent($1)
$$ LANGUAGE sql IMMUTABLE PARALLEL SAFE
SET search_path = public, pg_temp;
-- +goose StatementEnd

-- +goose Down
-- Mantém a função qualificada: reverter para a versão sem search_path reabriria o hardening.
SELECT 1;
