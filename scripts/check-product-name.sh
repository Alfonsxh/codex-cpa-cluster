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
# Narrow compatibility exceptions: immutable hosting identity, old persisted
# values/installer inputs and tests that explicitly prove their migration.
awk '
  {
    path=$0; sub(/:[0-9]+:.*/, "", path); sub(/^\.\//, "", path)
    line=$0; sub(/^[^:]+:[0-9]+:/, "", line)
    # Only live HTTPS hosting URLs are external identities. In particular,
    # github.com module/import paths must still fail the product-name gate.
    gsub(/https:\/\/(github\.com\/|api\.github\.com\/repos\/)Alfonsxh\/codex-cpa-cluster/, "", line)
    gsub(/https:\/\/img\.shields\.io\/github\/(v\/release|go-mod\/go-version|license)\/Alfonsxh\/codex-cpa-cluster/, "", line)
    if ((path == "scripts/build.mk" && line ~ /^GH_REPO \?=/) ||
        (path == "scripts/local-release.sh" && line ~ /^GH_REPO=/) ||
        (path == "scripts/run.sh" && line ~ /^DEFAULT_REPOSITORY=/) ||
        (path == "README.md" && line ~ /GitHub 仓库与发行下载继续沿用/) ||
        (path == "README.en.md" && line ~ /GitHub hosting and release downloads continue to use/) ||
        (path == "docs/deployment.md" && line ~ /GitHub 发行源仍为/)) {
      gsub(/Alfonsxh\/codex-cpa-cluster/, "", line)
    }
    if (path == "scripts/check-product-name.sh") next
    if (path == "internal/admin/branding_compatibility_test.go" || path == "scripts/test-run-compat.sh") next
    if (path == "internal/admin/general_settings.go" && line ~ /legacyProductName = "Codex CPA Cluster"/) next
    if (path ~ /^docs\/(deployment|upgrade|getting-started|backup-and-restore)\.md$/) {
      gsub(/\/home\/cpac|\/etc\/cpac|CPAC_\*/, "", line)
    }
    if (path == "scripts/run.sh") {
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
         line ~ /^CPAC_STAGING_ROOT=/)) next
    if (path == "scripts/test-run.sh" && line ~ /^for removed in .*scripts\/cpac/) next
    if (path == ".github/workflows/ci.yml" && line ~ /tar -tzf .*scripts\/\(cpac\|install-cpac/) next
    if (tolower(line) ~ /codex[ -]cpa[ -]cluster|(^|[^a-z])cpac([^a-z]|$)/) print $0
  }
' "$RESULTS" >"$RESULTS.filtered"
if [ -s "$RESULTS.filtered" ]; then
  cat "$RESULTS.filtered" >&2
  echo '发现未明确归类的旧产品名称；仅历史输入兼容与现有发行源可以保留' >&2
  exit 1
fi
printf '%s\n' 'Pool product-name gate passed'
