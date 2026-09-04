#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ACTION=${1:-build}
VERSION=${VERSION:?VERSION 不能为空，例如 v1.0.0}
PLATFORM=${PLATFORM:-linux/amd64}
IMAGE_PREFIXES=${IMAGE_PREFIXES:-}
ALLOW_DIRTY=${ALLOW_DIRTY:-false}
RELEASECTL=${RELEASECTL:-}
RELEASE_COMPONENTS="control web gateway edge"
TAB=$(printf '\t')

case "$VERSION" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "VERSION 必须是带 v 前缀的语义化 Tag，例如 v1.2.3：$VERSION" >&2; exit 1 ;;
esac
if ! printf '%s' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$'; then
  echo "VERSION 必须是带 v 前缀的语义化 Tag，例如 v1.2.3：$VERSION" >&2
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
if [ ! -f "$ROOT_DIR/docker-bake.hcl" ] || [ -L "$ROOT_DIR/docker-bake.hcl" ]; then
  echo "正式镜像构建配置缺失或不是普通文件：$ROOT_DIR/docker-bake.hcl" >&2
  exit 1
fi

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/cpac-release-images.XXXXXX")
cleanup() {
  rm -rf -- "$WORK_DIR"
}
trap cleanup EXIT HUP INT TERM

if [ -n "$RELEASECTL" ]; then
  RELEASECTL_BIN=$RELEASECTL
else
  RELEASECTL_BIN="$WORK_DIR/cpa-releasectl"
  go build -o "$RELEASECTL_BIN" "$ROOT_DIR/cmd/releasectl"
fi
if [ ! -x "$RELEASECTL_BIN" ]; then
  echo "发布工具不可执行：$RELEASECTL_BIN" >&2
  exit 1
fi

"$RELEASECTL_BIN" privacy --root "$ROOT_DIR"
if [ "$ALLOW_DIRTY" != true ] && [ -n "$(git -C "$ROOT_DIR" status --porcelain)" ]; then
  echo "工作区存在未提交修改，拒绝发布；测试构建可显式设置 ALLOW_DIRTY=true" >&2
  exit 1
fi

REVISION=$(git -C "$ROOT_DIR" rev-parse HEAD)
if [ "$ACTION" = publish ]; then
  TAG_REVISION=$(git -C "$ROOT_DIR" rev-parse "$VERSION^{commit}" 2>/dev/null || true)
  if [ -z "$TAG_REVISION" ] || [ "$TAG_REVISION" != "$REVISION" ]; then
    echo "发布版本必须是指向当前提交的 Git Tag：$VERSION" >&2
    exit 1
  fi
fi

docker buildx version >/dev/null

COMPONENT_PLAN="$WORK_DIR/components.tsv"
"$RELEASECTL_BIN" manifest plan --root "$ROOT_DIR" >"$COMPONENT_PLAN"
PLAN_COMPONENTS=$(cut -f 1 "$COMPONENT_PLAN" | paste -sd ' ' -)
if [ "$PLAN_COMPONENTS" != "$RELEASE_COMPONENTS" ]; then
  echo "组件摘要计划不完整或顺序异常：$PLAN_COMPONENTS" >&2
  exit 1
fi
while IFS="$TAB" read -r COMPONENT DIGEST EXTRA; do
  if [ -n "${EXTRA:-}" ] || [ "${#DIGEST}" -ne 64 ]; then
    echo "组件摘要计划无效：$COMPONENT" >&2
    exit 1
  fi
  case "$DIGEST" in *[!0-9a-f]*) echo "组件摘要计划无效：$COMPONENT" >&2; exit 1 ;; esac
done <"$COMPONENT_PLAN"

component_digest() {
  awk -F '\t' -v component="$1" '$1 == component { print $2; exit }' "$COMPONENT_PLAN"
}

CONTROL_DIGEST=$(component_digest control)
WEB_DIGEST=$(component_digest web)
GATEWAY_DIGEST=$(component_digest gateway)
EDGE_DIGEST=$(component_digest edge)
export PLATFORM CONTROL_DIGEST WEB_DIGEST GATEWAY_DIGEST EDGE_DIGEST

