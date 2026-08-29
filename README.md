# Codex CPA Cluster

Codex CPA Cluster 是 CLIProxyAPI 多账号服务的 Go 控制平面与数据面。正式运行时由四个镜像组成，业务权威数据继续使用既有 SQLite 与加密主密钥，不初始化或覆盖已有目标。

所有正式组件与 Release Metadata 统一由仓库根目录 `Dockerfile` 的明确 Target 构建；仓库不保留第二份部署 Dockerfile。

| 组件 | 技术 | 职责 |
|---|---|---|
| Control | Go、Gin、Cobra、Viper、SQLite、Zap | Admin API、Portal API、账号与用户生命周期、用量采集、额度、故障迁移、通知、日志维护 |
| Web | Go、React、Ant Design、React Query、ECharts | Admin、使用中心、Portal 静态资源与细粒度 API 代理 |
| Gateway | Go | API Key 鉴权、额度执行、CPA 路由、快照热加载、SSE 转发与请求排空 |
| Edge | Go | 稳定主机端口、蓝绿 Gateway 选择、Web 与数据面分流 |

## 核心能力

- 用户外部 API Key 与上游内部 Key 隔离，快照不包含管理密钥。
- 控制数据和高频用量分别存储于 `state/control-plane.sqlite3` 与 `state/usage.sqlite3`。
- 账号、用户、团队、额度、用量、运行任务与配置中心均由 Go API 提供。
- 账号故障迁移支持 `off` 与 `active`，批量迁移采用原子路由更新。
- Edge 固定主机端口，Gateway 蓝绿切换；已有 SSE 请求在原槽排空，新请求进入新槽。
- 四个镜像使用源码摘要和不可变标签发布，目标机本地拉取并校验镜像标签。
- Production 与 Test 都要求已有 SQLite 和匹配的 `secrets/control-plane.key`。

## 本地验证

```sh
npm ci --prefix frontend
npm ci --prefix tools/openapi
make verify
```

React 浏览器矩阵：

```sh
npm --prefix frontend run test:e2e
```

隔离数据面故障演练：

```sh
make test-build
make test-up
make test-smoke
make test-faults
make test-down
```

## 目标部署

1. 从 `.env.example` 生成仓库外的目标机私有 `target.env`；它是正式控制面部署参数的唯一来源，不提交目标地址或 Registry 凭据。
2. 从同一发布包更新目标目录内的 `docker-compose.yml` 与 `release-manifest.json`，并确认两份 SQLite、匹配的主密钥、Gateway 快照、活动槽文件和可写日志目录已经存在；部署脚本拒绝符号链接运行目录和不匹配的 Compose 副本。
3. 使用不可变的 `control`、`web`、`gateway`、`edge` 镜像。
4. 显式填写目录/Writer 停止确认；示例文件不会预置任何切换授权。
5. 依次执行镜像拉取、标签校验、所有权激活、Gateway 蓝绿排空、Admin/Web、Writer 和烟测。

```sh
make deploy TARGET_ENV=/absolute/path/to/target.env
```

发布与部署脚本不会连接 Production；目标选择只来自操作者提供的环境文件和本地 Harness 配置。
Edge 持有唯一公开端口，镜像或 Compose 配置变化时必须额外确认维护窗口；普通 Control、Web、Gateway 发布不会重建 Edge。

## 安全边界

- 不提交 API Key、OAuth、Webhook、SQLite、日志、备份或生成快照。
- Admin 在已初始化目标上直接挂载 Docker Socket 以管理账号容器；Web、Gateway 和 Edge 不获得该权限。
- 不从本仓库修改 `/opt/cliproxyapi` 或外部代理服务。
- `/health` 和 `/v1/models` 不能替代真实 `/v1/responses` SSE 验收。

详见 [架构](docs/architecture.md)、[开发](docs/development.md)、[部署](docs/deployment.md) 和 [备份恢复](docs/backup-and-restore.md)。
