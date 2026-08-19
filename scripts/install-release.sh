#!/usr/bin/env sh
set -eu

ARCHIVE=${1:?请提供发布包路径}
TARGET=${2:-/opt/codex-cpa-cluster}
PROFILE_SOURCE=${3:-}
RELEASE_VERSION=${RELEASE_VERSION:?请设置 RELEASE_VERSION}
RELEASE_IMAGE_PREFIX=${RELEASE_IMAGE_PREFIX:?请设置 RELEASE_IMAGE_PREFIX}
RELEASE_METADATA_IMAGE=${RELEASE_METADATA_IMAGE:-$RELEASE_IMAGE_PREFIX/codex-cpa-release:latest}
COMMIT_SHA=${RELEASE_COMMIT_SHA:-manual}
OPERATION_ID=${RELEASE_OPERATION_ID:-manual}

if ! printf '%s' "$RELEASE_VERSION" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$'; then
  echo "RELEASE_VERSION 必须是语义化版本：$RELEASE_VERSION" >&2
  exit 1
fi
if ! printf '%s' "$RELEASE_IMAGE_PREFIX" | grep -Eq '^[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9._/-]+$'; then
  echo "RELEASE_IMAGE_PREFIX 无效：$RELEASE_IMAGE_PREFIX" >&2
  exit 1
