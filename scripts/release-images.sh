#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ACTION=${1:-build}
VERSION=${VERSION:?VERSION 不能为空，例如 v1.0.0}
PLATFORM=${PLATFORM:-linux/amd64}
IMAGE_PREFIXES=${IMAGE_PREFIXES:-}
ALLOW_DIRTY=${ALLOW_DIRTY:-false}
RELEASE_COMPONENTS="control web gateway edge"

case "$VERSION" in
  v[0-9]*.[0-9]*.[0-9]*|[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "VERSION 必须是语义化版本，例如 v1.2.3：$VERSION" >&2; exit 1 ;;
esac
if ! printf '%s' "$VERSION" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$'; then
  echo "VERSION 必须是语义化版本，例如 v1.2.3：$VERSION" >&2
  exit 1
fi
case "$PLATFORM" in
  linux/*) ;;
  *) echo "PLATFORM 必须是单个 Linux 平台，例如 linux/amd64：$PLATFORM" >&2; exit 1 ;;
esac
case "$ALLOW_DIRTY" in true|false) ;; *) echo "ALLOW_DIRTY 只能为 true 或 false" >&2; exit 1 ;; esac
case "$ACTION" in build|publish) ;; *) echo "未知动作：$ACTION" >&2; exit 1 ;; esac
if [ "$ACTION" = publish ] && [ -z "$IMAGE_PREFIXES" ]; then
  echo "发布镜像时 IMAGE_PREFIXES 不能为空" >&2
  exit 1
fi

go run "$ROOT_DIR/cmd/releasectl" privacy --root "$ROOT_DIR"
if [ "$ALLOW_DIRTY" != true ] && [ -n "$(git -C "$ROOT_DIR" status --porcelain)" ]; then
  echo "工作区存在未提交修改，拒绝发布；测试构建可显式设置 ALLOW_DIRTY=true" >&2
  exit 1
fi

REVISION=$(git -C "$ROOT_DIR" rev-parse HEAD)
SAFE_VERSION=$(printf '%s' "$VERSION" | tr '/:' '--')
if [ "$ACTION" = publish ]; then
  TAG_REVISION=$(git -C "$ROOT_DIR" rev-parse "$VERSION^{commit}" 2>/dev/null || true)
  if [ -z "$TAG_REVISION" ] || [ "$TAG_REVISION" != "$REVISION" ]; then
    echo "发布版本必须是指向当前提交的 Git Tag：$VERSION" >&2
    exit 1
  fi
fi

component_digest() {
	go run "$ROOT_DIR/cmd/releasectl" manifest digest \
    --root "$ROOT_DIR" --component "$1"
}

build_component() {
  COMPONENT=$1
  DOCKERFILE=$2
  TARGET=${3:-}
  DIGEST=$(component_digest "$COMPONENT")
  LOCAL_IMAGE="codex-cpa-$COMPONENT:sha256-$DIGEST"
  set -- docker buildx build \
    --platform "$PLATFORM" \
    --load \
    --label "io.codex-cpa.component=$COMPONENT" \
    --label "io.codex-cpa.component-digest=$DIGEST" \
    --label "io.codex-cpa.source-digest=$DIGEST"
  if [ -n "$TARGET" ]; then
    set -- "$@" --target "$TARGET"
  fi
  set -- "$@" \
    -t "$LOCAL_IMAGE" \
    -f "$ROOT_DIR/$DOCKERFILE" \
    "$ROOT_DIR"
  "$@"
}

build_component control Dockerfile control
build_component web Dockerfile web
build_component gateway Dockerfile gateway
build_component edge Dockerfile edge
docker buildx build \
  --platform "$PLATFORM" \
  --load \
  --target release \
  --build-arg "RELEASE_VERSION=$VERSION" \
  --build-arg "RELEASE_REVISION=$REVISION" \
  -t "codex-cpa-release:build-$SAFE_VERSION" \
  -f "$ROOT_DIR/Dockerfile" \
  "$ROOT_DIR"

[ "$ACTION" = publish ] || exit 0

image_label() {
  docker image inspect --format "{{index .Config.Labels \"$2\"}}" "$1" 2>/dev/null || true
}

validate_component_image() {
  IMAGE=$1
  COMPONENT=$2
  DIGEST=$3
  if [ "$(image_label "$IMAGE" io.codex-cpa.component)" != "$COMPONENT" ] \
    || [ "$(image_label "$IMAGE" io.codex-cpa.component-digest)" != "$DIGEST" ] \
    || [ "$(image_label "$IMAGE" io.codex-cpa.source-digest)" != "$DIGEST" ]; then
    echo "已存在的镜像与组件指纹不匹配：$IMAGE" >&2
    exit 1
  fi
}

validate_release_image() {
  IMAGE=$1
  if [ "$(image_label "$IMAGE" io.codex-cpa.component)" != release ] \
    || [ "$(image_label "$IMAGE" org.opencontainers.image.version)" != "$VERSION" ] \
    || [ "$(image_label "$IMAGE" org.opencontainers.image.revision)" != "$REVISION" ]; then
    echo "已存在的发布元数据与版本或 revision 不匹配：$IMAGE" >&2
    exit 1
  fi
}

# 先验证所有已存在的版本标签。完全一致的标签允许跳过，以便多 Registry
# 发布在网络中断后安全续传；任何不一致仍在写入新标签前终止。
for PREFIX in $IMAGE_PREFIXES; do
  if ! printf '%s' "$PREFIX" | grep -Eq '^[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9._/-]+$'; then
    echo "镜像前缀无效：$PREFIX" >&2
    exit 1
  fi
  for COMPONENT in $RELEASE_COMPONENTS; do
    DIGEST=$(component_digest "$COMPONENT")
    CONTENT_IMAGE="$PREFIX/codex-cpa-$COMPONENT:sha256-$DIGEST"
    VERSION_IMAGE="$PREFIX/codex-cpa-$COMPONENT:$VERSION"
    if docker manifest inspect "$CONTENT_IMAGE" >/dev/null 2>&1; then
      docker pull "$CONTENT_IMAGE"
      validate_component_image "$CONTENT_IMAGE" "$COMPONENT" "$DIGEST"
    fi
    if docker manifest inspect "$VERSION_IMAGE" >/dev/null 2>&1; then
      docker pull "$VERSION_IMAGE"
      validate_component_image "$VERSION_IMAGE" "$COMPONENT" "$DIGEST"
    fi
  done
  RELEASE_VERSION_IMAGE="$PREFIX/codex-cpa-release:$VERSION"
  if docker manifest inspect "$RELEASE_VERSION_IMAGE" >/dev/null 2>&1; then
    docker pull "$RELEASE_VERSION_IMAGE"
    validate_release_image "$RELEASE_VERSION_IMAGE"
  fi
done

for PREFIX in $IMAGE_PREFIXES; do
  for COMPONENT in $RELEASE_COMPONENTS; do
    DIGEST=$(component_digest "$COMPONENT")
    LOCAL_IMAGE="codex-cpa-$COMPONENT:sha256-$DIGEST"
    CONTENT_IMAGE="$PREFIX/codex-cpa-$COMPONENT:sha256-$DIGEST"
    VERSION_IMAGE="$PREFIX/codex-cpa-$COMPONENT:$VERSION"
    VERSION_EXISTS=false
    docker manifest inspect "$VERSION_IMAGE" >/dev/null 2>&1 && VERSION_EXISTS=true
    if docker manifest inspect "$CONTENT_IMAGE" >/dev/null 2>&1; then
      docker pull "$CONTENT_IMAGE"
      validate_component_image "$CONTENT_IMAGE" "$COMPONENT" "$DIGEST"
    elif [ "$VERSION_EXISTS" = true ]; then
      docker tag "$VERSION_IMAGE" "$CONTENT_IMAGE"
      docker push "$CONTENT_IMAGE"
    else
      docker tag "$LOCAL_IMAGE" "$CONTENT_IMAGE"
      docker push "$CONTENT_IMAGE"
    fi
    if [ "$VERSION_EXISTS" = true ]; then
      echo "版本标签已存在且验证一致，跳过：$VERSION_IMAGE"
    else
      docker tag "$CONTENT_IMAGE" "$VERSION_IMAGE"
      docker push "$VERSION_IMAGE"
    fi
  done
  RELEASE_VERSION_IMAGE="$PREFIX/codex-cpa-release:$VERSION"
  if docker manifest inspect "$RELEASE_VERSION_IMAGE" >/dev/null 2>&1; then
    echo "版本标签已存在且验证一致，跳过：$RELEASE_VERSION_IMAGE"
  else
    docker tag "codex-cpa-release:build-$SAFE_VERSION" "$RELEASE_VERSION_IMAGE"
    docker push "$RELEASE_VERSION_IMAGE"
  fi
done

# 所有 Registry 的不可变版本标签均已写入或验证后，才移动 latest，避免
# Admin 提醒到半成品发布。
for PREFIX in $IMAGE_PREFIXES; do
  for COMPONENT in $RELEASE_COMPONENTS; do
    VERSION_IMAGE="$PREFIX/codex-cpa-$COMPONENT:$VERSION"
    if ! docker image inspect "$VERSION_IMAGE" >/dev/null 2>&1; then
      docker pull "$VERSION_IMAGE"
    fi
    DIGEST=$(component_digest "$COMPONENT")
    validate_component_image "$VERSION_IMAGE" "$COMPONENT" "$DIGEST"
    docker tag "$VERSION_IMAGE" "$PREFIX/codex-cpa-$COMPONENT:latest"
    docker push "$PREFIX/codex-cpa-$COMPONENT:latest"
  done
  RELEASE_VERSION_IMAGE="$PREFIX/codex-cpa-release:$VERSION"
  if ! docker image inspect "$RELEASE_VERSION_IMAGE" >/dev/null 2>&1; then
    docker pull "$RELEASE_VERSION_IMAGE"
  fi
  validate_release_image "$RELEASE_VERSION_IMAGE"
  docker tag "$RELEASE_VERSION_IMAGE" "$PREFIX/codex-cpa-release:latest"
  docker push "$PREFIX/codex-cpa-release:latest"
done

echo "镜像发布完成：version=$VERSION revision=$REVISION prefixes=$IMAGE_PREFIXES"
