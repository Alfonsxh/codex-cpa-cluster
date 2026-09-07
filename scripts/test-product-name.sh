#!/usr/bin/env sh
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/cpa-pool-name-test.XXXXXX")
trap 'rm -rf -- "$TEST_ROOT"' EXIT HUP INT TERM
FORBIDDEN_NAME='Codex CPA Clu''ster'
FORBIDDEN_SLUG='codex-cpa-clu''ster'
printf '%s\n' 'Codex CPA Pool' >"$TEST_ROOT/page.html"
sh "$ROOT_DIR/scripts/check-product-name.sh" "$TEST_ROOT" >/dev/null
for residue in "$FORBIDDEN_NAME" "CPA"'C_INGRESS_MODE=managed' "$FORBIDDEN_SLUG.tar.gz"; do
  printf '%s\n' "$residue" >"$TEST_ROOT/page.html"
  if sh "$ROOT_DIR/scripts/check-product-name.sh" "$TEST_ROOT" >/dev/null 2>&1; then
    echo 'name gate accepted a product or protocol regression' >&2
    exit 1
  fi
done
printf 'https://github.com/Alfonsxh/%s/releases/latest\n' "$FORBIDDEN_SLUG" >"$TEST_ROOT/page.html"
if sh "$ROOT_DIR/scripts/check-product-name.sh" "$TEST_ROOT" >/dev/null 2>&1; then
  echo 'name gate accepted a retired repository URL' >&2
  exit 1
fi
printf '%s\n' 'https://github.com/Alfonsxh/codex-cpa-pool/releases/latest' >"$TEST_ROOT/page.html"
sh "$ROOT_DIR/scripts/check-product-name.sh" "$TEST_ROOT" >/dev/null
printf 'module github.com/Alfonsxh/%s\n' "$FORBIDDEN_SLUG" >"$TEST_ROOT/go.mod"
if sh "$ROOT_DIR/scripts/check-product-name.sh" "$TEST_ROOT" >/dev/null 2>&1; then
  echo 'name gate accepted the old Go module as an external hosting identity' >&2
  exit 1
fi
rm "$TEST_ROOT/go.mod"
mkdir "$TEST_ROOT/internal"
printf 'package example\nimport "github.com/Alfonsxh/%s/internal/controlplane"\n' \
  "$FORBIDDEN_SLUG" >"$TEST_ROOT/internal/example.go"
if sh "$ROOT_DIR/scripts/check-product-name.sh" "$TEST_ROOT" >/dev/null 2>&1; then
  echo 'name gate accepted an old internal import as an external hosting identity' >&2
  exit 1
fi
rm "$TEST_ROOT/internal/example.go"
printf '%s\n' 'Pool asset' >"$TEST_ROOT/$FORBIDDEN_SLUG.svg"
if sh "$ROOT_DIR/scripts/check-product-name.sh" "$TEST_ROOT" >/dev/null 2>&1; then
  echo 'name gate accepted a legacy asset filename' >&2
  exit 1
fi
printf '%s\n' 'Pool product-name regression tests passed'
