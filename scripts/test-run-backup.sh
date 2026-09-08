#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d /tmp/cpap-backup-contract.XXXXXX)
trap 'rm -rf -- "$TEST_ROOT"' EXIT HUP INT TERM
CPAP_TEST_SQLITE=$(command -v sqlite3)
CPAP_TEST_WRITER="$TEST_ROOT/write-during-backup.sh"
export CPAP_TEST_SQLITE CPAP_TEST_WRITER
mkdir -p "$TEST_ROOT/bin" "$TEST_ROOT/runtime/state"
for database in control-plane.sqlite3 usage.sqlite3; do
  "$CPAP_TEST_SQLITE" "$TEST_ROOT/runtime/state/$database" \
    'PRAGMA journal_mode=WAL; CREATE TABLE marker(value); INSERT INTO marker VALUES(1);' >/dev/null
done

# Commit from a second connection immediately before the actual Backup API
# call. The backup must retain the earlier read view while this writer succeeds.
cat >"$CPAP_TEST_WRITER" <<'WRITER'
#!/usr/bin/env sh
set -eu
"$CPAP_TEST_SQLITE" -bail "$CPAP_TEST_SOURCE" 'UPDATE marker SET value = 2;'
WRITER
cat >"$TEST_ROOT/bin/sqlite3" <<'SQLITE'
#!/usr/bin/env sh
set -eu
if [ "$1" = -readonly ]; then
  CPAP_TEST_SOURCE=$3
  export CPAP_TEST_SOURCE
  input=$(mktemp /tmp/cpap-backup-sql.XXXXXX)
  trap 'rm -f -- "$input"' EXIT HUP INT TERM
  awk '/^\.backup / { print ".shell \"" ENVIRON["CPAP_TEST_WRITER"] "\"" } { print }' >"$input"
  "$CPAP_TEST_SQLITE" "$@" <"$input"
else
  case "${2:-}" in
    .backup*) CPAP_TEST_SOURCE=$1; export CPAP_TEST_SOURCE; "$CPAP_TEST_WRITER" ;;
  esac
  "$CPAP_TEST_SQLITE" "$@"
fi
SQLITE
chmod 0755 "$TEST_ROOT/bin/sqlite3" "$CPAP_TEST_WRITER"

# Exercise the production function without executing installer dispatch.
sed -n '/^backup_target() {/,/^claim_admin_key() {/p' "$ROOT_DIR/scripts/run.sh" \
  | sed '$d' >"$TEST_ROOT/backup-function.sh"
. "$TEST_ROOT/backup-function.sh"
die() { printf '%s\n' "$*" >&2; exit 1; }
DEFAULT_BACKUP_ROOT="$TEST_ROOT/backups"
PATH="$TEST_ROOT/bin:$PATH"
export PATH
printf '%s\n' fixture >"$TEST_ROOT/runtime/target.env"
backup=$(backup_target "$TEST_ROOT/runtime")
mkdir "$TEST_ROOT/extracted"
tar -xzf "$backup" -C "$TEST_ROOT/extracted"
for database in control-plane.sqlite3 usage.sqlite3; do
  [ "$(sqlite3 "$TEST_ROOT/runtime/state/$database" 'SELECT value FROM marker;')" = 2 ] \
    || die "concurrent writer did not commit: $database"
  [ "$(sqlite3 "$TEST_ROOT/extracted/state/$database" 'SELECT value FROM marker;')" = 1 ] \
    || die "backup restarted onto the concurrent write: $database"
done
printf '%s\n' 'run.sh backup keeps a consistent read snapshot while concurrent writers commit'
