# 部署

## 运行模型

正式拓扑只使用 `docker-compose.yml`，镜像固定为：

- `codex-cpa-control`
- `codex-cpa-web`
- `codex-cpa-gateway`
- `codex-cpa-edge`

Control 镜像包含 Admin 与各 Worker 二进制；不同容器使用不同入口启动，以便独立健康检查、重启和最小影响更新。

## 全新目标

全新单机目标使用唯一的部署脚本，不要手工创建 SQLite、主密钥或快照：

```sh
sudo /home/cpac/deploy.sh
```

`scripts/deploy.sh` 校验 GitHub Release 中的脚本、归档和机器可读发布环境，在 `/home/cpac/` 同一文件系统创建临时根目录，然后通过 Control 镜像内的 `cpa-bootstrap` 一次性生成两份当前 Schema 的 SQLite、32 字节主密钥、随机管理凭据、空 Gateway 快照、初始蓝槽文件和账号容器只读挂载所需的 `management/config/static` 目录。临时目标完整后才原子重命名为 `/home/cpac/runtime`。初始化工具拒绝任何已有权威文件和符号链接运行目录，不能用于修复或覆盖既有目标；旧版本升级时，统一脚本在备份后幂等补齐缺失的空运行目录。

交互执行使用分阶段终端界面：成功阶段隐藏底层命令噪声，失败阶段展开完整诊断；`managed` 模式的最终完成卡片显示 `https://<域名>/admin/`，`external` 模式明确要求从既有反向代理的入口访问 `/admin/`。`NO_COLOR=1` 仅关闭 ANSI 颜色，不改变步骤、错误或安全语义。

域名和入口模式写入 `/home/cpac/config.env`，待领取的首次管理员凭据临时写入 `/home/cpac/bootstrap-admin.key`。首次交互部署先检测 Nginx、同域名站点与证书，再选择 `managed` 或 `external`；无交互部署必须明确传入 `--ingress managed|external`。旧版本位于 `/etc/cpac/` 的两个文件会先经过一致性校验，再迁移到统一目录并删除旧副本；任何冲突都会停止部署。`managed` 模式才会安装/启动 Nginx 和 Certbot、把 CPAC 专属站点指向 `127.0.0.1:18317` 并申请或复用证书。该站点带 `# Managed by CPAC deploy.sh` 标记；同名未托管站点会失败关闭，绝不覆盖。`external` 模式不安装、不启动、不改动 Nginx/Certbot，不申请证书，也不对公网发起健康检查；操作者将既有反向代理指向 `127.0.0.1:18317`，保留 `Host`、`X-Forwarded-*`，支持 WebSocket/SSE 和 3600 秒流式超时，并自行验证 `<既有入口>/__health` 返回 `200`。

## 底层目标前置条件

目标目录必须已经存在，并至少包含：

```text
docker-compose.yml
release-manifest.json
state/control-plane.sqlite3
state/usage.sqlite3
secrets/control-plane.key
state/gateway/
state/edge/
state/edge/active-gateway.conf
logs/gateway/
```

`docker-compose.yml` 与 `release-manifest.json` 必须来自本次选择的同一个发布包。主密钥必须与控制库匹配，`active-gateway.conf` 必须只选择 `blue` 或 `green`，`logs/gateway/` 必须允许镜像内 UID `10001` 写入。`deploy.sh` 的内部目标动作不会初始化新目标、导入退役 JSON、替换 OAuth，或沿符号链接读写运行数据；首次初始化只由同一脚本在未发布的临时根目录调用镜像内 `cpa-bootstrap` 完成。

成功部署会在各阶段后报告可验证结果，并在带完整左右边框的完成卡片中汇总版本变化、Control/Web/Gateway/Edge 镜像更新或复用、Gateway 槽位切换和旧槽排空、Admin/Web/Edge 与四个 Writer 容器动作、升级备份及入口模式。`external` 入口会明确标记 Nginx/Certbot 未修改，同时仍给出按所记录域名生成的站点和管理员登录链接。

## 镜像发布

```sh
make verify
make images VERSION=v2.0.0 PLATFORM=linux/amd64
make publish VERSION=v2.0.0 IMAGE_PREFIXES=ghcr.io/owner
make package VERSION=v2.0.0
```

四个组件均按源码摘要构建不可变标签。`cpa-releasectl` 负责 Manifest、发布描述、归档安全和隐私检查。目标先用镜像标签完成非执行式身份校验，只有 Control 镜像与不可变标签一致后，才允许运行其中的 `cpa-releasectl` 读取 Manifest。

## Test 应用顺序

目标环境文件从 `.env.example` 生成并保存在仓库外。四个镜像必须使用发布描述中的 `:sha256-<源码摘要>` 标签。`CPA_CONFIRM_DEPLOY_ROOT`、首次接管确认和 Edge 维护确认在示例中故意留空，必须由操作者针对本次目标显式填写。正式控制面的端口、镜像、Compose 身份和所有权参数只来自该 `target.env`；配置中心生成的 `state/compose.env` 只服务业务 CPA 账号容器。

普通升级执行：

```sh
make target-config TARGET_ENV=/absolute/path/to/test.env
make target-pull TARGET_ENV=/absolute/path/to/test.env
make target-verify-images TARGET_ENV=/absolute/path/to/test.env
make target-activate TARGET_ENV=/absolute/path/to/test.env
make target-up-core TARGET_ENV=/absolute/path/to/test.env
make target-up-writers TARGET_ENV=/absolute/path/to/test.env
make target-smoke TARGET_ENV=/absolute/path/to/test.env
```

通知带外部副作用，单独执行 `up-notifications`。

`up-core` 先确认非活动 Gateway 已无遗留请求，再更新该槽；需要更新活动槽时，Edge 先把新请求切到已验证的新槽，旧槽的 `/__stats` 归零后才重建。排空超时会保留旧容器和已有 SSE 并使部署失败，重试仍会先等待它排空。Edge 自身镜像或 Compose 配置变化时，必须设置 `CPA_ALLOW_EDGE_RECREATE=true`，并用 `CPA_CONFIRM_EDGE_MAINTENANCE` 精确重复目标目录；该操作有明确的单端口维护窗口。

## 上线验收

切换前后分别记录：

1. 两份 SQLite 的 `quick_check`、Schema 版本和关键行数。
2. 四个镜像的不可变引用、组件标签和源码摘要。
3. `/__health`、公开路径 404/401、内部快照接口。
4. 同一个真实 API Key 的 `/v1/models` 和 `/v1/responses` 非流式请求。
5. SSE 的 created、delta、completed 与 `[DONE]`，以及切槽期间已有请求排空。
6. Admin、Portal、使用中心与浏览器矩阵。

Production 不由 CI 连接。只有操作者在目标机本地选择版本、备份并应用；Pod 或容器健康不等同于业务验收。
