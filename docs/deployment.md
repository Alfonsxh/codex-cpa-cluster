# 部署与发布

本文区分两个容易混淆的动作：

- **发布**：维护者把源码构建成不可变镜像和 GitHub Release。
- **部署**：操作者在目标机拉取已发布产物并更新本地服务。

发布过程不会连接 Test 或 Production 主机。

## 生产目录

生产根目录只保留：

```text
.env
docker-compose.yml
compose.accounts.yml
bin/codex-cpa
state/
secrets/
auth/
configs/
logs/
management/
backups/
```

`admin/`、`scripts/`、`edge/`、`web/`、`gateway/`、`portal/` 和 `dashboard/` 是构建上下文或镜像内容，不是稳态运行依赖。

`.env` 只包含部署根路径、实例名、Compose 项目名和 Docker 网络名。版本、端口、组件镜像、
CPA 更新通道及候选/已应用状态保存在 `state/control-plane.sqlite3`；
`state/compose.env` 是控制面按需生成的私有 Compose 投影。

生产 `docker-compose.yml` 只声明镜像和运行拓扑，不包含 `build`。源码仓库中的 `docker-compose.dev.yml` 才提供本地构建上下文，发布包和生产部署不会加载它。

## 本地质量门禁

GitHub Actions 自动触发默认关闭。任何发布都必须先在受信任工作站运行：

```bash
make verify
```

它执行 Python、JavaScript、Lua 和 Shell 检查、单元测试、公开发布边界检查、Compose 校验与 `git diff --check`。

## 维护者发布

要求：

- Docker Buildx 可用。
- `docker login ghcr.io` 已写入本机 Docker Credential Store。
- GitHub CLI `gh` 已登录，并具备仓库 Release 权限。
- 工作区干净，版本 Tag 指向当前提交。

发布命令：

```bash
make release VERSION=v1.1.0 IMAGE_PREFIX=ghcr.io/alfonsxh
```

发布器按以下顺序执行：

1. 校验版本、分支、Git 状态和远端 Tag。
2. 运行 `make verify`。
3. 创建或验证指向当前提交的本地版本 Tag。
4. 构建并发布活动的 Admin/Web/Gateway/Edge、隔离 Test 使用的四个 Go v2 候选组件，以及兼容用的
   Release metadata 镜像。
5. 生成发布包、组件指纹清单和 `SHA256SUMS`。
6. 推送 Git Tag。
7. 创建草稿 GitHub Release、上传附件，最后发布。

发布中断后可以重复执行：已存在的镜像版本标签必须与组件指纹完全一致，否则发布器会拒绝覆盖。

## 发布产物

| 产物 | 用途 |
| --- | --- |
| `codex-cpa-cluster-vX.Y.Z.tar.gz` | 当前部署器使用的版本化发布包 |
| `codex-cpa` | 统一的 `install` / `upgrade` 入口 |
| `release-vX.Y.Z.json` | revision、组件指纹和镜像位置 |
| `SHA256SUMS` | 发布附件完整性校验 |
| GHCR Admin/Web/Gateway/Edge 镜像 | 当前生产运行时 |
| GHCR `v2-control/v2-web/v2-gateway/v2-edge` 镜像 | 隔离 Test 和显式目标机候选流程拉取；其中 v2 Web 为 Gin + React Portal/Native/Admin/Usage 静态资产；v1 升级器不自动应用 |
| GHCR metadata 镜像 | 兼容当前 Admin 的版本更新检查 |

当前发布包仍包含升级器用于校验组件指纹的源码输入，但安装完成后的生产目录不会保留这些源码。后续可以在不改变运行拓扑的前提下继续收敛发布包。

Go v2 候选镜像与活动镜像使用相同的不可变源码指纹、语义版本和防覆盖规则。它们由独立的
`docker-compose.v2.yml` 和 `deploy-v2-target.sh` 显式应用，不进入 v1 自动升级投影。发布成功只证明候选
可复现，不代表获得公网端口或 Writer 所有权。

## Go v2 目标机候选与切换

目标机流程必须先用 `scripts/v2-target-data.py snapshot` 通过 SQLite online backup 创建两份同源隔离
副本，再对两份副本执行 `migrate-usage` 和状态差异比较。候选环境把 Docker socket 指向 `/dev/null`，
默认只在备用回环端口验证真实旧数据和专用测试 Key；不启动 Collector、Quota、通知等真实副作用 Worker。
需要从受控 LAN 直接验收页面时，可显式把 `V2_PUBLIC_BIND_ADDRESS` 和 `V2_PUBLIC_PROBE_HOST` 设置为
目标机 LAN 地址；内部探针端口仍固定绑定 `127.0.0.1`，该设置也不会切换宿主 Nginx 或 Writer。

