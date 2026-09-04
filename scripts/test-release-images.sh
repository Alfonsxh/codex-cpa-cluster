#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/cpac-release-images-test.XXXXXX")
cleanup() {
  rm -rf -- "$TEST_ROOT"
}
trap cleanup EXIT HUP INT TERM

BIN_DIR="$TEST_ROOT/bin"
STATE_DIR="$TEST_ROOT/state"
DOCKER_LOG="$TEST_ROOT/docker.log"
RELEASECTL_LOG="$TEST_ROOT/releasectl.log"
REAL_RELEASECTL="$TEST_ROOT/cpa-releasectl"
REAL_GIT=$(command -v git)
mkdir -p "$BIN_DIR" "$STATE_DIR"
go build -o "$REAL_RELEASECTL" "$ROOT_DIR/cmd/releasectl"

cat >"$BIN_DIR/git" <<'EOF'
#!/usr/bin/env sh
set -eu
case " $* " in
  *" ls-files "*) exec "$FAKE_REAL_GIT" "$@" ;;
  *" status --porcelain "*) exit 0 ;;
  *" rev-parse HEAD "*) printf '%s\n' aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ;;
  *" rev-parse "*"^{commit}"*) printf '%s\n' aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ;;
  *) echo "unsupported fake git command: $*" >&2; exit 90 ;;
esac
EOF

cat >"$BIN_DIR/releasectl" <<'EOF'
#!/usr/bin/env sh
set -eu
printf '%s\n' "$*" >>"$FAKE_RELEASECTL_LOG"
exec "$FAKE_REAL_RELEASECTL" "$@"
EOF

cat >"$BIN_DIR/docker" <<'EOF'
#!/usr/bin/env sh
set -eu
printf '%s\n' "$*" >>"$FAKE_DOCKER_LOG"

reference_path() {
  printf '%s/%s\n' "$FAKE_STATE_DIR" "$(printf '%s' "$1" | sed 's#[/:]#_#g')"
}

component_digest() {
  case "$1" in
    control) printf '%s\n' "$CONTROL_DIGEST" ;;
    web) printf '%s\n' "$WEB_DIGEST" ;;
    gateway) printf '%s\n' "$GATEWAY_DIGEST" ;;
    edge) printf '%s\n' "$EDGE_DIGEST" ;;
    *) echo "unknown fake build component: $1" >&2; exit 90 ;;
  esac
}

