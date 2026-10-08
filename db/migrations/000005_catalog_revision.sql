-- +goose Up
-- NO-OP intencional. Parte do hardening experimental de busca (000003–000007), totalmente revertido pela 000008.
-- O staging já aplicou 000003–000008 (versão do goose = 9 após a 000009); o prod está
-- na versão 2 e deve seguir direto para a 000009, sem criar/remover esse schema.
-- Os nomes/versões são mantidos para não divergir o histórico do goose no staging.
-- O conteúdo original está no histórico do git (commit 666bee2).
SELECT 1;

-- +goose Down
SELECT 1;
