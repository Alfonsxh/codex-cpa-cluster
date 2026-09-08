#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/cpa-pool-compat.XXXXXX")
trap 'rm -rf -- "$TEST_ROOT"' EXIT HUP INT TERM
# Load the actual installer helpers without entering the operator or target CLI.
sed '/^ENTRY_COMMAND=/,$d' "$ROOT_DIR/scripts/run.sh" >"$TEST_ROOT/helpers.sh"
CPAP_STAGING_ROOT="$TEST_ROOT/operator"
CPAP_DEPLOY_ROOT="$TEST_ROOT/operator/runtime"
export CPAP_STAGING_ROOT CPAP_DEPLOY_ROOT
. "$TEST_ROOT/helpers.sh"

fail() { printf '%s\n' "$*" >&2; exit 1; }
reject() {
  if ( "$@" ) >"$TEST_ROOT/rejected.log" 2>&1; then fail "unexpectedly accepted: $*"; fi
}

# Compatibility inputs may agree; ambiguity must stop before any mutation.
CPAC_STAGING_ROOT="$CPAP_STAGING_ROOT" sh "$ROOT_DIR/scripts/run.sh" help >/dev/null
reject env CPAC_STAGING_ROOT="$TEST_ROOT/other" sh "$ROOT_DIR/scripts/run.sh" help
mkdir -p "$TEST_ROOT/legacy"
[ "$(resolve_existing_root "$TEST_ROOT/current" "$TEST_ROOT/legacy")" = "$TEST_ROOT/legacy" ] \
  || fail 'existing root was not reused'
mkdir "$TEST_ROOT/current"
reject resolve_existing_root "$TEST_ROOT/current" "$TEST_ROOT/legacy"

# Existing runtime aliases resolve without touching the target or accepting a
# second deployment. Missing new-install parents are normalized without mkdir.
PHYSICAL_ROOT=$(CDPATH= cd -- "$TEST_ROOT/legacy" && pwd -P)
ln -s legacy "$TEST_ROOT/runtime"
[ "$(resolve_existing_root "$TEST_ROOT/runtime" "$TEST_ROOT/legacy")" = "$TEST_ROOT/runtime" ] \
  || fail 'two entrances to the same runtime were treated as different deployments'
[ "$(resolve_deploy_root "$TEST_ROOT/runtime/")" = "$PHYSICAL_ROOT" ] \
  || fail 'relative runtime alias was not resolved to the physical root'
ln -s runtime "$TEST_ROOT/runtime-chain"
[ "$(resolve_deploy_root "$TEST_ROOT/runtime-chain")" = "$PHYSICAL_ROOT" ] \
  || fail 'runtime alias chain was not resolved'
[ "$(resolve_deploy_root "$TEST_ROOT/runtime/new/child")" = "$PHYSICAL_ROOT/new/child" ] \
  && [ ! -e "$PHYSICAL_ROOT/new" ] || fail 'new-root resolution created or misplaced directories'
ln -s absent "$TEST_ROOT/dangling-runtime"
ln -s loop-b "$TEST_ROOT/loop-a"
ln -s loop-a "$TEST_ROOT/loop-b"
printf '%s\n' preserved >"$TEST_ROOT/runtime-file"
ln -s runtime-file "$TEST_ROOT/file-runtime"
ln -s / "$TEST_ROOT/root-runtime"
for invalid_root in \
  "$TEST_ROOT/dangling-runtime" "$TEST_ROOT/dangling-runtime/" \
  "$TEST_ROOT/dangling-runtime/child" "$TEST_ROOT/loop-a" \
  "$TEST_ROOT/file-runtime" "$TEST_ROOT/runtime-file/child" "$TEST_ROOT/root-runtime" \
  relative/runtime "$TEST_ROOT/absent/../runtime"
do
  reject resolve_deploy_root "$invalid_root"
  reject env CPAP_ALLOW_NON_ROOT=true CPAP_DEPLOY_ROOT="$invalid_root" \
    CPAP_CONFIG_FILE="$TEST_ROOT/invalid-root-config.env" \
    sh "$ROOT_DIR/scripts/run.sh" run --domain example.test --ingress external --tag v9.8.7
  [ ! -e "$TEST_ROOT/invalid-root-config.env" ] || fail 'invalid runtime wrote configuration'
done

