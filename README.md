# app-catalogo

Discovery layer unificada da Prefeitura do Rio de Janeiro. Indexa e expõe serviços públicos (Carta de Serviços via SalesForce), cursos, vagas de emprego e oportunidades MEI com busca full-text e recomendação personalizada por perfil de cidadão.

## Stack

- **Go 1.25** — Gin (HTTP), pgx/v5 (PostgreSQL), zerolog, Viper
- **PostgreSQL 16** — `pgvector`, `pg_trgm`, `unaccent` para FTS (`websearch_to_tsquery` + `ts_rank`)
- **Redis 7** — cache L2 (resultados de busca, perfis)
- **OpenTelemetry** → SigNoz | Prometheus
- **just** · **Nix** · **Docker** · **Kubernetes**

## Pré-requisitos

- [Nix](https://nixos.org/) com flakes habilitado (ambiente reproduzível via `flake.nix`)
- Docker + Docker Compose
- [`just`](https://github.com/casey/just)
- [`air`](https://github.com/air-verse/air) para hot reload em dev

## Início Rápido

```bash
# Copie e preencha as variáveis de ambiente
cp .env.example .env

# Suba a infra local (Postgres + Redis + Adminer)
just up

# Rode as migrations
just migrate

# Inicie API + worker com hot reload
just dev
```

A API fica disponível em `http://localhost:8080`.  
O Adminer (UI do banco) em `http://localhost:8083`.

## Variáveis de Ambiente

| Variável | Descrição |
|----------|-----------|
| `DB_HOST` / `DB_PORT` / `DB_USER` / `DB_PASSWORD` / `DB_NAME` | Conexão PostgreSQL |
| `REDIS_ADDR` | Endereço Redis (ex: `localhost:6380`) |
| `CPF_HASH_SALT` | Salt para SHA-256 do CPF — **obrigatório** |
| `SALESFORCE_INSTANCE_URL` / `SALESFORCE_CLIENT_ID` / `SALESFORCE_CLIENT_SECRET` | Credenciais Carta de Serviços |
| `SALESFORCE_WEBHOOK_SECRET` | Segredo HMAC-SHA256 para validar webhooks |
| `APPGOAPI_BASE_URL` / `APPGOAPI_SYNC_ENABLED` | URL base e flag de sync do app-go-api |
| `APP_RMI_URL` | URL base do app-rmi (perfil do cidadão) |
| `KEYCLOAK_URL` / `KEYCLOAK_REALM` / `KEYCLOAK_CLIENT_ID` / `KEYCLOAK_CLIENT_SECRET` | Auth para chamadas ao app-rmi e app-go-api |
| `AUTH_JWT_ISSUER` / `AUTH_JWKS_URL` | Emissor e JWKS para verificar o JWT de `X-Auth-Request-Token` (padrão derivado de `KEYCLOAK_URL`/`KEYCLOAK_REALM`; ver [Autenticação](#autenticação)) |
| `AUTH_JWT_AUTHORIZED_PARTIES` | Clients do Keycloak (claim `azp`) aceitos, separados por vírgula (padrão `superapp`; ver [Autenticação](#autenticação)) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Endpoint SigNoz/OTel (opcional) |
| `WORKER_METRICS_PORT` | Porta em que o worker expõe `GET /metrics` com as métricas de sync (padrão `9091`; `0` desativa) |

## Comandos

```bash
just up              # inicia infra local
just down            # para infra local
just dev             # API + worker com hot reload
just dev-api         # só a API com hot reload
just dev-worker      # só o worker com hot reload
just migrate         # aplica migrations pendentes
just migrate-create  # cria nova migration (ex: just migrate-create add_index)
just fmt             # formata código
just lint            # linting (golangci-lint)
just build           # compila os três binários em bin/
just test            # testes com race detector + cobertura
just db              # shell psql
just reset           # DESTRUTIVO: apaga tudo e reinicia do zero
```

## API

### Busca e recomendação

```
POST /api/v1/search              busca (requer X-Auth-Request-Token; mesmo ranking que public por ora)
POST /api/public/search          busca por relevância FTS (sem auth)
GET  /api/v1/recommendations     recomendações personalizadas por perfil — requer auth
GET  /api/public/recommendations recomendações com scoring neutro (sem auth)
GET  /api/v1/catalog/:id         detalhe de um item do catálogo
GET  /api/public/catalog/:id     idem, sem auth
```

### Admin

```
GET  /api/v1/admin/sync/status        status dos datasources (requer role admin)
POST /api/v1/admin/sync/trigger       dispara sync ad-hoc; ?source= para fonte específica (requer role admin)
```

### Webhooks e infra

```
POST /api/webhooks/salesforce    recebe atualizações da Carta de Serviços (HMAC-SHA256)
GET  /health                     liveness probe
GET  /ready                      readiness probe (pinga o banco)
GET  /metrics                    métricas Prometheus
```

> Rate limiting: 300 req/min por IP, em memória. Para múltiplas réplicas é necessário migrar para Redis.

## Fontes de Dados

| Fonte | Entidades | Estratégia |
|-------|-----------|------------|
| Typesense | Carta de Serviços (**temporário**) | Delta sync por `last_update` (unix) a cada 30min; full sync na 1ª execução. Ativo enquanto a migração para o SalesForce não é concluída |
| SalesForce | Carta de Serviços (**futuro**) | Delta sync por `LastModifiedDate` + webhooks HMAC-SHA256; em migração a partir do Typesense |
| app-go-api | Cursos, Vagas, MEI | Full sync paginado (delta via `updated_since` ainda não ativado) |
| app-rmi | Perfil do cidadão | Demand-driven: busca síncrona no primeiro acesso; refresh em background quando stale |

## Autenticação

O JWT do cidadão chega no header `X-Auth-Request-Token`. O header pode vir direto do cliente, então o serviço não confia na borda: verifica o token antes de criar qualquer identidade ou papel (`internal/api/middleware`).

- Assinatura conferida com as chaves públicas do JWKS do Keycloak (cache em memória), só com algoritmos assimétricos (RS*, PS*, ES*); `none` e HS* são recusados.
- Claims obrigatórias: `iss` igual a `AUTH_JWT_ISSUER`, `exp` presente e válido, `iat` coerente (tolerância de 30s) e `azp` igual a um dos clients de `AUTH_JWT_AUTHORIZED_PARTIES` (comparação exata). Token sem `azp` ou emitido para outro client do realm não autentica.
- `GET /api/v1/search`, `/api/v1/recommendations` e `/api/v1/catalog/:id` exigem token válido com CPF válido em `preferred_username`; caso contrário, 401.
- `/api/v1/admin/*` exige token válido (sem ele, 401) com papel `ADMIN` (sem ele, 403); o CPF não é exigido nessas rotas.
- Sem emissor/JWKS configurados, ou com `AUTH_JWT_AUTHORIZED_PARTIES` sem nenhum client (ex.: `,`), nenhum token é aceito (falha fechada).
- `preferred_username` contém o CPF do cidadão. O papel `ADMIN` vem das roles `admin` ou `go:admin` em `resource_access.superapp.roles`.
- `aud` não é conferido; a restrição ao client é feita pelo `azp`.

| Variável | Padrão |
|----------|--------|
| `AUTH_JWT_ISSUER` | `KEYCLOAK_URL` + `/realms/` + `KEYCLOAK_REALM` |
| `AUTH_JWKS_URL` | `<AUTH_JWT_ISSUER>/protocol/openid-connect/certs` |
| `AUTH_JWT_AUTHORIZED_PARTIES` | `superapp` |

CPF **nunca** é persistido — apenas `cpf_hash` (`SHA-256(CPF + CPF_HASH_SALT)`). O CPF em texto vai apenas para a chamada ao app-rmi, em memória.

Chamadas de serviço ao app-rmi e app-go-api usam `client_credentials` Keycloak via `KeycloakTokenManager` (renovação automática 30s antes da expiração).

## Recomendações

O scoring de recomendação usa 6 dimensões com pesos configurados:

| Dimensão | Peso |
|----------|------|
| Escolaridade | 0,25 |
| Renda familiar | 0,20 |
| Localização | 0,20 |
| Acessibilidade/PCD | 0,15 |
| Faixa etária | 0,10 |
| Tipo de item no contexto | 0,10 |

Recomendações anônimas usam os mesmos pesos com valores neutros.

## Estrutura

```
cmd/
  api/main.go          servidor HTTP (porta 8080)
  worker/main.go       scheduler de datasources
  migrate/main.go      runner de migrations (goose)
internal/
  api/                 router, middlewares, handlers
  services/            search, recommendation, citizen_profile, salesforce_sync
  repository/          catalog_item, citizen_profile, search (pgx/v5 direto)
  models/              catalog_item, citizen_profile, recommendation, search
  clients/             salesforce, appgoapi, rmi, keycloak_token_manager
  datasource/          interface DataSource + Manager (scheduler) + adaptadores por fonte
  cache/               Redis TTL cache genérico
  db/                  pool pgx/v5
  config/              Viper singleton
  observability/       OTel tracing + Prometheus metrics + rate limiting
db/migrations/         goose SQL migrations (.up.sql / .down.sql)
k8s/
  staging/             Deployment + KEDA + Service
  prod/
```

## Migrations

```bash
# Criar nova migration
just migrate-create nome_da_migration

# Aplicar
just migrate
```

Migrations ficam em `db/migrations/NNNNNN_nome.{up,down}.sql` (goose).

O `search_vector` é uma coluna `TSVECTOR GENERATED ALWAYS AS STORED` — calculada automaticamente pelo PostgreSQL a partir de `title`, `short_desc`, `description` e `tags` usando `unaccent` + dicionário `portuguese`.

## Deploy

Manifests Kubernetes em `k8s/staging/` e `k8s/prod/`. O projeto usa KEDA para autoscaling.

Fluxo de branches:
- `feat/*` / `fix/*` → PR para `main` (sempre a partir de `main`)
- Merge em `main` → deploy automático para staging
- GitHub Release → deploy para produção
- Nunca commitar direto em `main`
