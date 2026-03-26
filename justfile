# app-catalogo Development Commands
# Run 'just' to see all available commands

default:
    @just --list

# Start infrastructure
up:
    @docker-compose up -d

# Stop infrastructure
down:
    @docker-compose down

# Run API + worker com hot reload (requer air)
dev:
    #!/usr/bin/env bash
    set -euo pipefail
    just up
    trap 'kill 0' EXIT
    air -c .air.api.toml & air -c .air.worker.toml &
    wait

# Run só a API com hot reload
dev-api:
    @just up
    air -c .air.api.toml

# Run só o worker com hot reload
dev-worker:
    @just up
    air -c .air.worker.toml

# Run migrations
migrate:
    go run cmd/migrate/main.go up

# Create new migration
migrate-create NAME:
    goose -dir db/migrations create {{NAME}} sql

# Run all tests
test:
    go test ./... -v -race -coverprofile=coverage.out

# Format code
fmt:
    go fmt ./...
    gofmt -s -w .

# Lint
lint:
    golangci-lint run

# Build all binaries
build:
    go build -o bin/catalogo-api cmd/api/main.go
    go build -o bin/catalogo-worker cmd/worker/main.go
    go build -o bin/catalogo-migrate cmd/migrate/main.go

# Database shell
db:
    docker-compose exec postgres psql -U ${DB_USER:-catalogo} -d ${DB_NAME:-catalogo}

# Reset everything (DESTRUCTIVE)
reset:
    @echo "WARNING: This deletes ALL data. Press Enter to continue, Ctrl+C to cancel"
    @read
    docker-compose down -v
    just up
    just migrate
