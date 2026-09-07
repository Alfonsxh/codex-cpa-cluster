# 快速开始

本分支只提供 Go 服务、React 前端和一个统一的 `scripts/run.sh`。目标机不安装额外 CLI；同一个脚本负责首次初始化、升级和必要的管理子命令。

## 开发依赖

- Go：版本以 `go.mod` 为准。
- Node.js 22：React 和 OpenAPI TypeScript 生成。
- Docker Engine 与 Docker Compose v2：镜像构建、正式配置校验和隔离数据面演练。

```sh
npm ci --prefix frontend
npm ci --prefix tools/openapi
make -f scripts/build.mk verify
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
make -f scripts/build.mk test-build
make -f scripts/build.mk test-up
make -f scripts/build.mk test-smoke
make -f scripts/build.mk test-faults
make -f scripts/build.mk test-down
```

该环境使用仓库 fixture 和本机回环端口，验证 Key 拒绝、模型请求、Responses SSE、损坏快照、上游故障和蓝绿排空，不接触真实目标数据。

## 全新目标安装

域名 DNS 需预先指向目标机。安装和以后每次升级都可以重复执行同一条命令：

```sh
curl -fsSL https://github.com/Alfonsxh/codex-cpa-pool/releases/latest/download/run.sh | sudo sh
```

产品名称为 Codex CPA Pool；官方发布源为 `Alfonsxh/codex-cpa-pool`。下文的 `/home/ccpa` 是新安装默认目录；旧版 `/home/cpac` 环境原址沿用，命令中的路径应替换为实际运维根目录。安装器使用 `CPAP_*` 参数与配置，并兼容旧 `CPAC_*`。详见[部署兼容规则](deployment.md#名称与现有部署兼容)。

默认入口选择 GitHub Latest Release。已安装环境可以执行 `sudo /home/ccpa/run.sh --tag`，按版本顺序查看高于当前部署版本的 GitHub Releases 并交互选择；非交互调用只打印候选，不执行升级。需要固定版本时使用 `sudo /home/ccpa/run.sh --tag v2.0.0`；指定 Tag 必须存在对应 GitHub Release 及完整校验附件，脚本不会从孤立 Git Tag 或源码归档部署。

首次执行会提示域名，检测现有 Nginx/同域名站点/证书，并选择 `external`（复用既有反向代理）或 `managed`（由 Codex CPA Pool 管理 Nginx/TLS）；选择会与域名一起写入 `/home/ccpa/config.env`。无交互环境必须使用例如 `sudo /home/ccpa/run.sh run --domain qdata.example.com --ingress external`。部署保留宿主机时区，容器使用 UTC。首次 Web 设置通过可搜索下拉框选择系统时区（默认 `Asia/Shanghai`）；页面显示、额度自然周、每日统计和通知统一读取配置中心的 `system.timezone`。`external` 不安装、不启动、不修改 Nginx 或 Certbot，只输出对 `127.0.0.1:18317` 的反向代理契约并跳过公网检查；`managed` 才配置 Nginx/TLS，且拒绝覆盖无本项目托管标记的同名站点。旧版本保存在 `/etc/cpac/` 的域名配置和待领取管理员凭据会在下一次默认入口执行时安全迁移到实际运维根目录并删除旧文件。零账号目标的 Gateway 可以健康启动，但在创建账号和用户 API Key 前，模型请求仍返回 401。

日后如确需切换入口，先处理好宿主机站点归属，再执行并确认：

```sh
sudo /home/ccpa/run.sh ingress set managed
# 或
sudo /home/ccpa/run.sh ingress set external
```

### 首次管理员设置

使用部署完成时只显示一次的管理密钥登录 `https://<域名>/admin/`。全新控制面会自动进入 `/admin/setup`，完成状态全部由后端依据 SQLite 设置和加密 Secret 状态实时计算，不使用浏览器存储充当事实来源。

初始化页只保留可在当前页面完成的配置：允许的邮箱域名、用户初始密码、公开访问地址、系统时区、默认周额度、通知、品牌和上游代理。页面不再区分“必须完成”和“推荐设置”，可跳过的项目仍可逐项恢复；CPA 创建、OAuth 授权和用户创建应在对应管理页面按实际业务顺序操作。

引导接口异常不会阻断账号、用户、配置中心等既有管理页面；页面提供明确的运行总览出口。接口只返回配置状态和数量，不返回密码、Key、Webhook、OAuth 或代理 Secret。用户初始密码仍通过只写 Modal 设置，关闭后不会进入 Local Storage 或 Session Storage。

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
make -f scripts/build.mk target-config TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-pull TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-verify-images TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-activate TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-up-core TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-up-writers TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-smoke TARGET_ENV=/absolute/path/to/test.env
```

通知会产生外部消息，必须单独获批后执行 `up-notifications`。目标烟测只证明 Go 拓扑与静态路径；上线前仍要使用真实 API Key 验证非流式 Responses 和 SSE，并完成浏览器验收。

更多信息见 [架构](architecture.md)、[部署](deployment.md)、[备份恢复](backup-and-restore.md) 和 [故障排查](troubleshooting.md)。

### 系统时区

首次设置或「配置中心 → 系统设置 → 系统时区」使用同一个 IANA 时区下拉框，可按城市或时区名称搜索。页面时间、今日用量、自然周额度、每日趋势和通知调度共享此值，并自动遵守所选地区的夏令时。修改后采集器重启并按新时区重新归集周用量，原始事件时间戳保持不变；浏览器会刷新相关统计。

旧版配置按 `system.timezone`、`user_quota.timezone`、`notification.timezone` 的优先顺序读取；配置中心首次读取时将旧键迁移为唯一的 `system.timezone`。如果旧额度和通知时区不同，以原额度时区为准，通知随后也使用此时区，升级前应核对通知发送时间。未配置过时区的新安装仍需在首次设置中确认选择。

宿主机时区不由 Web 配置修改。容器与运行日志使用 UTC，业务时区不再通过环境变量配置；旧 `target.env` 中的 `CPA_TIMEZONE` 不再生效。现存业务 CPA 容器的进程时区会在正常重建时改为 UTC，切换系统时区不会重建这些容器。