if [ "$ACTION" = build ]; then
  docker buildx bake --file "$ROOT_DIR/docker-bake.hcl" --load
  echo "镜像构建完成：version=$VERSION revision=$REVISION components=$RELEASE_COMPONENTS"
  exit 0
fi

for PREFIX in $IMAGE_PREFIXES; do
  if ! printf '%s' "$PREFIX" | grep -Eq '^[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9._/-]+$'; then
    echo "镜像前缀无效：$PREFIX" >&2
    exit 1
  fi
done

inspect_remote_image() {
  INSPECT_REFERENCE=$1
  INSPECT_OUTPUT=$2
  INSPECT_RAW="$INSPECT_OUTPUT.raw"
  INSPECT_ERROR="$INSPECT_OUTPUT.error"
  if docker buildx imagetools inspect --format '{{json .}}' "$INSPECT_REFERENCE" >"$INSPECT_RAW" 2>"$INSPECT_ERROR"; then
    if ! INSPECT_METADATA=$("$RELEASECTL_BIN" image-metadata --input "$INSPECT_RAW"); then
      echo "无法解析远端镜像元数据：$INSPECT_REFERENCE" >&2
      return 1
    fi
    printf 'exists\t%s\n' "$INSPECT_METADATA" >"$INSPECT_OUTPUT"
    return 0
  fi
  if grep -Eqi 'not found|manifest unknown|name unknown|(^|[^0-9])404([^0-9]|$)' "$INSPECT_ERROR"; then
    printf '%s\n' missing >"$INSPECT_OUTPUT"
    return 0
  fi
  echo "检查远端镜像失败：$INSPECT_REFERENCE" >&2
  cat "$INSPECT_ERROR" >&2
  return 1
}

run_inspection_batch() {
  INSPECTION_REQUESTS=$1
  [ -s "$INSPECTION_REQUESTS" ] || return 0
  INSPECTION_PIDS="$WORK_DIR/inspection-pids.tsv"
  : >"$INSPECTION_PIDS"
  while IFS="$TAB" read -r INSPECTION_REFERENCE INSPECTION_OUTPUT; do
    (inspect_remote_image "$INSPECTION_REFERENCE" "$INSPECTION_OUTPUT") &
    printf '%s\t%s\n' "$!" "$INSPECTION_REFERENCE" >>"$INSPECTION_PIDS"
  done <"$INSPECTION_REQUESTS"
  INSPECTION_FAILED=false
  while IFS="$TAB" read -r INSPECTION_PID INSPECTION_REFERENCE; do
    if ! wait "$INSPECTION_PID"; then
      echo "远端镜像检查失败：$INSPECTION_REFERENCE" >&2
      INSPECTION_FAILED=true
    fi
  done <"$INSPECTION_PIDS"
  [ "$INSPECTION_FAILED" = false ]
}

metadata_status() {
  cut -f 1 "$1"
}

metadata_manifest() {
  cut -f 2 "$1"
}

validate_existing_metadata() {
  METADATA_FILE=$1
  METADATA_REFERENCE=$2
  METADATA_COMPONENT=$3
  METADATA_DIGEST=$4
  IFS="$TAB" read -r METADATA_STATUS METADATA_MANIFEST METADATA_ACTUAL_COMPONENT \
    METADATA_COMPONENT_DIGEST METADATA_SOURCE_DIGEST <"$METADATA_FILE"
  if [ "$METADATA_STATUS" != exists ] \
    || [ "$METADATA_ACTUAL_COMPONENT" != "$METADATA_COMPONENT" ] \
    || [ "$METADATA_COMPONENT_DIGEST" != "$METADATA_DIGEST" ] \
    || [ "$METADATA_SOURCE_DIGEST" != "$METADATA_DIGEST" ]; then
    echo "已存在的镜像与组件指纹不匹配：$METADATA_REFERENCE" >&2
    exit 1
  fi
}

ensure_remote_exists() {
  ENSURE_REFERENCE=$1
  ENSURE_OUTPUT=$2
  ENSURE_ATTEMPT=1
  while [ "$ENSURE_ATTEMPT" -le 5 ]; do
    inspect_remote_image "$ENSURE_REFERENCE" "$ENSURE_OUTPUT"
    if [ "$(metadata_status "$ENSURE_OUTPUT")" = exists ]; then
      return 0
    fi
    ENSURE_ATTEMPT=$((ENSURE_ATTEMPT + 1))
    [ "$ENSURE_ATTEMPT" -gt 5 ] || sleep 1
  done
  echo "远端镜像在发布后仍不可见：$ENSURE_REFERENCE" >&2
  return 1
}

