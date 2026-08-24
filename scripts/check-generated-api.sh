#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
CODEGEN_TMP=$(mktemp -d "${TMPDIR:-/tmp}/cpa-openapi.XXXXXX")

cleanup() {
  case "$CODEGEN_TMP" in
    */cpa-openapi.*) rm -rf -- "$CODEGEN_TMP" ;;
    *) echo "拒绝清理非预期临时目录：$CODEGEN_TMP" >&2 ;;
  esac
}
trap cleanup EXIT HUP INT TERM

go tool oapi-codegen \
  -config "$ROOT_DIR/api/oapi-codegen.yaml" \
  -o "$CODEGEN_TMP/models.gen.go" \
  "$ROOT_DIR/api/openapi.yaml"
CPA_OPENAPI_TS_OUTPUT="$CODEGEN_TMP/frontend" \
  npm --prefix "$ROOT_DIR/tools/openapi" run generate

if ! cmp -s "$ROOT_DIR/internal/contract/models.gen.go" "$CODEGEN_TMP/models.gen.go"; then
  echo "Go API 契约代码已过期；请运行 make generate-api" >&2
  diff -u "$ROOT_DIR/internal/contract/models.gen.go" "$CODEGEN_TMP/models.gen.go" || true
  exit 1
fi
if ! diff -ru "$ROOT_DIR/frontend/src/api/generated" "$CODEGEN_TMP/frontend"; then
  echo "TypeScript API 契约代码已过期；请运行 make generate-api" >&2
  exit 1
fi
