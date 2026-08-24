# 本地开发

## 环境

- Python 3.12（生产代码兼容 Python 3.8+）
- Node.js 22
- Go 1.25（以 `go.mod` 为准）
- Lua 5.4
- Docker 与 Docker Compose v2

安装 Python 依赖：

```bash
python3 -m pip install -r requirements.txt
cd frontend
npm ci --registry=https://registry.npmmirror.com
cd ..
```

现网 v1 前端仍保留原生 JavaScript；新的 Go v2 Portal、Native、Admin 和 Usage 均位于 `frontend/`，采用 React、TypeScript、
Vite、Ant Design、React Router、TanStack Query、React Hook Form 和 Zod。两套前端在切流验收前并存，
新构建暂不替换现网静态资源。

Go 全量迁移采用 Gin、Cobra、Viper、Zap、sqlx、Redigo、Resty v2、fsnotify、renameio、robfig/cron、
Pond v2 和 Moby 官方 Go SDK；选型与兼容边界见
[ADR 0003](adr/0003-go-migration-framework-stack.md)。Go v2 Gateway、Stable Edge、Web、Admin 和 Workers 已接入独立 Test
Compose、完整目标机候选 Compose 和候选镜像发布；宿主入口和 Writer 所有权仍须现场门禁通过后显式切换，现网在切换前继续使用 OpenResty
Gateway/Edge 和 Nginx Web。

如果宿主机没有 Lua，但 Docker daemon 正在运行，统一验证脚本会自动使用 Gateway 的 OpenResty 镜像执行 Lua 测试。

## Go v2 与 Python v1 Writer Lease

所有可变更的 Go Admin/Worker 都拒绝在未显式激活所有权的目标上启动。复制 Test 根目录后，先用
Cobra 所有权命令激活 `go-v2`，再在 30 秒 TTL 内启动第一个 Worker；后续进程会共同续约运行时 Lease，
同时排他持有自己的 Worker Scope：

```bash
go run ./cmd/ownership --root /path/to/existing-test-root status

go run ./cmd/ownership --root /path/to/existing-test-root activate \
  --owner go-v2 \
  --allow-empty-bootstrap \
  --confirm-owner go-v2
```

`--allow-empty-bootstrap` 仅用于隔离 Test 副本。接管已有 Lease 时必须先停止旧 Writer、记录当前
Owner 与 Generation、等待其过期，再同时通过 `--expected-owner` 和 `--expected-generation` 精确确认
前一代所有权；任一字段不匹配都会拒绝激活，不得通过直接修改 SQLite 绕过命令。普通 Worker 只有
Join 权限，无法自行创建 `runtime-writer`。例如接管已过期的 `python-v1` 第 7 代：

```bash
go run ./cmd/ownership --root /path/to/existing-test-root activate \
  --owner go-v2 \
  --expected-owner python-v1 \
  --expected-generation 7 \
  --confirm-owner go-v2
```

过渡期 Python v1 Admin、Collector 和 Log Maintenance 使用相同的 SQLite Token + Generation fencing
协议；Admin 同时持有 `admin`、`quota`、`account-failover`、`notifications`。Lease 丢失会直接终止
Python 进程。Go 入口先以 `OpenExisting` 取得 Lease，之后才执行控制面初始化；取得两层 Lease 后，
每一个 Go 控制面写事务还会在同一 SQLite 事务和跨进程文件锁内重新核对 runtime/worker 的 Owner、
Token、Generation 和过期时间。即使旧进程在心跳发现丢失之前恢复运行，也无法提交控制面写入。
同一精确代际栅栏还包住用量库写入、Gateway 快照/heartbeat、failover 审计、日志轮转和 Webhook
发送；激活探测及发送后的状态 Patch 在锁外完成，避免嵌套锁。Webhook 远端成功后若进程在状态落库
前崩溃，仍可能至少一次投递，这是企业微信协议没有幂等键的已知边界。
`--health` 路径始终 query-only，不需要所有权。