# The saved physical root disambiguates old directories without an environment
# override, survives ordinary config edits, and never becomes a fresh install.
SAVED_CONFIG="$TEST_ROOT/saved-root.env"
write_config "$SAVED_CONFIG" example.test external "$PHYSICAL_ROOT"
(
  unset CPAP_DEPLOY_ROOT CPAC_DEPLOY_ROOT
  [ "$(resolve_operator_deploy_root "$SAVED_CONFIG" "$TEST_ROOT/current" "$TEST_ROOT/legacy")" = "$PHYSICAL_ROOT" ] \
    || fail 'saved root did not disambiguate different existing defaults'
)
write_config "$SAVED_CONFIG" changed.example.test managed
[ "$(config_deploy_root "$SAVED_CONFIG")" = "$PHYSICAL_ROOT" ] \
  || fail 'domain or ingress update erased the saved root'
CPAP_DEPLOY_ROOT="$TEST_ROOT/runtime-chain" \
  resolve_operator_deploy_root "$SAVED_CONFIG" "$TEST_ROOT/current" "$TEST_ROOT/legacy" >/dev/null
reject env CPAP_DEPLOY_ROOT="$TEST_ROOT/current" CPAP_ALLOW_NON_ROOT=true \
  sh "$ROOT_DIR/scripts/run.sh" --config "$SAVED_CONFIG" --tag
cp "$SAVED_CONFIG" "$TEST_ROOT/saved-root.before"
if (
  unset CPAP_DEPLOY_ROOT CPAC_DEPLOY_ROOT
  CPAP_ALLOW_NON_ROOT=true sh "$ROOT_DIR/scripts/run.sh" --config "$SAVED_CONFIG" --tag
) >"$TEST_ROOT/saved-root-query.log" 2>&1; then fail 'query accepted an absent version marker'; fi
grep -Fq '未检测到有效的当前部署版本' "$TEST_ROOT/saved-root-query.log" \
  || fail '--config did not select the saved root before default discovery'
cmp -s "$SAVED_CONFIG" "$TEST_ROOT/saved-root.before" || fail 'read-only query rewrote configuration'
printf 'CPAP_DEPLOY_ROOT=%s\n' "$PHYSICAL_ROOT" >>"$SAVED_CONFIG"
reject config_deploy_root "$SAVED_CONFIG"
printf 'CPAP_DEPLOY_ROOT=\n' >"$SAVED_CONFIG"
reject config_deploy_root "$SAVED_CONFIG"
printf 'CPAP_DEPLOY_ROOT=%s\n' "$TEST_ROOT/removed-deployment" >"$SAVED_CONFIG"
reject resolve_operator_deploy_root "$SAVED_CONFIG" "$TEST_ROOT/current" "$TEST_ROOT/legacy"
[ ! -e "$TEST_ROOT/removed-deployment" ] || fail 'missing saved root was recreated'
printf 'CPAP_DEPLOY_ROOT=$(touch %s)\n' "$TEST_ROOT/injected" >"$SAVED_CONFIG"
reject config_deploy_root "$SAVED_CONFIG"
[ ! -e "$TEST_ROOT/injected" ] || fail 'saved configuration was executed'
rm "$SAVED_CONFIG"
ln -s runtime-file "$SAVED_CONFIG"
reject config_deploy_root "$SAVED_CONFIG"
rm "$SAVED_CONFIG"
write_config "$SAVED_CONFIG" example.test external "$PHYSICAL_ROOT"

# The stdin entry must resolve before replacing run.sh, and pass a physical root
# even to a downloaded installer that does not itself know about aliases.
PIPE_OPERATOR="$TEST_ROOT/alias-operator"
mkdir "$PIPE_OPERATOR"
ln -s ../legacy "$PIPE_OPERATOR/runtime"
cat >"$TEST_ROOT/capture-installer.sh" <<'CAPTURE_INSTALLER'
#!/usr/bin/env sh
set -eu
printf '%s\n' "$CPAP_DEPLOY_ROOT" "${CPAC_DEPLOY_ROOT-unset}" >"$ROOT_CAPTURE"
CAPTURE_INSTALLER
sed "s|/opt/codex-cpa-cluster|$TEST_ROOT/legacy|g" "$ROOT_DIR/scripts/run.sh" >"$TEST_ROOT/alias-bootstrap.sh"
(
  unset CPAP_DEPLOY_ROOT CPAC_DEPLOY_ROOT
  CPAP_ALLOW_NON_ROOT=true CPAP_STAGING_ROOT="$PIPE_OPERATOR" \
    CPAP_RUN_ASSET_URL="file://$TEST_ROOT/capture-installer.sh" ROOT_CAPTURE="$TEST_ROOT/root-capture" \
    sh -s -- help <"$TEST_ROOT/alias-bootstrap.sh" >/dev/null
)
[ "$(cat "$TEST_ROOT/root-capture")" = "$(printf '%s\n' "$PHYSICAL_ROOT" unset)" ] \
  || fail 'stdin bootstrap did not pass the canonical default runtime'
