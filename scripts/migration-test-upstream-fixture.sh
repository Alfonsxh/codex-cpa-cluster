#!/usr/bin/env sh
set -eu

ACTION=${1:-}
ROOT_DIR=${MIGRATION_FIXTURE_ROOT:-/home/claude/codex-cpa-go-v2-20260821-54fd5828/fixtures-20260822}
DATA_ROOT=${MIGRATION_DATA_ROOT:-/home/claude/CLIProxyAPI-v2-candidate}
NETWORK=${MIGRATION_FIXTURE_NETWORK:-cliproxy-v2-candidate-data-upstream}
SOURCE_PROJECT=${MIGRATION_SOURCE_PROJECT:-cliproxy-v2-candidate-data}
FIXTURE_PROJECT=${MIGRATION_FIXTURE_PROJECT:-$SOURCE_PROJECT-deterministic-fixtures}
FIXTURE_IMAGE=${MIGRATION_FIXTURE_IMAGE:-eceasy/cli-proxy-api:latest}
BINARY=${MIGRATION_FIXTURE_BINARY:-$ROOT_DIR/incoming/cpa-test-upstream}
INTERNAL_KEY_FILE=${MIGRATION_FIXTURE_INTERNAL_KEY_FILE:-$ROOT_DIR/internal.key}
EXTERNAL_KEY_FILE=${MIGRATION_FIXTURE_EXTERNAL_KEY_FILE:-/home/claude/codex-cpa-go-v2-20260821-54fd5828/evidence/test-only.key}
CONTROL_DB=${MIGRATION_FIXTURE_CONTROL_DB:-$DATA_ROOT/state/control-plane.sqlite3}
MANIFEST=${MIGRATION_FIXTURE_MANIFEST:-$ROOT_DIR/oauth-containers-multi.tsv}
FIXTURE_LIST=${MIGRATION_FIXTURE_LIST:-$ROOT_DIR/deterministic-fixtures.txt}
EVIDENCE=${MIGRATION_FIXTURE_EVIDENCE:-$ROOT_DIR/pre-deterministic-fixtures.json}

fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

archive_file() {
  source_path=$1
  archived_path=$source_path.restored
  archive_index=1
  while test -e "$archived_path"; do
    archived_path=$source_path.restored.$archive_index
    archive_index=$((archive_index + 1))
  done
  mv "$source_path" "$archived_path"
}

validate_common() {
  test -f "$DATA_ROOT/.v2-isolated-copy.json" || fail "isolated data marker is missing"
  test -f "$CONTROL_DB" || fail "isolated control database is missing"
  test -f "$EXTERNAL_KEY_FILE" || fail "dedicated external Test Key is missing"
  test "$(stat -c '%a' "$EXTERNAL_KEY_FILE")" = "600" || fail "external Test Key must have mode 0600"
  test "$(docker network inspect --format '{{index .Labels "io.codex-cpa.scope"}}' "$NETWORK")" = "migration-disposable" || fail "fixture network is not migration-disposable"
}

