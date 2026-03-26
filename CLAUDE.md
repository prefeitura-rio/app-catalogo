# CLAUDE.md — app-catalogo

Busca global e recomendação inteligente da Prefeitura Rio. Discovery layer unificada que indexa serviços públicos (Carta de Serviços via SalesForce), cursos, vagas de emprego e oportunidades MEI.

## Stack

- **Go 1.24+** com Gin (HTTP), pgx/v5 (PostgreSQL sem ORM), zerolog (logs), Viper (config)
- **PostgreSQL 16** + extensões `pgvector`, `pg_trgm`, `unaccent` — busca FTS + vetores semânticos
- **Redis** — cache L2 (search results, perfis, clusters)
- **OpenTelemetry** → SigNoz | Prometheus
- **just** como task runner | **Nix** para ambiente reproduzível | **Docker** + **Kubernetes**

## Estrutura

```
cmd/
  api/main.go        ← servidor HTTP (porta 8080)
  worker/main.go     ← workers de sync (SalesForce, app-go-api, perfil cidadão)
  migrate/main.go    ← goose migrations
internal/
  config/            ← Viper singleton (padrão app-go-api)
  api/               ← router, middleware, handlers
  services/          ← search, recommendation, citizen_profile, salesforce_sync
  repository/        ← catalog_item, citizen_profile, search (pgx/v5 direto)
  models/            ← catalog_item, citizen_profile, recommendation, search
  clients/           ← rmi, appgoapi, salesforce (HTTP clients)
  workers/           ← salesforce, appgoapi, citizen_profile
  cache/             ← Redis TTL cache genérico
  db/                ← pool pgx/v5
  observability/     ← OTel tracing + Prometheus metrics
db/migrations/       ← goose SQL migrations (.up.sql / .down.sql)
k8s/
  staging/           ← Deployment + KEDA + Service
  prod/              ← idem
```

## Fontes de Dados

| Fonte | Entidades | Estratégia |
|-------|-----------|-----------|
| SalesForce | Carta de Serviços | Polling 15min + webhooks HMAC |
| app-go-api | Cursos, Vagas, MEI | Polling HTTP 30min |
| app-rmi | Perfil do cidadão | Demand-driven + background refresh |

## Autenticação

Mesmo padrão do workspace: `Istio valida JWT → injeta X-Auth-Request-Token → serviço decodifica sem re-validar`. `preferred_username` = CPF do cidadão.

CPF **nunca** persiste em texto — apenas `cpf_hash` (SHA-256 + salt via `CPF_HASH_SALT`).

## API

```
POST /api/v1/search          ← busca autenticada (ranking por perfil)
POST /api/public/search      ← busca pública (ranking por popularidade)
GET  /api/v1/recommendations
GET  /api/public/recommendations
GET  /api/v1/catalog/:id
GET  /api/public/catalog/:id
GET  /api/v1/admin/sync/status
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

Usar goose. Cada migration: `db/migrations/NNNNNN_nome.up.sql` + `db/migrations/NNNNNN_nome.down.sql`.

```bash
just migrate-create nome_da_migration
```

## Princípios

- Sem GORM — queries SQL diretas com pgx/v5 (scoring/ranking requer SQL complexo)
- Sem mocking de API — erros reais, não stubs
- CPF hash-only — `SHA-256(CPF + CPF_HASH_SALT)` antes de qualquer persistência
- Webhook SalesForce: validar HMAC-SHA256 antes de processar qualquer payload
