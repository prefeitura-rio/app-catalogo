# Incidente: Typesense de staging vazio — investigação e recuperação

**Data:** 2026-07-08
**Ambiente:** staging (`gke_rj-superapp-staging`, namespace `busca`)
**Status final:** resolvido — `prefrio_services_base` restaurada (50/50 documentos), cluster saudável

---

## 1. Resumo executivo

O `app-busca-search` de staging passou a retornar resultado vazio para toda busca de "Carta de Serviços". Investigação mostrou que o cluster Typesense de staging estava em **crash-loop** (drift entre `.Values.replicas` do Helm chart e o número real de réplicas do StatefulSet), e que **não havia backup nem pipeline automatizado** para repopular os dados depois de corrigida a infra. A recuperação usada foi reimportar os 50 serviços cuja cópia (`source_data`) ainda existia intacta no Postgres do `app-catalogo` — que sincroniza esses dados do Typesense periodicamente e por isso, sem querer, funcionou como backup de fato.

**Causa raiz:** infra (fora do escopo de qualquer app) — já corrigida pelo time de infra antes da recuperação.
**app-catalogo não causou nem poderia ter causado o incidente** — sua única interação com o Typesense é leitura (`GET /documents/export`).

---

## 2. Sintoma inicial

Relato no canal: `app-busca-search` retornando array vazio para todas as requisições de busca em staging. Print do painel Typesense mostrava 4 collections com schema presente e **0 documentos**:

| Collection | Documentos (no incidente) |
|---|---|
| `hub_search` | 0 |
| `prefrio_services_base` | 0 |
| `service_versions` | 0 |
| `tombamentos_overlay` | 0 |

---

## 3. Descartando o app-catalogo como causa

Antes de investigar a infra, foi necessário confirmar que o trabalho em andamento no app-catalogo (PREF-322) não tinha relação com o incidente:

- Toda a interação do app-catalogo com o Typesense está em `internal/clients/typesense_client.go` — um único método, `ExportSince`, fazendo `GET /collections/{col}/documents/export`. **Nenhuma operação de escrita** (POST/PUT/DELETE) existe em lugar nenhum do repositório.
- O fluxo de sync (`internal/datasource/typesense.go`) lê do Typesense e grava no **Postgres do próprio app-catalogo** — nunca escreve de volta no Typesense.
- Nenhum branch de trabalho havia sido mergeado em `main` (sem deploy disparado).
- Histórico de shell da máquina de trabalho: zero comandos relacionados a Typesense antes do incidente.

Conclusão: **impossível** que o app-catalogo tenha causado o esvaziamento.

---

## 4. Causa raiz (infraestrutura)

O Typesense é deployado via um Helm chart próprio (`prefeitura-rio/charts`, chart `typesense`), **separado** do deploy do `app-busca-search` (que é só um Deployment stateless que se conecta a ele via secret).

O chart gera a lista de nós do cluster Raft dinamicamente a partir de `.Values.replicas`:

```
{{- define "typesense.replicaString" -}}
{{- $replicaCount := int $.Values.replicas -}}
{{- range $i := until $replicaCount }}
  {{- if $i }},{{- end }}{{ template "typesense.fullname" $ }}-{{ $i }}.{{ template "typesense.fullname" $ }}-cluster:8107:8108
{{- end -}}
{{- end -}}
```

Em staging, o `ConfigMap` (`nodes`) continha **3 peers** (`typesense-0/1/2.typesense-cluster:...`), mas o StatefulSet só tinha **2 réplicas** rodando (`typesense-0`, `typesense-1` — sem `typesense-2`). Os logs do container mostravam exatamente esse sintoma:

```
E raft_server.cpp:182] Unable to resolve host: typesense-2.typesense-cluster
E raft_server.cpp:56] Failed to parse nodes configuration
E typesense_server_utils.cpp:294] Failed to start peering state
```

Isso é **drift entre o Helm values aplicado e o estado real do cluster** — alguém rodou `helm upgrade` com `replicas=3` (ou o contrário) sem sincronizar com o StatefulSet real, causando crash-loop indefinido no container principal (istio-proxy ficava `1/1 Ready`, mascarando o problema num primeiro olhar). Um `data-typesense-2` PVC de idade compatível confirmou que uma 3ª réplica chegou a existir brevemente.

**Correção:** feita pelo time de infra (fora do escopo desta investigação) — alinhar `.Values.replicas` com o número real de réplicas, ou reativar a 3ª réplica. Confirmado corrigido: pods voltaram a `2/2 Running`, 0 restarts.

