#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ACTION=${1:-publish}
VERSION=${VERSION:?VERSION 不能为空，例如 v1.1.0}
IMAGE_PREFIX=${IMAGE_PREFIX:?IMAGE_PREFIX 不能为空，例如 ghcr.io/owner}
PLATFORM=${PLATFORM:-linux/amd64}
GH_REPO=${GH_REPO:-Alfonsxh/codex-cpa-cluster}
GIT_REMOTE=${GIT_REMOTE:-origin}
RELEASE_BRANCH=${RELEASE_BRANCH:-main}
DIST_DIR=${DIST_DIR:-$ROOT_DIR/dist}

case "$DIST_DIR" in
  /*) ;;
  *) DIST_DIR="$ROOT_DIR/$DIST_DIR" ;;
esac

case "$ACTION" in
  check|publish) ;;
  *) echo "动作必须是 check 或 publish：$ACTION" >&2; exit 1 ;;
esac
if ! printf '%s' "$VERSION" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$'; then
  echo "VERSION 必须是语义化版本：$VERSION" >&2
  exit 1
fi
if ! printf '%s' "$IMAGE_PREFIX" | grep -Eq '^[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9._/-]+$'; then
  echo "IMAGE_PREFIX 无效：$IMAGE_PREFIX" >&2
  exit 1
fi
if ! printf '%s' "$GH_REPO" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'; then
  echo "GH_REPO 必须是 owner/repository：$GH_REPO" >&2
  exit 1
fi

for command in git docker gh python3 make; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "缺少发布依赖：$command" >&2
    exit 1
  fi
done
docker buildx version >/dev/null
gh auth status --hostname github.com >/dev/null

CURRENT_BRANCH=$(git -C "$ROOT_DIR" symbolic-ref --quiet --short HEAD || true)
if [ "$CURRENT_BRANCH" != "$RELEASE_BRANCH" ]; then
  echo "只能从 $RELEASE_BRANCH 分支发布，当前分支：${CURRENT_BRANCH:-detached HEAD}" >&2
  exit 1
fi
if [ -n "$(git -C "$ROOT_DIR" status --porcelain --untracked-files=normal)" ]; then
  echo "工作区存在未提交修改，拒绝发布" >&2
  exit 1
fi

# 发布只接受已经推送到主分支的提交，避免 GitHub Release 指向本地独有 revision。
git -C "$ROOT_DIR" fetch "$GIT_REMOTE" "$RELEASE_BRANCH" --tags
REVISION=$(git -C "$ROOT_DIR" rev-parse HEAD)
REMOTE_BRANCH_REVISION=$(git -C "$ROOT_DIR" rev-parse "$GIT_REMOTE/$RELEASE_BRANCH")
if [ "$REVISION" != "$REMOTE_BRANCH_REVISION" ]; then
  echo "当前提交尚未与 $GIT_REMOTE/$RELEASE_BRANCH 同步，拒绝发布" >&2
  exit 1
fi

LOCAL_TAG_REVISION=$(git -C "$ROOT_DIR" rev-parse "$VERSION^{commit}" 2>/dev/null || true)
if [ -n "$LOCAL_TAG_REVISION" ] && [ "$LOCAL_TAG_REVISION" != "$REVISION" ]; then
  echo "本地 Tag 已指向其他提交：$VERSION" >&2
  exit 1
fi

REMOTE_TAG_REVISION=$(
  git -C "$ROOT_DIR" ls-remote "$GIT_REMOTE" \
    "refs/tags/$VERSION" "refs/tags/$VERSION^{}" \
    | awk -v direct="refs/tags/$VERSION" -v peeled="refs/tags/$VERSION^{}" '
        $2 == direct { direct_revision = $1 }
        $2 == peeled { peeled_revision = $1 }
        END { print peeled_revision ? peeled_revision : direct_revision }
      '
)
if [ -n "$REMOTE_TAG_REVISION" ] && [ "$REMOTE_TAG_REVISION" != "$REVISION" ]; then
  echo "远端 Tag 已指向其他提交：$VERSION" >&2
  exit 1
fi

RELEASE_STATE=missing
if RELEASE_DRAFT=$(gh release view "$VERSION" --repo "$GH_REPO" --json isDraft --jq .isDraft 2>/dev/null); then
  if [ "$RELEASE_DRAFT" = true ]; then
    RELEASE_STATE=draft
  else
    echo "GitHub Release 已发布，拒绝覆盖：$VERSION" >&2
    exit 1
  fi
fi

printf 'version=%s\nrevision=%s\nimage_prefix=%s\ngithub_repo=%s\nrelease_state=%s\n' \
  "$VERSION" "$REVISION" "$IMAGE_PREFIX" "$GH_REPO" "$RELEASE_STATE"
if [ "$ACTION" = check ]; then
  printf '%s\n' '发布预检通过；未创建 Tag、镜像或 GitHub Release'
  exit 0
fi

make -C "$ROOT_DIR" verify

if [ -z "$LOCAL_TAG_REVISION" ]; then
  # Tag 先保留在本地；镜像和发布包全部完成后才推送到 GitHub。
  git -C "$ROOT_DIR" tag -a "$VERSION" -m "Release $VERSION"
fi

VERSION="$VERSION" \
PLATFORM="$PLATFORM" \
IMAGE_PREFIXES="$IMAGE_PREFIX" \
  sh "$ROOT_DIR/scripts/release-images.sh" publish

mkdir -p "$DIST_DIR"
ARCHIVE="$DIST_DIR/codex-cpa-cluster-$VERSION.tar.gz"
RELEASE_DESCRIPTOR="$DIST_DIR/release-$VERSION.json"
INSTALLER="$DIST_DIR/codex-cpa"
CHECKSUMS="$DIST_DIR/SHA256SUMS"
sh "$ROOT_DIR/scripts/package-release.sh" "$ARCHIVE"
install -m 0755 "$ROOT_DIR/codex-cpa" "$INSTALLER"
python3 "$ROOT_DIR/scripts/release_manifest.py" release \
  --root "$ROOT_DIR" \
  --output "$RELEASE_DESCRIPTOR" \
  --release-version "$VERSION" \
  --revision "$REVISION" \
  --image-prefix "$IMAGE_PREFIX" \
  --archive-name "$(basename -- "$ARCHIVE")"

# 使用 Python 生成跨 macOS/Linux 一致的 SHA-256 文件格式。
python3 - "$ARCHIVE" "$RELEASE_DESCRIPTOR" "$INSTALLER" "$CHECKSUMS" <<'PY'
import hashlib
import os
import sys
from pathlib import Path

artifacts = [Path(item) for item in sys.argv[1:-1]]
output = Path(sys.argv[-1])
lines = []
for artifact in artifacts:
    digest = hashlib.sha256(artifact.read_bytes()).hexdigest()
    lines.append("{}  {}".format(digest, artifact.name))
temporary = output.with_name(".{}.{}.tmp".format(output.name, os.getpid()))
temporary.write_text("\n".join(lines) + "\n", encoding="utf-8")
os.replace(temporary, output)
PY

if [ -z "$REMOTE_TAG_REVISION" ]; then
  git -C "$ROOT_DIR" push "$GIT_REMOTE" "refs/tags/$VERSION"
fi

if [ "$RELEASE_STATE" = missing ]; then
  gh release create "$VERSION" \
    "$ARCHIVE#Deployment archive" \
    "$RELEASE_DESCRIPTOR#Release descriptor" \
    "$INSTALLER#Installer and upgrader" \
    "$CHECKSUMS#SHA-256 checksums" \
    --repo "$GH_REPO" \
    --verify-tag \
    --draft \
    --generate-notes \
    --title "$VERSION"
else
  gh release upload "$VERSION" \
    "$ARCHIVE#Deployment archive" \
    "$RELEASE_DESCRIPTOR#Release descriptor" \
    "$INSTALLER#Installer and upgrader" \
    "$CHECKSUMS#SHA-256 checksums" \
    --repo "$GH_REPO" \
    --clobber
fi

# GitHub Release 最后公开，确保用户看见版本时全部附件已经可用。
gh release edit "$VERSION" --repo "$GH_REPO" --draft=false --latest
printf '发布完成：https://github.com/%s/releases/tag/%s\n' "$GH_REPO" "$VERSION"
