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
state/control-plane.sqlite3
state/usage.sqlite3
secrets/control-plane.key
state/gateway/
state/edge/
logs/gateway/
```

主密钥必须与控制库匹配。部署工具不会初始化新目标、导入退役 JSON 或替换 OAuth。

## 镜像发布

```sh
make verify
make images VERSION=v2.0.0 PLATFORM=linux/amd64
make publish VERSION=v2.0.0 IMAGE_PREFIXES=ghcr.io/owner
make package VERSION=v2.0.0
```

四个组件均按源码摘要构建不可变标签。`cpa-releasectl` 负责 Manifest、发布描述、归档安全和隐私检查。

## Test 应用顺序

目标环境文件从 `.env.example` 生成并保存在仓库外。执行：

```sh
V2_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh config
V2_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh pull
V2_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh verify-images
V2_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh activate
V2_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh up-core
V2_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh up-writers
V2_ENV_FILE=/absolute/path/to/test.env sh scripts/deploy-target.sh smoke
```

通知带外部副作用，单独执行 `up-notifications`。

## 上线验收

切换前后分别记录：

1. 两份 SQLite 的 `quick_check`、Schema 版本和关键行数。
2. 四个镜像的不可变引用、组件标签和源码摘要。
3. `/__health`、公开路径 404/401、内部快照接口。
4. 同一个真实 API Key 的 `/v1/models` 和 `/v1/responses` 非流式请求。
5. SSE 的 created、delta、completed 与 `[DONE]`，以及切槽期间已有请求排空。
6. Admin、Portal、使用中心与浏览器矩阵。

Production 不由 CI 连接。只有操作者在目标机本地选择版本、备份并应用；Pod 或容器健康不等同于业务验收。
