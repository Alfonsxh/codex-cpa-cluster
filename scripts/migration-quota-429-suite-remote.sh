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
FIXTURE_ROOT=${MIGRATION_FIXTURE_ROOT:-$MIGRATION_ROOT/fixtures-20260822}
FIXTURE_SCRIPT=${MIGRATION_FIXTURE_SCRIPT:-$FIXTURE_ROOT/incoming/migration-test-upstream-fixture.sh}
COMPARE_SCRIPT=${MIGRATION_FAULT_COMPARE_SCRIPT:-$MIGRATION_ROOT/incoming/migration-data-plane-fault-compare.py}
RUN_ID=${MIGRATION_RUN_ID:-$(date +%Y%m%dT%H%M%SCST)}

case "$RUN_ID" in
  ""|*[!A-Za-z0-9._-]*)
    printf '%s\n' "MIGRATION_RUN_ID contains unsupported characters" >&2
    exit 1
    ;;
esac
case "$V1_ROOT:$V2_ROOT" in
  *:/home/AI/CLIProxyAPI*|*/home/AI/CLIProxyAPI:*|*:/opt/codex-cpa-cluster*|*/opt/codex-cpa-cluster:*)
    printf '%s\n' "production roots are forbidden in the quota comparison suite" >&2
    exit 1
    ;;
esac

test -f "$V1_ROOT/.v2-isolated-copy.json"
test -f "$V2_ROOT/.v2-isolated-copy.json"
test ! "$V1_ROOT" -ef "$V2_ROOT"
test -f "$TEST_KEY_FILE"
test "$(stat -c '%a' "$TEST_KEY_FILE")" = "600"
test -x "$FIXTURE_SCRIPT"
test -f "$COMPARE_SCRIPT"

temporary=$(mktemp -d "$MIGRATION_ROOT/.quota-429-suite.XXXXXX")
case "$temporary" in
  "$MIGRATION_ROOT"/.quota-429-suite.*) ;;
  *) printf '%s\n' "unexpected quota-suite temporary directory" >&2; exit 1 ;;
esac

v1_quota=$V1_ROOT/state/gateway/quota-snapshot.json
v1_heartbeat=$V1_ROOT/state/gateway/quota-heartbeat.json
v2_quota=$V2_ROOT/state/gateway/quota-snapshot.json
v2_heartbeat=$V2_ROOT/state/gateway/quota-heartbeat.json
for path in "$v1_quota" "$v1_heartbeat" "$v2_quota" "$v2_heartbeat"; do
  test -f "$path"
  test ! -L "$path"
done
test ! "$v1_quota" -ef "$v2_quota"
test ! "$v1_heartbeat" -ef "$v2_heartbeat"

cp -p "$v1_quota" "$temporary/v1-quota.json"
cp -p "$v1_heartbeat" "$temporary/v1-heartbeat.json"
cp -p "$v2_quota" "$temporary/v2-quota.json"
cp -p "$v2_heartbeat" "$temporary/v2-heartbeat.json"
snapshots_modified=false
fixture_active=false

restore_one() {
  source_path=$1
  target_path=$2
  staged=$target_path.migration-429-$RUN_ID-restore
  test ! -e "$staged"
  cp -p "$source_path" "$staged"
  mv -f "$staged" "$target_path"
}

restore_snapshots() {
  if test "$snapshots_modified" = "true"; then
    restore_one "$temporary/v1-quota.json" "$v1_quota"
    restore_one "$temporary/v1-heartbeat.json" "$v1_heartbeat"
    restore_one "$temporary/v2-quota.json" "$v2_quota"
    restore_one "$temporary/v2-heartbeat.json" "$v2_heartbeat"
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
    "$MIGRATION_ROOT"/.quota-429-suite.*) rm -rf -- "$temporary" ;;
  esac
  set -e
  exit "$exit_status"
}
trap cleanup EXIT HUP INT TERM

run_compare() {
  mode=$1
  output=$MIGRATION_ROOT/evidence/quota-$mode-$RUN_ID.json
  sed -n '1p' "$TEST_KEY_FILE" | python3 "$COMPARE_SCRIPT" \
    --target "v1-main,v1,$V1_BASE_URL" \
    --target "go-v2,v2,$V2_BASE_URL" \
    --mode "$mode" \
    --timeout 20 \
    --output "$output" \
    --api-key-stdin
}

"$FIXTURE_SCRIPT" start
fixture_active=true
run_compare baseline

