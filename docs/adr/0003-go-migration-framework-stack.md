# ADR 0003: Go 全量迁移的框架栈与兼容边界

- Status: Accepted
- Date: 2026-08-20

## Context

项目将逐步把 Gateway、Edge、Admin、后台任务、CLI 和 Web 前端迁移到 Go/React，同时必须让
现有 API Key、Codex 流式请求、配额语义、运行数据和蓝绿切换协议保持兼容。迁移的主要目标
是缩小自研基础设施范围、提高迭代速度和可维护性，而不是用另一套自研 HTTP/配置/日志框架
重写现有系统。

数据面是最高风险边界。当前生产 Gateway 仍由 `gateway/nginx.conf`、
`gateway/request_gate.lua` 和 `gateway/gateway_state.lua` 提供；Go v2 已接入独立 Test Compose 与候选
镜像发布，但尚未接入 Production Compose、活动 Edge 或生产端口。任何迁移阶段都不得把同一个真实
用户请求同时发送给两个上游。

## Decision

采用框架驱动的模块化单体，而不是在迁移期拆成一组彼此远程调用的微服务。业务能力按
`internal/<domain>` 隔离，Gin Admin/Gateway、Cobra Worker 和 React 页面只做薄入口；同一个领域的
定时任务、Admin API 和测试复用同一业务包。后台任务可以独立构建和切换所有权，但不通过内部 HTTP
互相调用。这样可以直接利用成熟框架快速开发，同时避免为了“服务化”新增网络故障点和分布式事务。

新增功能按完整垂直切片交付：领域逻辑与单元测试 → Cobra/健康检查 → Gin 细粒度 API → React
路由懒加载页面 → 跨版本契约验证。只有通用机制交给框架；API Key 路由、事务、原子快照、幂等和
回滚仍由可审查的业务代码实现。

采用以下成熟组件作为全量迁移的统一技术栈：

| 领域 | 选型 | 使用边界 |
| --- | --- | --- |
| HTTP 路由与中间件 | Gin | Gateway、Edge、Admin 和 Web 的 Go HTTP 入口；显式关闭默认可信代理和可能泄漏请求头的默认访问日志 |
| 流式代理 | `net/http/httputil.ReverseProxy` | Codex/SSE/分块响应、请求取消和 Hop-by-Hop Header 处理；Gin 只负责路由和安全中间件 |
| CLI | Cobra | 统一 Gateway 与后续控制面命令、帮助和退出码 |
| 配置 | Viper | 配置文件、`CLIPROXY_GATEWAY_*` 环境变量和 Cobra Flags；运行态 SQLite 仍是业务配置事实来源 |
| 运维日志 | Zap | 结构化服务日志；API Key、内部 Key、请求正文不进入日志 |
| 文件变化 | fsnotify + 定时兜底 | 快照本地变化即时加载；500ms 定时加载维持鉴权新鲜度，并覆盖 NFS/SMB/FUSE 等通知缺失场景 |
| 原子文件发布 | renameio v2 | 鉴权与额度快照使用同目录临时文件、fsync 和原子 rename，不自研不完整的临时文件协议 |
| Admin 数据访问 | `database/sql` + sqlx，显式迁移 | 后续迁移现有 SQLite 查询和事务；不使用 GORM AutoMigrate 修改生产数据库 |
| SQLite 版本迁移 | Goose（嵌入式 SQL migration） | 新增 schema 版本时生成可审查、可回滚的 SQL；现有 v10 接管阶段先做精确校验，不自动创建或迁移生产库 |
| Admin 会话 | SCS v2 | 服务端短时会话、到期清理、Token 轮换和可撤销登出；管理密钥不进入 Cookie 或浏览器存储 |
| 后台调度 | robfig/cron | 后续迁移采集、额度快照、通知和日志维护任务 |
| 有界异步任务 | Pond v2 | Admin 容器启停/重启使用单 Worker、有界非阻塞队列、panic recovery 和 Context 取消；任务目录与领域结果仍由项目维护 |
| RESP Usage Queue | Redigo | 复用成熟 RESP 编解码和 Context I/O，只发上游已承诺的 `AUTH`、`LPOP`；不发送 `HELLO`/`CLIENT` |
| 外部 HTTP 调用 | Resty v2 | 统一 OAuth 额度、企业微信和版本元数据请求的超时、响应上限、脱敏和可观测性；非幂等写请求不自动重试 |
| 容器控制 | Moby API/Client 官方 Go SDK | 只按当前 Compose Project 精确标签列举并操作容器；不执行 shell 或任意 Compose 命令 |
| API 契约生成 | OpenAPI 3 + oapi-codegen | Go API 完成旧版契约夹具对齐后生成服务端接口和 TypeScript 类型，减少手写 DTO 漂移；不以生成器改变既有响应语义 |
| Web 前端 | React + TypeScript + Vite + React Router + TanStack Query | 替换原生 JS 页面；按页面/组件请求细粒度 API，并通过精确失效刷新更新必要数据 |
| 管理端组件库 | Ant Design | 复用表格、Modal、状态反馈、布局和无障碍交互；项目 CSS 只维护品牌和页面级布局，不再自研基础控件 |
| 表单与校验 | React Hook Form + Zod | 复用表单状态、类型推导和前端输入校验；服务端继续执行独立校验 |
| 前端测试 | Vitest + Testing Library | 验证 API 请求边界、登录密钥不持久化，以及 loading/empty/error/mutation 状态 |

