#!/usr/bin/env sh
set -eu

ARCHIVE=${1:?请提供发布包路径}
TARGET=${2:-/opt/codex-cpa-cluster}
REQUESTED_HEALTH_PORT=${3:-}
HEALTH_PORT=$REQUESTED_HEALTH_PORT
PROFILE_SOURCE=${4:-}
RELEASE_VERSION=${RELEASE_VERSION:?请设置要部署的 RELEASE_VERSION，例如 v1.0.0}
RELEASE_IMAGE_PREFIX=${RELEASE_IMAGE_PREFIX:?请设置 RELEASE_IMAGE_PREFIX，例如 registry.example.com/team}
RELEASE_METADATA_IMAGE=${RELEASE_METADATA_IMAGE:-$RELEASE_IMAGE_PREFIX/codex-cpa-release:latest}
COMMIT_SHA=${RELEASE_COMMIT_SHA:-manual}
PIPELINE_ID=${RELEASE_OPERATION_ID:-manual}

if ! printf '%s' "$RELEASE_VERSION" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$'; then
  echo "RELEASE_VERSION 必须是语义化版本：$RELEASE_VERSION" >&2
  exit 1
fi
if ! printf '%s' "$RELEASE_IMAGE_PREFIX" | grep -Eq '^[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9._/-]+$'; then
  echo "RELEASE_IMAGE_PREFIX 无效：$RELEASE_IMAGE_PREFIX" >&2
  exit 1
fi
if ! printf '%s' "$RELEASE_METADATA_IMAGE" | grep -Eq '^[A-Za-z0-9._:/-]+:[A-Za-z0-9._-]+$'; then
  echo "RELEASE_METADATA_IMAGE 无效" >&2
  exit 1
fi

if [ ! -f "$ARCHIVE" ]; then
  echo "发布包不存在：$ARCHIVE" >&2
  exit 1
fi
if [ -n "$PROFILE_SOURCE" ] && [ ! -f "$PROFILE_SOURCE" ]; then
  echo "部署配置档案不存在：$PROFILE_SOURCE" >&2
  exit 1
fi
if ! python3 -c 'import sqlite3; import xml.etree.ElementTree as ET; ET.fromstring("<svg/>")'; then
  echo "生产主机 Python 缺少可用的 SQLite 或 XML 标准库，拒绝部署" >&2
  exit 1
fi
if tar -tzf "$ARCHIVE" | grep -Eq '(^/|(^|/)\.\.(/|$))'; then
  echo "发布包包含不安全路径" >&2
  exit 1
fi
if [ ! -d "$TARGET" ] || [ ! -f "$TARGET/docker-compose.yml" ] \
  || [ ! -f "$TARGET/state/control-plane.sqlite3" ] \
  || [ ! -f "$TARGET/secrets/control-plane.key" ]; then
  echo "生产部署目录未初始化或缺少控制面数据库/主密钥，拒绝覆盖：$TARGET" >&2
  exit 1
fi

if ! command -v flock >/dev/null 2>&1; then
  echo "生产主机缺少 flock，无法安全串行化发布与运行时操作" >&2
  exit 1
fi
RUNTIME_OPERATION_LOCK="$TARGET/state/runtime-operation.lock"
exec 9>>"$RUNTIME_OPERATION_LOCK"
chmod 600 "$RUNTIME_OPERATION_LOCK"
if ! flock -n 9; then
  echo "另一个发布或运行时操作正在进行，拒绝并发部署" >&2
  exit 1
fi

target_env_value() {
  NAME=$1
  FALLBACK=$2
  VALUE=
  for ENV_PATH in "$TARGET/state/compose.env" "$TARGET/.env"; do
    if [ -f "$ENV_PATH" ]; then
      VALUE=$(awk -F= -v name="$NAME" '$1 == name {sub(/^[^=]*=/, ""); print}' "$ENV_PATH" | tail -n 1)
      [ -z "$VALUE" ] || break
    fi
  done
  printf '%s' "${VALUE:-$FALLBACK}"
}

target_setting_value() {
  KEY=$1
  ENV_NAME=$2
  FALLBACK=$3
  VALUE=$(python3 - "$TARGET/state/control-plane.sqlite3" "$KEY" <<'PY'
import json
import sqlite3
import sys

path, key = sys.argv[1:]
try:
    with sqlite3.connect(path) as connection:
        row = connection.execute(
            "SELECT value_json FROM settings WHERE key = ?", (key,)
        ).fetchone()
    value = json.loads(row[0]) if row else ""
except (OSError, sqlite3.Error, TypeError, ValueError):
    value = ""
if isinstance(value, bool):
    print("true" if value else "false")
elif value is not None:
    print(value)
PY
  )
  if [ -z "$VALUE" ]; then
    VALUE=$(target_env_value "$ENV_NAME" "$FALLBACK")
  fi
  printf '%s' "${VALUE:-$FALLBACK}"
}

if [ -z "$HEALTH_PORT" ]; then
  HEALTH_PORT=$(target_setting_value gateway.port GATEWAY_PORT 18317)
fi
HEALTH_PORT=${HEALTH_PORT:-18317}
case "$HEALTH_PORT" in
  *[!0-9]*|'') echo "健康检查端口无效：$HEALTH_PORT" >&2; exit 1 ;;
esac
if [ "$HEALTH_PORT" -lt 1024 ] || [ "$HEALTH_PORT" -gt 65535 ]; then
  echo "健康检查端口必须位于 1024-65535：$HEALTH_PORT" >&2
  exit 1
fi
INTERNAL_HEALTH_PORT=$(target_setting_value gateway.internal_port GATEWAY_INTERNAL_PORT 18316)
INTERNAL_HEALTH_PORT=${INTERNAL_HEALTH_PORT:-18316}
case "$INTERNAL_HEALTH_PORT" in
  *[!0-9]*|'') echo "内部探针端口无效：$INTERNAL_HEALTH_PORT" >&2; exit 1 ;;
esac
if [ "$INTERNAL_HEALTH_PORT" -lt 1024 ] || [ "$INTERNAL_HEALTH_PORT" -gt 65535 ]; then
  echo "内部探针端口必须位于 1024-65535：$INTERNAL_HEALTH_PORT" >&2
  exit 1
fi
GATEWAY_DRAIN_TIMEOUT_SECONDS=$(target_setting_value delivery.gateway_drain_timeout_seconds GATEWAY_DRAIN_TIMEOUT_SECONDS 3600)
GATEWAY_DRAIN_TIMEOUT_SECONDS=${GATEWAY_DRAIN_TIMEOUT_SECONDS:-3600}
case "$GATEWAY_DRAIN_TIMEOUT_SECONDS" in
  *[!0-9]*|'') echo "Gateway 排空超时无效：$GATEWAY_DRAIN_TIMEOUT_SECONDS" >&2; exit 1 ;;
esac
if [ "$GATEWAY_DRAIN_TIMEOUT_SECONDS" -lt 30 ] \
  || [ "$GATEWAY_DRAIN_TIMEOUT_SECONDS" -gt 7200 ]; then
  echo "Gateway 排空超时必须位于 30-7200 秒：$GATEWAY_DRAIN_TIMEOUT_SECONDS" >&2
  exit 1
fi
ALLOW_EDGE_RECREATE=${ALLOW_EDGE_RECREATE:-false}
case "$ALLOW_EDGE_RECREATE" in
  true|false) ;;
  *)
    echo "ALLOW_EDGE_RECREATE 只能为 true 或 false" >&2
    exit 1
    ;;
esac

BACKUP_DIR="$TARGET/backups/deployments"
BACKUP_FILE="$BACKUP_DIR/ci-${PIPELINE_ID}-${COMMIT_SHA}.tar.gz"
STATE_BACKUP_FILE="$BACKUP_DIR/ci-${PIPELINE_ID}-${COMMIT_SHA}-state.tar.gz"
RUNTIME_BACKUP_FILE="$BACKUP_DIR/ci-${PIPELINE_ID}-${COMMIT_SHA}-runtime.tar.gz"
CONTROL_DB_BACKUP_FILE="$BACKUP_DIR/ci-${PIPELINE_ID}-${COMMIT_SHA}-control-plane.sqlite3"
USAGE_DB_BACKUP_FILE="$BACKUP_DIR/ci-${PIPELINE_ID}-${COMMIT_SHA}-usage.sqlite3"
ENV_BACKUP_FILE="$BACKUP_DIR/ci-${PIPELINE_ID}-${COMMIT_SHA}.env"
COMPOSE_ENV_BACKUP_FILE="$BACKUP_DIR/ci-${PIPELINE_ID}-${COMMIT_SHA}.compose.env"
DATA_MANIFEST_BEFORE="$BACKUP_DIR/ci-${PIPELINE_ID}-${COMMIT_SHA}-data-before.json"
DATA_MANIFEST_AFTER="$BACKUP_DIR/ci-${PIPELINE_ID}-${COMMIT_SHA}-data-after.json"
DATA_MANIFEST_CLEAN="$BACKUP_DIR/ci-${PIPELINE_ID}-${COMMIT_SHA}-data-after-clean.json"
CONTROL_DB_EXISTED=false
USAGE_DB_EXISTED=false
MASTER_KEY_EXISTED=false
TARGET_ENV_EXISTED=false
TARGET_COMPOSE_ENV_EXISTED=false
PRE_APPLY_ENV_MUTATED=false
APPLY_RELEASE_STARTED=false
[ -f "$TARGET/state/control-plane.sqlite3" ] && CONTROL_DB_EXISTED=true
[ -f "$TARGET/state/usage.sqlite3" ] && USAGE_DB_EXISTED=true
[ -f "$TARGET/secrets/control-plane.key" ] && MASTER_KEY_EXISTED=true
[ -f "$TARGET/.env" ] && TARGET_ENV_EXISTED=true
[ -f "$TARGET/state/compose.env" ] && TARGET_COMPOSE_ENV_EXISTED=true
mkdir -p "$BACKUP_DIR"
chmod 700 "$BACKUP_DIR"

RELEASE_ROOT=$(mktemp -d "$BACKUP_DIR/.release-${PIPELINE_ID}-XXXXXX")
cleanup_release_root() {
  python3 - \
    "$RELEASE_ROOT" "$BACKUP_DIR" \
    "$TARGET/.env" "$ENV_BACKUP_FILE" \
    "$TARGET_ENV_EXISTED" "$PRE_APPLY_ENV_MUTATED" "$APPLY_RELEASE_STARTED" <<'PY'
import os
import shutil
import sys
from pathlib import Path

candidate = Path(sys.argv[1]).resolve()
parent = Path(sys.argv[2]).resolve()
environment = Path(sys.argv[3]).resolve()
environment_backup = Path(sys.argv[4]).resolve()
environment_existed = sys.argv[5] == "true"
environment_mutated = sys.argv[6] == "true"
apply_started = sys.argv[7] == "true"
if environment_mutated and not apply_started:
    if environment_existed:
        if environment_backup.is_file():
            shutil.copy2(environment_backup, environment)
            os.chmod(environment, 0o600)
    else:
        try:
            environment.unlink()
        except FileNotFoundError:
            pass
if candidate.parent != parent or not candidate.name.startswith(".release-"):
    raise RuntimeError("refusing to remove unexpected release directory")
if candidate.exists():
    shutil.rmtree(candidate)
PY
}
trap cleanup_release_root EXIT HUP INT TERM
tar -xzf "$ARCHIVE" -C "$RELEASE_ROOT"

