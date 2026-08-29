#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/codex-cpa-deploy-contract.XXXXXX")
TEST_ROOT=$(CDPATH= cd -- "$TEST_ROOT" && pwd -P)
trap 'rm -rf -- "$TEST_ROOT"' EXIT HUP INT TERM

DIGEST=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
HASH=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
IMAGE_ID=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
OLD_IMAGE_ID=dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
FAKE_BIN="$TEST_ROOT/bin"
COMMAND_LOG="$TEST_ROOT/docker.log"
mkdir -p "$FAKE_BIN"
: >"$COMMAND_LOG"

cat >"$FAKE_BIN/docker" <<'FAKE_DOCKER'
#!/usr/bin/env sh
set -eu
printf '%s\n' "$*" >>"$FAKE_DOCKER_LOG"
scenario=${FAKE_DOCKER_SCENARIO:-ordinary}
digest=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
hash=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
image_id=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
old_image_id=dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd

case " $* " in
  *" manifest get "*)
    printf '%s\n' "$digest"
    exit 0
    ;;
  *" status --field active "*)
    printf '%s\n' true
    exit 0
    ;;
  *" status --field found "*)
    printf '%s\n' true
    exit 0
    ;;
  *" status --field owner "*)
    printf '%s\n' codex-cpa
    exit 0
    ;;
  *" status --field generation "*)
    printf '%s\n' 7
    exit 0
    ;;
  *" cpa-ownership "*" status "*)
    printf '%s\n' '{"found":true,"active":true,"owner":"codex-cpa","generation":7}'
    exit 0
    ;;
esac

if [ "${1:-}" = image ] && [ "${2:-}" = inspect ]; then
  case " $* " in
    *"io.codex-cpa.component-digest"*|*"io.codex-cpa.source-digest"*)
      if [ "$scenario" = control-label-mismatch ] && printf '%s' "$*" | grep -q -- 'codex-cpa-control'; then
        printf '%s\n' eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee
      else
        printf '%s\n' "$digest"
      fi
      ;;
    *"io.codex-cpa.component"*)
      case "$*" in
        *codex-cpa-control*) printf '%s\n' control ;;
        *codex-cpa-web*) printf '%s\n' web ;;
        *codex-cpa-gateway*) printf '%s\n' gateway ;;
        *codex-cpa-edge*) printf '%s\n' edge ;;
        *) exit 1 ;;
      esac
      ;;
    *"{{.Id}}"*)
      printf 'sha256:%s\n' "$image_id"
      ;;
    *)
      printf '%s\n' '{}'
      ;;
  esac
  exit 0
fi

if [ "${1:-}" = inspect ]; then
  case " $* " in
    *"com.docker.compose.project"*)
      printf '%s\n' codex-cpa
      ;;
    *"com.docker.compose.service"*)
      case "$*" in
        *gateway-blue*) printf '%s\n' gateway-blue ;;
        *gateway-green*) printf '%s\n' gateway-green ;;
        *-usage-collector*) printf '%s\n' usage-collector ;;
        *-account-failover*) printf '%s\n' account-failover ;;
        *-log-maintenance*) printf '%s\n' log-maintenance ;;
        *-notifications*) printf '%s\n' notifications ;;
        *-quota*) printf '%s\n' quota ;;
        *-admin*) printf '%s\n' admin ;;
        *-web*) printf '%s\n' web ;;
        *-edge*) printf '%s\n' edge ;;
        *) exit 1 ;;
      esac
      ;;
    *"com.docker.compose.config-hash"*)
      printf '%s\n' "$hash"
      ;;
    *"{{.State.Running}}"*)
      printf '%s\n' true
      ;;
    *".State.Health"*)
      printf '%s\n' healthy
      ;;
    *"{{.Image}}"*)
      if [ "$scenario" = edge-change ] && printf '%s' "$*" | grep -q -- '-edge'; then
        printf 'sha256:%s\n' "$old_image_id"
      elif [ "$scenario" = slot-switch-failure ] && printf '%s' "$*" | grep -q -- 'gateway-blue'; then
        printf 'sha256:%s\n' "$old_image_id"
      else
        printf 'sha256:%s\n' "$image_id"
      fi
      ;;
    *".NetworkSettings.Networks"*)
      printf '%s\n' '{"codex-cpa_control":{},"codex-cpa_ingress":{},"cliproxy-backend":{}}'
      ;;
    *"8319/tcp"*)
      printf '%s\n' '127.0.0.1 18316'
      ;;
    *)
      printf '%s\n' '{}'
      ;;
  esac
  exit 0