Gateway/Edge 不申请 Writer Lease，API Key/Codex 数据面可在控制面切换期间继续运行。Production 的首次
v1 激活、v1→v2 转移和 v2→v1 回滚必须由发布 Runbook 驱动；本地命令不得指向 Production 根目录。

## 本地 Compose

基础 Compose 是生产镜像拓扑；本地构建必须显式叠加开发 override：

```bash
cp .env.example .env
export DEPLOY_ROOT="$PWD"
make dev-build
python3 scripts/cliproxy.py --root "$PWD" render
make dev-up
```

等价的 Compose 文件顺序是：

```text
docker-compose.yml        # 生产运行拓扑，不含 build
  + docker-compose.dev.yml  # 本地 Dockerfile/build args
  + compose.accounts.yml    # 运行时生成的业务 CPA
```

`make dev-build` 在没有本地运行状态时使用 `compose.env.example`；`render` 会创建本地
SQLite 并生成私有的 `state/compose.env`，`make dev-up` 只读取该生成投影。

不要把 `docker-compose.dev.yml` 复制到生产目录，也不要让生产部署执行 `docker compose build`。

### Go v2 隔离数据面

`docker-compose.v2-test.yml` 使用固定项目名 `codex-cpa-v2-test`。Makefile 显式传递 `-p`，因此根目录
`.env` 的 `COMPOSE_PROJECT_NAME` 不能覆盖它。测试上游和两套 Gateway 只加入内部 Bridge；只有不读取
Key、OAuth 或 SQLite 的 Edge 额外加入入口 Bridge，并把 `28317/28319` 绑定到回环地址。

```bash
make v2-test-build
make v2-test-up
make v2-test-smoke
make v2-test-down
```

Smoke 覆盖健康检查、Go Web 的 Portal/React Admin 静态入口、无 Key 的 `401`、专用夹具 Key 的 `/v1/models`、`/v1/responses` SSE 完整事件和
Edge 实际加载的蓝槽。失败时先保留容器收集日志；清理必须带同一个固定项目名，不能对根目录执行宽泛
的 `docker compose down`。

另外两组显式 Test 门禁不会连接 Production：

```bash
make v2-lease-rehearsal
make v2-test-faults
make v2-worker-lease-rehearsal
make v2-worker-process-rehearsal
```

`v2-lease-rehearsal` 在临时 SQLite 根目录中让 Python v1、Go v2、Python v1 依次取得第 1/2/3 代
runtime 与 worker Lease，并验证两边的陈旧代际都无法续约或写入。`v2-test-faults` 使用独立
`codex-cpa-v2-fault-test` Compose 项目、复制到临时目录的快照和回环 `29317/29319`，验证上游断开
返回受控 `502`、无效 Key 仍为 `401`、损坏鉴权快照在新鲜度到期后 fail-closed `503`、恢复快照后
自动恢复、无效 Edge 选择保留最后槽位，以及蓝切绿时已建立 SSE 完整排空。脚本退出时只清理自己的
Compose 项目和临时夹具。
`v2-worker-lease-rehearsal` 在另一个临时根目录中并发持有 Admin、Collector、Quota、Failover、
Notifications 和 Log Maintenance 六个 Scope，验证重复 Worker 被拒绝、每个 Worker 都能执行受栅栏
保护的状态写入、正常停止释放，以及逻辑 TTL 后整组推进到回滚代际。
`v2-worker-process-rehearsal` 使用 Python v1 只初始化一次性 schema v6/v10 Test 根目录，随后构建并
启动六个真实 Cobra Worker。它验证重复 Admin 被拒绝、Collector `SIGKILL` 后旧 Scope 未到期时不能
重启、到期重启仅推进 Collector Generation、前后耐久 checkpoint 一致，并最终让 Python v1 接回
runtime 和六个 Worker Scope。脚本只清理自己由 `mktemp` 创建的目录。

## 统一验证

```bash
make verify
```

