#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/codex-cpa-runtime-deploy-contract.XXXXXX")
TEST_ROOT=$(CDPATH= cd -- "$TEST_ROOT" && pwd -P)
trap 'rm -rf -- "$TEST_ROOT"' EXIT HUP INT TERM

DIGEST=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
HASH=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
IMAGE_ID=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
OLD_IMAGE_ID=dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
FAKE_BIN="$TEST_ROOT/bin"
COMMAND_LOG="$TEST_ROOT/docker.log"
SYSTEM_LOG="$TEST_ROOT/system.log"
mkdir -p "$FAKE_BIN"
: >"$COMMAND_LOG"
: >"$SYSTEM_LOG"

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
  *cpa-bootstrap*--root*)
    bootstrap_root=
    expect_root=false
    for argument in "$@"; do
      if [ "$expect_root" = true ]; then
        bootstrap_root=$argument
        expect_root=false
      elif [ "$argument" = --root ]; then
        expect_root=true
      fi
    done
    [ -n "$bootstrap_root" ] || exit 2
    mkdir -p \
      "$bootstrap_root/state/gateway" \
      "$bootstrap_root/state/edge" \
      "$bootstrap_root/secrets" \
      "$bootstrap_root/logs/gateway" \
      "$bootstrap_root/auth" \
      "$bootstrap_root/configs" \
      "$bootstrap_root/management/config/static"
    : >"$bootstrap_root/state/control-plane.sqlite3"
    : >"$bootstrap_root/state/usage.sqlite3"
    printf '%032d' 0 >"$bootstrap_root/secrets/control-plane.key"
    printf '%s\n' 'set $active_gateway_backend gateway-blue:8317;' \
      >"$bootstrap_root/state/edge/active-gateway.conf"
    printf '%s\n' generated-admin-management-key
    exit 0
    ;;
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
    *"8317/tcp"*)
      printf '%s\n' '127.0.0.1 18317'
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
    "$DEPLOY_ROOT/auth" \
    "$DEPLOY_ROOT/configs" \
    "$DEPLOY_ROOT/management/config/static" \
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
    sh "$ROOT_DIR/scripts/run.sh" __target "$action"
}

if grep -Eq '^[[:space:]]+depends_on:' "$ROOT_DIR/docker-compose.yml"; then
  echo "formal target Compose must leave dependency ordering to run.sh" >&2
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

RELEASE_VERSION=v9.9.9
RELEASE_SERVER="$TEST_ROOT/release-server"
RELEASE_CONTENT="$TEST_ROOT/release-content"
OPERATOR_ROOT="$TEST_ROOT/operator"
OPERATOR_CONFIG="$OPERATOR_ROOT/config.env"
mkdir -p \
  "$RELEASE_SERVER" \
  "$RELEASE_CONTENT" \
  "$OPERATOR_ROOT" \
  "$TEST_ROOT/nginx/available" \
  "$TEST_ROOT/nginx/enabled" \
  "$TEST_ROOT/certificates/qdata.example.com" \
  "$TEST_ROOT/acme"
cp "$ROOT_DIR/scripts/run.sh" "$OPERATOR_ROOT/run.sh"
chmod 0755 "$OPERATOR_ROOT/run.sh"
cat >"$OPERATOR_ROOT/deploy.sh" <<'LEGACY_OPERATOR'
#!/usr/bin/env sh
DEFAULT_REPOSITORY=${CPAC_GITHUB_REPOSITORY:-Alfonsxh/codex-cpa-cluster}
ENTRY_COMMAND=${1:-deploy}
LEGACY_OPERATOR
chmod 0755 "$OPERATOR_ROOT/deploy.sh"
cp "$ROOT_DIR/docker-compose.yml" "$RELEASE_CONTENT/docker-compose.yml"
printf '%s\n' '{}' >"$RELEASE_CONTENT/release-manifest.json"
tar -czf "$RELEASE_SERVER/codex-cpa-cluster-$RELEASE_VERSION.tar.gz" \
  -C "$RELEASE_CONTENT" docker-compose.yml release-manifest.json
