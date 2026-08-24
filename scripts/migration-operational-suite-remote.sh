#!/usr/bin/env sh
set -eu

MIGRATION_ROOT=${MIGRATION_ROOT:-/home/claude/codex-cpa-go-v2-20260821-54fd5828}
V1_ROOT=${MIGRATION_V1_ROOT:-/home/claude/CLIProxyAPI-v2-baseline}
V2_ROOT=${MIGRATION_V2_ROOT:-/home/claude/CLIProxyAPI-v2-candidate}
: "${MIGRATION_V1_BASE_URL:?MIGRATION_V1_BASE_URL is required}"
: "${MIGRATION_V2_BASE_URL:?MIGRATION_V2_BASE_URL is required}"
: "${MIGRATION_V2_DATA_CONTAINERS:?MIGRATION_V2_DATA_CONTAINERS is required}"
V1_BASE_URL=$MIGRATION_V1_BASE_URL
V2_BASE_URL=$MIGRATION_V2_BASE_URL
V1_INTERNAL_URL=${MIGRATION_V1_INTERNAL_URL:-http://127.0.0.1:19316}
V2_INTERNAL_URL=${MIGRATION_V2_INTERNAL_URL:-http://127.0.0.1:18316}
TEST_KEY_FILE=${MIGRATION_TEST_KEY_FILE:-$MIGRATION_ROOT/evidence/test-only.key}
FIXTURE_ROOT=${MIGRATION_FIXTURE_ROOT:-$MIGRATION_ROOT/fixtures-20260822}
FIXTURE_SCRIPT=${MIGRATION_FIXTURE_SCRIPT:-$FIXTURE_ROOT/incoming/migration-test-upstream-fixture.sh}
COMPARE_SCRIPT=${MIGRATION_OPERATIONAL_COMPARE_SCRIPT:-$MIGRATION_ROOT/incoming/migration-data-plane-operational-compare.py}
RUN_ID=${MIGRATION_RUN_ID:-$(date +%Y%m%dT%H%M%SCST)}
OUTPUT=${MIGRATION_OPERATIONAL_OUTPUT:-$MIGRATION_ROOT/evidence/data-plane-operational-$RUN_ID.json}
RUNTIME_OUTPUT=${MIGRATION_RUNTIME_OUTPUT:-$MIGRATION_ROOT/evidence/runtime-stability-$RUN_ID.json}
PRIVACY_OUTPUT=${MIGRATION_PRIVACY_OUTPUT:-$MIGRATION_ROOT/evidence/log-privacy-$RUN_ID.json}

case "$RUN_ID" in
  ""|*[!A-Za-z0-9._-]*)
    printf '%s\n' "MIGRATION_RUN_ID contains unsupported characters" >&2
    exit 1
    ;;
esac
case "$V1_ROOT:$V2_ROOT" in
  *:/home/AI/CLIProxyAPI*|*/home/AI/CLIProxyAPI:*|*:/opt/codex-cpa-cluster*|*/opt/codex-cpa-cluster:*)
    printf '%s\n' "production roots are forbidden in the operational suite" >&2
    exit 1
    ;;
esac

test -f "$V1_ROOT/.v2-isolated-copy.json"
test -f "$V2_ROOT/.v2-isolated-copy.json"
test ! "$V1_ROOT" -ef "$V2_ROOT"
test -f "$TEST_KEY_FILE"
test "$(stat -c '%a' "$TEST_KEY_FILE")" = "600"
test -f "$FIXTURE_ROOT/internal.key"
test "$(stat -c '%a' "$FIXTURE_ROOT/internal.key")" = "600"
test -x "$FIXTURE_SCRIPT"
test -f "$COMPARE_SCRIPT"

temporary=$(mktemp -d "$MIGRATION_ROOT/.operational-suite.XXXXXX")
case "$temporary" in
  "$MIGRATION_ROOT"/.operational-suite.*) ;;
  *) printf '%s\n' "unexpected operational-suite temporary directory" >&2; exit 1 ;;
esac
before=$temporary/runtime-before.tsv
after=$temporary/runtime-after.tsv
fixture_active=false

runtime_state() {
  for container in \
    cliproxy-edge \
    cliproxy-v1-main-compare-edge \
    cliproxy-v1-main-compare-web \
    cliproxy-v1-main-compare-admin \
    cliproxy-v1-main-compare-gateway-blue \
    cliproxy-v1-main-compare-gateway-green \
    cliproxy-v1-main-compare-docker-read-proxy \
    cliproxy-v2-candidate-edge \
    cliproxy-v2-candidate-web \
    cliproxy-v2-candidate-admin \
    cliproxy-v2-candidate-gateway-blue \
    cliproxy-v2-candidate-gateway-green \
    cliproxy-v2-candidate-docker-read-proxy \
    $MIGRATION_V2_DATA_CONTAINERS
  do
    docker inspect --format '{{.Name}}	{{.RestartCount}}	{{.State.Status}}	{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container"
  done
}