APP_CLI="$RELEASE_ROOT/scripts/cliproxy.py"
DATA_GUARD="$RELEASE_ROOT/scripts/runtime_data_guard.py"
RELEASE_MANIFEST="$RELEASE_ROOT/release-manifest.json"
RELEASE_MANIFEST_HELPER="$RELEASE_ROOT/scripts/release_manifest.py"
EDGE_SLOT_HELPER="$RELEASE_ROOT/scripts/edge_slot.py"
GATEWAY_RELEASE_PROBE="$RELEASE_ROOT/scripts/gateway_release_probe.py"
RELEASE_IMAGE_COMPARE="$RELEASE_ROOT/scripts/release_image_compare.py"
if [ ! -f "$RELEASE_ROOT/admin/Dockerfile" ] \
  || [ ! -f "$RELEASE_ROOT/gateway/Dockerfile" ] \
  || [ ! -f "$RELEASE_ROOT/web/Dockerfile" ] \
  || [ ! -f "$RELEASE_ROOT/edge/Dockerfile" ] \
  || [ ! -f "$RELEASE_ROOT/docker-compose.yml" ] \
  || [ ! -f "$APP_CLI" ] \
  || [ ! -f "$DATA_GUARD" ] \
  || [ ! -f "$RELEASE_MANIFEST" ] \
  || [ ! -f "$RELEASE_MANIFEST_HELPER" ] \
  || [ ! -f "$EDGE_SLOT_HELPER" ] \
  || [ ! -f "$GATEWAY_RELEASE_PROBE" ] \
  || [ ! -f "$RELEASE_IMAGE_COMPARE" ]; then
  echo "发布包缺少不可变镜像部署文件" >&2
  exit 1
fi
python3 "$RELEASE_MANIFEST_HELPER" verify \
  --root "$RELEASE_ROOT" \
  --manifest "$RELEASE_MANIFEST"

create_empty_archive() {
  python3 - "$1" <<'PY'
import sys
import tarfile

with tarfile.open(sys.argv[1], "w:gz"):
    pass
PY
}

set --
for SOURCE_PATH in \
  .dockerignore .env.example compose.env.example .gitignore AGENTS.md LICENSE Makefile README.md \
  .git .harness .claude config docs docker-compose.yml admin dashboard edge gateway portal release scripts tests web; do
  if [ -e "$TARGET/$SOURCE_PATH" ]; then
    set -- "$@" "$SOURCE_PATH"
  fi
done
if [ "$#" -gt 0 ]; then
  tar -czf "$BACKUP_FILE" -C "$TARGET" "$@"
else
  create_empty_archive "$BACKUP_FILE"
fi
chmod 600 "$BACKUP_FILE"

set --
for RUNTIME_PATH in \
  .env compose.accounts.yml auth configs secrets state/edge \
  management/config management/auth management/plugins; do
  if [ -e "$TARGET/$RUNTIME_PATH" ]; then
    set -- "$@" "$RUNTIME_PATH"
  fi
done
if [ "$#" -gt 0 ]; then
  tar -czf "$RUNTIME_BACKUP_FILE" -C "$TARGET" "$@"
else
  create_empty_archive "$RUNTIME_BACKUP_FILE"
fi
chmod 600 "$RUNTIME_BACKUP_FILE"

if [ -f "$TARGET/.env" ]; then
  cp "$TARGET/.env" "$ENV_BACKUP_FILE"
  chmod 600 "$ENV_BACKUP_FILE"
fi
if [ -f "$TARGET/state/compose.env" ]; then
  cp "$TARGET/state/compose.env" "$COMPOSE_ENV_BACKUP_FILE"
  chmod 600 "$COMPOSE_ENV_BACKUP_FILE"
fi

set --
for STATE_FILE in \
  state/accounts.json state/keys.json state/user-routes.json state/configuration.json \
  state/account-failover.json state/notification-state.json state/log-maintenance.json \
  state/deployment.json secrets/user-internal-keys.json secrets/deployment-profile.json; do
  if [ -f "$TARGET/$STATE_FILE" ]; then
    set -- "$@" "$STATE_FILE"
  fi
done
if [ "$#" -gt 0 ]; then
  tar -czf "$STATE_BACKUP_FILE" -C "$TARGET" "$@"
else
  create_empty_archive "$STATE_BACKUP_FILE"
fi
chmod 600 "$STATE_BACKUP_FILE"

online_sqlite_backup() {
  SOURCE_DB=$1
  BACKUP_DB=$2
  python3 - "$SOURCE_DB" "$BACKUP_DB" <<'PY'
import os
import sqlite3
import sys

source_path, backup_path = sys.argv[1:]
with sqlite3.connect(source_path, timeout=30) as source:
    source.execute("PRAGMA busy_timeout = 30000")
    with sqlite3.connect(backup_path) as backup:
        source.backup(backup)
os.chmod(backup_path, 0o600)
PY
}

if [ "$CONTROL_DB_EXISTED" = true ]; then
  online_sqlite_backup "$TARGET/state/control-plane.sqlite3" "$CONTROL_DB_BACKUP_FILE"
fi
if [ "$USAGE_DB_EXISTED" = true ]; then
  online_sqlite_backup "$TARGET/state/usage.sqlite3" "$USAGE_DB_BACKUP_FILE"
fi
python3 "$DATA_GUARD" snapshot "$TARGET" "$DATA_MANIFEST_BEFORE"

env_value() {
  target_env_value "$1" "$2"
}
PREVIOUS_ADMIN_IMAGE=$(env_value ADMIN_IMAGE cliproxy-admin:local)
PREVIOUS_WEB_IMAGE=$(env_value WEB_RUNTIME_IMAGE codex-cpa-web:local)
PREVIOUS_GATEWAY_IMAGE=$(env_value GATEWAY_RUNTIME_IMAGE codex-cpa-gateway:local)
PREVIOUS_EDGE_IMAGE=$(env_value EDGE_RUNTIME_IMAGE codex-cpa-edge:local)
ADMIN_CONTAINER_NAME="$(env_value INSTANCE_NAME cliproxy)-admin"
WEB_CONTAINER_NAME="$(env_value INSTANCE_NAME cliproxy)-web"
EDGE_CONTAINER_NAME="$(env_value INSTANCE_NAME cliproxy)-edge"
LEGACY_GATEWAY_CONTAINER_NAME="$(env_value INSTANCE_NAME cliproxy)-gateway"
PRESERVE_LEGACY_GATEWAY_ON_FIRST_CUTOVER=${PRESERVE_LEGACY_GATEWAY_ON_FIRST_CUTOVER:-false}
case "$PRESERVE_LEGACY_GATEWAY_ON_FIRST_CUTOVER" in
  true|false) ;;
  *)
    echo "PRESERVE_LEGACY_GATEWAY_ON_FIRST_CUTOVER 只能为 true 或 false" >&2
    exit 1
    ;;
esac
INSPECTED_ADMIN_IMAGE=$(docker inspect --format '{{.Config.Image}}' "$ADMIN_CONTAINER_NAME" 2>/dev/null || true)
INSPECTED_WEB_IMAGE=$(docker inspect --format '{{.Config.Image}}' "$WEB_CONTAINER_NAME" 2>/dev/null || true)
INSPECTED_EDGE_IMAGE=$(docker inspect --format '{{.Config.Image}}' "$EDGE_CONTAINER_NAME" 2>/dev/null || true)
WEB_CONTAINER_ID_BEFORE=$(docker inspect --format '{{.Id}}' "$WEB_CONTAINER_NAME" 2>/dev/null || true)
PREVIOUS_ADMIN_IMAGE=${INSPECTED_ADMIN_IMAGE:-$PREVIOUS_ADMIN_IMAGE}
PREVIOUS_WEB_IMAGE=${INSPECTED_WEB_IMAGE:-$PREVIOUS_WEB_IMAGE}
PREVIOUS_EDGE_IMAGE=${INSPECTED_EDGE_IMAGE:-$PREVIOUS_EDGE_IMAGE}
PREVIOUS_ADMIN_APP_ROOT=$(docker image inspect \
  --format '{{index .Config.Labels "io.codex-cpa.app-root"}}' \
  "$PREVIOUS_ADMIN_IMAGE" 2>/dev/null || true)
PREVIOUS_ADMIN_APP_ROOT=${PREVIOUS_ADMIN_APP_ROOT:-/opt/codex-cpa-cluster/app}
PREVIOUS_ADMIN_CLI_PATH="$PREVIOUS_ADMIN_APP_ROOT/scripts/cliproxy.py"
EDGE_CONTAINER_ID_BEFORE=$(docker inspect --format '{{.Id}}' "$EDGE_CONTAINER_NAME" 2>/dev/null || true)
EDGE_STARTED_AT_BEFORE=$(docker inspect --format '{{.State.StartedAt}}' "$EDGE_CONTAINER_NAME" 2>/dev/null || true)
if [ -f "$TARGET/state/compose.env" ]; then
  TARGET_COMPOSE_SERVICES=$(docker compose \
    --project-directory "$TARGET" \
    --env-file "$TARGET/.env" \
    --env-file "$TARGET/state/compose.env" \
    -f "$TARGET/docker-compose.yml" \
    config --services) || TARGET_COMPOSE_SERVICES=
else
  TARGET_COMPOSE_SERVICES=$(docker compose \
    --project-directory "$TARGET" \
    --env-file "$TARGET/.env" \
    -f "$TARGET/docker-compose.yml" \
    config --services) || TARGET_COMPOSE_SERVICES=
fi
if [ -z "$TARGET_COMPOSE_SERVICES" ]; then
  echo "无法解析目标当前 Compose 拓扑，拒绝部署" >&2
  exit 1
fi
TARGET_HAS_LEGACY_GATEWAY=false
TARGET_HAS_EDGE=false
printf '%s\n' "$TARGET_COMPOSE_SERVICES" | grep -Fx gateway >/dev/null \
  && TARGET_HAS_LEGACY_GATEWAY=true
printf '%s\n' "$TARGET_COMPOSE_SERVICES" | grep -Fx edge >/dev/null \
  && TARGET_HAS_EDGE=true
case "$TARGET_HAS_LEGACY_GATEWAY:$TARGET_HAS_EDGE" in
  true:false) LEGACY_TOPOLOGY=true ;;
  false:true) LEGACY_TOPOLOGY=false ;;
  *)
    echo "目标当前 Compose 拓扑不明确：legacy_gateway=$TARGET_HAS_LEGACY_GATEWAY edge=$TARGET_HAS_EDGE" >&2
    exit 1
    ;;
