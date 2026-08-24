#!/usr/bin/env sh
set -eu

MIGRATION_ROOT=${MIGRATION_ROOT:-/home/claude/codex-cpa-go-v2-20260821-54fd5828}
V1_ROOT=${MIGRATION_V1_ROOT:-/home/claude/CLIProxyAPI-v2-baseline}
V2_ROOT=${MIGRATION_V2_ROOT:-/home/claude/CLIProxyAPI-v2-candidate}
: "${MIGRATION_V1_BASE_URL:?MIGRATION_V1_BASE_URL is required}"
: "${MIGRATION_V2_BASE_URL:?MIGRATION_V2_BASE_URL is required}"
V1_BASE_URL=$MIGRATION_V1_BASE_URL
V2_BASE_URL=$MIGRATION_V2_BASE_URL
V1_ADMIN_CONTAINER=${MIGRATION_V1_ADMIN_CONTAINER:-cliproxy-v1-main-compare-admin}
TEST_KEY_FILE=${MIGRATION_TEST_KEY_FILE:-$MIGRATION_ROOT/evidence/test-only.key}
OUTPUT=${MIGRATION_ROUTE_OUTPUT:-$MIGRATION_ROOT/evidence/data-plane-route-deterministic.json}
COMPARE_SCRIPT=${MIGRATION_ROUTE_COMPARE_SCRIPT:-$MIGRATION_ROOT/scripts/migration-data-plane-route-compare.py}

test -f "$V1_ROOT/.v2-isolated-copy.json"
test -f "$V2_ROOT/.v2-isolated-copy.json"
test -f "$V1_ROOT/state/control-plane.sqlite3"
test -f "$V2_ROOT/state/control-plane.sqlite3"
test -f "$TEST_KEY_FILE"
test "$(stat -c '%a' "$TEST_KEY_FILE")" = "600"
test -f "$COMPARE_SCRIPT"

temporary=$(mktemp -d "$MIGRATION_ROOT/.route-compare.XXXXXX")
credentials_fifo=$temporary/credentials
mkfifo -m 0600 "$credentials_fifo"
cleanup() {
  rm -rf -- "$temporary"
}
trap cleanup EXIT HUP INT TERM

produce_credentials() {
  docker exec "$V1_ADMIN_CONTAINER" python3 -c '
import sys
sys.path.insert(0, "/opt/codex-cpa-runtime/scripts")
from cliproxy import ControlPlane
key = ControlPlane(sys.argv[1]).store.read_secret("cpa_management_key", "")
if not key:
    raise SystemExit("isolated management Key is unavailable")
print(key)
' "$V1_ROOT"
  cat "$TEST_KEY_FILE"
}

produce_credentials >"$credentials_fifo" &
producer_pid=$!
set +e
python3 "$COMPARE_SCRIPT" \
  --target "v1-main,v1,$V1_BASE_URL,$V1_ROOT/state/control-plane.sqlite3" \
  --target "go-v2,v2,$V2_BASE_URL,$V2_ROOT/state/control-plane.sqlite3" \
  --timeout 20 \
  --output "$OUTPUT" \
  --credentials-stdin \
  --confirm-isolated-route-data-test \
  --summary <"$credentials_fifo"
compare_status=$?
wait "$producer_pid"
producer_status=$?
set -e
if test "$producer_status" -ne 0; then
  printf '%s\n' "isolated credential producer failed" >&2
  exit "$producer_status"
fi
exit "$compare_status"
