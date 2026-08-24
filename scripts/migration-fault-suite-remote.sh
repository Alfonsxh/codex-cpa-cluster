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
FAULT_COMPARE_SCRIPT=${MIGRATION_FAULT_COMPARE_SCRIPT:-$MIGRATION_ROOT/incoming/migration-data-plane-fault-compare.py}
FIXTURE_LIST=${MIGRATION_FIXTURE_LIST:-$MIGRATION_ROOT/fixtures-20260822/deterministic-fixtures.txt}
RUN_ID=${MIGRATION_RUN_ID:-$(date +%Y%m%dT%H%M%SCST)}

case "$RUN_ID" in
  ""|*[!A-Za-z0-9._-]*)
    printf '%s\n' "MIGRATION_RUN_ID contains unsupported characters" >&2
    exit 1
    ;;
esac
case "$V1_ROOT:$V2_ROOT" in
  *:/home/AI/CLIProxyAPI*|*/home/AI/CLIProxyAPI:*|*:/opt/codex-cpa-cluster*|*/opt/codex-cpa-cluster:*)
    printf '%s\n' "production roots are forbidden in the migration fault suite" >&2
    exit 1
    ;;
esac

test -f "$V1_ROOT/.v2-isolated-copy.json"
test -f "$V2_ROOT/.v2-isolated-copy.json"
test ! "$V1_ROOT" -ef "$V2_ROOT"
test -f "$TEST_KEY_FILE"
test "$(stat -c '%a' "$TEST_KEY_FILE")" = "600"
test -x "$FIXTURE_SCRIPT"
test -f "$FAULT_COMPARE_SCRIPT"

V1_AUTH_SNAPSHOT=$V1_ROOT/state/gateway/auth-snapshot.json
V2_AUTH_SNAPSHOT=$V2_ROOT/state/gateway/auth-snapshot.json
test -f "$V1_AUTH_SNAPSHOT"
test -f "$V2_AUTH_SNAPSHOT"
test ! -L "$V1_AUTH_SNAPSHOT"
test ! -L "$V2_AUTH_SNAPSHOT"
test ! "$V1_AUTH_SNAPSHOT" -ef "$V2_AUTH_SNAPSHOT"

temporary=$(mktemp -d "$MIGRATION_ROOT/.fault-suite.XXXXXX")
case "$temporary" in
  "$MIGRATION_ROOT"/.fault-suite.*) ;;
  *) printf '%s\n' "unexpected fault-suite temporary directory" >&2; exit 1 ;;
esac
V1_AUTH_BACKUP=$temporary/v1-auth-snapshot.json
V2_AUTH_BACKUP=$temporary/v2-auth-snapshot.json

fixture_active=false
snapshots_modified=false

restore_snapshots() {
  if test "$snapshots_modified" = "true"; then
    cp -p "$V1_AUTH_BACKUP" "$V1_AUTH_SNAPSHOT"
    cp -p "$V2_AUTH_BACKUP" "$V2_AUTH_SNAPSHOT"
    snapshots_modified=false
  fi
}

cleanup() {
  exit_status=$?
  trap - EXIT HUP INT TERM
  set +e
  restore_snapshots || exit_status=1
  if test "$fixture_active" = "true"; then
    "$FIXTURE_SCRIPT" restore || exit_status=1
  fi
  case "$temporary" in
    "$MIGRATION_ROOT"/.fault-suite.*) rm -rf -- "$temporary" ;;
  esac
  set -e
  exit "$exit_status"
}
trap cleanup EXIT HUP INT TERM

run_compare() {
  mode=$1
  output=$MIGRATION_ROOT/evidence/fault-$mode-$RUN_ID.json
  sed -n '1p' "$TEST_KEY_FILE" | python3 "$FAULT_COMPARE_SCRIPT" \
    --target "v1-main,v1,$V1_BASE_URL" \
    --target "go-v2,v2,$V2_BASE_URL" \
    --mode "$mode" \
    --timeout 15 \
    --output "$output" \
    --api-key-stdin
}

"$FIXTURE_SCRIPT" start
fixture_active=true
test -s "$FIXTURE_LIST"

run_compare baseline

while IFS= read -r fixture_name; do
  test -n "$fixture_name"
  test "$(docker inspect --format '{{index .Config.Labels "io.codex-cpa.migration-disposable"}}' "$fixture_name")" = "true"
  docker stop --time 10 "$fixture_name" >/dev/null
done <"$FIXTURE_LIST"
run_compare upstream-unavailable
while IFS= read -r fixture_name; do
  docker start "$fixture_name" >/dev/null
done <"$FIXTURE_LIST"
sleep 1
run_compare baseline

cp -p "$V1_AUTH_SNAPSHOT" "$V1_AUTH_BACKUP"
cp -p "$V2_AUTH_SNAPSHOT" "$V2_AUTH_BACKUP"
snapshots_modified=true
printf '%s\n' '{"version":1,"broken":true}' >"$V1_AUTH_SNAPSHOT"
printf '%s\n' '{"version":1,"broken":true}' >"$V2_AUTH_SNAPSHOT"
sleep 6
run_compare auth-unavailable
restore_snapshots
sleep 2
run_compare baseline

"$FIXTURE_SCRIPT" restore
fixture_active=false
trap - EXIT HUP INT TERM
case "$temporary" in
  "$MIGRATION_ROOT"/.fault-suite.*) rm -rf -- "$temporary" ;;
esac

printf '%s\n' "v1/Go-v2 isolated data-plane fault comparison passed"
