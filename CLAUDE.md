# CLAUDE.md — app-catalogo

Busca global e recomendação inteligente da Prefeitura Rio. Discovery layer unificada que indexa serviços públicos (Carta de Serviços via SalesForce), cursos, vagas de emprego e oportunidades MEI.

## Stack

- **Go 1.25** com Gin (HTTP), pgx/v5 (PostgreSQL sem ORM), zerolog (logs), Viper (config)
- **PostgreSQL 16** + extensões `pgvector`, `pg_trgm`, `unaccent` — busca FTS + vetores semânticos
- **Redis** — L2 cache for search and authenticated/anonymous recommendation results; rate limiting uses a separate client pool
- **OpenTelemetry** → SigNoz | Prometheus
- **just** como task runner | **Nix** para ambiente reproduzível | **Docker** + **Kubernetes**

## Estrutura

```
cmd/
  api/main.go        ← servidor HTTP (porta 8080)
  worker/main.go     ← scheduler de datasources e backfill de embeddings
  migrate/main.go    ← goose migrations
internal/
  config/            ← Viper singleton (padrão app-go-api)
  api/               ← router, middleware, handlers
  services/          ← search, recommendation, citizen_profile, salesforce_sync
  repository/        ← catalog_item, citizen_profile, search (pgx/v5 direto)
  models/            ← catalog_item, citizen_profile, recommendation, search
  clients/           ← rmi, appgoapi, salesforce, typesense, Keycloak, Gemini e reranker
  datasource/        ← adapters e scheduler de sincronização
  cache/             ← Redis TTL cache genérico
  db/                ← pool pgx/v5
  observability/     ← OTel tracing + Prometheus metrics
db/migrations/       ← migrations SQL combinadas com seções Goose Up e Down
k8s/
  staging/           ← Argo Rollout blue-green, worker Deployment, KEDA e Services
  prod/              ← Argo Rollout canary, worker Deployment, KEDA e Services
```

## Fontes de Dados

| Fonte | Entidades | Estratégia |
|-------|-----------|-----------|
| Typesense | Carta de Serviços (**temporário**) | Deltas inclusivos e exportações completas periódicas |
| SalesForce | Carta de Serviços | Snapshot completo inicial e periódico, deltas entre snapshots e webhooks HMAC-SHA256 |
| app-go-api | Cursos, Vagas, MEI | Snapshot completo periódico e independente por vertical |
| app-rmi | Perfil do cidadão | Demand-driven + background refresh |

## Autenticação

Istio injeta o token do cidadão em `X-Auth-Request-Token`, e o serviço verifica novamente a assinatura RS256 contra o JWKS configurado antes de confiar nas claims. Issuer, audience, validade temporal e authorized party opcional também são validados; um token presente e inválido retorna `401`.

`preferred_username` contém o CPF do cidadão. CPF **nunca** persiste em texto — apenas `cpf_hash` (`HMAC-SHA256(CPF_HASH_SALT, CPF)`).

## API

```
GET  /api/v1/search               ← transporte autenticado por query string
POST /api/v1/search               ← mesmo pipeline autenticado via JSON
GET  /api/public/search           ← transporte público por query string
POST /api/public/search           ← mesmo pipeline público via JSON
GET  /api/v1/recommendations      ← recomendação personalizada autenticada
GET  /api/public/recommendations  ← recomendação anônima com scoring neutro
GET  /api/v1/catalog/:id
GET  /api/public/catalog/:id
GET  /api/v1/admin/sync/status
POST /api/v1/admin/sync/trigger
POST /api/webhooks/salesforce ← HMAC-SHA256 auth própria
GET  /health | /ready | /metrics
```

## Comandos

```bash
just up       # infra local (postgres + redis)
just migrate  # rodar migrations
just dev      # servidor em dev mode
just fmt      # formatar
just lint     # linting
just build    # compilar binários
just test     # testes
```

## Migrations

Usar Goose. Cada migration vive em `db/migrations/NNNNNN_nome.sql` e contém seções `-- +goose Up` e `-- +goose Down`.

```bash
just migrate-create nome_da_migration
```

## Princípios

- Sem GORM — queries SQL diretas com pgx/v5 (scoring/ranking requer SQL complexo)
- CPF hash-only — `HMAC-SHA256(CPF_HASH_SALT, CPF)` antes de qualquer persistência
- Webhook SalesForce: validar HMAC-SHA256 antes de processar qualquer payload

## Branch Strategy

```
feat/* ou fix/* → PR para main → merge em main → deploy automático para staging
GitHub Release  → deploy para produção
```

- Sempre criar branches a partir de **main**
- PR direto para `main` (não há branch `staging`)
- Merge em `main` dispara `deploy-staging.yaml` automaticamente
- Nunca commitar direto em `main` — PRs obrigatórios
