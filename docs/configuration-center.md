# 配置中心

## CPA 出口代理

控制面不转发业务流量，只解析最终出口并把 CLIProxyAPI 原生 `proxy-url` 写入每个 CPA
配置。控制面默认代理支持 HTTP、HTTPS、SOCKS5，并使用控制面主密钥加密保存。

账号出口有三个模式，优先级固定为：账号自定义代理、账号强制直连、控制面默认代理。
“继承默认”仅在默认代理开关开启时使用控制面地址，否则直连。修改单个账号的出口只
重建该 CPA；修改控制面默认代理不会改变 Gateway、Edge 或 API Key。

配置中心位于管理后台“系统设置”。它是组织级参数的唯一人工维护入口；账号邮箱、OAuth、用户 Key、用户当前路由和秘密凭据仍由各自的专用入口维护。

## 为什么使用 SQLite，而不是长期维护多个 JSON

`state/control-plane.sqlite3` 是低频控制面数据的唯一事实来源。配置、账号、路由、Key 记录、用户团队与标签、后台运行状态、Logo 和加密后的运维秘密都在同一个数据库中事务化更新，并统一执行权限、备份和迁移。

以下旧控制文件已经废弃，系统不会再读取或生成：

- `state/accounts.json`
- `state/keys.json`
- `state/user-routes.json`
- `state/configuration.json`
- `state/account-failover.json`
- `state/notification-state.json`
- `state/log-maintenance.json`
- `state/deployment.json`
- `secrets/user-internal-keys.json`

`store cleanup-projections` 要求数据库内已有旧 JSON 导入记录，且每个待删文件仍与最后一次数据库同步摘要一致；任何文件被旧版本或人工改写都会拒绝清理。随后还会验证 SQLite 完整性、Schema 版本、数据库与主密钥权限以及全部加密秘密可解密，再只删除上述固定白名单。`store verify` 也会把任一残留旧文件报告为失败。删除后不能回滚到依赖这些 JSON 的旧版本；只剩旧 JSON 的环境必须先使用过渡版本导入 SQLite。

仍保留的 JSON 是在线文件协议，而不是兼容控制存储：`state/gateway/*.json` 由 OpenResty 读取，`state/public/accounts.json` 只供网关内部聚合账号统计，`auth/<account>/*.json` 由上游 CLIProxyAPI 读取。公网不再直接提供账号 JSON；CPA 选择页改用管理员鉴权接口。配置档案则只是一次性入口。

高频 `usage_events` 不合并进控制面数据库，继续使用 `state/usage.sqlite3`，避免用量写入与管理配置争用同一事务。事件写入时仍保存当时的 `team_id` 与成员版本作为审计字段；团队报表则从控制面读取当前成员邮箱，并动态聚合所选时间范围内这些用户的全部事件。移动团队会立即改变团队报表归属，但不会改写历史事件。

监听地址、内部探针端口和会话有效期也由配置中心写入 SQLite；需要 Compose 插值的值会
同步为 `state/compose.env` 运行投影。数据库仍是人工配置事实来源，生成文件不是第二个
手工配置入口。
公网安全策略强制 Gateway、Management 和业务 CPA 使用回环监听，内部探针使用独立端口。

## `.env`、SQLite 与 Compose 投影

三者职责固定如下：

| 存储 | 负责 | 不负责 |
| --- | --- | --- |
| `.env` | 宿主机路径和 Compose 身份：`DEPLOY_ROOT`、`INSTANCE_NAME`、`COMPOSE_PROJECT_NAME`、`DOCKER_NETWORK_NAME` | 业务配置、端口、镜像版本、发布版本 |
| `state/control-plane.sqlite3` | 人工期望配置；CPA 更新通道；候选/已应用 CPA 版本和摘要；待发布/已验收应用版本与组件镜像 | OAuth 原文和 Compose 临时语法 |
| `state/compose.env` | 从 SQLite 原子生成的 Compose 插值，权限 `0600` | 人工编辑和长期事实存储 |

旧版混合 `.env` 第一次由新版控制面读取时，会先把已知配置导入 SQLite，把原文件备份到
`state/legacy.env`，再将 `.env` 收敛为四个启动项。无法识别的旧键只记录在迁移状态和
备份中，不会被静默当成新配置继续使用。

## 品牌与身份配置