cat >"$RELEASE_SERVER/release-$RELEASE_VERSION.env" <<EOF
CPAC_RELEASE_VERSION=$RELEASE_VERSION
CPAC_RELEASE_REVISION=9999999999999999999999999999999999999999
CPAC_RELEASE_ARCHIVE=codex-cpa-cluster-$RELEASE_VERSION.tar.gz
CPAC_CONTROL_IMAGE=registry.example.test/codex-cpa-control:sha256-$DIGEST
CPAC_WEB_IMAGE=registry.example.test/codex-cpa-web:sha256-$DIGEST
CPAC_GATEWAY_IMAGE=registry.example.test/codex-cpa-gateway:sha256-$DIGEST
CPAC_EDGE_IMAGE=registry.example.test/codex-cpa-edge:sha256-$DIGEST
EOF
cp "$ROOT_DIR/scripts/run.sh" "$RELEASE_SERVER/run.sh"
printf '%s\n' '# verified self-update fixture' >>"$RELEASE_SERVER/run.sh"
chmod 0755 "$RELEASE_SERVER/run.sh"
(
  cd "$RELEASE_SERVER"
  sha256sum \
    "codex-cpa-cluster-$RELEASE_VERSION.tar.gz" \
    "release-$RELEASE_VERSION.env" \
    run.sh >SHA256SUMS
)
cat >"$RELEASE_SERVER/releases.json" <<'EOF'
[
  {"tag_name":"v9.9.7"},
  {"tag_name":"v9.9.10-rc.2"},
  {"tag_name":"not-a-release"},
  {"tag_name":"v10.0.0-alpha.1"},
  {"tag_name":"v9.9.9"},
  {"tag_name":"v9.9.10"},
  {"tag_name":"v9.9.8"},
  {"tag_name":"v9.10.0-rc.1"},
  {"tag_name":"v9.9.10-rc.10"},
  {"tag_name":"v9.9.9"}
]
EOF
printf '%s\n' '{"message":"unexpected response"}' \
  >"$RELEASE_SERVER/releases-malformed.json"
printf '%s\n' certificate >"$TEST_ROOT/certificates/qdata.example.com/fullchain.pem"
printf '%s\n' private-key >"$TEST_ROOT/certificates/qdata.example.com/privkey.pem"

cat >"$FAKE_BIN/curl" <<'FAKE_OPERATOR_CURL'
#!/usr/bin/env sh
set -eu
output=
url=
write_format=
expect_output=false
expect_write=false
for argument in "$@"; do
  if [ "$expect_output" = true ]; then
    output=$argument
    expect_output=false
  elif [ "$expect_write" = true ]; then
    write_format=$argument
    expect_write=false
  else
    case "$argument" in
      -o) expect_output=true ;;
      -w) expect_write=true ;;
      http://*|https://*) url=$argument ;;
    esac
  fi
done
case "$url" in
  https://api.github.com/repos/*/releases\?per_page=100\&page=*)
    [ -n "$output" ] && [ "$output" != /dev/null ] || exit 2
    cp "${FAKE_RELEASES_FILE:-$FAKE_RELEASE_DIR/releases.json}" "$output"
    ;;
  */releases/latest)
    [ "$output" = /dev/null ] || exit 2
    printf 'https://github.example.test/releases/tag/%s' "$FAKE_RELEASE_VERSION"
    ;;
  */releases/download/*/*)
    asset=${url##*/}
    [ -n "$output" ] && [ "$output" != /dev/null ] || exit 2
    cp "$FAKE_RELEASE_DIR/$asset" "$output"
    ;;
  */__internal/edge/slot)
    printf '%s\n' blue
    ;;
  http://127.0.0.1:18317/v1/models)
    printf '%s' 401
    ;;
  http://127.0.0.1:18317/__internal/snapshots)
    printf '%s' 404
    ;;
  *)
    case "$write_format" in
      *http_code*) printf '%s' 200 ;;
    esac
    ;;
esac
FAKE_OPERATOR_CURL
chmod 0755 "$FAKE_BIN/curl"

for command in certbot flock getent nginx systemctl; do
  cat >"$FAKE_BIN/$command" <<'FAKE_SUCCESS'
