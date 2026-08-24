#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ACTION=${1:-config}
ENV_FILE=${V2_ENV_FILE:-$ROOT_DIR/v2-target.env}
COMPOSE_FILE=${V2_COMPOSE_FILE:-$ROOT_DIR/docker-compose.v2.yml}
MANIFEST_FILE=${V2_RELEASE_MANIFEST:-$ROOT_DIR/release-manifest.json}

case "$ENV_FILE" in /*) ;; *) ENV_FILE="$ROOT_DIR/$ENV_FILE" ;; esac
case "$COMPOSE_FILE" in /*) ;; *) COMPOSE_FILE="$ROOT_DIR/$COMPOSE_FILE" ;; esac
case "$MANIFEST_FILE" in /*) ;; *) MANIFEST_FILE="$ROOT_DIR/$MANIFEST_FILE" ;; esac

[ -f "$ENV_FILE" ] || { echo "Go v2 target env file is missing: $ENV_FILE" >&2; exit 1; }
[ -f "$COMPOSE_FILE" ] || { echo "Go v2 target Compose file is missing: $COMPOSE_FILE" >&2; exit 1; }

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

: "${V2_CONTROL_IMAGE:?V2_CONTROL_IMAGE is required}"
: "${V2_WEB_IMAGE:?V2_WEB_IMAGE is required}"
: "${V2_GATEWAY_IMAGE:?V2_GATEWAY_IMAGE is required}"
: "${V2_EDGE_IMAGE:?V2_EDGE_IMAGE is required}"
: "${V2_DEPLOY_ROOT:?V2_DEPLOY_ROOT is required}"
: "${V2_PUBLIC_BIND_ADDRESS:=127.0.0.1}"
: "${V2_PUBLIC_PROBE_HOST:=$V2_PUBLIC_BIND_ADDRESS}"
: "${V2_PUBLIC_PORT:?V2_PUBLIC_PORT is required}"
: "${V2_INTERNAL_PORT:?V2_INTERNAL_PORT is required}"
: "${V2_UPSTREAM_NETWORK:?V2_UPSTREAM_NETWORK is required}"
: "${V2_RUNTIME_OWNER:=go-v2}"
: "${V2_OWNERSHIP_ACTIVATION_TTL:=2m}"
: "${V2_CONFIRM_DEPLOY_ROOT:?V2_CONFIRM_DEPLOY_ROOT must exactly repeat V2_DEPLOY_ROOT}"

[ "$V2_CONFIRM_DEPLOY_ROOT" = "$V2_DEPLOY_ROOT" ] || {
  echo "V2_CONFIRM_DEPLOY_ROOT does not match V2_DEPLOY_ROOT" >&2
  exit 1
}
[ -f "$V2_DEPLOY_ROOT/state/control-plane.sqlite3" ] \
  && [ -f "$V2_DEPLOY_ROOT/state/usage.sqlite3" ] \
  && [ -f "$V2_DEPLOY_ROOT/secrets/control-plane.key" ] || {
    echo "Go v2 target requires an existing control database, usage database, and matching master key" >&2
    exit 1
  }

compose() {
  docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
}

image_label() {
  docker image inspect --format "{{index .Config.Labels \"$2\"}}" "$1" 2>/dev/null || true
}

expected_digest() {
  python3 - "$MANIFEST_FILE" "$1" <<'PY'
import json, sys
with open(sys.argv[1], "r", encoding="utf-8") as handle:
    manifest = json.load(handle)
component = manifest.get("components", {}).get(sys.argv[2], {})
value = str(component.get("source_sha256", ""))
if len(value) != 64 or any(character not in "0123456789abcdef" for character in value):
    raise SystemExit("release manifest component digest is invalid: " + sys.argv[2])
print(value)
PY
}

validate_image() {
  image=$1
  component=$2
  expected=$3
  actual_component=$(image_label "$image" io.codex-cpa.component)
  actual_digest=$(image_label "$image" io.codex-cpa.component-digest)
  if [ "$actual_component" != "$component" ] || [ "$actual_digest" != "$expected" ]; then
    echo "Go v2 image label mismatch: component=$component image=$image" >&2
    exit 1
  fi
}

pull_images() {
  [ -f "$MANIFEST_FILE" ] || {
    echo "release manifest is required to pull exact Go v2 images: $MANIFEST_FILE" >&2
    exit 1
  }
  for pair in \
    "v2-control|$V2_CONTROL_IMAGE" \
    "v2-web|$V2_WEB_IMAGE" \
    "v2-gateway|$V2_GATEWAY_IMAGE" \
    "v2-edge|$V2_EDGE_IMAGE"
  do
    component=${pair%%|*}
    image=${pair#*|}
    digest=$(expected_digest "$component")
    docker pull "$image"
    validate_image "$image" "$component" "$digest"
  done
}

verify_images() {
  [ -f "$MANIFEST_FILE" ] || {
    echo "release manifest is required to verify exact Go v2 images: $MANIFEST_FILE" >&2
    exit 1
  }
  for pair in \
    "v2-control|$V2_CONTROL_IMAGE" \
    "v2-web|$V2_WEB_IMAGE" \
    "v2-gateway|$V2_GATEWAY_IMAGE" \
    "v2-edge|$V2_EDGE_IMAGE"
  do
    component=${pair%%|*}
    image=${pair#*|}
    digest=$(expected_digest "$component")
    validate_image "$image" "$component" "$digest"
  done
  echo "Go v2 target images verified"
}

ownership_json() {
  docker run --rm \
    -v "$V2_DEPLOY_ROOT:$V2_DEPLOY_ROOT" \
    "$V2_CONTROL_IMAGE" \
    /usr/local/bin/cpa-ownership \
    --root "$V2_DEPLOY_ROOT" \
    status
}

ownership_field() {
  python3 -c 'import json,sys; value=json.load(sys.stdin).get(sys.argv[1]); print("" if value is None else str(value).lower() if isinstance(value, bool) else value)' "$1"
}

require_active_owner() {
  status=$(ownership_json)
  active=$(printf '%s' "$status" | ownership_field active)
  owner=$(printf '%s' "$status" | ownership_field owner)
  if [ "$active" != true ] || [ "$owner" != "$V2_RUNTIME_OWNER" ]; then
    echo "Go v2 runtime ownership is not active for $V2_RUNTIME_OWNER" >&2
    exit 1
  fi
}

require_container_network() {
  container=$1
  network=$2
  networks=$(docker inspect --format '{{json .NetworkSettings.Networks}}' "$container")
  printf '%s' "$networks" | grep -Fq "\"$network\":" || {
    echo "Go v2 container is missing required network: container=$container network=$network" >&2
    return 1
  }
}

ensure_candidate_network() {
  container=$1
  project=$2
  service=$3
  network=$4

  actual_project=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$container")
  actual_service=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.service"}}' "$container")
  [ "$actual_project" = "$project" ] && [ "$actual_service" = "$service" ] || {
    echo "refusing network repair outside exact Go v2 service: container=$container project=$actual_project service=$actual_service" >&2
    return 1
  }
  actual_network=$(docker network inspect --format '{{.Name}}' "$network")
  [ "$actual_network" = "$network" ] || {
    echo "refusing network repair for unexpected network: expected=$network actual=$actual_network" >&2
    return 1
  }
  if require_container_network "$container" "$network" 2>/dev/null; then
    return
  fi

  docker network connect \
    --alias "$service" \
    --alias "$container" \
    "$network" \
    "$container"
  require_container_network "$container" "$network"
  echo "restored Go v2 candidate network: container=$container network=$network"
}

wait_candidate_container() {
  wait_container=$1
  wait_seconds=${V2_SERVICE_WAIT_SECONDS:-120}
  case "$wait_seconds" in
    ""|0|*[!0-9]*)
      echo "V2_SERVICE_WAIT_SECONDS must be a positive integer" >&2
      return 1
      ;;
  esac

  wait_attempt=0
  wait_status=missing
  while [ "$wait_attempt" -lt "$wait_seconds" ]; do
    wait_status=$(docker inspect --format \
      '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' \
      "$wait_container" 2>/dev/null || true)
    case "$wait_status" in
      healthy|running)
        return 0
        ;;
      exited|dead)
        echo "Go v2 candidate container stopped before readiness: container=$wait_container status=$wait_status" >&2
        return 1
        ;;
    esac
    wait_attempt=$((wait_attempt + 1))
    sleep 1
  done

  echo "Go v2 candidate container did not become ready: container=$wait_container status=$wait_status" >&2
  return 1
}

require_container_port() {
  container=$1
  container_port=$2
  host_ip=$3
  host_port=$4
  bindings=$(docker inspect --format '{{json .NetworkSettings.Ports}}' "$container")
  python3 -c '
import json, sys

bindings = json.loads(sys.argv[1] or "{}")
expected = {"HostIp": sys.argv[3], "HostPort": sys.argv[4]}
raise SystemExit(0 if expected in bindings.get(sys.argv[2], []) else 1)
' "$bindings" "$container_port" "$host_ip" "$host_port" || {
    echo "Go v2 container is missing required port binding: container=$container binding=$host_ip:$host_port->$container_port" >&2
    return 1
  }
}

require_core_topology() {
  instance=${V2_INSTANCE_NAME:-cliproxy-v2}
  project=${V2_COMPOSE_PROJECT_NAME:-cliproxy-v2}
  control_network="${project}_v2-control"
  ingress_network="${project}_v2-ingress"

  require_container_network "$instance-docker-read-proxy" "$control_network"
  for container in "$instance-gateway-blue" "$instance-gateway-green" "$instance-admin"; do
    require_container_network "$container" "$control_network"
    require_container_network "$container" "$V2_UPSTREAM_NETWORK"
  done
  require_container_network "$instance-web" "$control_network"
  require_container_network "$instance-edge" "$control_network"
  require_container_network "$instance-edge" "$ingress_network"
  require_container_port "$instance-edge" "8317/tcp" "$V2_PUBLIC_BIND_ADDRESS" "$V2_PUBLIC_PORT"
  require_container_port "$instance-edge" "8319/tcp" "127.0.0.1" "$V2_INTERNAL_PORT"
}

activate_owner() {
  status=$(ownership_json)
  found=$(printf '%s' "$status" | ownership_field found)
  active=$(printf '%s' "$status" | ownership_field active)
  owner=$(printf '%s' "$status" | ownership_field owner)
  generation=$(printf '%s' "$status" | ownership_field generation)
  if [ "$active" = true ]; then
    if [ "$owner" = "$V2_RUNTIME_OWNER" ]; then
      printf '%s\n' "$status"
      return
    fi
    echo "runtime ownership is still active for another writer: $owner generation=$generation" >&2
    exit 1
  fi
  set -- docker run --rm \
    -v "$V2_DEPLOY_ROOT:$V2_DEPLOY_ROOT" \
    "$V2_CONTROL_IMAGE" \
    /usr/local/bin/cpa-ownership \
    --root "$V2_DEPLOY_ROOT" \
    --ttl "$V2_OWNERSHIP_ACTIVATION_TTL" \
    activate \
    --owner "$V2_RUNTIME_OWNER" \
    --confirm-owner "$V2_RUNTIME_OWNER"
  if [ "$found" = true ]; then
    set -- "$@" --expected-owner "$owner" --expected-generation "$generation"
  else
    case "${V2_BOOTSTRAP_MODE:-}" in
      isolated-test)
        set -- "$@" --allow-empty-bootstrap
        ;;
      legacy-cutover)
        [ "${V2_CONFIRM_LEGACY_WRITERS_STOPPED:-}" = "$V2_DEPLOY_ROOT" ] || {
          echo "legacy bootstrap requires V2_CONFIRM_LEGACY_WRITERS_STOPPED=$V2_DEPLOY_ROOT" >&2
          exit 1
        }
        set -- "$@" --confirm-legacy-bootstrap "legacy-writers-stopped:$V2_DEPLOY_ROOT"
        ;;
      *)
        echo "empty ownership history requires V2_BOOTSTRAP_MODE=isolated-test or legacy-cutover" >&2
        exit 1
        ;;
    esac
  fi
  "$@"
}

smoke() {
  public_url="http://$V2_PUBLIC_PROBE_HOST:$V2_PUBLIC_PORT"
  internal_url="http://127.0.0.1:$V2_INTERNAL_PORT"
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url/__health")" = 200 ]
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url/v1/models")" = 401 ]
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url/__internal/snapshots")" = 404 ]
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$internal_url/__internal/snapshots")" = 200 ]
  # The HTML routes are served by Web without touching Admin. The public site
  # configuration proves the Edge -> Web -> Admin path as well, so a missing
  # candidate control network cannot pass smoke with static pages alone.
  for path in / /admin/ /usage/ /native/ /site-config.json; do
    [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url$path")" = 200 ]
  done
  echo "Go v2 target smoke passed"
}

case "$ACTION" in
  pull)
    pull_images
    ;;
  verify-images)
    verify_images
    ;;
  config)
    compose --profile writers --profile external-effects config --quiet
    ;;
  ownership-status)
    ownership_json
    ;;
  activate)
    activate_owner
    ;;
  up-core)
    require_active_owner
    instance=${V2_INSTANCE_NAME:-cliproxy-v2}
    project=${V2_COMPOSE_PROJECT_NAME:-cliproxy-v2}
    control_network="${project}_v2-control"
    ingress_network="${project}_v2-ingress"
    # Start one service at a time. Some older Docker/Compose combinations can
    # lose a secondary network or port proxy during a concurrent recreate
    # while still reporting the container itself as healthy. Do not ask
    # Compose to wait before repairing the exact candidate topology: an Edge
    # without v2-control can only return 502 and will never become healthy.
    for service in v2-docker-read-proxy v2-gateway-blue v2-gateway-green v2-admin v2-web v2-edge; do
      compose up -d --no-deps "$service"
      container="$instance-${service#v2-}"
      case "$service" in
        v2-docker-read-proxy|v2-web)
          ensure_candidate_network "$container" "$project" "$service" "$control_network"
          ;;
        v2-gateway-blue|v2-gateway-green|v2-admin)
          ensure_candidate_network "$container" "$project" "$service" "$control_network"
          ensure_candidate_network "$container" "$project" "$service" "$V2_UPSTREAM_NETWORK"
          ;;
        v2-edge)
          ensure_candidate_network "$container" "$project" "$service" "$control_network"
          ensure_candidate_network "$container" "$project" "$service" "$ingress_network"
          ;;
      esac
      wait_candidate_container "$container"
    done
    require_core_topology
    ;;
  up-writers)
    require_active_owner
    compose --profile writers up -d --wait \
      v2-usage-collector v2-quota v2-account-failover v2-log-maintenance
    ;;
  up-notifications)
    require_active_owner
    compose --profile external-effects up -d --wait v2-notifications
    ;;
  smoke)
    smoke
    ;;
  ps)
    compose --profile writers --profile external-effects ps
    ;;
  down)
    compose --profile writers --profile external-effects down --remove-orphans
    ;;
  *)
    echo "unsupported action: $ACTION" >&2
    exit 1
    ;;
esac