# Phase 1a: inspect every immutable tag before writing anything. Inspections run
# concurrently because each Registry request is independent and read-only.
INSPECTION_REQUESTS="$WORK_DIR/preflight-requests.tsv"
INVENTORY_FILE="$WORK_DIR/inventory.tsv"
PUBLISH_PLAN="$WORK_DIR/publish-plan.tsv"
: >"$INSPECTION_REQUESTS"
: >"$INVENTORY_FILE"
PREFIX_INDEX=0
for PREFIX in $IMAGE_PREFIXES; do
  PREFIX_INDEX=$((PREFIX_INDEX + 1))
  while IFS="$TAB" read -r COMPONENT DIGEST; do
    CONTENT_IMAGE="$PREFIX/codex-cpa-$COMPONENT:sha256-$DIGEST"
    VERSION_IMAGE="$PREFIX/codex-cpa-$COMPONENT:$VERSION"
    CONTENT_METADATA="$WORK_DIR/p${PREFIX_INDEX}-${COMPONENT}-content.meta"
    VERSION_METADATA="$WORK_DIR/p${PREFIX_INDEX}-${COMPONENT}-version.meta"
    printf '%s\t%s\n' "$CONTENT_IMAGE" "$CONTENT_METADATA" >>"$INSPECTION_REQUESTS"
    printf '%s\t%s\n' "$VERSION_IMAGE" "$VERSION_METADATA" >>"$INSPECTION_REQUESTS"
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
      "$PREFIX" "$COMPONENT" "$DIGEST" "$CONTENT_IMAGE" "$VERSION_IMAGE" \
      "$CONTENT_METADATA" "$VERSION_METADATA" >>"$INVENTORY_FILE"
  done <"$COMPONENT_PLAN"
done
run_inspection_batch "$INSPECTION_REQUESTS"

: >"$PUBLISH_PLAN"
while IFS="$TAB" read -r PREFIX COMPONENT DIGEST CONTENT_IMAGE VERSION_IMAGE CONTENT_METADATA VERSION_METADATA; do
  CONTENT_EXISTS=false
  VERSION_EXISTS=false
  if [ "$(metadata_status "$CONTENT_METADATA")" = exists ]; then
    validate_existing_metadata "$CONTENT_METADATA" "$CONTENT_IMAGE" "$COMPONENT" "$DIGEST"
    CONTENT_EXISTS=true
  fi
  if [ "$(metadata_status "$VERSION_METADATA")" = exists ]; then
    validate_existing_metadata "$VERSION_METADATA" "$VERSION_IMAGE" "$COMPONENT" "$DIGEST"
    VERSION_EXISTS=true
  fi
  if [ "$CONTENT_EXISTS" = true ] && [ "$VERSION_EXISTS" = true ]; then
    if [ "$(metadata_manifest "$CONTENT_METADATA")" != "$(metadata_manifest "$VERSION_METADATA")" ]; then
      echo "内容标签与版本标签指向不同镜像：$CONTENT_IMAGE $VERSION_IMAGE" >&2
      exit 1
    fi
    PLAN_ACTION=reuse
  elif [ "$CONTENT_EXISTS" = true ]; then
    PLAN_ACTION=promote-version
  elif [ "$VERSION_EXISTS" = true ]; then
    PLAN_ACTION=promote-content
  else
    PLAN_ACTION=build
  fi
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$PREFIX" "$COMPONENT" "$DIGEST" "$CONTENT_IMAGE" "$VERSION_IMAGE" \
    "$PLAN_ACTION" "$CONTENT_METADATA" "$VERSION_METADATA" >>"$PUBLISH_PLAN"
  printf '发布计划：registry=%s component=%s action=%s digest=%.12s\n' \
    "$PREFIX" "$COMPONENT" "$PLAN_ACTION" "$DIGEST"
done <"$INVENTORY_FILE"

