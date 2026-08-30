#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/cpac-contract.XXXXXX")
trap 'rm -rf -- "$TEST_ROOT"' EXIT HUP INT TERM
CONFIG_FILE="$TEST_ROOT/etc/cpac/config.env"

CPAC_ALLOW_NON_ROOT=true sh "$ROOT_DIR/scripts/cpac" domain set QData.Example.COM. \
  --yes --config "$CONFIG_FILE" >/dev/null
[ "$(cat "$CONFIG_FILE")" = "CPA_DOMAIN=qdata.example.com" ] || {
  echo "cpac did not normalize and persist the domain" >&2
  exit 1
}
mode=$(stat -f '%Lp' "$CONFIG_FILE" 2>/dev/null || stat -c '%a' "$CONFIG_FILE")
[ "$mode" = 600 ] || { echo "cpac config mode = $mode" >&2; exit 1; }

if CPAC_ALLOW_NON_ROOT=true sh "$ROOT_DIR/scripts/cpac" domain set invalid \
  --yes --config "$CONFIG_FILE" >/dev/null 2>&1; then
  echo "cpac accepted an invalid FQDN" >&2
  exit 1
fi
[ "$(cat "$CONFIG_FILE")" = "CPA_DOMAIN=qdata.example.com" ] || {
  echo "invalid domain changed the stored config" >&2
  exit 1
}

MISSING_CONFIG="$TEST_ROOT/missing/config.env"
if CPAC_ALLOW_NON_ROOT=true \
  CPAC_DEPLOY_ROOT="$TEST_ROOT/deploy" \
  sh "$ROOT_DIR/scripts/cpac" deploy --config "$MISSING_CONFIG" </dev/null >/dev/null 2>&1; then
  echo "non-interactive first deploy accepted a missing domain" >&2
  exit 1
fi

PENDING="$TEST_ROOT/etc/cpac/bootstrap-admin.key"
printf '%s\n' 'pending-secret-must-remain' >"$PENDING"
chmod 0600 "$PENDING"
if CPAC_CONFIG_FILE="$CONFIG_FILE" CPAC_ALLOW_NON_ROOT=true \
  sh "$ROOT_DIR/scripts/cpac" admin-key claim </dev/null >/dev/null 2>&1; then
  echo "non-interactive admin-key claim unexpectedly succeeded" >&2
  exit 1
fi
[ -f "$PENDING" ] || { echo "failed claim removed pending key" >&2; exit 1; }

printf '%s\n' 'cpac contract tests passed'