框架只承担通用机制。以下兼容规则继续由项目代码和跨语言契约测试显式维护：

- 公共 API 路径白名单、API Key SHA-256 查找和外部 Key 到内部 Key 的替换。
- 鉴权快照超过 5 秒 fail-closed，额度采集/快照异常 fail-open。
- 兼容的 `401`、`429`、`503` JSON 与 `Retry-After` 行为。
- 公网 `8317` 与内部探针 `8319` 隔离，日志保持五列 TSV 契约。
- 流式响应不设置全局 `WriteTimeout`，客户端取消必须传递到上游。
- Stable Edge 与 Gateway 蓝绿预检、切换、排空、回滚协议保持不变。

控制面切换采用跨语言 SQLite Lease，而不是双写或数据库主从。`runtime-writer` 是版本级共享所有权，
Admin、Collector、Quota、Failover、Notifications 和 Log Maintenance 使用独立排他 Scope。Lease 的
随机 Token 与递增 Generation 同时匹配才允许续约/释放。Go 使用 sqlx 实现协议；过渡期 Python v1
使用 `scripts/ownership_lease.py` 保持完全相同的 JSON 字段和文件锁/SQLite 事务边界。Go 可变更入口通过
Cobra/Viper 参数声明 Owner 和 TTL，并在取得两层 Lease 前只允许 `OpenExisting` 执行 Lease 读写，不得
创建目录、数据库、主密钥或迁移 schema。Python 在 Lease 丢失时直接退出进程，Go 取消 fence context
并关闭监听器。接管已有 Lease 必须精确匹配前任 Owner 与 Generation；Go Store 还会在每个控制面写
事务内原子核对 runtime/worker 的 Token、Generation 和有效期，消除心跳发现丢失前的陈旧写窗口。
Gateway/Edge 不依赖 Writer Lease，控制面移交不进入 API Key/Codex 请求路径。