复制 `v2-compose.env.example` 为目标机私有的 `v2-target.env`，填入发布描述器中的四个不可变镜像、
隔离根目录、备用端口和现有业务 CPA 网络。候选顺序为：

```bash
V2_ENV_FILE=/path/to/v2-target.env sh scripts/deploy-v2-target.sh pull
V2_ENV_FILE=/path/to/v2-target.env sh scripts/deploy-v2-target.sh config
V2_ENV_FILE=/path/to/v2-target.env sh scripts/deploy-v2-target.sh activate
V2_ENV_FILE=/path/to/v2-target.env sh scripts/deploy-v2-target.sh up-core
V2_ENV_FILE=/path/to/v2-target.env sh scripts/deploy-v2-target.sh smoke
```

显式激活 Lease 默认使用两分钟的一次性接管窗口，覆盖 Gateway 健康检查和 Admin 启动依赖；Admin
成功 Join 后立即改用自身 30 秒 Lease 和心跳续约。目标机若覆盖
`V2_OWNERSHIP_ACTIVATION_TTL`，取值仍必须落在所有权 CLI 的 5 秒到 5 分钟边界内。

Registry 不可达时，发布工作站可以把同一批 Linux 镜像以 `docker save` 传到目标机并用
`docker load` 导入；导入后必须执行 `verify-images`，用发布清单逐个校验本地镜像的组件名和源码指纹，
通过后才允许继续 `config`/`activate`。这一离线入口不会在目标机构建应用镜像，也不能跳过清单校验。

正式接管时先停止旧 Admin、Collector 和日志维护，确认没有旧 Writer/队列消费者后，对在线根目录创建
新备份并迁移 usage v10。`activate` 对已有 Lease 要求精确匹配过期的 Owner/Generation；首次遗留环境
还必须设置 `V2_BOOTSTRAP_MODE=legacy-cutover` 并让 `V2_CONFIRM_LEGACY_WRITERS_STOPPED` 精确等于根目录。
核心通过后才依次执行 `up-writers` 和 `up-notifications`。宿主 Nginx 的 upstream 切换不在脚本中，必须在
API Key、SSE、取消、内部边界、数据摘要及 v2→v1→v2 演练通过后单独执行，并保留原 v1 Edge/Gateway
作为热回滚入口。

## 目标机安装与升级

目标机不执行 Git clone，也不构建应用镜像。统一入口按以下信任链工作：

```text
GitHub Release
  ├── SHA256SUMS ──校验──▶ 发布包 / 发布描述器
  ├── 发布描述器 ─────────▶ version / revision / image prefix
  └── 发布包组件指纹 ─────▶ Registry 不可变镜像标签与 OCI label
```

首次安装：

```bash
sudo ./codex-cpa install v1.1.0 --profile /path/to/profile.json
```

既有环境升级：

```bash
sudo /opt/codex-cpa-cluster/bin/codex-cpa upgrade v1.2.0
```

网络受限或使用内部 Release 分发时，可以提供同目录下的三个已下载附件：

```bash
sudo ./codex-cpa install v1.1.0 \
  --archive ./codex-cpa-cluster-v1.1.0.tar.gz
```

默认从描述器读取镜像前缀。若已把同一批带指纹标签的镜像复制到内部 Registry，可追加 `--image-prefix registry.example.com/team`。

升级器先把目标组件写为 SQLite 的 `pending` 部署并生成 Compose 投影，启动后执行数据库、
页面、Gateway 和路由验收，成功后再转为 `applied`。Admin 在此期间仍把上一条 `applied`
版本显示为当前版本。业务 CPA 若需因 Compose 变化而重建，会逐个显式传入发布前容器的
不可变 image ID；应用升级不会跟随 `:latest`，也不会把某个账号的局部 CPA 更新扩散到
其他账号。

## 凭据边界

- GitHub 和 GHCR 凭据只保存在发布工作站。
- 目标机 Registry 凭据只保存在目标机 Docker Credential Store。
- 发布包不包含域名、账号、Key、OAuth、Webhook、私网地址或生产路径。
- GitHub 不保存 SSH 私钥，也不主动部署生产环境。

## 不采用的方案

- **Watchtower**：无法执行数据库检查、Gateway 蓝绿切换和数据不变量验收。
- **GitHub Actions 部署生产**：需要额外的远程凭据，同时当前没有可用 Runner 额度。
- **Kubernetes**：与单机 SQLite、OAuth 文件和 Docker 动态账号模型不匹配。
