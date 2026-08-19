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
4. 构建并发布 Admin、Web、Gateway、Edge 和兼容用的 Release metadata 镜像。
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
| GHCR 组件镜像 | 生产运行时 |
| GHCR metadata 镜像 | 兼容当前 Admin 的版本更新检查 |

当前发布包仍包含升级器用于校验组件指纹的源码输入，但安装完成后的生产目录不会保留这些源码。后续可以在不改变运行拓扑的前提下继续收敛发布包。

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

## 凭据边界

- GitHub 和 GHCR 凭据只保存在发布工作站。
- 目标机 Registry 凭据只保存在目标机 Docker Credential Store。
- 发布包不包含域名、账号、Key、OAuth、Webhook、私网地址或生产路径。
- GitHub 不保存 SSH 私钥，也不主动部署生产环境。

## 不采用的方案

- **Watchtower**：无法执行数据库检查、Gateway 蓝绿切换和数据不变量验收。
- **GitHub Actions 部署生产**：需要额外的远程凭据，同时当前没有可用 Runner 额度。
- **Kubernetes**：与单机 SQLite、OAuth 文件和 Docker 动态账号模型不匹配。