[ "${1:-}" = buildx ] || {
  echo "layer pull/push path must not be used: $*" >&2
  exit 91
}
shift
case "${1:-}" in
  version)
    printf '%s\n' 'github.com/docker/buildx fake'
    ;;
  imagetools)
    shift
    case "${1:-}" in
      inspect)
        shift
        REFERENCE=
        while [ "$#" -gt 0 ]; do
          case "$1" in
            --format) shift 2 ;;
            *) REFERENCE=$1; shift ;;
          esac
        done
        if [ -n "${FAKE_INSPECT_NETWORK_MATCH:-}" ] \
          && printf '%s' "$REFERENCE" | grep -Fq "$FAKE_INSPECT_NETWORK_MATCH"; then
          echo "failed to do request: dial tcp: i/o timeout" >&2
          exit 1
        fi
        if [ -n "${FAKE_INSPECT_TRANSIENT_MATCH:-}" ] \
          && printf '%s' "$REFERENCE" | grep -Fq "$FAKE_INSPECT_TRANSIENT_MATCH"; then
          FAILURE_COUNTER=$(reference_path "inspect-failures-$REFERENCE")
          FAILURE_COUNT=0
          [ ! -f "$FAILURE_COUNTER" ] || FAILURE_COUNT=$(cat "$FAILURE_COUNTER")
          if [ "$FAILURE_COUNT" -lt "${FAKE_INSPECT_TRANSIENT_FAILURES:-1}" ]; then
            FAILURE_COUNT=$((FAILURE_COUNT + 1))
            printf '%s\n' "$FAILURE_COUNT" >"$FAILURE_COUNTER"
            echo "failed to do request: read: connection reset by peer" >&2
            exit 1
          fi
        fi
        RECORD=$(reference_path "$REFERENCE")
        if [ ! -f "$RECORD" ]; then
          echo "manifest unknown: $REFERENCE" >&2
          exit 1
        fi
        IFS=$(printf '\t') read -r MANIFEST COMPONENT DIGEST SOURCE_DIGEST <"$RECORD"
        printf '{"name":"%s","manifest":{"digest":"%s"},"image":{"config":{"Labels":{"io.codex-cpa.component":"%s","io.codex-cpa.component-digest":"%s","io.codex-cpa.source-digest":"%s"}}}}\n' \
          "$REFERENCE" "$MANIFEST" "$COMPONENT" "$DIGEST" "$SOURCE_DIGEST"
        ;;
      create)
        shift
        DESTINATION=
        SOURCE=
        while [ "$#" -gt 0 ]; do
          case "$1" in
            --prefer-index=false) shift ;;
            --tag) DESTINATION=$2; shift 2 ;;
            *) SOURCE=$1; shift ;;
          esac
        done
        if [ -n "${FAKE_FAIL_CREATE_MATCH:-}" ] \
          && printf '%s' "$DESTINATION" | grep -Fq "$FAKE_FAIL_CREATE_MATCH"; then
          echo "forced imagetools create failure: $DESTINATION" >&2
          exit 92
        fi
        if [ -n "${FAKE_CREATE_TRANSIENT_MATCH:-}" ] \
          && printf '%s' "$DESTINATION" | grep -Fq "$FAKE_CREATE_TRANSIENT_MATCH"; then
          FAILURE_COUNTER=$(reference_path "create-failures-$DESTINATION")
          FAILURE_COUNT=0
          [ ! -f "$FAILURE_COUNTER" ] || FAILURE_COUNT=$(cat "$FAILURE_COUNTER")
          if [ "$FAILURE_COUNT" -lt "${FAKE_CREATE_TRANSIENT_FAILURES:-1}" ]; then
            FAILURE_COUNT=$((FAILURE_COUNT + 1))
            printf '%s\n' "$FAILURE_COUNT" >"$FAILURE_COUNTER"
            echo "failed to do request: write: broken pipe" >&2
            exit 1
          fi
        fi
        SOURCE_RECORD=$(reference_path "$SOURCE")
        [ -f "$SOURCE_RECORD" ] || { echo "fake create source missing: $SOURCE" >&2; exit 90; }
        cp "$SOURCE_RECORD" "$(reference_path "$DESTINATION")"
        ;;
      *) echo "unsupported fake imagetools command: $*" >&2; exit 90 ;;
    esac
    ;;
  bake)
    [ "${FAKE_FAIL_BAKE:-false}" != true ] || {
      echo "forced bake failure" >&2
      exit 93
    }
    if [ -n "${CPAC_BAKE_TAG_PLAN:-}" ] && [ -f "$CPAC_BAKE_TAG_PLAN" ]; then
      while IFS=$(printf '\t') read -r COMPONENT REFERENCE; do
        DIGEST=$(component_digest "$COMPONENT")
        printf 'sha256:%s\t%s\t%s\t%s\n' "$DIGEST" "$COMPONENT" "$DIGEST" "$DIGEST" \
          >"$(reference_path "$REFERENCE")"
        printf 'bake-tag %s %s\n' "$COMPONENT" "$REFERENCE" >>"$FAKE_DOCKER_LOG"
      done <"$CPAC_BAKE_TAG_PLAN"
    fi
    shift
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --file) shift 2 ;;
        --push|--load) shift ;;
        *) shift ;;
      esac
    done
    ;;
  *) echo "unsupported fake docker command: $*" >&2; exit 90 ;;
esac
EOF