fi

if [ "${1:-}" = network ] && [ "${2:-}" = inspect ]; then
  network_name=
  for argument in "$@"; do
    network_name=$argument
  done
  printf '%s\n' "$network_name"
  exit 0
fi

if [ "${1:-}" = compose ]; then
  case " $* " in
    *" config --hash "*)
      if [ "$scenario" = hash-failure ]; then
        exit 9
      fi
      service=
      for argument in "$@"; do
        service=$argument
      done
      printf '%s %s\n' "$service" "$hash"
      ;;
    *)
      ;;
  esac
  exit 0
fi

if [ "${1:-}" = exec ]; then
  case "$scenario" in
    drain-timeout)
      printf '%s\n' '[{"label":"fixture@example.com","account":"alpha","inflight":1}]'
      ;;
    invalid-stats)
      printf '%s\n' '{"unexpected":true}'
      ;;
    *)
      printf '%s\n' '[]'
      ;;
  esac
  exit 0
fi

case "${1:-}" in
  pull|run|network)
    exit 0
    ;;
esac
exit 0
FAKE_DOCKER
chmod 0755 "$FAKE_BIN/docker"

cat >"$FAKE_BIN/curl" <<'FAKE_CURL'
#!/usr/bin/env sh
set -eu
case "$*" in
  *'/__internal/edge/slot'*) printf '%s\n' blue ;;
  *) printf '%s\n' 200 ;;
esac
FAKE_CURL
chmod 0755 "$FAKE_BIN/curl"

cat >"$FAKE_BIN/sleep" <<'FAKE_SLEEP'
#!/usr/bin/env sh
exit 0
FAKE_SLEEP
chmod 0755 "$FAKE_BIN/sleep"

new_fixture() {
  fixture_name=$1
  FIXTURE="$TEST_ROOT/$fixture_name"
  DEPLOY_ROOT="$FIXTURE/target"
  ENV_FILE="$FIXTURE/target.env"
  COMPOSE_FILE="$DEPLOY_ROOT/docker-compose.yml"
  MANIFEST_FILE="$DEPLOY_ROOT/release-manifest.json"
  mkdir -p \
    "$DEPLOY_ROOT/state/gateway" \
    "$DEPLOY_ROOT/state/edge" \
    "$DEPLOY_ROOT/secrets" \
    "$DEPLOY_ROOT/logs/gateway"
  : >"$DEPLOY_ROOT/state/control-plane.sqlite3"
  : >"$DEPLOY_ROOT/state/usage.sqlite3"
  : >"$DEPLOY_ROOT/secrets/control-plane.key"
  printf '%s\n' 'set $active_gateway_backend gateway-blue:8317;' \
    >"$DEPLOY_ROOT/state/edge/active-gateway.conf"
  cp "$ROOT_DIR/docker-compose.yml" "$COMPOSE_FILE"
  printf '%s\n' '{}' >"$MANIFEST_FILE"
}

