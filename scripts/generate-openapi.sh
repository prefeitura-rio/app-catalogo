#!/usr/bin/env bash

set -euo pipefail

readonly generated_swagger_path="docs/swagger.json"
readonly generated_openapi_path="docs/openapi-v3.json"
converted_openapi_path="$(mktemp "docs/.converted-openapi-v3.json.XXXXXX")"
validated_openapi_path="$(mktemp "docs/.validated-openapi-v3.json.XXXXXX")"
readonly converted_openapi_path validated_openapi_path
trap 'rm -f "$generated_swagger_path" "$converted_openapi_path" "$validated_openapi_path"' EXIT

go tool swag init \
  -g cmd/api/main.go \
  --output docs \
  --outputTypes json \
  --requiredByDefault
go run ./cmd/openapi-convert convert "$generated_swagger_path" "$converted_openapi_path"
jq -f scripts/postprocess-openapi-v3.jq "$converted_openapi_path" > "$validated_openapi_path"
go run ./cmd/openapi-convert validate "$validated_openapi_path"
chmod 0644 "$validated_openapi_path"
mv "$validated_openapi_path" "$generated_openapi_path"
