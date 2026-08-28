# 故障排查

所有命令都必须使用当前操作者提供的 Test/Production 私有环境文件。先确认目标，再执行只读诊断；不要从旧归档或 Git 历史推断主机和目录。

```sh
V2_ENV_FILE=/absolute/path/to/target.env sh scripts/deploy-target.sh config
V2_ENV_FILE=/absolute/path/to/target.env sh scripts/deploy-target.sh ownership-status
V2_ENV_FILE=/absolute/path/to/target.env sh scripts/deploy-target.sh ps
V2_ENV_FILE=/absolute/path/to/target.env sh scripts/deploy-target.sh smoke
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

`deploy-target.sh up-core` 会逐个启动并校验精确容器/服务标签、网络与端口；不要用手工连接未知容器作为长期修复。

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

## 所有权激活失败

如果已有活动所有者，先查明并停止旧 Writer，等待或显式完成受控所有权交接。空所有权历史只允许：

- 隔离测试：`V2_BOOTSTRAP_MODE=isolated-test`。
- 经批准的正式切换：`V2_BOOTSTRAP_MODE=legacy-cutover`，并精确确认旧 Writer 已停止的目标目录。

不要删除所有权记录或直接修改 SQLite 绕过 Fencing。

## 发布或镜像校验失败

- 四个镜像必须来自同一发布 Manifest。
- `io.codex-cpa.component` 与 `io.codex-cpa.component-digest` 必须匹配。
- 移动标签、容器启动时间和本地 image ID 不能代替组件源码摘要。
- Registry 凭据只存在于操作者与目标机 Docker Credential Store，不打印到日志。