| 参数 | 开源默认值 | 说明 |
| --- | --- | --- |
| `branding.product_name` | `Codex CPA Cluster` | 页面和通知的完整名称 |
| `branding.short_name` | `Codex CPA` | 紧凑导航和客户端导出名称 |
| `branding.environment_label` | `Self-hosted service` | 环境或访问范围说明，可留空 |
| `branding.public_base_url` | 空 | 通知和 Codex、Claude Code、CC Switch 导出的 HTTP(S) 根地址；协议按配置原样使用，留空时使用浏览器当前来源 |
| `identity.allowed_email_domains` | `[]` | 允许多个域名；创建用户前至少配置一个 |
| `identity.key_prefix` | `cpa_` | 必须以下划线结束，只影响新建或轮换的 Key |
| `portal.provider_name` | `Codex CPA` | Codex、Claude Code、CC Switch 等导出配置中的名称 |
| `portal.api_key_env` | `CPA_API_KEY` | 导出 Shell 配置使用的环境变量 |
| `portal.default_model` | `gpt-5.6-sol` | 导出客户端配置的默认模型 |

`branding.public_base_url` 是客户端访问协议的唯一配置入口。例如填写
`https://cpa.example.com` 会为所有客户端导出 HTTPS 地址，填写
`http://cpa.example.com:18317` 则导出 HTTP 地址；前端不会再为 CC Switch 隐式把 HTTPS
改成 HTTP。该参数只决定通知与导出内容，不会替服务器开启 TLS 或额外监听端口。若要让
HTTP 和 HTTPS 同时可访问，需要外部反向代理分别监听两个协议，并明确决定 HTTP 是直接
提供服务还是重定向到 HTTPS。

用户周额度与“今日”统计使用独立的 `user_quota.timezone`，开源默认值为 `UTC`；通知调度的 `notification.timezone` 也默认为 `UTC`。已有部署可在部署档案中显式保留原时区。修改用户额度时区会重新划分历史周聚合和调整记录，并重启采集器。

`user_quota.reset_personal_weekly_on_new_week` 默认开启。开启时，管理员为用户设置的“单独不限额”或“自定义额度”只持续到当前自然周结束，下一周开始后恢复继承 `user_quota.default_weekly_tokens`；本周追加额度和用量清零调整仍按原规则随周结束失效。关闭开关后，现有及后续个人额度策略会持续生效，直到管理员手动恢复默认。首次升级到包含该开关的版本时，已有个人策略会保留到当前周结束，不会在部署当日立即清除。

页面 Logo 通过“品牌与身份”中的上传控件管理。允许 PNG、JPEG、GIF、WebP 和 SVG，最大 2 MiB。SVG 会拒绝脚本、事件处理器、外部资源、`foreignObject`、实体和危险 URL；原始文件作为 BLOB 存入控制面数据库，通过 `/branding/logo` 输出。

## JSON 配置档案

配置档案用于一次性初始化，不是第二份持久配置。示例见 `config/profile.example.json`：

```json
{
  "version": 1,
  "values": {
    "branding.product_name": "Codex CPA Cluster",
    "identity.allowed_email_domains": ["example.com", "example.org"],
    "identity.key_prefix": "cpa_"
  }
}
```

应用和查看：

```bash
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml \
  exec -T admin codex-cpa profile import-once - < config/profile.example.json
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml exec -T admin codex-cpa profile show
```

档案的 `values` 只允许 `CONFIG_DEFINITIONS` 中已声明的键。可选的 `branding.logo` 对象接受 `filename`、`content_type` 和 `data_base64`，用于在受保护的部署档案中携带组织专用 Logo；所有内容先经过与后台上传相同的类型和安全校验，再写入控制面数据库。未知键或非法值会使导入失败。

```json
{
  "branding": {
    "logo": {
      "filename": "organization-logo.svg",
      "content_type": "image/svg+xml",
      "data_base64": "<base64-content>"
    }
  }
}
```

旧生产环境可把组织专有档案放在 `secrets/deployment-profile.json`。新版发布脚本只在首次迁移时导入并记录规范化内容指纹，验收后删除该文件；相同档案再次出现时不会覆盖数据库，不同档案会被拒绝，后续修改必须通过配置中心完成。

## 生效方式

| 标签 | 行为 |
| --- | --- |
| 立即生效 | 后端下一次读取时生效，并重新生成公开站点配置 |
| 重建业务 CPA | 保存、渲染、Compose 校验后逐个重建账号容器 |
| 重启采集器 | 保存后重启 `usage-collector` |
| 下次采集生效 | 下一轮生成用户额度快照 |
| 仅新账号 | 只改变之后自动分配的端口或参数 |
| 下次部署 | 重新生成 `state/compose.env`，不主动中断当前网关 |

