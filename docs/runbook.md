# Runbook operacional — app-catalogo

Guia para operar o app-catalogo em incidente: o que o serviço faz, onde olhar, o que cada sinal quer dizer e como agir (rollback, sync manual, fila do webhook, migrations). Os nomes de variáveis, rotas, métricas e comandos abaixo são os do código; quando algo ainda não existe (alerta, dashboard), está dito.

Este documento não traz segredos. Valores sensíveis vivem no Infisical (Secret `catalogo-secrets`, ver [Variáveis críticas](#variáveis-críticas)) e nunca vão para issue, log ou chat.

## Sumário

- [Visão rápida](#visão-rápida)
- [Onde olhar](#onde-olhar)
- [Sinais e alertas: o que significam e o primeiro passo](#sinais-e-alertas-o-que-significam-e-o-primeiro-passo)
- [Rotina de incidente](#rotina-de-incidente)
  - [Triagem em 5 minutos](#triagem-em-5-minutos)
  - [Rollback](#rollback)
  - [Sync manual](#sync-manual)
  - [Fila do webhook SalesForce](#fila-do-webhook-salesforce)
  - [Migrations: rodar e reverter](#migrations-rodar-e-reverter)
- [Variáveis críticas](#variáveis-críticas)

## Visão rápida

**O que faz.** Discovery layer da Prefeitura do Rio: indexa serviços da Carta de Serviços, cursos, vagas e oportunidades MEI num PostgreSQL (FTS + `pgvector`) e expõe busca e recomendação. Sem o catálogo, a busca global e as recomendações dos consumidores param; nada de escrita do cidadão depende dele.

**Quem consome.**

- Superapp e Portal Interno (`https://staging.app.dados.rio`, `https://admin.staging.app.dados.rio`; em produção `https://pref.rio`, `https://admin.pref.rio`, as origens de `CORS_ALLOWED_ORIGINS` nos manifests), pela borda `https://services.staging.app.dados.rio/catalogo` e `https://services.pref.rio/catalogo` (o Istio tira o prefixo `/catalogo`).
- A Carta de Serviços (SalesForce), que chama `POST /api/webhooks/salesforce` a cada mudança de serviço.

**Processos** (mesma imagem `ghcr.io/prefeitura-rio/app-catalogo`, namespace `catalogo`):

| Workload | Comando | O que roda |
|----------|---------|------------|
| Rollout `catalogo` (Argo Rollouts) | `./api` (porta 8080) | API HTTP, verificação do JWT, reprocessador da fila do webhook (a cada 30 s, só com `SALESFORCE_WEBHOOK_SECRET`) e as syncs **manuais** (`POST /api/v1/admin/sync/trigger`) |
| Deployment `catalogo-worker` | `./worker` (métricas na 9091) | Syncs **agendadas** (sync inicial ao subir + ticker por fonte) e o backfill de embeddings (Gemini) |
| Job `catalogo-migrate` (hook `PreSync` do Argo CD) | `./migrate up` | Migrations goose antes de cada sync do Argo CD |

**De quem depende.**

| Dependência | Para quê | Se cair |
|-------------|----------|---------|
| PostgreSQL 16 (Cloud SQL, banco `catalogo`) | tudo | `/ready` responde 503, o pod sai do Service; busca e recomendação falham (5xx) |
| Redis (Valkey `redis` no namespace `catalogo`) | cache de busca, vetores e recomendação | a API segue sem cache (log `redis indisponível — cache desativado` no startup); mais carga no banco |
| Keycloak (realm de `KEYCLOAK_REALM`) | JWKS para verificar o JWT; `client_credentials` para chamar app-rmi e app-go-api | rotas `/api/v1/*` respondem 401; recomendação personalizada e sync do app-go-api falham |
| SalesForce / Carta de Serviços (API CloudHub, `SALESFORCE_INSTANCE_URL`) | sync da Carta (delta a cada 15 min) e o detalhe de cada serviço do webhook | a Carta fica desatualizada; eventos do webhook ficam pendentes com nova tentativa |
| app-go-api (`APP_GO_API_BASE_URL`) | cursos, vagas e MEI (sync completo a cada 30 min) | itens desses tipos ficam desatualizados; nada é apagado |
| app-rmi (`RMI_BASE_URL`) | perfil do cidadão, no caminho síncrono de `GET /api/v1/recommendations` | o circuit breaker abre e a recomendação cai na genérica (sem personalização) |
| Gemini (`GOOGLE_API_KEY`, opcional) | embeddings para busca semântica | a busca fica só FTS; o backfill para |
| Reranker (`RERANKER_URL`, opcional) | reordenação cross-encoder da busca | a busca fica sem rerank |

A infraestrutura (namespace, Secret do Infisical, Valkey, banco, VirtualService/AuthorizationPolicy do Istio e a Application `catalogo` do Argo CD) é definida em `prefeitura-rio/infra`, projeto `superapp` (`modules/k8s/catalogo.tf`, `modules/k8s/main.tf`, `modules/gcp/postgres.tf`, `modules/k8s/yamls/argocd/applications/catalogo.yaml`). Mudança nela é por PR nesse repositório, nunca à mão no console ou no cluster.

**Clusters.** Staging: `application` em `rj-superapp-staging`; produção: `application` em `rj-superapp` (região `us-central1`). Credencial de leitura:

```bash
gcloud container clusters get-credentials application --dns-endpoint --region us-central1 --project rj-superapp-staging   # staging
gcloud container clusters get-credentials application --dns-endpoint --region us-central1 --project rj-superapp           # produção
```

## Onde olhar

### Saúde e estado do deploy

```bash
kubectl -n catalogo get rollout catalogo                    # fase do Rollout (Healthy, Paused, Degraded)
kubectl argo rollouts get rollout catalogo -n catalogo       # passos, ReplicaSets e AnalysisRuns (plugin do Argo Rollouts)
kubectl -n catalogo get deploy catalogo-worker
kubectl -n catalogo get pods -o wide
kubectl -n catalogo get events --field-selector type=Warning --sort-by=.lastTimestamp
kubectl -n argocd get application catalogo -o jsonpath='{.status.sync.status} {.status.health.status} {.status.operationState.phase}{"\n"}'
```

- `GET /health`: liveness, sempre 200 enquanto o processo responde.
- `GET /ready`: readiness, faz ping no banco; 503 com `banco de dados indisponível` quando o PostgreSQL não responde.
- `GET /api/v1/admin/sync/status` (papel admin): o último evento de sync por fonte (`salesforce`; o `typesense` antigo pode aparecer, mas a fonte foi desligada), com `last_status`, horários, `items_processed`, `items_failed` e `error_message`. O app-go-api não grava esse histórico; para ele, use as métricas e os logs do worker.

### Métricas (`/metrics`, Prometheus)

Cada processo expõe as próprias métricas numa porta só delas, fora da porta pública: a API em `GET /metrics` na porta 9092 (`METRICS_PORT`) e o worker na porta 9091 (`WORKER_METRICS_PORT`; Service `catalogo-worker-metrics`). A porta 8080 da API (a que o gateway expõe em `/catalogo/*`) não serve `/metrics`. Os dois pods têm as anotações `prometheus.io/scrape|port|path`, então o Prometheus do `istio-system` (job `kubernetes-pods`) coleta as duas; para ler direto:

```bash
kubectl -n catalogo port-forward svc/catalogo-worker-metrics 9091:9091   # syncs agendadas
kubectl -n catalogo get pods -l app=catalogo                             # escolha um pod da API
kubectl -n catalogo port-forward pod/<pod-da-api> 9092:9092              # API (busca, webhook, RMI, syncs manuais)
curl -s localhost:9091/metrics | grep '^catalogo_'
curl -s localhost:9092/metrics | grep '^catalogo_'
```

| Métrica | Processo | Labels | Leitura |
|---------|----------|--------|---------|
| `catalogo_sync_runs_total` | worker (agendada), API (manual) | `source`, `trigger` (`initial`, `periodic`, `manual`), `result` (`success`, `failure`) | execução de sync contada depois das novas tentativas |
| `catalogo_sync_retries_total` | idem | `source` | novas tentativas depois de falha (até 4 tentativas em ~3,5 min) |
| `catalogo_sync_duration_seconds` | idem | `source`, `type` (= trigger) | duração de cada tentativa |
| `catalogo_sync_last_success_timestamp_seconds` | idem | `source` | horário Unix do último sucesso; só existe depois do primeiro sucesso do processo |
| `catalogo_salesforce_webhook_events_total` | API | `result` (`enqueued`, `duplicate`, `processed`, `failed`) | eventos do webhook; `failed` conta tentativas que falharam (o evento continua pendente) |
| `catalogo_http_client_circuit_state` | API (`rmi`), API e worker (`appgoapi`) | `client` | 0 fechado, 1 meio-aberto, 2 aberto |
| `catalogo_http_client_retries_total` | idem | `client` | novas tentativas dos clientes HTTP |
| `catalogo_search_duration_seconds` | API | `has_query`, `type_filter` | latência da busca |
| `catalogo_recommendations_duration_seconds` | API | `personalized` | latência da recomendação |

O tráfego HTTP visto pelo Istio (taxa, 5xx, latência) fica no Prometheus do `istio-system` (`istio_requests_total`, `istio_request_duration_milliseconds`), o mesmo que o KEDA e a análise do canary consultam:

```promql
sum(rate(istio_requests_total{destination_workload="catalogo"}[5m]))                                                     # req/s
sum(rate(istio_requests_total{destination_workload="catalogo",response_code=~"5.."}[5m]))
  / sum(rate(istio_requests_total{destination_workload="catalogo"}[5m]))                                                  # taxa de 5xx
time() - catalogo_sync_last_success_timestamp_seconds                                                                    # segundos desde o último sucesso, por fonte
```

### Traces e logs (SigNoz)

- **Traces:** a API envia OTLP para `signoz-otel-collector.signoz.svc.cluster.local:4317` (`TRACING_ENABLED=true` nos manifests), serviço `app-catalogo` (`TRACING_SERVICE_NAME`). No SigNoz, filtre `service.name = app-catalogo` para latência por rota, erros e spans das chamadas ao banco. O worker não exporta traces.
- **Logs:** zerolog no stdout dos pods, em JSON (em `APP_ENV=development` sai no formato de console); no SigNoz filtre `k8s.namespace.name = catalogo` e o container (`catalogo` para a API, `catalogo-worker` para o worker), ou use `kubectl -n catalogo logs`. Campos úteis:
  - acesso da API: mensagem `request`, com `method`, `path`, `status`, `latency`, `request_id`, `ip` (nível `error` para 5xx, `warn` para 4xx);
  - syncs: `source`, `trigger`, `mode`, `attempt`, `retry_in`; mensagens `datasource: sync falhou, nova tentativa agendada`, `datasource: erro no sync periódico`, `datasource: sync manual falhou`;
  - webhook: `event_id`, `slug`, `attempt`, `retry_in`; mensagens `webhook: falha ao sincronizar registro; evento mantido pendente`, `webhook: assinatura inválida`, `webhook: falha ao gravar evento`;
  - startup: `verificação de JWT via JWKS ativada` (ou o erro de `AUTH_JWT_ISSUER/AUTH_JWKS_URL ... endpoints autenticados responderão 401`), `RATE_LIMIT_ENABLED=false — rate limiting em memória desligado`, `worker: <fonte> não configurado ou desabilitado, fonte ignorada`.

```bash
kubectl -n catalogo logs deploy/catalogo-worker --since=1h | grep -E 'datasource|webhook'
kubectl -n catalogo logs -l app=catalogo --since=15m --max-log-requests=10 | grep '"level":"error"'
```

### Dashboards

Não há dashboard do app-catalogo versionado, nem no SigNoz (os dashboards do `prefeitura-rio/infra`, `superapp/modules/signoz/`, são de outros serviços) nem em Grafana. Até existir um, use o explorador do SigNoz com os filtros acima e as queries PromQL desta seção.

## Sinais e alertas: o que significam e o primeiro passo

Não há regras de alerta do app-catalogo configuradas (nem no repositório nem no `prefeitura-rio/infra`; pedidas na OPS-149, sobre as métricas que o Prometheus do `istio-system` já coleta da API e do worker). Os avisos que chegam hoje são as notificações de deploy no Discord (staging e release) e a análise automática do canary de produção. A tabela cobre esses avisos e os sinais que valem um alerta quando ele for criado, com o primeiro passo de cada um.

| Sinal | O que significa | Primeiro passo |
|-------|-----------------|----------------|
| Discord `❌ Staging Deploy Failed — app-catalogo` | o workflow `deploy-staging.yaml` falhou (build, Argo CD, pod de preview sem `/ready`) e abortou o blue-green; o serviço ativo continua na versão anterior | abrir o run no GitHub Actions; se o preview não ficou Ready, `kubectl -n catalogo logs` do pod novo e `kubectl -n catalogo logs job/catalogo-migrate` (enquanto o Job existir) |
| Discord `❌ Production Release Failed — app-catalogo` | o `release.yaml` falhou: operação do Argo CD `Failed` (normalmente a migration do hook `PreSync`), Rollout `Degraded`/`Aborted` ou worker fora da imagem nova | ver o passo que falhou no run; migration: logs do `catalogo-migrate` (o workflow imprime); Rollout: `kubectl argo rollouts get rollout catalogo -n catalogo` e o último `AnalysisRun` |
| AnalysisRun `catalogo-success-rate` falhou (produção) | o canary teve taxa de sucesso abaixo de 95% ou de 5xx acima de 5% em mais de 5 medições; o Argo Rollouts aborta e volta o tráfego ao estável | conferir que o tráfego voltou ao estável (`kubectl argo rollouts get rollout catalogo -n catalogo`); investigar os logs do ReplicaSet canary; não promover à mão |
| `/ready` 503, pods `NotReady` | a API não alcança o PostgreSQL | checar o Cloud SQL (instância e conexões) e `DB_*`; `kubectl -n catalogo describe pod` |
| Pods em `CrashLoopBackOff` logo no startup | configuração inválida: `CPF_HASH_SALT` com menos de 32 bytes, `TRUSTED_PROXIES` com item que não é IP/CIDR, `RATE_LIMIT_ENABLED` não booleano, `SALESFORCE_INSTANCE_URL` inválida, banco inacessível | `kubectl -n catalogo logs <pod> --previous`: a primeira linha diz qual variável; corrigir no Infisical (ou no manifest, para as variáveis em `env`) |
| Taxa de 5xx da API alta (`istio_requests_total`) | erro do banco, timeout de requisição (`SERVER_REQUEST_TIMEOUT`, 30 s) ou regressão de código | logs `"level":"error"` e traces no SigNoz; se começou com um deploy, [rollback](#rollback) |
| Muitos 401 em `/api/v1/*` | token inválido ou expirado, `azp` fora de `AUTH_JWT_AUTHORIZED_PARTIES`, token sem CPF válido em `preferred_username` (rotas não admin), ou verificador sem emissor/JWKS | conferir no log de startup se o JWKS está ativado; conferir `AUTH_JWT_*`/`KEYCLOAK_*` e se o Keycloak responde |
| Muitos 429 | rate limit em memória (300 req/min por IP, por pod) | conferir `TRUSTED_PROXIES` (IP de proxy contado como cliente) e o tráfego por IP; `RATE_LIMIT_ENABLED=false` só com limite na borda |
| `catalogo_sync_runs_total{result="failure"}` subindo, ou `time() - catalogo_sync_last_success_timestamp_seconds` acima de 2× o intervalo da fonte | a fonte falhou nas 4 tentativas | logs do worker com o `source`; checar a dependência (SalesForce, app-go-api, Keycloak); depois de corrigir, [sync manual](#sync-manual) |
| `catalogo_sync_retries_total` subindo, sem falha final | falhas transitórias absorvidas pelas novas tentativas | só acompanhar; vira incidente se virar `failure` |
| `catalogo_http_client_circuit_state{client="rmi"} == 2` | o app-rmi está fora ou lento; recomendações autenticadas caem na genérica | checar o app-rmi e o Keycloak (`client_credentials`); o circuito tenta de novo sozinho depois de 30 s |
| `catalogo_http_client_circuit_state{client="appgoapi"} == 2` | o app-go-api está fora; o sync de cursos, vagas e MEI falha | checar o app-go-api; o circuito tenta de novo depois de 60 s; o próximo ciclo (30 min) ou um sync manual recupera |
| `catalogo_salesforce_webhook_events_total{result="failed"}` subindo | o detalhe do serviço na Carta não pôde ser lido; os eventos ficam pendentes e voltam com espera que dobra (30 s até 1 h) | ver `last_error` na fila ([consulta](#fila-do-webhook-salesforce)); checar a API CloudHub |
| Webhook respondendo 401 (`webhook: assinatura inválida`) ou `webhook não configurado` | HMAC não confere com `SALESFORCE_WEBHOOK_SECRET`, ou o segredo não está definido | conferir com o time da Carta qual segredo assina; o segredo novo entra pelo Infisical |
| Webhook respondendo 503 | o evento não pôde ser gravado no banco; o remetente deve reenviar | checar o PostgreSQL; nada se perde se o remetente reenviar |
| Busca com resultados de outra época, ou serviços faltando | Carta desatualizada (SalesForce) ou cache antigo (TTL de 60 s) | `GET /api/v1/admin/sync/status`; [sync manual](#sync-manual); full da Carta se a hierarquia ou os órgãos estão vazios |
| KEDA no `fallback` (réplicas fixas) | o Prometheus do `istio-system` não responde à query do ScaledObject | `kubectl -n catalogo describe scaledobject catalogo-scaled-object`; o serviço segue com 1 (staging) ou 2 (produção) réplicas |

## Rotina de incidente

### Triagem em 5 minutos

1. Começou com um deploy? Compare o horário com o último run de `deploy-staging.yaml` (staging) ou `release.yaml` (produção) e com `kubectl argo rollouts get rollout catalogo -n catalogo`. Se sim, avalie o [rollback](#rollback) antes de investigar.
2. Os pods estão Ready? `kubectl -n catalogo get pods`; `/ready` 503 = banco.
3. Qual superfície falha? Busca/recomendação (API, banco, Redis, RMI), dados desatualizados (worker e fontes) ou Carta (webhook e SalesForce).
4. Erros nos logs e traces do SigNoz no período, filtrando `"level":"error"` e o `request_id` de uma requisição que falhou.
5. Registre o que viu (horários, versão, métricas) na issue do incidente, sem token, CPF ou outro dado pessoal.

### Rollback

O deploy é feito pelos workflows, que fixam a imagem na Application `catalogo` do Argo CD (`spec.source.kustomize.images`) e acompanham o Argo Rollouts. Não há Flagger: a entrega progressiva é só do Argo Rollouts (blue-green em staging, canary com análise em produção).

**Produção, durante o canary** (passos de 10% → 80% com pausas, ~20 min): se a análise falhar, o Argo Rollouts aborta sozinho e volta todo o tráfego ao ReplicaSet estável. Para abortar à mão:

```bash
kubectl argo rollouts abort catalogo -n catalogo
kubectl argo rollouts get rollout catalogo -n catalogo   # tráfego 100% no estável
```

Abortar não muda a imagem desejada: a Application continua apontando para a versão nova e o Rollout fica `Degraded`. Para encerrar, faça o rollback por release abaixo.

**Produção, depois do rollout concluído** (rollback por GitHub Release anterior): reexecute o run do `release.yaml` da versão anterior. O run usa a tag dessa release (`github.ref_name`), reconstrói e publica a imagem como `:stable`, aponta a Application para o novo digest e acompanha o canary.

```bash
gh release list -R prefeitura-rio/app-catalogo --limit 5
gh run list -R prefeitura-rio/app-catalogo --workflow release.yaml --limit 10   # achar o run da versão anterior
gh run rerun <run-id> -R prefeitura-rio/app-catalogo
```

O GitHub só reexecuta runs de até 30 dias. Alternativa equivalente, e a única depois desse prazo: publicar uma release nova a partir de um commit que reverte a mudança (`git revert` por PR na `main`, depois a release).

**Staging:** o deploy só promove o blue-green depois de `/health` e `/ready` passarem no preview; se falham, o workflow aborta e o serviço ativo fica inalterado. Para voltar uma mudança que passou, reverta o merge por PR na `main` (o push dispara um novo deploy).

**O que o rollback não desfaz:** migrations já aplicadas. O código anterior tem de funcionar com o schema novo (as migrations seguem expand/contract); se não funcionar, veja [reverter migrations](#reverter).

### Sync manual

`POST /api/v1/admin/sync/trigger` dispara uma sync em segundo plano e responde 202 na hora. Exige um JWT válido com papel admin (role `admin` ou `go:admin` em `resource_access.superapp.roles`).

| Parâmetro | Valores | Efeito |
|-----------|---------|--------|
| `source` | `salesforce`, `app-go-api`; vazio = todas | fonte a sincronizar; fonte que a API não registrou responde 400 com `available_sources` |
| `mode` | `delta` (padrão), `full` | `full` reprocessa a Carta inteira no SalesForce (lista, hierarquia, órgãos SICI e detalhe de todos os serviços); no app-go-api, que já é sync completo, é igual ao `delta`; outro valor responde 400 |

```bash
# TOKEN: JWT de um usuário admin, obtido pelo login do Keycloak; nunca cole o token em issue, log ou chat
curl -sS -X POST -H "X-Auth-Request-Token: Bearer $TOKEN" \
  "https://services.staging.app.dados.rio/catalogo/api/v1/admin/sync/trigger?source=salesforce&mode=full"
curl -sS -H "X-Auth-Request-Token: Bearer $TOKEN" \
  "https://services.staging.app.dados.rio/catalogo/api/v1/admin/sync/status"
```

Pontos de atenção:

- A sync manual roda **no pod da API** que recebeu a chamada, com as mesmas novas tentativas da agendada. O resultado aparece nos logs desse pod (`datasource: sync manual falhou`), em `catalogo_sync_runs_total{trigger="manual"}` do `/metrics` da API e, para o SalesForce, em `/admin/sync/status`.
- A API só conhece as fontes `salesforce` e `app-go-api`, e só quando configuradas (`SALESFORCE_INSTANCE_URL` com `SALESFORCE_SYNC_ENABLED`; `APP_GO_API_BASE_URL` com `APP_GO_API_SYNC_ENABLED`). Para `source=typesense` (fonte legada desligada), uma fonte desconhecida ou uma não configurada a resposta é 400 e nada roda; `available_sources` lista as fontes que aquele pod dispara.
- O **Typesense** foi desligado: nem a API nem o worker o sincronizam, e as variáveis `TYPESENSE_*` que ainda estiverem no Secret são ignoradas.

### Fila do webhook SalesForce

Todo evento válido do webhook (HMAC conferido) é gravado em `salesforce_webhook_events` antes da resposta (`queued`) e processado em seguida. Não existe status `failed`: a coluna `status` só tem `pending` e `completed`. Um evento que falhou continua `pending`, com `last_error` preenchido, `attempts` incrementado e `next_attempt_at` adiado (30 s, 1 min, 2 min… até 1 h, sem desistir). O reprocessador roda **na API**, a cada 30 s, só quando `SALESFORCE_WEBHOOK_SECRET` está definido; ele assume até 20 eventos vencidos por passada (`FOR UPDATE SKIP LOCKED`, seguro com várias réplicas) e também retoma os que ficaram sem dono porque o pod reiniciou (lease de 2 min). Eventos concluídos ficam 7 dias e depois são apagados.

Ver o que está pendente:

```sql
SELECT id, slug, event_type, attempts, next_attempt_at, received_at, left(last_error, 200) AS last_error
FROM salesforce_webhook_events
WHERE status = 'pending'
ORDER BY next_attempt_at
LIMIT 50;
```

**Reprocessar os eventos que falharam**, depois de corrigir a causa (por exemplo, a API CloudHub voltou): antecipe a próxima tentativa; o reprocessador os pega em até 30 s. O processamento é idempotente (sincroniza o estado atual do serviço pelo slug), então repetir é seguro.

```sql
UPDATE salesforce_webhook_events
SET next_attempt_at = NOW(), updated_at = NOW()
WHERE status = 'pending' AND last_error IS NOT NULL;
```

Confira o resultado em `catalogo_salesforce_webhook_events_total{result="processed"}` do `/metrics` da API e nos logs `webhook: registro sincronizado`. Se muitos serviços foram afetados, um [sync manual](#sync-manual) `source=salesforce&mode=full` recupera a Carta inteira de uma vez.

Esses SQL são escrita no banco de produção: rode com a credencial de operação do banco, numa sessão registrada no incidente, e nunca apague linhas `pending` (o evento se perderia).

### Migrations: rodar e reverter

As migrations são goose, em `db/migrations/NNNNNN_nome.sql`, cada arquivo com as seções `-- +goose Up` e `-- +goose Down`. O binário `./migrate` (na imagem, em `/app`) aceita os comandos do goose: `up` (padrão), `down`, `status`, `version`, `up-to <versão>`, `down-to <versão>`, `redo`.

**Rodar.** Em staging e produção, o Job `catalogo-migrate` (hook `PreSync` do Argo CD) roda `./migrate up` antes de cada sync da Application, com o mesmo Secret da API e prazo de 300 s. Se ele falha, a sync do Argo CD falha e a versão nova não entra (é o `Failed` que o `release.yaml` reporta). O Job some depois do sucesso (`HookSucceeded`); para ver o log de uma falha, `kubectl -n catalogo logs job/catalogo-migrate` enquanto ele existe. A variável `RUN_MIGRATIONS` é lida pela configuração, mas nenhum processo a usa: a API e o worker não migram ao subir.

Localmente: `just migrate` (`go run cmd/migrate/main.go up`) e `go run cmd/migrate/main.go status`.

<a id="reverter"></a>**Reverter.** Prefira corrigir para frente (migration nova que desfaz) por PR: ela passa pela esteira e pelo hook. Use `down` só quando o código em produção não funciona com o schema novo e não dá para esperar um PR:

1. Confira o estado: `./migrate status` e `./migrate version`.
2. Leia a seção `-- +goose Down` da migration a reverter. Down que remove tabela ou coluna **apaga dados** (ex.: o down da 000013 remove a fila do webhook com os eventos pendentes); nesse caso, faça backup antes.
3. Primeiro faça o [rollback](#rollback) do código para uma versão que funcione sem a migration; depois reverta o schema.
4. Rode o down num pod que já tem a imagem e o Secret, por quem tem escrita no cluster:

   ```bash
   kubectl -n catalogo exec deploy/catalogo-worker -c catalogo-worker -- ./migrate status
   kubectl -n catalogo exec deploy/catalogo-worker -c catalogo-worker -- ./migrate down              # só a última
   kubectl -n catalogo exec deploy/catalogo-worker -c catalogo-worker -- ./migrate down-to 000014    # até a versão indicada
   ```

5. Enquanto a migration continuar em `db/migrations` na `main`, o próximo sync do Argo CD a reaplica (`up` no hook). Remova-a ou corrija-a por PR antes do próximo deploy.

## Variáveis críticas

Os valores vêm do Secret `catalogo-secrets` (sincronizado do Infisical pelo operador, com `secrets.infisical.com/auto-reload`, que reinicia os pods quando o segredo muda) e, para as não sensíveis, do bloco `env` dos manifests em `k8s/`. Os nomes e padrões são os de `internal/config/config.go`. Mudança de valor em staging ou produção vai pelo Infisical ou por PR nos manifests, nunca por `kubectl set env`.

**Liga e desliga (feature flags):**

| Variável | Padrão | Efeito |
|----------|--------|--------|
| `SALESFORCE_SYNC_ENABLED` | `true` | sync da Carta pelo SalesForce (exige `SALESFORCE_INSTANCE_URL`); `false` tira a fonte do worker e da sync manual |
| `APP_GO_API_SYNC_ENABLED` | `true` | sync de cursos, vagas e MEI (exige `APP_GO_API_BASE_URL`) |
| `RATE_LIMIT_ENABLED` | `true` | rate limit em memória (300 req/min por IP); `false` só com limite na borda; valor não booleano impede o startup |
| `TRACING_ENABLED` | `false` (`true` nos manifests) | envio de traces ao SigNoz |
| `GOOGLE_API_KEY` | vazio | presente, liga a busca semântica e o backfill de embeddings |
| `RERANKER_URL` | vazio | presente, liga o rerank da busca (`RERANKER_TIMEOUT`, padrão 2 s) |
| `SALESFORCE_WEBHOOK_SECRET` | vazio | presente, liga o webhook e o reprocessador da fila; vazio, o webhook responde 401 |

**Obrigatórias e de segurança:**

| Variável | Observação |
|----------|------------|
| `CPF_HASH_SALT` | salt do `SHA-256` do CPF; pelo menos 32 bytes, senão o processo não sobe. Trocar o valor muda todos os hashes: os perfis gravados deixam de ser encontrados e são buscados de novo no app-rmi |
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, `DB_SSL_MODE`, `DB_MAX_OPEN_CONNS` (25), `DB_MIN_CONNS` (5) | conexão com o PostgreSQL |
| `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD`, `REDIS_DB` | cache; indisponível, a API segue sem cache |
| `KEYCLOAK_URL`, `KEYCLOAK_REALM`, `KEYCLOAK_CLIENT_ID`, `KEYCLOAK_CLIENT_SECRET` | emissor padrão do JWT e `client_credentials` para app-rmi e app-go-api |
| `AUTH_JWT_ISSUER`, `AUTH_JWKS_URL` | verificação do JWT (padrão derivado do Keycloak); sem eles, todo `/api/v1/*` responde 401 |
| `AUTH_JWT_AUTHORIZED_PARTIES` | clients (`azp`) aceitos, padrão `superapp`; lista vazia = nenhum token aceito |
| `TRUSTED_PROXIES` | proxies cujo `X-Forwarded-For` define o IP do cliente (rate limit e log); os manifests acrescentam o IP do load balancer de cada ambiente; item inválido impede o startup |
| `CORS_ALLOWED_ORIGINS` | origens do navegador aceitas (no `env` dos manifests) |

**Endereços e ritmo das fontes:**

| Variável | Padrão |
|----------|--------|
| `SALESFORCE_INSTANCE_URL`, `SALESFORCE_BASE_SERVICE_URL` | vazio, `https://prefeitura.rio` |
| `SALESFORCE_SYNC_INTERVAL`, `SALESFORCE_DETAIL_CONCURRENCY` | `15m`, `8` |
| `APP_GO_API_BASE_URL`, `APP_GO_API_SYNC_INTERVAL` | vazio, `30m` |
| `RMI_BASE_URL` | vazio |
| `CITIZEN_PROFILE_STALE_THRESHOLD`, `CITIZEN_PROFILE_SYNC_INTERVAL` | `1h`, `24h` |
| `EMBEDDING_BACKFILL_INTERVAL` | `5m` |
| `CACHE_SEARCH_TTL`, `CACHE_SEARCH_VECTORS_TTL` | `60s`, `1h` |
| `CACHE_RECOMMENDATION_AUTHENTICATED_TTL`, `CACHE_RECOMMENDATION_CLUSTER_TTL` | `5m`, `15m` |
| `SERVER_PORT`, `SERVER_REQUEST_TIMEOUT`, `METRICS_PORT`, `WORKER_METRICS_PORT` | `8080`, `30` (segundos), `9092` e `9091` (`0` desliga; `METRICS_PORT` igual a `SERVER_PORT` impede o startup) |
| `APP_ENV`, `LOG_LEVEL` | `development`, `info` |
