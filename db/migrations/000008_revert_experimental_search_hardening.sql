-- +goose Up
-- Desfaz o hardening experimental de busca (migrations 000003–000007 / wllsena).
-- Ordem inversa: 000007 → 000006 → 000005 → 000004 → 000003.
-- Idempotente o suficiente para staging onde parte do schema pode já ter sido removida.

-- === 000007_service_intelligence (Down) ===
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'catalog_item_journeys'
          AND column_name = 'migration_origin'
    ) THEN
        DELETE FROM catalog_item_journeys
        WHERE migration_origin = 'facilita-retirement-v1'
          AND (from_external_id, from_source, to_external_id, to_source) IN (
            ('atendimento-em-maternidades-cffe0736', 'typesense', 'distribuicao-de-kit-enxoval-do-bebe-77f09458', 'typesense'),
            ('atendimento-em-maternidades-cffe0736', 'typesense', 'informacoes-sobre-o-programa-bolsa-familia-4547c2ba', 'typesense'),
            ('atendimento-em-maternidades-cffe0736', 'typesense', 'informacoes-sobre-vacinacao-humana-728a6848', 'typesense'),
            ('distribuicao-de-kit-enxoval-do-bebe-77f09458', 'typesense', 'atendimento-em-maternidades-cffe0736', 'typesense'),
            ('distribuicao-de-kit-enxoval-do-bebe-77f09458', 'typesense', 'informacoes-sobre-o-programa-bolsa-familia-4547c2ba', 'typesense'),
            ('emissao-de-2-via-do-iptu-ce2b748c', 'typesense', 'iptu-consulta-a-pagamentos-e-debito-automatico-b175364b', 'typesense'),
            ('emissao-de-2-via-do-iptu-ce2b748c', 'typesense', 'parcelamento-de-debitos-em-divida-ativa-6ba1f0f4', 'typesense'),
            ('emissao-de-2-via-do-iptu-ce2b748c', 'typesense', 'certidao-negativa-de-debito-nada-consta-439306e1', 'typesense'),
            ('iptu-consulta-a-pagamentos-e-debito-automatico-b175364b', 'typesense', 'emissao-de-2-via-do-iptu-ce2b748c', 'typesense'),
            ('iptu-consulta-a-pagamentos-e-debito-automatico-b175364b', 'typesense', 'parcelamento-de-debitos-em-divida-ativa-6ba1f0f4', 'typesense'),
            ('parcelamento-de-debitos-em-divida-ativa-6ba1f0f4', 'typesense', 'certidao-negativa-de-debito-nada-consta-439306e1', 'typesense'),
            ('parcelamento-de-debitos-em-divida-ativa-6ba1f0f4', 'typesense', 'iptu-consulta-a-pagamentos-e-debito-automatico-b175364b', 'typesense'),
            ('certidao-de-habite-se-aceitacao-df83d300', 'typesense', 'informacoes-sobre-cadastro-no-programa-minha-casa-401628a4', 'typesense'),
            ('certidao-de-habite-se-aceitacao-df83d300', 'typesense', 'certidao-negativa-de-debito-nada-consta-439306e1', 'typesense'),
            ('atendimento-clinico-em-animais-8c9a32e8', 'typesense', 'castracao-gratuita-de-caes-e-gatos-programa-bicho-797d5e5f', 'typesense'),
            ('atendimento-clinico-em-animais-8c9a32e8', 'typesense', 'cadastro-de-animais-no-sisbicho-b5ad2d27', 'typesense'),
            ('castracao-gratuita-de-caes-e-gatos-programa-bicho-797d5e5f', 'typesense', 'cadastro-de-animais-no-sisbicho-b5ad2d27', 'typesense'),
            ('castracao-gratuita-de-caes-e-gatos-programa-bicho-797d5e5f', 'typesense', 'atendimento-clinico-em-animais-8c9a32e8', 'typesense'),
            ('informacoes-sobre-matricula-na-rede-municipal-2026-6c635361', 'typesense', 'informacoes-sobre-merenda-escolar-146237e8', 'typesense'),
            ('informacoes-sobre-matricula-na-rede-municipal-2026-6c635361', 'typesense', 'inclusao-de-aluno-para-acompanhamento-escolar-b1ed4c9e', 'typesense'),
            ('atendimento-em-unidades-de-atencao-primaria-em-2f6e4910', 'typesense', 'atendimento-em-unidades-de-pronto-atendimento-upa-362ec1a2', 'typesense'),
            ('atendimento-em-unidades-de-atencao-primaria-em-2f6e4910', 'typesense', 'informacoes-sobre-vacinacao-humana-728a6848', 'typesense'),
            ('atendimento-em-unidades-de-atencao-primaria-em-2f6e4910', 'typesense', 'distribuicao-de-insumos-para-tratamento-de-7e3ea1a4', 'typesense'),
            ('consulta-e-encaminhamento-para-vagas-de-emprego-a8a12ae6', 'typesense', 'inclusao-de-pessoas-com-deficiencia-no-mercado-de-2b6c31d8', 'typesense'),
            ('consulta-e-encaminhamento-para-vagas-de-emprego-a8a12ae6', 'typesense', 'informacoes-sobre-educacao-de-jovens-e-adultos-eja-901bf85b', 'typesense'),
            ('informacoes-sobre-educacao-de-jovens-e-adultos-eja-901bf85b', 'typesense', 'consulta-e-encaminhamento-para-vagas-de-emprego-a8a12ae6', 'typesense'),
            ('vistoria-em-foco-de-aedes-aegypti-dengue-d2b9b06d', 'typesense', 'atendimento-em-unidades-de-pronto-atendimento-upa-362ec1a2', 'typesense'),
            ('vistoria-em-foco-de-aedes-aegypti-dengue-d2b9b06d', 'typesense', 'atendimento-em-unidades-de-atencao-primaria-em-2f6e4910', 'typesense'),
            ('cadastro-para-acesso-as-cozinhas-comunitarias-042e8b69', 'typesense', 'informacoes-sobre-o-programa-bolsa-familia-4547c2ba', 'typesense'),
            ('cadastro-para-acesso-as-cozinhas-comunitarias-042e8b69', 'typesense', 'informacoes-sobre-acoes-de-acolhimento-a-pessoas-8aaba05a', 'typesense'),
            ('atendimento-para-pessoas-vitimas-de-violencia-2edbfc24', 'typesense', 'informacoes-sobre-acoes-de-acolhimento-a-pessoas-8aaba05a', 'typesense'),
            ('informacoes-sobre-acoes-de-acolhimento-a-pessoas-8aaba05a', 'typesense', 'cadastro-para-acesso-as-cozinhas-comunitarias-042e8b69', 'typesense'),
            ('informacoes-sobre-acoes-de-acolhimento-a-pessoas-8aaba05a', 'typesense', 'consulta-e-encaminhamento-para-vagas-de-emprego-a8a12ae6', 'typesense')
          );
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    BEGIN
        SET CONSTRAINTS trg_catalog_item_journeys_revision IMMEDIATE;
    EXCEPTION
        WHEN undefined_object THEN
            NULL;
    END;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_catalog_item_journeys_revision ON catalog_item_journeys;