# Phase 1b: promote existing immutable content remotely. These operations move
# manifests/config only and never pull image layers to the publisher.
while IFS="$TAB" read -r PREFIX COMPONENT DIGEST CONTENT_IMAGE VERSION_IMAGE PLAN_ACTION CONTENT_METADATA VERSION_METADATA; do
  case "$PLAN_ACTION" in
    promote-version)
      docker buildx imagetools create --prefer-index=false --tag "$VERSION_IMAGE" "$CONTENT_IMAGE"
      ;;
    promote-content)
      docker buildx imagetools create --prefer-index=false --tag "$CONTENT_IMAGE" "$VERSION_IMAGE"
      ;;
  esac
done <"$PUBLISH_PLAN"

# Phase 1c: build only components absent from at least one Registry. A generated
# Bake override keeps every Registry tag as a distinct list item; comma-joining
# tags would create one invalid literal tag. One Bake invocation shares common
# stages and pushes content/version tags together.
BUILD_TARGETS="$WORK_DIR/build-targets"
BUILD_TAG_PLAN="$WORK_DIR/build-tags.tsv"
BAKE_OVERRIDE="$WORK_DIR/publish-tags.hcl"
: >"$BUILD_TARGETS"
: >"$BUILD_TAG_PLAN"
: >"$BAKE_OVERRIDE"
for COMPONENT in $RELEASE_COMPONENTS; do
  COMPONENT_TAG_FILE="$WORK_DIR/build-${COMPONENT}-tags"
  awk -F '\t' -v component="$COMPONENT" '
    $2 == component && $6 == "build" { print $4; print $5 }
  ' "$PUBLISH_PLAN" >"$COMPONENT_TAG_FILE"
  if [ -s "$COMPONENT_TAG_FILE" ]; then
    printf '%s\n' "$COMPONENT" >>"$BUILD_TARGETS"
    printf 'target "%s" {\n  tags = [\n' "$COMPONENT" >>"$BAKE_OVERRIDE"
    while IFS= read -r COMPONENT_TAG; do
      printf '    "%s",\n' "$COMPONENT_TAG" >>"$BAKE_OVERRIDE"
      printf '%s\t%s\n' "$COMPONENT" "$COMPONENT_TAG" >>"$BUILD_TAG_PLAN"
    done <"$COMPONENT_TAG_FILE"
    printf '  ]\n}\n\n' >>"$BAKE_OVERRIDE"
  fi
done
set -- docker buildx bake \
  --file "$ROOT_DIR/docker-bake.hcl" \
  --file "$BAKE_OVERRIDE" \
  --push
while IFS= read -r COMPONENT; do
  set -- "$@" "$COMPONENT"
done <"$BUILD_TARGETS"
if [ -s "$BUILD_TARGETS" ]; then
  CPAC_BAKE_TAG_PLAN=$BUILD_TAG_PLAN
  export CPAC_BAKE_TAG_PLAN
  "$@"
else
  echo "所有不可变组件镜像均已存在，跳过构建"
fi

# Reinspect only references that changed. Reused immutable tags retain the
# metadata already validated during the global fail-closed preflight.
PREPARE_REQUESTS="$WORK_DIR/prepare-requests.tsv"
: >"$PREPARE_REQUESTS"
while IFS="$TAB" read -r PREFIX COMPONENT DIGEST CONTENT_IMAGE VERSION_IMAGE PLAN_ACTION CONTENT_METADATA VERSION_METADATA; do
  case "$PLAN_ACTION" in
    promote-version)
      printf '%s\t%s\n' "$VERSION_IMAGE" "$VERSION_METADATA" >>"$PREPARE_REQUESTS"
      ;;
    promote-content)
      printf '%s\t%s\n' "$CONTENT_IMAGE" "$CONTENT_METADATA" >>"$PREPARE_REQUESTS"
      ;;
    build)
      printf '%s\t%s\n' "$CONTENT_IMAGE" "$CONTENT_METADATA" >>"$PREPARE_REQUESTS"
      printf '%s\t%s\n' "$VERSION_IMAGE" "$VERSION_METADATA" >>"$PREPARE_REQUESTS"
      ;;
  esac
done <"$PUBLISH_PLAN"
run_inspection_batch "$PREPARE_REQUESTS"