fi
case "$TARGET" in
  /|""|*[!A-Za-z0-9._/-]*)
    echo "部署目录必须是使用安全字符的绝对路径，且不能是根目录：$TARGET" >&2
    exit 1
    ;;
  /*) ;;
  *) echo "部署目录必须是绝对路径：$TARGET" >&2; exit 1 ;;
esac
if [ ! -f "$ARCHIVE" ]; then
  echo "发布包不存在：$ARCHIVE" >&2
  exit 1
fi
if [ -n "$PROFILE_SOURCE" ] && [ ! -f "$PROFILE_SOURCE" ]; then
  echo "部署配置档案不存在：$PROFILE_SOURCE" >&2
  exit 1
fi
for COMMAND in docker python3 tar; do
  if ! command -v "$COMMAND" >/dev/null 2>&1; then
    echo "生产主机缺少依赖：$COMMAND" >&2
    exit 1
  fi
done
docker info >/dev/null
docker compose version >/dev/null
if ! python3 -c 'import sqlite3; import xml.etree.ElementTree as ET; ET.fromstring("<svg/>")'; then
  echo "生产主机 Python 缺少可用的 SQLite 或 XML 标准库" >&2
  exit 1
fi

if [ -e "$TARGET" ]; then
  if [ ! -d "$TARGET" ]; then
    echo "部署目标不是目录：$TARGET" >&2
    exit 1
  fi
  if [ -n "$(find "$TARGET" -mindepth 1 -maxdepth 1 -print -quit)" ]; then
    echo "首次安装只允许使用空目录：$TARGET" >&2
    exit 1
  fi
else
  mkdir -p "$TARGET"
fi

RELEASE_ROOT=$(mktemp -d)
cleanup_release_root() {
  python3 - "$RELEASE_ROOT" <<'PY'
import shutil
import sys
from pathlib import Path

path = Path(sys.argv[1]).resolve()
if not path.name.startswith("tmp") and "codex-cpa" not in path.name:
    raise RuntimeError("refusing to remove unexpected temporary directory")
if path.exists():
    shutil.rmtree(path)
PY
}
trap cleanup_release_root EXIT HUP INT TERM

python3 - "$ARCHIVE" "$RELEASE_ROOT" <<'PY'
import posixpath
import sys
import tarfile
from pathlib import PurePosixPath

archive, destination = sys.argv[1:]
with tarfile.open(archive, "r:gz") as handle:
    for member in handle.getmembers():
        path = PurePosixPath(member.name)
        if path.is_absolute() or ".." in path.parts:
            raise RuntimeError("release archive contains an unsafe path")
        if not (member.isfile() or member.isdir() or member.issym() or member.islnk()):
            raise RuntimeError("release archive contains an unsupported file type")
        if member.issym() or member.islnk():
            link = PurePosixPath(member.linkname)
            base = path.parent.as_posix() if member.issym() else "."
            resolved = posixpath.normpath(posixpath.join(base, link.as_posix()))
            if link.is_absolute() or resolved == ".." or resolved.startswith("../"):
                raise RuntimeError("release archive contains an unsafe link")
    handle.extractall(destination)
PY

MANIFEST="$RELEASE_ROOT/release-manifest.json"
MANIFEST_HELPER="$RELEASE_ROOT/scripts/release_manifest.py"
APP_CLI="$RELEASE_ROOT/scripts/cliproxy.py"
if [ ! -f "$RELEASE_ROOT/docker-compose.yml" ] \
  || [ ! -f "$RELEASE_ROOT/.env.example" ] \
  || [ ! -f "$RELEASE_ROOT/codex-cpa" ] \
  || [ ! -f "$MANIFEST" ] \
  || [ ! -f "$MANIFEST_HELPER" ] \
  || [ ! -f "$APP_CLI" ]; then
  echo "发布包缺少首次安装文件" >&2
  exit 1
fi
python3 "$MANIFEST_HELPER" verify --root "$RELEASE_ROOT" --manifest "$MANIFEST"

manifest_digest() {
  python3 - "$MANIFEST" "$1" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    print(json.load(handle)["components"][sys.argv[2]]["source_sha256"])
PY
}

ADMIN_DIGEST=$(manifest_digest admin)
WEB_DIGEST=$(manifest_digest web)
GATEWAY_DIGEST=$(manifest_digest gateway)
EDGE_DIGEST=$(manifest_digest edge)
ADMIN_IMAGE="$RELEASE_IMAGE_PREFIX/codex-cpa-admin:sha256-$ADMIN_DIGEST"
WEB_IMAGE="$RELEASE_IMAGE_PREFIX/codex-cpa-web:sha256-$WEB_DIGEST"
GATEWAY_IMAGE="$RELEASE_IMAGE_PREFIX/codex-cpa-gateway:sha256-$GATEWAY_DIGEST"
EDGE_IMAGE="$RELEASE_IMAGE_PREFIX/codex-cpa-edge:sha256-$EDGE_DIGEST"
METADATA_VERSION_IMAGE="$RELEASE_IMAGE_PREFIX/codex-cpa-release:$RELEASE_VERSION"

verify_component_image() {
  IMAGE=$1
  COMPONENT=$2
  DIGEST=$3
  docker pull "$IMAGE"
  if [ "$(docker image inspect --format '{{index .Config.Labels "io.codex-cpa.component"}}' "$IMAGE")" != "$COMPONENT" ] \
    || [ "$(docker image inspect --format '{{index .Config.Labels "io.codex-cpa.component-digest"}}' "$IMAGE")" != "$DIGEST" ]; then
    echo "镜像与发布包组件指纹不匹配：$IMAGE" >&2
    exit 1
  fi
}

docker pull "$METADATA_VERSION_IMAGE"
python3 - "$METADATA_VERSION_IMAGE" "$RELEASE_VERSION" "$COMMIT_SHA" <<'PY'
import json
import subprocess
import sys

image, version, revision = sys.argv[1:]
labels = json.loads(subprocess.check_output(
    ["docker", "image", "inspect", "--format", "{{json .Config.Labels}}", image],
    text=True,
))
if labels.get("io.codex-cpa.component") != "release":
    raise RuntimeError("release metadata image type is invalid")
if labels.get("org.opencontainers.image.version") != version:
    raise RuntimeError("release metadata version does not match")
if revision != "manual" and labels.get("org.opencontainers.image.revision") != revision:
    raise RuntimeError("release metadata revision does not match")
PY
verify_component_image "$ADMIN_IMAGE" admin "$ADMIN_DIGEST"
verify_component_image "$WEB_IMAGE" web "$WEB_DIGEST"
verify_component_image "$GATEWAY_IMAGE" gateway "$GATEWAY_DIGEST"
verify_component_image "$EDGE_IMAGE" edge "$EDGE_DIGEST"
docker run --rm "$WEB_IMAGE" nginx -t
docker run --rm "$GATEWAY_IMAGE" openresty -t
docker run --rm "$EDGE_IMAGE" openresty -t

mkdir -p \
  "$TARGET/auth" \
  "$TARGET/backups/deployments" \
  "$TARGET/bin" \
  "$TARGET/configs" \
  "$TARGET/logs/gateway" \
  "$TARGET/management/auth" \
  "$TARGET/management/config/static" \
  "$TARGET/management/logs" \
  "$TARGET/management/plugins" \
  "$TARGET/secrets" \
  "$TARGET/state/edge" \
  "$TARGET/state/gateway" \
  "$TARGET/state/public"
chmod 700 "$TARGET/backups/deployments" "$TARGET/secrets" "$TARGET/state"
cp "$RELEASE_ROOT/docker-compose.yml" "$TARGET/docker-compose.yml"
cp "$RELEASE_ROOT/.env.example" "$TARGET/.env"
chmod 644 "$TARGET/docker-compose.yml"
chmod 600 "$TARGET/.env"

python3 - "$TARGET/.env" "$TARGET" <<'PY'
import os
import re
import sys
from pathlib import Path

path = Path(sys.argv[1])
replacements = {
    "DEPLOY_ROOT": sys.argv[2],
}
rendered = []
seen = set()
for line in path.read_text(encoding="utf-8").splitlines():
    match = re.match(r"^([A-Za-z_][A-Za-z0-9_]*)=", line)
    if match and match.group(1) in replacements:
        name = match.group(1)
        rendered.append("{}={}".format(name, replacements[name]))
        seen.add(name)
    else:
        rendered.append(line)
for name, value in replacements.items():
    if name not in seen:
        rendered.append("{}={}".format(name, value))
temporary = path.with_name(".env.install")
temporary.write_text("\n".join(rendered).rstrip() + "\n", encoding="utf-8")
os.chmod(temporary, 0o600)
os.replace(temporary, path)
PY

python3 - "$TARGET" <<'PY'
import json
import os
import secrets
import sys
from pathlib import Path

root = Path(sys.argv[1])
key = secrets.token_hex(32)
key_path = root / "secrets" / "cpa-management.key"
key_path.write_text(key + "\n", encoding="utf-8")
os.chmod(key_path, 0o600)

# Management is a control-only CLIProxyAPI instance. Generate its minimal
# initial configuration because it is runtime state and does not belong in Git.
config = root / "management" / "config" / "config.yaml"
config.write_text(
    "\n".join([
        'host: ""',
        "port: 8317",
        "tls:",
        "  enable: false",
        '  cert: ""',
        '  key: ""',
        "remote-management:",
        "  allow-remote: true",
        "  secret-key: {}".format(json.dumps(key)),
        "  disable-control-panel: false",
        "  disable-auto-update-panel: false",
        'auth-dir: "~/.cli-proxy-api"',
        "api-keys: []",
        "",
    ]),
    encoding="utf-8",
)
os.chmod(config, 0o600)
PY

if [ -n "$PROFILE_SOURCE" ]; then
  cp "$PROFILE_SOURCE" "$TARGET/secrets/deployment-profile.json"
  chmod 600 "$TARGET/secrets/deployment-profile.json"
fi

run_cli() {
  docker run --rm -i \
    -v "$TARGET:$TARGET" \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -w "$TARGET" \
    -e "CLIPROXY_ROOT=$TARGET" \
    -e "DEPLOY_ROOT=$TARGET" \
    "$ADMIN_IMAGE" \
    python3 /opt/codex-cpa-runtime/scripts/cliproxy.py --root "$TARGET" "$@"
}

compose() {
  docker compose \
    --project-directory "$TARGET" \
    --env-file "$TARGET/.env" \
    --env-file "$TARGET/state/compose.env" \
    -f "$TARGET/docker-compose.yml" \
    -f "$TARGET/compose.accounts.yml" \
    "$@"
}

run_cli store migrate-secrets
if [ -f "$TARGET/secrets/deployment-profile.json" ]; then
  run_cli profile import-once "$TARGET/secrets/deployment-profile.json"
fi
run_cli render
HEALTH_PORT=$(awk -F= '$1 == "GATEWAY_PORT" {print $2}' "$TARGET/state/compose.env" | tail -n 1)
INTERNAL_HEALTH_PORT=$(awk -F= '$1 == "GATEWAY_INTERNAL_PORT" {print $2}' "$TARGET/state/compose.env" | tail -n 1)
HEALTH_PORT=${HEALTH_PORT:-18317}
INTERNAL_HEALTH_PORT=${INTERNAL_HEALTH_PORT:-18316}
DEPLOYED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
run_cli stage-deployment \
  --version "$RELEASE_VERSION" \
  --commit "$COMMIT_SHA" \
  --pipeline "$OPERATION_ID" \
  --deployed-at "$DEPLOYED_AT" \
  --metadata-image "$RELEASE_METADATA_IMAGE" \
  --admin-image "$ADMIN_IMAGE" \
  --web-image "$WEB_IMAGE" \
  --gateway-image "$GATEWAY_IMAGE" \
  --edge-image "$EDGE_IMAGE" \
  --gateway-port "$HEALTH_PORT" \
  --gateway-internal-port "$INTERNAL_HEALTH_PORT" >/dev/null
run_cli store verify
compose config --quiet
compose pull management
compose up -d --no-deps \
  admin usage-collector log-maintenance management web gateway-blue edge

attempt=0
while [ "$attempt" -lt 120 ]; do
  PENDING=
  for SERVICE in admin usage-collector log-maintenance management web gateway-blue edge; do
    CONTAINER_ID=$(compose ps -q "$SERVICE")
    if [ -z "$CONTAINER_ID" ]; then
      PENDING="$PENDING $SERVICE(missing)"
      continue
    fi
    STATUS=$(docker inspect --format '{{.State.Running}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$CONTAINER_ID")
    case "$STATUS" in
      "true healthy"|"true none") ;;
      *) PENDING="$PENDING $SERVICE($STATUS)" ;;
    esac
  done
  if [ -z "$PENDING" ]; then
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done
if [ -n "$PENDING" ]; then
  echo "服务未在 120 秒内就绪：$PENDING" >&2
  compose ps >&2 || true
  exit 1
fi

compose exec -T edge openresty -t
compose exec -T gateway-blue openresty -t
run_cli store verify
docker run --rm --network host \
  -v "$TARGET:$TARGET" \
  -w "$TARGET" \
  -e "CLIPROXY_ROOT=$TARGET" \
  -e "DEPLOY_ROOT=$TARGET" \
  "$ADMIN_IMAGE" \
  python3 /opt/codex-cpa-runtime/scripts/gateway_release_probe.py \
    --root "$TARGET" \
    --public-url "http://127.0.0.1:$HEALTH_PORT" \
    --internal-url "http://127.0.0.1:$INTERNAL_HEALTH_PORT" \
    --label "Fresh install Gateway"
run_cli record-deployment \
  --version "$RELEASE_VERSION" \
  --commit "$COMMIT_SHA" \
  --pipeline "$OPERATION_ID" \
  --deployed-at "$DEPLOYED_AT" \
  --metadata-image "$RELEASE_METADATA_IMAGE" \
  --admin-image "$ADMIN_IMAGE" \
  --web-image "$WEB_IMAGE" \
  --gateway-image "$GATEWAY_IMAGE" \
  --edge-image "$EDGE_IMAGE" \
  --gateway-port "$HEALTH_PORT" \
  --gateway-internal-port "$INTERNAL_HEALTH_PORT" >/dev/null

python3 - "$RELEASE_ROOT/codex-cpa" "$TARGET/bin/codex-cpa" <<'PY'
import os
import sys
from pathlib import Path

source, destination = map(Path, sys.argv[1:])
temporary = destination.with_name(".codex-cpa.install")
temporary.write_bytes(source.read_bytes())
os.chmod(temporary, 0o755)
os.replace(temporary, destination)
PY

echo "安装成功：version=$RELEASE_VERSION target=$TARGET"
echo "首次登录密钥：$TARGET/secrets/cpa-management.key"
echo "完成首次登录后，请执行：docker compose --project-directory $TARGET --env-file $TARGET/.env --env-file $TARGET/state/compose.env -f $TARGET/docker-compose.yml -f $TARGET/compose.accounts.yml exec -T admin codex-cpa store migrate-secrets --cleanup"
