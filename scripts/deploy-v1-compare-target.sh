#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ENV_FILE=${V1_COMPARE_ENV_FILE:-$ROOT_DIR/v1-compare.env}
ACTION=${1:-config}

case "$ENV_FILE" in /*) ;; *) ENV_FILE="$ROOT_DIR/$ENV_FILE" ;; esac

[ -f "$ENV_FILE" ] || {
  echo "missing v1 comparison environment file: $ENV_FILE" >&2
  exit 1
}

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

: "${V1_COMPARE_ADMIN_IMAGE:?V1_COMPARE_ADMIN_IMAGE is required}"
: "${V1_COMPARE_DOCKER_READ_PROXY_IMAGE:?V1_COMPARE_DOCKER_READ_PROXY_IMAGE is required}"
: "${V1_COMPARE_WEB_IMAGE:?V1_COMPARE_WEB_IMAGE is required}"
: "${V1_COMPARE_GATEWAY_IMAGE:?V1_COMPARE_GATEWAY_IMAGE is required}"
: "${V1_COMPARE_EDGE_IMAGE:?V1_COMPARE_EDGE_IMAGE is required}"
: "${V1_COMPARE_SOURCE_REVISION:?V1_COMPARE_SOURCE_REVISION is required}"
: "${V1_COMPARE_DEPLOY_ROOT:?V1_COMPARE_DEPLOY_ROOT is required}"
: "${V1_COMPARE_COMPOSE_PROJECT_NAME:?V1_COMPARE_COMPOSE_PROJECT_NAME is required}"
: "${V1_COMPARE_INSTANCE_NAME:?V1_COMPARE_INSTANCE_NAME is required}"
: "${V1_COMPARE_UPSTREAM_NETWORK:?V1_COMPARE_UPSTREAM_NETWORK is required}"
: "${V1_COMPARE_UPSTREAM_DEPLOY_ROOT:?V1_COMPARE_UPSTREAM_DEPLOY_ROOT is required}"
: "${V1_COMPARE_CONFIRM_UPSTREAM_DEPLOY_ROOT:?V1_COMPARE_CONFIRM_UPSTREAM_DEPLOY_ROOT is required}"
: "${V1_COMPARE_PUBLIC_PORT:?V1_COMPARE_PUBLIC_PORT is required}"
: "${V1_COMPARE_INTERNAL_PORT:?V1_COMPARE_INTERNAL_PORT is required}"
: "${V1_COMPARE_HOST_DOCKER_SOCKET_PATH:?V1_COMPARE_HOST_DOCKER_SOCKET_PATH is required}"
: "${V1_COMPARE_LIVE_COMPOSE_PROJECT:?V1_COMPARE_LIVE_COMPOSE_PROJECT is required}"

V1_COMPARE_PUBLIC_BIND_ADDRESS=${V1_COMPARE_PUBLIC_BIND_ADDRESS:-127.0.0.1}
V1_COMPARE_PUBLIC_PROBE_HOST=${V1_COMPARE_PUBLIC_PROBE_HOST:-$V1_COMPARE_PUBLIC_BIND_ADDRESS}
export V1_COMPARE_PUBLIC_BIND_ADDRESS V1_COMPARE_PUBLIC_PROBE_HOST

case "$V1_COMPARE_SOURCE_REVISION" in
  *[!0-9a-f]*)
    echo "V1_COMPARE_SOURCE_REVISION must be one lowercase 40-character Git revision" >&2
    exit 1
    ;;
esac
[ "${#V1_COMPARE_SOURCE_REVISION}" -eq 40 ] || {
  echo "V1_COMPARE_SOURCE_REVISION must be one lowercase 40-character Git revision" >&2
  exit 1
}

[ "$V1_COMPARE_HOST_DOCKER_SOCKET_PATH" = /var/run/docker.sock ] || {
  echo "v1 comparison read proxy requires the exact host Docker socket path" >&2
  exit 1
}
case "$V1_COMPARE_LIVE_COMPOSE_PROJECT" in
  ""|*[!A-Za-z0-9_.-]*)
    echo "v1 comparison live Compose project is invalid" >&2
    exit 1
    ;;
esac

canonical_root=$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.argv[1]).resolve())' "$V1_COMPARE_DEPLOY_ROOT")
case "$canonical_root" in
  / | /home/AI/CLIProxyAPI | /opt/codex-cpa-cluster)
    echo "refusing live or broad v1 comparison root: $canonical_root" >&2
    exit 1
    ;;
esac
V1_COMPARE_DEPLOY_ROOT=$canonical_root
export V1_COMPARE_DEPLOY_ROOT

require_disposable_upstream() {
  canonical_upstream_root=$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.argv[1]).resolve())' "$V1_COMPARE_UPSTREAM_DEPLOY_ROOT")
  [ "$V1_COMPARE_CONFIRM_UPSTREAM_DEPLOY_ROOT" = "$V1_COMPARE_UPSTREAM_DEPLOY_ROOT" ] \
    && [ "$canonical_upstream_root" = "$V1_COMPARE_UPSTREAM_DEPLOY_ROOT" ] || {
    echo "v1 comparison upstream root confirmation does not match its canonical path" >&2
    exit 1
  }
  case "$canonical_upstream_root" in
    / | /home/AI/CLIProxyAPI | /opt/codex-cpa-cluster)
      echo "refusing live or broad v1 comparison upstream root: $canonical_upstream_root" >&2
      exit 1
      ;;
  esac
  [ -f "$canonical_upstream_root/.v2-isolated-copy.json" ] || {
    echo "v1 comparison upstream isolated-copy marker is missing" >&2
    exit 1
  }
  network_scope=$(docker network inspect --format '{{index .Labels "io.codex-cpa.scope"}}' "$V1_COMPARE_UPSTREAM_NETWORK")
  [ "$network_scope" = migration-disposable ] || {
    echo "v1 comparison upstream network is not migration-disposable" >&2
    exit 1
  }
  upstream_containers=$(docker ps -aq \
    --filter "label=com.docker.compose.project=$V1_COMPARE_LIVE_COMPOSE_PROJECT")
  [ -n "$upstream_containers" ] || {
    echo "v1 comparison disposable upstream project has no containers" >&2
    exit 1
  }
  for upstream_container in $upstream_containers; do
    docker inspect "$upstream_container" | python3 -c '
import json
import pathlib
import sys

container = json.load(sys.stdin)[0]
network = sys.argv[1]
root = pathlib.Path(sys.argv[2]).resolve()
if network not in container.get("NetworkSettings", {}).get("Networks", {}):
    raise SystemExit(1)
binds = [
    pathlib.Path(item.get("Source", "")).resolve()
    for item in container.get("Mounts", [])
    if item.get("Type") == "bind"
]
if not binds or any(source != root and root not in source.parents for source in binds):
    raise SystemExit(1)
' "$V1_COMPARE_UPSTREAM_NETWORK" "$canonical_upstream_root" || {
      echo "v1 comparison upstream container escapes the disposable root or network" >&2
      exit 1
    }
  done
  V1_COMPARE_UPSTREAM_DEPLOY_ROOT=$canonical_upstream_root
  export V1_COMPARE_UPSTREAM_DEPLOY_ROOT
}

require_isolated_root() {
  [ -d "$V1_COMPARE_DEPLOY_ROOT" ] || {
    echo "isolated v1 comparison root does not exist: $V1_COMPARE_DEPLOY_ROOT" >&2
    exit 1
  }
  [ -f "$V1_COMPARE_DEPLOY_ROOT/.v2-isolated-copy.json" ] || {
    echo "isolated-copy marker is missing: $V1_COMPARE_DEPLOY_ROOT/.v2-isolated-copy.json" >&2
    exit 1
  }
  for relative in \
    state/control-plane.sqlite3 \
    state/usage.sqlite3 \
    state/gateway/auth-snapshot.json \
    state/gateway/quota-snapshot.json \
    state/gateway/quota-heartbeat.json \
    state/edge/active-gateway.conf \
    secrets/control-plane.key; do
    [ -f "$V1_COMPARE_DEPLOY_ROOT/$relative" ] || {
      echo "isolated v1 comparison state is incomplete: $relative" >&2
      exit 1
    }
  done
  docker network inspect "$V1_COMPARE_UPSTREAM_NETWORK" >/dev/null
  require_disposable_upstream
}

compose() {
  docker compose \
    --project-directory "$ROOT_DIR" \
    --env-file "$ENV_FILE" \
    -f "$ROOT_DIR/docker-compose.v1-compare.yml" \
    "$@"
}

component_image() {
  case "$1" in
    admin) printf '%s\n' "$V1_COMPARE_ADMIN_IMAGE" ;;
    web) printf '%s\n' "$V1_COMPARE_WEB_IMAGE" ;;
    gateway) printf '%s\n' "$V1_COMPARE_GATEWAY_IMAGE" ;;
    edge) printf '%s\n' "$V1_COMPARE_EDGE_IMAGE" ;;
    *) return 1 ;;
  esac
}

verify_images() {
  for component in admin web gateway edge; do
    image=$(component_image "$component")
    case "$image" in
      *:sha256-*) expected_digest=${image##*:sha256-} ;;
      *)
        echo "v1 comparison image must use an immutable sha256 component tag: $image" >&2
        exit 1
        ;;
    esac
    case "$expected_digest" in
      *[!0-9a-f]*)
        echo "v1 comparison image has an invalid component digest: $image" >&2
        exit 1
        ;;
    esac
    [ "${#expected_digest}" -eq 64 ] || {
      echo "v1 comparison image has an invalid component digest: $image" >&2
      exit 1
    }
    docker image inspect "$image" >/dev/null
    architecture=$(docker image inspect --format '{{.Architecture}}' "$image")
    [ "$architecture" = amd64 ] || {
      echo "v1 comparison image must be linux/amd64: $image ($architecture)" >&2
      exit 1
    }
    label_component=$(docker image inspect --format '{{index .Config.Labels "io.codex-cpa.component"}}' "$image")
    label_digest=$(docker image inspect --format '{{index .Config.Labels "io.codex-cpa.component-digest"}}' "$image")
    [ "$label_component" = "$component" ] || {
      echo "v1 comparison image component mismatch: $image" >&2
      exit 1
    }
    [ "$label_digest" = "$expected_digest" ] || {
      echo "v1 comparison image digest mismatch: $image" >&2
      exit 1
    }
  done
  read_proxy_image=$V1_COMPARE_DOCKER_READ_PROXY_IMAGE
  case "$read_proxy_image" in
    *:sha256-*) read_proxy_digest=${read_proxy_image##*:sha256-} ;;
    *)
      echo "v1 comparison Docker read proxy must use an immutable sha256 component tag" >&2
      exit 1
      ;;
  esac
  [ "${#read_proxy_digest}" -eq 64 ] || {
    echo "v1 comparison Docker read proxy digest is invalid" >&2
    exit 1
  }
  docker image inspect "$read_proxy_image" >/dev/null
  [ "$(docker image inspect --format '{{.Architecture}}' "$read_proxy_image")" = amd64 ]
  [ "$(docker image inspect --format '{{index .Config.Labels "io.codex-cpa.component"}}' "$read_proxy_image")" = v2-control ]
  [ "$(docker image inspect --format '{{index .Config.Labels "io.codex-cpa.component-digest"}}' "$read_proxy_image")" = "$read_proxy_digest" ]
  echo "v1 main comparison images verified"
}

prepare_runtime_paths() {
  install -d -m 0770 "$V1_COMPARE_DEPLOY_ROOT/logs/gateway"
  chgrp 65534 "$V1_COMPARE_DEPLOY_ROOT/logs/gateway"
  # OpenResty workers drop to nobody before the Lua refresh timer reads JSON.
  # Keep content unchanged while making only the three runtime snapshots
  # readable by that fixed container group.
  chgrp 65534 \
    "$V1_COMPARE_DEPLOY_ROOT/state/gateway/auth-snapshot.json" \
    "$V1_COMPARE_DEPLOY_ROOT/state/gateway/quota-snapshot.json" \
    "$V1_COMPARE_DEPLOY_ROOT/state/gateway/quota-heartbeat.json"
  chmod 0640 \
    "$V1_COMPARE_DEPLOY_ROOT/state/gateway/auth-snapshot.json" \
    "$V1_COMPARE_DEPLOY_ROOT/state/gateway/quota-snapshot.json" \
    "$V1_COMPARE_DEPLOY_ROOT/state/gateway/quota-heartbeat.json"
}

wait_comparison_container() {
  wait_container=$1
  wait_attempt=0
  while [ "$wait_attempt" -lt 120 ]; do
    wait_state=$(docker inspect --format \
      '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' \
      "$wait_container" 2>/dev/null || true)
    case "$wait_state" in
      healthy|running) return 0 ;;
      exited|dead)
        echo "v1 comparison container stopped before readiness: $wait_container" >&2
        return 1
        ;;
    esac
    wait_attempt=$((wait_attempt + 1))
    sleep 1
  done
  echo "v1 comparison container did not become ready: $wait_container" >&2
  return 1
}

comparison_container_has_network() {
  comparison_container=$1
  comparison_network=$2
  docker inspect "$comparison_container" | python3 -c '
import json
import sys

container = json.load(sys.stdin)[0]
actual = container.get("NetworkSettings", {}).get("Networks", {})
raise SystemExit(0 if sys.argv[1] in actual else 1)
' "$comparison_network"
}

require_comparison_container_identity() {
  comparison_container=$1
  comparison_service=$2
  actual_project=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$comparison_container")
  actual_service=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.service"}}' "$comparison_container")
  [ "$actual_project" = "$V1_COMPARE_COMPOSE_PROJECT_NAME" ] \
    && [ "$actual_service" = "$comparison_service" ] || {
    echo "refusing network repair outside exact v1 comparison service: container=$comparison_container project=$actual_project service=$actual_service" >&2
    return 1
  }
}

ensure_comparison_network() {
  comparison_container=$1
  comparison_service=$2
  comparison_network=$3
  require_comparison_container_identity "$comparison_container" "$comparison_service"
  actual_network=$(docker network inspect --format '{{.Name}}' "$comparison_network")
  [ "$actual_network" = "$comparison_network" ] || {
    echo "refusing v1 comparison network repair for unexpected network: expected=$comparison_network actual=$actual_network" >&2
    return 1
  }
  if comparison_container_has_network "$comparison_container" "$comparison_network" 2>/dev/null; then
    return
  fi
  docker network connect \
    --alias "$comparison_service" \
    --alias "$comparison_container" \
    "$comparison_network" \
    "$comparison_container"
  comparison_container_has_network "$comparison_container" "$comparison_network"
  echo "restored v1 comparison network: container=$comparison_container network=$comparison_network"
}

remove_unexpected_comparison_networks() {
  comparison_container=$1
  comparison_service=$2
  shift 2
  require_comparison_container_identity "$comparison_container" "$comparison_service"
  actual_networks=$(docker inspect "$comparison_container" | python3 -c '
import json
import sys

container = json.load(sys.stdin)[0]
for name in sorted(container.get("NetworkSettings", {}).get("Networks", {})):
    print(name)
')
  for actual_network in $actual_networks; do
    expected=false
    for expected_network in "$@"; do
      if [ "$actual_network" = "$expected_network" ]; then
        expected=true
        break
      fi
    done
    if [ "$expected" = false ]; then
      docker network disconnect --force "$actual_network" "$comparison_container"
      echo "removed unexpected v1 comparison network: container=$comparison_container network=$actual_network"
    fi
  done
}

reconcile_comparison_service_networks() {
  comparison_service=$1
  shift
  comparison_container="$V1_COMPARE_INSTANCE_NAME-$comparison_service"
  for comparison_network in "$@"; do
    ensure_comparison_network "$comparison_container" "$comparison_service" "$comparison_network"
  done
  remove_unexpected_comparison_networks "$comparison_container" "$comparison_service" "$@"
  wait_comparison_container "$comparison_container"
  for comparison_network in "$@"; do
    comparison_container_has_network "$comparison_container" "$comparison_network" || {
      echo "v1 comparison service is missing its required network: container=$comparison_container network=$comparison_network" >&2
      exit 1
    }
  done
}

smoke() {
  public_url="http://$V1_COMPARE_PUBLIC_PROBE_HOST:$V1_COMPARE_PUBLIC_PORT"
  internal_url="http://127.0.0.1:$V1_COMPARE_INTERNAL_PORT"
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url/__health")" = 200 ]
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url/v1/models")" = 401 ]
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url/__internal/snapshots")" = 404 ]
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$internal_url/__internal/snapshots")" = 200 ]
  for path in / /admin/ /usage/ /native/; do
    [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url$path")" = 200 ]
  done
  echo "v1 main comparison smoke passed"
}

case "$ACTION" in
  config)
    require_isolated_root
    compose config --quiet
    ;;
  verify-images)
    verify_images
    ;;
  up)
    require_isolated_root
    verify_images
    prepare_runtime_paths
    compose config --quiet
    control_network="${V1_COMPARE_COMPOSE_PROJECT_NAME}_control"
    ingress_network="${V1_COMPARE_COMPOSE_PROJECT_NAME}_ingress"
    # Older Docker/Compose combinations can silently lose a secondary network
    # during a concurrent recreate. Start each isolated service separately,
    # repair only containers whose exact project/service labels match, and
    # remove any stale attachment (especially the production upstream).
    for comparison_service in docker-read-proxy admin web gateway-blue gateway-green edge; do
      compose up -d --no-deps "$comparison_service"
      case "$comparison_service" in
        docker-read-proxy|admin|web)
          reconcile_comparison_service_networks "$comparison_service" "$control_network"
          ;;
        gateway-blue|gateway-green)
          reconcile_comparison_service_networks \
            "$comparison_service" "$control_network" "$V1_COMPARE_UPSTREAM_NETWORK"
          ;;
        edge)
          reconcile_comparison_service_networks "$comparison_service" "$control_network" "$ingress_network"
          ;;
      esac
    done
    smoke
    ;;
  smoke)
    smoke
    ;;
  ps)
    compose ps
    ;;
  down)
    compose down --remove-orphans
    ;;
  *)
    echo "unsupported action: $ACTION" >&2
    exit 1
    ;;
esac