write_env() {
  confirmation=$1
  control_image=${2:-registry.example.test/codex-cpa-control:sha256-$DIGEST}
  allow_edge=${3:-false}
  edge_confirmation=${4:-}
  cat >"$ENV_FILE" <<EOF
CPA_CONTROL_IMAGE=$control_image
CPA_WEB_IMAGE=registry.example.test/codex-cpa-web:sha256-$DIGEST
CPA_GATEWAY_IMAGE=registry.example.test/codex-cpa-gateway:sha256-$DIGEST
CPA_EDGE_IMAGE=registry.example.test/codex-cpa-edge:sha256-$DIGEST
CPA_DEPLOY_ROOT=$DEPLOY_ROOT
CPA_CONFIRM_DEPLOY_ROOT=$confirmation
CPA_PUBLIC_BIND_ADDRESS=127.0.0.1
CPA_PUBLIC_PROBE_HOST=127.0.0.1
CPA_PUBLIC_PORT=18317
CPA_INTERNAL_PORT=18316
CPA_UPSTREAM_NETWORK=cliproxy-backend
CPA_ACCOUNT_COMPOSE_PROJECT=cliproxy-multi
CPA_ACCOUNT_INSTANCE_NAME=cliproxy
CPA_RUNTIME_OWNER=codex-cpa
CPA_GATEWAY_DRAIN_TIMEOUT_SECONDS=1
CPA_ALLOW_EDGE_RECREATE=$allow_edge
CPA_CONFIRM_EDGE_MAINTENANCE=$edge_confirmation
EOF
}

run_action() {
  scenario=$1
  action=$2
  PATH="$FAKE_BIN:$PATH" \
    FAKE_DOCKER_LOG="$COMMAND_LOG" \
    FAKE_DOCKER_SCENARIO="$scenario" \
    CPA_ENV_FILE="$ENV_FILE" \
    sh "$ROOT_DIR/scripts/deploy-target.sh" "$action"
}

if grep -Eq '^[[:space:]]+depends_on:' "$ROOT_DIR/docker-compose.yml"; then
  echo "formal target Compose must leave dependency ordering to deploy-target.sh" >&2
  exit 1
fi

expect_failure() {
  test_name=$1
  expected=$2
  scenario=$3
  action=$4
  output_file="$FIXTURE/output.log"
  if run_action "$scenario" "$action" >"$output_file" 2>&1; then
    echo "expected deployment contract failure: $test_name" >&2
    exit 1
  fi
  grep -Fq "$expected" "$output_file" || {
    echo "deployment contract failed for an unexpected reason: $test_name" >&2
    sed -n '1,120p' "$output_file" >&2
    exit 1
  }
}

new_fixture empty-confirmation
write_env ""
expect_failure empty-confirmation \
  "CPA_CONFIRM_DEPLOY_ROOT must exactly repeat CPA_DEPLOY_ROOT" ordinary config

new_fixture mutable-image
write_env "$DEPLOY_ROOT" 'registry.example.test/codex-cpa-control:v1.2.3'
expect_failure mutable-image \
  "CPA_CONTROL_IMAGE must use the immutable" ordinary config

new_fixture symlink-runtime-directory
rm -rf -- "$DEPLOY_ROOT/state/gateway"
mkdir "$FIXTURE/gateway-outside-root"
ln -s "$FIXTURE/gateway-outside-root" "$DEPLOY_ROOT/state/gateway"
write_env "$DEPLOY_ROOT"
expect_failure symlink-runtime-directory \
  "Go target runtime directory must be an existing non-symlink directory" ordinary config

new_fixture stale-target-compose
printf '%s\n' '# stale target copy' >>"$COMPOSE_FILE"
write_env "$DEPLOY_ROOT"
expect_failure stale-target-compose \
  "Go target Compose file does not match the selected release" ordinary config

new_fixture invalid-active-slot
printf '%s\n' 'set $active_gateway_backend gateway-unknown:8317;' \
  >"$DEPLOY_ROOT/state/edge/active-gateway.conf"
write_env "$DEPLOY_ROOT"
expect_failure invalid-active-slot \
  "Go target active Gateway selection must contain exactly blue or green" ordinary config

