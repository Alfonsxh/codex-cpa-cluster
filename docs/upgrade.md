# 升级

适用于已安装的 Go 2.x 环境。Python v1 环境先按[迁移手册](python-to-go-migration.md)处理。

## 执行升级

进入原运维目录，运行：

```sh
sudo ./run.sh
```

脚本读取已保存的部署目录和入口配置，校验正式 Release、备份恢复必需数据，再拉取镜像并切换服务。数据目录保持原位，无需重复输入 `CPAP_DEPLOY_ROOT`。

| 操作 | 命令 |
| --- | --- |
| 升级到最新正式版 | `sudo ./run.sh` |
| 查看并选择更高的正式版本 | `sudo ./run.sh --tag` |
| 指定已有 Release | `sudo ./run.sh --tag v2.0.2` |

`--tag` 不带版本时，非交互环境只列候选，不执行升级。指定版本必须具有完整 Release 附件。

## 目录与入口

新安装默认使用 `/home/ccpa`，历史环境使用原运维目录。`config.env` 保存域名、入口模式和真实运行目录；`runtime` 可以是指向既有部署的软链接。

若尚未记录自定义目录，可在核对部署身份后向现有 `config.env` 添加 `CPAP_DEPLOY_ROOT=/srv/ccpa-runtime`。保留其他字段，权限为 `0600`；目录冲突或无效链接会停止升级。

升级保留 `target.env` 的身份、端口和网络配置。`external` 沿用既有反向代理；`managed` 管理本项目的 Nginx/TLS。切换入口须显式执行 `./run.sh ingress set managed|external`。详见[部署](deployment.md)。

## 完成与失败处理

- 完成后检查管理页面，并用原 API Key 验证实际模型请求和 SSE。
- Gateway 先切换新请求，再等待旧连接排空；超时会保留旧槽，处理后重试。
- Edge 自身更新需要维护确认，不能视为无中断的 Gateway 切换。
- 服务应用失败时会尝试恢复上一发布配置，不自动用旧数据库覆盖业务数据。
- 备份位于运维目录的 `backups/`。恢复前先确认数据库与应用兼容；已产生新写入时，不直接用旧备份覆盖。

恢复步骤见[备份与恢复](backup-and-restore.md)，升级异常见[故障排查](troubleshooting.md)。