(
  unset CPAP_DEPLOY_ROOT
  CPAC_DEPLOY_ROOT="$PIPE_OPERATOR/runtime" CPAP_ALLOW_NON_ROOT=true CPAP_STAGING_ROOT="$PIPE_OPERATOR" \
    CPAP_RUN_ASSET_URL="file://$TEST_ROOT/capture-installer.sh" ROOT_CAPTURE="$TEST_ROOT/root-capture" \
    sh -s -- help <"$TEST_ROOT/alias-bootstrap.sh" >/dev/null
)
[ "$(cat "$TEST_ROOT/root-capture")" = "$(printf '%s\n' "$PHYSICAL_ROOT" "$PHYSICAL_ROOT")" ] \
  || fail 'legacy runtime input conflicted with the resolved root after bootstrap'
sed "s|/opt/codex-cpa-cluster|$TEST_ROOT/current|g" "$ROOT_DIR/scripts/run.sh" >"$TEST_ROOT/saved-bootstrap.sh"
(
  unset CPAP_DEPLOY_ROOT CPAC_DEPLOY_ROOT
  CPAP_ALLOW_NON_ROOT=true CPAP_STAGING_ROOT="$PIPE_OPERATOR" \
    CPAP_RUN_ASSET_URL="file://$TEST_ROOT/capture-installer.sh" ROOT_CAPTURE="$TEST_ROOT/root-capture" \
    sh -s -- help --config "$SAVED_CONFIG" <"$TEST_ROOT/saved-bootstrap.sh" >/dev/null
)
[ "$(cat "$TEST_ROOT/root-capture")" = "$(printf '%s\n' "$PHYSICAL_ROOT" unset)" ] \
  || fail 'stdin bootstrap did not reuse --config root in an ambiguous layout'
cp "$ROOT_DIR/scripts/run.sh" "$PIPE_OPERATOR/run.sh"
(
  unset CPAP_DEPLOY_ROOT CPAC_DEPLOY_ROOT
  CPAP_ALLOW_NON_ROOT=true CPAP_STAGING_ROOT="$PIPE_OPERATOR" CPAP_CONFIG_FILE="$SAVED_CONFIG" \
    CPAP_RUN_ASSET_URL="file://$TEST_ROOT/capture-installer.sh" \
    sh -s -- help <"$TEST_ROOT/saved-bootstrap.sh" >/dev/null 2>&1
)
cmp -s "$ROOT_DIR/scripts/run.sh" "$PIPE_OPERATOR/run.sh" \
  || fail 'stdin bootstrap removed saved-root support with an older installer'
for invalid_root in "$TEST_ROOT/dangling-runtime" "$TEST_ROOT/loop-a" "$TEST_ROOT/file-runtime"; do
  printf '%s\n' preserved-script >"$PIPE_OPERATOR/run.sh"
  reject env CPAP_ALLOW_NON_ROOT=true CPAP_DEPLOY_ROOT="$invalid_root" CPAP_STAGING_ROOT="$PIPE_OPERATOR" \
    CPAP_RUN_ASSET_URL="file://$TEST_ROOT/capture-installer.sh" \
    sh -s -- help <"$TEST_ROOT/alias-bootstrap.sh"
  [ "$(cat "$PIPE_OPERATOR/run.sh")" = preserved-script ] || fail 'invalid runtime replaced the installer'
done

# Exercise stdin selection itself, with isolated equivalents of the default paths.
sed -e "s|/home/ccpa|$TEST_ROOT/home/ccpa|g" \
  -e "s|/home/cpac|$TEST_ROOT/home/cpac|g" \
  -e "s|/opt/codex-cpa-cluster|$TEST_ROOT/opt/codex-cpa-cluster|g" \
  "$ROOT_DIR/scripts/run.sh" >"$TEST_ROOT/bootstrap.sh"
