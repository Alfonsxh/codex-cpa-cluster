# 部署

日常安装见[快速开始](getting-started.md)，更新版本见[升级](upgrade.md)。本页说明目录、入口和底层部署约束。

## 目录与配置

新安装默认布局：

```text
/home/ccpa/
  run.sh           安装与运维入口
  config.env       域名、入口模式、真实部署目录
  runtime/         业务数据、Compose 与 target.env
  backups/         升级备份
```

`runtime` 可指向已有物理目录；安装器先解析真实路径，拒绝断链、循环、文件或根目录。内部数据库、密钥和配置文件仍须为普通文件。已保存的目录优先，发现不同部署的冲突配置时停止。

`runtime/target.env` 是正式 Compose 的配置来源，保存四组件镜像、目录、网络、端口和部署身份。`state/compose.env` 仅用于业务账号容器，不能替代它。

## 名称与现有部署兼容

产品为 Codex CPA Pool，发布源为 `Alfonsxh/codex-cpa-pool`。安装器使用 `CPAP_*` 参数，兼容旧 `CPAC_*` 和 `/home/cpac` 安装；改名不搬迁数据。

升级只替换现有 `target.env` 的四个镜像字段。选择历史 Release 时，安装器保留已具备兼容修复的入口。旧 `/etc/cpac/` 运维配置和待领取管理凭据经一致性校验后迁至实际运维目录。

## 入口与 HTTPS

| 模式 | 行为 |
| --- | --- |
| `external` | 不修改 Nginx/Certbot，不申请证书，不执行公网健康检查 |
| `managed` | 配置本项目的 Nginx/TLS，拒绝覆盖未标记为本项目托管的同名站点 |

外部代理连接 `127.0.0.1:<CPA_PUBLIC_PORT>`，保留 `Host` 和 `X-Forwarded-*`，支持 WebSocket/SSE，流式超时设为 3600 秒。端口以 `target.env` 为准，新安装默认为 `18317`。完成后从实际公网入口检查 `/__health`。

入口切换使用 `sudo ./run.sh ingress set managed` 或 `external`，并先处理站点归属。容器使用 UTC；业务时间由配置中心的 `system.timezone` 决定，安装器不修改宿主机时区。

## 初始化与既有目标

首次安装仅在临时目录调用 Control 镜像的 `cpa-bootstrap`，生成两份 SQLite、匹配主密钥、管理员凭据和初始快照，完整后再发布运行目录。它不能用于修复或覆盖已有数据。

已有目标的底层部署至少要求：

- 同一发布包的 `docker-compose.yml` 和 `release-manifest.json`。
- `state/control-plane.sqlite3`、`state/usage.sqlite3` 及匹配的 `secrets/control-plane.key`。
- 原有 OAuth、账号配置、`state/gateway/` 和 `state/edge/active-gateway.conf`。
- 可由服务 UID `10001` 写入的 `logs/gateway/`；活动槽只能为 `blue` 或 `green`。

四组件为 `control`、`web`、`gateway`、`edge`。目标只接受发布描述中的 `:sha256-<源码摘要>` 镜像，并检查组件名称及两个摘要标签；镜像发布流程见[开发指南](development.md#发布版本)。

## 底层应用与验收

仅在已初始化的目标上使用同一 Release 的工具，先完成[备份](backup-and-restore.md)。下列底层入口不包含安装器的自动备份。私有环境文件留在仓库外，并明确填写目标目录、首次所有权接管及必要的 Edge 维护确认。

```sh
make -f scripts/build.mk run TARGET_ENV=/absolute/path/to/target.env
```

依次执行配置检查、拉取、镜像校验、所有权激活、核心服务、后台任务和烟测。首次接管前必须停止旧 Writer；所有动作限定在指定 Compose 项目和网络。

Gateway 更新先准备非活动槽，再切换新请求、等待旧槽排空。超时不强制终止旧 SSE。Edge 镜像或 Compose 配置变化须设置 `CPA_ALLOW_EDGE_RECREATE=true`，并用 `CPA_CONFIRM_EDGE_MAINTENANCE` 重复实际部署目录。

通知 Worker 随部署启动；通知关闭或未配置 Webhook 时待命。其健康检查使用调度心跳，发送状态见[配置中心](configuration-center.md#企业微信通知)。

上线前后核对数据库完整性、Schema、关键行数及原 Key/路由；验证管理页面、`/v1/models`、实际 Responses 和 SSE。生产使用在 Test 验收过的发布摘要，但需独立核对自身数据和请求。CI 不连接部署目标。
