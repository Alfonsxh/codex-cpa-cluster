# 升级指南

## 升级前提

当前版本以 SQLite 已成为权威存储为前提。目标目录必须同时存在：

- `state/control-plane.sqlite3`
- `secrets/control-plane.key`

只剩旧控制 JSON、尚未迁移 SQLite 的环境不能直接升级，必须先使用最后一个支持 JSON 导入的过渡版本。

## 升级前检查

```bash
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml exec -T admin codex-cpa store verify
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml exec -T admin codex-cpa health
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml exec -T admin codex-cpa verify-routing
```

然后执行一次成对备份，参阅[备份与恢复](backup-and-restore.md)。

## 执行升级

统一入口会下载并校验发布附件，然后调用只面向已初始化目录的蓝绿升级器：

```bash
sudo /opt/codex-cpa-cluster/bin/codex-cpa upgrade v1.1.0
```

发布包和当前活动的四个应用镜像必须属于同一组件指纹。需要从内部 Registry 拉取同版本镜像时，使用
`--image-prefix registry.example.com/team`。同版本发布中的 Go v2 候选镜像仅供隔离 Test 使用，当前
升级器不会拉取或应用它们。

部署器会：

1. 获取目标机运行锁，避免与 CPA 镜像更新并发。
2. 校验发布清单和组件镜像指纹。
3. 在线备份控制面与用量数据库。
4. 把目标应用组件暂存到 SQLite，生成 `state/compose.env` 后应用 Admin、Web 和 Gateway 变化。
5. 在 inactive Gateway 完成真实鉴权、额度与路由验证。
6. 平滑切换 Edge 并等待旧连接排空。
7. 使用各业务 CPA 发布前的不可变 image ID 完成必要的 Compose 对账并验证模型列表。
8. 再次验证数据库、页面、内部 Key 和外部 Key 路由，最后把暂存版本标记为已应用。

`runtime.cliproxy_image` 可以继续配置为 `:latest`，它只用于显式“拉取镜像”。升级应用本身
不会拉取或应用该移动标签；候选 CPA 版本必须由账号管理中的镜像更新流程独立验收。

普通升级默认拒绝重建稳定 Edge。确需更新时，应在维护窗口显式设置：

```bash
sudo /opt/codex-cpa-cluster/bin/codex-cpa upgrade v1.1.0 \
  --allow-edge-recreate
```

## 不可逆迁移

`store cleanup-projections` 会在严格校验后删除旧控制 JSON。完成后不能回滚到仍依赖这些 JSON 的旧应用版本。

升级前必须阅读目标版本 Release Notes。未来版本应在发布清单中声明最低来源版本和数据库兼容范围；跨越不兼容版本时应逐个升级。

## 回滚原则

- 应用或路由验证失败时，部署器恢复上一版 Compose、镜像和活动 Gateway slot。
- 数据库不能盲目降级；只允许回滚到声明兼容当前 Schema 的应用版本。
- 新 Gateway 仍有流式请求时不会被强制停止，应等待排空或人工确认。
- 备份未验证可恢复之前，不执行不可逆清理。