#!/usr/bin/env sh
if [ -n "${FAKE_SYSTEM_LOG:-}" ]; then
  printf '%s %s\n' "$(basename -- "$0")" "$*" >>"$FAKE_SYSTEM_LOG"
fi
exit 0
FAKE_SUCCESS
  chmod 0755 "$FAKE_BIN/$command"
done
cat >"$FAKE_BIN/timedatectl" <<'FAKE_TIMEDATECTL'
#!/usr/bin/env sh
set -eu
if [ -n "${FAKE_SYSTEM_LOG:-}" ]; then
  printf '%s %s\n' "$(basename -- "$0")" "$*" >>"$FAKE_SYSTEM_LOG"
fi
timezone_file=${FAKE_TIMEZONE_FILE:-${FAKE_SYSTEM_LOG:-/tmp/cpac-system}.timezone}
case "${1:-}" in
  show)
    if [ -f "$timezone_file" ]; then
      cat "$timezone_file"
    else
      printf '%s\n' UTC
    fi
    ;;
  set-timezone)
    [ "$#" -eq 2 ] || exit 2
    printf '%s\n' "$2" >"$timezone_file"
    ;;
  *) exit 2 ;;
esac
FAKE_TIMEDATECTL
chmod 0755 "$FAKE_BIN/timedatectl"
cat >"$FAKE_BIN/readlink" <<'FAKE_READLINK'
#!/usr/bin/env sh
set -eu
if [ "${1:-}" = -f ]; then
  shift
  [ "${1:-}" != -- ] || shift
  target=$(/usr/bin/readlink "$1")
  case "$target" in
    /*) printf '%s\n' "$target" ;;
    *) (CDPATH= cd -- "$(dirname -- "$1")" && CDPATH= cd -- "$(dirname -- "$target")" && printf '%s/%s\n' "$PWD" "$(basename -- "$target")") ;;
  esac
else
  /usr/bin/readlink "$@"
fi
FAKE_READLINK
chmod 0755 "$FAKE_BIN/readlink"

run_operator_deploy() {
  PATH="$FAKE_BIN:$PATH" \
    FAKE_DOCKER_LOG="$COMMAND_LOG" \
    FAKE_SYSTEM_LOG="$SYSTEM_LOG" \
    FAKE_DOCKER_SCENARIO=ordinary \
    FAKE_RELEASE_DIR="$RELEASE_SERVER" \
    FAKE_RELEASE_VERSION="$RELEASE_VERSION" \
    CPAC_ALLOW_NON_ROOT=true \
    CPAC_STAGING_ROOT="$OPERATOR_ROOT" \
    CPAC_DEPLOY_ROOT="$OPERATOR_ROOT/runtime" \
    CPAC_BACKUP_DIR="$OPERATOR_ROOT/backups" \
    CPAC_LEGACY_CONFIG_FILE="$TEST_ROOT/etc/cpac/config.env" \
    CPAC_LOCK_FILE="$TEST_ROOT/cpa-deploy.lock" \
    CPAC_NGINX_AVAILABLE_DIRECTORY="$TEST_ROOT/nginx/available" \
    CPAC_NGINX_ENABLED_DIRECTORY="$TEST_ROOT/nginx/enabled" \
    CPAC_CERTIFICATE_ROOT="$TEST_ROOT/certificates" \
    CPAC_ACME_ROOT="$TEST_ROOT/acme" \
    sh "$OPERATOR_ROOT/run.sh" run \
      --domain qdata.example.com --ingress managed --tag "$RELEASE_VERSION"
}

run_release_choice() {
  PATH="$FAKE_BIN:$PATH" \
    FAKE_RELEASE_DIR="$RELEASE_SERVER" \
    FAKE_RELEASES_FILE="${FAKE_RELEASES_FILE:-$RELEASE_SERVER/releases.json}" \
    CPAC_ALLOW_NON_ROOT=true \
    CPAC_STAGING_ROOT="$OPERATOR_ROOT" \
    CPAC_DEPLOY_ROOT="$OPERATOR_ROOT/runtime" \
    CPAC_CONFIG_FILE="$OPERATOR_CONFIG" \
    sh "$OPERATOR_ROOT/run.sh" --tag
}

if sh "$OPERATOR_ROOT/run.sh" --tag v9.9.9 --version v9.9.8 \
  >"$OPERATOR_ROOT/conflicting-release-tag.log" 2>&1; then
  echo "operator deploy accepted conflicting --tag and --version values" >&2
  exit 1
fi
grep -Fq -- "--tag 与 --version 不能指定不同版本" \
  "$OPERATOR_ROOT/conflicting-release-tag.log" || {
    echo "operator deploy did not explain conflicting Release selectors" >&2
    exit 1
  }

INSTALL_OUTPUT="$OPERATOR_ROOT/install-output.log"
run_operator_deploy >"$INSTALL_OUTPUT"
for expected_output in \
  "== CPAC 安装与升级 ==" \
  "检查系统环境" \
  "统一服务器时区为 Asia/Shanghai" \
  "拉取 Control / Web / Gateway / Edge 镜像" \
  "镜像 Control" \
  "管理员登录  https://qdata.example.com/admin/" \
  "首次管理员凭据已安全保留"
do
  grep -Fq "$expected_output" "$INSTALL_OUTPUT" || {
    echo "operator deploy output is missing: $expected_output" >&2
    sed -n '1,240p' "$INSTALL_OUTPUT" >&2
    exit 1
  }
done
[ "$(cat "$SYSTEM_LOG.timezone")" = Asia/Shanghai ] \
  || { echo "operator deploy did not set the server timezone" >&2; exit 1; }
grep -Fq 'timedatectl set-timezone Asia/Shanghai' "$SYSTEM_LOG" \
  || { echo "operator deploy did not issue the server timezone change" >&2; exit 1; }
grep -Fxq 'CPA_TIMEZONE=Asia/Shanghai' "$OPERATOR_ROOT/runtime/target.env" \
  || { echo "operator deploy did not persist the service timezone" >&2; exit 1; }
completion_rows=$(grep -c '^│' "$INSTALL_OUTPUT")
bordered_completion_rows=$(grep -c '^│.*│$' "$INSTALL_OUTPUT")
[ "$completion_rows" -gt 0 ] && [ "$completion_rows" -eq "$bordered_completion_rows" ] || {
  echo "completion card rows are missing their right border" >&2
  sed -n '1,240p' "$INSTALL_OUTPUT" >&2
  exit 1
}
if grep -Fq "Go target images verified" "$INSTALL_OUTPUT"; then
  echo "successful operator deploy leaked captured target command output" >&2
  exit 1
fi
escape_character=$(printf '\033')
if grep -q "$escape_character" "$INSTALL_OUTPUT"; then
  echo "non-terminal operator deploy emitted ANSI color" >&2
  exit 1
fi
cp "$RELEASE_SERVER/SHA256SUMS" "$RELEASE_SERVER/SHA256SUMS.valid"
while IFS= read -r checksum_line; do
  case "$checksum_line" in
    *"  run.sh") printf '%064d  run.sh\n' 0 ;;
    *) printf '%s\n' "$checksum_line" ;;
  esac
done <"$RELEASE_SERVER/SHA256SUMS.valid" >"$RELEASE_SERVER/SHA256SUMS"
if run_operator_deploy >"$OPERATOR_ROOT/checksum-failure.log" 2>&1; then
  echo "operator deploy accepted a release with an invalid checksum" >&2
  exit 1
fi
for expected_failure in "校验发布文件 失败" "发布文件 SHA256 校验失败"; do
  grep -Fq "$expected_failure" "$OPERATOR_ROOT/checksum-failure.log" || {
    echo "captured failure output is missing: $expected_failure" >&2
    sed -n '1,240p' "$OPERATOR_ROOT/checksum-failure.log" >&2
    exit 1
  }
done
mv "$RELEASE_SERVER/SHA256SUMS.valid" "$RELEASE_SERVER/SHA256SUMS"
cmp -s "$OPERATOR_ROOT/run.sh" "$RELEASE_SERVER/run.sh" \
  || { echo "run.sh did not update itself from the verified release asset" >&2; exit 1; }
[ -f "$OPERATOR_ROOT/runtime/.deploy-initialized" ] \
  || { echo "fresh deploy did not publish its version marker" >&2; exit 1; }
[ -d "$OPERATOR_ROOT/runtime/management/config/static" ] \
  && [ ! -L "$OPERATOR_ROOT/runtime/management/config/static" ] \
  || { echo "fresh deploy did not create the account management static directory" >&2; exit 1; }
[ -f "$OPERATOR_ROOT/bootstrap-admin.key" ] \
  || { echo "fresh deploy did not preserve the pending admin key" >&2; exit 1; }
[ ! -e "$TEST_ROOT/etc/cpac" ] \
  || { echo "fresh deploy created the removed external operator config directory" >&2; exit 1; }
[ ! -e "$OPERATOR_ROOT/runtime/scripts" ] \
  || { echo "fresh deploy published a second target-side script directory" >&2; exit 1; }
[ "$(cat "$OPERATOR_CONFIG")" = "$(printf 'CPA_DOMAIN=qdata.example.com\nCPAC_INGRESS_MODE=managed')" ] \
  || { echo "fresh deploy did not persist its domain" >&2; exit 1; }
[ ! -e "$OPERATOR_ROOT/deploy.sh" ] \
  || { echo "run.sh did not remove the recognized legacy operator script" >&2; exit 1; }

cp "$OPERATOR_CONFIG" "$OPERATOR_ROOT/config-before-release-choice.env"
cp "$OPERATOR_ROOT/runtime/.deploy-initialized" "$OPERATOR_ROOT/deploy-marker-before-release-choice"
printf '%s\n' 'version=v9.9.8' >"$OPERATOR_ROOT/runtime/.deploy-initialized"
chmod 0600 "$OPERATOR_ROOT/runtime/.deploy-initialized"
: >"$COMMAND_LOG"
: >"$SYSTEM_LOG"
RELEASE_CHOICE_OUTPUT="$OPERATOR_ROOT/release-choice-output.log"
run_release_choice </dev/null >"$RELEASE_CHOICE_OUTPUT"
grep -Fq '当前部署  v9.9.8' "$RELEASE_CHOICE_OUTPUT" \
  || { echo "release choice did not report the current deployed version" >&2; cat "$RELEASE_CHOICE_OUTPUT" >&2; exit 1; }
grep -Fq '非交互模式仅展示版本，不执行升级' "$RELEASE_CHOICE_OUTPUT" \
  || { echo "non-interactive release choice did not remain read-only" >&2; cat "$RELEASE_CHOICE_OUTPUT" >&2; exit 1; }
selected_release_tags=$(sed -n 's/^  [0-9][0-9]*) \([^ ]*\).*/\1/p' "$RELEASE_CHOICE_OUTPUT")
expected_release_tags=$(printf '%s\n' \
  v10.0.0-alpha.1 \
  v9.10.0-rc.1 \
  v9.9.10 \
  v9.9.10-rc.10 \
  v9.9.10-rc.2 \
  v9.9.9)
