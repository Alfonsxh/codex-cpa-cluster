# 备份与恢复

## 必须成对保存的数据

控制面数据库中的秘密使用 `secrets/control-plane.key` 加密。只备份数据库或只备份主密钥都不足以恢复系统。

最小完整备份包括：

```text
state/control-plane.sqlite3
state/usage.sqlite3
secrets/control-plane.key
auth/
.env
```

建议同时保存 `state/compose.env`、`configs/`、`compose.accounts.yml` 和 `state/gateway/` 以便诊断，但它们可以从控制面数据库重新渲染。

## 在线备份

控制面数据库使用 SQLite Backup API：

```bash
mkdir -p backups/manual
chmod 700 backups/manual

docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml \
  exec -T admin codex-cpa store backup \
  "$PWD/backups/manual/control-plane.sqlite3"
```

不要在 WAL 模式下只复制 `.sqlite3` 主文件。

用量数据库和 OAuth 文件应在文件系统快照或受控维护窗口中备份。所有备份文件必须限制读取权限，并存放到与生产主机故障域不同的位置。

## 验证备份

```bash
docker compose --env-file .env --env-file state/compose.env \
  -f docker-compose.yml -f compose.accounts.yml exec -T admin codex-cpa store verify
```

定期在隔离目录执行恢复演练，确认：

- 数据库完整性检查通过。
- 主密钥可以解密全部秘密。
- OAuth 文件数量与账号对应。
- 账号、用户、路由和用量事件数量没有异常减少。

## 恢复顺序

1. 停止 Admin、采集器和业务 CPA，避免恢复期间继续写入。
2. 恢复启动用 `.env`、控制面数据库、用量数据库、主密钥和 OAuth 文件。
3. 检查文件所有者与权限，主密钥必须为 `0600`。
4. 启动 Admin 并执行 `store verify`。
5. 执行 `codex-cpa render` 重新生成 `state/compose.env` 和其他运行投影。
6. 启动全部服务并执行 `health` 与 `verify-routing`。

恢复操作应使用与数据库 Schema 兼容的应用版本。
