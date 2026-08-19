# 本地开发

## 环境

- Python 3.12（生产代码兼容 Python 3.8+）
- Node.js 22
- Lua 5.4
- Docker 与 Docker Compose v2

安装 Python 依赖：

```bash
python3 -m pip install -r requirements.txt
```

前端使用原生 JavaScript，没有额外的 npm 安装步骤。

如果宿主机没有 Lua，但 Docker daemon 正在运行，统一验证脚本会自动使用 Gateway 的 OpenResty 镜像执行 Lua 测试。

## 本地 Compose

基础 Compose 是生产镜像拓扑；本地构建必须显式叠加开发 override：

```bash
cp .env.example .env
export DEPLOY_ROOT="$PWD"
make dev-build
python3 scripts/cliproxy.py --root "$PWD" render
make dev-up
```

等价的 Compose 文件顺序是：

```text
docker-compose.yml        # 生产运行拓扑，不含 build
  + docker-compose.dev.yml  # 本地 Dockerfile/build args
  + compose.accounts.yml    # 运行时生成的业务 CPA
```

`make dev-build` 在没有本地运行状态时使用 `compose.env.example`；`render` 会创建本地
SQLite 并生成私有的 `state/compose.env`，`make dev-up` 只读取该生成投影。

不要把 `docker-compose.dev.yml` 复制到生产目录，也不要让生产部署执行 `docker compose build`。

## 统一验证

```bash
make verify
```

该命令覆盖 Python、JavaScript、Lua 和 Shell 检查、单元测试、公开发布边界、Compose 配置与 Git diff 检查。

也可以单独运行：

```bash
python3 -m unittest discover -s tests
node tests/test_token_usage.js
node tests/test_admin_monitor_interactions.js
node tests/test_admin_view_state.js
lua tests/test_gateway_state.lua
lua tests/test_request_gate.lua
```

## 修改原则

- 保持 `/v1` 数据面不依赖 Admin 和 Web。
- 修改 Gateway 或 Edge 发布行为时，先更新 ADR 或新增 ADR。
- SQLite 是控制面事实来源，新增文件投影必须可从数据库重新生成。
- 不在源码、测试夹具或文档中写入真实域名、邮箱、Key、Webhook 或私网地址。
- 为业务分支和安全不变量增加测试。

## 问题记录

- [2026-08-19 Web 分视图加载状态回归](problem-records/2026-08-19-web-view-state-regressions.md)：记录管理中心按需加载后出现的跨页面状态依赖、修复边界和回归矩阵。

## 提交和 Pull Request

提交信息建议遵循：

```text
feat(scope): description
fix(scope): description
docs(scope): description
chore(scope): description
```

Pull Request 应说明问题、最小修改范围、验证结果和部署影响。详细要求见仓库根目录的 `CONTRIBUTING.md`。