[ "$selected_release_tags" = "$expected_release_tags" ] || {
  echo "release choices were not filtered, de-duplicated and sorted by version" >&2
  printf 'expected:\n%s\nactual:\n%s\n' "$expected_release_tags" "$selected_release_tags" >&2
  exit 1
}
cmp -s "$OPERATOR_CONFIG" "$OPERATOR_ROOT/config-before-release-choice.env" \
  || { echo "release choice changed the operator configuration" >&2; exit 1; }
[ ! -s "$COMMAND_LOG" ] && [ ! -s "$SYSTEM_LOG" ] \
  || { echo "release choice invoked deployment or system commands" >&2; exit 1; }

printf '%s\n' 'version=v10.0.0' >"$OPERATOR_ROOT/runtime/.deploy-initialized"
run_release_choice </dev/null >"$OPERATOR_ROOT/no-release-update.log"
grep -Fq '没有比当前部署更新的 GitHub Release' "$OPERATOR_ROOT/no-release-update.log" \
  || { echo "release choice did not report the no-update state" >&2; cat "$OPERATOR_ROOT/no-release-update.log" >&2; exit 1; }

rm -f -- "$OPERATOR_ROOT/runtime/.deploy-initialized"
if run_release_choice </dev/null >"$OPERATOR_ROOT/missing-release-marker.log" 2>&1; then
  echo "release choice accepted a missing deployed-version marker" >&2
  exit 1