PREPARED_FILE="$WORK_DIR/prepared.tsv"
: >"$PREPARED_FILE"
while IFS="$TAB" read -r PREFIX COMPONENT DIGEST CONTENT_IMAGE VERSION_IMAGE PLAN_ACTION CONTENT_METADATA VERSION_METADATA; do
  if [ "$(metadata_status "$CONTENT_METADATA")" != exists ]; then
    ensure_remote_exists "$CONTENT_IMAGE" "$CONTENT_METADATA"
  fi
  if [ "$(metadata_status "$VERSION_METADATA")" != exists ]; then
    ensure_remote_exists "$VERSION_IMAGE" "$VERSION_METADATA"
  fi
  validate_existing_metadata "$CONTENT_METADATA" "$CONTENT_IMAGE" "$COMPONENT" "$DIGEST"
  validate_existing_metadata "$VERSION_METADATA" "$VERSION_IMAGE" "$COMPONENT" "$DIGEST"
  CONTENT_MANIFEST=$(metadata_manifest "$CONTENT_METADATA")
  VERSION_MANIFEST=$(metadata_manifest "$VERSION_METADATA")
  if [ "$CONTENT_MANIFEST" != "$VERSION_MANIFEST" ]; then
    echo "准备阶段生成了不一致的不可变标签：$CONTENT_IMAGE $VERSION_IMAGE" >&2
    exit 1
  fi
  printf '%s\t%s\t%s\t%s\t%s\n' \
    "$PREFIX" "$COMPONENT" "$DIGEST" "$VERSION_IMAGE" "$VERSION_MANIFEST" >>"$PREPARED_FILE"
done <"$PUBLISH_PLAN"

# Phase 2: all Registries now contain all immutable tags. Only now may latest
# move. Existing matching latest tags are reused, making retries idempotent.
LATEST_REQUESTS="$WORK_DIR/latest-requests.tsv"
LATEST_PLAN="$WORK_DIR/latest-plan.tsv"
: >"$LATEST_REQUESTS"
: >"$LATEST_PLAN"
LATEST_INDEX=0
while IFS="$TAB" read -r PREFIX COMPONENT DIGEST VERSION_IMAGE VERSION_MANIFEST; do
  LATEST_INDEX=$((LATEST_INDEX + 1))
  LATEST_IMAGE="$PREFIX/codex-cpa-$COMPONENT:latest"
  LATEST_METADATA="$WORK_DIR/latest-${LATEST_INDEX}.meta"
  printf '%s\t%s\n' "$LATEST_IMAGE" "$LATEST_METADATA" >>"$LATEST_REQUESTS"
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$PREFIX" "$COMPONENT" "$DIGEST" "$VERSION_IMAGE" "$VERSION_MANIFEST" "$LATEST_METADATA" >>"$LATEST_PLAN"
done <"$PREPARED_FILE"
run_inspection_batch "$LATEST_REQUESTS"

while IFS="$TAB" read -r PREFIX COMPONENT DIGEST VERSION_IMAGE VERSION_MANIFEST LATEST_METADATA; do
  LATEST_IMAGE="$PREFIX/codex-cpa-$COMPONENT:latest"
  if [ "$(metadata_status "$LATEST_METADATA")" = exists ] \
    && [ "$(metadata_manifest "$LATEST_METADATA")" = "$VERSION_MANIFEST" ]; then
    printf 'latest 已是目标版本，跳过：%s\n' "$LATEST_IMAGE"
    continue
  fi
  docker buildx imagetools create --prefer-index=false --tag "$LATEST_IMAGE" "$VERSION_IMAGE"
  ensure_remote_exists "$LATEST_IMAGE" "$LATEST_METADATA"
  validate_existing_metadata "$LATEST_METADATA" "$LATEST_IMAGE" "$COMPONENT" "$DIGEST"
  if [ "$(metadata_manifest "$LATEST_METADATA")" != "$VERSION_MANIFEST" ]; then
    echo "latest 未指向准备完成的版本镜像：$LATEST_IMAGE" >&2
    exit 1
  fi
done <"$LATEST_PLAN"

echo "镜像发布完成：version=$VERSION revision=$REVISION prefixes=$IMAGE_PREFIXES"