esac

container_published_port() {
  CONTAINER=$1
  CONTAINER_PORT=$2
  docker inspect --format "{{json (index .NetworkSettings.Ports \"$CONTAINER_PORT/tcp\")}}" \
    "$CONTAINER" 2>/dev/null \
    | python3 -c '
import json
import sys

try:
    bindings = json.load(sys.stdin)
except (TypeError, ValueError):
    raise SystemExit(1)
ports = sorted(
    {
        str(item.get("HostPort", "")).strip()
        for item in bindings or []
        if isinstance(item, dict) and str(item.get("HostPort", "")).strip()
    }
)
if len(ports) != 1:
    raise SystemExit(1)
print(ports[0])
'
}

STABLE_EDGE_PUBLIC_PORT=
STABLE_EDGE_INTERNAL_PORT=
if [ "$LEGACY_TOPOLOGY" != true ]; then
  if [ "$(docker inspect --format '{{.State.Running}}' "$EDGE_CONTAINER_NAME" 2>/dev/null || true)" != true ]; then
    echo "稳态发布前必须确认现有 Edge 正在运行" >&2
    exit 1
  fi
  STABLE_EDGE_PUBLIC_PORT=$(container_published_port "$EDGE_CONTAINER_NAME" 8317 || true)
  STABLE_EDGE_INTERNAL_PORT=$(container_published_port "$EDGE_CONTAINER_NAME" 8319 || true)
  if [ -z "$STABLE_EDGE_PUBLIC_PORT" ] || [ -z "$STABLE_EDGE_INTERNAL_PORT" ]; then
    echo "无法确认现有 Edge 的宿主机端口，拒绝部署" >&2
    exit 1
  fi
  if [ -n "$REQUESTED_HEALTH_PORT" ] \
    && [ "$REQUESTED_HEALTH_PORT" != "$STABLE_EDGE_PUBLIC_PORT" ]; then
    echo "指定健康检查端口与现有 Edge 不一致：requested=$REQUESTED_HEALTH_PORT actual=$STABLE_EDGE_PUBLIC_PORT" >&2
    exit 1
  fi
  HEALTH_PORT=$STABLE_EDGE_PUBLIC_PORT
  INTERNAL_HEALTH_PORT=$STABLE_EDGE_INTERNAL_PORT
  echo "稳态 Edge 端口保持不变：public=$HEALTH_PORT internal=$INTERNAL_HEALTH_PORT"
fi

if [ "$PRESERVE_LEGACY_GATEWAY_ON_FIRST_CUTOVER" = true ]; then
  if [ "$LEGACY_TOPOLOGY" != true ]; then
    echo "保留旧 Gateway 只允许用于首次拓扑迁移" >&2
    exit 1
  fi
  if [ "$(docker inspect --format '{{.State.Running}}' "$LEGACY_GATEWAY_CONTAINER_NAME" 2>/dev/null || true)" != true ]; then
    echo "保留旧 Gateway 前必须确认旧 Gateway 正在运行" >&2
    exit 1
  fi
  LEGACY_GATEWAY_PUBLIC_PORT=$(container_published_port "$LEGACY_GATEWAY_CONTAINER_NAME" 8317 || true)
  LEGACY_GATEWAY_INTERNAL_PORT=$(container_published_port "$LEGACY_GATEWAY_CONTAINER_NAME" 8319 || true)
  if [ -z "$LEGACY_GATEWAY_PUBLIC_PORT" ] || [ -z "$LEGACY_GATEWAY_INTERNAL_PORT" ]; then
    echo "无法确认旧 Gateway 的宿主机端口，拒绝保留式迁移" >&2
    exit 1
  fi
  if [ "$LEGACY_GATEWAY_PUBLIC_PORT" = "$HEALTH_PORT" ] \
    || [ "$LEGACY_GATEWAY_INTERNAL_PORT" = "$INTERNAL_HEALTH_PORT" ]; then
    echo "保留旧 Gateway 时，新 Edge 必须使用不同的公网和内部端口" >&2
    exit 1
  fi
  echo "首次迁移将保留旧 Gateway 排空：旧端口=${LEGACY_GATEWAY_PUBLIC_PORT}/${LEGACY_GATEWAY_INTERNAL_PORT} 新 Edge 端口=${HEALTH_PORT}/${INTERNAL_HEALTH_PORT}"
fi

if [ "$LEGACY_TOPOLOGY" != true ] && [ -n "$EDGE_CONTAINER_ID_BEFORE" ]; then
  ACTIVE_GATEWAY_SLOT=$(python3 "$EDGE_SLOT_HELPER" --root "$TARGET" read \
    --fallback "$(env_value GATEWAY_ACTIVE_SLOT blue)")
else
  ACTIVE_GATEWAY_SLOT=$(env_value GATEWAY_ACTIVE_SLOT blue)
fi
case "$ACTIVE_GATEWAY_SLOT" in
  blue|green) ;;
  *) echo "活动 Gateway slot 无效：$ACTIVE_GATEWAY_SLOT" >&2; exit 1 ;;
esac
INACTIVE_GATEWAY_SLOT=$(python3 - "$ACTIVE_GATEWAY_SLOT" <<'PY'
import sys
print("green" if sys.argv[1] == "blue" else "blue")
PY
)
ACTIVE_GATEWAY_SERVICE="gateway-$ACTIVE_GATEWAY_SLOT"
INACTIVE_GATEWAY_SERVICE="gateway-$INACTIVE_GATEWAY_SLOT"
ORIGINAL_GATEWAY_SLOT=$ACTIVE_GATEWAY_SLOT
ORIGINAL_ACTIVE_GATEWAY_SERVICE=$ACTIVE_GATEWAY_SERVICE
ORIGINAL_INACTIVE_GATEWAY_SERVICE=$INACTIVE_GATEWAY_SERVICE
ACTIVE_GATEWAY_CONTAINER_NAME="$(env_value INSTANCE_NAME cliproxy)-$ACTIVE_GATEWAY_SERVICE"
INACTIVE_GATEWAY_CONTAINER_NAME="$(env_value INSTANCE_NAME cliproxy)-$INACTIVE_GATEWAY_SERVICE"
INSPECTED_ACTIVE_GATEWAY_IMAGE=$(docker inspect --format '{{.Config.Image}}' "$ACTIVE_GATEWAY_CONTAINER_NAME" 2>/dev/null || true)
if [ "$LEGACY_TOPOLOGY" = true ]; then
  INSPECTED_ACTIVE_GATEWAY_IMAGE=$(docker inspect --format '{{.Config.Image}}' "$LEGACY_GATEWAY_CONTAINER_NAME" 2>/dev/null || true)
fi
PREVIOUS_GATEWAY_IMAGE=${INSPECTED_ACTIVE_GATEWAY_IMAGE:-$PREVIOUS_GATEWAY_IMAGE}
ACTIVE_GATEWAY_CONTAINER_ID_BEFORE=$(docker inspect --format '{{.Id}}' "$ACTIVE_GATEWAY_CONTAINER_NAME" 2>/dev/null || true)
ACTIVE_GATEWAY_STARTED_AT_BEFORE=$(docker inspect --format '{{.State.StartedAt}}' "$ACTIVE_GATEWAY_CONTAINER_NAME" 2>/dev/null || true)

manifest_component_digest() {
  python3 - "$RELEASE_MANIFEST" "$1" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    payload = json.load(handle)
print(payload["components"][sys.argv[2]]["source_sha256"])
PY
}

ADMIN_SOURCE_DIGEST=$(manifest_component_digest admin)
WEB_SOURCE_DIGEST=$(manifest_component_digest web)
GATEWAY_SOURCE_DIGEST=$(manifest_component_digest gateway)
EDGE_SOURCE_DIGEST=$(manifest_component_digest edge)
ADMIN_COMPONENT_DIGEST=$ADMIN_SOURCE_DIGEST
WEB_COMPONENT_DIGEST=$WEB_SOURCE_DIGEST
GATEWAY_COMPONENT_DIGEST=$GATEWAY_SOURCE_DIGEST
EDGE_COMPONENT_DIGEST=$EDGE_SOURCE_DIGEST
ADMIN_RUNTIME_IMAGE="$RELEASE_IMAGE_PREFIX/codex-cpa-admin:sha256-$ADMIN_COMPONENT_DIGEST"
WEB_RUNTIME_IMAGE="$RELEASE_IMAGE_PREFIX/codex-cpa-web:sha256-$WEB_COMPONENT_DIGEST"
GATEWAY_RUNTIME_IMAGE="$RELEASE_IMAGE_PREFIX/codex-cpa-gateway:sha256-$GATEWAY_COMPONENT_DIGEST"
EDGE_RUNTIME_IMAGE="$RELEASE_IMAGE_PREFIX/codex-cpa-edge:sha256-$EDGE_COMPONENT_DIGEST"
RELEASE_VERSION_IMAGE="$RELEASE_IMAGE_PREFIX/codex-cpa-release:$RELEASE_VERSION"

desired_service_config_hash() {
  SERVICE=$1
  EDGE_IMAGE_OVERRIDE=${2:-$EDGE_RUNTIME_IMAGE}
  if [ -f "$TARGET/state/compose.env" ] && [ -f "$TARGET/compose.accounts.yml" ]; then
    ADMIN_IMAGE=$ADMIN_RUNTIME_IMAGE \
    WEB_RUNTIME_IMAGE=$WEB_RUNTIME_IMAGE \
    GATEWAY_RUNTIME_IMAGE=$GATEWAY_RUNTIME_IMAGE \
    EDGE_RUNTIME_IMAGE=$EDGE_IMAGE_OVERRIDE \
    DEPLOY_ROOT=$TARGET \
      docker compose \
        --project-directory "$TARGET" \
        --env-file "$TARGET/.env" \
        --env-file "$TARGET/state/compose.env" \
        -f "$RELEASE_ROOT/docker-compose.yml" \
        -f "$TARGET/compose.accounts.yml" \
        config --hash "$SERVICE"
  elif [ -f "$TARGET/compose.accounts.yml" ]; then
    ADMIN_IMAGE=$ADMIN_RUNTIME_IMAGE \
    WEB_RUNTIME_IMAGE=$WEB_RUNTIME_IMAGE \
    GATEWAY_RUNTIME_IMAGE=$GATEWAY_RUNTIME_IMAGE \
    EDGE_RUNTIME_IMAGE=$EDGE_IMAGE_OVERRIDE \
    DEPLOY_ROOT=$TARGET \
      docker compose \
        --project-directory "$TARGET" \
        --env-file "$TARGET/.env" \
        -f "$RELEASE_ROOT/docker-compose.yml" \
        -f "$TARGET/compose.accounts.yml" \
        config --hash "$SERVICE"
  elif [ -f "$TARGET/state/compose.env" ]; then
    ADMIN_IMAGE=$ADMIN_RUNTIME_IMAGE \
    WEB_RUNTIME_IMAGE=$WEB_RUNTIME_IMAGE \
    GATEWAY_RUNTIME_IMAGE=$GATEWAY_RUNTIME_IMAGE \
    EDGE_RUNTIME_IMAGE=$EDGE_IMAGE_OVERRIDE \
    DEPLOY_ROOT=$TARGET \
      docker compose \
        --project-directory "$TARGET" \
        --env-file "$TARGET/.env" \
        --env-file "$TARGET/state/compose.env" \
        -f "$RELEASE_ROOT/docker-compose.yml" \
        config --hash "$SERVICE"
  else
    ADMIN_IMAGE=$ADMIN_RUNTIME_IMAGE \
    WEB_RUNTIME_IMAGE=$WEB_RUNTIME_IMAGE \
    GATEWAY_RUNTIME_IMAGE=$GATEWAY_RUNTIME_IMAGE \
    EDGE_RUNTIME_IMAGE=$EDGE_IMAGE_OVERRIDE \
    DEPLOY_ROOT=$TARGET \
      docker compose \
        --project-directory "$TARGET" \
        --env-file "$TARGET/.env" \
        -f "$RELEASE_ROOT/docker-compose.yml" \
        config --hash "$SERVICE"
  fi
}

