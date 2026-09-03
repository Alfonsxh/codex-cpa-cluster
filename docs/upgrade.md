# 升级与切换

## 日常升级

已经初始化的目标直接执行：

```sh
curl -fsSL https://github.com/Alfonsxh/codex-cpa-cluster/releases/latest/download/run.sh | sudo sh
```

命令重新获取最新 `run.sh`，然后自动复用 `/home/cpac/config.env` 中的域名和入口模式，在 `/home/cpac/backups/` 创建 root-only 备份并执行正式蓝绿部署。升级成功后会删除旧版 `/home/cpac/deploy.sh` 入口。升级绝不因缺少 Nginx、证书或不同域名站点而改变既有入口模式：`external` 始终不触碰 Nginx/Certbot 且跳过公网检查；`managed` 只更新 CPAC 自己托管的站点。旧版无标记站点不会被自动认领，保留旧站点时应先执行 `sudo /home/cpac/run.sh ingress set external`。旧版本的 `/etc/cpac/config.env` 和待领取管理员凭据会先安全迁移到 `/home/cpac/`。固定版本排障可使用 `sudo /home/cpac/run.sh run --version v2.0.0`。需要切换入口时必须显式确认 `sudo /home/cpac/run.sh ingress set managed|external`，再执行日常部署。

## 前置条件

升级只面向已经初始化的目标，必须同时存在：

- `state/control-plane.sqlite3`
- `state/usage.sqlite3`
- 当前发布包中的 `docker-compose.yml` 与 `release-manifest.json`
- 匹配的 `secrets/control-plane.key`
- `state/gateway/`、`state/edge/active-gateway.conf` 与可写的 `logs/gateway/`
- 当前账号 OAuth 与运行配置

发布工具不会从退役控制文件初始化目标，也不会替换现有 OAuth。数据库 Schema 新于目标镜像支持范围时必须停止，不得降级数据文件。

## 发布版本

操作者工作站执行：

```sh
make verify
npm --prefix frontend run test:e2e
make images VERSION=v2.0.0 PLATFORM=linux/amd64
make publish VERSION=v2.0.0 IMAGE_PREFIXES=ghcr.io/owner
make package VERSION=v2.0.0
```

`control`、`web`、`gateway` 和 `edge` 由各自源码摘要生成不可变标签；目标使用同一 `release-manifest.json` 校验镜像组件标签。CI 只校验和打包，不持有或连接目标。

## Test 顺序

1. 记录升级前数据库 `quick_check`、Schema、关键行数、活动槽和真实请求结果。
2. 由统一脚本通过 SQLite Backup API 生成两份 `quick_check=ok` 的一致性数据库副本，并与匹配主密钥、OAuth 和账号配置一起生成可恢复备份；归档不得混入运行中的 WAL/SHM 文件。
3. 将同一发布包的 `docker-compose.yml` 与 `release-manifest.json` 放入 Test 目标目录，再使用仓库外 Test `target.env` 执行 `config`、`pull` 和 `verify-images`；只接受发布描述中的源码摘要标签，并拒绝不匹配的 Compose 副本、符号链接运行目录、缺失活动槽或不可判定的 Compose Hash。
4. 确认既有 Writer 已停止后，完成受控所有权激活。
5. 执行 `up-core`：更新非活动 Gateway、切换新请求、等待旧槽排空，再更新旧槽；随后执行 `up-writers` 和 `smoke`。通知另行批准。
6. 验证 Admin、Portal、使用中心、浏览器矩阵和同一个真实 API Key 的模型、非流式 Responses、SSE。
7. 对比升级前后数据库事实，确认用量只增不减且 API Key/路由未被重建。

```sh
make target-pull TARGET_ENV=/absolute/path/to/test.env
make target-verify-images TARGET_ENV=/absolute/path/to/test.env
make target-activate TARGET_ENV=/absolute/path/to/test.env
make target-up-core TARGET_ENV=/absolute/path/to/test.env
make target-up-writers TARGET_ENV=/absolute/path/to/test.env
make target-smoke TARGET_ENV=/absolute/path/to/test.env
```

## 最小影响原则

- Web 或 Admin 更新不要求重建上游账号容器。
- 新 Gateway 先在非活动槽健康，再由稳定 Edge 只切换新请求。
- 已建立 SSE 留在原槽排空，不重放。
- Edge 持有公开端口；重建 Edge 必须有明确维护窗口和端口验证。
- Writer 只有一个有效 Generation，所有权切换后旧进程失败关闭。
- Edge 镜像或 Compose 配置变化不是普通蓝绿更新，必须显式确认维护窗口；没有确认时部署在重建前失败关闭。

## 回滚

镜像或路由验收失败时停止新 Writer，恢复上一组不可变镜像、原活动槽和已验证备份。数据库只能回到与备份成对的主密钥和声明兼容的应用版本；不得仅回滚容器而忽略已发生的 Schema/业务写入。

Production 切换必须复用已在 Test 通过的发布摘要，但重新采集 Production 自身的备份、运行状态和真实 API Key 证据。Test 健康不能作为 Production 已验收的证明。
