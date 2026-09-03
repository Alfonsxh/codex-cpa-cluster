#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/cpa-deploy-contract.XXXXXX")
trap 'rm -rf -- "$TEST_ROOT"' EXIT HUP INT TERM
OPERATOR_ROOT="$TEST_ROOT/home/cpac"
CONFIG_FILE="$OPERATOR_ROOT/config.env"
LEGACY_CONFIG_FILE="$TEST_ROOT/etc/cpac/config.env"
mkdir -p "$OPERATOR_ROOT"

run_operator_script() {
  CPAC_ALLOW_NON_ROOT=true \
    CPAC_STAGING_ROOT="$OPERATOR_ROOT" \
    CPAC_LEGACY_CONFIG_FILE="$LEGACY_CONFIG_FILE" \
    sh "$ROOT_DIR/scripts/run.sh" "$@"
}

run_operator_script domain set QData.Example.COM. --yes >/dev/null
[ "$(cat "$CONFIG_FILE")" = "CPA_DOMAIN=qdata.example.com" ] || {
  echo "run.sh did not normalize and persist the domain" >&2
  exit 1
}
mode=$(stat -c '%a' "$CONFIG_FILE" 2>/dev/null || stat -f '%Lp' "$CONFIG_FILE")
[ "$mode" = 600 ] || { echo "run.sh config mode = $mode" >&2; exit 1; }

if run_operator_script domain set invalid --yes >/dev/null 2>&1; then
  echo "run.sh accepted an invalid FQDN" >&2
  exit 1
fi
[ "$(cat "$CONFIG_FILE")" = "CPA_DOMAIN=qdata.example.com" ] || {
  echo "invalid domain changed the stored config" >&2
  exit 1
}

run_operator_script ingress set external --yes >/dev/null
[ "$(cat "$CONFIG_FILE")" = "$(printf 'CPA_DOMAIN=qdata.example.com\nCPAC_INGRESS_MODE=external')" ] || {
  echo "run.sh did not persist the external ingress mode" >&2
  exit 1
}
if run_operator_script run --ingress managed </dev/null >/dev/null 2>&1; then
  echo "run.sh silently switched a persisted ingress mode" >&2
  exit 1
fi
run_operator_script ingress set managed --yes >/dev/null
[ "$(cat "$CONFIG_FILE")" = "$(printf 'CPA_DOMAIN=qdata.example.com\nCPAC_INGRESS_MODE=managed')" ] || {
  echo "run.sh did not persist the managed ingress mode" >&2
  exit 1
}

MISSING_CONFIG="$TEST_ROOT/missing/config.env"
if CPAC_DEPLOY_ROOT="$TEST_ROOT/deploy" \
  run_operator_script run --config "$MISSING_CONFIG" </dev/null >/dev/null 2>&1; then
  echo "non-interactive first deploy accepted a missing domain" >&2
  exit 1
fi
if CPAC_DEPLOY_ROOT="$TEST_ROOT/deploy" \
  run_operator_script run --domain qdata.example.com --config "$MISSING_CONFIG" \
    </dev/null >/dev/null 2>&1; then
  echo "non-interactive first deploy accepted no ingress selection" >&2
  exit 1
fi

rm -f -- "$CONFIG_FILE"
mkdir -p "$(dirname -- "$LEGACY_CONFIG_FILE")"
printf '%s\n' 'CPA_DOMAIN=legacy.example.com' >"$LEGACY_CONFIG_FILE"
chmod 0600 "$LEGACY_CONFIG_FILE"
LEGACY_PENDING="$(dirname -- "$LEGACY_CONFIG_FILE")/bootstrap-admin.key"
printf '%s\n' 'pending-secret-must-remain' >"$LEGACY_PENDING"
chmod 0600 "$LEGACY_PENDING"
run_operator_script domain set Legacy.Example.COM. --yes --config "$LEGACY_CONFIG_FILE" >/dev/null
[ "$(cat "$CONFIG_FILE")" = "$(printf 'CPA_DOMAIN=legacy.example.com\nCPAC_INGRESS_MODE=managed')" ] \
  || { echo "legacy domain config was not migrated" >&2; exit 1; }
