# Codex CPA Cluster

<p align="center">
  <img src="portal/assets/codex-cpa-cluster-logo.svg" width="420" alt="Codex CPA Cluster">
</p>

<p align="center">
  面向 CLIProxyAPI 的自托管多账号控制面，为团队提供统一 API 入口、用户 Key、账号路由、额度与用量管理。
</p>

<p align="center">
  <a href="https://github.com/Alfonsxh/codex-cpa-cluster/releases"><img alt="GitHub Release" src="https://img.shields.io/github/v/release/Alfonsxh/codex-cpa-cluster?display_name=tag"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/github/license/Alfonsxh/codex-cpa-cluster"></a>
  <img alt="Docker Compose" src="https://img.shields.io/badge/deploy-Docker%20Compose-2496ED?logo=docker&logoColor=white">
  <img alt="Python" src="https://img.shields.io/badge/runtime-Python%203-3776AB?logo=python&logoColor=white">
</p>

## 它解决什么问题

当多个 CLIProxyAPI 账号需要由一个团队共享时，直接暴露每个实例会带来 Key 分发、账号切换、额度统计和运维入口分散等问题。Codex CPA Cluster 在业务实例前增加独立控制面与网关：

- 一个外部 Key 可在多个 CPA 账号之间切换路由。
- 支持用户、团队、标签、周额度、用量统计和账号自动切换。
- 提供管理中心、用户使用中心、客户端配置导出和企业微信通知。
- 品牌、身份域名、Key 前缀、默认模型和出口代理可在线配置。
- Gateway 只公开批准的 API 路径，上游管理接口不会意外暴露。
- 稳定 Edge 与蓝绿 Gateway 隔离控制面更新，保护长时间流式请求。

## 架构概览

```text
                           ┌──────────────┐
HTTPS / API Client ───────▶│ Stable Edge  │
                           └──────┬───────┘
                   ┌──────────────┴──────────────┐
                   ▼                             ▼
             ┌──────────┐              ┌─────────────────┐
             │ Web / UI │              │ Active Gateway  │
             └────┬─────┘              └────────┬────────┘
                  ▼                              ▼
             ┌──────────┐              ┌─────────────────┐
             │  Admin   │──── Docker ─▶│ CPA containers  │
             └────┬─────┘              └─────────────────┘
                  ▼
       SQLite / OAuth / secrets / logs
```

系统以单机 Docker Compose 为交付边界。SQLite、OAuth 文件和运行时渲染结果保存在部署目录；Admin、Web、Gateway 和 Edge 使用独立镜像。详细设计见[架构说明](docs/architecture.md)和 [ADR 0001](docs/adr/0001-stable-edge-blue-green-gateway.md)。

## 快速开始

生产安装要求 Linux、Docker、Docker Compose v2 和 Python 3.8+。下载目标版本的统一入口后，只需指定版本：

```bash
VERSION=v1.1.0
curl -fL "https://github.com/Alfonsxh/codex-cpa-cluster/releases/download/$VERSION/codex-cpa" \
  -o codex-cpa
chmod +x codex-cpa
sudo ./codex-cpa install "$VERSION" \
  --profile /path/to/profile.json
```

安装器自动下载并校验 `SHA256SUMS`、发布描述器和归档，拉取与源码指纹一致的镜像，初始化 SQLite 后启动服务。生产机不需要 Git 仓库和应用源码。

默认入口只绑定本机：

| 功能 | 地址 |
| --- | --- |
| Portal | `http://127.0.0.1:18317/` |
| 使用中心 | `http://127.0.0.1:18317/usage/` |
| 管理中心 | `http://127.0.0.1:18317/admin/` |
| API | `http://127.0.0.1:18317/v1` |

首次启动和生产 TLS 配置见[快速开始](docs/getting-started.md)。新环境不会自动创建示例账号或用户。

## 文档

| 文档 | 内容 |
| --- | --- |
| [快速开始](docs/getting-started.md) | 初始化、首次登录和账号接入 |
| [架构说明](docs/architecture.md) | 组件职责、数据边界和请求路径 |
| [部署与发布](docs/deployment.md) | 生产目录、镜像发布和安全边界 |
| [升级指南](docs/upgrade.md) | 版本兼容、备份、升级与回滚 |
| [备份与恢复](docs/backup-and-restore.md) | SQLite、主密钥和 OAuth 的成对备份 |
| [故障排查](docs/troubleshooting.md) | 常见启动、健康检查和路由问题 |
| [本地开发](docs/development.md) | 依赖、测试和贡献流程 |
| [配置中心](docs/configuration-center.md) | 配置项、SQLite 权威存储和生效方式 |

## 发布模型

项目不依赖 GitHub Actions Runner 发布。维护者在已登录 GHCR 和 GitHub CLI 的受信任工作站执行：

```bash
make verify
make release VERSION=v1.1.0 IMAGE_PREFIX=ghcr.io/alfonsxh
```

发布器验证源码、测试和公开边界，构建版本化镜像，生成带组件指纹的发布包及校验和，并创建 GitHub Release。它不会连接任何生产主机。完整流程见[部署与发布](docs/deployment.md)。

## 数据与安全

- `state/control-plane.sqlite3` 是低频控制面数据的唯一事实来源。
- `state/usage.sqlite3` 独立保存高频用量事件。
- 管理密钥、Webhook 和代理地址使用 AES-256-GCM 加密存储。
- `secrets/control-plane.key` 必须与控制面数据库成对备份。
- OAuth 文件保存在 `auth/`，不会进入源码、应用镜像或发布包。
- Admin 当前需要 Docker Socket 管理动态 CPA 容器；请仅通过 HTTPS 暴露入口并保护管理凭据。

发现安全问题请不要创建公开 Issue，参阅[安全策略](SECURITY.md)。

## 开发

```bash
python3 -m pip install -r requirements.txt
cp .env.example .env
python3 scripts/cliproxy.py --root "$PWD" render
make dev-build
make verify
```

提交变更前请阅读[贡献指南](CONTRIBUTING.md)。项目采用 [MIT License](LICENSE)。