当前已经落地的开发入口包括 `cmd/gateway/main.go`、`cmd/edge/main.go`、`cmd/admin/main.go`、
`internal/gateway/`、`internal/edge/`、`internal/controlplane/`、`internal/admin/`、`internal/usage/`、`internal/failover/` 和
`internal/logmaintenance/`、`cmd/log-maintenance/`、`internal/quota/`、`cmd/quota/`、`cmd/failover/`、
`internal/notifications/`、`cmd/notifications/`、`internal/accountlifecycle/` 和 `frontend/`。Go Admin 已实现服务端会话、有界目录总览、实时通用设置、团队目录、分页用户目录、用户创建/Key 轮换/停用/删除/密码重置、公共站点配置与 Logo、受鉴权 Native 账号目录、细粒度账号目录、账号生命周期和全账号
负载均衡入口，并通过一个 sqlx 只读连接池读取现有 schema v10 用量库；账号/用户模型明细采用独立
API，不进入目录响应。用户生命周期由 Gin 只接收细粒度命令，sqlx 事务负责期望状态更新，renameio 鉴权快照发布后必须等待 Gateway 激活；创建、轮换、停用或删除失败时使用有界无取消上下文恢复控制面并重新发布旧快照。创建与删除还把 schema v10 使用中心凭据作为补偿步骤，历史用量不删除。用户周额度通过独立的 `GET|PUT|DELETE /admin/api/users/quota` 按单个用户读取和修改既有 schema v10 策略，沿用 Collector 的自然周、加权 Token、Bonus 与使用量重置聚合语义，不把额度塞回分页目录。总览使用一个控制面 SQLite 只读事务聚合计数，不读取或返回身份、Key、Docker、OAuth、Gateway 或用量事件；通用设置通过白名单和按键更新只维护品牌、登录域名与客户端导出字段，不覆盖通知或其他配置。React 已实现登录、总览、通用设置、用户与团队管理、一次性凭据展示、Ant Design 账号页以及按需打开、10 秒实时
刷新的用量抽屉。账号生命周期用 sqlx 期望状态事务、SQLite secret-free Journal、renameio 文件投影、
Moby 容器探针和 Gateway 激活确认组成可恢复流程；更新/停用/OAuth 清理/删除先迁移路由到已启用且额度可用的备用
账号，并等待全部 Gateway 的目标账号 in-flight 归零后才重建、删除或在清理 OAuth 后重启旧容器。排空或任一 Gateway 探针失败会
恢复原路由和快照；进程中断后的删除恢复也不得绕过这一排空边界。账号停用后不把路由恢复到停用账号，
其他更新在新容器探针通过后恢复到新账号。Journal 不保存 API Key、OAuth 或代理明文，自定义代理密文
无法恢复时 fail-closed。Portal 与 Native 也已使用 React Router、TanStack Query 和 Ant Design，覆盖品牌配置失败、账号加载/空目录、401、请求失败与重试状态。Native API 永不返回独立端口或账号邮箱，只有回环 Host 才构造回环管理 URL，浏览器再次校验 HTTP、回环地址和精确路径后才渲染可点击卡片。Go Collector 已开始使用 Redigo 实现严格 RESP2 `AUTH`/`LPOP` 队列适配，并用独立
sqlx 单写连接实现现有 schema v10 的身份哈希、团队快照、事件去重、冻结权重和周用量物化；写入器只
接管既有 v10 数据库，不负责创建或迁移。Go Log Maintenance 使用 robfig/cron 的 `@every` 调度、
`SkipIfStillRunning` 和 panic recovery，文件层使用 renameio 创建同目录原子备份，再对原日志
copy-truncate；它拒绝日志路径或备份中的符号链接/非普通文件，并把兼容状态写入
`runtime_state(log_maintenance)`。健康检查通过 query-only sqlx 连接读取既有控制数据库，不会初始化
目录、数据库、迁移或主密钥。Go Quota Worker 使用 Resty v2 并行读取官方账号额度，复用控制数据库的
inherit/custom/direct 代理策略，严格筛选无符号链接的 Codex OAuth 文件；请求不重试、不跟随重定向、
限制响应体大小，Token、官方 account ID 和含凭据代理地址不写入状态或错误。标准化结果单独保存到
`runtime_state(official_quota)`。Go Failover Controller 通过 TCP 探针和该额度快照构建 fail-closed
账号状态，只接受 `off/active`，并以期望路由事务、原子鉴权快照、Gateway 激活等待和失败回滚执行整批
迁移；它写入 Python v1 兼容的 `runtime_state(account_failover)`，迁移后立即刷新近 1 小时活跃用户数，
审计不含用户身份。Go Notification Worker 使用 Resty、robfig/cron 和 Go tzdata，兼容
`runtime_state(notification)` 的定时报表、额度预警/耗尽/恢复/刷新、时区槽位去重、失败重试和 4096
字节 Markdown v2 上限；Gin Admin 提供独立的通知设置、加密 Webhook、清除和手动发送 API，React
通知页仅在打开期间每 10 秒刷新这一份细粒度状态。控制面通过按键设置更新和顶层运行状态 Patch
事务，避免 Admin 与 Worker 用整份 JSON 覆盖彼此的无关字段。Go Quota、Failover 和 Notifications
必须作为同一套隔离版本测试，不能与 Python Worker 共享真实
OAuth 或迁移所有权。Go Stable Edge 使用 Gin 与 `ReverseProxy` 保持现有 Web/API 路由拆分、100 MiB
请求上限、公网内部探针隔离、流式刷新和客户端取消传播；它严格复用现有
`active-gateway.conf` 蓝绿指令，通过 fsnotify 与 500ms 轮询加载原子替换，运行期文件损坏时保留最后
一个有效槽位。Edge 不读取 API Key、鉴权/额度快照或内部凭据。`v2/Dockerfile`、
`docker-compose.v2-test.yml` 和发布器提供固定基础镜像摘要、隔离数据面以及
`v2-control/v2-web/v2-gateway/v2-edge` 候选镜像；活动部署器仍只应用 v1 四组件，现网继续由原
OpenResty/Python/原生 JS 组件提供服务。