带运行时操作的更新采用“写入 → 渲染 → 校验 → 应用”顺序；任一步失败时恢复原数据库值，重新渲染在线运行产物并应用原配置。管理 API 会记录操作审计，但不记录完整 Key 或秘密。

### CPA 请求稳定性

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `cpa.request_retry` | `2` | 单次上游请求失败后的重试次数 |
| `cpa.disable_image_generation` | `chat` | 普通响应不注入 hosted 图片工具，避免与 Codex `image_gen` 命名空间冲突；专用图片接口仍可用 |
| `cpa.max_retry_credentials` | `1` | 一次请求最多切换尝试的 OAuth 凭据数量 |
| `cpa.max_retry_interval` | `12` 秒 | 等待临时冷却凭据后再次重试的最长时间 |
| `cpa.transient_error_cooldown_seconds` | `10` 秒 | 上游 408/500/502/503/504 后当前凭据的冷却时间 |

生产账号通常只有一份 OAuth。较短的临时错误冷却可以限制单次上游 5xx 被放大成整分钟 `auth_unavailable` 的范围；不要设置为零，零值在 CLIProxyAPI 中代表旧版 60 秒默认值。图片工具策略默认使用 `chat`，对应 CLIProxyAPI 的 `disable-image-generation: "chat"`：普通 `/v1/responses` 请求不会叠加 hosted `image_generation`，但 `/v1/images/generations` 和 `/v1/images/edits` 保持可用。以上参数保存后重建业务 CPA。

## 仍然独立保存的内容

以下数据不属于通用配置，避免配置接口意外泄密或跨越部署安全边界：

- `encrypted_secrets`：控制面数据库中的 AES-256-GCM 加密表，保存管理密钥、用户初始密码、企业微信 Webhook 和默认/账号代理地址。
- `secrets/control-plane.key`：数据库之外唯一的 32 字节加密主密钥，权限必须为 `0600`；数据库备份必须与对应主密钥成对恢复。
- `auth/<account>/`：OAuth 文件。
- `.env`：只保存宿主机路径和 Compose 身份，不保存版本、端口或业务配置。
- `state/compose.env`：SQLite 生成的私有 Compose 投影。CPA 镜像更新全部验证成功后才更新；应用发布只暂存组件镜像，最终验收后才把版本标记为已应用。
- `state/usage.sqlite3`：高频用量事件（含审计用的事件时团队归属）、周额度策略和调整账本；团队报表按控制面当前成员动态聚合。

`configs/`、`compose.accounts.yml`、`state/gateway/` 和 `state/public/` 是从数据库渲染的运行产物。CLIProxyAPI 与 OpenResty 仍需要文件接口，因此这些文件会保留，但不能手工维护。`secrets/issued-keys.tsv` 不再生成；完整 Key 只在创建/轮换响应中展示，并由控制面数据库保存。

CPA 的 `runtime.cliproxy_image` 是拉取通道，可以长期保持 `:latest`。拉取后控制面通过受限、
无网络的临时容器读取 CLIProxyAPI 版本横幅，把语义版本、镜像 ID 和仓库摘要写入 SQLite；
界面同时展示语义版本和短 SHA。只有所选运行中 CPA 全部通过模型探测，候选镜像才成为
已应用镜像，`state/compose.env` 随后固定到带 digest 的引用或本机不可变 image ID。

## API 与运维命令

| 表面 | 说明 |
| --- | --- |
| `GET /site-config.json` | 无敏感信息的公开品牌和客户端配置 |
| `GET /branding/logo` | 当前 Logo；未上传时返回 404 |
| `GET /admin/api/settings` | 管理员读取全部配置定义、当前值和存储状态 |
| `POST /admin/api/settings/configuration` | 校验并保存通用配置 |
| `POST /admin/api/settings/initial-password` | 加密保存用户初始密码，只返回是否已配置 |
| `POST /admin/api/settings/logo` | 上传 Base64 编码 Logo |
| `DELETE /admin/api/settings/logo` | 恢复内置中性 Logo |

数据库检查与在线备份：

```bash
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml exec -T admin codex-cpa store verify
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml \
  exec -T admin codex-cpa store backup backups/control-plane.sqlite3
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml \
  exec -T admin codex-cpa store cleanup-projections
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml \
  exec -T admin codex-cpa store migrate-secrets --cleanup
```

SQLite 备份使用在线 Backup API，而不是在 WAL 模式下直接复制主文件。
