#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

if command -v lua5.4 >/dev/null 2>&1; then
  LUA_BIN=lua5.4
  LUA_MODE=native
elif command -v lua >/dev/null 2>&1; then
  LUA_BIN=lua
  LUA_MODE=native
else
  # 发布本身必须使用 Docker；本机未安装 Lua 时复用生产 OpenResty
  # 镜像中的 LuaJIT，避免为了本地质量门禁额外修改宿主机环境。
  if ! docker info >/dev/null 2>&1; then
    echo "缺少 Lua 且 Docker daemon 未运行，无法执行 Gateway 测试" >&2
    exit 1
  fi
  LUA_BIN=$(
    awk -F= '$1 == "ARG GATEWAY_BASE_IMAGE" {print $2; exit}' \
      "$ROOT_DIR/gateway/Dockerfile"
  )
  LUA_MODE=docker
fi

run_lua() {
  if [ "$LUA_MODE" = native ]; then
    (cd "$ROOT_DIR" && "$LUA_BIN" "$@")
  else
    docker run --rm \
      -v "$ROOT_DIR:/workspace:ro" \
      -w /workspace \
      "$LUA_BIN" \
      /usr/local/openresty/luajit/bin/luajit "$@"
  fi
}

printf '%s\n' '[1/8] 编译与语法检查'
python3 -m py_compile \
  "$ROOT_DIR/codex-cpa" \
  "$ROOT_DIR/admin/account_failover.py" \
  "$ROOT_DIR/admin/log_maintenance.py" \
  "$ROOT_DIR/admin/server.py" \
  "$ROOT_DIR/admin/usage_collector.py" \
  "$ROOT_DIR/admin/usage_store.py" \
  "$ROOT_DIR/admin/wecom_notifications.py" \
  "$ROOT_DIR/scripts/admin-preview.py" \
  "$ROOT_DIR/scripts/branding.py" \
  "$ROOT_DIR/scripts/check-public-release.py" \
  "$ROOT_DIR/scripts/cliproxy.py" \
  "$ROOT_DIR/scripts/control_plane_store.py" \
  "$ROOT_DIR/scripts/edge_slot.py" \
  "$ROOT_DIR/scripts/gateway_release_probe.py" \
  "$ROOT_DIR/scripts/migration-admin-write-compare.py" \
  "$ROOT_DIR/scripts/ownership_lease.py" \
  "$ROOT_DIR/scripts/release_image_compare.py" \
  "$ROOT_DIR/scripts/release_manifest.py" \
  "$ROOT_DIR/scripts/runtime_data_guard.py" \
  "$ROOT_DIR/scripts/v1-compare-admin.py" \
  "$ROOT_DIR/scripts/v2-target-data.py"
node --check "$ROOT_DIR/admin/static/app.js"
node --check "$ROOT_DIR/admin/static/monitor-utils.js"
node --check "$ROOT_DIR/admin/static/view-state-utils.js"
node --check "$ROOT_DIR/portal/token-usage.js"
node --check "$ROOT_DIR/portal/my-keys.js"
node --check "$ROOT_DIR/portal/native.js"
node --check "$ROOT_DIR/portal/branding.js"
node --check "$ROOT_DIR/portal/landing.js"
sh -n \
  "$ROOT_DIR/scripts/check-generated-api.sh" \
  "$ROOT_DIR/scripts/cliproxy.sh" \
  "$ROOT_DIR/scripts/deploy-release.sh" \
  "$ROOT_DIR/scripts/deploy-v1-compare-target.sh" \
  "$ROOT_DIR/scripts/deploy-v2-target.sh" \
  "$ROOT_DIR/scripts/generate-api.sh" \
  "$ROOT_DIR/scripts/install-release.sh" \
  "$ROOT_DIR/scripts/local-release.sh" \
  "$ROOT_DIR/scripts/package-release.sh" \
  "$ROOT_DIR/scripts/release-images.sh" \
  "$ROOT_DIR/scripts/v2-test-faults.sh" \
  "$ROOT_DIR/scripts/v2-test-smoke.sh" \
  "$ROOT_DIR/scripts/v2-worker-process-rehearsal.sh" \
  "$ROOT_DIR/scripts/verify.sh"
run_lua -e 'assert(loadfile("gateway/gateway_state.lua")); assert(loadfile("gateway/request_gate.lua"))'
(cd "$ROOT_DIR" && sh scripts/check-generated-api.sh)
UNFORMATTED_GO=$(find "$ROOT_DIR/cmd" "$ROOT_DIR/internal" -type f -name '*.go' -exec gofmt -l {} +)
if [ -n "$UNFORMATTED_GO" ]; then
  echo "Go 文件未格式化：" >&2
  printf '%s\n' "$UNFORMATTED_GO" >&2
  exit 1
fi

printf '%s\n' '[2/8] Go 测试'
(cd "$ROOT_DIR" && go vet ./... && go test ./... && go test -race ./...)

printf '%s\n' '[3/8] JavaScript 测试'
node "$ROOT_DIR/tests/test_token_usage.js"
node "$ROOT_DIR/tests/test_admin_monitor_interactions.js"
node "$ROOT_DIR/tests/test_admin_view_state.js"
npm --prefix "$ROOT_DIR/frontend" run typecheck
npm --prefix "$ROOT_DIR/frontend" test
npm --prefix "$ROOT_DIR/frontend" run build

printf '%s\n' '[4/8] Lua 测试'
run_lua tests/test_gateway_state.lua
run_lua tests/test_request_gate.lua

printf '%s\n' '[5/8] Python 测试'
python3 -m unittest discover -s "$ROOT_DIR/tests"

printf '%s\n' '[6/8] 公开发布边界'
python3 "$ROOT_DIR/scripts/check-public-release.py" --root "$ROOT_DIR"

printf '%s\n' '[7/8] Compose 配置'
docker compose \
  --project-directory "$ROOT_DIR" \
  --env-file "$ROOT_DIR/.env.example" \
  --env-file "$ROOT_DIR/compose.env.example" \
  -f "$ROOT_DIR/docker-compose.yml" \
  config --quiet
docker compose \
  --project-directory "$ROOT_DIR" \
  --env-file "$ROOT_DIR/.env.example" \
  --env-file "$ROOT_DIR/compose.env.example" \
  -f "$ROOT_DIR/docker-compose.yml" \
  -f "$ROOT_DIR/docker-compose.dev.yml" \
  config --quiet
docker compose \
  -p codex-cpa-v1-compare-verify \
  --project-directory "$ROOT_DIR" \
  --env-file "$ROOT_DIR/v1-compare.env.example" \
  -f "$ROOT_DIR/docker-compose.v1-compare.yml" \
  config --quiet
docker compose \
  -p codex-cpa-v2-test \
  --project-directory "$ROOT_DIR" \
  -f "$ROOT_DIR/docker-compose.v2-test.yml" \
  config --quiet
docker compose \
  -p codex-cpa-v2-verify \
  --project-directory "$ROOT_DIR" \
  --env-file "$ROOT_DIR/v2-compose.env.example" \
  -f "$ROOT_DIR/docker-compose.v2.yml" \
  --profile writers \
  --profile external-effects \
  config --quiet

printf '%s\n' '[8/8] Git diff 格式'
git -C "$ROOT_DIR" diff --check

printf '%s\n' '验证通过'
