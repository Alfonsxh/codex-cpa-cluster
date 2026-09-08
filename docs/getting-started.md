# 快速开始

## 安装

准备可访问 GitHub 和镜像仓库的 Linux 主机；使用域名时先配置 DNS。

```sh
curl -fsSLO https://github.com/Alfonsxh/codex-cpa-pool/releases/latest/download/run.sh
sudo sh run.sh
```

按提示输入域名并选择入口：

| 模式 | 用途 |
| --- | --- |
| `external` | 复用已有反向代理和证书，按安装器提示连接本机 Edge |
| `managed` | 由安装器管理本项目的 Nginx 与 HTTPS |

新安装默认位于 `/home/ccpa`；已有环境沿用原目录。无交互安装需明确指定域名和入口，例如 `sudo sh run.sh run --domain cpa.example.com --ingress external`。

入口要求、自定义目录和部署校验见[部署](deployment.md)。

## 首次配置

1. 使用安装器显示的管理密钥访问 `/admin/`，进入首次设置。
2. 设置邮箱域名、用户初始密码、公开地址、系统时区和默认周额度。
3. 在账号管理中创建账号并完成 OAuth 授权。
4. 创建用户、分配账号并领取 API Key，在客户端验证模型请求。

品牌、通知和代理可在[配置中心](configuration-center.md)设置。空账号池可以健康启动，但尚不能提供模型服务。

## 后续操作

| 需求 | 文档 |
| --- | --- |
| 更新版本 | [升级](upgrade.md) |
| 保存和恢复数据 | [备份与恢复](backup-and-restore.md) |
| 排查请求或通知异常 | [故障排查](troubleshooting.md) |
| 本地开发、预览与测试 | [开发指南](development.md) |