snapshots_modified=true
python3 - "$TEST_KEY_FILE" "$RUN_ID" \
  "$V1_ROOT/state/control-plane.sqlite3" "$v1_quota" "$v1_heartbeat" \
  "$V2_ROOT/state/control-plane.sqlite3" "$v2_quota" "$v2_heartbeat" <<'PY'
import hashlib
import json
import os
import sqlite3
import sys
import tempfile
import time
from pathlib import Path


key = Path(sys.argv[1]).read_text(encoding="utf-8").strip()
run_id = sys.argv[2]
targets = [sys.argv[3:6], sys.argv[6:9]]
users = []
for database_value, _, _ in targets:
    database_path = Path(database_value).resolve()
    database = sqlite3.connect("file:{}?mode=ro".format(database_path), uri=True)
    try:
        rows = database.execute(
            "SELECT DISTINCT lower(trim(user_email)) FROM key_records "
            "WHERE status = 'active' AND secret = ?",
            (key,),
        ).fetchall()
    finally:
        database.close()
    values = {str(row[0] or "") for row in rows if str(row[0] or "")}
    if len(values) != 1:
        raise SystemExit("dedicated Test Key does not resolve to one isolated user")
    users.append(next(iter(values)))
if users[0] != users[1]:
    raise SystemExit("dedicated Test Key identity differs across isolated copies")


def atomic_json(path, payload):
    path = Path(path)
    metadata = path.stat()
    mode = metadata.st_mode & 0o777
    descriptor, temporary = tempfile.mkstemp(prefix=path.name + ".migration-429.", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            json.dump(payload, stream, ensure_ascii=False, indent=2, sort_keys=True)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temporary, mode)
        os.chown(temporary, metadata.st_uid, metadata.st_gid)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


now = int(time.time())
for index, (_, quota_value, heartbeat_value) in enumerate(targets):
    quota_path = Path(quota_value)
    quota = json.loads(quota_path.read_text(encoding="utf-8"))
    records = quota.get("records")
    if not isinstance(records, list):
        raise SystemExit("isolated quota snapshot records are invalid")
    user = users[index]
    updated = False
    for record in records:
        if isinstance(record, dict) and str(record.get("user_email") or "").lower() == user:
            target = record
            updated = True
            break
    if not updated:
        target = {"user_email": user}
        records.append(target)
    target.update({
        "week_start_at": now - 60,
        "week_end_at": now + 3600,
        "limit_tokens": 0,
        "used_tokens": 0,
        "raw_used_tokens": 0,
        "weighted_raw_used_tokens": 0,
        "quota_unit": "weighted_tokens",
    })
    quota["version"] = 1
    quota["generation"] = hashlib.sha256(
        (run_id + ":quota:" + str(index)).encode("utf-8")
    ).hexdigest()[:32]
    quota["generated_at"] = now
    quota["content_sha256"] = hashlib.sha256(
        json.dumps(records, separators=(",", ":"), sort_keys=True).encode("utf-8")
    ).hexdigest()
    heartbeat = {
        "version": 1,
        "updated_at": now,
        "ok": True,
        "error": "",
        "stale_after_seconds": 15,
        "last_success_at": now,
        "fail_open_after_seconds": 300,
    }
    atomic_json(quota_path, quota)
    atomic_json(heartbeat_value, heartbeat)
print(json.dumps({"staged": True, "targets": len(targets), "records_updated": 2}))
PY
test "$(stat -c '%u:%g:%a' "$v1_quota")" = "$(stat -c '%u:%g:%a' "$temporary/v1-quota.json")"
test "$(stat -c '%u:%g:%a' "$v1_heartbeat")" = "$(stat -c '%u:%g:%a' "$temporary/v1-heartbeat.json")"
test "$(stat -c '%u:%g:%a' "$v2_quota")" = "$(stat -c '%u:%g:%a' "$temporary/v2-quota.json")"
test "$(stat -c '%u:%g:%a' "$v2_heartbeat")" = "$(stat -c '%u:%g:%a' "$temporary/v2-heartbeat.json")"
sleep 6
run_compare quota-exceeded

restore_snapshots
sleep 3
run_compare baseline

"$FIXTURE_SCRIPT" restore
fixture_active=false
trap - EXIT HUP INT TERM
case "$temporary" in
  "$MIGRATION_ROOT"/.quota-429-suite.*) rm -rf -- "$temporary" ;;
esac

test ! -e "$FIXTURE_ROOT/deterministic-fixtures.txt"
test ! -e "$FIXTURE_ROOT/oauth-containers-multi.tsv"
printf '%s\n' "v1/Go-v2 isolated quota 429 comparison passed"
