# app-catalogo

Unified discovery layer for Prefeitura do Rio de Janeiro. It indexes public services, courses, jobs, and MEI opportunities with versioned hybrid retrieval and profile-aware recommendations.

## Stack

- **Go 1.25** — Gin (HTTP), pgx/v5 (PostgreSQL), zerolog, Viper
- **PostgreSQL 16** — `pgvector`, `pg_trgm`, and `unaccent` for exact, full-text, fuzzy, and semantic retrieval
- **Redis 7** — L2 cache for search and authenticated/anonymous recommendation results; rate limiting uses a separate client pool
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
| `REDIS_HOST` / `REDIS_PORT` / `REDIS_PASSWORD` / `REDIS_DB` | Redis connection used through independent cache and rate-limit client pools |
| `CACHE_SEARCH_TTL` / `CACHE_RECOMMENDATION_AUTHENTICATED_TTL` / `CACHE_RECOMMENDATION_CLUSTER_TTL` | Strictly positive maximum cache lifetimes; the historical `CLUSTER` name controls anonymous recommendations, and effective lifetimes are shortened to the next catalog eligibility transition |
| `SERVER_HOST` / `SERVER_PORT` / `SERVER_REQUEST_TIMEOUT` | HTTP listener and application request deadline; the timeout accepts a Go duration or legacy integer seconds |
| `SERVER_READ_HEADER_TIMEOUT` / `SERVER_READ_TIMEOUT` / `SERVER_WRITE_TIMEOUT` / `SERVER_SHUTDOWN_TIMEOUT` / `SERVER_IDLE_TIMEOUT` | HTTP transport and graceful-shutdown deadlines expressed as Go durations; shutdown must outlive writes |
| `SERVER_MAX_HEADER_BYTES` | Maximum accepted HTTP request-header size |
| `SERVER_TRUSTED_PROXIES` | Optional comma-separated proxy IPs/CIDRs allowed to supply forwarded client IP headers; empty disables forwarded headers |
| `RATE_LIMIT_REQUESTS` / `RATE_LIMIT_WINDOW` / `RATE_LIMIT_REDIS_TIMEOUT` | Shared fixed-window quota, window, and Redis operation deadline |
| `RATE_LIMIT_KEY_SECRET` | Required secret used only to HMAC client identifiers in rate-limit keys; configure it in the deployment secret store without a default |
| `APP_CATALOGO_INTERNAL_API_KEY` | Required visible-ASCII secret shared only with the SuperApp BFF to authenticate its pseudonymous catalog-search client identity |
| `APP_CATALOGO_INTERNAL_REQUEST_MAX_SKEW` | Bounded acceptance window for signed BFF catalog-search request timestamps |
| `CPF_HASH_SALT` | Required server-side HMAC-SHA256 secret for pseudonymizing CPF values; at least 32 bytes and no default |
| `SALESFORCE_INSTANCE_URL` / `SALESFORCE_CLIENT_ID` / `SALESFORCE_CLIENT_SECRET` | Carta de Serviços credentials; the instance URL must use HTTPS outside loopback development |
| `SALESFORCE_WEBHOOK_SECRET` | Required HMAC-SHA256 secret whenever `SALESFORCE_INSTANCE_URL` enables Salesforce; configure it in the deployment secret store |
| `SALESFORCE_SYNC_INTERVAL` / `SALESFORCE_FULL_SYNC_INTERVAL` | Positive Go durations for incremental polling and periodic complete reconciliation |
| `APP_GO_API_BASE_URL` / `APP_GO_API_SYNC_ENABLED` / `APP_GO_API_SYNC_INTERVAL` | app-go-api base URL, synchronization gate, and full-snapshot interval |
| `RMI_BASE_URL` | app-rmi base URL for citizen profiles |
| `KEYCLOAK_URL` / `KEYCLOAK_REALM` / `KEYCLOAK_CLIENT_ID` / `KEYCLOAK_CLIENT_SECRET` | Service-account authentication for app-rmi and app-go-api; the base URL must use HTTPS outside loopback development |
| `AUTH_JWT_ISSUER` / `AUTH_JWT_JWKS_URL` / `AUTH_JWT_AUDIENCE` | Required canonical HTTPS issuer, Keycloak JWKS endpoint, and app-catalogo audience for user-token verification |
| `AUTH_JWT_AUTHORIZED_PARTY` / `AUTH_JWT_ROLE_CLIENT_ID` | Optional authorized-party constraint and required `resource_access` client whose roles are accepted |
| `AUTH_JWT_CLOCK_SKEW` / `AUTH_JWT_JWKS_CACHE_TTL` / `AUTH_JWT_UNKNOWN_KEY_REFRESH_INTERVAL` / `AUTH_JWT_HTTP_TIMEOUT` | Strictly parsed bounds for registered claims, key rotation, refresh abuse protection, and JWKS transport |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Endpoint SigNoz/OTel (opcional) |
| `GEMINI_API_KEY` | Server-side Gemini key used for query and document embeddings; local shells can provide it through `~/.zshrc` |
| `EMBEDDING_BACKFILL_INTERVAL` / `EMBEDDING_REQUEST_TIMEOUT` | Embedding worker schedule and per-provider-call deadline |
| `RERANKER_URL` / `RERANKER_TIMEOUT` / `SEARCH_RERANKER_VERSION` | Optional trusted cross-encoder sidecar, request timeout, and immutable model/release identifier included in ranker provenance |
| `FACILITA_SEARCH_BASE_URL` / `FACILITA_INTERNAL_API_KEY` / `FACILITA_SEARCH_TIMEOUT` | Optional protected Facilita candidate source, shared secret, and positive request deadline; URL and key must be configured together |
| `SEARCH_RANKER_VERSION` | Human-readable ranker release; responses append a public configuration fingerprint |
| `SEARCH_CANDIDATE_POOL_SIZE` / `SEARCH_SEMANTIC_OVERFETCH_FACTOR` / `SEARCH_SEMANTIC_TIMEOUT` / `SEARCH_MAX_SEMANTIC_DISTANCE` | Retrieval resource bounds; the bounded ANN alias overfetch is canonicalized before fusion, while the cosine distance cutoff must be calibrated with the judged dataset |
| `SEARCH_HYDE_ENABLED` | Optional HyDE generation gate; disabled by default |
| `SEARCH_EXACT_WEIGHT` / `SEARCH_FULL_TEXT_WEIGHT` / `SEARCH_TRIGRAM_WEIGHT` / `SEARCH_SEMANTIC_WEIGHT` / `SEARCH_HYDE_WEIGHT` / `SEARCH_FACILITA_WEIGHT` | Versioned weighted-RRF configuration; the Facilita signal is active only when its protected candidate client is configured |
| `TYPESENSE_SYNC_INTERVAL` / `TYPESENSE_FULL_SYNC_INTERVAL` | Typesense delta polling interval and periodic complete-snapshot reconciliation interval; the full interval cannot be shorter than the delta interval |

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
just openapi         # regenerates and validates the OpenAPI contract
just db              # shell psql
just reset           # DESTRUTIVO: apaga tudo e reinicia do zero
```

## API

### Busca e recomendação

```
GET  /api/v1/search              authenticated hybrid catalog search
GET  /api/public/search          public hybrid catalog search
POST /api/v1/search              authenticated JSON search transport
POST /api/public/search          public JSON search transport; keeps free text out of URLs
GET  /api/v1/recommendations     recomendações personalizadas por perfil — requer auth
GET  /api/public/recommendations recomendações com scoring neutro (sem auth)
GET  /api/v1/catalog/:id         detalhe de um item do catálogo
GET  /api/public/catalog/:id     idem, sem auth
```

Search keeps the canonical user query separate from synonym expansion. Exact matches, Portuguese full-text matches, title trigram matches, version-compatible semantic vectors, and optional bounded Facilita service ranks form independent pools. Every retriever keeps only its best signal per canonical entity before applying its candidate limit, so aliases cannot consume multiple fusion positions and signals found through different aliases accumulate on the same entity. Facilita contributes only slug and rank evidence: the catalog rehydrates the item, reapplies status, temporal eligibility, type, and request filters, and remains the authority for every public field. ANN retrieval first overfetches a bounded alias window and then canonicalizes it; this mitigates alias saturation without claiming exhaustive ANN recall at the configured resource bound. Weighted reciprocal-rank fusion produces one deterministic global order; an optional cross-encoder may reorder a complete leading window, and pagination runs last. If Facilita, Gemini, HyDE, or the reranker fails, the pipeline degrades to the strongest available stage without returning a partial external response.

Browse reads its catalog revision, canonical winners, total, page, and facets in one repeatable-read transaction. Ranked retrieval binds its complete candidate union to the same kind of repeatable-read catalog snapshot; Gemini and other remote work finishes before that database transaction begins. Cross-source aliases are deduplicated before counting or pagination. Retrieval evidence may come from any eligible alias, while the public fields always come from the newest eligible alias for that canonical entity; the catalog UUID is the deterministic tie-breaker. Facet counts use those same canonical winners, apply all current filters, and are sorted by count descending then canonical value in binary order. Ranked-query facets instead describe the bounded canonical retrieval-candidate union before reranking and pagination. Unsupported modalities and facet values that cannot round-trip through the bounded public filters are omitted.

Canonical grouping prefers an explicit `source_data.canonical_id`, then a valid service `source_data.slug`, then a `/servicos/<slug>` escaped URL path, and finally the source document identity. Go and SQL both ignore URL authority, query, and fragment text and deliberately keep percent encoding intact; ingestion should still populate `canonical_id` or `slug` whenever authoritative cross-source identity is available.

Every response includes `search_id`, `catalog_revision`, `effective_pipeline`, `degraded`, `sources`, the complete non-secret `ranker_descriptor`, and a fingerprinted `ranker_version`. The descriptor versions retrieval SQL semantics, query expansion, weights, semantic compatibility and overfetch, HyDE generation, the exact successful Facilita catalog/retrieval/expansion/ranker provenance, and reranker availability. `sources.facilita` distinguishes `not_applicable`, `unavailable`, `no_effect`, and `applied`, classifies bounded failures, and reports latency plus received and eligible candidate counts without query text. `catalog_revision` uses `catalog-v2:<content-revision>:window-until-<unix-microseconds|infinity>`: it changes both after catalog writes and when the PostgreSQL eligibility clock crosses the next `valid_from` or `valid_until` boundary. Ranked cache keys include that exact results snapshot, the validated external provenance, and the ordered external candidate digest but exclude pagination; a cache entry contains the complete final order, facets, provenance, diagnostics, and public items so every page is cut from one ranking without re-running Gemini or the reranker. The candidate source is called before a cache lookup to establish its current fingerprint. An exact fingerprint hit may reuse the ranked snapshot, while an unavailable or changed source cannot be served under stale provenance. Concurrent misses for the same ranking are coalesced into bounded shared work. An oversized complete snapshot is served as a bounded page without being cached, while a page that itself exceeds the public response byte limit fails explicitly and is never truncated. Every apparent hit is revalidated before return, including conditional source-state consistency, and writes use the shorter of the configured TTL and the remaining database-observed eligibility window. A boundary crossed during execution marks the response degraded and prevents caching. Runtime fallbacks are also returned with `degraded: true` and are never cached under the nominal ranker identity. Search metrics expose cache outcomes, fallbacks, candidate counts, reranker outcomes, zero-result searches, end-to-end latency, and bounded external-source request outcomes, latency, received candidates, and eligible contributions without using raw queries as metric labels.

Recommendation candidates and their temporal catalog revision are read in one repeatable-read transaction. Authenticated and anonymous recommendation cache keys include that revision, the ranking version, and the journey-graph version; authenticated identities are hidden behind a complete SHA-256 digest. Hits are revalidated, and their TTLs stop at the next eligibility transition. If the window moves while scoring or journey boosts run, the response is returned without being cached. A journey-graph read failure fails the recommendation instead of serving or caching a nominal ranking without its declared graph stage, and score ties are ordered by public item ID.

Anonymous recommendations deliberately use neutral scoring. The former `cluster_hint` query parameter was removed because it never affected candidate selection or ranking and only fragmented the cache. Any future anonymous personalization must expose independently typed, validated signals and preserve neutrality for dimensions the caller does not provide.

The runtime also uses anonymous terminology for that cache path. The historical `CACHE_RECOMMENDATION_CLUSTER_TTL` environment-variable name remains the external deployment contract because Kubernetes injects configuration from the separately managed `catalogo-secrets` object. Renaming it requires an atomic secret and application rollout; this source-only branch does not introduce a second alias or silently ignore the deployed key.

Recommendation type filters are canonicalized as a set before repository and cache access. An explicitly supplied limit is parsed strictly against the machine-readable OpenAPI bounds, while omission uses the documented default.

Privacy boundary: when Facilita candidates are configured and service results are allowed, the normalized non-empty query is sent in the body of a protected server-to-server POST request; it is never placed in that request URL or logged by app-catalogo. On a semantic cache miss, when Gemini retrieval is configured, the query is also sent server-side to Google Gemini to produce its query embedding. When HyDE is enabled and weighted, the same query is serialized as untrusted JSON user data under a fixed system instruction, the hypothetical document is generated with a fixed temperature, seed, candidate count, output bound, and response type, and that document is embedded. Gemini seed determinism is best effort; the prompt version, prompt SHA-256, model, generation settings, and determinism policy are therefore included in `ranker_descriptor` and its fingerprint. When reranking is enabled, the normalized query and the bounded public title/description projection of the leading candidates are sent to the configured `RERANKER_URL`. Treat these endpoints as trusted data processors: redirects are rejected, payloads and response bodies are excluded from errors, and production traffic must use HTTPS except for the explicitly approved in-cluster Facilita origin. Raw query text is excluded from application logs and metric labels. Every frontend must disclose this provider processing before submission and warn users not to enter personal or sensitive information.

The offline relevance harness, judgment contract, privacy rules, and executable promotion policy are documented in [`docs/search-evaluation.md`](docs/search-evaluation.md).

### OpenAPI generation

Handler annotations, JSON omission tags, explicit format tags, and model types are the source for generated Swagger. `scripts/generate-openapi.sh` runs the `swag` version recorded as a Go tool in `go.mod`, converts the bounded local Swagger JSON with the repository's Go converter, applies `scripts/postprocess-openapi-v3.jq` for constraints and the Prometheus route that Swagger annotations cannot represent, and validates the final OpenAPI 3.0 document. The converter pins `kin-openapi`, rejects malformed JSON, duplicate properties, unknown Swagger fields, reference siblings, and external references, and validates the version 2 structure before conversion. The generation pipeline keeps every intermediate in the contract directory and atomically replaces the committed document only after final validation. Pull-request CI and staging both regenerate the complete document, run `TestGeneratedOpenAPIContract`, and reject drift from the committed contract.

The Go migration deliberately omits the old converter's unused shared request-body component and relies on OpenAPI's default `form` plus `explode: true` serialization instead of emitting that default explicitly. `TestGeneratedOpenAPIContract` locks the exact route/method set, authentication boundary, response correlation header, catalog transport bounds, recommendation parameter boundary and correlated error schema, non-null sync-status wrapper, webhook boundary, critical response schemas, search request bounds, and Prometheus representation so those representation-only differences cannot weaken the client contract.

Aggregate JSON and request-body byte ceilings use verified `x-max-json-bytes`, `x-max-search-projection-bytes`, and `x-max-body-bytes` extensions because OpenAPI 3.0 has no standard keyword for encoded object or body size. Fixed response DTOs reject unknown fields; the upstream webhook payload and raw `source_data` remain deliberately open where runtime accepts additional properties.

When the search transport changes, update the Go transport model, the postprocessor, the generated document, and the contract test in the same changeset by running `just openapi`. Unsupported source filters must not be added to `SearchRequestBody`; retaining them in the GET parser is compatibility behavior only.

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

Rate limiting uses an atomic Redis fixed-window counter shared by all replicas and a client pool isolated from application caches. Redis keys contain an HMAC client identifier and a policy fingerprint derived from the quota, window, and endpoint class, so policy changes and public, authenticated, admin, and webhook traffic do not reuse stale counters. Redis failures activate a resource-bounded in-process fixed window instead of bypassing protection; bounded metrics expose locally allowed and rejected decisions. Liveness, readiness, and metrics endpoints do not consume application quotas.

The SuperApp BFF signs `x-catalog-search-client-id`, `x-catalog-search-client-timestamp`, `x-search-id`, and `x-request-id` with `APP_CATALOGO_INTERNAL_API_KEY` and sends the result in `x-catalog-search-client-signature`. The request ID is a canonical decimal, time-ordered distributed identifier within the protocol bound. Only a complete, canonical, fresh HMAC-SHA256 signature on `POST /api/public/search` may replace the BFF egress IP in the rate-limit identity. Missing, malformed, stale, duplicated, or invalid headers fall back to the canonical client IP and never grant authentication or authorization. Direct public consumers therefore remain compatible without trusted headers. Configure the same internal key in the SuperApp and app-catalogo secret stores and rotate them as one deployment change.

Forwarded IP headers remain disabled unless `SERVER_TRUSTED_PROXIES` names the direct ingress proxy network. Production ingress must also enforce an edge rate limit for volumetric traffic because the application fallback is deliberately bounded per replica and is not a replacement for perimeter protection.

## Fontes de Dados

| Fonte | Entidades | Estratégia |
|-------|-----------|------------|
| Typesense | Carta de Serviços (**temporário**) | Inclusive deltas plus periodic complete exports; only a validated no-cursor export may reconcile disappeared rows, and the upstream cursor/full-snapshot schedule is stored in `sync_events.metadata` |
| SalesForce | Carta de Serviços (**futuro**) | A complete snapshot runs on startup and periodically; overlapping deltas use the greatest upstream `LastModifiedDate`. Transactions are serialized per object, cursors never regress, and stale versions cannot replace newer catalog rows. HMAC-SHA256 webhooks apply the same version guard |
| app-go-api | Cursos, Vagas, MEI | Complete snapshot per vertical; every page must report a stable explicit total that exactly matches the received records before atomic source-scoped reconciliation |
| app-rmi | Perfil do cidadão | Demand-driven: busca síncrona no primeiro acesso; refresh em background quando stale |

SGRC request creation is outside this service boundary. `app-catalogo` indexes and ranks discoverable services; it does not submit municipal requests, own SGRC credentials, or expose an SGRC adapter. A real adapter needs an authoritative upstream contract, authentication requirements, and durable idempotency guarantees in the orchestration service that executes the citizen journey.

An empty Salesforce full snapshot is rejected and leaves catalog state and the cursor unchanged. This fail-closed limitation remains until the upstream integration provides an explicit completeness signal that can distinguish a valid empty catalog from a truncated or failed response.

Typesense validates the complete bounded JSONL export before applying its atomic batch. Malformed or interrupted exports fail the event without advancing the upstream cursor, and an empty full snapshot is rejected because the export protocol has no independent count proving that the collection is empty. Timestamp ties are intentionally reprocessed because the source cursor has second-level precision and catalog upserts are idempotent. Successful deltas only upsert returned documents; they never infer deletion. A periodic successful no-cursor export atomically upserts the snapshot and soft-deletes only missing `typesense` rows whose upstream timestamp is not newer than the snapshot boundary.

Source status is persisted as active, draft, or inactive for courses, jobs, and MEI opportunities, so a returned cancellation or expiration removes the item from public discovery. Each app-go-api vertical is reconciled independently in one transaction: current rows are upserted and rows from that exact source that disappeared are marked inactive with `deleted_at`; rows belonging to other sources are never touched. Missing totals, changing totals, premature empty pages, duplicate IDs, invalid records, and truncated responses fail closed before reconciliation. An explicitly reported zero total is a complete empty snapshot for that vertical. The reconciliation also records the fetch-start boundary, so an older slow snapshot cannot overwrite or deactivate a row committed by a newer concurrent fetch. A partially successful combined app-go-api run returns both its committed change count and its aggregated error; the datasource manager invalidates search caches whenever that count is non-zero. A successful Salesforce webhook also invalidates search caches before it is acknowledged.

Every catalog write path applies one shared validation contract before opening a transaction. It rejects unsupported source/type/status values, malformed or oversized UTF-8 text and arrays, credential-bearing or non-HTTP(S) public URLs, malformed structured metadata, unknown target-audience fields, invalid validity windows, and public search projections that exceed their byte budget. These application-level bounds preserve compatibility with existing schemas while preventing one upstream record from creating an unbounded public response.

Typesense synchronization and each app-go-api vertical acquire a PostgreSQL session advisory lease keyed by their canonical source before reading the upstream system. The lease uses a dedicated connection outside the application query pool and spans fetch, reconciliation, and cursor/event completion without keeping a database transaction open during remote I/O. Replicas therefore serialize only work for the same source while different sources remain independent. Waiting honors context cancellation; explicit unlock is verified and the dedicated connection is always closed as the final release guarantee. The API registers Typesense in its manual datasource manager with the same client and settings as the worker, so `source=typesense` uses this identical path.

## Autenticação

Istio validates the citizen token and injects `X-Auth-Request-Token`; the
service independently verifies the RS256 signature against the configured
Keycloak JWKS before trusting any claim. Issuer, audience, expiration, issued
time, optional not-before, and optional authorized party are validated with a
bounded clock skew. Unknown key IDs trigger one concurrency-collapsed refresh
with cooldown, redirects are rejected, and an expired key cache never fails
open. A missing proxy token keeps public routes anonymous; a present malformed,
forged, or invalid token returns `401` with a support log ID. Only roles under
`AUTH_JWT_ROLE_CLIENT_ID` can grant application authorization.

`preferred_username` contains the citizen CPF. Ordinary `Authorization`
headers are deliberately ignored at this boundary; ingress must strip and
overwrite the proxy token header.

CPF **nunca** é persistido — apenas `cpf_hash` (`HMAC-SHA256(CPF_HASH_SALT, CPF)`). O CPF em texto vai apenas para a chamada ao app-rmi, em memória.

Service calls to app-rmi and app-go-api use Keycloak `client_credentials`. Keycloak and Salesforce clients reject redirects, bound response bodies, omit upstream payloads and credentials from errors, collapse concurrent token refreshes, and validate endpoint configuration before startup. A Salesforce query may renew credentials after `401` only once, and pagination is restricted to the configured Salesforce origin.

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

Migrations live in `db/migrations/NNNNNN_name.sql` and contain Goose `Up` and `Down` sections.

The `search_vector` column is maintained by a PostgreSQL trigger from `title`, `short_desc`, `description`, and `tags`. Embeddings are generated asynchronously and carry model, model-version, dimension, task-type, canonical-document-version, source-hash, and generation-time metadata. Source changes invalidate the vector and make the item eligible for a safe worker claim.

Migration `000005_catalog_revision.sql` owns the singleton content revision used by search and recommendation snapshots. Any committed insert, delete, or observable catalog update advances it once per transaction; transient embedding claim ownership does not. Revision changes roll back with their catalog transaction, and the reversible `Down` removes the trigger, function, and state table.

The initial historical schema still contains `citizen_profiles.cluster_id`, `citizen_profiles.cluster_updated_at`, and `demographic_clusters`, but the runtime no longer reads or writes them. Removing persisted structures is a separate production data operation: first audit both environments for unexpected rows or values, then ship a reversible, fail-closed migration with an explicit recovery plan. Do not rewrite the already-applied initial migration.

`APP_CATALOGO_MIGRATION_TEST_DATABASE_URL` explicitly enables the destructive migration round-trip test. It must reference a dedicated database whose name contains `test`; never point it at shared or production data. CI exposes it only to the isolated migration test step before the general suite.

## Deploy

Manifests Kubernetes em `k8s/staging/` e `k8s/prod/`. O projeto usa KEDA para autoscaling.

Database migrations run as a bounded Argo CD `PreSync` Job using the same
immutable application image. API and worker startup never run migrations, so
a failed schema change cannot crash-loop a newly rolled out workload or race
across replicas. The image and every catalog container run as UID 10001 with a
read-only root filesystem, no privilege escalation, no Linux capabilities,
and the runtime-default seccomp profile.

Fluxo de branches:
- `feat/*` / `fix/*` → PR para `main` (sempre a partir de `main`)
- Merge em `main` → deploy automático para staging
- GitHub Release → deploy para produção
- Nunca commitar direto em `main`

## License status

This repository currently has no `LICENSE` file and does not declare an open-source license. Adding a license requires an explicit project-owner or legal decision; repository visibility must not be interpreted as a license grant.