mkdir -p "$TEST_ROOT/home/cpac/runtime/state" "$TEST_ROOT/home/cpac/runtime/secrets"
printf '%s\n' original-script >"$TEST_ROOT/home/cpac/run.sh"
printf '%s\n' existing-database >"$TEST_ROOT/home/cpac/runtime/state/control-plane.sqlite3"
printf '%s\n' existing-key >"$TEST_ROOT/home/cpac/runtime/secrets/control-plane.key"
(
  unset CPAP_STAGING_ROOT CPAP_DEPLOY_ROOT
  CPAP_ALLOW_NON_ROOT=true CPAP_RUN_ASSET_URL="file://$ROOT_DIR/scripts/run.sh" \
    sh -s -- help <"$TEST_ROOT/bootstrap.sh" >/dev/null
)
[ ! -e "$TEST_ROOT/home/ccpa" ] || fail 'bootstrap created a parallel operator root'
[ "$(cat "$TEST_ROOT/home/cpac/runtime/state/control-plane.sqlite3")" = existing-database ] \
  && [ "$(cat "$TEST_ROOT/home/cpac/runtime/secrets/control-plane.key")" = existing-key ] \
  || fail 'bootstrap changed existing database or master key'
printf '%s\n' preserved-script >"$TEST_ROOT/home/cpac/run.sh"
mkdir -p "$TEST_ROOT/home/ccpa"
if (
  unset CPAP_STAGING_ROOT CPAP_DEPLOY_ROOT
  CPAP_ALLOW_NON_ROOT=true CPAP_RUN_ASSET_URL="file://$ROOT_DIR/scripts/run.sh" \
    sh -s -- help <"$TEST_ROOT/bootstrap.sh"
) >"$TEST_ROOT/conflict.log" 2>&1; then fail 'bootstrap accepted two operator roots'; fi
[ "$(cat "$TEST_ROOT/home/cpac/run.sh")" = preserved-script ] \
  && [ ! -e "$TEST_ROOT/home/ccpa/run.sh" ] || fail 'ambiguous bootstrap replaced an entrypoint'

CONFIG="$TEST_ROOT/config.env"
printf 'CPA_DOMAIN=example.test\nCPAC_INGRESS_MODE=external\n' >"$CONFIG"
[ "$(config_ingress_mode "$CONFIG")" = external ] || fail 'legacy ingress was lost'
printf 'CPAP_INGRESS_MODE=managed\n' >>"$CONFIG"
reject config_ingress_mode "$CONFIG"
printf 'CPA_DOMAIN=example.test\nCPAC_INGRESS_MODE=external\nCPAP_INGRESS_MODE=external\n' >"$CONFIG"
[ "$(config_ingress_mode "$CONFIG")" = external ] || fail 'equal ingress aliases rejected'
printf 'CPAP_INGRESS_MODE=external\n' >>"$CONFIG"
reject config_ingress_mode "$CONFIG"

RELEASE="$TEST_ROOT/release"
mkdir "$RELEASE"
DIGEST=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
cat >"$RELEASE/release-v9.8.7.env" <<RELEASE_ENV
CPAC_RELEASE_VERSION=v9.8.7
CPAC_RELEASE_ARCHIVE=codex-cpa-cluster-v9.8.7.tar.gz
CPAC_CONTROL_IMAGE=registry.example.test/codex-cpa-control:sha256-$DIGEST
CPAC_WEB_IMAGE=registry.example.test/codex-cpa-web:sha256-$DIGEST
CPAC_GATEWAY_IMAGE=registry.example.test/codex-cpa-gateway:sha256-$DIGEST
CPAC_EDGE_IMAGE=registry.example.test/codex-cpa-edge:sha256-$DIGEST
RELEASE_ENV
[ "$(release_value "$RELEASE/release-v9.8.7.env" CPAP_RELEASE_VERSION)" = v9.8.7 ] \
  || fail 'legacy release env was not readable'
[ "$(release_archive_name "$RELEASE/release-v9.8.7.env" v9.8.7)" = codex-cpa-cluster-v9.8.7.tar.gz ] \
  || fail 'legacy release archive was not readable'
cp "$RELEASE/release-v9.8.7.env" "$TEST_ROOT/conflicting.env"
printf 'CPAP_RELEASE_VERSION=v9.8.8\n' >>"$TEST_ROOT/conflicting.env"
reject release_value "$TEST_ROOT/conflicting.env" CPAP_RELEASE_VERSION
printf 'CPAP_RELEASE_ARCHIVE=../escape.tar.gz\n' >"$TEST_ROOT/escape.env"
reject release_archive_name "$TEST_ROOT/escape.env" v9.8.7

