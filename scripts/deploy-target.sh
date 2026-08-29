#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ACTION=${1:-config}
ENV_FILE=${CPA_ENV_FILE:-$ROOT_DIR/target.env}
RELEASE_COMPOSE_FILE="$ROOT_DIR/docker-compose.yml"

case "$ENV_FILE" in /*) ;; *) ENV_FILE="$ROOT_DIR/$ENV_FILE" ;; esac

[ -f "$ENV_FILE" ] && [ ! -L "$ENV_FILE" ] || {
  echo "Go target env file must be a regular non-symlink file: $ENV_FILE" >&2
  exit 1
}
[ -f "$RELEASE_COMPOSE_FILE" ] && [ ! -L "$RELEASE_COMPOSE_FILE" ] || {
  echo "Go release Compose file must be a regular non-symlink file: $RELEASE_COMPOSE_FILE" >&2
  exit 1
}

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

: "${CPA_CONTROL_IMAGE:?CPA_CONTROL_IMAGE is required}"
: "${CPA_WEB_IMAGE:?CPA_WEB_IMAGE is required}"
: "${CPA_GATEWAY_IMAGE:?CPA_GATEWAY_IMAGE is required}"
: "${CPA_EDGE_IMAGE:?CPA_EDGE_IMAGE is required}"
: "${CPA_DEPLOY_ROOT:?CPA_DEPLOY_ROOT is required}"
: "${CPA_PUBLIC_BIND_ADDRESS:=127.0.0.1}"
: "${CPA_PUBLIC_PROBE_HOST:=$CPA_PUBLIC_BIND_ADDRESS}"
: "${CPA_PUBLIC_PORT:?CPA_PUBLIC_PORT is required}"
: "${CPA_INTERNAL_PORT:?CPA_INTERNAL_PORT is required}"
: "${CPA_UPSTREAM_NETWORK:?CPA_UPSTREAM_NETWORK is required}"
: "${CPA_ACCOUNT_COMPOSE_PROJECT:?CPA_ACCOUNT_COMPOSE_PROJECT is required}"
: "${CPA_ACCOUNT_INSTANCE_NAME:?CPA_ACCOUNT_INSTANCE_NAME is required}"
: "${CPA_RUNTIME_OWNER:=codex-cpa}"
: "${CPA_OWNERSHIP_ACTIVATION_TTL:=2m}"
: "${CPA_ALLOW_EDGE_RECREATE:=false}"
: "${CPA_GATEWAY_DRAIN_TIMEOUT_SECONDS:=3600}"
: "${CPA_CONFIRM_DEPLOY_ROOT:?CPA_CONFIRM_DEPLOY_ROOT must exactly repeat CPA_DEPLOY_ROOT}"

case "$CPA_DEPLOY_ROOT" in
  /*) ;;
  *) echo "CPA_DEPLOY_ROOT must be an absolute path" >&2; exit 1 ;;
esac
[ "$CPA_DEPLOY_ROOT" != / ] || {
  echo "CPA_DEPLOY_ROOT must not be the filesystem root" >&2
  exit 1
}
[ -d "$CPA_DEPLOY_ROOT" ] || {
  echo "CPA_DEPLOY_ROOT does not exist: $CPA_DEPLOY_ROOT" >&2
  exit 1
}
CANONICAL_DEPLOY_ROOT=$(CDPATH= cd -- "$CPA_DEPLOY_ROOT" && pwd -P)
[ "$CANONICAL_DEPLOY_ROOT" = "$CPA_DEPLOY_ROOT" ] || {
  echo "CPA_DEPLOY_ROOT must be canonical and must not traverse a symlink: $CPA_DEPLOY_ROOT" >&2
  exit 1
}
[ "$CPA_CONFIRM_DEPLOY_ROOT" = "$CPA_DEPLOY_ROOT" ] || {
  echo "CPA_CONFIRM_DEPLOY_ROOT does not match CPA_DEPLOY_ROOT" >&2
  exit 1
}

require_real_directory() {
  directory=$1
  description=$2
  [ -d "$directory" ] && [ ! -L "$directory" ] || {
    echo "$description must be an existing non-symlink directory: $directory" >&2
    exit 1
  }
  canonical_directory=$(CDPATH= cd -- "$directory" && pwd -P)
  [ "$canonical_directory" = "$directory" ] || {
    echo "$description must be canonical and must not traverse a symlink: $directory" >&2
    exit 1
  }
}

require_regular_file() {
  required_path=$1
  description=$2
  [ -f "$required_path" ] && [ ! -L "$required_path" ] || {
    echo "$description must be an existing regular non-symlink file: $required_path" >&2
    exit 1
  }
}

read_active_slot_file() {
  slot_file=$1
  slot_content=$(cat "$slot_file") || return 1
  case "$slot_content" in
    'set $active_gateway_backend gateway-blue:8317;') printf '%s\n' blue ;;
    'set $active_gateway_backend gateway-green:8317;') printf '%s\n' green ;;
    *) return 1 ;;
  esac
}

TARGET_COMPOSE_FILE="$CPA_DEPLOY_ROOT/docker-compose.yml"
MANIFEST_FILE="$CPA_DEPLOY_ROOT/release-manifest.json"
require_regular_file "$TARGET_COMPOSE_FILE" "Go target Compose file"
require_regular_file "$MANIFEST_FILE" "Go release manifest"
if ! cmp -s "$RELEASE_COMPOSE_FILE" "$TARGET_COMPOSE_FILE"; then
  echo "Go target Compose file does not match the selected release: $TARGET_COMPOSE_FILE" >&2
  exit 1
fi
COMPOSE_FILE=$TARGET_COMPOSE_FILE

for required_directory in \
  "$CPA_DEPLOY_ROOT/state" \
  "$CPA_DEPLOY_ROOT/state/gateway" \
  "$CPA_DEPLOY_ROOT/state/edge" \
  "$CPA_DEPLOY_ROOT/secrets" \
  "$CPA_DEPLOY_ROOT/logs" \
  "$CPA_DEPLOY_ROOT/logs/gateway"
do
  require_real_directory "$required_directory" "Go target runtime directory"
done
for required_file in \
  "$CPA_DEPLOY_ROOT/state/control-plane.sqlite3" \
  "$CPA_DEPLOY_ROOT/state/usage.sqlite3" \
  "$CPA_DEPLOY_ROOT/secrets/control-plane.key" \
  "$CPA_DEPLOY_ROOT/state/edge/active-gateway.conf"
do
  require_regular_file "$required_file" "Go target runtime file"
done
if ! read_active_slot_file "$CPA_DEPLOY_ROOT/state/edge/active-gateway.conf" >/dev/null; then
  echo "Go target active Gateway selection must contain exactly blue or green" >&2
  exit 1
fi

validate_port() {
  port_name=$1
  port_value=$2
  case "$port_value" in
    ""|0|*[!0-9]*)
      echo "$port_name must be an integer between 1 and 65535" >&2
      exit 1
      ;;
  esac
  [ "$port_value" -le 65535 ] || {
    echo "$port_name must be an integer between 1 and 65535" >&2
    exit 1
  }
}

validate_content_image() {
  image_name=$1
  image_value=$2
  digest=${image_value##*:sha256-}
  prefix=${image_value%:sha256-*}
  if [ "$digest" = "$image_value" ] || [ "$prefix" = "$image_value" ] \
    || [ -z "$prefix" ] || [ "${#digest}" -ne 64 ]; then
    echo "$image_name must use the immutable :sha256-<64 lowercase hex> tag from release metadata" >&2
    exit 1
  fi
  case "$digest" in
    *[!0-9a-f]*)
      echo "$image_name must use the immutable :sha256-<64 lowercase hex> tag from release metadata" >&2
      exit 1
      ;;
  esac
}

validate_port CPA_PUBLIC_PORT "$CPA_PUBLIC_PORT"
validate_port CPA_INTERNAL_PORT "$CPA_INTERNAL_PORT"
[ "$CPA_PUBLIC_PORT" != "$CPA_INTERNAL_PORT" ] || {
  echo "CPA_PUBLIC_PORT and CPA_INTERNAL_PORT must be different" >&2
  exit 1
}
validate_content_image CPA_CONTROL_IMAGE "$CPA_CONTROL_IMAGE"
validate_content_image CPA_WEB_IMAGE "$CPA_WEB_IMAGE"
validate_content_image CPA_GATEWAY_IMAGE "$CPA_GATEWAY_IMAGE"
validate_content_image CPA_EDGE_IMAGE "$CPA_EDGE_IMAGE"
case "$CPA_ALLOW_EDGE_RECREATE" in
  true|false) ;;
  *) echo "CPA_ALLOW_EDGE_RECREATE must be true or false" >&2; exit 1 ;;
esac
case "$CPA_GATEWAY_DRAIN_TIMEOUT_SECONDS" in
  ""|0|*[!0-9]*)
    echo "CPA_GATEWAY_DRAIN_TIMEOUT_SECONDS must be a positive integer" >&2
    exit 1
    ;;
esac

compose() {
  docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
}

image_label() {
  docker image inspect --format "{{index .Config.Labels \"$2\"}}" "$1" 2>/dev/null || true
}

expected_digest() {
  docker run --rm \
    -v "$MANIFEST_FILE:/release-manifest.json:ro" \
    "$CPA_CONTROL_IMAGE" \
    /usr/local/bin/cpa-releasectl manifest get \
    --manifest /release-manifest.json \
    --component "$1"
}

image_tag_digest() {
  printf '%s\n' "${1##*:sha256-}"
}

validate_image() {
  image=$1
  component=$2
  expected=$3
  actual_component=$(image_label "$image" io.codex-cpa.component)
  actual_digest=$(image_label "$image" io.codex-cpa.component-digest)
  actual_source_digest=$(image_label "$image" io.codex-cpa.source-digest)
  if [ "$actual_component" != "$component" ] || [ "$actual_digest" != "$expected" ] \
    || [ "$actual_source_digest" != "$expected" ]; then
    echo "Go image label mismatch: component=$component image=$image" >&2
    exit 1
  fi
}

validate_tagged_image() {
  tagged_image=$1
  tagged_component=$2
  validate_image "$tagged_image" "$tagged_component" "$(image_tag_digest "$tagged_image")"
}

validate_release_manifest_images() {
  for pair in \
    "control|$CPA_CONTROL_IMAGE" \
    "web|$CPA_WEB_IMAGE" \
    "gateway|$CPA_GATEWAY_IMAGE" \
    "edge|$CPA_EDGE_IMAGE"
  do
    component=${pair%%|*}
    image=${pair#*|}
    manifest_digest=$(expected_digest "$component")
    tag_digest=$(image_tag_digest "$image")
    [ "$manifest_digest" = "$tag_digest" ] || {
      echo "Go release manifest does not match the selected image tag: component=$component image=$image" >&2
      exit 1
    }
    validate_image "$image" "$component" "$manifest_digest"
  done
}

pull_images() {
  require_regular_file "$MANIFEST_FILE" "Go release manifest"
  for pair in \
    "control|$CPA_CONTROL_IMAGE" \
    "web|$CPA_WEB_IMAGE" \
    "gateway|$CPA_GATEWAY_IMAGE" \
    "edge|$CPA_EDGE_IMAGE"
  do
    component=${pair%%|*}
    image=${pair#*|}
    docker pull "$image"
    validate_tagged_image "$image" "$component"
  done
  # Only execute releasectl from Control after its non-executing image labels
  # have matched the immutable source-digest tag.
  validate_release_manifest_images
}

verify_images() {
  require_regular_file "$MANIFEST_FILE" "Go release manifest"
  for pair in \
    "control|$CPA_CONTROL_IMAGE" \
    "web|$CPA_WEB_IMAGE" \
    "gateway|$CPA_GATEWAY_IMAGE" \
    "edge|$CPA_EDGE_IMAGE"
  do
    component=${pair%%|*}
    image=${pair#*|}
    validate_tagged_image "$image" "$component"
  done
  validate_release_manifest_images
  require_edge_recreate_policy
  echo "Go target images verified"
}

ownership_json() {
  docker run --rm \
    -v "$CPA_DEPLOY_ROOT:$CPA_DEPLOY_ROOT" \
    "$CPA_CONTROL_IMAGE" \
    /usr/local/bin/cpa-ownership \
    --root "$CPA_DEPLOY_ROOT" \
    status
}

ownership_status_field() {
  docker run --rm \
    -v "$CPA_DEPLOY_ROOT:$CPA_DEPLOY_ROOT" \
    "$CPA_CONTROL_IMAGE" \
    /usr/local/bin/cpa-ownership \
    --root "$CPA_DEPLOY_ROOT" \
    status --field "$1"
}

require_active_owner() {
  active=$(ownership_status_field active)
  owner=$(ownership_status_field owner)
  if [ "$active" != true ] || [ "$owner" != "$CPA_RUNTIME_OWNER" ]; then
    echo "Go runtime ownership is not active for $CPA_RUNTIME_OWNER" >&2
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

ensure_target_network() {
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
  echo "restored Go target network: container=$container network=$network"
}

wait_target_container() {
  wait_container=$1
  wait_seconds=${CPA_SERVICE_WAIT_SECONDS:-120}
  case "$wait_seconds" in
    ""|0|*[!0-9]*)
      echo "CPA_SERVICE_WAIT_SECONDS must be a positive integer" >&2
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
        echo "Go target container stopped before readiness: container=$wait_container status=$wait_status" >&2
        return 1
        ;;
    esac
    wait_attempt=$((wait_attempt + 1))
    sleep 1
  done

  echo "Go target container did not become ready: container=$wait_container status=$wait_status" >&2
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

container_exists() {
  docker inspect "$1" >/dev/null 2>&1
}

container_running() {
  [ "$(docker inspect --format '{{.State.Running}}' "$1" 2>/dev/null || true)" = true ]
}

container_image_id() {
  docker inspect --format '{{.Image}}' "$1"
}

target_image_id() {
  docker image inspect --format '{{.Id}}' "$1"
}

container_config_hash() {
  hash=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.config-hash"}}' "$1") || {
    echo "unable to read running Compose hash: container=$1" >&2
    return 1
  }
  validate_sha256_value "$hash" "running Compose hash for $1" || return 1
  printf '%s\n' "$hash"
}

service_config_hash() {
  service=$1
  output=$(compose config --hash "$service") || {
    echo "unable to calculate target Compose hash: service=$service" >&2
    return 1
  }
  hash=$(printf '%s\n' "$output" | awk -v service="$service" '$1 == service { if (found) exit 2; found=1; hash=$2 } END { if (!found) exit 1; print hash }') || {
    echo "target Compose hash output is missing or ambiguous: service=$service" >&2
    return 1
  }
  validate_sha256_value "$hash" "target Compose hash for $service" || return 1
  printf '%s\n' "$hash"
}

validate_sha256_value() {
  digest_value=$1
  digest_description=$2
  if [ "${#digest_value}" -ne 64 ]; then
    echo "$digest_description must be a 64-character lowercase hexadecimal digest" >&2
    return 1
  fi
  case "$digest_value" in
    *[!0-9a-f]*)
      echo "$digest_description must be a 64-character lowercase hexadecimal digest" >&2
      return 1
      ;;
  esac
}

service_recreate_state() {
  container=$1
  service=$2
  image=$3
  running_image=$(container_image_id "$container") || {
    echo "unable to read running image identity: container=$container" >&2
    return 1
  }
  desired_image=$(target_image_id "$image") || {
    echo "unable to read target image identity: image=$image" >&2
    return 1
  }
  [ -n "$running_image" ] && [ -n "$desired_image" ] || {
    echo "running and target image identities must not be empty: container=$container image=$image" >&2
    return 1
  }
  if [ "$running_image" != "$desired_image" ]; then
    printf '%s\n' true
    return
  fi
  running_hash=$(container_config_hash "$container") || return 1
  desired_hash=$(service_config_hash "$service") || return 1
  if [ "$running_hash" != "$desired_hash" ]; then
    printf '%s\n' true
  else
    printf '%s\n' false
  fi
}

require_service_current() {
  current_container=$1
  current_service=$2
  current_image=$3
  recreate_state=$(service_recreate_state "$current_container" "$current_service" "$current_image") || return 1
  [ "$recreate_state" = false ] || {
    echo "Go service did not converge to its exact target image and Compose configuration: service=$current_service container=$current_container" >&2
    return 1
  }
}

require_exact_compose_service() {
  container=$1
  project=$2
  service=$3
  actual_project=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$container")
  actual_service=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.service"}}' "$container")
  [ "$actual_project" = "$project" ] && [ "$actual_service" = "$service" ] || {
    echo "refusing Go rollout for unexpected container identity: container=$container project=$actual_project service=$actual_service" >&2
    return 1
  }
}

gateway_inflight() {
  gateway_container=$1
  stats=$(docker exec "$gateway_container" wget -qO- http://127.0.0.1:8319/__stats) || {
    echo "unable to read Go Gateway in-flight statistics: container=$gateway_container" >&2
    return 1
  }
  printf '%s' "$stats" | awk '
    { payload = payload $0 }
    END {
      gsub(/[[:space:]]/, "", payload)
      if (payload == "[]") {
        print 0
        exit
      }
      if (payload !~ /^\[\{.*\}\]$/) {
        exit 2
      }
      object_count = gsub(/\{/, "{", payload)
      remaining = payload
      field_count = 0
      total = 0
      while (match(remaining, /"inflight":[0-9]+/)) {
        field = substr(remaining, RSTART, RLENGTH)
        sub(/^"inflight":/, "", field)
        total += field + 0
        field_count++
        remaining = substr(remaining, RSTART + RLENGTH)
      }
      if (field_count == 0 || field_count != object_count) {
        exit 2
      }
      print total
    }
  ' || {
    echo "Go Gateway returned invalid in-flight statistics: container=$gateway_container" >&2
    return 1
  }
}

wait_gateway_drain() {
  gateway_container=$1
  elapsed=0
  while [ "$elapsed" -lt "$CPA_GATEWAY_DRAIN_TIMEOUT_SECONDS" ]; do
    inflight=$(gateway_inflight "$gateway_container") || return 1
    case "$inflight" in
      ""|*[!0-9]*)
        echo "Go Gateway returned a non-numeric in-flight count: container=$gateway_container" >&2
        return 1
        ;;
    esac
    if [ "$inflight" -eq 0 ]; then
      echo "Go Gateway drained: container=$gateway_container"
      return
    fi
    elapsed=$((elapsed + 1))
    sleep 1
  done
  echo "Go Gateway drain timed out without terminating existing requests: container=$gateway_container inflight=$inflight" >&2
  return 1
}

wait_edge_slot() {
  expected_slot=$1
  edge_slot_port=${ROLLOUT_EDGE_INTERNAL_PORT:-$CPA_INTERNAL_PORT}
  elapsed=0
  while [ "$elapsed" -lt 30 ]; do
    actual_slot=$(curl --noproxy '*' -fsS "http://127.0.0.1:$edge_slot_port/__internal/edge/slot" 2>/dev/null || true)
    if [ "$actual_slot" = "$expected_slot" ]; then
      return
    fi
    elapsed=$((elapsed + 1))
    sleep 1
  done
  echo "Go Edge did not activate Gateway slot: expected=$expected_slot actual=${actual_slot:-unavailable}" >&2
  return 1
}

container_bound_port() {
  container=$1
  container_port=$2
  bindings=$(docker inspect --format "{{range (index .NetworkSettings.Ports \"$container_port\")}}{{println .HostIp .HostPort}}{{end}}" "$container")
  printf '%s\n' "$bindings" | awk '$1 == "127.0.0.1" { print $2; exit }'
}

write_gateway_slot_file() {
  slot=$1
  selection_dir="$CPA_DEPLOY_ROOT/state/edge"
  selection_file="$selection_dir/active-gateway.conf"
  [ -d "$selection_dir" ] && [ ! -L "$selection_dir" ] \
    && [ -f "$selection_file" ] && [ ! -L "$selection_file" ] || {
      echo "active Gateway selection must be an existing regular file in a non-symlink directory" >&2
      return 1
    }
  temporary_selection=$(mktemp "$selection_dir/.active-gateway.XXXXXX")
  case "$slot" in
    blue|green) ;;
    *) rm -f -- "$temporary_selection"; echo "invalid Gateway slot: $slot" >&2; return 1 ;;
  esac
  if ! printf 'set $active_gateway_backend gateway-%s:8317;\n' "$slot" >"$temporary_selection" \
    || ! chmod 0644 "$temporary_selection" \
    || ! mv -f -- "$temporary_selection" "$selection_file"; then
    rm -f -- "$temporary_selection"
    return 1
  fi
}

switch_gateway_slot() {
  slot=$1
  selection_file="$CPA_DEPLOY_ROOT/state/edge/active-gateway.conf"
  previous_slot=$(read_active_slot_file "$selection_file") || {
    echo "cannot switch from an invalid active Gateway selection" >&2
    return 1
  }
  [ "$previous_slot" != "$slot" ] || return 0
  write_gateway_slot_file "$slot" || return 1
  if ! wait_edge_slot "$slot"; then
    if write_gateway_slot_file "$previous_slot" && wait_edge_slot "$previous_slot"; then
      echo "Go Edge slot switch failed and was rolled back to Gateway $previous_slot" >&2
    else
      echo "Go Edge slot switch and rollback both failed; manual recovery is required" >&2
    fi
    return 1
  fi
  echo "Go Edge switched new requests to Gateway $slot"
}

require_edge_recreate_policy() {
  edge_container=${CPA_INSTANCE_NAME:-codex-cpa}-edge
  container_exists "$edge_container" || return 0
  require_exact_compose_service "$edge_container" "${CPA_COMPOSE_PROJECT_NAME:-codex-cpa}" edge
  edge_recreate=$(service_recreate_state "$edge_container" edge "$CPA_EDGE_IMAGE") || return 1
  [ "$edge_recreate" = true ] || return 0
  [ "$CPA_ALLOW_EDGE_RECREATE" = true ] \
    && [ "${CPA_CONFIRM_EDGE_MAINTENANCE:-}" = "$CPA_DEPLOY_ROOT" ] || {
      echo "changed Edge image or configuration requires CPA_ALLOW_EDGE_RECREATE=true and CPA_CONFIRM_EDGE_MAINTENANCE=$CPA_DEPLOY_ROOT" >&2
      return 1
    }
}

start_gateway_service() {
  slot=$1
  project=$2
  control_network=$3
  service="gateway-$slot"
  container="${CPA_INSTANCE_NAME:-codex-cpa}-$service"
  compose up -d --no-deps "$service"
  ensure_target_network "$container" "$project" "$service" "$control_network"
  ensure_target_network "$container" "$project" "$service" "$CPA_UPSTREAM_NETWORK"
  wait_target_container "$container"
  require_service_current "$container" "$service" "$CPA_GATEWAY_IMAGE"
  docker exec "$container" wget -qO- http://127.0.0.1:8319/__internal/ready >/dev/null
}

require_core_topology() {
  instance=${CPA_INSTANCE_NAME:-codex-cpa}
  project=${CPA_COMPOSE_PROJECT_NAME:-codex-cpa}
  control_network="${project}_control"
  ingress_network="${project}_ingress"

  for container in "$instance-gateway-blue" "$instance-gateway-green" "$instance-admin"; do
    require_container_network "$container" "$control_network"
    require_container_network "$container" "$CPA_UPSTREAM_NETWORK"
  done
  require_container_network "$instance-web" "$control_network"
  require_container_network "$instance-edge" "$control_network"
  require_container_network "$instance-edge" "$ingress_network"
  require_container_port "$instance-edge" "8317/tcp" "$CPA_PUBLIC_BIND_ADDRESS" "$CPA_PUBLIC_PORT"
  require_container_port "$instance-edge" "8319/tcp" "127.0.0.1" "$CPA_INTERNAL_PORT"
}

activate_owner() {
  status=$(ownership_json)
  found=$(ownership_status_field found)
  active=$(ownership_status_field active)
  owner=$(ownership_status_field owner)
  generation=$(ownership_status_field generation)
  if [ "$active" = true ]; then
    if [ "$owner" = "$CPA_RUNTIME_OWNER" ]; then
      printf '%s\n' "$status"
      return
    fi
    echo "runtime ownership is still active for another writer: $owner generation=$generation" >&2
    exit 1
  fi
  set -- docker run --rm \
    -v "$CPA_DEPLOY_ROOT:$CPA_DEPLOY_ROOT" \
    "$CPA_CONTROL_IMAGE" \
    /usr/local/bin/cpa-ownership \
    --root "$CPA_DEPLOY_ROOT" \
    --ttl "$CPA_OWNERSHIP_ACTIVATION_TTL" \
    activate \
    --owner "$CPA_RUNTIME_OWNER" \
    --confirm-owner "$CPA_RUNTIME_OWNER"
  if [ "$found" = true ]; then
    set -- "$@" --expected-owner "$owner" --expected-generation "$generation"
  else
    case "${CPA_BOOTSTRAP_MODE:-}" in
      isolated-test)
        set -- "$@" --allow-empty-bootstrap
        ;;
      controlled-cutover)
        [ "${CPA_CONFIRM_WRITERS_STOPPED:-}" = "$CPA_DEPLOY_ROOT" ] || {
          echo "controlled cutover requires CPA_CONFIRM_WRITERS_STOPPED=$CPA_DEPLOY_ROOT" >&2
          exit 1
        }
        set -- "$@" --confirm-existing-writers-stopped "writers-stopped:$CPA_DEPLOY_ROOT"
        ;;
      *)
        echo "empty ownership history requires CPA_BOOTSTRAP_MODE=isolated-test or controlled-cutover" >&2
        exit 1
        ;;
    esac
  fi
  "$@"
}

smoke() {
  public_url="http://$CPA_PUBLIC_PROBE_HOST:$CPA_PUBLIC_PORT"
  internal_url="http://127.0.0.1:$CPA_INTERNAL_PORT"
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url/__health")" = 200 ]
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url/v1/models")" = 401 ]
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url/__internal/snapshots")" = 404 ]
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$internal_url/__internal/snapshots")" = 200 ]
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$internal_url/__internal/ready")" = 200 ]
  # The HTML routes are served by Web without touching Admin. The public site
  # configuration proves the Edge -> Web -> Admin path as well, so a missing
  # target control network cannot pass smoke with static pages alone.
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
    instance=${CPA_INSTANCE_NAME:-codex-cpa}
    project=${CPA_COMPOSE_PROJECT_NAME:-codex-cpa}
    control_network="${project}_control"
    ingress_network="${project}_ingress"
    require_edge_recreate_policy
    edge_container="$instance-edge"
    if container_exists "$edge_container" && container_running "$edge_container"; then
      require_exact_compose_service "$edge_container" "$project" edge
      ROLLOUT_EDGE_INTERNAL_PORT=$(container_bound_port "$edge_container" "8319/tcp")
      [ -n "$ROLLOUT_EDGE_INTERNAL_PORT" ] || {
        echo "running Edge has no loopback internal port binding" >&2
        exit 1
      }
      export ROLLOUT_EDGE_INTERNAL_PORT
      active_slot=$(curl --noproxy '*' -fsS "http://127.0.0.1:$ROLLOUT_EDGE_INTERNAL_PORT/__internal/edge/slot")
      case "$active_slot" in
        blue) inactive_slot=green ;;
        green) inactive_slot=blue ;;
        *) echo "running Edge returned invalid active Gateway slot: $active_slot" >&2; exit 1 ;;
      esac
      inactive_container="$instance-gateway-$inactive_slot"
      if container_exists "$inactive_container" && container_running "$inactive_container"; then
        require_exact_compose_service "$inactive_container" "$project" "gateway-$inactive_slot"
        # A previous rollout may have switched away from this slot and timed
        # out while preserving a long SSE. Never recreate it until it drains.
        wait_gateway_drain "$inactive_container"
      fi
      start_gateway_service "$inactive_slot" "$project" "$control_network"

      active_container="$instance-gateway-$active_slot"
      require_exact_compose_service "$active_container" "$project" "gateway-$active_slot"
      active_recreate=$(service_recreate_state "$active_container" "gateway-$active_slot" "$CPA_GATEWAY_IMAGE") || exit 1
      if [ "$active_recreate" = true ]; then
        switch_gateway_slot "$inactive_slot"
        if container_running "$active_container"; then
          wait_gateway_drain "$active_container"
        fi
        start_gateway_service "$active_slot" "$project" "$control_network"
      fi
    else
      start_gateway_service blue "$project" "$control_network"
      start_gateway_service green "$project" "$control_network"
    fi

    # Admin and Web are not on the model data path. Start them one at a time so
    # topology repair and readiness failures remain attributable.
    for service in admin web; do
      compose up -d --no-deps "$service"
      container="$instance-$service"
      ensure_target_network "$container" "$project" "$service" "$control_network"
      if [ "$service" = admin ]; then
        ensure_target_network "$container" "$project" "$service" "$CPA_UPSTREAM_NETWORK"
      fi
      wait_target_container "$container"
      if [ "$service" = admin ]; then
        require_service_current "$container" "$service" "$CPA_CONTROL_IMAGE"
      else
        require_service_current "$container" "$service" "$CPA_WEB_IMAGE"
      fi
    done

    if container_exists "$edge_container"; then
      edge_recreate=$(service_recreate_state "$edge_container" edge "$CPA_EDGE_IMAGE") || exit 1
      if [ "$edge_recreate" = true ]; then
        compose up -d --no-deps --force-recreate edge
      else
        compose up -d --no-deps --no-recreate edge
      fi
    else
      compose up -d --no-deps edge
    fi
    ensure_target_network "$edge_container" "$project" edge "$control_network"
    ensure_target_network "$edge_container" "$project" edge "$ingress_network"
    wait_target_container "$edge_container"
    require_service_current "$edge_container" edge "$CPA_EDGE_IMAGE"
    require_core_topology
    ;;
  up-writers)
    require_active_owner
    compose --profile writers up -d --wait --no-deps \
      usage-collector quota account-failover log-maintenance
    for service in usage-collector quota account-failover log-maintenance; do
      require_service_current "${CPA_INSTANCE_NAME:-codex-cpa}-$service" "$service" "$CPA_CONTROL_IMAGE"
    done
    ;;
  up-notifications)
    require_active_owner
    compose --profile external-effects up -d --wait --no-deps notifications
    require_service_current "${CPA_INSTANCE_NAME:-codex-cpa}-notifications" notifications "$CPA_CONTROL_IMAGE"
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