new_fixture unverified-control-image
write_env "$DEPLOY_ROOT"
: >"$COMMAND_LOG"
expect_failure unverified-control-image \
  "Go image label mismatch" control-label-mismatch verify-images
if grep -F 'manifest get' "$COMMAND_LOG" >/dev/null; then
  echo "unverified Control image was executed before its labels matched the immutable tag" >&2
  exit 1
fi

new_fixture edge-maintenance
write_env "$DEPLOY_ROOT"
expect_failure edge-maintenance \
  "changed Edge image or configuration requires" edge-change verify-images

new_fixture compose-hash-failure
write_env "$DEPLOY_ROOT"
expect_failure compose-hash-failure \
  "unable to calculate target Compose hash: service=edge" hash-failure verify-images

new_fixture drain-timeout
write_env "$DEPLOY_ROOT"
: >"$COMMAND_LOG"
expect_failure drain-timeout \
  "Go Gateway drain timed out without terminating existing requests" drain-timeout up-core
if grep -E 'compose .* up .*gateway-green' "$COMMAND_LOG" >/dev/null; then
  echo "drain timeout recreated the preserved inactive Gateway" >&2
  exit 1
fi

new_fixture invalid-inflight-stats
write_env "$DEPLOY_ROOT"
: >"$COMMAND_LOG"
expect_failure invalid-inflight-stats \
  "Go Gateway returned invalid in-flight statistics" invalid-stats up-core
if grep -E 'compose .* up .*gateway-green' "$COMMAND_LOG" >/dev/null; then
  echo "invalid in-flight statistics recreated the preserved inactive Gateway" >&2
  exit 1
fi

new_fixture slot-switch-rollback
write_env "$DEPLOY_ROOT"
: >"$COMMAND_LOG"
expect_failure slot-switch-rollback \
  "rolled back to Gateway blue" slot-switch-failure up-core
grep -Fxq 'set $active_gateway_backend gateway-blue:8317;' \
  "$DEPLOY_ROOT/state/edge/active-gateway.conf" || {
    echo "failed Gateway switch did not restore the previous active slot" >&2
    exit 1
  }
if grep -E 'compose .* up .*gateway-blue' "$COMMAND_LOG" >/dev/null; then
  echo "failed Gateway switch recreated the still-active old slot" >&2
  exit 1
fi

new_fixture writer-no-dependencies
write_env "$DEPLOY_ROOT"
: >"$COMMAND_LOG"
run_action ordinary up-writers >/dev/null
for service in quota usage-collector account-failover log-maintenance; do
  grep -E "compose .*--profile writers up .*--no-deps $service$" \
    "$COMMAND_LOG" >/dev/null || {
      echo "writer rollout did not start $service independently with --no-deps" >&2
      exit 1
    }
  grep -E "compose .*--profile writers config --hash $service$" \
    "$COMMAND_LOG" >/dev/null || {
      echo "writer rollout did not calculate the $service hash with its profile enabled" >&2
      exit 1
    }
done
if grep -E 'compose .*--profile writers up .*--no-deps .*usage-collector .*quota' \
  "$COMMAND_LOG" >/dev/null; then
  echo "writer rollout started multiple services concurrently" >&2
  exit 1
fi

new_fixture notification-profile
write_env "$DEPLOY_ROOT"
: >"$COMMAND_LOG"
run_action ordinary up-notifications >/dev/null
grep -E 'compose .*--profile external-effects up .*--no-deps notifications$' \
  "$COMMAND_LOG" >/dev/null || {
    echo "notification rollout did not start independently with its profile enabled" >&2
    exit 1
  }
grep -E 'compose .*--profile external-effects config --hash notifications$' \
  "$COMMAND_LOG" >/dev/null || {
    echo "notification rollout did not calculate its hash with its profile enabled" >&2
    exit 1
  }

printf '%s\n' 'Go target deployment contract tests passed'