ALTER TABLE catalog_item_journeys
    DROP COLUMN IF EXISTS migration_origin,
    DROP COLUMN IF EXISTS theme,
    DROP COLUMN IF EXISTS reason;

-- === 000006_service_slug_aliases (Down) ===
DROP TABLE IF EXISTS catalog_item_slug_aliases;

-- === 000005_catalog_revision (Down) ===
DROP TRIGGER IF EXISTS trg_catalog_items_revision ON catalog_items;
DROP FUNCTION IF EXISTS bump_catalog_revision();
DROP TABLE IF EXISTS catalog_state;

-- === 000004_search_retrieval_indexes (Down) ===
CREATE INDEX IF NOT EXISTS idx_catalog_items_embedding
    ON catalog_items USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);
DROP INDEX IF EXISTS idx_catalog_items_search_embedding;
DROP INDEX IF EXISTS idx_catalog_items_active_source_slug;
DROP INDEX IF EXISTS idx_catalog_items_active_external_id;
DROP INDEX IF EXISTS idx_catalog_items_active_title_trigram;

-- === 000003_embedding_lifecycle_metadata (Down) ===
DROP TRIGGER IF EXISTS trg_catalog_items_invalidate_embedding ON catalog_items;
DROP FUNCTION IF EXISTS invalidate_catalog_item_embedding();
DROP INDEX IF EXISTS idx_catalog_items_embedding_work;

DROP TRIGGER IF EXISTS trg_catalog_items_updated_at ON catalog_items;
CREATE TRIGGER trg_catalog_items_updated_at
    BEFORE UPDATE ON catalog_items
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

ALTER TABLE catalog_items
    DROP CONSTRAINT IF EXISTS catalog_items_embedding_claim_consistent,
    DROP CONSTRAINT IF EXISTS catalog_items_embedding_dimension_matches,
    DROP CONSTRAINT IF EXISTS catalog_items_embedding_metadata_consistent,
    DROP COLUMN IF EXISTS embedding_claimed_at,
    DROP COLUMN IF EXISTS embedding_claim_token,
    DROP COLUMN IF EXISTS embedding_generated_at,
    DROP COLUMN IF EXISTS embedding_source_hash,
    DROP COLUMN IF EXISTS embedding_document_version,
    DROP COLUMN IF EXISTS embedding_task_type,
    DROP COLUMN IF EXISTS embedding_dimensions,
    DROP COLUMN IF EXISTS embedding_model_version,
    DROP COLUMN IF EXISTS embedding_model;

-- +goose Down
-- Não recria o hardening experimental. Para voltar ao estado 000007,
-- faça goose down até a versão 2 e goose up (reaplica 000003–000007).
SELECT 1;
