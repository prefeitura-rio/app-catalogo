#!/usr/bin/env bash
# Decide se o deploy de staging pode pular o build (só mudaram arquivos que não
# entram na imagem: k8s/, docs/, *.md, .env.example).
#
# Uso: scripts/deploy-staging-check.sh   (no workflow deploy-staging.yaml)
#
# A base do diff é o último commit implantado com sucesso em staging, não o push
# anterior (github.event.before): se um deploy falhou antes de promover a imagem,
# o push seguinte precisa levar o código dele, mesmo que ele próprio só mude docs.
# O último deploy é o head_sha do run bem-sucedido mais recente deste workflow na
# branch em que o step DEPLOY_STEP terminou com sucesso (um run que pulou o build
# também termina em "success", mas não implanta nada).
#
# Falha segura: sem base (API indisponível, nenhum deploy achado) ou com a base
# fora da história de GITHUB_SHA, o build é completo.
#
# Grava k8s_only=true|false em GITHUB_OUTPUT e o motivo em GITHUB_STEP_SUMMARY.
#
# Variáveis: GITHUB_REPOSITORY, GITHUB_SHA, GITHUB_OUTPUT (obrigatórias); GH_TOKEN
# (para o gh); PUSH_BEFORE (github.event.before, só para o aviso de divergência);
# DEPLOY_EXCLUDE_RUN_ID (o run atual); DEPLOY_WORKFLOW (padrão deploy-staging.yaml);
# DEPLOY_BRANCH (padrão main); DEPLOY_STEP (padrão "Promote to active");
# DEPLOY_MAX_RUNS (runs bem-sucedidos consultados, padrão 30).
set -euo pipefail

WORKFLOW="${DEPLOY_WORKFLOW:-deploy-staging.yaml}"
BRANCH="${DEPLOY_BRANCH:-main}"
STEP="${DEPLOY_STEP:-Promote to active}"
MAX_RUNS="${DEPLOY_MAX_RUNS:-30}"
EXCLUDE_RUN_ID="${DEPLOY_EXCLUDE_RUN_ID:-}"
PUSH_BEFORE="${PUSH_BEFORE:-}"
SHA_RE='^[0-9a-f]{40}$'

: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY obrigatória}"
: "${GITHUB_SHA:?GITHUB_SHA obrigatória}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT obrigatória}"

summary() {
	if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
		echo "$1" >>"$GITHUB_STEP_SUMMARY"
	fi
}

decide() {
	echo "k8s_only=$1" >>"$GITHUB_OUTPUT"
	echo "$2"
	summary "$2"
	exit 0
}

# Imprime o head_sha do último deploy concluído; nada se não achar ou se a API falhar.
last_deployed_sha() {
	local runs id sha promoted
	runs=$(gh api -X GET "repos/${GITHUB_REPOSITORY}/actions/workflows/${WORKFLOW}/runs" \
		-f branch="$BRANCH" -f event=push -f status=success -f per_page="$MAX_RUNS" \
		--jq '.workflow_runs[] | "\(.id) \(.head_sha)"') || return 0
	while read -r id sha; do
		[[ -n "$id" ]] || continue
		[[ "$id" != "$EXCLUDE_RUN_ID" ]] || continue
		promoted=$(gh api "repos/${GITHUB_REPOSITORY}/actions/runs/${id}/jobs?per_page=100" |
			jq -r --arg step "$STEP" \
				'[.jobs[].steps[]? | select(.name == $step and .conclusion == "success")] | length') || return 0
		if [[ "$promoted" =~ ^[1-9][0-9]*$ && "$sha" =~ $SHA_RE ]]; then
			echo "$sha"
			return 0
		fi
	done <<<"$runs"
}

BASE=$(last_deployed_sha)

if [[ -z "$BASE" ]]; then
	echo "::warning::Último deploy de staging não encontrado (${WORKFLOW}, step \"${STEP}\"): build completo."
	decide false "Último deploy de staging não encontrado: build completo."
fi

if ! git merge-base --is-ancestor "$BASE" "$GITHUB_SHA" 2>/dev/null; then
	echo "::warning::O último deploy de staging (${BASE:0:7}) não está na história de ${GITHUB_SHA:0:7}: build completo."
	decide false "Último deploy de staging (\`${BASE:0:7}\`) fora da história de \`${GITHUB_SHA:0:7}\`: build completo."
fi

if [[ -n "$PUSH_BEFORE" && "$BASE" != "$PUSH_BEFORE" ]]; then
	echo "::warning::Staging está em ${BASE:0:7}, não no push anterior (${PUSH_BEFORE:0:7}): um deploy entre eles não chegou a promover a imagem. As mudanças são comparadas com ${BASE:0:7}."
	summary "⚠️ Staging está em \`${BASE:0:7}\`, não no push anterior (\`${PUSH_BEFORE:0:7}\`): um deploy entre eles não chegou a promover a imagem."
fi

CHANGED_FILES=$(git diff --name-only "$BASE" "$GITHUB_SHA")
if [[ -z "$CHANGED_FILES" ]]; then
	decide false "Nenhuma diferença para o último deploy de staging (\`${BASE:0:7}\`), como numa re-execução: build completo."
fi
NON_DEPLOYABLE=$(echo "$CHANGED_FILES" | grep -vE '^k8s/|^docs/|\.md$|^\.env\.example$' || true)

if [[ -z "$NON_DEPLOYABLE" ]]; then
	decide true "Desde o último deploy de staging (\`${BASE:0:7}\`) só mudaram arquivos que não entram na imagem (k8s, docs, markdown): build pulado, staging segue na imagem de \`${BASE:0:7}\`."
fi

decide false "Mudanças implantáveis desde o último deploy de staging (\`${BASE:0:7}\`): build e deploy de \`${GITHUB_SHA:0:7}\`."