chmod 0755 "$BIN_DIR/git" "$BIN_DIR/releasectl" "$BIN_DIR/docker"

VERSION=v9.9.9-rc.1
COMPONENT_PLAN="$TEST_ROOT/components.tsv"
"$REAL_RELEASECTL" manifest plan --root "$ROOT_DIR" >"$COMPONENT_PLAN"

digest_for() {
  awk -F '\t' -v component="$1" '$1 == component { print $2; exit }' "$COMPONENT_PLAN"
}

state_path() {
  printf '%s/%s\n' "$STATE_DIR" "$(printf '%s' "$1" | sed 's#[/:]#_#g')"
}

put_reference() {
  REFERENCE=$1
  COMPONENT=$2
  DIGEST=$3
  ACTUAL_COMPONENT=${4:-$COMPONENT}
  printf 'sha256:%s\t%s\t%s\t%s\n' "$DIGEST" "$ACTUAL_COMPONENT" "$DIGEST" "$DIGEST" \
    >"$(state_path "$REFERENCE")"
}

put_component() {
  PREFIX=$1
  COMPONENT=$2
  WITH_LATEST=${3:-true}
  DIGEST=$(digest_for "$COMPONENT")
  put_reference "$PREFIX/codex-cpa-$COMPONENT:sha256-$DIGEST" "$COMPONENT" "$DIGEST"
  put_reference "$PREFIX/codex-cpa-$COMPONENT:$VERSION" "$COMPONENT" "$DIGEST"
  if [ "$WITH_LATEST" = true ]; then
    put_reference "$PREFIX/codex-cpa-$COMPONENT:latest" "$COMPONENT" "$DIGEST"
  fi
}

put_registry() {
  PREFIX=$1
  for COMPONENT in control web gateway edge; do
    put_component "$PREFIX" "$COMPONENT"
  done
}

reset_scenario() {
  rm -rf -- "$STATE_DIR"
  mkdir -p "$STATE_DIR"
  : >"$DOCKER_LOG"
  : >"$RELEASECTL_LOG"
}

run_publish() {
  PREFIXES=$1
  shift
  env \
    PATH="$BIN_DIR:$PATH" \
    FAKE_STATE_DIR="$STATE_DIR" \
    FAKE_DOCKER_LOG="$DOCKER_LOG" \
    FAKE_RELEASECTL_LOG="$RELEASECTL_LOG" \
    FAKE_REAL_RELEASECTL="$REAL_RELEASECTL" \
    FAKE_REAL_GIT="$REAL_GIT" \
    RELEASECTL="$BIN_DIR/releasectl" \
    VERSION="$VERSION" \
    IMAGE_PREFIXES="$PREFIXES" \
    PLATFORM=linux/amd64 \
    "$@" \
    sh "$ROOT_DIR/scripts/release-images.sh" publish
}

assert_plan_once() {
  [ "$(grep -c '^manifest plan ' "$RELEASECTL_LOG")" -eq 1 ] \
    || { echo "component plan was not generated exactly once" >&2; exit 1; }
  ! grep -Fq 'manifest digest' "$RELEASECTL_LOG" \
    || { echo "per-component digest command was used" >&2; exit 1; }
}

PREFIX_A=ghcr.io/fixture-a
PREFIX_B=ghcr.io/fixture-b

# Complete retry: no layer build, pull/push or unnecessary remote retag.
reset_scenario
put_registry "$PREFIX_A"
run_publish "$PREFIX_A" >/dev/null
assert_plan_once
! grep -Fq 'buildx bake' "$DOCKER_LOG" || { echo "complete retry rebuilt images" >&2; exit 1; }
! grep -Fq 'imagetools create' "$DOCKER_LOG" || { echo "complete retry moved an existing tag" >&2; exit 1; }
! grep -Eq '(^| )(pull|push|manifest|image)( |$)' "$DOCKER_LOG" \
  || { echo "complete retry used a layer transfer command" >&2; exit 1; }