ADMIN_IMAGE_CHANGED=false
WEB_IMAGE_CHANGED=false
GATEWAY_IMAGE_CHANGED=false
EDGE_IMAGE_CHANGED=false
COMPOSE_CHANGED=false
GATEWAY_CONFIG_CHANGED=false
EDGE_CONFIG_CHANGED=false
WEB_CONFIG_CHANGED=false
[ "$PREVIOUS_ADMIN_IMAGE" = "$ADMIN_RUNTIME_IMAGE" ] || ADMIN_IMAGE_CHANGED=true
[ "$PREVIOUS_WEB_IMAGE" = "$WEB_RUNTIME_IMAGE" ] || WEB_IMAGE_CHANGED=true
[ "$PREVIOUS_GATEWAY_IMAGE" = "$GATEWAY_RUNTIME_IMAGE" ] || GATEWAY_IMAGE_CHANGED=true
[ "$PREVIOUS_EDGE_IMAGE" = "$EDGE_RUNTIME_IMAGE" ] || EDGE_IMAGE_CHANGED=true
cmp -s "$RELEASE_ROOT/docker-compose.yml" "$TARGET/docker-compose.yml" || COMPOSE_CHANGED=true
PREVIOUS_GATEWAY_CONFIG_HASH=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.config-hash"}}' "$ACTIVE_GATEWAY_CONTAINER_NAME" 2>/dev/null || true)
PREVIOUS_EDGE_CONFIG_HASH=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.config-hash"}}' "$EDGE_CONTAINER_NAME" 2>/dev/null || true)
PREVIOUS_WEB_CONFIG_HASH=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.config-hash"}}' "$WEB_CONTAINER_NAME" 2>/dev/null || true)
DESIRED_GATEWAY_CONFIG_HASH=$(desired_service_config_hash "$ACTIVE_GATEWAY_SERVICE" | awk -v service="$ACTIVE_GATEWAY_SERVICE" '$1 == service {print $2}')
DESIRED_EDGE_CONFIG_HASH=$(desired_service_config_hash edge | awk '$1 == "edge" {print $2}')
DESIRED_WEB_CONFIG_HASH=$(desired_service_config_hash web | awk '$1 == "web" {print $2}')
if [ -z "$DESIRED_GATEWAY_CONFIG_HASH" ]; then
  echo "无法计算目标 Gateway Compose 配置指纹" >&2
  exit 1
fi
[ "$PREVIOUS_GATEWAY_CONFIG_HASH" = "$DESIRED_GATEWAY_CONFIG_HASH" ] || GATEWAY_CONFIG_CHANGED=true
if [ -z "$DESIRED_EDGE_CONFIG_HASH" ]; then
  echo "无法计算目标 Edge Compose 配置指纹" >&2
  exit 1
fi
[ "$PREVIOUS_EDGE_CONFIG_HASH" = "$DESIRED_EDGE_CONFIG_HASH" ] || EDGE_CONFIG_CHANGED=true
if [ -z "$DESIRED_WEB_CONFIG_HASH" ]; then
  echo "无法计算目标 Web Compose 配置指纹" >&2
  exit 1
fi
[ "$PREVIOUS_WEB_CONFIG_HASH" = "$DESIRED_WEB_CONFIG_HASH" ] || WEB_CONFIG_CHANGED=true

gateway_apply_required() {
  [ "$1" != "$2" ] \
    || [ "$3" = true ] \
    || [ -z "$4" ]
}

edge_apply_required() {
  [ "$1" != "$2" ] \
    || [ "$3" = true ] \
    || [ -z "$4" ]
}

GATEWAY_APPLY_REQUIRED=false
if gateway_apply_required \
  "$PREVIOUS_GATEWAY_IMAGE" "$GATEWAY_RUNTIME_IMAGE" \
  "$GATEWAY_CONFIG_CHANGED" "$ACTIVE_GATEWAY_CONTAINER_ID_BEFORE"; then
  GATEWAY_APPLY_REQUIRED=true
fi
EDGE_APPLY_REQUIRED=false
if edge_apply_required \
  "$PREVIOUS_EDGE_IMAGE" "$EDGE_RUNTIME_IMAGE" \
  "$EDGE_CONFIG_CHANGED" "$EDGE_CONTAINER_ID_BEFORE"; then
  EDGE_APPLY_REQUIRED=true
fi
if [ "$LEGACY_TOPOLOGY" = true ]; then
  GATEWAY_APPLY_REQUIRED=true
  EDGE_APPLY_REQUIRED=true
fi
WEB_APPLY_REQUIRED=false
if [ "$WEB_IMAGE_CHANGED" = true ] || [ "$WEB_CONFIG_CHANGED" = true ] \
  || [ -z "$WEB_CONTAINER_ID_BEFORE" ]; then
  WEB_APPLY_REQUIRED=true
fi
image_has_component_digest() {
  IMAGE=$1
  COMPONENT=$2
  DIGEST=$3
  [ "$(docker image inspect --format '{{index .Config.Labels "io.codex-cpa.component"}}' "$IMAGE" 2>/dev/null || true)" = "$COMPONENT" ] \
    && [ "$(docker image inspect --format '{{index .Config.Labels "io.codex-cpa.component-digest"}}' "$IMAGE" 2>/dev/null || true)" = "$DIGEST" ]
}

ensure_release_metadata() {
  docker pull "$RELEASE_VERSION_IMAGE"
  python3 - "$RELEASE_VERSION_IMAGE" "$RELEASE_VERSION" "$COMMIT_SHA" <<'PY'
import json
import subprocess
import sys

image, expected_version, expected_revision = sys.argv[1:]
result = subprocess.run(
    ["docker", "image", "inspect", "--format", "{{json .Config.Labels}}", image],
    check=True,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True,
)
labels = json.loads(result.stdout)
if not isinstance(labels, dict) or labels.get("io.codex-cpa.component") != "release":
    raise SystemExit("发布元数据镜像类型无效")
if labels.get("org.opencontainers.image.version") != expected_version:
    raise SystemExit("发布元数据版本与 RELEASE_VERSION 不一致")
revision = str(labels.get("org.opencontainers.image.revision") or "")
if expected_revision != "manual" and revision != expected_revision:
    raise SystemExit("发布元数据 revision 与发布包不一致")
PY
}

ensure_admin_image() {
  docker pull "$ADMIN_RUNTIME_IMAGE"
  if ! image_has_component_digest "$ADMIN_RUNTIME_IMAGE" admin "$ADMIN_COMPONENT_DIGEST"; then
    echo "拉取的 Admin 镜像与发布包指纹不匹配：$ADMIN_RUNTIME_IMAGE" >&2
    return 1
  fi
}

ensure_web_image() {
  docker pull "$WEB_RUNTIME_IMAGE"
  if ! image_has_component_digest "$WEB_RUNTIME_IMAGE" web "$WEB_COMPONENT_DIGEST"; then
    echo "拉取的 Web 镜像与发布包指纹不匹配：$WEB_RUNTIME_IMAGE" >&2
    return 1
  fi
}

ensure_gateway_image() {
  docker pull "$GATEWAY_RUNTIME_IMAGE"
  if ! image_has_component_digest "$GATEWAY_RUNTIME_IMAGE" gateway "$GATEWAY_COMPONENT_DIGEST"; then
    echo "拉取的 Gateway 镜像与发布包指纹不匹配：$GATEWAY_RUNTIME_IMAGE" >&2
    return 1
  fi
}

ensure_edge_image() {
  docker pull "$EDGE_RUNTIME_IMAGE"
  if ! image_has_component_digest "$EDGE_RUNTIME_IMAGE" edge "$EDGE_COMPONENT_DIGEST"; then
    echo "拉取的 Edge 镜像与发布包指纹不匹配：$EDGE_RUNTIME_IMAGE" >&2
    return 1
  fi
}

ensure_release_metadata
ensure_admin_image
ensure_web_image
docker run --rm "$WEB_RUNTIME_IMAGE" nginx -t

run_cli_in_image() {
  CLI_IMAGE=$1
  CLI_PATH=$2
  shift 2
  docker run --rm -i \
    -v "$TARGET:$TARGET" \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -w "$TARGET" \
    -e "CLIPROXY_ROOT=$TARGET" \
    -e "DEPLOY_ROOT=$TARGET" \
    "$CLI_IMAGE" \
    python3 "$CLI_PATH" --root "$TARGET" "$@"
}

release_cli() {
  run_cli_in_image \
    "$ADMIN_RUNTIME_IMAGE" \
    /opt/codex-cpa-runtime/scripts/cliproxy.py \
    "$@"
}

import_profile_once() {
  if [ -n "$PROFILE_SOURCE" ]; then
    release_cli profile import-once --preserve-existing - < "$PROFILE_SOURCE"
  elif [ -f "$TARGET/secrets/deployment-profile.json" ]; then
    release_cli profile import-once --preserve-existing "$TARGET/secrets/deployment-profile.json"
  fi
}
if [ "$GATEWAY_APPLY_REQUIRED" = true ]; then
  ensure_gateway_image
  docker run --rm "$GATEWAY_RUNTIME_IMAGE" openresty -t