该命令覆盖 Python、JavaScript、Lua 和 Shell 检查、单元测试、公开发布边界、Compose 配置与 Git diff 检查。

也可以单独运行：

```bash
find cmd internal -type f -name '*.go' -exec gofmt -l {} +
go vet ./...
go test ./...
go test -race ./...
npm --prefix frontend run typecheck
npm --prefix frontend test
npm --prefix frontend run build
python3 -m unittest discover -s tests
node tests/test_token_usage.js
node tests/test_admin_monitor_interactions.js
node tests/test_admin_view_state.js
lua tests/test_gateway_state.lua
lua tests/test_request_gate.lua
```

## Go v2 Gateway 本地运行

准备由测试数据生成或人工构造的非生产 auth/quota/heartbeat 快照目录后，可在备用端口运行：

```bash
go run ./cmd/gateway \
  --public-address 127.0.0.1:28317 \
  --internal-address 127.0.0.1:28319 \
  --snapshot-dir /path/to/test-snapshots \
  --access-log /tmp/cpa-go-gateway-access.tsv
```

也可以使用 `CLIPROXY_GATEWAY_PUBLIC_ADDRESS`、
`CLIPROXY_GATEWAY_INTERNAL_ADDRESS`、`CLIPROXY_GATEWAY_SNAPSHOT_DIR`、
`CLIPROXY_GATEWAY_REFRESH_INTERVAL`、`CLIPROXY_GATEWAY_ACCESS_LOG` 和
`CLIPROXY_GATEWAY_LOG_LEVEL`，或通过 `--config` 读取 YAML/JSON/TOML。

鉴权快照缺失或连续加载失败超过 5 秒时，支持的公共 API 路径返回 `503`；这属于预期的
fail-closed 行为。内部状态可从备用内部端口的 `/__internal/snapshots` 查看。不要把本地
Go v2 端口接到 Production Edge，也不要用真实用户请求做双写/镜像流量测试。

## Go v2 Stable Edge 本地运行

Go Edge 复用现有 `state/edge/active-gateway.conf` 契约，只接受下面两种指令之一：

```text
set $active_gateway_backend gateway-blue:8317;
set $active_gateway_backend gateway-green:8317;
```

在隔离 Test 中启动两套备用 Go Gateway 和一个 Web Origin 后，可把 Origin 显式指向测试端口：

```bash
go run ./cmd/edge \
  --public-address 127.0.0.1:29317 \
  --internal-address 127.0.0.1:29319 \
  --active-gateway-file /path/to/test-root/state/edge/active-gateway.conf \
  --web-target http://127.0.0.1:28080 \
  --blue-public-target http://127.0.0.1:28317 \
  --blue-internal-target http://127.0.0.1:28319 \
  --green-public-target http://127.0.0.1:28417 \
  --green-internal-target http://127.0.0.1:28419
```

Gin 只负责公网/内部路由隔离和恢复中间件，标准库 `ReverseProxy` 负责 WebSocket、SSE/分块响应、
请求取消和 Hop-by-Hop Header。Edge 不解析或记录 Authorization；公网 `8317` 屏蔽
`/__internal/*` 与 `/__stats`，内部 `8319` 只转发这两类探针。选择文件通过 fsnotify 和 500ms 轮询
加载；原子切换后新请求进入新槽位，已有连接继续由旧 Gateway 排空。无效选择文件保留最后有效槽位。
此入口同时接入夹具专用 `docker-compose.v2-test.yml` 和目标机候选 `docker-compose.v2.yml`。后者默认绑定
备用回环端口；经操作员批准后只有公网候选端口可通过 `V2_PUBLIC_BIND_ADDRESS` 显式绑定受控 LAN 地址，
内部探针端口仍只绑定回环地址。目标机使用不可变发布镜像，并由 `deploy-v2-target.sh` 分开拉起核心、
普通 Writer 和通知 Worker；它不会自行切换宿主 Nginx，现场验收前不能替代当前 Edge。