# Only Web missing: one Bake invocation, one target, then an idempotent retry.
reset_scenario
for COMPONENT in control gateway edge; do put_component "$PREFIX_A" "$COMPONENT"; done
run_publish "$PREFIX_A" >/dev/null
assert_plan_once
[ "$(grep -c '^buildx bake ' "$DOCKER_LOG")" -eq 1 ] || { echo "missing Web did not use one Bake" >&2; exit 1; }
grep -Eq '(^| )web($| )' "$DOCKER_LOG" || { echo "missing Web did not select Web target" >&2; exit 1; }
for COMPONENT in control gateway edge; do
  ! grep -Eq "(^| )$COMPONENT($| )" "$DOCKER_LOG" || { echo "missing Web selected $COMPONENT" >&2; exit 1; }
done
: >"$DOCKER_LOG"
: >"$RELEASECTL_LOG"
run_publish "$PREFIX_A" >/dev/null
assert_plan_once
! grep -Fq 'buildx bake' "$DOCKER_LOG" || { echo "retry rebuilt completed Web" >&2; exit 1; }
! grep -Fq 'imagetools create' "$DOCKER_LOG" || { echo "retry retagged completed Web" >&2; exit 1; }

# Content exists but version is missing: promote remotely without a build.
reset_scenario
put_registry "$PREFIX_A"
WEB_DIGEST=$(digest_for web)
rm -f -- "$(state_path "$PREFIX_A/codex-cpa-web:$VERSION")" "$(state_path "$PREFIX_A/codex-cpa-web:latest")"
run_publish "$PREFIX_A" >/dev/null
! grep -Fq 'buildx bake' "$DOCKER_LOG" || { echo "content promotion rebuilt Web" >&2; exit 1; }
grep -Fq "imagetools create --prefer-index=false --tag $PREFIX_A/codex-cpa-web:$VERSION $PREFIX_A/codex-cpa-web:sha256-$WEB_DIGEST" "$DOCKER_LOG" \
  || { echo "content tag was not promoted to version remotely" >&2; exit 1; }

# Any immutable-label mismatch fails before build, promotion or latest movement.
reset_scenario
put_registry "$PREFIX_A"
put_reference "$PREFIX_A/codex-cpa-web:sha256-$WEB_DIGEST" web "$WEB_DIGEST" control
if run_publish "$PREFIX_A" >/dev/null 2>&1; then
  echo "mismatched immutable image was accepted" >&2
  exit 1
fi
! grep -Fq 'buildx bake' "$DOCKER_LOG" || { echo "mismatch triggered a build" >&2; exit 1; }
! grep -Fq 'imagetools create' "$DOCKER_LOG" || { echo "mismatch moved a remote tag" >&2; exit 1; }

# Registry/network failures are not interpreted as a missing image. Otherwise a
# transient outage could trigger an unnecessary build and overwrite mutable tags.
reset_scenario
put_registry "$PREFIX_A"
if run_publish "$PREFIX_A" REGISTRY_RETRY_DELAY_SECONDS=0 \
  FAKE_INSPECT_NETWORK_MATCH="codex-cpa-web:$VERSION" >/dev/null 2>&1; then
  echo "network inspection failure was accepted as a missing image" >&2
  exit 1
fi
! grep -Fq 'buildx bake' "$DOCKER_LOG" || { echo "network failure triggered a build" >&2; exit 1; }
! grep -Fq 'imagetools create' "$DOCKER_LOG" || { echo "network failure moved a remote tag" >&2; exit 1; }

# Bounded retries absorb transient Registry read failures without rebuilding or
# moving immutable/latest tags.
reset_scenario
put_registry "$PREFIX_A"
run_publish "$PREFIX_A" REGISTRY_RETRY_DELAY_SECONDS=0 \
  FAKE_INSPECT_TRANSIENT_MATCH="codex-cpa-web:$VERSION" \
  FAKE_INSPECT_TRANSIENT_FAILURES=2 >/dev/null