start_fixture() {
  validate_common
  test -x "$BINARY" || fail "fixture binary is missing or not executable"
  test -f "$INTERNAL_KEY_FILE" || fail "fixture internal Key file is missing"
  test "$(stat -c '%a' "$INTERNAL_KEY_FILE")" = "600" || fail "fixture internal Key must have mode 0600"
  test ! -e "$MANIFEST" || fail "fixture backup manifest already exists"
  test ! -e "$FIXTURE_LIST" || fail "fixture container list already exists"
  test ! -e "$EVIDENCE" || fail "fixture pre-state evidence already exists"

  temporary=$(mktemp -d "$ROOT_DIR/.fixture.XXXXXX")
  aliases_file=$temporary/aliases
  containers_file=$temporary/containers
  rollback_needed=false
  cleanup_start() {
    exit_status=$?
    trap - EXIT HUP INT TERM
    if test "$rollback_needed" = "true"; then
      set +e
      if test -f "$FIXTURE_LIST"; then
        while IFS= read -r rollback_fixture; do
          if test -n "$rollback_fixture" && test -n "$(docker ps -aq --filter name=^/${rollback_fixture}$)"; then
            if test "$(docker inspect --format '{{index .Config.Labels "io.codex-cpa.migration-disposable"}}' "$rollback_fixture")" = "true"; then
              docker rm -f "$rollback_fixture" >/dev/null 2>&1
            fi
          fi
        done <"$FIXTURE_LIST"
      fi
      if test -f "$MANIFEST"; then
        rollback_tab=$(printf '\t')
        while IFS="$rollback_tab" read -r rollback_original rollback_running rollback_connected rollback_account; do
          if test "$rollback_connected" = "true" && test -n "$rollback_original" && test -n "$rollback_account"; then
            rollback_network=$(docker inspect --format "{{with index .NetworkSettings.Networks \"$NETWORK\"}}{{.NetworkID}}{{end}}" "$rollback_original" 2>/dev/null)
            if test -z "$rollback_network"; then
              docker network connect --alias "cliproxy-$rollback_account" "$NETWORK" "$rollback_original" >/dev/null 2>&1
            fi
          fi
        done <"$MANIFEST"
      fi
      rm -f "$MANIFEST" "$FIXTURE_LIST"
      if test -f "$EVIDENCE"; then
        archive_file "$EVIDENCE"
      fi
      set -e
    fi
    rm -rf -- "$temporary"
    exit "$exit_status"
  }
  trap cleanup_start EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM

  python3 - "$CONTROL_DB" "$EXTERNAL_KEY_FILE" >"$aliases_file" <<'PY'
import re
import sqlite3
import sys
from pathlib import Path

database_path = Path(sys.argv[1]).resolve()
external_key = Path(sys.argv[2]).read_text(encoding="utf-8").strip()
database = sqlite3.connect("file:{}?mode=ro".format(database_path), uri=True)
try:
    accounts = sorted({
        str(row[0] or "")
        for row in database.execute(
            "SELECT account_id FROM key_records WHERE status = 'active' AND secret = ?",
            (external_key,),
        ).fetchall()
        if str(row[0] or "")
    })
finally:
    database.close()
if not accounts:
    raise SystemExit("dedicated Test Key has no active isolated accounts")
for account in accounts:
    if re.fullmatch(r"[a-z][a-z0-9-]+", account) is None:
        raise SystemExit("isolated account id is invalid")
    print(account)
PY

  docker ps -a \
    --filter "label=com.docker.compose.project=$SOURCE_PROJECT" \
    --format '{{.Names}}' >"$containers_file"
  test -s "$containers_file" || fail "no disposable OAuth CPA containers were found"

  set --
  while IFS= read -r container; do
    case "$container" in
      "$SOURCE_PROJECT"-*) ;;
      *) fail "unexpected disposable container name" ;;
    esac
    managed_by=$(docker inspect --format '{{index .Config.Labels "io.codex-cpa.managed-by"}}' "$container")
    case "$managed_by" in
      migration-disposable|go-v2) ;;
      *) fail "container is not an allowed disposable CPA" ;;
    esac
    network_id=$(docker inspect --format "{{with index .NetworkSettings.Networks \"$NETWORK\"}}{{.NetworkID}}{{end}}" "$container")
    test -n "$network_id" || fail "disposable CPA is outside the fixture network"
    set -- "$@" "$container"
  done <"$containers_file"

  umask 077
  docker inspect "$@" >"$EVIDENCE"
  : >"$MANIFEST"
  : >"$FIXTURE_LIST"
  rollback_needed=true

  while IFS= read -r account; do
    fixture_name=$FIXTURE_PROJECT-$account
    test -z "$(docker ps -aq --filter name=^/${fixture_name}$)" || fail "fixture account container name is occupied"
    printf '%s\n' "$fixture_name" >>"$FIXTURE_LIST"
    docker run -d \
      --name "$fixture_name" \
      --restart=no \
      --read-only \
      --tmpfs /tmp:rw,noexec,nosuid,size=16m \
      --cap-drop=ALL \
      --security-opt no-new-privileges \
      --network bridge \
      --label "com.docker.compose.project=$FIXTURE_PROJECT" \
      --label "com.docker.compose.service=cpa-test-upstream-$account" \
      --label com.docker.compose.oneoff=False \
      --label "io.codex-cpa.account=$account" \
      --label io.codex-cpa.migration-disposable=true \
      --label io.codex-cpa.managed-by=deterministic-fixture \
      --mount "type=bind,src=$BINARY,dst=/usr/local/bin/cpa-test-upstream,readonly" \
      --mount "type=bind,src=$INTERNAL_KEY_FILE,dst=/run/secrets/internal.key,readonly" \
      --entrypoint /usr/local/bin/cpa-test-upstream \
      "$FIXTURE_IMAGE" \
      --address=:8317 \
      --internal-key-file=/run/secrets/internal.key >/dev/null
  done <"$aliases_file"

  while IFS= read -r container; do
    running=$(docker inspect --format '{{.State.Running}}' "$container")
    connected=false
    if test -n "$(docker inspect --format "{{with index .NetworkSettings.Networks \"$NETWORK\"}}{{.NetworkID}}{{end}}" "$container")"; then
      connected=true
    fi
    account=$(docker inspect --format '{{index .Config.Labels "io.codex-cpa.account"}}' "$container")
    test -n "$account" || fail "disposable CPA account label is missing"
    printf '%s\t%s\t%s\t%s\n' "$container" "$running" "$connected" "$account" >>"$MANIFEST"
    if test "$connected" = "true"; then
      docker network disconnect --force "$NETWORK" "$container"
    fi
  done <"$containers_file"

  while IFS= read -r fixture_name; do
    account=$(docker inspect --format '{{index .Config.Labels "io.codex-cpa.account"}}' "$fixture_name")
    test -n "$account" || fail "fixture account label is missing"
    docker network disconnect bridge "$fixture_name"
    docker network connect --alias "cliproxy-$account" "$NETWORK" "$fixture_name"
  done <"$FIXTURE_LIST"

  while IFS= read -r fixture_name; do
    test "$(docker inspect --format '{{.State.Running}}' "$fixture_name")" = "true" || fail "fixture container did not start"
    test "$(docker inspect --format '{{index .Config.Labels "io.codex-cpa.migration-disposable"}}' "$fixture_name")" = "true" || fail "fixture label is missing"
    test "$(docker inspect --format '{{index .Config.Labels "com.docker.compose.oneoff"}}' "$fixture_name")" = "False" || fail "fixture Compose one-off label is invalid"
    test "$(docker inspect --format '{{.Config.Image}}' "$fixture_name")" = "$FIXTURE_IMAGE" || fail "fixture CPA image does not match"
    test "$(docker inspect --format '{{index .Config.Entrypoint 0}}' "$fixture_name")" = "/usr/local/bin/cpa-test-upstream" || fail "fixture entrypoint does not match"
    test -n "$(docker inspect --format "{{with index .NetworkSettings.Networks \"$NETWORK\"}}{{.NetworkID}}{{end}}" "$fixture_name")" || fail "fixture container is outside the upstream network"
  done <"$FIXTURE_LIST"
  rollback_needed=false
  printf 'deterministic fixtures started: containers=%s oauth_isolated=%s\n' \
    "$(wc -l <"$FIXTURE_LIST" | tr -d ' ')" \
    "$(wc -l <"$MANIFEST" | tr -d ' ')"
}

restore_oauth() {
  validate_common
  test -f "$MANIFEST" || fail "fixture backup manifest is missing"
  test -f "$FIXTURE_LIST" || fail "fixture container list is missing"
  while IFS= read -r fixture_name; do
    case "$fixture_name" in
      "$FIXTURE_PROJECT"-*) ;;
      *) fail "fixture container list contains an unexpected name" ;;
    esac
    test -n "$(docker ps -aq --filter name=^/${fixture_name}$)" || fail "fixture container is missing"
    test "$(docker inspect --format '{{index .Config.Labels "io.codex-cpa.migration-disposable"}}' "$fixture_name")" = "true" || fail "refusing to remove a non-fixture container"
    docker stop --time 10 "$fixture_name" >/dev/null 2>&1 || true
    docker rm "$fixture_name" >/dev/null
  done <"$FIXTURE_LIST"
  tab=$(printf '\t')
  while IFS="$tab" read -r original was_running was_connected account; do
    test -n "$original" && test -n "$account" || fail "fixture backup manifest is invalid"
    test -n "$(docker ps -aq --filter name=^/${original}$)" || fail "original OAuth container is missing"
    managed_by=$(docker inspect --format '{{index .Config.Labels "io.codex-cpa.managed-by"}}' "$original")
    case "$managed_by" in
      migration-disposable|go-v2) ;;
      *) fail "original container ownership changed during fixture test" ;;
    esac
    test "$(docker inspect --format '{{.State.Running}}' "$original")" = "$was_running" || fail "original OAuth container running state changed"
    current_network=$(docker inspect --format "{{with index .NetworkSettings.Networks \"$NETWORK\"}}{{.NetworkID}}{{end}}" "$original")
    if test "$was_connected" = "true"; then
      test -z "$current_network" || fail "original OAuth container unexpectedly remained connected"
      docker network connect --alias "cliproxy-$account" "$NETWORK" "$original"
    else
      test -z "$current_network" || fail "original OAuth container gained an unexpected network"
    fi
  done <"$MANIFEST"
  archive_file "$MANIFEST"
  archive_file "$FIXTURE_LIST"
  if test -f "$EVIDENCE"; then
    archive_file "$EVIDENCE"
  fi
  printf '%s\n' "OAuth CPA containers restored"
}

case "$ACTION" in
  start) start_fixture ;;
  restore) restore_oauth ;;
  *) fail "usage: migration-test-upstream-fixture.sh start|restore" ;;
esac
