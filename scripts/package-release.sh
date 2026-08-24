#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUTPUT=${1:-"$ROOT_DIR/dist/codex-cpa-cluster.tar.gz"}
MANIFEST_FILE="$ROOT_DIR/release-manifest.json"
MANIFEST_CREATED=false

cleanup_manifest_file() {
  if [ "$MANIFEST_CREATED" = true ] && [ -f "$MANIFEST_FILE" ]; then
    python3 - "$MANIFEST_FILE" <<'PY'
import sys
from pathlib import Path

path = Path(sys.argv[1]).resolve()
if path.name != "release-manifest.json":
    raise RuntimeError("refusing to remove unexpected manifest file")
path.unlink()
PY
  fi
}
trap cleanup_manifest_file EXIT HUP INT TERM

case "$OUTPUT" in
  /*) ;;
  *) OUTPUT="$ROOT_DIR/$OUTPUT" ;;
esac

mkdir -p "$(dirname -- "$OUTPUT")"
if [ -e "$MANIFEST_FILE" ]; then
  echo "拒绝覆盖已有发布清单：$MANIFEST_FILE" >&2
  exit 1
fi
python3 "$ROOT_DIR/scripts/release_manifest.py" create \
  --root "$ROOT_DIR" \
  --output "$MANIFEST_FILE"
MANIFEST_CREATED=true

COPYFILE_DISABLE=1 tar --no-xattrs \
  --exclude='*/__pycache__' --exclude='*.pyc' \
  --exclude='*/node_modules' --exclude='frontend/dist' \
  -czf "$OUTPUT" -C "$ROOT_DIR" \
  .dockerignore \
  .env.example \
  compose.env.example \
  codex-cpa \
  Makefile \
  CHANGELOG.md \
  CODE_OF_CONDUCT.md \
  CONTRIBUTING.md \
  LICENSE \
  README.md \
  SECURITY.md \
  requirements.txt \
  config \
  docs \
  docker-compose.yml \
  docker-compose.v1-compare.yml \
  docker-compose.v2.yml \
  docker-compose.v2-test.yml \
  v1-compare.env.example \
  v2-compose.env.example \
  admin \
  api \
  cmd \
  dashboard \
  edge \
  gateway/Dockerfile \
  gateway/gateway_state.lua \
  gateway/nginx.conf \
  gateway/request_gate.lua \
  frontend \
  go.mod \
  go.sum \
  internal \
  portal \
  release \
  web \
  scripts \
  testdata/v2 \
  tools/openapi \
  v2 \
  release-manifest.json

python3 - "$OUTPUT" <<'PY'
import sys
import tarfile
from pathlib import PurePosixPath

with tarfile.open(sys.argv[1], "r:gz") as archive:
    members = archive.getmembers()
    apple_double = sorted(
        member.name
        for member in members
        if any(part.startswith("._") for part in PurePosixPath(member.name).parts)
    )
    apple_xattrs = sorted(
        "{}:{}".format(member.name, key)
        for member in members
        for key in member.pax_headers
        if key.startswith("LIBARCHIVE.xattr.com.apple.")
    )
if apple_double or apple_xattrs:
    raise SystemExit("release archive contains Apple metadata")
PY

printf 'release=%s\n' "$OUTPUT"