Go Edge 内部监听的 `GET /__internal/edge/slot` 返回当前进程实际加载的 `blue` 或 `green`，并带
`Cache-Control: no-store`。后续发布脚本必须等待该结果与目标槽位一致，不能只根据选择文件内容推断
切换已经生效。

## Go v2 Web 本地运行

Go Web 使用 Gin 提供 React Portal/Native/Admin/Usage 静态文件和细粒度 Admin API 反向代理；v2 镜像不再
包含 Nginx 运行时。先构建 React，再连接隔离 Go Admin：

```bash
npm --prefix frontend run build
go run ./cmd/web \
  --address 127.0.0.1:28080 \
  --portal-root "$PWD/frontend/dist/portal" \
  --admin-root "$PWD/frontend/dist/admin" \
  --usage-root "$PWD/frontend/dist/usage" \
  --admin-target http://127.0.0.1:8318
```

`/admin/api/*` 保留管理自动化所需的管理 Header；`/usage/*`、`/site-config.json`、`/branding/logo`
和兼容 `/my-keys/api` 在代理前主动清除 `Authorization` 与管理密钥。静态文件只允许从三个显式根目录
读取普通非符号链接文件，React 路由回退到各自 `index.html`，只有带构建指纹的 `assets/` 使用 immutable
缓存，稳定 Logo 与 HTML 保持 `no-cache`。公开的 `/admin/reasoning-effort-colors.css` 同样清除管理凭据。
Go Web 与 Admin 都不在 `/v1` 数据面路径上。

## Go v2 Admin 与 React 本地运行

Go Admin 当前提供会话、有界目录总览、实时通用设置、团队目录、分页用户目录与团队分配、用户创建/Key 轮换/停用/删除/密码重置、单用户周额度策略、公共站点配置与 Logo、受鉴权 Native 账号目录、账号创建/修改/重命名/停用/OAuth 清理/删除、账号/用户用量明细、全账号负载均衡，以及细粒度 Docker 服务/任务/日志 API。目标机 Compose 已就绪，但正式接管仍要求显式 Writer Lease：

```bash
CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/admin \
  --address 127.0.0.1:8318 \
  --compose-project isolated-test-project
```

另开终端启动 React：

```bash
npm --prefix frontend run dev
```

