# 快速开始

本文面向第一次安装 Codex CPA Cluster 的操作者。系统使用单机 Docker Compose，不要求 Kubernetes。

## 前置条件

- Linux 主机或支持 Docker Desktop 的开发环境。
- Docker Engine 与 Docker Compose v2。
- Python 3.8+，用于校验发布附件与驱动安装状态机。
- 生产环境准备一个由外部反向代理终止 TLS 的域名。

默认端口只监听 `127.0.0.1`，不会直接暴露到公网。

应用 Python 运行时已经包含在 Admin 镜像中。现有部署升级还需要 Linux `flock`，用于串行化发布与运行时操作。

## 1. 下载统一入口

```bash
VERSION=v1.1.0
curl -fL "https://github.com/Alfonsxh/codex-cpa-cluster/releases/download/$VERSION/codex-cpa" \
  -o codex-cpa
chmod +x codex-cpa
```

若仓库或 Release 是私有的，可先设置只读 `GITHUB_TOKEN`。生产机 Registry 登录仍由 Docker Credential Store 管理。

## 2. 准备初始化配置

复制并修改 `config/profile.example.json`。至少需要确认：

- `branding.public_base_url`
- `identity.allowed_email_domains`
- `identity.key_prefix`

配置档案只在首次导入时写入 SQLite，之后应通过管理中心修改。

## 3. 安装并启动

```bash
sudo ./codex-cpa install "$VERSION" \
  --target /opt/codex-cpa-cluster \
  --profile /path/to/profile.json
```

安装器会验证发布包和镜像指纹、生成管理密钥、初始化 SQLite、渲染运行文件，并只启动活动的 `gateway-blue`。失败时保留目标目录供排障，不会删除持久化数据。

检查服务：

```bash
cd /opt/codex-cpa-cluster
docker compose -f docker-compose.yml -f compose.accounts.yml ps
docker compose -f docker-compose.yml -f compose.accounts.yml \
  exec -T admin codex-cpa store verify
```

## 4. 完成首次配置

打开 `http://127.0.0.1:18317/admin/`，使用 `/opt/codex-cpa-cluster/secrets/cpa-management.key` 的内容登录，然后依次完成：

1. 在“系统设置”中确认品牌、公开地址和允许的邮箱域名。
2. 配置固定初始密码；未配置时系统会拒绝新建或重置用户。
3. 在“账号管理”添加业务 CPA。
4. 为账号执行 OAuth 授权。
5. 创建用户并验证 `/v1/models` 路由。

浏览器只保存短期 HttpOnly 管理会话，不会把管理密钥写入 Web Storage。

确认首次登录可用后，把明文密钥和一次性配置档案从文件系统清理掉；密文已经保存在 SQLite：

```bash
cd /opt/codex-cpa-cluster
docker compose -f docker-compose.yml -f compose.accounts.yml \
  exec -T admin codex-cpa store migrate-secrets --cleanup
```

## 5. 配置生产 TLS

Edge 默认监听宿主机 `127.0.0.1:18317`。生产环境应由同机或可信网络内的反向代理提供 HTTPS，并把请求转发到该地址。

`branding.public_base_url` 只控制通知和客户端导出的地址，不会自动开启 TLS。不要把 Management 或业务 CPA 的内部端口直接暴露到公网。

## 下一步

- 了解组件职责：[架构说明](architecture.md)
- 配置完整参数：[配置中心](configuration-center.md)
- 建立备份：[备份与恢复](backup-and-restore.md)
- 升级已有环境：[升级指南](upgrade.md)