Go v2 Web 使用独立 `cmd/web` 与 `internal/web` Gin 进程替换候选镜像中的 Nginx 运行时。它只从
显式 React Portal/Admin/Usage 根目录读取普通非符号链接文件，为 React 路由提供受限的 `index.html` 回退，
并使用标准 `ReverseProxy` 代理白名单 Admin 与自服务 API。公开自服务代理会移除 Authorization 和管理
密钥，公开 CSS 也走无凭据代理，管理 API 保留既有自动化 Header；只有 Vite 构建指纹资产使用 immutable
缓存，稳定 Logo 和 HTML 使用 `no-cache`；v2 Web 仍不进入 `/v1` 数据面。现网 v1 Web 在正式切换前继续
保留 Nginx，不能因候选镜像完成而提前替换。

Go Admin 的运行维护切片使用模块化 Moby API/Client SDK 连接本地 Unix Socket，并用 Compose Project
与 Service 标签精确解析目标。账号 CPA、Web、Management、Usage Collector 和 Log Maintenance 可以
进入 Pond 单 Worker 有界队列执行启动、停止或重启；Edge、Gateway 和 Admin 只能读取最近 200 行日志，
不能从 Admin 运维 API 停止。日志先多读取固定脱敏前视窗口，完成 Bearer、API Key 和 OAuth 字段脱敏后
再截断到 2 MiB，避免凭据恰好跨截断边界时泄漏。React 运行维护页只在打开时请求服务和任务，日志抽屉
打开后才请求对应目标并定时刷新。任务状态是进程内运维状态，不作为耐久业务事实；Admin 重启后以
Docker 服务目录为准。

Go Store 的精确代际写栅栏同时保护控制面事务和控制面之外的副作用：Collector/Admin 的
`usage.sqlite3` 写入、auth/quota/heartbeat 原子快照、failover 审计、copy-truncate 日志轮转以及
Resty Webhook 发送。栅栏验证事务会在外部回调前释放 SQLite 单连接，但持续持有跨进程文件锁，既
允许 Webhook 在发送前读取加密配置，又阻止所有权在副作用中途转移。Gateway 激活探测和通知状态
Patch 不嵌套在该锁内。WeCom 无幂等键，因此“远端成功后、本地状态落库前崩溃”仍是明确记录的
至少一次投递边界。
控制面和 schema-v10 用量库的 sqlx 读连接与写连接分离：只读事务继续使用普通 WAL 快照，不申请写锁；所有写事务使用
modernc SQLite 的 `_txlock=immediate` 连接，使 busy handler 在建立 Lease 读取快照前等待，避免
deferred transaction 先读后升级时立即返回 `SQLITE_BUSY`。跨进程文件锁仍只覆盖写事务和受 fencing
保护的外部副作用。

## Consequences