fi
grep -Fq '未检测到有效的当前部署版本' "$OPERATOR_ROOT/missing-release-marker.log" \
  || { echo "release choice did not explain the missing deployed version" >&2; cat "$OPERATOR_ROOT/missing-release-marker.log" >&2; exit 1; }
mv "$OPERATOR_ROOT/deploy-marker-before-release-choice" \
  "$OPERATOR_ROOT/runtime/.deploy-initialized"

printf '%s\n' 'version=v9.9.8' >"$OPERATOR_ROOT/runtime/.deploy-initialized"
if FAKE_RELEASES_FILE="$RELEASE_SERVER/releases-malformed.json" \
  run_release_choice </dev/null >"$OPERATOR_ROOT/malformed-releases.log" 2>&1; then
  echo "release choice accepted a malformed GitHub response" >&2
  exit 1
fi
grep -Fq 'GitHub Releases 返回了无效响应' "$OPERATOR_ROOT/malformed-releases.log" \
  || { echo "release choice did not explain a malformed GitHub response" >&2; cat "$OPERATOR_ROOT/malformed-releases.log" >&2; exit 1; }
printf '%s\n' "version=$RELEASE_VERSION" >"$OPERATOR_ROOT/runtime/.deploy-initialized"
chmod 0600 "$OPERATOR_ROOT/runtime/.deploy-initialized"