cleanup() {
  exit_status=$?
  trap - EXIT HUP INT TERM
  set +e
  if test "$fixture_active" = "true"; then
    "$FIXTURE_SCRIPT" restore || exit_status=1
  fi
  case "$temporary" in
    "$MIGRATION_ROOT"/.operational-suite.*) rm -rf -- "$temporary" ;;
  esac
  set -e
  exit "$exit_status"
}
trap cleanup EXIT HUP INT TERM

runtime_state >"$before"
"$FIXTURE_SCRIPT" start
fixture_active=true

sed -n '1p' "$TEST_KEY_FILE" | python3 "$COMPARE_SCRIPT" \
  --target "v1-main,v1,$V1_BASE_URL,$V1_INTERNAL_URL" \
  --target "go-v2,v2,$V2_BASE_URL,$V2_INTERNAL_URL" \
  --timeout 30 \
  --requests 32 \
  --workers 16 \
  --delay-ms 200 \
  --max-body-bytes 104857600 \
  --output "$OUTPUT" \
  --api-key-stdin

runtime_state >"$after"
python3 - "$before" "$after" "$RUNTIME_OUTPUT" <<'PY'
import json
import sys
from pathlib import Path


def load(path):
    records = {}
    for line in Path(path).read_text(encoding="utf-8").splitlines():
        name, restarts, status, health = line.split("\t", 3)
        records[name] = {
            "restarts": int(restarts),
            "status": status,
            "health": health,
        }
    return records


before = load(sys.argv[1])
after = load(sys.argv[2])
failures = []
if before.keys() != after.keys():
    failures.append("container_set_changed")
for name in sorted(before.keys() & after.keys()):
    if before[name]["restarts"] != after[name]["restarts"]:
        failures.append("restart_count_changed")
    if after[name]["status"] != "running":
        failures.append("container_not_running")
    if after[name]["health"] not in {"healthy", "none"}:
        failures.append("container_not_healthy")
report = {
    "version": 1,
    "passed": not failures,
    "container_count": len(after),
    "restart_counts_unchanged": all(
        before.get(name, {}).get("restarts") == after[name]["restarts"]
        for name in after
    ),
    "all_running": all(record["status"] == "running" for record in after.values()),
    "all_health_valid": all(record["health"] in {"healthy", "none"} for record in after.values()),
    "failures": sorted(set(failures)),
}
Path(sys.argv[3]).write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
print(json.dumps(report, indent=2, sort_keys=True))
if failures:
    raise SystemExit(1)
PY

python3 - "$TEST_KEY_FILE" "$FIXTURE_ROOT/internal.key" "$V1_ROOT/logs" "$V2_ROOT/logs" "$PRIVACY_OUTPUT" <<'PY'
import json
import sys
from pathlib import Path


external = Path(sys.argv[1]).read_bytes().strip()
internal = Path(sys.argv[2]).read_bytes().strip()
if len(external) < 16 or len(internal) < 16:
    raise SystemExit("privacy check credentials are invalid")
files = []
occurrences = 0
for root_value in sys.argv[3:5]:
    root = Path(root_value).resolve()
    for path in sorted(root.rglob("*")):
        if not path.is_file() or path.is_symlink():
            continue
        payload = path.read_bytes()
        files.append(path)
        occurrences += payload.count(external) + payload.count(internal)
report = {
    "version": 1,
    "passed": occurrences == 0,
    "files_scanned": len(files),
    "credential_occurrences": occurrences,
}
Path(sys.argv[5]).write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
print(json.dumps(report, indent=2, sort_keys=True))
if occurrences:
    raise SystemExit(1)
PY

"$FIXTURE_SCRIPT" restore
fixture_active=false
trap - EXIT HUP INT TERM
case "$temporary" in
  "$MIGRATION_ROOT"/.operational-suite.*) rm -rf -- "$temporary" ;;
esac

test ! -e "$FIXTURE_ROOT/deterministic-fixtures.txt"
test ! -e "$FIXTURE_ROOT/oauth-containers-multi.tsv"
printf 'operational comparison: %s\n' "$OUTPUT"
printf 'runtime stability: %s\n' "$RUNTIME_OUTPUT"
printf 'log privacy: %s\n' "$PRIVACY_OUTPUT"