- 路由、配置、CLI、日志和文件监听不再重复建设，自研代码集中在 CPA 业务契约与安全规则。
- Gin 不负责代理实现，避免把长连接、Flush、取消和 Header 语义隐藏在通用 Handler 中。
- Go Edge 为每个蓝绿公网/内部 Origin 保留独立的 `ReverseProxy`，每个新请求只读取一次原子槽位；
  切槽后已有长连接继续绑定旧代理并由 Gateway in-flight 排空，新请求进入新槽位。无效或符号链接的
  选择文件不会覆盖最后有效槽位。
- Viper 不取代控制平面 SQLite；环境变量和文件仅承载进程启动参数。
- Admin 浏览器会话由 SCS 服务端存储，登出会撤销旧 Token；管理密钥只出现在登录请求中。
- React 页面可以按需加载细粒度 API，但“去缓存”不等于“数据库无需并发控制”；写事务、
  快照发布和重复刷新仍需由存储层保证一致性与合并请求。
- Go v2 的账号活跃数和账号/用户用量明细复用同一个 `usage.sqlite3` 只读连接池，每次明细查询使用
  SQLite 只读事务获得一致快照，不获取控制面写锁，也不创建或迁移数据库；React 只在抽屉打开时请求，
  关闭后停止轮询并释放 Query 缓存。路由变更仍使用跨进程锁、
  SQL 事务、原子快照和 Gateway 激活确认，不能以“实时刷新”为理由去掉一致性保护。
- Go Collector 对 `usage.sqlite3` 使用一个写连接串行事务，SQLite WAL 继续允许 Admin 的只读快照并发；
  这消除了进程内全局数据库锁，但不会取消 SQL 事务、唯一约束或跨进程运行操作锁。
- 状态差异使用 `cmd/migration-compare state` 生成 secret-free checkpoint；它对耐久表和运行态表分别
  计算组合摘要，snapshot 比较忽略随机 Generation/时间但保留记录语义。Lease、heartbeat 与 session
  漂移单独报告，不能掩盖账号、Key、路由、历史用量或额度策略漂移。
- Go 用户生命周期、使用中心 Key 轮换/路由切换和 Admin 全账号均衡共享一个进程级操作锁，串行执行完整的“SQLite 事务 → 使用中心凭据 → 鉴权快照激活/补偿”过程；
  跨版本测试仍必须遵守单一控制面写入所有权，不能让 Python 与 Go 同时创建、轮换、停用或删除用户。
  React/TanStack Query 的一次性 Key 和初始密码 Mutation 使用 `gcTime=0`，不进入 URL 或浏览器持久化存储，
  用户关闭展示框后从组件状态清除。
- Go 账号生命周期与用户/Key/路由命令共享同一 Admin 操作锁，并额外持久化单个 secret-free Journal。
  这不会替代 SQLite 事务或 Writer Lease；它只用于进程中断后恢复文件、容器、路由和快照。账号容器
  变更必须经过 Gateway in-flight 排空，所有 Gateway 探针均为 fail-closed，确保已受理的 Codex SSE
  请求不会被账号重建或删除截断。
- 用户额度 Modal 只在打开时请求该用户的自然周额度，关闭后以 `gcTime=0` 清除 Query；策略写入与
  用户/Key/路由命令共享 Admin 操作锁，但只修改 `user_quota_policies`。历史用量、Bonus 和使用量重置
  记录不删除，Gateway 的额度变化由唯一 Collector 的下一份额度快照生效，因此接口不会错误宣称已
  即时激活。
- RESP 客户端的框架选型受上游实际协议约束。通用 Redis 客户端若在认证前自动发送 `HELLO` 会扩大
  未承诺的协议面，因此 Collector 使用只实现连接与 RESP 编解码的 Redigo，并用线路级测试锁定首条
  命令为 `AUTH`、后续仅为带批量数的 `LPOP`。
- Go 后台任务统一由 robfig/cron 负责调度、重入跳过和 panic recovery；业务任务仍必须自己定义幂等、
  状态心跳和错误语义。日志轮转在新备份完成并 fsync 后才移动旧备份和截断原文件，保留运行进程
  已打开的日志 inode；copy-truncate 固有的极短并发写窗口由跨版本压力测试单独验收。
