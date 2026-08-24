#!/bin/sh
set -eu

REPOSITORY_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEMP_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/codex-cpa-worker-process.XXXXXX")
TARGET_ROOT="$TEMP_ROOT/target"
BIN_DIRECTORY="$TEMP_ROOT/bin"
PID_DIRECTORY="$TEMP_ROOT/pids"
LOG_DIRECTORY="$TEMP_ROOT/logs"
LEASE_TTL=8s

mkdir -p "$BIN_DIRECTORY" "$PID_DIRECTORY" "$LOG_DIRECTORY"

stop_workers() {
  for pid_file in "$PID_DIRECTORY"/*.pid; do
    [ -f "$pid_file" ] || continue
    pid=$(sed -n '1p' "$pid_file")
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      kill -TERM "$pid" 2>/dev/null || true
    fi
  done
  attempts=0
  while [ "$attempts" -lt 40 ]; do
    running=false
    for pid_file in "$PID_DIRECTORY"/*.pid; do
      [ -f "$pid_file" ] || continue
      pid=$(sed -n '1p' "$pid_file")
      if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        running=true
        break
      fi
    done
    [ "$running" = true ] || break
    attempts=$((attempts + 1))
    sleep 0.25
  done
  for pid_file in "$PID_DIRECTORY"/*.pid; do
    [ -f "$pid_file" ] || continue
    pid=$(sed -n '1p' "$pid_file")
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      kill -KILL "$pid" 2>/dev/null || true
    fi
    if [ -n "$pid" ]; then
      wait "$pid" 2>/dev/null || true
    fi
    rm -f "$pid_file"
  done
}

cleanup() {
  stop_workers
  rm -rf "$TEMP_ROOT"
}

trap cleanup EXIT INT TERM

for component in admin collector quota failover notifications log-maintenance ownership migration-compare; do
  go build -o "$BIN_DIRECTORY/cpa-$component" "$REPOSITORY_ROOT/cmd/$component"
done

python3 - "$REPOSITORY_ROOT" "$TARGET_ROOT" <<'PY'
import pathlib
import sys

repository = pathlib.Path(sys.argv[1])
root = pathlib.Path(sys.argv[2])
sys.path.insert(0, str(repository / "scripts"))
sys.path.insert(0, str(repository / "admin"))

from cliproxy import ControlPlane
from usage_store import UsageStore

control = ControlPlane(root)
control.ensure_layout()
control.store.write_secret("cpa_management_key", "rehearsal-management-key")
settings = control.store.read_settings()
settings["notification.enabled"] = False
settings["account_failover.mode"] = "off"
control.store.write_settings(settings)
UsageStore(root / "state" / "usage.sqlite3")
PY

OWNERSHIP="$BIN_DIRECTORY/cpa-ownership"
MIGRATION_COMPARE="$BIN_DIRECTORY/cpa-migration-compare"

"$OWNERSHIP" --root "$TARGET_ROOT" --ttl "$LEASE_TTL" activate \
  --owner go-v2 \
  --confirm-owner go-v2 \
  --allow-empty-bootstrap >"$LOG_DIRECTORY/runtime-activate.json"

start_worker() {
  name=$1
  shift
  "$@" >"$LOG_DIRECTORY/$name.log" 2>&1 &
  pid=$!
  printf '%s\n' "$pid" >"$PID_DIRECTORY/$name.pid"
}

start_go_workers() {
  start_worker admin \
    "$BIN_DIRECTORY/cpa-admin" \
    --root "$TARGET_ROOT" --address 127.0.0.1:0 --shutdown-timeout 2s \
    --gateway-probe-url http://127.0.0.1:9 --lease-ttl "$LEASE_TTL"
  start_worker collector \
    "$BIN_DIRECTORY/cpa-collector" \
    --root "$TARGET_ROOT" --interval 500ms --batch-size 25 --lease-ttl "$LEASE_TTL"
  start_worker quota \
    "$BIN_DIRECTORY/cpa-quota" \
    --root "$TARGET_ROOT" --interval 30s --lease-ttl "$LEASE_TTL"
  start_worker account-failover \
    "$BIN_DIRECTORY/cpa-failover" \
    --root "$TARGET_ROOT" --scheduler-interval 1s --probe-timeout 100ms \
    --account-address-format '%s.invalid:1' --gateway-probe-url http://127.0.0.1:9 \
    --snapshot-timeout 1s --lease-ttl "$LEASE_TTL"
  start_worker notifications \
    "$BIN_DIRECTORY/cpa-notifications" \
    --root "$TARGET_ROOT" --interval 5s --round-timeout 1s --lease-ttl "$LEASE_TTL"
  start_worker log-maintenance \
    "$BIN_DIRECTORY/cpa-log-maintenance" \
    --root "$TARGET_ROOT" --interval 5s --max-file-size-mb 1 --backups 1 \
    --lease-ttl "$LEASE_TTL"
}

lease_matches() {
  scope=$1
  owner_prefix=$2
  "$OWNERSHIP" --root "$TARGET_ROOT" status "$scope" | python3 -c '
import json
import sys
status = json.load(sys.stdin)
prefix = sys.argv[1]
valid = status.get("active") is True and str(status.get("owner", "")).startswith(prefix)
raise SystemExit(0 if valid else 1)
' "$owner_prefix"
}

wait_for_lease() {
  scope=$1
  owner_prefix=$2
  attempts=0
  while [ "$attempts" -lt 80 ]; do
    if lease_matches "$scope" "$owner_prefix"; then
      return 0
    fi
    attempts=$((attempts + 1))
    sleep 0.25
  done
  echo "worker lease did not become active: $scope" >&2
  return 1
}

wait_for_inactive_lease() {
  scope=$1
  attempts=0
  while [ "$attempts" -lt 80 ]; do
    if "$OWNERSHIP" --root "$TARGET_ROOT" status "$scope" | python3 -c '
import json
import sys
status = json.load(sys.stdin)
raise SystemExit(0 if status.get("active") is not True else 1)
'; then
      return 0
    fi
    attempts=$((attempts + 1))
    sleep 0.25
  done
  echo "worker lease did not become inactive: $scope" >&2
  return 1
}

assert_generation() {
  scope=$1
  expected=$2
  "$OWNERSHIP" --root "$TARGET_ROOT" status "$scope" | python3 -c '
import json
import sys
status = json.load(sys.stdin)
expected = int(sys.argv[1])
raise SystemExit(0 if status.get("generation") == expected else 1)
' "$expected"
}

wait_for_collector_checkpoint() {
  attempts=0
  while [ "$attempts" -lt 80 ]; do
    if "$BIN_DIRECTORY/cpa-collector" --root "$TARGET_ROOT" --health >/dev/null 2>&1 && \
       [ -f "$TARGET_ROOT/state/gateway/quota-snapshot.json" ]; then
      return 0
    fi
    attempts=$((attempts + 1))
    sleep 0.25
  done
  echo "collector did not publish its initial checkpoint" >&2
  return 1
}

start_go_workers
wait_for_lease runtime-writer go-v2
wait_for_lease admin go-v2-admin:
wait_for_lease usage-collector go-v2-usage-collector:
wait_for_lease quota go-v2-quota:
wait_for_lease account-failover go-v2-account-failover:
wait_for_lease notifications go-v2-notifications:
wait_for_lease log-maintenance go-v2-log-maintenance:
wait_for_collector_checkpoint

if "$BIN_DIRECTORY/cpa-admin" \
  --root "$TARGET_ROOT" --address 127.0.0.1:0 --lease-ttl "$LEASE_TTL" \
  >"$LOG_DIRECTORY/duplicate-admin.log" 2>&1; then
  echo "duplicate Admin process unexpectedly acquired the worker scope" >&2
  exit 1
fi

"$MIGRATION_COMPARE" state summarize \
  --root "$TARGET_ROOT" \
  --confirm-isolated-state-copy >"$TEMP_ROOT/checkpoint-before.json"

collector_pid=$(sed -n '1p' "$PID_DIRECTORY/collector.pid")
kill -KILL "$collector_pid"
wait "$collector_pid" 2>/dev/null || true
rm -f "$PID_DIRECTORY/collector.pid"

if "$BIN_DIRECTORY/cpa-collector" \
  --root "$TARGET_ROOT" --once --lease-ttl "$LEASE_TTL" \
  >"$LOG_DIRECTORY/premature-collector-restart.log" 2>&1; then
  echo "collector restarted before the stale generation expired" >&2
  exit 1
fi

wait_for_inactive_lease usage-collector
start_worker collector \
  "$BIN_DIRECTORY/cpa-collector" \
  --root "$TARGET_ROOT" --interval 500ms --batch-size 25 --lease-ttl "$LEASE_TTL"
wait_for_lease usage-collector go-v2-usage-collector:
assert_generation usage-collector 2
assert_generation runtime-writer 1
wait_for_collector_checkpoint

"$MIGRATION_COMPARE" state summarize \
  --root "$TARGET_ROOT" \
  --confirm-isolated-state-copy >"$TEMP_ROOT/checkpoint-after.json"

python3 - "$TEMP_ROOT/checkpoint-before.json" "$TEMP_ROOT/checkpoint-after.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as stream:
    before = json.load(stream)
with open(sys.argv[2], encoding="utf-8") as stream:
    after = json.load(stream)
if before["checkpoint_sha256"] != after["checkpoint_sha256"]:
    raise SystemExit("durable checkpoint changed across Collector process restart")
PY

stop_workers
wait_for_inactive_lease runtime-writer

"$OWNERSHIP" --root "$TARGET_ROOT" --ttl "$LEASE_TTL" activate \
  --owner python-v1 \
  --expected-owner go-v2 \
  --expected-generation 1 \
  --confirm-owner python-v1 >"$LOG_DIRECTORY/runtime-rollback.json"

python3 - "$REPOSITORY_ROOT" "$TARGET_ROOT" <<'PY'
import pathlib
import sys

repository = pathlib.Path(sys.argv[1])
root = pathlib.Path(sys.argv[2])
sys.path.insert(0, str(repository / "scripts"))

from ownership_lease import LeaseStore, RUNTIME_SCOPE

store = LeaseStore(root)
runtime = store.join(RUNTIME_SCOPE, "python-v1", 8)
if runtime["generation"] != 2:
    raise SystemExit("Python rollback did not join runtime generation 2")
expected = {
    "admin": 2,
    "usage-collector": 3,
    "quota": 2,
    "account-failover": 2,
    "notifications": 2,
    "log-maintenance": 2,
}
leases = []
for scope, generation in expected.items():
    lease = store.take(scope, "python-v1:" + scope, 8)
    if lease["generation"] != generation:
        raise SystemExit(
            "unexpected rollback generation for {}: {}".format(
                scope, lease["generation"]
            )
        )
    leases.append(lease)
for lease in reversed(leases):
    store.release(lease)
PY

echo "Go v2 worker process rehearsal passed: six real commands, duplicate rejection, Collector restart checkpoint, and Python-v1 rollback generations"
