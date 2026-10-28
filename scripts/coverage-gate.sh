#!/usr/bin/env bash
# Gate de cobertura do CI.
#
# Uso: scripts/coverage-gate.sh <coverage.out>
#
# Falha quando:
#   - algum pacote de internal/ não tem nenhum arquivo _test.go;
#   - a cobertura total do perfil fica abaixo do baseline menos a tolerância.
#
# O baseline é o número em .github/coverage-baseline. Com COVERAGE_BASELINE_REF
# (ex.: origin/main), vale o arquivo dessa ref: um PR não baixa o próprio limite.
# Para subir o baseline, atualize o arquivo no PR que aumentou a cobertura.
#
# Variáveis: COVERAGE_BASELINE_REF (opcional), COVERAGE_TOLERANCE (padrão 0.1,
# em pontos percentuais, para variação de caminhos concorrentes sob -race).
set -euo pipefail

BASELINE_FILE=".github/coverage-baseline"
PROFILE="${1:-}"
TOLERANCE="${COVERAGE_TOLERANCE:-0.1}"
NUMBER_RE='^[0-9]+([.][0-9]+)?$'

fail() {
	echo "::error::$1"
	exit 1
}

[[ -n "$PROFILE" ]] || fail "uso: $0 <coverage.out>"
[[ -f "$PROFILE" ]] || fail "perfil de cobertura não encontrado: $PROFILE"
[[ "$TOLERANCE" =~ $NUMBER_RE ]] || fail "COVERAGE_TOLERANCE inválida: '$TOLERANCE'"

# 1. Todo pacote de internal/ precisa de teste.
untested=$(go list -f '{{if and .GoFiles (not .TestGoFiles) (not .XTestGoFiles)}}{{.ImportPath}}{{end}}' ./internal/...)
if [[ -n "$untested" ]]; then
	echo "Pacotes de internal/ sem nenhum teste:"
	echo "$untested" | sed 's/^/  - /'
	fail "todo pacote de internal/ precisa de ao menos um arquivo _test.go"
fi
echo "Todos os pacotes de internal/ têm teste."

# 2. Cobertura total contra o baseline.
current=$(go tool cover -func="$PROFILE" | awk '$1 == "total:" { sub(/%$/, "", $NF); print $NF }')
[[ "$current" =~ $NUMBER_RE ]] || fail "não foi possível ler a cobertura total de $PROFILE"

baseline_source="$BASELINE_FILE"
baseline=""
if [[ -n "${COVERAGE_BASELINE_REF:-}" ]]; then
	if baseline=$(git show "${COVERAGE_BASELINE_REF}:${BASELINE_FILE}" 2>/dev/null); then
		baseline_source="${COVERAGE_BASELINE_REF}:${BASELINE_FILE}"
	else
		echo "Baseline ausente em ${COVERAGE_BASELINE_REF}; usando o arquivo da árvore de trabalho."
		baseline=""
	fi
fi
if [[ -z "$baseline" ]]; then
	[[ -f "$BASELINE_FILE" ]] || fail "baseline não encontrado: $BASELINE_FILE"
	baseline=$(<"$BASELINE_FILE")
fi
baseline=$(echo "$baseline" | tr -d '[:space:]')
[[ "$baseline" =~ $NUMBER_RE ]] || fail "baseline inválido em ${baseline_source}: '$baseline'"

status=$(awk -v c="$current" -v b="$baseline" -v t="$TOLERANCE" 'BEGIN {
	if (c + t < b) print "abaixo"; else if (c > b + 0.5) print "acima"; else print "ok"
}')
diff=$(awk -v c="$current" -v b="$baseline" 'BEGIN { printf "%+.1f", c - b }')

echo "Cobertura total: ${current}% | baseline: ${baseline}% (${baseline_source}) | diferença: ${diff} p.p. | tolerância: ${TOLERANCE} p.p."

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
	{
		echo "## Cobertura"
		echo ""
		echo "| Baseline | Atual | Diferença |"
		echo "|---|---|---|"
		echo "| ${baseline}% | ${current}% | ${diff} p.p. |"
	} >>"$GITHUB_STEP_SUMMARY"
fi

case "$status" in
abaixo)
	fail "a cobertura caiu de ${baseline}% para ${current}% (${diff} p.p., tolerância ${TOLERANCE} p.p.); adicione testes para o código novo"
	;;
acima)
	echo "::notice::a cobertura subiu para ${current}%; atualize ${BASELINE_FILE} para travar o ganho"
	;;
esac
echo "Gate de cobertura: OK"