- Admin 即时运维任务由 Pond v2 限制并发和队列长度；项目代码只维护任务 ID、状态、去重/冲突、取消与
  最近 60 条内存目录。队列满时 API 返回 `429`，同目标冲突返回 `409`，不能退化为无限 goroutine。
- Docker SDK 不扩大操作权限：远程/TCP Docker Host 被拒绝，重复 Compose Service 标签 fail-closed，
  Edge/Gateway/Admin 的停止与重启在后端目标白名单被拒绝，不能仅依赖 React 隐藏按钮。
- Go 官方额度客户端固定生产目标为现有官方 HTTPS 接口；只允许测试注入回环 HTTP 地址，拒绝重定向，
  并显式关闭自动重试，避免 Bearer Token 跨域传播或把一次额度读取放大成重复请求。额度抓取按账号
  归一化 `auth_missing`、`auth_expired`、`unavailable` 和周窗口，单账号故障不会把未知容量标成可用。
- Go v2 必须先在备用端口使用专用测试 Key 做契约、流式、取消和差异验证，再进入现有
  蓝绿 Gateway 发布流程。真实生产请求只由当前活动版本处理。
- `cmd/migration-compare` 使用 Cobra/Viper/Resty 对两个显式 Test Origin 顺序发送专用测试请求；默认
  只允许 loopback，Key 必须从 `0600` 非符号链接文件读取，结果不含 Key 或请求正文。它不是在线流量
  镜像器。
- 迁移完成后可以删除对应 Python/Lua/原生 JS 组件，但只能在新版本通过数据保护、回滚和
  公网真实路由验收后进行，不能按代码完成度提前删除。

## Alternatives considered

- 继续自研 `net/http` Router、Flags、配置合并和日志组件：代码量更少但会把大量通用机制的
  测试和维护责任留在项目内，不符合快速迁移目标。
- Fiber/fasthttp：性能上限较高，但与标准 `net/http`、`ReverseProxy` 和现有流式取消语义
  的适配成本更高。
- GORM AutoMigrate：CRUD 开发快，但自动结构变更不适合直接接管已有生产 SQLite 和回滚
  约束。
- go-redis：连接池和 Redis Cluster 能力完善，但当前版本在连接初始化时先发送 `HELLO`；CPA Usage
  Queue 只承诺 `AUTH`/`LPOP`，因此该额外握手被线路级兼容测试拒绝。
- 一次性替换所有运行容器：无法在切流前证明 API Key、Codex 长连接、运行数据和回滚链路，
  不满足连续性门禁。

## Maintenance notes

- 新增 Go HTTP 服务时统一复用 Gin 安全初始化：`SetTrustedProxies(nil)`、无默认请求日志、
  不记录 Authorization/正文、显式恢复中间件。
- 修改 Gateway 行为时同步维护 `testdata/gateway/contracts.json`、Go 测试、Lua 测试和
  `tests/test_gateway_contracts.py`。
- Go v2 接入 Production Compose、目标机应用或活动端口前，更新 ADR 0001、Gateway 域文档、契约
  索引和发布验证；候选镜像可发布不等于已上线组件。
- 数据访问和 React 阶段只在对应代码开始迁移时引入依赖，避免提前扩大供应链与发布体积。

## Evidence

`go.mod`、`cmd/gateway/main.go`、`internal/gateway/`、`testdata/gateway/contracts.json`、
`tests/test_gateway_contracts.py`、`internal/edge/`、`cmd/edge/main.go`、`internal/migrationcheck/`、
`cmd/migration-compare/`、`v2/Dockerfile`、`docker-compose.v2-test.yml`、
`scripts/v2-test-smoke.sh`、`scripts/v2-test-faults.sh`、`scripts/v2-worker-process-rehearsal.sh`、`scripts/release_manifest.py`、`scripts/release-images.sh`、
`docs/go-v2-migration.md`、`scripts/verify.sh`、`.github/workflows/ci.yml`、
`gateway/nginx.conf`、`gateway/request_gate.lua`、`gateway/gateway_state.lua` 和
[ADR 0001](0001-stable-edge-blue-green-gateway.md)。
