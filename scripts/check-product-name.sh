#!/usr/bin/env sh
set -eu

ROOT_DIR=${1:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}
ROOT_DIR=$(CDPATH= cd -- "$ROOT_DIR" && pwd)
RESULTS=$(mktemp "${TMPDIR:-/tmp}/cpa-pool-branding.XXXXXX")
trap 'rm -f -- "$RESULTS" "$RESULTS.filtered"' EXIT HUP INT TERM
FORBIDDEN='codex[ -]cpa[ -]clu''ster|\bCPA''C\b|CPA''C_'

scan() {
  rg --hidden \
    --glob '!.git/**' --glob '!.harness/**' --glob '!AGENTS.md' \
    --glob '!**/node_modules/**' --glob '!dist/**' --glob '!frontend/dist/**' \
    --glob '!frontend/coverage/**' --glob '!frontend/playwright-report/**' \
    --glob '!frontend/test-results/**' \
    "$@" .
}
cd "$ROOT_DIR"
if scan --files | rg -i "$FORBIDDEN" >"$RESULTS"; then
  cat "$RESULTS" >&2
  echo '发现旧产品名称的文件名' >&2
  exit 1
fi
scan -n -i "$FORBIDDEN" >"$RESULTS" || status=$?
[ "${status:-0}" -le 1 ] || exit "$status"
# Narrow compatibility exceptions: old persisted values/installer inputs and
# tests that explicitly prove their migration. Current hosting uses the Pool name.
awk '
  {
    path=$0; sub(/:[0-9]+:.*/, "", path); sub(/^\.\//, "", path)
    line=$0; sub(/^[^:]+:[0-9]+:/, "", line)
    if (path == "scripts/check-product-name.sh") next
    if (path == "internal/admin/branding_compatibility_test.go" || path == "scripts/test-run-compat.sh") next
    if (path == "internal/admin/general_settings.go" && line ~ /legacyProductName = "Codex CPA Cluster"/) next
    if (path ~ /^docs\/(deployment|upgrade|getting-started|backup-and-restore)\.md$/) {
      gsub(/\/home\/cpac|\/etc\/cpac|CPAC_\*/, "", line)
    }
    if (path == "scripts/run.sh") {
      if (line == "  if [ \"${CPAC_DEPLOY_ROOT+set}\" = set ]; then" ||
          line == "    CPAC_DEPLOY_ROOT=$CPAP_DEPLOY_ROOT" ||
          line == "    export CPAC_DEPLOY_ROOT") next
      if (line ~ /operator_legacy_(set|value)=/ || line ~ /resolve_existing_root / ||
          line ~ /^LEGACY_CONFIG_FILE=/ || line ~ /\$1 == "CPAC_INGRESS_MODE"/ ||
          line ~ /legacy_release_key=/ || line ~ /"codex-cpa-cluster-\$2.tar.gz"/ ||
          line ~ /grep -[Fqx]+ .*Managed by CPAC (run|deploy)\.sh/ ||
          line ~ /grep -q .*CPAC_/ || line ~ /grep -Fq .*DEFAULT_REPOSITORY=/ ||
          line ~ /rm -f -- .*\/\.cpac-initialized/) next
    }
    if (path == "scripts/test-run-runtime.sh" &&
        (line ~ /^DEFAULT_REPOSITORY=/ || line ~ /Managed by CPAC (run|deploy)\.sh/ ||
         line ~ /codex-cpa-cluster-\$RELEASE_VERSION/ || line ~ /s\/\^CPAP_\/CPAC_\// ||
         line == "CPAC_DEPLOY_ROOT=\"$OPERATOR_ROOT/runtime\" run_operator_deploy >\"$OPERATOR_ROOT/alias-upgrade.log\"" ||
         line ~ /^CPAC_STAGING_ROOT=/)) next
    if (path == "scripts/test-run.sh" && line ~ /^for removed in .*scripts\/cpac/) next
    if (path == ".github/workflows/ci.yml" && line ~ /tar -tzf .*scripts\/\(cpac\|install-cpac/) next
    if (tolower(line) ~ /codex[ -]cpa[ -]cluster|(^|[^a-z])cpac([^a-z]|$)/) print $0
  }
' "$RESULTS" >"$RESULTS.filtered"
if [ -s "$RESULTS.filtered" ]; then
  cat "$RESULTS.filtered" >&2
  echo '发现未明确归类的旧产品名称；仅历史输入兼容可以保留' >&2
  exit 1
fi
printf '%s\n' 'Pool product-name gate passed'