# Preserve every existing target setting while updating exactly four image refs.
TARGET="$TEST_ROOT/target.env"
cat >"$TARGET" <<TARGET_ENV
# operator settings must survive a rename and a pinned historical release
CPA_CONTROL_IMAGE=registry.example.test/control:old
CPA_WEB_IMAGE=registry.example.test/web:old
CPA_GATEWAY_IMAGE=registry.example.test/gateway:old
CPA_EDGE_IMAGE=registry.example.test/edge:old
CPA_COMPOSE_PROJECT_NAME=custom-project
CPA_INSTANCE_NAME=custom-instance
CPA_RUNTIME_OWNER=custom-owner
CPA_ACCOUNT_COMPOSE_PROJECT=custom-accounts
CPA_ACCOUNT_INSTANCE_NAME=custom-account-instance
CPA_UPSTREAM_NETWORK=custom-network
CPA_PUBLIC_PORT=28765
CPA_INTERNAL_PORT=28764
CPA_ALLOW_EDGE_RECREATE=false
TARGET_ENV
awk '!/^CPA_(CONTROL|WEB|GATEWAY|EDGE)_IMAGE=/' "$TARGET" >"$TEST_ROOT/settings.before"
write_target_env "$RELEASE/release-v9.8.7.env" v9.8.7 "$TARGET" "$CPAP_DEPLOY_ROOT"
awk '!/^CPA_(CONTROL|WEB|GATEWAY|EDGE)_IMAGE=/' "$TARGET" >"$TEST_ROOT/settings.after"
cmp -s "$TEST_ROOT/settings.before" "$TEST_ROOT/settings.after" || fail 'target settings changed'
[ "$(grep -c "sha256-$DIGEST" "$TARGET")" = 4 ] || fail 'not all image refs were updated'
[ "$(deployment_instance_name "$TARGET")" = custom-instance ] || fail 'custom instance was ignored'
awk '!/^CPA_INSTANCE_NAME=/' "$TARGET" >"$TEST_ROOT/optional-instance.env"
[ "$(deployment_instance_name "$TEST_ROOT/optional-instance.env")" = codex-cpa ] \
  || fail 'missing optional instance lost its established default'
printf 'CPA_INSTANCE_NAME=first\nCPA_INSTANCE_NAME=second\n' >"$TEST_ROOT/duplicate-instance.env"
reject deployment_instance_name "$TEST_ROOT/duplicate-instance.env"
printf 'CPA_INSTANCE_NAME=--unsafe\n' >"$TEST_ROOT/invalid-instance.env"
reject deployment_instance_name "$TEST_ROOT/invalid-instance.env"
[ "$(deployment_upstream_network "$TARGET")" = custom-network ] || fail 'custom upstream network was ignored'
printf 'CPA_UPSTREAM_NETWORK=custom-network\nUNRELATED=$(touch "%s")\n' \
  "$TEST_ROOT/should-not-execute" >"$TEST_ROOT/network.env"
deployment_upstream_network "$TEST_ROOT/network.env" >/dev/null
[ ! -e "$TEST_ROOT/should-not-execute" ] || fail 'network lookup executed unrelated env contents'
printf 'CPA_UPSTREAM_NETWORK=duplicate-network\n' >>"$TEST_ROOT/network.env"
reject deployment_upstream_network "$TEST_ROOT/network.env"
printf 'CPA_UPSTREAM_NETWORK=--unsafe\n' >"$TEST_ROOT/network.env"
reject deployment_upstream_network "$TEST_ROOT/network.env"
PORT_ROOT="$TEST_ROOT/custom-runtime"
mkdir "$PORT_ROOT"
cp "$TARGET" "$PORT_ROOT/target.env"
OPERATOR_PUBLIC_PORT=$(operator_public_port "$PORT_ROOT")
[ "$OPERATOR_PUBLIC_PORT" = 28765 ] || fail 'custom ingress port was reset'
show_external_ingress_contract example.test >"$TEST_ROOT/custom-ingress.txt"
grep -Fq 'proxy_pass http://127.0.0.1:28765;' "$TEST_ROOT/custom-ingress.txt" \
  || fail 'external ingress hint ignored the existing public port'