EXTERNAL_OPERATOR_ROOT="$TEST_ROOT/home/external-cpac"
EXTERNAL_CONFIG="$EXTERNAL_OPERATOR_ROOT/config.env"
mkdir -p "$EXTERNAL_OPERATOR_ROOT"
cp "$ROOT_DIR/scripts/run.sh" "$EXTERNAL_OPERATOR_ROOT/run.sh"
chmod 0755 "$EXTERNAL_OPERATOR_ROOT/run.sh"
: >"$SYSTEM_LOG"
PATH="$FAKE_BIN:$PATH" \
  FAKE_DOCKER_LOG="$COMMAND_LOG" \
  FAKE_SYSTEM_LOG="$SYSTEM_LOG" \
  FAKE_DOCKER_SCENARIO=ordinary \
  FAKE_RELEASE_DIR="$RELEASE_SERVER" \
  FAKE_RELEASE_VERSION="$RELEASE_VERSION" \
  CPAC_ALLOW_NON_ROOT=true \
  CPAC_STAGING_ROOT="$EXTERNAL_OPERATOR_ROOT" \
  CPAC_DEPLOY_ROOT="$EXTERNAL_OPERATOR_ROOT/runtime" \
  CPAC_BACKUP_DIR="$EXTERNAL_OPERATOR_ROOT/backups" \
  CPAC_LEGACY_CONFIG_FILE="$TEST_ROOT/external-etc/cpac/config.env" \
  CPAC_LOCK_FILE="$TEST_ROOT/external-cpa-deploy.lock" \
  CPAC_NGINX_AVAILABLE_DIRECTORY="$TEST_ROOT/nginx/available" \
  CPAC_NGINX_ENABLED_DIRECTORY="$TEST_ROOT/nginx/enabled" \
  CPAC_CERTIFICATE_ROOT="$TEST_ROOT/certificates" \
  CPAC_ACME_ROOT="$TEST_ROOT/acme" \
  sh "$EXTERNAL_OPERATOR_ROOT/run.sh" run \
    --domain existing.example.com --ingress external --version "$RELEASE_VERSION" \
    >"$EXTERNAL_OPERATOR_ROOT/install-output.log"
[ "$(cat "$EXTERNAL_CONFIG")" = "$(printf 'CPA_DOMAIN=existing.example.com\nCPAC_INGRESS_MODE=external')" ] \
  || { echo "external ingress mode was not persisted" >&2; exit 1; }
grep -Fq '复用现有反向代理' "$EXTERNAL_OPERATOR_ROOT/install-output.log" \
  || { echo "external ingress did not describe the existing proxy contract" >&2; exit 1; }