---

## 5. Por que não havia como simplesmente "ressincronizar"

Pesquisa exaustiva (busca de código em toda a organização GitHub, incluindo clonagem e leitura de `app-busca-search`, `portal-interno`, `charts`, `app-go-api`, `app-mcp-server`) confirmou:

- **Não existe pipeline automatizado** de população do Typesense de Carta de Serviços em nenhum repositório da prefeitura.
- O conteúdo é **curado manualmente, um serviço por vez**, por servidores públicos via `portal-interno` → `POST /api/v1/admin/services` (`app-busca-search`). Não há bulk-import nem CSV para esse domínio (existe bulk apenas para outros domínios: matrículas de curso, propostas MEI, candidaturas de vaga).
- A collection `hub_search` (índice federado, campos `source_type`/`source_collection`/`source_id`) **não tem nenhum escritor em código público** em toda a organização — só a criação do schema e leitores (`app-mcp-server`, uma ferramenta de agente de IA). Provavelmente populada manualmente uma vez, fora de qualquer repositório versionado, ou um recurso planejado e nunca implementado.
- O script `create_service_versions_collection.sh` existe, mas é só um bootstrap manual de **schema** (não de dados).

Ou seja: sem backup, a única forma de recuperar `prefrio_services_base` seria recriar os 50 serviços manualmente pelo portal-interno (lento, sujeito a erro humano) — a menos que se encontrasse uma cópia dos dados em outro lugar.

---

## 6. Fonte de recuperação encontrada

O worker do `app-catalogo` sincroniza periodicamente do Typesense (`TypesenseDataSource`) e grava o **JSON bruto original** de cada serviço na coluna `source_data` do Postgres — funcionando, sem esse ser seu propósito, como uma cópia de segurança.

```sql
SELECT source_data FROM catalog_items WHERE source = 'typesense';
-- 50 registros, intactos, com id/slug/nome_servico/etc. originais
```

Extraído via `kubectl exec` num pod temporário do namespace `catalogo`, conectando ao Postgres real de staging (`postgres.cloudsql-proxy.svc.cluster.local`).

---

## 7. Rota de recuperação: import direto no Typesense vs. loop via CreateService

Duas rotas eram tecnicamente possíveis; a decisão foi pela **rota mais segura**, não a "mais completa":

| | Import direto no Typesense (escolhida) | Loop via `CreateService` (app-busca-search) |
|---|---|---|
| Preserva `id`/`slug` originais | ✅ Sim | ❌ Não — gera UUID e slug novos sempre |
| Gera embeddings (Gemini) | ❌ Não — backfill separado necessário | ✅ Sim |
| Grava histórico em `service_versions` | ❌ Não | ✅ Sim |
| Requer autenticação | Só a API key do Typesense | JWT — mas o middleware do app-busca-search **não valida assinatura** (`internal/middleware/jwt_auth.go`), só decodifica o payload. Usar essa rota exigiria fabricar um JWT não assinado para contornar essa falha real de validação. |

A rota via `CreateService` foi descartada por depender de **explorar uma falha de segurança real do sistema** (ausência de verificação de assinatura JWT) para autenticação — fora do escopo de uma recuperação legítima, e poluiria o `autor` do audit trail com uma identidade fabricada.

---

## 8. O script de seed

Arquivo: `seed-prefrio-services-base.sh` (entregue separadamente).

**O que faz:**
1. Healthcheck do Typesense.
2. Valida que **todas** as linhas do arquivo de entrada têm `id` e `slug` não-vazios (pré-requisito para idempotência e preservação de URL) — aborta sem escrever nada se algo estiver ausente.
3. Confere o estado da collection **antes** do import; recusa rodar se já houver documentos, a menos que `FORCE=1` seja passado explicitamente.
4. Importa via `POST /collections/prefrio_services_base/documents/import?action=upsert` (idempotente).
5. Compara o número de sucessos retornado contra o número esperado de documentos — só reporta "OK" se os números baterem exatamente.
6. Reporta contagem antes/depois e lembra, na própria saída (não só em comentário), do que fica pendente (embeddings, outras 3 collections).

**Uso:**
```bash
kubectl port-forward -n busca svc/typesense 28108:8108 &
TYPESENSE_URL=http://localhost:28108 TYPESENSE_API_KEY=<key> \
  ./seed-prefrio-services-base.sh services.jsonl
```

### 8.1 Revisão e correção do script

O script passou por revisão de um agente Claude independente (Codex não pôde ser usado — exige opt-in `CODEX_COPILOT=1`, não habilitado). A revisão encontrou e eu corrigi:

