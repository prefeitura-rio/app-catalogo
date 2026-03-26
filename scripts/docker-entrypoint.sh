#!/bin/sh
set -e

echo "Iniciando app-catalogo..."

if [ -f "$(pwd)/.env" ]; then
    set -a
    . "$(pwd)/.env"
    set +a
fi

export DB_HOST="${DB_HOST:-postgres}"
export DB_PORT="${DB_PORT:-5432}"
export DB_USER="${DB_USER:-catalogo}"
export DB_PASSWORD="${DB_PASSWORD:-catalogo}"
export DB_NAME="${DB_NAME:-catalogo}"
export DB_SSL_MODE="${DB_SSL_MODE:-disable}"
export SERVER_HOST="${SERVER_HOST:-0.0.0.0}"
export SERVER_PORT="${SERVER_PORT:-8080}"
export APP_ENV="${APP_ENV:-production}"

if [ "$RUN_MIGRATIONS" = "true" ]; then
    echo "Executando migrations..."
    goose -dir db/migrations postgres \
        "user=${DB_USER} password=${DB_PASSWORD} dbname=${DB_NAME} host=${DB_HOST} port=${DB_PORT} sslmode=${DB_SSL_MODE}" \
        up
    echo "Migrations concluidas."
fi

echo "Executando: $@"
exec "$@"
