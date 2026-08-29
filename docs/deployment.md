# 部署

## 运行模型

正式拓扑只使用 `docker-compose.yml`，镜像固定为：

- `codex-cpa-control`
- `codex-cpa-web`
- `codex-cpa-gateway`
- `codex-cpa-edge`

Control 镜像包含 Admin 与各 Worker 二进制；不同容器使用不同入口启动，以便独立健康检查、重启和最小影响更新。

## 目标前置条件

目标目录必须已经存在，并至少包含：

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

`docker-compose.yml` 与 `release-manifest.json` 必须来自本次选择的同一个发布包。主密钥必须与控制库匹配，`active-gateway.conf` 必须只选择 `blue` 或 `green`，`logs/gateway/` 必须允许镜像内 UID `10001` 写入。部署工具不会初始化新目标、导入退役 JSON、替换 OAuth，或沿符号链接读写运行数据。

## 镜像发布

```sh
make verify
make images VERSION=v2.0.0 PLATFORM=linux/amd64
make publish VERSION=v2.0.0 IMAGE_PREFIXES=ghcr.io/owner
make package VERSION=v2.0.0
```

四个组件均按源码摘要构建不可变标签。`cpa-releasectl` 负责 Manifest、发布描述、归档安全和隐私检查。目标先用镜像标签完成非执行式身份校验，只有 Control 镜像与不可变标签一致后，才允许运行其中的 `cpa-releasectl` 读取 Manifest。

## Test 应用顺序

目标环境文件从 `.env.example` 生成并保存在仓库外。四个镜像必须使用发布描述中的 `:sha256-<源码摘要>` 标签。`CPA_CONFIRM_DEPLOY_ROOT`、首次接管确认和 Edge 维护确认在示例中故意留空，必须由操作者针对本次目标显式填写。正式控制面的端口、镜像、Compose 身份和所有权参数只来自该 `target.env`；配置中心生成的 `state/compose.env` 只服务业务 CPA 账号容器。

普通升级执行：

```sh
CPA_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh config
CPA_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh pull
CPA_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh verify-images
CPA_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh activate
CPA_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh up-core
CPA_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh up-writers
CPA_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh smoke
```

通知带外部副作用，单独执行 `up-notifications`。

`up-core` 先确认非活动 Gateway 已无遗留请求，再更新该槽；需要更新活动槽时，Edge 先把新请求切到已验证的新槽，旧槽的 `/__stats` 归零后才重建。排空超时会保留旧容器和已有 SSE 并使部署失败，重试仍会先等待它排空。Edge 自身镜像或 Compose 配置变化时，必须设置 `CPA_ALLOW_EDGE_RECREATE=true`，并用 `CPA_CONFIRM_EDGE_MAINTENANCE` 精确重复目标目录；该操作有明确的单端口维护窗口。

## 上线验收

切换前后分别记录：

1. 两份 SQLite 的 `quick_check`、Schema 版本和关键行数。
2. 四个镜像的不可变引用、组件标签和源码摘要。
3. `/__health`、公开路径 404/401、内部快照接口。
4. 同一个真实 API Key 的 `/v1/models` 和 `/v1/responses` 非流式请求。
5. SSE 的 created、delta、completed 与 `[DONE]`，以及切槽期间已有请求排空。
6. Admin、Portal、使用中心与浏览器矩阵。

Production 不由 CI 连接。只有操作者在目标机本地选择版本、备份并应用；Pod 或容器健康不等同于业务验收。
