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
- 个人使用中心按 7/30/90 个自然日展示总用量及“模型 + 推理强度”组合趋势，不建立第二套用量事实表。
- 账号故障迁移支持 `off` 与 `active`，批量迁移采用原子路由更新。
- Edge 固定主机端口，Gateway 蓝绿切换；已有 SSE 请求在原槽排空，新请求进入新槽。
- 四个镜像使用源码摘要和不可变标签发布，目标机本地拉取并校验镜像标签。
- `cpac` 可为全新单机目标一次性初始化 SQLite 和主密钥；后续 Production/Test 升级必须保留并复用这组权威状态。

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

### 统一安装/升级入口

目标机只需安装一次 `cpac`。安装器从最新 GitHub Release 下载 `cpac-linux-amd64`，并使用同一 Release 的 `SHA256SUMS` 校验后写入 `/usr/local/bin/cpac`：

```sh
curl -fsSL https://github.com/Alfonsxh/codex-cpa-cluster/raw/refs/heads/main/scripts/install-cpac.sh | sudo sh
```

之后安装和升级统一使用同一个命令：

```sh
sudo cpac deploy
```

首次运行时，`cpac` 检测不到本机配置，会提示输入访问域名：

```text
$ sudo cpac deploy
请输入访问域名: qdata.example.com
```

域名经过严格的 FQDN 格式校验和规范化后，以单一 `CPA_DOMAIN=<域名>` 配置写入 `/etc/cpac/config.env`。配置通过同目录临时文件原子替换，由 `root` 所有且权限为 `0600`，不会把任意用户输入作为 Shell 执行。后续再次执行 `sudo cpac deploy` 时自动读取该域名，不再重复询问，并根据本机状态自动选择“首次安装”或“原地升级”。无交互环境可显式传入域名：

```sh
sudo cpac deploy --domain qdata.example.com
```

如果配置尚不存在、标准输入也不是终端，并且没有提供 `--domain`，命令必须失败并给出明确提示，不能猜测域名。已有部署不能通过普通 `--domain` 静默覆盖域名；改域名使用单独的 `cpac domain set <新域名>` 命令并要求交互确认，避免升级时误改线上入口。

```text
sudo cpac deploy
        |
        +-- 没有已记录域名? -- 是 --> 提示输入并安全保存
        |                         \--> 无交互且未传 --domain：失败
        |
        +-- 没有部署状态? ------ 是 --> 首次安装
        |                         \--> 初始化、启动、烟测
        |
        \-- 已有部署状态 --------> 备份、拉取、蓝绿升级、烟测
                                  \--> 失败则保留原版本并报告
```

`cpac deploy` 从 GitHub Release 下载归档、发布环境和校验文件，在校验 SHA-256 后才使用不可变镜像。首次安装先在同文件系统的临时根目录创建两份 SQLite、主密钥、空 Gateway 快照和随机管理员凭据，完成后再原子发布为 `/opt/codex-cpa-cluster`；升级先生成 `/var/backups/cpac/` 备份，再执行 Gateway 蓝绿排空、Control/Web 更新和烟测。失败时保留当前槽并尝试恢复上一发布配置。

首次安装没有固定的默认管理员密码。`cpac` 生成高强度随机管理凭据并写入加密控制面；交互部署会在烟测成功后显示一次，自动化部署则保留在 root-only 待领取文件中，操作者随后在交互终端执行 `sudo cpac admin-key claim`。升级既不显示也不重置已有凭据。

域名配置只描述本站入口，不自动修改 DNS、TLS 证书或仓库范围外的 Nginx/外部代理服务。它们必须在执行 `cpac deploy` 前已将域名转发到 Edge 的本机端口。

### 底层部署方式

1. 从 `.env.example` 生成仓库外的目标机私有 `target.env`；它是正式控制面部署参数的唯一来源，不提交目标地址或 Registry 凭据。
2. 从同一发布包更新目标目录内的 `docker-compose.yml` 与 `release-manifest.json`，并确认两份 SQLite、匹配的主密钥、Gateway 快照、活动槽文件和可写日志目录已经存在；部署脚本拒绝符号链接运行目录和不匹配的 Compose 副本。
3. 使用不可变的 `control`、`web`、`gateway`、`edge` 镜像。
4. 显式填写目录/Writer 停止确认；示例文件不会预置任何切换授权。
5. 依次执行镜像拉取、标签校验、所有权激活、Gateway 蓝绿排空、Admin/Web、Writer 和烟测。

```sh
make deploy TARGET_ENV=/absolute/path/to/target.env
```

`cpac` 在目标机本地调用同一套 `scripts/deploy-target.sh` 动作；直接使用 `make deploy` 仍只适用于已经初始化的目标。CI 和发布脚本不会连接 Production；目标选择只来自操作者提供的环境文件和本地 Harness 配置。
Edge 持有唯一公开端口，镜像或 Compose 配置变化时必须额外确认维护窗口；普通 Control、Web、Gateway 发布不会重建 Edge。

## 安全边界

- 不提交 API Key、OAuth、Webhook、SQLite、日志、备份或生成快照。
- Admin 在已初始化目标上直接挂载 Docker Socket 以管理账号容器；Web、Gateway 和 Edge 不获得该权限。
- 不从本仓库修改 `/opt/cliproxyapi` 或外部代理服务。
- `/health` 和 `/v1/models` 不能替代真实 `/v1/responses` SSE 验收。

详见 [架构](docs/architecture.md)、[开发](docs/development.md)、[部署](docs/deployment.md) 和 [备份恢复](docs/backup-and-restore.md)。