Admin Vite 把 `/admin/api` 和 `/branding` 代理到本机 `8318`。要单独调试入口页与 Native 页面，使用
`npm --prefix frontend run dev:portal`；它只代理 `/site-config.json`、`/branding` 和
`/admin/api/native-accounts`。管理密钥仅用于换取 SCS
服务端短时会话，不写入 Local Storage；变更请求携带内存中的 CSRF Token。总览页只通过一个 SQLite
只读事务请求不含身份/密钥的目录计数，通用设置页只读取和按键更新可实时生效的品牌与身份字段；团队页只请求
团队目录，用户页只请求分页用户目录和团队目录，账号页只请求账号/额度/1h 活跃数；账号或用户用量
明细仅在点击“用量”后请求，打开期间每 10 秒读取现有 `state/usage.sqlite3` 的只读快照，关闭后停止
请求并释放该 Query 缓存。单用户周额度同样只在打开“额度策略”时请求，关闭后立即释放 Query；
`PUT|DELETE /admin/api/users/quota` 只修改该用户当前策略，Mutation 完成后不刷新分页目录。
账号用量的 `window=since_reset` 从 `runtime_state(official_quota)` 读取该账号的周额度边界；只有缺少账号
周窗口或边界无效时才返回 `409 usage_window_unavailable`，不会用固定七天窗口伪造官方周期。
通用设置页还通过专用接口上传/恢复经过内容校验的 Logo，并在轮换管理密钥成功后立即清理页面查询、
使当前管理会话失效并返回重新登录界面；密钥和 Logo 原始字节均不写浏览器存储。运行维护页只在进入页面后
请求精确 Compose Project 标签下的服务、CPA 镜像状态和 Pond 有界任务，日志抽屉打开后才
读取对应目标最近 200 行并每 5 秒刷新。日志在 2 MiB 截断前完成 Bearer、API Key 与 OAuth 字段脱敏。
兼容 `/admin/api/logs` 保留 v1 的成功字段 `exit_code: 0`，并额外返回 `truncated`，让页面能明确提示
有界输出而不把截断误判成命令失败。
任务使用单 Worker、16 个等待槽和最近 60 条内存目录；队列满返回 `429`，同目标冲突返回 `409`。
停止账号或服务前，React 必须先调用 `/admin/api/operations/impact`；影响查询失败时确认按钮保持锁定。
只接受本地 Unix Docker Socket；Edge、Gateway 和 Admin 在后端只能读日志，不能通过运维 API 停止或重启。
用户创建和 Key 轮换只有在新鉴权快照被备用 Gateway 探针确认后才返回完整 Key；停用和删除也等待
撤销快照生效。激活失败会恢复精确的控制面行并发布回滚快照。创建/密码重置会以 scrypt 更新现有
schema v10 使用中心凭据，删除只清理当前登录凭据、会话和配额策略，保留历史用量与调整记录。
这些生命周期操作必须使用隔离 Test 数据、测试 Key 和备用 Gateway；Go/Python 不得同时持有用户或
路由写入权。React 一次性凭据 Mutation 使用 `gcTime=0`，只保留在当前 Modal 内存中，不写浏览器存储。
`/admin/api/settings/general` 不接受代理、配额、Worker 或部署字段；这些设置继续走各自带事务、重建或
所有权门禁的专用接口，避免一个通用表单触发跨组件副作用。
Go Admin 不创建、迁移或重建用量库，只允许会话、凭据和当前用户额度策略这些已定义的 schema v10
窄写入；历史事件、周物化与额度调整仍由唯一 Usage Collector 拥有。现网 Python Usage Collector 仍是
生产 schema v10 的唯一采集写入方。Go Admin 发现数据库版本过旧或字段不完整时会禁用相关 API，
而不会修改数据库结构。额度策略保存后由唯一 Collector 的下一份 quota snapshot 发布到 Gateway。
全账号负载均衡会写控制面路由并发布鉴权快照，只能连接具有测试数据和备用 Gateway
探针的 Test 根目录；不要对 Production 根目录执行本地开发命令。

账号更新、停用、OAuth 清理或删除会先把该账号当前路由原子迁移到“已启用、额度状态可用且仍有余量”的备用账号，等待
每个 Gateway 激活新鉴权快照，再轮询每个 Gateway 内部 `GET /__stats`。只有目标账号 in-flight 总数
归零后，Moby Runtime 才能删除、重建或在清理 OAuth 后重启旧 CPA 容器；任一 Gateway 不可用、返回损坏数据或排空超时都
fail-closed，API 返回 `409 account_requests_active`，并恢复原路由与快照。没有安全备用账号时整次操作
在写入前拒绝。账号停用成功后路由保留在备用账号；普通更新、重命名或 OAuth 清理在新容器探针成功后恢复到原账号或新账号。

每个账号操作还在控制面 `runtime_state(account_lifecycle_operation)` 保存不含 Key、OAuth 或代理明文的
单操作 Journal。Admin 取得 Writer Lease 后、开始监听前必须先执行恢复；恢复会根据权威 SQLite、操作
UUID 备份目录、文件投影、Moby 容器与 Gateway 快照确定性收敛。已提交删除的恢复路径同样必须等待
in-flight 归零，不能因为进程重启绕过排空。自定义代理密文缺失时返回
`503 account_lifecycle_not_ready` 并保留 Journal，等待安全处置。创建、更新、删除及恢复均复用现有
API Key 行，只增加或移除账号矩阵行，不生成或旋转用户 Key。

专项验证：