PENDING="$OPERATOR_ROOT/bootstrap-admin.key"
[ "$(cat "$PENDING")" = 'pending-secret-must-remain' ] \
  || { echo "legacy pending admin key was not migrated" >&2; exit 1; }
[ ! -e "$LEGACY_CONFIG_FILE" ] && [ ! -e "$LEGACY_PENDING" ] \
  || { echo "legacy operator state remained after migration" >&2; exit 1; }
[ ! -e "$(dirname -- "$LEGACY_CONFIG_FILE")" ] \
  || { echo "empty legacy config directory remained after migration" >&2; exit 1; }

mkdir -p "$(dirname -- "$LEGACY_CONFIG_FILE")"
printf '%s\n' 'CPA_DOMAIN=conflict.example.com' >"$LEGACY_CONFIG_FILE"
chmod 0600 "$LEGACY_CONFIG_FILE"
if run_operator_script domain set legacy.example.com --yes >/dev/null 2>&1; then
  echo "run.sh accepted conflicting old and new domain configs" >&2
  exit 1
fi
[ "$(cat "$CONFIG_FILE")" = "$(printf 'CPA_DOMAIN=legacy.example.com\nCPAC_INGRESS_MODE=managed')" ] \
  && [ "$(cat "$LEGACY_CONFIG_FILE")" = 'CPA_DOMAIN=conflict.example.com' ] \
  || { echo "conflicting migration mutated operator config" >&2; exit 1; }
rm -f -- "$LEGACY_CONFIG_FILE"
rmdir -- "$(dirname -- "$LEGACY_CONFIG_FILE")"

mkdir -p "$(dirname -- "$LEGACY_CONFIG_FILE")"
printf '%s\n' 'CPA_DOMAIN=legacy.example.com' >"$LEGACY_CONFIG_FILE"
chmod 0600 "$LEGACY_CONFIG_FILE"
LEGACY_PENDING="$(dirname -- "$LEGACY_CONFIG_FILE")/bootstrap-admin.key"
printf '%s\n' 'different-pending-secret' >"$LEGACY_PENDING"
chmod 0600 "$LEGACY_PENDING"
if run_operator_script domain set legacy.example.com --yes >/dev/null 2>&1; then
  echo "run.sh accepted conflicting old and new pending admin keys" >&2
  exit 1
fi
[ "$(cat "$PENDING")" = 'pending-secret-must-remain' ] \
  && [ "$(cat "$LEGACY_PENDING")" = 'different-pending-secret' ] \
  || { echo "conflicting pending key migration mutated credentials" >&2; exit 1; }
rm -f -- "$LEGACY_CONFIG_FILE" "$LEGACY_PENDING"
rmdir -- "$(dirname -- "$LEGACY_CONFIG_FILE")"

printf '%s\n' 'pending-secret-must-remain' >"$PENDING"
chmod 0600 "$PENDING"
if run_operator_script admin-key claim </dev/null >/dev/null 2>&1; then
  echo "non-interactive admin-key claim unexpectedly succeeded" >&2
  exit 1
fi
[ -f "$PENDING" ] || { echo "failed claim removed pending key" >&2; exit 1; }

for removed in "$ROOT_DIR/scripts/cpac" "$ROOT_DIR/scripts/install-cpac.sh" "$ROOT_DIR/scripts/deploy-target.sh"; do
  [ ! -e "$removed" ] || { echo "removed deployment entry still exists: $removed" >&2; exit 1; }
done
sh "$ROOT_DIR/scripts/run.sh" help | grep -Fq "sudo $ROOT_DIR/scripts/run.sh"
sh "$ROOT_DIR/scripts/run.sh" help | grep -Fq "sudo $ROOT_DIR/scripts/run.sh run [--domain DOMAIN]"
if run_operator_script deploy >/dev/null 2>&1; then
  echo "run.sh still accepted the removed deploy command" >&2
  exit 1
fi

printf '%s\n' 'single run.sh contract tests passed'
