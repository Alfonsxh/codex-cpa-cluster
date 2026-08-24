#!/usr/bin/env sh
set -eu

MIGRATION_ROOT=${MIGRATION_ROOT:-/home/claude/codex-cpa-go-v2-20260821-54fd5828}
V1_ROOT=${MIGRATION_V1_ROOT:-/home/claude/CLIProxyAPI-v2-baseline}
V2_ROOT=${MIGRATION_V2_ROOT:-/home/claude/CLIProxyAPI-v2-candidate}
: "${MIGRATION_V1_BASE_URL:?MIGRATION_V1_BASE_URL is required}"
: "${MIGRATION_V2_BASE_URL:?MIGRATION_V2_BASE_URL is required}"
V1_BASE_URL=$MIGRATION_V1_BASE_URL
V2_BASE_URL=$MIGRATION_V2_BASE_URL
TEST_KEY_FILE=${MIGRATION_TEST_KEY_FILE:-$MIGRATION_ROOT/evidence/test-only.key}
FIXTURE_SCRIPT=${MIGRATION_FIXTURE_SCRIPT:-$MIGRATION_ROOT/fixtures-20260822/incoming/migration-test-upstream-fixture.sh}
DATA_COMPARE_SCRIPT=${MIGRATION_DATA_COMPARE_SCRIPT:-$MIGRATION_ROOT/scripts/migration-data-plane-compare.py}
ROUTE_COMPARE_WRAPPER=${MIGRATION_ROUTE_COMPARE_WRAPPER:-$MIGRATION_ROOT/scripts/migration-data-plane-route-remote.sh}
RUN_ID=${MIGRATION_RUN_ID:-$(date +%Y%m%dT%H%M%SCST)}
DATA_OUTPUT=${MIGRATION_DATA_OUTPUT:-$MIGRATION_ROOT/evidence/data-plane-$RUN_ID.json}
ROUTE_OUTPUT=${MIGRATION_ROUTE_OUTPUT:-$MIGRATION_ROOT/evidence/data-plane-route-$RUN_ID.json}

case "$RUN_ID" in
  ""|*[!A-Za-z0-9._-]*)
    printf '%s\n' "MIGRATION_RUN_ID contains unsupported characters" >&2
    exit 1
    ;;
esac

test -f "$V1_ROOT/.v2-isolated-copy.json"
test -f "$V2_ROOT/.v2-isolated-copy.json"
test -f "$V1_ROOT/state/control-plane.sqlite3"
test -f "$V2_ROOT/state/control-plane.sqlite3"
test -f "$TEST_KEY_FILE"
test "$(stat -c '%a' "$TEST_KEY_FILE")" = "600"
test -x "$FIXTURE_SCRIPT"
test -f "$DATA_COMPARE_SCRIPT"
test -x "$ROUTE_COMPARE_WRAPPER"

fixture_active=false
cleanup() {
  exit_status=$?
  trap - EXIT HUP INT TERM
  if test "$fixture_active" = "true"; then
    if ! "$FIXTURE_SCRIPT" restore; then
      printf '%s\n' "failed to restore isolated OAuth CPA containers" >&2
      exit_status=1
    fi
  fi
  exit "$exit_status"
}
trap cleanup EXIT HUP INT TERM

"$FIXTURE_SCRIPT" start
fixture_active=true

sed -n '1p' "$TEST_KEY_FILE" | python3 "$DATA_COMPARE_SCRIPT" \
  --target "v1-main,v1,$V1_BASE_URL,$V1_ROOT/state/control-plane.sqlite3" \
  --target "go-v2,v2,$V2_BASE_URL,$V2_ROOT/state/control-plane.sqlite3" \
  --timeout 120 \
  --output "$DATA_OUTPUT" \
  --api-key-stdin \
  --confirm-test-data-request \
  --summary

MIGRATION_ROUTE_OUTPUT="$ROUTE_OUTPUT" \
  MIGRATION_ROOT="$MIGRATION_ROOT" \
  MIGRATION_V1_ROOT="$V1_ROOT" \
  MIGRATION_V2_ROOT="$V2_ROOT" \
  MIGRATION_V1_BASE_URL="$V1_BASE_URL" \
  MIGRATION_V2_BASE_URL="$V2_BASE_URL" \
  MIGRATION_TEST_KEY_FILE="$TEST_KEY_FILE" \
  "$ROUTE_COMPARE_WRAPPER"

"$FIXTURE_SCRIPT" restore
fixture_active=false
trap - EXIT HUP INT TERM

printf 'data-plane comparison: %s\n' "$DATA_OUTPUT"
printf 'route comparison: %s\n' "$ROUTE_OUTPUT"