```bash
go test -race ./internal/accountlifecycle ./internal/runtimeops ./internal/admin ./cmd/admin
sh scripts/generate-api.sh
sh scripts/check-generated-api.sh
npm --prefix frontend run typecheck
npm --prefix frontend test
```

## Go v2 Usage Collector 本地验证

Go Collector 已实现严格 RESP2 `AUTH`/批量 `LPOP`、外部/内部 Key 哈希身份同步、事件去重、团队审计
快照、推理倍率冻结、自然周物化、个人额度聚合、quota snapshot、heartbeat、健康检查和周用量重建。
它只接管已存在且精确为 schema v10 的用量库，不自动创建或迁移数据库：

```bash
CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/collector --once

CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/collector --health

CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/collector --rebuild-weekly-usage
```

`--interval` 和 `--batch-size` 仅用于显式覆盖配置中心的 `collector.*` 值。Collector 使用 Redigo 是为了
复用成熟 RESP 编解码，同时避免通用 Redis 客户端连接时发送上游未承诺的 `HELLO`/`CLIENT` 命令；
线路级测试会锁定首条命令为 `AUTH`。

旧/Go Collector 不得同时消费同一个 `usage` 队列。差异测试必须使用复制的 schema v10 数据库和独立
测试队列，或对同一组确定性 JSON 事件夹具分别执行；生产 Collector、Compose 和流量目前没有切换。
Go Collector 对每一笔 schema v10 写事务、quota snapshot 与 heartbeat 原子替换都先核对当前
`runtime-writer` 和 `usage-collector` 的 Token/Generation；陈旧实例不能在切换后继续消费结果落库或
覆盖 Gateway 额度状态。
积压测试会在已提交批次边界模拟进程重启，并保守重放最后一个完整批次，依靠 schema v10
`event_key` 唯一约束得到确定结果。上游仅支持破坏性 `LPOP`，所以该测试不会错误宣称能恢复
“队列已弹出、SQLite 尚未提交”窗口中的进程崩溃；切换 Runbook 必须把停止旧 Collector、确认退出、
记录 checkpoint 和启动新 Collector 作为严格顺序。

## Go v2 Log Maintenance 本地验证

Go 日志维护进程使用 robfig/cron `@every` 调度，并通过框架的 `SkipIfStillRunning` 防止慢轮次重入。
它保持旧版 `runtime_state(log_maintenance)` 字段兼容，保留两份 copy-truncate 备份的默认行为；备份先在
日志同目录写完、fsync 并原子发布，日志路径、父目录或已有备份中的符号链接/非普通文件都会使该目标
失败且不截断原文件：

```bash
CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/log-maintenance --once

CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/log-maintenance --health

CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/log-maintenance \
  --interval 1m \
  --max-file-size-mb 32 \
  --backups 2
```

`--health` 只使用 sqlx query-only 连接读取已有控制数据库；目标缺失时返回失败，不创建 `state/`、
`secrets/`、SQLite、WAL 或主密钥。当前 Production Compose 仍运行 Python 日志维护容器，Go 命令只允许
对复制的 Test 根目录做兼容与压力测试。完成长时间并发写/轮转、状态差异和部署健康检查前，不替换
生产服务。
每一个 `RotateFile` 调用都在 `log-maintenance` 精确代际栅栏内完成，最终 heartbeat 仍由受栅栏保护的
控制面事务写入；实现不会把日志副本操作和状态事务嵌套到同一把锁中。

## Go v2 Official Quota Worker 本地验证

官方额度 Worker 使用 Resty v2 并行读取账号额度，沿用控制数据库中的 inherit/custom/direct 出口策略，
只选择 `auth/<account>/*.json` 中最新、启用且无符号链接的 Codex OAuth 记录。它不自动重试、不跟随
重定向，并限制响应体；持久化的 `runtime_state(official_quota)` 只含标准化额度和心跳，不含 access
token、官方 account ID 或代理凭据：

```bash
CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/quota --once

CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/quota --health

CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/quota
```

