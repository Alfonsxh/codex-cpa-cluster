# 快速开始

本分支只提供 Go 服务与 React 前端，不保留旧安装器。全新单机目标使用 `cpac` 初始化；底层 `deploy-target.sh` 继续只接受已经初始化且数据完整的目标目录。

## 开发依赖

- Go：版本以 `go.mod` 为准。
- Node.js 22：React 和 OpenAPI TypeScript 生成。
- Docker Engine 与 Docker Compose v2：镜像构建、正式配置校验和隔离数据面演练。

```sh
npm ci --prefix frontend
npm ci --prefix tools/openapi
make verify
```

## 本地页面预览

Go Preview 提供固定测试数据和只读 Admin Mock：

```sh
go run ./cmd/test-preview --address 127.0.0.1:8896 --root .
CPA_DEV_PROXY_TARGET=http://127.0.0.1:8896 npm --prefix frontend run dev
```

访问 `http://127.0.0.1:5173/admin/`，输入任意非空本地预览密钥。Preview 不执行账号、用户、密钥或运行时写操作。

## 隔离数据面

```sh
make test-build
make test-up
make test-smoke
make test-faults
make test-down
```

该环境使用仓库 fixture 和本机回环端口，验证 Key 拒绝、模型请求、Responses SSE、损坏快照、上游故障和蓝绿排空，不接触真实目标数据。

## 全新目标安装

目标需预先安装 Docker Engine、Docker Compose v2，并配置好域名的 DNS/TLS 入口。安装 `cpac` 后只需执行：

```sh
curl -fsSL https://github.com/Alfonsxh/codex-cpa-cluster/raw/refs/heads/main/scripts/install-cpac.sh | sudo sh
sudo cpac deploy
```

首次执行会提示域名并写入 `/etc/cpac/config.env`；无交互环境使用 `sudo cpac deploy --domain qdata.example.com`。`cpac` 校验 GitHub Release、初始化空状态、启动服务并调用正式烟测。零账号目标的 Gateway 可以健康启动，但在创建账号和用户 API Key 前，模型请求仍返回 401。

## 已有 Test 目标的底层部署

目标至少必须存在：

```text
docker-compose.yml
release-manifest.json
state/control-plane.sqlite3
state/usage.sqlite3
secrets/control-plane.key
state/gateway/
state/edge/
state/edge/active-gateway.conf
logs/gateway/
```

从同一个发布包更新目标内的 `docker-compose.yml` 与 `release-manifest.json`，再从 `.env.example` 生成仓库外私有环境文件，填入同一发布描述中的四个 `:sha256-<源码摘要>` 镜像和实际目标参数。示例中的目录、首次接管和 Edge 维护确认故意留空；不得把示例文件本身当作切换授权。正式控制面配置只来自该环境文件，然后执行：

```sh
CPA_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh config
CPA_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh pull
CPA_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh verify-images
CPA_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh activate
CPA_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh up-core
CPA_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh up-writers
CPA_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh smoke
```

通知会产生外部消息，必须单独获批后执行 `up-notifications`。目标烟测只证明 Go 拓扑与静态路径；上线前仍要使用真实 API Key 验证非流式 Responses 和 SSE，并完成浏览器验收。

更多信息见 [架构](architecture.md)、[部署](deployment.md)、[备份恢复](backup-and-restore.md) 和 [故障排查](troubleshooting.md)。