fi
EDGE_RUNTIME_REUSED=false
if [ "$EDGE_APPLY_REQUIRED" = true ]; then
  ensure_edge_image
  docker run --rm "$EDGE_RUNTIME_IMAGE" openresty -t
  PREVIOUS_EDGE_DESIRED_CONFIG_HASH=$(desired_service_config_hash \
    edge "$PREVIOUS_EDGE_IMAGE" | awk '$1 == "edge" {print $2}')
  if [ -n "$PREVIOUS_EDGE_IMAGE" ] \
    && [ -n "$PREVIOUS_EDGE_DESIRED_CONFIG_HASH" ] \
    && [ "$PREVIOUS_EDGE_CONFIG_HASH" = "$PREVIOUS_EDGE_DESIRED_CONFIG_HASH" ] \
    && python3 "$RELEASE_IMAGE_COMPARE" edge-runtime-equivalent \
      "$PREVIOUS_EDGE_IMAGE" "$EDGE_RUNTIME_IMAGE"; then
    EDGE_RUNTIME_IMAGE=$PREVIOUS_EDGE_IMAGE
    EDGE_IMAGE_CHANGED=false
    EDGE_CONFIG_CHANGED=false
    EDGE_APPLY_REQUIRED=false
    EDGE_RUNTIME_REUSED=true
    echo "Edge 发布镜像与当前运行时等价且沿用旧镜像后 Compose 指纹不变；保留现有 Edge 容器"
  fi
fi
echo "发布计划：admin=$ADMIN_IMAGE_CHANGED Web=$WEB_IMAGE_CHANGED Gateway=$GATEWAY_IMAGE_CHANGED Edge=$EDGE_IMAGE_CHANGED Compose=$COMPOSE_CHANGED Web配置=$WEB_CONFIG_CHANGED Gateway配置=$GATEWAY_CONFIG_CHANGED Edge配置=$EDGE_CONFIG_CHANGED 首次拓扑迁移=$LEGACY_TOPOLOGY Web应用=$WEB_APPLY_REQUIRED Gateway应用=$GATEWAY_APPLY_REQUIRED Edge应用=$EDGE_APPLY_REQUIRED Edge复用=$EDGE_RUNTIME_REUSED"
if [ "$EDGE_APPLY_REQUIRED" = true ] && [ "$ALLOW_EDGE_RECREATE" != true ]; then
  echo "本次发布需要重建 Edge，常规发布已停止；请在维护窗口显式设置 ALLOW_EDGE_RECREATE=true" >&2
  exit 1
fi

write_deploy_root_env() {
  python3 - "$TARGET/.env" "$TARGET" <<'PY'
import os
import re
import sys
from pathlib import Path

path = Path(sys.argv[1])
deploy_root = sys.argv[2]
if not Path(deploy_root).is_absolute() or not re.fullmatch(r"/[A-Za-z0-9._/-]+", deploy_root):
    raise RuntimeError("DEPLOY_ROOT must be an absolute path using safe path characters")
lines = path.read_text(encoding="utf-8").splitlines() if path.exists() else []
rendered = []
seen = False
for line in lines:
    if re.match(r"^\s*(?:export\s+)?DEPLOY_ROOT\s*=", line):
        if not seen:
            rendered.append("DEPLOY_ROOT={}".format(deploy_root))
        seen = True
    else:
        rendered.append(line)
if not seen:
    rendered.append("DEPLOY_ROOT={}".format(deploy_root))
temporary = path.with_name(".{}.{}.tmp".format(path.name, os.getpid()))
temporary.write_text("\n".join(rendered).rstrip() + "\n", encoding="utf-8")
os.chmod(temporary, 0o600)
os.replace(temporary, path)
os.chmod(path, 0o600)
PY
}

compose() {
  if [ -f "$TARGET/state/compose.env" ]; then
    docker compose \
      --project-directory "$TARGET" \
      --env-file "$TARGET/.env" \
      --env-file "$TARGET/state/compose.env" \
      -f "$TARGET/docker-compose.yml" \
      -f "$TARGET/compose.accounts.yml" \
      "$@"
  else
    docker compose \
      --project-directory "$TARGET" \
      --env-file "$TARGET/.env" \
      -f "$TARGET/docker-compose.yml" \
      -f "$TARGET/compose.accounts.yml" \
      "$@"
  fi
}

PREVIOUS_CONFIGURED_CPA_SERVICES=$(compose config --services | awk '/^cliproxy-/ {print}')
PREVIOUS_RUNNING_CPA_SERVICES=$(compose ps --status running --services | awk '/^cliproxy-/ {print}')
PRESERVED_CLIPROXY_IMAGE=$(docker inspect --format '{{.Image}}' \
  "$(env_value INSTANCE_NAME cliproxy)-management" 2>/dev/null || true)
if [ -z "$PRESERVED_CLIPROXY_IMAGE" ]; then
  for SERVICE in $PREVIOUS_RUNNING_CPA_SERVICES; do
    CONTAINER_ID=$(compose ps -q "$SERVICE")
    [ -n "$CONTAINER_ID" ] || continue
    PRESERVED_CLIPROXY_IMAGE=$(docker inspect --format '{{.Image}}' \
      "$CONTAINER_ID" 2>/dev/null || true)
    [ -z "$PRESERVED_CLIPROXY_IMAGE" ] || break
  done
fi
if [ -z "$PRESERVED_CLIPROXY_IMAGE" ]; then
  CONFIGURED_CLIPROXY_IMAGE=$(target_env_value CLIPROXY_IMAGE "")
  if [ -n "$CONFIGURED_CLIPROXY_IMAGE" ]; then
    PRESERVED_CLIPROXY_IMAGE=$(docker image inspect --format '{{.Id}}' \
      "$CONFIGURED_CLIPROXY_IMAGE" 2>/dev/null || true)
  fi
fi
if [ -n "$PRESERVED_CLIPROXY_IMAGE" ] \
  && ! printf '%s' "$PRESERVED_CLIPROXY_IMAGE" \
    | grep -Eq '^sha256:[0-9a-f]{64}$'; then
  echo "无法识别当前 CPA 的不可变镜像 ID" >&2
  exit 1
fi

assert_edge_unchanged() {
  CURRENT_EDGE_CONTAINER_ID=$(docker inspect --format '{{.Id}}' "$EDGE_CONTAINER_NAME" 2>/dev/null || true)
  CURRENT_EDGE_STARTED_AT=$(docker inspect --format '{{.State.StartedAt}}' "$EDGE_CONTAINER_NAME" 2>/dev/null || true)
  if [ -z "$EDGE_CONTAINER_ID_BEFORE" ] \
    || [ "$CURRENT_EDGE_CONTAINER_ID" != "$EDGE_CONTAINER_ID_BEFORE" ] \
    || [ "$CURRENT_EDGE_STARTED_AT" != "$EDGE_STARTED_AT_BEFORE" ]; then
    echo "Edge 复用期间容器或启动时间发生变化" >&2
    return 1
  fi
  echo "Edge 容器保持不变：$CURRENT_EDGE_CONTAINER_ID"
}

assert_data_plane_unchanged() {
  assert_edge_unchanged || return 1
  CURRENT_ACTIVE_GATEWAY_CONTAINER_ID=$(docker inspect --format '{{.Id}}' "$ACTIVE_GATEWAY_CONTAINER_NAME" 2>/dev/null || true)
  CURRENT_ACTIVE_GATEWAY_STARTED_AT=$(docker inspect --format '{{.State.StartedAt}}' "$ACTIVE_GATEWAY_CONTAINER_NAME" 2>/dev/null || true)
  CURRENT_GATEWAY_IMAGE=$(docker inspect --format '{{.Config.Image}}' "$ACTIVE_GATEWAY_CONTAINER_NAME" 2>/dev/null || true)
  if [ -z "$ACTIVE_GATEWAY_CONTAINER_ID_BEFORE" ] \
    || [ "$CURRENT_ACTIVE_GATEWAY_CONTAINER_ID" != "$ACTIVE_GATEWAY_CONTAINER_ID_BEFORE" ] \
    || [ "$CURRENT_ACTIVE_GATEWAY_STARTED_AT" != "$ACTIVE_GATEWAY_STARTED_AT_BEFORE" ] \
    || [ "$CURRENT_GATEWAY_IMAGE" != "$PREVIOUS_GATEWAY_IMAGE" ]; then
    echo "控制面/Web 发布期间 Edge 或活动 Gateway 的容器、启动时间或镜像发生变化" >&2
    return 1
  fi
  echo "控制面/Web 发布未重启数据面：edge=$CURRENT_EDGE_CONTAINER_ID gateway=$CURRENT_ACTIVE_GATEWAY_CONTAINER_ID"
}

http_get() {
  docker run --rm --network host "$ADMIN_RUNTIME_IMAGE" python3 -c '
import sys
import urllib.request

opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
with opener.open(sys.argv[1], timeout=3) as response:
    sys.stdout.buffer.write(response.read())
' "$1"
}

invalid_key_status() {
  docker run --rm --network host "$ADMIN_RUNTIME_IMAGE" python3 -c '
import sys
import urllib.error
import urllib.request

opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
request = urllib.request.Request(
    sys.argv[1],
    headers={"Authorization": "Bearer invalid_deployment_probe"},
)
try:
    with opener.open(request, timeout=3) as response:
        print(response.status)
except urllib.error.HTTPError as error:
    print(error.code)
' "$1"
}

wait_for_service() {
  attempts=0
  while [ "$attempts" -lt 60 ]; do
    if http_get "http://127.0.0.1:$HEALTH_PORT/__health" >/dev/null 2>&1 \
      && http_get "http://127.0.0.1:$HEALTH_PORT/admin/" >/dev/null 2>&1 \
      && http_get "http://127.0.0.1:$HEALTH_PORT/usage/" >/dev/null 2>&1; then
      return 0
    fi
    attempts=$((attempts + 1))
    sleep 1
  done
  return 1
}

diagnose_edge_startup() {
  echo "Edge 启动验收失败，采集容器状态与脱敏错误日志" >&2
  docker inspect --format \
    'edge_state={{.State.Status}} running={{.State.Running}} exit={{.State.ExitCode}} error={{.State.Error}} started={{.State.StartedAt}} finished={{.State.FinishedAt}} restart={{.RestartCount}}' \
    "$EDGE_CONTAINER_NAME" >&2 2>/dev/null || true
  docker logs --tail 100 "$EDGE_CONTAINER_NAME" >&2 2>/dev/null || true
  if [ -f "$TARGET/logs/gateway/edge-error.log" ]; then
    tail -n 100 "$TARGET/logs/gateway/edge-error.log" \
      | sed -E 's/(Authorization:|Bearer)[[:space:]]+[^[:space:]]+/\1 [REDACTED]/Ig' >&2 \
      || true
  fi
}