默认周期读取配置中心 `account_failover.poll_seconds`；`--interval` 仅用于 30 秒到 1 小时的显式测试
覆盖。Go 已实现 default 周窗口选择、额度过期/未来时间、OAuth、账号停用和安全余量的 fail-closed
容量标准化。当前命令只允许使用复制的 Test 根目录和测试 OAuth；不要让 Python 与 Go 对同一组真实
OAuth 同时轮询，也不要把 Go Worker 接入 Production Compose。

## Go v2 Account Failover Controller 本地验证

故障转移 Controller 使用 Cobra/Viper、Zap 和 robfig/cron，读取 `runtime_state(official_quota)`，并行执行
业务 CPA TCP 存活探针，再生成与现网 Python v1 兼容的 `runtime_state(account_failover)`。只支持 `off`
和 `active`；遗留 `observe` 会 fail-closed 为 `off`。只有额度、OAuth、运行状态、允许状态和安全余量均
明确满足时才会迁移。整批迁移使用期望路由事务、原子鉴权快照、Gateway 激活确认和失败回滚；成功后
立即重新读取近 1 小时活跃用户数，状态与审计只记录账号和数量，不记录用户邮箱：

```bash
CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/failover --once

CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/failover --health

CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/failover
```

`--health` 使用 query-only 连接且不会初始化缺失目标。对比测试必须使用复制的控制数据库、独立用量库、
备用 Gateway 探针和测试 Key；不得同时运行 Python/Go 自动迁移器，也不得连接 Production Compose、
Edge 或真实流量。

## Go v2 企业微信通知本地验证

通知迁移复用 Resty v2、robfig/cron、Cobra/Viper、Zap 和 Go 内置 tzdata。它读取现有
`runtime_state(official_quota)`、账号目录和 schema v10 用量库的近 1 小时活跃数，写入与 Python v1
兼容的 `runtime_state(notification)`；Webhook 继续使用控制面 AES-GCM 加密存储。发送请求不自动
重试、不跟随重定向，响应体和 Markdown v2 正文有显式上限：

```bash
CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/notifications --once

CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/notifications --health

CLIPROXY_ROOT=/path/to/existing-test-root \
go run ./cmd/notifications
```

Go Admin 的 `/admin/api/settings/notifications`、Webhook 保存/清除、正式额度报告和独立通道测试接口，以及 React
“通知设置”页只在备用 Admin 端口使用。页面打开时每 10 秒刷新通知状态；只有手动发送或 Worker
到期时才读取账号额度和活跃数；`/admin/api/notifications/test` 只发送带时间戳的连接测试文本，不读取
账号或用量，也不会显示为正式额度报告。Python/Go 通知 Worker 不得同时对同一个真实 Webhook 发送消息；
差异测试应使用测试机器人、复制控制数据库和独立用量库。当前未接入 Compose 或 Production。
手动发送和 Worker 发送都在各自 Admin/Notifications Worker 的精确代际栅栏中执行，Resty 不重试；
远端已接收但本地状态尚未 Patch 时崩溃仍可能导致后续至少一次投递，测试与运维判断必须保留该边界。

## v1/v2 隔离配对验证

`cmd/migration-compare` 使用 Cobra、Viper 和 Resty 对两个已经运行的隔离 Origin 发送显式测试请求。
它默认只接受 loopback、拒绝 URL 中的凭据/查询参数、要求 `0600` 且非符号链接的专用测试 Key 文件，
不重试、不跟随重定向，也不会把 Key 或请求正文写入结果：

