#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ACTION=${1:-config}
ENV_FILE=${V2_ENV_FILE:-$ROOT_DIR/v2-target.env}
COMPOSE_FILE=${V2_COMPOSE_FILE:-$ROOT_DIR/docker-compose.yml}
MANIFEST_FILE=${V2_RELEASE_MANIFEST:-$ROOT_DIR/release-manifest.json}

case "$ENV_FILE" in /*) ;; *) ENV_FILE="$ROOT_DIR/$ENV_FILE" ;; esac
case "$COMPOSE_FILE" in /*) ;; *) COMPOSE_FILE="$ROOT_DIR/$COMPOSE_FILE" ;; esac
case "$MANIFEST_FILE" in /*) ;; *) MANIFEST_FILE="$ROOT_DIR/$MANIFEST_FILE" ;; esac

[ -f "$ENV_FILE" ] || { echo "Go target env file is missing: $ENV_FILE" >&2; exit 1; }
[ -f "$COMPOSE_FILE" ] || { echo "Go target Compose file is missing: $COMPOSE_FILE" >&2; exit 1; }

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
    echo "Go target requires an existing control database, usage database, and matching master key" >&2
    exit 1
  }

compose() {
  docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
}

image_label() {
  docker image inspect --format "{{index .Config.Labels \"$2\"}}" "$1" 2>/dev/null || true
}

expected_digest() {
  docker run --rm \
    -v "$MANIFEST_FILE:/release-manifest.json:ro" \
    "$V2_CONTROL_IMAGE" \
    /usr/local/bin/cpa-releasectl manifest get \
    --manifest /release-manifest.json \
    --component "$1"
}

validate_image() {
  image=$1
  component=$2
  expected=$3
  actual_component=$(image_label "$image" io.codex-cpa.component)
  actual_digest=$(image_label "$image" io.codex-cpa.component-digest)
  if [ "$actual_component" != "$component" ] || [ "$actual_digest" != "$expected" ]; then
    echo "Go image label mismatch: component=$component image=$image" >&2
    exit 1
  fi
}

pull_images() {
  [ -f "$MANIFEST_FILE" ] || {
    echo "release manifest is required to pull exact Go images: $MANIFEST_FILE" >&2
    exit 1
  }
  docker pull "$V2_CONTROL_IMAGE"
  for pair in \
    "control|$V2_CONTROL_IMAGE" \
    "web|$V2_WEB_IMAGE" \
    "gateway|$V2_GATEWAY_IMAGE" \
    "edge|$V2_EDGE_IMAGE"
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
    echo "release manifest is required to verify exact Go images: $MANIFEST_FILE" >&2
    exit 1
  }
  for pair in \
    "control|$V2_CONTROL_IMAGE" \
    "web|$V2_WEB_IMAGE" \
    "gateway|$V2_GATEWAY_IMAGE" \
    "edge|$V2_EDGE_IMAGE"
  do
    component=${pair%%|*}
    image=${pair#*|}
    digest=$(expected_digest "$component")
    validate_image "$image" "$component" "$digest"
  done
  echo "Go target images verified"
}

ownership_json() {
  docker run --rm \
    -v "$V2_DEPLOY_ROOT:$V2_DEPLOY_ROOT" \
    "$V2_CONTROL_IMAGE" \
    /usr/local/bin/cpa-ownership \
    --root "$V2_DEPLOY_ROOT" \
    status
}

ownership_status_field() {
  docker run --rm \
    -v "$V2_DEPLOY_ROOT:$V2_DEPLOY_ROOT" \
    "$V2_CONTROL_IMAGE" \
    /usr/local/bin/cpa-ownership \
    --root "$V2_DEPLOY_ROOT" \
    status --field "$1"
}

require_active_owner() {
  active=$(ownership_status_field active)
  owner=$(ownership_status_field owner)
  if [ "$active" != true ] || [ "$owner" != "$V2_RUNTIME_OWNER" ]; then
    echo "Go runtime ownership is not active for $V2_RUNTIME_OWNER" >&2
    exit 1
  fi
}

require_container_network() {
  container=$1
  network=$2
  networks=$(docker inspect --format '{{json .NetworkSettings.Networks}}' "$container")
  printf '%s' "$networks" | grep -Fq "\"$network\":" || {
    echo "Go container is missing required network: container=$container network=$network" >&2
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
    echo "refusing network repair outside exact Go service: container=$container project=$actual_project service=$actual_service" >&2
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
  echo "restored Go candidate network: container=$container network=$network"
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
        echo "Go candidate container stopped before readiness: container=$wait_container status=$wait_status" >&2
        return 1
        ;;
    esac
    wait_attempt=$((wait_attempt + 1))
    sleep 1
  done

  echo "Go candidate container did not become ready: container=$wait_container status=$wait_status" >&2
  return 1
}

require_container_port() {
  container=$1
  container_port=$2
  host_ip=$3
  host_port=$4
  bindings=$(docker inspect --format "{{range (index .NetworkSettings.Ports \"$container_port\")}}{{println .HostIp .HostPort}}{{end}}" "$container")
  printf '%s\n' "$bindings" | grep -Fxq "$host_ip $host_port" || {
    echo "Go container is missing required port binding: container=$container binding=$host_ip:$host_port->$container_port" >&2
    return 1
  }
}

require_core_topology() {
  instance=${V2_INSTANCE_NAME:-cliproxy-v2}
  project=${V2_COMPOSE_PROJECT_NAME:-cliproxy-v2}
  control_network="${project}_control"
  ingress_network="${project}_ingress"

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
  found=$(ownership_status_field found)
  active=$(ownership_status_field active)
  owner=$(ownership_status_field owner)
  generation=$(ownership_status_field generation)
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
  echo "Go target smoke passed"
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
    control_network="${project}_control"
    ingress_network="${project}_ingress"
    # Start one service at a time. Some older Docker/Compose combinations can
    # lose a secondary network or port proxy during a concurrent recreate
    # while still reporting the container itself as healthy. Do not ask
    # Compose to wait before repairing the exact candidate topology: an Edge
    # without the control network can only return 502 and will never become healthy.
    for service in docker-read-proxy gateway-blue gateway-green admin web edge; do
      compose up -d --no-deps "$service"
      container="$instance-$service"
      case "$service" in
        docker-read-proxy|web)
          ensure_candidate_network "$container" "$project" "$service" "$control_network"
          ;;
        gateway-blue|gateway-green|admin)
          ensure_candidate_network "$container" "$project" "$service" "$control_network"
          ensure_candidate_network "$container" "$project" "$service" "$V2_UPSTREAM_NETWORK"
          ;;
        edge)
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
      usage-collector quota account-failover log-maintenance
    ;;
  up-notifications)
    require_active_owner
    compose --profile external-effects up -d --wait notifications
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