wait_for_runtime_services() {
  python3 - "$TARGET" "$ACTIVE_GATEWAY_SERVICE" <<'PY'
import json
import os
import subprocess
import sys
import time
from pathlib import Path

root = Path(sys.argv[1]).resolve()
active_gateway = sys.argv[2]
compose = [
    "docker", "compose", "--project-directory", str(root),
    "--env-file", str(root / ".env"),
]
compose_environment = root / "state" / "compose.env"
if compose_environment.is_file():
    compose.extend(["--env-file", str(compose_environment)])
compose.extend(
    [
        "-f", str(root / "docker-compose.yml"),
        "-f", str(root / "compose.accounts.yml"),
    ]
)
expected = set(
    subprocess.check_output(compose + ["config", "--services"], text=True).splitlines()
)
expected.discard("gateway-blue")
expected.discard("gateway-green")
expected.add(active_gateway)
public_accounts_path = root / "state" / "public" / "accounts.json"
disabled_services = set()
if public_accounts_path.is_file():
    payload = json.loads(public_accounts_path.read_text(encoding="utf-8"))
    for account in payload.get("accounts", []):
        if isinstance(account, dict) and account.get("group_enabled") is False:
            account_id = str(account.get("id") or "").strip()
            if account_id:
                disabled_services.add("cliproxy-{}".format(account_id))
expected.difference_update(disabled_services)
deadline = time.time() + int(os.environ.get("RUNTIME_SERVICES_WAIT_SECONDS", "600"))
last = []
while time.time() < deadline:
    running = set(
        subprocess.check_output(
            compose + ["ps", "--status", "running", "--services"], text=True
        ).splitlines()
    )
    missing = sorted(expected - running)
    unhealthy = []
    for service in sorted(expected & running):
        identifiers = subprocess.check_output(
            compose + ["ps", "-q", service], text=True
        ).splitlines()
        for identifier in identifiers:
            payload = json.loads(
                subprocess.check_output(
                    ["docker", "inspect", identifier], text=True
                )
            )[0]
            health = (payload.get("State", {}).get("Health") or {}).get("Status")
            if health and health != "healthy":
                unhealthy.append("{}:{}".format(service, health))
    last = missing + unhealthy
    if not last:
        print(
            "Compose 必需服务验证通过：{}，停用 CPA 保持停止：{}（inactive Gateway 可停止）".format(
                len(expected), len(disabled_services)
            )
        )
        raise SystemExit(0)
    time.sleep(2)
raise RuntimeError("Compose 服务未全部就绪：{}".format(", ".join(last)))
PY
}

compose_network_name() {
  NETWORK_ID=$(docker inspect --format '{{range $name, $network := .NetworkSettings.Networks}}{{$name}}{{"\n"}}{{end}}' \
    "$ADMIN_CONTAINER_NAME" 2>/dev/null | head -n 1)
  printf '%s' "${NETWORK_ID:-$(env_value DOCKER_NETWORK_NAME cliproxy-backend)}"
}

gateway_service_url() {
  printf 'http://%s:8319' "$1"
}

gateway_service_public_url() {
  printf 'http://%s:8317' "$1"
}

http_get_from_network() {
  NETWORK_NAME=$(compose_network_name)
  docker run --rm --network "$NETWORK_NAME" "$ADMIN_RUNTIME_IMAGE" \
    python3 -c '
import sys
import urllib.request
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
with opener.open(sys.argv[1], timeout=5) as response:
    sys.stdout.buffer.write(response.read())
' "$1"
}

invalid_key_status_from_network() {
  NETWORK_NAME=$(compose_network_name)
  docker run --rm --network "$NETWORK_NAME" "$ADMIN_RUNTIME_IMAGE" \
    python3 -c '
import sys
import urllib.error
import urllib.request
request = urllib.request.Request(sys.argv[1], headers={"Authorization": "Bearer invalid_deployment_probe"})
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
try:
    with opener.open(request, timeout=5) as response:
        print(response.status)
except urllib.error.HTTPError as error:
    print(error.code)
' "$1"
}

wait_for_gateway_snapshots() {
  SERVICE=${1:-}
  attempts=0
  while [ "$attempts" -lt 30 ]; do
    if [ -n "$SERVICE" ]; then
      SNAPSHOT_STATUS=$(http_get_from_network \
        "$(gateway_service_url "$SERVICE")/__internal/snapshots" 2>/dev/null || true)
      INVALID_STATUS=$(invalid_key_status_from_network \
        "$(gateway_service_public_url "$SERVICE")/v1/models" 2>/dev/null || true)
    else
      SNAPSHOT_STATUS=$(http_get \
        "http://127.0.0.1:$INTERNAL_HEALTH_PORT/__internal/snapshots" 2>/dev/null || true)
      INVALID_STATUS=$(invalid_key_status \
        "http://127.0.0.1:$HEALTH_PORT/v1/models" 2>/dev/null || true)
    fi
    if printf '%s' "$SNAPSHOT_STATUS" | python3 -c '
import json
import sys
import time

try:
    payload = json.load(sys.stdin)
except (TypeError, ValueError):
    raise SystemExit(1)
auth = payload.get("auth", {})
quota = payload.get("quota", {})
now = int(time.time())
auth_loader_at = int(auth.get("snapshot_loader_success_at", 0))
quota_loader_at = int(quota.get("snapshot_loader_success_at", 0))
heartbeat_at = int(quota.get("heartbeat_at", 0))
heartbeat_stale_after = max(5, int(quota.get("heartbeat_stale_after", 0)))
valid = (
    len(str(auth.get("active_generation", ""))) == 32
    and 0 <= now - auth_loader_at <= 5
    and len(str(quota.get("active_generation", ""))) == 32
    and 0 <= now - quota_loader_at <= 5
    and bool(quota.get("heartbeat_ok"))
    and 0 <= now - heartbeat_at <= heartbeat_stale_after
)
raise SystemExit(0 if valid else 1)
' \
      && [ "$INVALID_STATUS" = "401" ]; then
      return 0
    fi
    attempts=$((attempts + 1))
    sleep 1
  done
  echo "网关鉴权或额度快照未在限定时间内激活" >&2
  return 1
}

ensure_and_verify_business_cpas() {
  docker run --rm -i --network host \
    -v "$TARGET:$TARGET" \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -w "$TARGET" \
    -e "CLIPROXY_ROOT=$TARGET" \
    -e "DEPLOY_ROOT=$TARGET" \
    "$ADMIN_RUNTIME_IMAGE" \
    python3 - "$TARGET" $PREVIOUS_RUNNING_CPA_SERVICES <<'PY'
import json
import sys
import time
import urllib.request
from pathlib import Path

root = Path(sys.argv[1])
previous_running = set(sys.argv[2:])
sys.path.insert(0, "/opt/codex-cpa-runtime/scripts")
from cliproxy import ControlPlane

app = ControlPlane(root)
accounts = app.accounts()
services = app.services()
active = app.active_records()
internal = app.sync_internal_keys() if hasattr(app, "sync_internal_keys") else {}
shared_internal_key = next(
    (record.get("key", "") for record in internal.values() if record.get("key")),
    "",
)
legacy_keys = {}
for record in active:
    legacy_keys.setdefault(record["account"], record["key"])
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

for account, service in services.items():
    if accounts[account].get("group_enabled") is False:
        app.compose("stop", service)
        print("{} 已停用；保持服务停止并跳过模型验证".format(account), flush=True)
        continue
    if service not in previous_running:
        app.compose("stop", service)
        print("{} 发布前未运行；保持服务停止".format(account), flush=True)
        continue
    container = app._docker_json(
        "container", "inspect", app.account_container_name(account)
    )
    image_id = str((container or {}).get("Image") or "").strip()
    if not image_id.startswith("sha256:"):
        raise RuntimeError("{} 缺少发布前不可变镜像 ID".format(account))
    # Compose changes may recreate the service, but the exact pre-release
    # image ID is passed explicitly. Application releases never move a CPA to
    # the update channel or to another account's partially applied version.
    app._compose_with_image(image_id, "up", "-d", "--no-deps", service)
    key = shared_internal_key or legacy_keys.get(account, "")
    if not key:
        print("{} 已就绪；当前没有可用于模型探测的有效 Key".format(account), flush=True)
        continue
    url = "http://127.0.0.1:{}/v1/models".format(accounts[account]["port"])
    deadline = time.time() + 60
    last_error = "服务尚未就绪"
    while time.time() < deadline:
        request = urllib.request.Request(url, headers={"Authorization": "Bearer " + key})
        try:
            with opener.open(request, timeout=3) as response:
                payload = json.load(response)
            models = payload.get("data") if isinstance(payload, dict) else None
            if response.status == 200 and isinstance(models, list):
                print("{} 内部 Key 验证通过：MODELS {}".format(account, len(models)), flush=True)
                break
            last_error = "模型列表格式异常"
        except Exception as error:
            last_error = "{}: {}".format(type(error).__name__, error)
        time.sleep(1)
    else:
        raise RuntimeError("{} 内部 Key 验证失败：{}".format(account, last_error))
PY
}

verify_gateway_routes() {
  docker run --rm -i --network host \
    -v "$TARGET:$TARGET" \
    -w "$TARGET" \
    -e "CLIPROXY_ROOT=$TARGET" \
    -e "DEPLOY_ROOT=$TARGET" \
    "$ADMIN_RUNTIME_IMAGE" \
    python3 /opt/codex-cpa-runtime/scripts/gateway_release_probe.py \
      --root "$TARGET" \
      --public-url "http://127.0.0.1:$HEALTH_PORT" \
      --internal-url "http://127.0.0.1:$INTERNAL_HEALTH_PORT" \
      --label "Edge active Gateway"
}

verify_gateway_routes_on_service() {
  SERVICE=$1
  NETWORK_NAME=$(compose_network_name)
  docker run --rm -i --network "$NETWORK_NAME" \
    -v "$TARGET:$TARGET" \
    -w "$TARGET" \
    -e "CLIPROXY_ROOT=$TARGET" \
    -e "DEPLOY_ROOT=$TARGET" \
    "$ADMIN_RUNTIME_IMAGE" \
    python3 /opt/codex-cpa-runtime/scripts/gateway_release_probe.py \
      --root "$TARGET" \
      --public-url "http://$SERVICE:8317" \
      --internal-url "http://$SERVICE:8319" \
      --label "Inactive $SERVICE"
}

