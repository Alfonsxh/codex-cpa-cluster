# 快速开始

本分支只提供 Go 服务、React 前端和一个统一的 `scripts/deploy.sh`。目标机不安装额外 CLI；同一个脚本负责首次初始化、升级和必要的管理子命令。

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

域名 DNS 需预先指向目标机。下载唯一部署脚本后执行：

```sh
sudo install -d -o root -g root -m 0755 /home/cpac
sudo curl -fL https://github.com/Alfonsxh/codex-cpa-cluster/releases/latest/download/deploy.sh \
  -o /home/cpac/deploy.sh
sudo chmod 0755 /home/cpac/deploy.sh
sudo /home/cpac/deploy.sh
```

首次执行会提示域名并写入 `/home/cpac/config.env`；无交互环境使用 `sudo /home/cpac/deploy.sh deploy --domain qdata.example.com`。旧版本保存在 `/etc/cpac/` 的域名配置和待领取管理员凭据会在下一次默认入口执行时安全迁移并删除旧文件。脚本安装必要依赖、配置 Nginx/TLS、校验 GitHub Release、初始化空状态、启动服务并调用正式烟测。零账号目标的 Gateway 可以健康启动，但在创建账号和用户 API Key 前，模型请求仍返回 401。

### 首次管理员设置

使用部署完成时只显示一次的管理密钥登录 `https://<域名>/admin/`。全新控制面会自动进入 `/admin/setup`，完成状态全部由后端依据 SQLite、加密 Secret 状态、OAuth 和容器运行状态实时计算，不使用浏览器存储充当事实来源。

必需流程为：允许的邮箱域名、用户初始密码、首个 CPA、该 CPA 的 OAuth 与运行检查、首个用户。必需项不能标记为跳过。公开访问地址、额度时区、默认周额度、通知、品牌和上游代理属于推荐项，可以逐项跳过并随时恢复。

引导接口异常不会阻断账号、用户、配置中心等既有管理页面；接口只返回配置状态、数量和阻塞原因，不返回密码、Key、Webhook、OAuth 或代理 Secret。用户初始密码仍通过只写 Modal 设置，关闭后不会进入 Local Storage 或 Session Storage。

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
make target-config TARGET_ENV=/absolute/path/to/test.env
make target-pull TARGET_ENV=/absolute/path/to/test.env
make target-verify-images TARGET_ENV=/absolute/path/to/test.env
make target-activate TARGET_ENV=/absolute/path/to/test.env
make target-up-core TARGET_ENV=/absolute/path/to/test.env
make target-up-writers TARGET_ENV=/absolute/path/to/test.env
make target-smoke TARGET_ENV=/absolute/path/to/test.env
```

通知会产生外部消息，必须单独获批后执行 `up-notifications`。目标烟测只证明 Go 拓扑与静态路径；上线前仍要使用真实 API Key 验证非流式 Responses 和 SSE，并完成浏览器验收。

更多信息见 [架构](architecture.md)、[部署](deployment.md)、[备份恢复](backup-and-restore.md) 和 [故障排查](troubleshooting.md)。