grep -Fq '已跳过公网检查' "$EXTERNAL_OPERATOR_ROOT/install-output.log" \
  || { echo "external ingress unexpectedly performed public HTTPS validation" >&2; exit 1; }
grep -Fq '外部托管；Nginx / Certbot 未修改' "$EXTERNAL_OPERATOR_ROOT/install-output.log" \
  || { echo "external ingress summary did not confirm Nginx was untouched" >&2; exit 1; }
grep -Fq '管理员登录  https://existing.example.com/admin/' "$EXTERNAL_OPERATOR_ROOT/install-output.log" \
  || { echo "external ingress completion did not show the login URL" >&2; exit 1; }
if grep -Eq '^(nginx|certbot) |^systemctl .*nginx' "$SYSTEM_LOG"; then
  echo "external ingress changed Nginx or Certbot" >&2
  cat "$SYSTEM_LOG" >&2
  exit 1
fi
[ ! -e "$TEST_ROOT/nginx/available/existing.example.com.conf" ] \
  && [ ! -e "$TEST_ROOT/nginx/enabled/existing.example.com.conf" ] \
  || { echo "external ingress wrote an Nginx site" >&2; exit 1; }

mv "$OPERATOR_ROOT/runtime/release-manifest.json" \
  "$OPERATOR_ROOT/runtime/release-manifest.json.missing"
if run_operator_deploy >"$OPERATOR_ROOT/missing-metadata.log" 2>&1; then
  echo "upgrade accepted a target with missing release metadata" >&2
  exit 1
fi
grep -Fq \
  "升级前置文件缺失或不是普通文件：$OPERATOR_ROOT/runtime/release-manifest.json" \
  "$OPERATOR_ROOT/missing-metadata.log" || {
    echo "upgrade did not explain the missing release metadata" >&2
    sed -n '1,120p' "$OPERATOR_ROOT/missing-metadata.log" >&2
    exit 1
  }
mv "$OPERATOR_ROOT/runtime/release-manifest.json.missing" \
  "$OPERATOR_ROOT/runtime/release-manifest.json"

rm -rf -- "$OPERATOR_ROOT/runtime/management/config"
mkdir "$OPERATOR_ROOT/unsafe-management-static"
ln -s "$OPERATOR_ROOT/unsafe-management-static" \
  "$OPERATOR_ROOT/runtime/management/config"
if run_operator_deploy >"$OPERATOR_ROOT/unsafe-layout.log" 2>&1; then
  echo "upgrade accepted a symbolic-link account runtime layout" >&2
  exit 1
fi
grep -Fq \
  "账号运行目录必须是普通目录且不能是符号链接：$OPERATOR_ROOT/runtime/management/config" \
  "$OPERATOR_ROOT/unsafe-layout.log" || {
    echo "upgrade did not explain the unsafe account runtime layout" >&2
    sed -n '1,120p' "$OPERATOR_ROOT/unsafe-layout.log" >&2
    exit 1
  }
if [ -d "$OPERATOR_ROOT/backups" ] \
  && find "$OPERATOR_ROOT/backups" -type f -name '*.tar.gz' | grep -q .; then
  echo "unsafe account runtime layout was backed up or mutated before rejection" >&2
  exit 1
fi
rm -- "$OPERATOR_ROOT/runtime/management/config"

mkdir -p "$OPERATOR_ROOT/runtime/scripts"
printf '%s\n' legacy >"$OPERATOR_ROOT/runtime/scripts/legacy.sh"
old_target_env="$OPERATOR_ROOT/runtime/.target-env.old"
awk -F= -v old_digest="$OLD_IMAGE_ID" '
  $1 == "CPA_CONTROL_IMAGE" || $1 == "CPA_WEB_IMAGE" {
    sub(/sha256-[0-9a-f]+$/, "sha256-" old_digest)
  }
  { print }
