#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/cpa-removed-runtime-test.XXXXXX")
trap 'rm -rf -- "$TEST_ROOT"' EXIT HUP INT TERM
# Exercise the exact expression used by the quality gate, without running its
# already validated build and application stages.
sed -n '/^FORBIDDEN_RUNTIME=/{p;}' "$ROOT_DIR/scripts/verify.sh" >"$TEST_ROOT/pattern.sh"
. "$TEST_ROOT/pattern.sh"
: "${FORBIDDEN_RUNTIME:?quality gate pattern was not found}"
INTERPRETER=$(printf 'py%s' thon)
INSTALL_COMMAND=$(printf 'p%s install' ip)
WORKFLOW_ACTION=$(printf 'set%s-%s' up "$INTERPRETER")
TEST_RUNNER=$(printf 'py%s' test)
TEST_MODULE=$(printf 'unit%s' test)

assert_allowed() {
  printf '%s\n' "$1" >"$TEST_ROOT/input.md"
  if rg -n "$FORBIDDEN_RUNTIME" "$TEST_ROOT/input.md" >/dev/null; then
    echo 'runtime gate rejected a historical document filename' >&2
    exit 1
  fi
}
assert_rejected() {
  printf '%s\n' "$1" >"$TEST_ROOT/input.md"
  if ! rg -n "$FORBIDDEN_RUNTIME" "$TEST_ROOT/input.md" >/dev/null; then
    echo 'runtime gate accepted an actual removed command or tool' >&2
    exit 1
  fi
}

assert_allowed "[保留数据迁移方案]($INTERPRETER-to-go-migration.md)"
assert_allowed "[历史实现](../docs/$INTERPRETER-to-go-migration.md#受控切换)"
assert_allowed "[解释器说明]($INTERPRETER.md)"
assert_allowed "[旧版说明](${INTERPRETER}3-notes.md)"
assert_rejected "$INTERPRETER -V"
assert_rejected "${INTERPRETER}3 -m example"
assert_rejected "sudo /usr/bin/${INTERPRETER}3 script"
assert_rejected "env $INTERPRETER script"
assert_rejected "result=\$($INTERPRETER -V)"
assert_rejected "运行 \`$INTERPRETER -m example\`。"
assert_rejected "运行 \`${INTERPRETER}3\`。"
assert_rejected "$(printf '\140\140\140sh\n%s -V\n\140\140\140' "${INTERPRETER}3")"
assert_rejected "$INSTALL_COMMAND example"
assert_rejected "uses: actions/$WORKFLOW_ACTION@v1"
assert_rejected "$TEST_RUNNER -q"
assert_rejected "$INTERPRETER -m $TEST_MODULE"
assert_rejected "$TEST_MODULE"
# Exercise the exact deployment namespace gate as well, with only the two
# existing migration summaries eligible for a historical-label exception.
sed -n '/^FORBIDDEN_DEPLOYMENT_NAMESPACE=/,/^DOCKERFILE_COUNT=/{ /^DOCKERFILE_COUNT=/!p; }' \
  "$ROOT_DIR/scripts/verify.sh" >"$TEST_ROOT/namespace.sh"
FIXTURE_ROOT="$TEST_ROOT/source"
mkdir -p "$FIXTURE_ROOT/docs"
MIGRATION_TITLE=$(sed -n '1p' "$ROOT_DIR/docs/python-to-go-migration.md")
MIGRATION_INTRO=$(sed -n '3p' "$ROOT_DIR/docs/upgrade.md")
check_namespace() { ROOT_DIR="$FIXTURE_ROOT" sh -eu "$TEST_ROOT/namespace.sh"; }
printf '%s\n' "$MIGRATION_TITLE" >"$FIXTURE_ROOT/docs/python-to-go-migration.md"
printf '%s\n' "$MIGRATION_INTRO" >"$FIXTURE_ROOT/docs/upgrade.md"
check_namespace
LEGACY_VARIABLE='V''2_ROOT=/tmp/runtime'
LEGACY_SERVICE='go-''v2'
LEGACY_COMPOSE='docker-compose.v''2-test.yml'
for prohibited in "$LEGACY_VARIABLE" "make $LEGACY_SERVICE" "docker compose -f $LEGACY_COMPOSE up"; do
  for fixture in python-to-go-migration.md upgrade.md; do
    case "$fixture" in
      python-to-go-migration.md) preserved_line=$MIGRATION_TITLE ;;
      upgrade.md) preserved_line=$MIGRATION_INTRO ;;
    esac
    printf '%s %s\n' "$preserved_line" "$prohibited" >"$FIXTURE_ROOT/docs/$fixture"
    if check_namespace >/dev/null 2>&1; then
      echo 'historical namespace exception hid another forbidden token on the same line' >&2
      exit 1
    fi
    printf '%s\n\n\140\140\140sh\n%s\n\140\140\140\n' \
      "$preserved_line" "$prohibited" >"$FIXTURE_ROOT/docs/$fixture"
    if check_namespace >/dev/null 2>&1; then
      echo 'historical namespace exception skipped a forbidden deployment command in the document' >&2
      exit 1
    fi
    printf '%s\n' "$preserved_line" >"$FIXTURE_ROOT/docs/$fixture"
  done
done
printf '%s\n' "$MIGRATION_TITLE" >"$FIXTURE_ROOT/docs/new-deployment.md"
if check_namespace >/dev/null 2>&1; then
  echo 'historical namespace exception leaked into another document' >&2
  exit 1
fi
printf '%s\n' 'Removed-runtime command and historical namespace tests passed'
