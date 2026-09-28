-- +goose Up
-- Preserve the editorial explanation carried by the retired Facilita Rio
-- journey graph. Retrieval continues to resolve the endpoints against the
-- authoritative catalog at request time, so stale targets never leak.
ALTER TABLE catalog_item_journeys
    ADD COLUMN reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN theme TEXT NOT NULL DEFAULT '',
    ADD COLUMN migration_origin TEXT NOT NULL DEFAULT '';

CREATE CONSTRAINT TRIGGER trg_catalog_item_journeys_revision
    AFTER INSERT OR UPDATE OR DELETE ON catalog_item_journeys
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    EXECUTE FUNCTION bump_catalog_revision();

WITH facilita_journeys (
    from_external_id, from_source, to_external_id, to_source,
    journey_type, weight, reason, theme
) AS (
VALUES
    ('atendimento-em-maternidades-cffe0736', 'typesense', 'distribuicao-de-kit-enxoval-do-bebe-77f09458', 'typesense', 'sequence', 1.0, 'kit enxoval', 'jornada gestante'),
    ('atendimento-em-maternidades-cffe0736', 'typesense', 'informacoes-sobre-o-programa-bolsa-familia-4547c2ba', 'typesense', 'related', 0.9, 'apoio financeiro', 'jornada gestante'),
    ('atendimento-em-maternidades-cffe0736', 'typesense', 'informacoes-sobre-vacinacao-humana-728a6848', 'typesense', 'sequence', 0.9, 'vacinação do bebê', 'jornada gestante'),
    ('distribuicao-de-kit-enxoval-do-bebe-77f09458', 'typesense', 'atendimento-em-maternidades-cffe0736', 'typesense', 'prerequisite', 1.0, 'maternidade', 'jornada gestante'),
    ('distribuicao-de-kit-enxoval-do-bebe-77f09458', 'typesense', 'informacoes-sobre-o-programa-bolsa-familia-4547c2ba', 'typesense', 'related', 0.9, 'apoio financeiro', 'jornada gestante'),
    ('emissao-de-2-via-do-iptu-ce2b748c', 'typesense', 'iptu-consulta-a-pagamentos-e-debito-automatico-b175364b', 'typesense', 'sequence', 1.0, 'consultar débitos', 'jornada tributária'),
    ('emissao-de-2-via-do-iptu-ce2b748c', 'typesense', 'parcelamento-de-debitos-em-divida-ativa-6ba1f0f4', 'typesense', 'related', 0.9, 'parcelar dívida', 'jornada tributária'),
    ('emissao-de-2-via-do-iptu-ce2b748c', 'typesense', 'certidao-negativa-de-debito-nada-consta-439306e1', 'typesense', 'sequence', 0.8, 'certidão negativa', 'jornada tributária'),
    ('iptu-consulta-a-pagamentos-e-debito-automatico-b175364b', 'typesense', 'emissao-de-2-via-do-iptu-ce2b748c', 'typesense', 'sequence', 1.0, 'emitir boleto', 'jornada tributária'),
    ('iptu-consulta-a-pagamentos-e-debito-automatico-b175364b', 'typesense', 'parcelamento-de-debitos-em-divida-ativa-6ba1f0f4', 'typesense', 'related', 0.9, 'parcelar dívida', 'jornada tributária'),
    ('parcelamento-de-debitos-em-divida-ativa-6ba1f0f4', 'typesense', 'certidao-negativa-de-debito-nada-consta-439306e1', 'typesense', 'sequence', 1.0, 'certidão negativa', 'jornada tributária'),
    ('parcelamento-de-debitos-em-divida-ativa-6ba1f0f4', 'typesense', 'iptu-consulta-a-pagamentos-e-debito-automatico-b175364b', 'typesense', 'related', 0.9, 'consultar pagamentos', 'jornada tributária'),
    ('certidao-de-habite-se-aceitacao-df83d300', 'typesense', 'informacoes-sobre-cadastro-no-programa-minha-casa-401628a4', 'typesense', 'related', 0.9, 'programa habitacional', 'jornada imóvel'),
    ('certidao-de-habite-se-aceitacao-df83d300', 'typesense', 'certidao-negativa-de-debito-nada-consta-439306e1', 'typesense', 'sequence', 0.8, 'certidão negativa', 'jornada imóvel'),
    ('atendimento-clinico-em-animais-8c9a32e8', 'typesense', 'castracao-gratuita-de-caes-e-gatos-programa-bicho-797d5e5f', 'typesense', 'related', 1.0, 'castração', 'jornada animal'),
    ('atendimento-clinico-em-animais-8c9a32e8', 'typesense', 'cadastro-de-animais-no-sisbicho-b5ad2d27', 'typesense', 'prerequisite', 0.9, 'cadastro SISBICHO', 'jornada animal'),
    ('castracao-gratuita-de-caes-e-gatos-programa-bicho-797d5e5f', 'typesense', 'cadastro-de-animais-no-sisbicho-b5ad2d27', 'typesense', 'prerequisite', 1.0, 'cadastro SISBICHO', 'jornada animal'),
    ('castracao-gratuita-de-caes-e-gatos-programa-bicho-797d5e5f', 'typesense', 'atendimento-clinico-em-animais-8c9a32e8', 'typesense', 'related', 0.9, 'atendimento clínico', 'jornada animal'),
    ('informacoes-sobre-matricula-na-rede-municipal-2026-6c635361', 'typesense', 'informacoes-sobre-merenda-escolar-146237e8', 'typesense', 'sequence', 1.0, 'merenda', 'jornada escolar'),
    ('informacoes-sobre-matricula-na-rede-municipal-2026-6c635361', 'typesense', 'inclusao-de-aluno-para-acompanhamento-escolar-b1ed4c9e', 'typesense', 'related', 0.9, 'acompanhamento', 'jornada escolar'),
    ('atendimento-em-unidades-de-atencao-primaria-em-2f6e4910', 'typesense', 'atendimento-em-unidades-de-pronto-atendimento-upa-362ec1a2', 'typesense', 'alternative', 1.0, 'UPA', 'jornada saúde'),
    ('atendimento-em-unidades-de-atencao-primaria-em-2f6e4910', 'typesense', 'informacoes-sobre-vacinacao-humana-728a6848', 'typesense', 'related', 0.9, 'vacinação', 'jornada saúde'),
    ('atendimento-em-unidades-de-atencao-primaria-em-2f6e4910', 'typesense', 'distribuicao-de-insumos-para-tratamento-de-7e3ea1a4', 'typesense', 'related', 0.8, 'insumos', 'jornada saúde'),
    ('consulta-e-encaminhamento-para-vagas-de-emprego-a8a12ae6', 'typesense', 'inclusao-de-pessoas-com-deficiencia-no-mercado-de-2b6c31d8', 'typesense', 'related', 1.0, 'vagas para pessoas com deficiência', 'jornada emprego'),
    ('consulta-e-encaminhamento-para-vagas-de-emprego-a8a12ae6', 'typesense', 'informacoes-sobre-educacao-de-jovens-e-adultos-eja-901bf85b', 'typesense', 'prerequisite', 0.9, 'qualificação EJA', 'jornada emprego'),
    ('informacoes-sobre-educacao-de-jovens-e-adultos-eja-901bf85b', 'typesense', 'consulta-e-encaminhamento-para-vagas-de-emprego-a8a12ae6', 'typesense', 'sequence', 1.0, 'vagas após qualificação', 'jornada emprego'),
    ('vistoria-em-foco-de-aedes-aegypti-dengue-d2b9b06d', 'typesense', 'atendimento-em-unidades-de-pronto-atendimento-upa-362ec1a2', 'typesense', 'sequence', 1.0, 'UPA para casos graves', 'jornada saúde'),
    ('vistoria-em-foco-de-aedes-aegypti-dengue-d2b9b06d', 'typesense', 'atendimento-em-unidades-de-atencao-primaria-em-2f6e4910', 'typesense', 'related', 0.9, 'atenção primária', 'jornada saúde'),
    ('cadastro-para-acesso-as-cozinhas-comunitarias-042e8b69', 'typesense', 'informacoes-sobre-o-programa-bolsa-familia-4547c2ba', 'typesense', 'related', 1.0, 'apoio financeiro', 'jornada acolhimento'),
    ('cadastro-para-acesso-as-cozinhas-comunitarias-042e8b69', 'typesense', 'informacoes-sobre-acoes-de-acolhimento-a-pessoas-8aaba05a', 'typesense', 'related', 0.9, 'outros serviços sociais', 'jornada acolhimento'),
    ('atendimento-para-pessoas-vitimas-de-violencia-2edbfc24', 'typesense', 'informacoes-sobre-acoes-de-acolhimento-a-pessoas-8aaba05a', 'typesense', 'related', 1.0, 'acolhimento social', 'jornada acolhimento'),
    ('informacoes-sobre-acoes-de-acolhimento-a-pessoas-8aaba05a', 'typesense', 'cadastro-para-acesso-as-cozinhas-comunitarias-042e8b69', 'typesense', 'sequence', 1.0, 'alimentação', 'jornada acolhimento'),
    ('informacoes-sobre-acoes-de-acolhimento-a-pessoas-8aaba05a', 'typesense', 'consulta-e-encaminhamento-para-vagas-de-emprego-a8a12ae6', 'typesense', 'sequence', 0.9, 'emprego', 'jornada acolhimento')
)
INSERT INTO catalog_item_journeys (
    from_external_id, from_source, to_external_id, to_source,
    journey_type, weight, reason, theme, migration_origin
)
SELECT
    from_external_id, from_source, to_external_id, to_source,
    journey_type, weight, reason, theme, 'facilita-retirement-v1'
FROM facilita_journeys
ON CONFLICT (from_external_id, from_source, to_external_id, to_source) DO NOTHING;

-- +goose Down
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

SET CONSTRAINTS trg_catalog_item_journeys_revision IMMEDIATE;
DROP TRIGGER IF EXISTS trg_catalog_item_journeys_revision ON catalog_item_journeys;

ALTER TABLE catalog_item_journeys
    DROP COLUMN migration_origin,
    DROP COLUMN theme,
    DROP COLUMN reason;