最新版主分支 v1 使用 `docker-compose.v1-compare.yml` 与 `scripts/deploy-v1-compare-target.sh` 启动。
该拓扑只包含 Admin、Web、两个 Gateway 和 Edge，不启动 Collector、通知、日志维护或 Management；
Admin 只能通过只读 Docker 代理查看选定的隔离上游项目。控制网络为 internal，只有 Gateway 加入
与 Go v2 共用的 disposable CPA 上游网络，只有 Edge 加入端口发布网络。v1、v2 控制状态仍使用两份
独立副本，但数据面请求必须落到同一组隔离 CPA，避免把“不同 OAuth/账号运行态”误判成 Gateway
实现差异。目标根目录和上游根目录都必须带 `.v2-isolated-copy.json`；上游网络必须带
`io.codex-cpa.scope=migration-disposable`，且所有上游容器的 bind mount 都必须位于确认后的隔离根下。
脚本拒绝已知在线根目录、正式业务网络和正式 Compose 项目。
为兼容会在并发重建时静默丢失第二张网络的旧版 Docker/Compose，`up` 按服务顺序启动，并且只在
Compose project/service 标签精确匹配比较版容器时补齐声明的网络；随后移除任何非预期网络，防止 v1
比较 Gateway 因历史 attachment 重新连到正式 `cliproxy-backend`。

```bash
cp v1-compare.env.example v1-compare.env
make v1-compare-config V1_COMPARE_ENV=v1-compare.env
make v1-compare-verify-images V1_COMPARE_ENV=v1-compare.env
make v1-compare-up V1_COMPARE_ENV=v1-compare.env
```

```bash
go run ./cmd/migration-compare \
  --v1-public-url http://127.0.0.1:18317 \
  --v2-public-url http://127.0.0.1:28317 \
  --v1-internal-url http://127.0.0.1:18316 \
  --v2-internal-url http://127.0.0.1:28319 \
  --test-key-file /path/to/test-only.key \
  --confirm-dedicated-test-key
```

可选 `--stream-request-file` 必须是包含 `"stream": true` 的 JSON 对象；启用后会分别验证两个版本
`/v1/responses` 的状态、SSE Content-Type 和初始事件类型序列。该命令只允许专用 Test 请求，不得把
真实在线请求复制到两个版本。完整阶段、门禁和剩余能力见
[Go v2 全量迁移与验证矩阵](go-v2-migration.md)。

如果当前 Test 用户路由账号不能完成 Responses，可使用
`scripts/migration-data-plane-route-compare.py`。它仅从两个隔离 Portal 的共同 selectable 账号中选择
候选，通过 `/usage/me/group` 同时切换两份副本，执行 Models、非流式 Responses 和 SSE 对比后在
`finally` 中恢复原路由及密码状态；报告只保存摘要和单向摘要值。管理 Key 与 Test Key 必须按顺序从
stdin 提供，禁止在参数、日志或报告中出现。

同一命令还提供 secret-free 状态摘要。它以 sqlx query-only 连接读取两个隔离副本，报告 schema、表行数
和组合 SHA-256，不输出 Key、内部 Key、秘密密文、OAuth 或 Session 值；snapshot 比较忽略随机
Generation/时间，只比较记录语义：

```bash
go run ./cmd/migration-compare state \
  --v1-root /path/to/isolated-v1-copy \
  --v2-root /path/to/isolated-v2-copy \
  --confirm-isolated-state-copies
```

## 修改原则

- 保持 `/v1` 数据面不依赖 Admin 和 Web。
- 修改 Gateway 或 Edge 发布行为时，先更新 ADR 或新增 ADR。
- SQLite 是控制面事实来源，新增文件投影必须可从数据库重新生成。
- 不在源码、测试夹具或文档中写入真实域名、邮箱、Key、Webhook 或私网地址。
- 为业务分支和安全不变量增加测试。

## 问题记录

- [2026-08-19 Web 分视图加载状态回归](problem-records/2026-08-19-web-view-state-regressions.md)：记录管理中心按需加载后出现的跨页面状态依赖、修复边界和回归矩阵。

## 提交和 Pull Request

提交信息建议遵循：

```text
feat(scope): description
fix(scope): description
docs(scope): description
chore(scope): description
```

Pull Request 应说明问题、最小修改范围、验证结果和部署影响。详细要求见仓库根目录的 `CONTRIBUTING.md`。
