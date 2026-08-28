# 快速开始

本分支只提供 Go 服务与 React 前端，不提供第二套运行时或旧安装器。正式 Test/Production 部署只接受已经初始化且数据完整的目标目录。

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
make v2-test-build
make v2-test-up
make v2-test-smoke
make v2-test-faults
make v2-test-down
```

该环境使用仓库 fixture 和本机回环端口，验证 Key 拒绝、模型请求、Responses SSE、损坏快照、上游故障和蓝绿排空，不接触真实目标数据。

## 已有 Test 目标部署

目标至少必须存在：

```text
docker-compose.yml
state/control-plane.sqlite3
state/usage.sqlite3
secrets/control-plane.key
state/gateway/
state/edge/
```

从 `.env.example` 生成仓库外私有环境文件，填入同一发布的四个不可变镜像和实际目标参数，然后执行：

```sh
V2_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh config
V2_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh pull
V2_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh verify-images
V2_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh activate
V2_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh up-core
V2_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh up-writers
V2_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh smoke
```

通知会产生外部消息，必须单独获批后执行 `up-notifications`。目标烟测只证明 Go 拓扑与静态路径；上线前仍要使用真实 API Key 验证非流式 Responses 和 SSE，并完成浏览器验收。

更多信息见 [架构](architecture.md)、[部署](deployment.md)、[备份恢复](backup-and-restore.md) 和 [故障排查](troubleshooting.md)。
