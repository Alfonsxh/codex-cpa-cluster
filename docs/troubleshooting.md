# 故障排查

## 基础诊断

优先收集以下只读信息：

```bash
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml ps
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml exec -T admin codex-cpa status
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml exec -T admin codex-cpa health
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml exec -T admin codex-cpa store verify
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml exec -T admin codex-cpa verify-routing
```

查看最近日志：

```bash
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml \
  logs --tail=200 admin web edge gateway-blue gateway-green
```

提交 Issue 前请移除管理密钥、用户 Key、OAuth 内容、Webhook、邮箱和私有域名。

## Edge 无法启动

检查 `18317` 是否已被旧 Gateway 或其他进程占用：

```bash
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml ps
docker ps --format 'table {{.Names}}\t{{.Ports}}'
```

从旧单 Gateway 首次迁移到稳定 Edge 时，两个容器不能同时绑定同一个宿主机端口，需要维护窗口。

## Gateway 健康但 API 返回 401

依次确认：

1. 使用的是外部用户 Key，而不是管理密钥。
2. 用户 Key 未撤销，且用户已经选择有效 CPA 账号。
3. `state/gateway/` 快照已经重新渲染。
4. 业务 CPA 容器内的内部 Key 与 Gateway 快照一致。

执行：

```bash
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml exec -T admin codex-cpa render
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml exec -T admin codex-cpa verify-routing
```

## 管理中心可用但用量不更新

```bash
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml ps usage-collector
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml logs --tail=200 usage-collector
```

确认 `state/usage.sqlite3` 可写、磁盘空间充足，并检查各 CPA 日志目录是否持续产生事件。

## Compose 配置无法解析

```bash
docker compose \
  --env-file .env \
  --env-file state/compose.env \
  -f docker-compose.yml \
  -f compose.accounts.yml \
  config --quiet
```

`compose.accounts.yml` 是数据库渲染产物，不应手工修改。修复控制面数据后重新执行 `codex-cpa render`。

## 发布失败

- 确认工作区干净，版本 Tag 指向当前提交。
- 确认 Docker 与 `gh` 已分别登录。
- 已存在版本镜像但指纹不同属于不可恢复的版本冲突，必须使用新版本号。
- 已存在且指纹一致的镜像可以安全复用，再次执行同一发布命令即可续传。