' "$OPERATOR_ROOT/runtime/target.env" >"$old_target_env"
chmod 0600 "$old_target_env"
mv "$old_target_env" "$OPERATOR_ROOT/runtime/target.env"
printf '%s\n' 'version=v9.9.8' >"$OPERATOR_ROOT/runtime/.deploy-initialized"
chmod 0600 "$OPERATOR_ROOT/runtime/.deploy-initialized"
UPGRADE_OUTPUT="$OPERATOR_ROOT/upgrade-output.log"
run_operator_deploy >"$UPGRADE_OUTPUT"
for expected_change in \
  "版本        v9.9.8 -> $RELEASE_VERSION" \
  "镜像 Control  更新 dddddddddddd -> aaaaaaaaaaaa" \
  "Web      更新 dddddddddddd -> aaaaaaaaaaaa" \
  "Gateway  复用 aaaaaaaaaaaa" \
  "Edge     复用 aaaaaaaaaaaa" \
  "备份        已创建 $OPERATOR_ROOT/backups/"
do
  grep -Fq "$expected_change" "$UPGRADE_OUTPUT" || {
    echo "upgrade summary is missing: $expected_change" >&2
    sed -n '1,260p' "$UPGRADE_OUTPUT" >&2
    exit 1
  }
done
[ -d "$OPERATOR_ROOT/runtime/management/config/static" ] \
  && [ ! -L "$OPERATOR_ROOT/runtime/management/config/static" ] \
  || { echo "upgrade did not repair the missing account management static directory" >&2; exit 1; }
[ ! -e "$OPERATOR_ROOT/runtime/scripts" ] \
  || { echo "upgrade did not remove the legacy target-side script directory" >&2; exit 1; }
backup_count=$(find "$OPERATOR_ROOT/backups" -type f -name '*.tar.gz' | wc -l | tr -d ' ')
[ "$backup_count" -eq 1 ] || { echo "upgrade backup count = $backup_count" >&2; exit 1; }
backup_file=$(find "$OPERATOR_ROOT/backups" -type f -name '*.tar.gz')
for database in state/control-plane.sqlite3 state/usage.sqlite3; do
  [ "$(tar -tzf "$backup_file" | grep -Fxc "$database")" -eq 1 ] || {
    echo "upgrade backup does not contain exactly one consistent $database" >&2
    exit 1
  }
done
if tar -tzf "$backup_file" | grep -Eq '^state/(control-plane|usage)\.sqlite3-(wal|shm)$'; then
  echo "upgrade backup contains live SQLite WAL/SHM files" >&2
  exit 1
fi
backup_extract="$OPERATOR_ROOT/backup-extract"
mkdir "$backup_extract"
tar -xzf "$backup_file" -C "$backup_extract" \
  state/control-plane.sqlite3 state/usage.sqlite3
for database in state/control-plane.sqlite3 state/usage.sqlite3; do
  [ "$(sqlite3 "$backup_extract/$database" 'PRAGMA quick_check;')" = ok ] || {
    echo "backup quick_check failed: $database" >&2
    exit 1
  }
done

site_file="$TEST_ROOT/nginx/available/qdata.example.com.conf"
legacy_site="$TEST_ROOT/nginx/legacy-site.conf"
sed 's/^# Managed by CPAC run\.sh$/# Managed by CPAC deploy.sh/' \
  "$site_file" >"$legacy_site"
mv "$legacy_site" "$site_file"
run_operator_deploy >"$OPERATOR_ROOT/legacy-nginx-marker.log"
grep -Fxq '# Managed by CPAC run.sh' "$site_file" \
  || { echo "run.sh did not migrate the legacy Nginx ownership marker" >&2; exit 1; }

site_without_marker="$TEST_ROOT/nginx/unmanaged-site.conf"
sed '/^# Managed by CPAC run\.sh$/d' "$site_file" >"$site_without_marker"
mv "$site_without_marker" "$site_file"
if run_operator_deploy >"$OPERATOR_ROOT/unmanaged-site.log" 2>&1; then
  echo "managed ingress overwrote an unmanaged same-domain site" >&2
  exit 1
fi
grep -Fq 'Nginx 已有未托管的同名站点，拒绝覆盖' "$OPERATOR_ROOT/unmanaged-site.log" \
  || { echo "managed ingress did not explain the unmanaged site refusal" >&2; cat "$OPERATOR_ROOT/unmanaged-site.log" >&2; exit 1; }

printf '%s\n' 'Go target deployment contract tests passed'
