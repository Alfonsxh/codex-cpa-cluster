#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

mkdir -p "$ROOT_DIR/internal/contract"
go tool oapi-codegen \
  -config "$ROOT_DIR/api/oapi-codegen.yaml" \
  -o "$ROOT_DIR/internal/contract/models.gen.go" \
  "$ROOT_DIR/api/openapi.yaml"
npm --prefix "$ROOT_DIR/tools/openapi" run generate