quarantine_legacy_source() {
  python3 - "$TARGET" "$BACKUP_DIR" "$PIPELINE_ID" "$COMMIT_SHA" <<'PY'
import json
import os
import sys
from pathlib import Path

target = Path(sys.argv[1]).resolve()
backup_dir = Path(sys.argv[2]).resolve()
name = "legacy-source-{}-{}".format(sys.argv[3], sys.argv[4])
destination = backup_dir / name
if destination.exists():
    raise RuntimeError("legacy source quarantine already exists")
destination.mkdir(mode=0o700)
names = [
    ".dockerignore", ".env.example", ".gitignore", ".harness", ".claude",
    ".git", "AGENTS.md", "LICENSE", "Makefile", "README.md", "__pycache__", "tmp",
    "config", "docs", "admin", "dashboard", "edge", "gateway", "portal", "release", "scripts", "tests", "web",
]
names.extend(path.name for path in target.glob("._*"))
moved = []
for item in names:
    source = target / item
    if not source.exists() and not source.is_symlink():
        continue
    resolved_parent = source.parent.resolve()
    if resolved_parent != target:
        raise RuntimeError("refusing to move path outside deployment root")
    os.replace(source, destination / source.name)
    moved.append(source.name)
(destination / "manifest.json").write_text(
    json.dumps({"moved": sorted(set(moved))}, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)
os.chmod(destination / "manifest.json", 0o600)
print("已将宿主机旧源码移入可恢复隔离目录：{}".format(destination))
PY
  [ "$?" -eq 0 ] || return 1
  SOURCE_QUARANTINED=true
}

GATEWAY_APPLY_STARTED=false
EDGE_APPLY_STARTED=false
GATEWAY_SWITCHED=false
LEGACY_GATEWAY_STOPPED=false
LEGACY_GATEWAY_PRESERVED=false
SOURCE_QUARANTINED=false

write_active_gateway_slot() {
  SLOT=$1
  python3 "$EDGE_SLOT_HELPER" --root "$TARGET" write "$SLOT" >/dev/null || return 1
  ACTIVE_GATEWAY_SLOT=$SLOT
  ACTIVE_GATEWAY_SERVICE="gateway-$SLOT"
  if [ "$SLOT" = blue ]; then
    INACTIVE_GATEWAY_SLOT=green
  else
    INACTIVE_GATEWAY_SLOT=blue
  fi
  INACTIVE_GATEWAY_SERVICE="gateway-$INACTIVE_GATEWAY_SLOT"
  ACTIVE_GATEWAY_CONTAINER_NAME="$(env_value INSTANCE_NAME cliproxy)-$ACTIVE_GATEWAY_SERVICE"
  INACTIVE_GATEWAY_CONTAINER_NAME="$(env_value INSTANCE_NAME cliproxy)-$INACTIVE_GATEWAY_SERVICE"
}

write_active_slot_file_only() {
  python3 "$EDGE_SLOT_HELPER" --root "$TARGET" write "$1" >/dev/null || return 1
}

reload_edge() {
  compose exec -T edge openresty -t || return 1
  compose exec -T edge openresty -s reload || return 1
}

gateway_inflight_total() {
  SERVICE=$1
  http_get_from_network "$(gateway_service_url "$SERVICE")/__stats" 2>/dev/null \
    | python3 -c '
import json
import sys
try:
    payload = json.load(sys.stdin)
except (TypeError, ValueError):
    raise SystemExit(1)
print(sum(max(0, int(item.get("inflight", 0))) for item in payload if isinstance(item, dict)))
'
}

drain_gateway_slot() {
  SERVICE=$1
  attempts=0
  while [ "$attempts" -lt "$GATEWAY_DRAIN_TIMEOUT_SECONDS" ]; do
    TOTAL=$(gateway_inflight_total "$SERVICE" || true)
    if [ "$TOTAL" = 0 ]; then
      echo "Gateway slot 已排空：$SERVICE"
      return 0
    fi
    attempts=$((attempts + 1))
    sleep 1
  done
  echo "Gateway slot 在 ${GATEWAY_DRAIN_TIMEOUT_SECONDS} 秒内未排空，保留该 slot 运行：$SERVICE" >&2
  return 1
}

switch_gateway_slot() {
  NEW_SLOT=$1
  OLD_SERVICE=$ACTIVE_GATEWAY_SERVICE
  write_active_gateway_slot "$NEW_SLOT" || return 1
  # The selected file is already changed. Mark the cutover as attempted before
  # signalling Edge so rollback always restores/reloads the original file even
  # when the reload command returns an ambiguous failure.
  GATEWAY_SWITCHED=true
  reload_edge || return 1
  wait_for_service || return 1
  wait_for_gateway_snapshots || return 1
  verify_gateway_routes || return 1
  if drain_gateway_slot "$OLD_SERVICE"; then
    compose stop "$OLD_SERVICE" || return 1
  else
    echo "旧 Gateway 尚有未完成请求，本次发布拒绝标记成功" >&2
    return 1
  fi
}

apply_web_release() {
  if [ "$WEB_APPLY_REQUIRED" = true ]; then
    compose up -d --no-deps web || return 1
  fi
}

apply_gateway_release() {
  if [ "$LEGACY_TOPOLOGY" = true ]; then
    GATEWAY_APPLY_STARTED=true
    EDGE_APPLY_STARTED=true
    write_active_slot_file_only "$ACTIVE_GATEWAY_SLOT" || return 1
    compose up -d --no-deps web "$ACTIVE_GATEWAY_SERVICE" || return 1
    compose exec -T "$ACTIVE_GATEWAY_SERVICE" openresty -t || return 1
    wait_for_gateway_snapshots "$ACTIVE_GATEWAY_SERVICE" || return 1
    verify_gateway_routes_on_service "$ACTIVE_GATEWAY_SERVICE" || return 1
    if [ "$PRESERVE_LEGACY_GATEWAY_ON_FIRST_CUTOVER" = true ]; then
      LEGACY_GATEWAY_PRESERVED=true
      echo "首次 Edge 迁移：新 Edge 使用独立端口，旧 Gateway 保持运行以承载已有长连接"
    else
      echo "首次 Edge 迁移：停止旧 Gateway 并接管宿主机端口；此步骤只允许在维护窗口执行"
      docker stop "$LEGACY_GATEWAY_CONTAINER_NAME" >/dev/null || return 1
      LEGACY_GATEWAY_STOPPED=true
    fi
    # A failed/rolled-back migration may leave a stopped Edge container whose
    # Docker network endpoint no longer exists. Never reuse it for the stable
    # host-port cutover; recreate it with a fresh endpoint.
    compose up -d --no-deps --force-recreate edge || return 1
    if ! wait_for_service; then
      diagnose_edge_startup
      return 1
    fi
    wait_for_gateway_snapshots || return 1
    verify_gateway_routes || return 1
    return 0
  fi

  if [ "$EDGE_APPLY_REQUIRED" = true ]; then
    # Edge 本身拥有稳定宿主机端口，镜像/端口/挂载变化仍需要短暂重建。
    # 常规 Admin/Web/Gateway 发布不会进入此路径。
    EDGE_APPLY_STARTED=true
    write_active_slot_file_only "$ACTIVE_GATEWAY_SLOT" || return 1
    compose up -d --no-deps --force-recreate edge || return 1
    if ! wait_for_service; then
      diagnose_edge_startup
      return 1
    fi
  fi

  if [ "$GATEWAY_APPLY_REQUIRED" = true ]; then
    GATEWAY_APPLY_STARTED=true
    compose up -d --no-deps "$INACTIVE_GATEWAY_SERVICE" || return 1
    compose exec -T "$INACTIVE_GATEWAY_SERVICE" openresty -t || return 1
    wait_for_gateway_snapshots "$INACTIVE_GATEWAY_SERVICE" || return 1
    verify_gateway_routes_on_service "$INACTIVE_GATEWAY_SERVICE" || return 1
    switch_gateway_slot "$INACTIVE_GATEWAY_SLOT" || return 1
  elif [ "$EDGE_APPLY_REQUIRED" != true ]; then
    assert_data_plane_unchanged || return 1
  fi
  return 0
}

verify_data_plane_apply_invariant() {
  if [ "$EDGE_RUNTIME_REUSED" = true ]; then
    assert_edge_unchanged || return 1
  fi
  if [ "$GATEWAY_APPLY_REQUIRED" != true ] && [ "$EDGE_APPLY_REQUIRED" != true ]; then
    assert_data_plane_unchanged || return 1
  fi
}

restore_control_services() {
  if [ "$LEGACY_TOPOLOGY" = true ] && [ -f "$TARGET/scripts/cliproxy.py" ]; then
    chmod +x "$TARGET/scripts/cliproxy.py" "$TARGET/scripts/cliproxy.sh" || return 1
    run_cli_in_image \
      "$PREVIOUS_ADMIN_IMAGE" "$TARGET/scripts/cliproxy.py" render || return 1
    compose up -d --build --no-deps \
      admin usage-collector log-maintenance management || return 1
  else
    run_cli_in_image \
      "$PREVIOUS_ADMIN_IMAGE" \
      "$PREVIOUS_ADMIN_CLI_PATH" render || return 1
    compose up -d --no-deps \
      admin usage-collector log-maintenance management || return 1
  fi
}

restore_previous_business_cpas() {
  for SERVICE in $PREVIOUS_CONFIGURED_CPA_SERVICES; do
    WAS_RUNNING=false
    for RUNNING_SERVICE in $PREVIOUS_RUNNING_CPA_SERVICES; do
      if [ "$RUNNING_SERVICE" = "$SERVICE" ]; then
        WAS_RUNNING=true
        break
      fi
    done
    if [ "$WAS_RUNNING" = true ]; then
      compose up -d --no-deps "$SERVICE" || return 1
    else
      compose stop "$SERVICE" >/dev/null 2>&1 || return 1
    fi
  done
}

restore_release() {
  echo "部署验收失败，正在恢复上一版源码、Compose 和控制状态" >&2
  compose stop admin usage-collector log-maintenance >/dev/null 2>&1 || true
  if [ -f "$ENV_BACKUP_FILE" ]; then
    cp "$ENV_BACKUP_FILE" "$TARGET/.env" || return 1
    chmod 600 "$TARGET/.env" || return 1
    write_deploy_root_env || return 1
  fi
  python3 - "$RUNTIME_BACKUP_FILE" "$TARGET" "$MASTER_KEY_EXISTED" <<'PY'
import os
import shutil
import sys
import tarfile
from pathlib import Path

archive = Path(sys.argv[1]).resolve()
target = Path(sys.argv[2]).resolve()
master_key_existed = sys.argv[3] == "true"
names = {
    "secrets/cpa-management.key",
    "secrets/wecom-webhook.url",
    "secrets/issued-keys.tsv",
    "secrets/deployment-profile.json",
    "secrets/control-plane.key",
}
with tarfile.open(archive, "r:gz") as handle:
    members = {member.name.lstrip("./"): member for member in handle.getmembers()}
    for name in sorted(names):
        member = members.get(name)
        destination = target / name
        if member is None:
            if name == "secrets/control-plane.key" and not master_key_existed:
                try:
                    destination.unlink()
                except FileNotFoundError:
                    pass
            continue
        if not member.isfile():
            raise RuntimeError("runtime backup member is not a file: {}".format(name))
        source = handle.extractfile(member)
        if source is None:
            raise RuntimeError("runtime backup member cannot be read: {}".format(name))
        destination.parent.mkdir(parents=True, exist_ok=True)
        temporary = destination.with_name(".{}.restore".format(destination.name))
        with temporary.open("wb") as output:
            shutil.copyfileobj(source, output)
        os.chmod(temporary, member.mode & 0o777)
        os.replace(temporary, destination)
PY
  [ "$?" -eq 0 ] || return 1
  if [ -f "$ENV_BACKUP_FILE" ]; then
    cp "$ENV_BACKUP_FILE" "$TARGET/.env" || return 1
    chmod 600 "$TARGET/.env" || return 1
  fi
  if [ "$TARGET_COMPOSE_ENV_EXISTED" = true ]; then
    cp "$COMPOSE_ENV_BACKUP_FILE" "$TARGET/state/compose.env" || return 1
    chmod 600 "$TARGET/state/compose.env" || return 1
  else
    rm -f "$TARGET/state/compose.env" || return 1
  fi
  write_deploy_root_env || return 1
  tar -xzf "$BACKUP_FILE" -C "$TARGET" || return 1
  tar -xzf "$STATE_BACKUP_FILE" -C "$TARGET" || return 1
  if [ "$SOURCE_QUARANTINED" = true ]; then
    python3 - "$TARGET" "$BACKUP_DIR" "$PIPELINE_ID" "$COMMIT_SHA" <<'PY'
import json
import shutil
import sys
from pathlib import Path

target = Path(sys.argv[1]).resolve()
quarantine = Path(sys.argv[2]).resolve() / "legacy-source-{}-{}".format(sys.argv[3], sys.argv[4])
manifest = quarantine / "manifest.json"
if manifest.is_file():
    for name in json.loads(manifest.read_text(encoding="utf-8")).get("moved", []):
        source = quarantine / name
        destination = target / name
        if not source.exists() or destination.exists():
            continue
        if source.is_dir():
            shutil.copytree(source, destination, symlinks=True)
        elif source.is_symlink():
            destination.symlink_to(source.readlink())
        else:
            shutil.copy2(source, destination)
PY
    [ "$?" -eq 0 ] || return 1
  fi
  if [ "$CONTROL_DB_EXISTED" = true ]; then
    python3 - "$TARGET/state" "$CONTROL_DB_BACKUP_FILE" <<'PY'
import os
import shutil
import sys
from pathlib import Path

state = Path(sys.argv[1]).resolve()
backup = Path(sys.argv[2]).resolve()
for name in ("control-plane.sqlite3", "control-plane.sqlite3-wal", "control-plane.sqlite3-shm"):
    path = state / name
    if path.exists() and name != "control-plane.sqlite3":
        path.unlink()
shutil.copy2(backup, state / "control-plane.sqlite3")
os.chmod(state / "control-plane.sqlite3", 0o600)
PY
    [ "$?" -eq 0 ] || return 1
  else
    python3 - "$TARGET/state" <<'PY'
import sys
from pathlib import Path

state = Path(sys.argv[1]).resolve()
for name in ("control-plane.sqlite3", "control-plane.sqlite3-wal", "control-plane.sqlite3-shm"):
    try:
        (state / name).unlink()
    except FileNotFoundError:
        pass
PY
    [ "$?" -eq 0 ] || return 1
  fi
  if [ "$LEGACY_TOPOLOGY" = true ]; then
    docker stop \
      "$EDGE_CONTAINER_NAME" \
      "$ACTIVE_GATEWAY_CONTAINER_NAME" \
      "$INACTIVE_GATEWAY_CONTAINER_NAME" >/dev/null 2>&1 || true
    if [ "$LEGACY_GATEWAY_STOPPED" = true ]; then
      docker start "$LEGACY_GATEWAY_CONTAINER_NAME" >/dev/null || return 1
    fi
    restore_control_services || return 1
  else
    ACTIVE_GATEWAY_SLOT=$ORIGINAL_GATEWAY_SLOT
    ACTIVE_GATEWAY_SERVICE=$ORIGINAL_ACTIVE_GATEWAY_SERVICE
    INACTIVE_GATEWAY_SERVICE=$ORIGINAL_INACTIVE_GATEWAY_SERVICE
    write_active_slot_file_only "$ORIGINAL_GATEWAY_SLOT" || return 1
    compose up -d --no-deps "$ORIGINAL_ACTIVE_GATEWAY_SERVICE" || return 1
    if [ "$EDGE_APPLY_STARTED" = true ] || [ "$GATEWAY_SWITCHED" = true ]; then
      compose up -d --no-deps edge || return 1
      compose exec -T edge openresty -t || return 1
      compose exec -T edge openresty -s reload || return 1
    fi
    if [ "$GATEWAY_APPLY_STARTED" = true ]; then
      if [ "$GATEWAY_SWITCHED" = true ]; then
        ROLLBACK_INFLIGHT=$(gateway_inflight_total "$ORIGINAL_INACTIVE_GATEWAY_SERVICE" || true)
        if [ "$ROLLBACK_INFLIGHT" = 0 ]; then
          compose stop "$ORIGINAL_INACTIVE_GATEWAY_SERVICE" >/dev/null 2>&1 || true
        else
          echo "回滚后新 Gateway 仍有请求或无法读取计数，保持运行直至人工确认：$ORIGINAL_INACTIVE_GATEWAY_SERVICE inflight=${ROLLBACK_INFLIGHT:-unknown}" >&2
        fi
      else
        compose stop "$ORIGINAL_INACTIVE_GATEWAY_SERVICE" >/dev/null 2>&1 || true
      fi
    fi
    restore_control_services || return 1
    compose up -d --no-deps web || return 1
    if [ "$GATEWAY_APPLY_STARTED" != true ] && [ "$EDGE_APPLY_STARTED" != true ]; then
      assert_data_plane_unchanged || return 1
    fi
  fi
  restore_previous_business_cpas || return 1
  wait_for_service || return 1
  if [ "$LEGACY_TOPOLOGY" = true ]; then
    compose exec -T gateway openresty -t || return 1
  else
    wait_for_runtime_services || return 1
    compose exec -T edge openresty -t || return 1
    compose exec -T "$ACTIVE_GATEWAY_SERVICE" openresty -t || return 1
  fi
  wait_for_gateway_snapshots || return 1
  verify_gateway_routes || return 1
}

apply_release() {
  APPLY_RELEASE_STARTED=true
  # Schema-v2 writers treat removed compatibility JSON as an empty-state
  # change and can overwrite SQLite while the v3 release is taking over.
  cp "$RELEASE_ROOT/docker-compose.yml" "$TARGET/docker-compose.yml" \
    && chmod 644 "$TARGET/docker-compose.yml" \
    && write_deploy_root_env \
    && compose stop admin usage-collector log-maintenance \
    && release_cli store migrate-secrets \
    && import_profile_once \
    && release_cli store cleanup-projections \
    && release_cli render \
    && release_cli stage-deployment \
      --version "$RELEASE_VERSION" \
      --commit "$COMMIT_SHA" \
      --pipeline "$PIPELINE_ID" \
      --deployed-at "$DEPLOYED_AT" \
      --metadata-image "$RELEASE_METADATA_IMAGE" \
      --admin-image "$ADMIN_RUNTIME_IMAGE" \
      --web-image "$WEB_RUNTIME_IMAGE" \
      --gateway-image "$GATEWAY_RUNTIME_IMAGE" \
      --edge-image "$EDGE_RUNTIME_IMAGE" \
      --gateway-port "$HEALTH_PORT" \
      --gateway-internal-port "$INTERNAL_HEALTH_PORT" \
      --preserve-cliproxy-image "$PRESERVED_CLIPROXY_IMAGE" \
    && compose config --quiet \
    && release_cli store verify \
    && compose up -d --no-deps \
      admin usage-collector log-maintenance management \
    && apply_web_release \
    && apply_gateway_release \
    && ensure_and_verify_business_cpas \
    && wait_for_service \
    && wait_for_runtime_services \
    && compose exec -T edge openresty -t \
    && compose exec -T "$ACTIVE_GATEWAY_SERVICE" openresty -t \
    && wait_for_gateway_snapshots \
    && verify_gateway_routes \
    && python3 "$DATA_GUARD" snapshot "$TARGET" "$DATA_MANIFEST_AFTER" \
    && python3 "$DATA_GUARD" compare "$DATA_MANIFEST_BEFORE" "$DATA_MANIFEST_AFTER" \
    && quarantine_legacy_source \
    && release_cli store migrate-secrets --cleanup \
    && release_cli store verify \
    && wait_for_service \
    && wait_for_runtime_services \
    && wait_for_gateway_snapshots \
    && verify_gateway_routes \
    && verify_data_plane_apply_invariant \
    && python3 "$DATA_GUARD" snapshot "$TARGET" "$DATA_MANIFEST_CLEAN" \
    && python3 "$DATA_GUARD" compare "$DATA_MANIFEST_BEFORE" "$DATA_MANIFEST_CLEAN" \
    && release_cli record-deployment \
      --version "$RELEASE_VERSION" \
      --commit "$COMMIT_SHA" \
      --pipeline "$PIPELINE_ID" \
      --deployed-at "$DEPLOYED_AT" \
      --metadata-image "$RELEASE_METADATA_IMAGE" \
      --admin-image "$ADMIN_RUNTIME_IMAGE" \
      --web-image "$WEB_RUNTIME_IMAGE" \
      --gateway-image "$GATEWAY_RUNTIME_IMAGE" \
      --edge-image "$EDGE_RUNTIME_IMAGE" \
      --gateway-port "$HEALTH_PORT" \
      --gateway-internal-port "$INTERNAL_HEALTH_PORT" \
    && release_cli store verify
}

DEPLOYED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
if ! apply_release; then
  if ! restore_release; then
    echo "自动恢复失败，请使用以下备份手工恢复：" >&2
    echo "$BACKUP_FILE" >&2
    echo "$STATE_BACKUP_FILE" >&2
    echo "$RUNTIME_BACKUP_FILE" >&2
    echo "$CONTROL_DB_BACKUP_FILE" >&2
    echo "$USAGE_DB_BACKUP_FILE" >&2
  fi
  exit 1
fi

CONTROL_DB_BACKUP_RESULT=not-applicable
USAGE_DB_BACKUP_RESULT=not-applicable
[ "$CONTROL_DB_EXISTED" = true ] && CONTROL_DB_BACKUP_RESULT=$CONTROL_DB_BACKUP_FILE
[ "$USAGE_DB_EXISTED" = true ] && USAGE_DB_BACKUP_RESULT=$USAGE_DB_BACKUP_FILE
echo "部署成功：commit=$COMMIT_SHA pipeline=$PIPELINE_ID port=$HEALTH_PORT admin_image=$ADMIN_RUNTIME_IMAGE web_image=$WEB_RUNTIME_IMAGE gateway_image=$GATEWAY_RUNTIME_IMAGE edge_image=$EDGE_RUNTIME_IMAGE active_slot=$ACTIVE_GATEWAY_SLOT gateway_applied=$GATEWAY_APPLY_STARTED edge_applied=$EDGE_APPLY_STARTED edge_runtime_reused=$EDGE_RUNTIME_REUSED legacy_gateway_preserved=$LEGACY_GATEWAY_PRESERVED source_backup=$BACKUP_FILE runtime_backup=$RUNTIME_BACKUP_FILE state_backup=$STATE_BACKUP_FILE control_db_backup=$CONTROL_DB_BACKUP_RESULT usage_db_backup=$USAGE_DB_BACKUP_RESULT"