[ "$(grep -F "codex-cpa-web:$VERSION" "$DOCKER_LOG" | grep -c 'imagetools inspect')" -eq 3 ] \
  || { echo "transient inspection failure did not use the bounded retry" >&2; exit 1; }
! grep -Fq 'buildx bake' "$DOCKER_LOG" || { echo "transient retry rebuilt images" >&2; exit 1; }
! grep -Fq 'imagetools create' "$DOCKER_LOG" || { echo "transient retry moved a remote tag" >&2; exit 1; }

# Remote tag promotion is idempotent, so transient write failures are retried
# without rebuilding any image layers.
reset_scenario
put_registry "$PREFIX_A"
rm -f -- "$(state_path "$PREFIX_A/codex-cpa-web:$VERSION")" "$(state_path "$PREFIX_A/codex-cpa-web:latest")"
run_publish "$PREFIX_A" REGISTRY_RETRY_DELAY_SECONDS=0 \
  FAKE_CREATE_TRANSIENT_MATCH="$PREFIX_A/codex-cpa-web:$VERSION" \
  FAKE_CREATE_TRANSIENT_FAILURES=2 >/dev/null
[ "$(grep -F -- "--tag $PREFIX_A/codex-cpa-web:$VERSION" "$DOCKER_LOG" | grep -c 'imagetools create')" -eq 3 ] \
  || { echo "transient tag failure did not use the bounded retry" >&2; exit 1; }
! grep -Fq 'buildx bake' "$DOCKER_LOG" || { echo "transient tag retry rebuilt images" >&2; exit 1; }

# Build failure leaves latest untouched.
reset_scenario
for COMPONENT in control gateway edge; do put_component "$PREFIX_A" "$COMPONENT"; done
if run_publish "$PREFIX_A" FAKE_FAIL_BAKE=true >/dev/null 2>&1; then
  echo "forced Bake failure unexpectedly succeeded" >&2
  exit 1
fi
! grep -F ':latest' "$DOCKER_LOG" | grep -Fq 'imagetools create' \
  || { echo "Bake failure moved latest" >&2; exit 1; }

# Promotion failure also leaves latest untouched.
reset_scenario
put_registry "$PREFIX_A"
rm -f -- "$(state_path "$PREFIX_A/codex-cpa-web:$VERSION")"
if run_publish "$PREFIX_A" FAKE_FAIL_CREATE_MATCH="codex-cpa-web:$VERSION" >/dev/null 2>&1; then
  echo "forced promotion failure unexpectedly succeeded" >&2
  exit 1
fi
! grep -F ':latest' "$DOCKER_LOG" | grep -Fq 'imagetools create' \
  || { echo "promotion failure moved latest" >&2; exit 1; }

# Registry decisions stay independent: only missing Web tags in B are built.
reset_scenario
put_registry "$PREFIX_A"
put_registry "$PREFIX_B"
rm -f -- \
  "$(state_path "$PREFIX_B/codex-cpa-web:sha256-$WEB_DIGEST")" \
  "$(state_path "$PREFIX_B/codex-cpa-web:$VERSION")" \
  "$(state_path "$PREFIX_B/codex-cpa-web:latest")"
run_publish "$PREFIX_A $PREFIX_B" >/dev/null
[ "$(grep -c '^buildx bake ' "$DOCKER_LOG")" -eq 1 ] || { echo "multi-Registry plan did not use one Bake" >&2; exit 1; }
grep -Fq "$PREFIX_B/codex-cpa-web:sha256-$WEB_DIGEST" "$DOCKER_LOG" \
  || { echo "Registry B Web content tag was not built" >&2; exit 1; }
! grep -F 'buildx bake' "$DOCKER_LOG" | grep -Fq "$PREFIX_A/codex-cpa-web" \
  || { echo "Registry A Web was unnecessarily rebuilt" >&2; exit 1; }

printf '%s\n' 'release image publication contract tests passed'