- **[Crítico]** A versão original comparava só `fail > 0`, nunca `ok == esperado`. Se a chamada de import falhasse por inteiro (key errada, collection errada, HTTP 401/404/500), o Typesense retorna um único JSON de erro (não a resposta linha-a-linha esperada) — `grep -c` não encontrava nem `"success":true` nem `"success":false`, então `ok=0, fail=0`, e o script imprimia **"OK — 0 documentos confirmados"** com exit 0. Reproduzido e confirmado antes da correção.
- **[Alto]** `id`/`slug` nunca eram validados explicitamente antes de escrever — a idempotência e a preservação de URL dependiam disso silenciosamente. Adicionada validação explícita (item 2 acima).
- Adicionados: timeout no curl de import (antes podia travar indefinidamente), guard contra rodar em collection não-vazia sem `FORCE=1`, e o resumo de pendências passou a aparecer também no output de execução (não só no comentário do arquivo).

**Todos os 5 cenários de falha testados** contra um Typesense local descartável (mesmo schema real):

| Teste | Resultado |
|---|---|
| API key errada | Aborta com erro claro (HTTP 401), exit 1 |
| Caminho feliz | 50/50 importados, exit 0 |
| Rodar de novo sem `FORCE=1` | Aborta, exige confirmação explícita |
| Rodar de novo com `FORCE=1` | Upsert idempotente — sem duplicar |
| Doc sem `slug` no arquivo | Aborta antes de escrever qualquer coisa |

---

## 9. Execução e verificação final

Executado contra o Typesense de staging real (após confirmar `kubectl config current-context` = `gke_rj-superapp-staging_...`).

**Resultado:** `50/50` sucesso, `0` falhas.

**Verificação independente pós-execução** (6 dimensões, todas confirmadas):

1. Pods do Typesense seguem `2/2 Running`, 0 restarts — a escrita não desestabilizou o cluster.
2. Logs do Typesense sem erros desde o import.
3. Recontagem fresca da collection: 50.
4. **Comparação de IDs**: os 50 IDs originais (Postgres) e os 50 IDs agora no Typesense são **idênticos** (mesmo `set`) — nenhum faltando, nenhum a mais, sem duplicatas.
5. Todos os campos-chave presentes e não-vazios nos 50 documentos importados; `nome_servico` idêntico ao original em 100% dos casos.
6. Busca funcional testada com 4 termos diversos (`iptu`, `alvara`, `certidao`, `meio ambiente`) — todos retornaram resultados corretos.

---

## 10. Estado final

| Collection | Documentos | Situação |
|---|---|---|
| `prefrio_services_base` | **50** | ✅ Restaurada, verificada |
| `hub_search` | 0 | Sem dado de origem — nenhum código na org escreve nela |
| `service_versions` | 0 | Audit trail — populará organicamente com novas edições |
| `tombamentos_overlay` | 0 | Sem dado de origem |

---

## 11. Pendências / follow-ups

1. **Backfill de embeddings** nos 50 serviços restaurados — busca textual funciona normalmente; busca semântica/híbrida no Typesense fica degradada até um backfill (via Gemini) rodar. Fora do escopo desta recuperação.
2. **Causa raiz de infra**: entender por que houve drift entre `.Values.replicas` do Helm e o StatefulSet real, para evitar recorrência (ex.: processo de deploy do Typesense não estar totalmente via GitOps/Helm consistente).
3. **Considerar snapshot/backup automatizado do Typesense** — hoje não existe nenhum, e o único motivo da recuperação ter sido possível foi o app-catalogo sincronizar (sem esse ser seu propósito) uma cópia dos dados.
4. Avisar o time (Bruno/Gabriel/Lucas) que os 50 serviços voltaram a aparecer na busca de staging.

---

## Apêndice — repositórios investigados

| Repositório | Papel nesta investigação |
|---|---|
| `app-catalogo` | Consumidor read-only do Typesense; fonte de recuperação (Postgres com `source_data`) |
| `app-busca-search` | Dono do Typesense — CRUD admin, schema, migração de versão |
| `portal-interno` | UI usada por servidores públicos para curar serviços (único "populador" real) |
| `charts` | Helm chart do Typesense — causa raiz do crash-loop |
| `app-go-api` | Tem seu próprio uso (não relacionado) de Typesense — collections `cursos`/`empregos`, só leitura |
| `app-mcp-server` | Consumidor de `hub_search` via ferramenta de agente de IA — não escreve |