printf 'CPA_PUBLIC_PORT=18317\n' >>"$PORT_ROOT/target.env"
reject operator_public_port "$PORT_ROOT"
printf 'CPA_PUBLIC_PORT=65536\n' >"$PORT_ROOT/target.env"
reject operator_public_port "$PORT_ROOT"
printf 'CPA_PUBLIC_PORT=0\n' >"$PORT_ROOT/target.env"
reject operator_public_port "$PORT_ROOT"
printf 'CPA_CONTROL_IMAGE=duplicate\n' >>"$TARGET"
cp "$TARGET" "$TEST_ROOT/target.before"
reject write_target_env "$RELEASE/release-v9.8.7.env" v9.8.7 "$TARGET" "$CPAP_DEPLOY_ROOT"
cmp -s "$TARGET" "$TEST_ROOT/target.before" || fail 'invalid target.env was overwritten'

# A pinned old release must not re-execute the old installer with new paths.
SCRIPT_DIRECTORY="$TEST_ROOT/installer"
mkdir "$SCRIPT_DIRECTORY"
SCRIPT_PATH="$SCRIPT_DIRECTORY/run.sh"
cp "$ROOT_DIR/scripts/run.sh" "$SCRIPT_PATH"
cat >"$RELEASE/run.sh" <<'OLD_INSTALLER'
#!/usr/bin/env sh
CPAC_STAGING_ROOT=${CPAC_STAGING_ROOT:-/home/cpac}
echo 'old installer must not run' >&2
exit 91
OLD_INSTALLER
if update_operator_script "$RELEASE/run.sh" >"$TEST_ROOT/update.log"; then
  fail 'historical release downgraded the installer'
fi
cmp -s "$ROOT_DIR/scripts/run.sh" "$SCRIPT_PATH" || fail 'current installer was changed'

# Protocol 1 releases from before runtime alias support must not remove it on
# the next update, even though they already understand the Pool prefix.
cat >"$RELEASE/run.sh" <<'PRE_ALIAS_INSTALLER'
#!/usr/bin/env sh
# CPAP_OPERATOR_PROTOCOL=1
echo 'pre-alias installer must not replace the current entrypoint' >&2
exit 92
PRE_ALIAS_INSTALLER
if update_operator_script "$RELEASE/run.sh" >"$TEST_ROOT/pre-alias-update.log"; then
  fail 'older Pool release removed runtime alias support'
fi
cmp -s "$ROOT_DIR/scripts/run.sh" "$SCRIPT_PATH" || fail 'pre-alias installer replaced the current entrypoint'

printf '%s\n' '# CPAP_RUNTIME_ROOT_ALIAS=1' >>"$RELEASE/run.sh"
if update_operator_script "$RELEASE/run.sh" >"$TEST_ROOT/pre-config-update.log"; then
  fail 'older release removed saved-root support'
fi
cmp -s "$ROOT_DIR/scripts/run.sh" "$SCRIPT_PATH" || fail 'pre-config installer replaced the current entrypoint'

printf '%s\n' immutable-archive >"$RELEASE/codex-cpa-cluster-v9.8.7.tar.gz"
(
  cd "$RELEASE"
  sha256sum codex-cpa-cluster-v9.8.7.tar.gz release-v9.8.7.env run.sh >SHA256SUMS
)
cp "$RELEASE/SHA256SUMS" "$TEST_ROOT/valid-sums"
verify_release "$RELEASE" v9.8.7
printf '%s\n' corrupted >>"$RELEASE/codex-cpa-cluster-v9.8.7.tar.gz"
reject verify_release "$RELEASE" v9.8.7
printf '%s\n' immutable-archive >"$RELEASE/codex-cpa-cluster-v9.8.7.tar.gz"
sed 's/codex-cpa-cluster-v9.8.7.tar.gz/codex-cpa-cluster-v9X8X7XtarXgz/' \
  "$TEST_ROOT/valid-sums" >"$RELEASE/SHA256SUMS"
reject verify_release "$RELEASE" v9.8.7
cat "$TEST_ROOT/valid-sums" "$TEST_ROOT/valid-sums" >"$RELEASE/SHA256SUMS"
reject verify_release "$RELEASE" v9.8.7
cp "$TEST_ROOT/valid-sums" "$RELEASE/SHA256SUMS"
printf 'invalid  run.sh\n' >>"$RELEASE/SHA256SUMS"
reject verify_release "$RELEASE" v9.8.7
cp "$TEST_ROOT/valid-sums" "$RELEASE/SHA256SUMS"
printf '%s\n' corrupted >>"$RELEASE/release-v9.8.7.env"
reject verify_release "$RELEASE" v9.8.7
printf '%s\n' 'Pool rename compatibility tests passed'
