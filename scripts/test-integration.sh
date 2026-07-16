#!/usr/bin/env bash

set -euo pipefail

readonly repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly compose_file="${repository_root}/docker-compose.integration.yml"
readonly compose_project_name="app-catalogo-integration-${USER:-operator}-$$"

compose() {
  docker compose --project-name "${compose_project_name}" --file "${compose_file}" "$@"
}

cleanup() {
  compose down --volumes --remove-orphans >/dev/null 2>&1 || true
}

published_port() {
  local service_name="$1"
  local container_port="$2"
  local endpoint
  local host_port

  endpoint="$(compose port "${service_name}" "${container_port}")"
  host_port="${endpoint##*:}"
  if [[ ! "${host_port}" =~ ^[0-9]+$ ]]; then
    printf 'Could not resolve the published port for %s:%s from %q.\n' \
      "${service_name}" "${container_port}" "${endpoint}" >&2
    return 1
  fi
  printf '%s\n' "${host_port}"
}

trap cleanup EXIT INT TERM

cd "${repository_root}"
compose up --detach --wait

readonly postgres_port="$(published_port postgres 5432)"
readonly redis_port="$(published_port redis 6379)"
readonly test_database_url="postgres://catalogo:catalogo@127.0.0.1:${postgres_port}/catalogo_test?sslmode=disable"

export DB_HOST=127.0.0.1
export DB_PORT="${postgres_port}"
export DB_USER=catalogo
export DB_PASSWORD=catalogo
export DB_NAME=catalogo_test
export DB_SSL_MODE=disable
export REDIS_HOST=127.0.0.1
export REDIS_PORT="${redis_port}"
export APP_CATALOGO_SEARCH_TEST_DATABASE_URL="${test_database_url}"
export APP_CATALOGO_EMBEDDING_TEST_DATABASE_URL="${test_database_url}"
export APP_CATALOGO_RATE_LIMIT_TEST_REDIS_ADDR="127.0.0.1:${redis_port}"

go run cmd/migrate/main.go up

export APP_CATALOGO_MIGRATION_TEST_DATABASE_URL="${test_database_url}"
go test ./internal/repository -run '^Test(CatalogRevision|ServiceSlugAlias)MigrationRoundTrip$' -count=1 -v
unset APP_CATALOGO_MIGRATION_TEST_DATABASE_URL

go test ./... -count=1 -v -race -coverprofile=coverage.out -timeout 2m
