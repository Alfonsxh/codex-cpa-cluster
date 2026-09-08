# 故障排查

先确认实际部署目录和私有 `target.env`。在匹配 Release 的目录执行：

```sh
make -f scripts/build.mk target-ps TARGET_ENV=/absolute/path/to/target.env
make -f scripts/build.mk target-ownership-status TARGET_ENV=/absolute/path/to/target.env
make -f scripts/build.mk target-smoke TARGET_ENV=/absolute/path/to/target.env
```

日志使用同一目标的 Compose 项目与配置；分享前删除密钥、OAuth、Webhook、邮箱和私有地址。

## 通知没有发送

企业微信先看配置中心的“后台调度”和“最近心跳”：

- 无心跳或超过 3 分钟未更新：检查 `notifications` 容器、所有权和数据库错误。
- 心跳正常：检查开关、Webhook、发送错误、计划时间和系统时区。
- 手动发送成功仅证明通道可用；超过补发窗口的漏发不会自动补发。

Telegram 正式版公告由发布工作站发送，按[通知文档](telegram-release.md#预览与回执)检查回执。

## Gateway 请求失败

| 状态 | 检查 |
| --- | --- |
| `401` | Key 是否有效、路由是否可用、鉴权快照是否刷新 |
| `503 authentication_snapshot_unavailable` | `state/gateway/auth-snapshot.json` 的完整性、新鲜度与权限 |
| `502 upstream_unavailable` | 账号容器、内部网络、内部 Key 与上游错误 |

账号管理的“模型通信测试”可直接验证指定账号生成，最长等待 25 秒，输出上限 64 Token。它产生少量用量，需要可用内部凭据，但无需将用户绑定到该账号。

模型列表或账号测试成功不能代替外部 Gateway 的实际 Responses/SSE 验证。

## Edge 健康但访问 502

核对 Edge 的 Control/Ingress 网络、Gateway 与 Admin 的上游网络，以及 Web 到 Admin 的连接。`state/edge/active-gateway.conf` 只能选择 `blue` 或 `green`。

用匹配目标的 `target-up-core` 恢复声明拓扑，不将手工连接未知容器作为长期修复。

## 页面白屏或资源 404

检查 `web` 镜像与 Release 描述是否一致。下例端口须替换为实际 `CPA_PUBLIC_PORT`：

```sh
curl --noproxy '*' -I http://127.0.0.1:18317/admin/
curl --noproxy '*' -I http://127.0.0.1:18317/portal/assets/codex-cpa-pool-logo.svg
```

HTML 与稳定品牌资源使用 `no-cache`，内容指纹 JS/CSS 使用长期缓存。

## 用量不更新

检查 Collector 健康、Writer Lease、用量库权限与磁盘空间，以及 Gateway 日志是否产生新事件。旧 Generation 不能继续写入；不要直接修改 SQLite 绕过所有权。

## 账号代理投影损坏

普通修复先迁移路由并等待请求排空。仅当源账号已不可用、仍有路由且全池没有安全迁移目标时，才能使用受限接口：

```http
POST /admin/api/accounts/repair-proxy
X-Management-Key: <管理密钥>
Content-Type: application/json

{"id":"<account-id>","proxy_url":"http://<existing-proxy>:<port>","confirm":"repair-proxy:<account-id>"}
```

操作前核对既有代理网络并保留可恢复备份。该接口只修改独立代理并重建同一账号，不修改外部代理服务、账号标识或路由。首个账号恢复后，其余账号回到普通迁移流程。

## 部署检查失败

| 问题 | 处理 |
| --- | --- |
| 镜像不匹配 | 使用同一 Release 的四组件摘要引用，核对组件名与两个摘要标签 |
| 所有权冲突 | 先查明并停止旧 Writer，再进行受控交接，不删除所有权记录 |
| Gateway 排空超时 | 旧槽与 SSE 保持运行，处理长请求后重试 |
| Edge 维护未确认 | 明确维护窗口后填写两个 Edge 确认字段，见[部署](deployment.md#底层应用与验收) |
| 配置来源冲突 | 正式控制面使用 `target.env`，账号投影 `state/compose.env` 不可替代它 |
