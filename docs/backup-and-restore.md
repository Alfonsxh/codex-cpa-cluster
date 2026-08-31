# 备份与恢复

## 必须成对保存

最小业务备份包括：

```text
state/control-plane.sqlite3
state/usage.sqlite3
secrets/control-plane.key
auth/
configs/
state/gateway/
state/edge/
```

`control-plane.sqlite3` 中的秘密依赖 `control-plane.key`；缺少任一文件都不能视为可恢复备份。OAuth、Webhook、API Key、邮箱和私有地址不得进入仓库、发布包或公开日志。

## 备份流程

1. 记录当前四个不可变镜像引用、组件摘要、Compose 项目名和活动 Gateway 槽。
2. 停止 `usage-collector`、`quota`、`account-failover`、`notifications` 和 `log-maintenance`，阻止新的数据库写入。
3. 对两份 SQLite 使用支持 SQLite Backup API 的受控工具或一致性文件系统快照；不要在 WAL 模式下只复制主文件。
4. 同一批次复制匹配主密钥、OAuth、账号配置和 Gateway/Edge 状态。
5. 将备份保存到不同故障域，并限制为操作者可读。

统一的 `/home/cpac/deploy.sh` 在每次升级前自动执行上述数据库一致性步骤：使用 SQLite Backup API 生成两份独立副本，要求副本 `quick_check=ok`，不把运行中的 WAL/SHM 文件放入归档，再与同批主密钥、OAuth 和运行配置一起保存到 `/home/cpac/backups/`。这份升级前备份不替代异地灾备。

账号重命名、删除和 OAuth 清理产生的可恢复目录位于 `backups/accounts/`，由 `internal/accountlifecycle` 管理；它们不是两份 SQLite 的完整灾备替代品。

## 备份验收

在隔离目录验证：

- 两份 SQLite `PRAGMA quick_check` 返回 `ok`。
- Schema 版本不高于恢复镜像支持版本。
- 主密钥权限为 `0600`，控制面能读取现有加密秘密状态。
- 账号、用户、路由、团队和用量关键行数与备份前记录一致。
- OAuth 与账号 ID 对应，目录内不存在符号链接或额外秘密副本。

## 恢复顺序

1. 停止目标的全部 Go Control Writer 和账号生命周期写操作。
2. 恢复两份 SQLite、匹配主密钥、OAuth、账号配置与槽位/快照文件。
3. 恢复正确所有者和最小权限，不从备份启动未知脚本或二进制。
4. 使用与 Schema 兼容的不可变镜像执行 `make target-config` 和 `make target-verify-images`。
5. 激活唯一 Go 所有者，再依次启动核心服务与 Writer。
6. 执行目标烟测、数据库检查、浏览器检查和真实 API Key 的 Responses/SSE 验收。

恢复失败时保持 Writer 停止，不得用空数据库、错误主密钥或仅页面可打开作为成功条件。
