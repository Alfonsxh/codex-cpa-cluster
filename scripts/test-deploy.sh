#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/cpa-deploy-contract.XXXXXX")
trap 'rm -rf -- "$TEST_ROOT"' EXIT HUP INT TERM
CONFIG_FILE="$TEST_ROOT/etc/cpac/config.env"

CPAC_ALLOW_NON_ROOT=true sh "$ROOT_DIR/scripts/deploy.sh" domain set QData.Example.COM. \
  --yes --config "$CONFIG_FILE" >/dev/null
[ "$(cat "$CONFIG_FILE")" = "CPA_DOMAIN=qdata.example.com" ] || {
  echo "deploy.sh did not normalize and persist the domain" >&2
  exit 1
}
mode=$(stat -c '%a' "$CONFIG_FILE" 2>/dev/null || stat -f '%Lp' "$CONFIG_FILE")
[ "$mode" = 600 ] || { echo "deploy.sh config mode = $mode" >&2; exit 1; }

if CPAC_ALLOW_NON_ROOT=true sh "$ROOT_DIR/scripts/deploy.sh" domain set invalid \
  --yes --config "$CONFIG_FILE" >/dev/null 2>&1; then
  echo "deploy.sh accepted an invalid FQDN" >&2
  exit 1
fi
[ "$(cat "$CONFIG_FILE")" = "CPA_DOMAIN=qdata.example.com" ] || {
  echo "invalid domain changed the stored config" >&2
  exit 1
}

MISSING_CONFIG="$TEST_ROOT/missing/config.env"
if CPAC_ALLOW_NON_ROOT=true \
  CPAC_DEPLOY_ROOT="$TEST_ROOT/deploy" \
  sh "$ROOT_DIR/scripts/deploy.sh" deploy --config "$MISSING_CONFIG" </dev/null >/dev/null 2>&1; then
  echo "non-interactive first deploy accepted a missing domain" >&2
  exit 1
fi

PENDING="$TEST_ROOT/etc/cpac/bootstrap-admin.key"
printf '%s\n' 'pending-secret-must-remain' >"$PENDING"
chmod 0600 "$PENDING"
if CPAC_CONFIG_FILE="$CONFIG_FILE" CPAC_ALLOW_NON_ROOT=true \
  sh "$ROOT_DIR/scripts/deploy.sh" admin-key claim </dev/null >/dev/null 2>&1; then
  echo "non-interactive admin-key claim unexpectedly succeeded" >&2
  exit 1
fi
[ -f "$PENDING" ] || { echo "failed claim removed pending key" >&2; exit 1; }

for removed in "$ROOT_DIR/scripts/cpac" "$ROOT_DIR/scripts/install-cpac.sh" "$ROOT_DIR/scripts/deploy-target.sh"; do
  [ ! -e "$removed" ] || { echo "removed deployment entry still exists: $removed" >&2; exit 1; }
done
sh "$ROOT_DIR/scripts/deploy.sh" help | grep -Fq "sudo $ROOT_DIR/scripts/deploy.sh"

printf '%s\n' 'single deploy.sh contract tests passed'
