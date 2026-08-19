# 架构说明

## 设计目标

系统需要同时满足三个相互独立的目标：

1. API 数据面不能依赖管理后台的可用性。
2. Gateway 更新不能中断已经建立的长时间流式请求。
3. 账号、Key、额度和路由配置必须有唯一、可事务化的事实来源。

因此，本项目采用单机 Compose 和控制面/数据面分离，而不是把所有功能合并为一个容器。

## 运行拓扑

```text
                              public request
                                     │
                                     ▼
                            ┌─────────────────┐
                            │  Stable Edge    │
                            │ host port owner │
                            └───────┬─────────┘
                       UI / Admin   │   API
                    ┌───────────────┴───────────────┐
                    ▼                               ▼
              ┌───────────┐             ┌────────────────────┐
              │    Web    │             │ Gateway blue/green │
              └─────┬─────┘             └──────────┬─────────┘
                    ▼                              │ internal key
              ┌───────────┐                        ▼
              │   Admin   │──────────────▶ CPA account containers
              └─────┬─────┘                        │
                    │                               ▼
         ┌──────────┴──────────┐              upstream APIs
         ▼                     ▼
 control-plane.sqlite3   usage.sqlite3
 OAuth / secrets / rendered runtime files
```

## 组件职责

| 组件 | 职责 | 不负责 |
| --- | --- | --- |
| Edge | 稳定占用宿主机端口，把 UI 和 API 分流到对应组件 | 用户鉴权、额度、配置写入 |
| Web | 提供 Portal、使用中心静态资源，并代理受限的管理路径 | API Key 路由 |
| Gateway | 外部 Key 鉴权、额度检查、账号路由和请求准入 | 控制面配置修改 |
| Admin | 管理 API、配置渲染、账号容器编排和运维命令 | 承载 `/v1` 数据流量 |
| Usage collector | 采集 CPA 用量事件并更新额度快照 | 低频控制面配置 |
| CPA containers | 每个业务账号独立运行 CLIProxyAPI | 外部用户 Key 管理 |

Gateway 的蓝绿切换流程和排空不变量记录在 [ADR 0001](adr/0001-stable-edge-blue-green-gateway.md)。

## 数据边界

```text
control-plane.sqlite3 ──▶ 配置、账号、路由、Key、团队、秘密密文
usage.sqlite3         ──▶ 高频用量事件、额度策略、调整账本
control-plane.key     ──▶ 控制面秘密的唯一加密主密钥
auth/                 ──▶ 上游 OAuth 文件
configs/              ──▶ 渲染给 CLIProxyAPI 的运行文件
state/gateway/        ──▶ 渲染给 Gateway 的只读快照
compose.accounts.yml  ──▶ 动态账号服务定义
```

SQLite 是人工配置的事实来源；`.env`、Compose overlay 和 JSON 快照只是运行投影，不能与数据库并行手改。

## 部署边界

当前运行时依赖本机 Docker API 动态创建和更新 CPA 账号容器，因此单机 Compose 是与现有产品能力匹配的部署边界。迁移 Kubernetes 不能自动获得高可用，反而需要同时重构 SQLite、OAuth 文件、动态工作负载和滚动发布协议。

生产机只应保存 Compose 描述、版本信息和持久化目录。应用源码属于镜像内容。
