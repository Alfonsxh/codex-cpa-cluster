# Codex CPA Cluster

Codex CPA Cluster 是 CLIProxyAPI 多账号服务的 Go 控制平面与数据面。正式运行时由四个镜像组成，业务权威数据继续使用既有 SQLite 与加密主密钥，不初始化或覆盖已有目标。

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
make v2-test-build
make v2-test-up
make v2-test-smoke
make v2-test-faults
make v2-test-down
```

## 目标部署

1. 从 `.env.example` 生成目标机私有环境文件，不提交密钥、目标地址或 Registry 凭据。
2. 确认目标目录已有 `docker-compose.yml`、两份 SQLite 和匹配的主密钥。
3. 使用不可变的 `control`、`web`、`gateway`、`edge` 镜像。
4. 依次执行镜像拉取、标签校验、所有权激活、核心服务、Writer 和烟测。

```sh
make deploy V2_TARGET_ENV=/absolute/path/to/target.env
```

发布与部署脚本不会连接 Production；目标选择只来自操作者提供的环境文件和本地 Harness 配置。

## 安全边界

- 不提交 API Key、OAuth、Webhook、SQLite、日志、备份或生成快照。
- Admin 默认仅通过项目范围 Docker 只读代理读取运行状态；最终切换才显式启用直连 Socket。
- 不从本仓库修改 `/opt/cliproxyapi` 或外部代理服务。
- `/health` 和 `/v1/models` 不能替代真实 `/v1/responses` SSE 验收。

详见 [架构](docs/architecture.md)、[开发](docs/development.md)、[部署](docs/deployment.md) 和 [备份恢复](docs/backup-and-restore.md)。
