# 故障排查

所有命令都必须使用当前操作者提供的 Test/Production 私有环境文件。先确认目标，再执行只读诊断；不要从旧归档或 Git 历史推断主机和目录。

```sh
make target-config TARGET_ENV=/absolute/path/to/target.env
make target-ownership-status TARGET_ENV=/absolute/path/to/target.env
make target-ps TARGET_ENV=/absolute/path/to/target.env
make target-smoke TARGET_ENV=/absolute/path/to/target.env
```

查看 Go 服务日志：

```sh
docker compose --env-file /absolute/path/to/target.env -f docker-compose.yml \
  --profile writers --profile external-effects \
  logs --tail=200 admin web edge gateway-blue gateway-green usage-collector quota account-failover
```

输出前删除管理密钥、用户 Key、OAuth、Webhook、邮箱、私有域名和目标地址。

## Edge 健康但访问 502

依次检查：

1. `edge` 同时连接 `<project>_control` 与 `<project>_ingress`。
2. `gateway-blue`、`gateway-green` 和 `admin` 同时连接 Control 与配置的上游网络。
3. `web` 连接 Control 网络，Admin 健康。
4. `state/edge/active-gateway.conf` 只选择 `blue` 或 `green` 对应配置。

`run.sh` 的内部 `up-core` 动作会逐个启动并校验精确容器/服务标签、网络与端口；不要用手工连接未知容器作为长期修复。

## Gateway 返回 401、503 或 502

| 状态 | 含义 | 检查 |
|---|---|---|
| `401` | 外部 Key 不在有效鉴权快照 | Key 是否撤销、用户是否有有效路由、鉴权快照是否已刷新 |
| `503 authentication_snapshot_unavailable` | 快照损坏、缺失或超过新鲜度边界 | `state/gateway/auth-snapshot.json`、Collector/Failover 日志与文件权限 |
| `502 upstream_unavailable` | 已鉴权但目标账号不可达 | 账号容器、内部网络、内部 Key 和上游错误 |

不得用 `/v1/models` 成功推断 Responses/SSE 正常；必须发送实际请求。

## 管理页面白屏或静态资源 404

```sh
curl --noproxy '*' -I http://127.0.0.1:<public-port>/admin/
curl --noproxy '*' -I http://127.0.0.1:<public-port>/portal/assets/codex-cpa-cluster-logo.svg
```

入口 HTML 应为 `no-cache`，带内容指纹的 JS/CSS 应为长期不可变缓存，稳定品牌 SVG 应为 `no-cache`。检查 `web` 镜像是否与发布 Manifest 的 Web 源码摘要一致。

## 用量不更新

检查 `usage-collector` 健康和日志、`state/usage.sqlite3` 权限/磁盘空间、Gateway 访问日志是否持续产生事件，以及 Collector 是否仍持有 Writer Lease。Generation 改变后的旧进程不能继续写入。

## 所有 CPA 都因代理投影缺失而不可用

普通账号更新必须先把路由迁移到有额度的健康 CPA 并等待进行中请求排空；如果所有账号都因同一个代理投影问题而不可用，就不存在安全迁移目标。此时只能对一个已经确认不可用、仍有路由的账号执行受限恢复：

```http
POST /admin/api/accounts/repair-proxy
X-Management-Key: <当前管理密钥>
Content-Type: application/json

{
  "id": "<account-id>",
  "proxy_url": "http://<existing-proxy>:<port>",
  "confirm": "repair-proxy:<account-id>"
}
```

该动作只允许写入新的独立代理并重建同一个账号；不能同时修改邮箱、标识、启停策略或路由。如果源账号仍可用、没有路由，或已经存在有剩余额度的安全目标，接口会拒绝。第一个账号恢复额度读取后，其余账号必须回到普通更新流程，使用路由迁移与请求排空。代理值只进入加密控制面和生成配置，不会出现在响应中。

这不是修改外部代理拓扑的接口。执行前仍需单独确认既有代理容器、网络与端口健康，并保留控制库、主密钥、OAuth 和账号配置备份。

## 所有权激活失败

如果已有活动所有者，先查明并停止既有 Writer，等待或显式完成受控所有权交接。空所有权历史只允许：

- 隔离测试：`CPA_BOOTSTRAP_MODE=isolated-test`。
- 经批准的正式切换：`CPA_BOOTSTRAP_MODE=controlled-cutover`，并用 `CPA_CONFIRM_WRITERS_STOPPED` 精确重复已停止全部既有 Writer 的目标目录。

不要删除所有权记录或直接修改 SQLite 绕过 Fencing。

## 发布或镜像校验失败

- 四个镜像必须来自同一发布 Manifest。
- 目标引用必须是发布描述中的 `:sha256-<源码摘要>` 标签。
- `io.codex-cpa.component`、`io.codex-cpa.component-digest` 与 `io.codex-cpa.source-digest` 必须匹配。
- 移动标签、容器启动时间和本地 image ID 不能代替组件源码摘要。
- Registry 凭据只存在于操作者与目标机 Docker Credential Store，不打印到日志。

## Gateway 排空或 Edge 维护确认失败

- 排空超时不会终止旧 SSE；旧槽保持运行。修复异常长请求后重新执行 `up-core`，脚本仍会先检查该槽 `/__stats`。
- Edge 镜像或 Compose Hash 变化时，`verify-images` 会在重建前失败。只有已批准维护窗口时，才设置 `CPA_ALLOW_EDGE_RECREATE=true`，并用 `CPA_CONFIRM_EDGE_MAINTENANCE` 精确重复 `CPA_DEPLOY_ROOT`。
- `target.env` 是正式控制面唯一配置来源；不要把配置中心的 `state/compose.env` 改造成第二份控制面环境文件。
