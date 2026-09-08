# 部署

## 运行模型

正式拓扑只使用 `docker-compose.yml`，镜像固定为：

- `codex-cpa-control`
- `codex-cpa-web`
- `codex-cpa-gateway`
- `codex-cpa-edge`

Control 镜像包含 Admin 与各 Worker 二进制；不同容器使用不同入口启动，以便独立健康检查、重启和最小影响更新。

## 名称与现有部署兼容

产品与 GitHub 发行源统一使用 Codex CPA Pool（`Alfonsxh/codex-cpa-pool`）。新发布归档使用 `codex-cpa-pool` 前缀，安装器仍可读取历史归档。新安装的默认运维根目录是 `/home/ccpa`，安装器参数和配置使用 `CPAP_*`。旧 `CPAC_*` 参数、配置与 `/home/cpac` 中的已安装环境继续被识别并原址沿用；改名不会移动运行数据或重建 SQLite、API Key、OAuth。升级只替换既有 `target.env` 的 Control、Web、Gateway、Edge 四个镜像字段，保留目标身份、网络、端口和维护确认配置；指定的历史 Release 若附带旧安装器，则保留当前 Pool 安装器，避免退回旧安装协议。

下文以新默认目录为例，已安装环境应使用部署完成卡片中报告的实际运维根目录。

## 全新目标

全新单机目标使用唯一的部署脚本，不要手工创建 SQLite、主密钥或快照：

```sh
curl -fsSL https://github.com/Alfonsxh/codex-cpa-pool/releases/latest/download/run.sh | sudo sh
```

管道入口会把 Release 中的 `run.sh` 原子安装到内部运维目录，再重新连接当前终端并执行，因此首次安装仍可交互输入域名和入口模式。`scripts/run.sh` 随后校验 GitHub Release 中的脚本、归档和机器可读发布环境，保留宿主机时区，全部 Codex CPA Pool 服务及后续创建的业务 CPA 容器使用 UTC。业务时区由首次 Web 设置及配置中心的 `system.timezone` 统一管理，不再读取 `CPA_TIMEZONE`。在 `/home/ccpa/` 同一文件系统创建临时根目录后，通过 Control 镜像内的 `cpa-bootstrap` 一次性生成两份当前 Schema 的 SQLite、32 字节主密钥、随机管理凭据、空 Gateway 快照、初始蓝槽文件和账号容器只读挂载所需的 `management/config/static` 目录。临时目标完整后才原子重命名为 `/home/ccpa/runtime`。初始化工具拒绝任何已有权威文件和符号链接运行目录，不能用于修复或覆盖既有目标；旧版本升级时，统一脚本在备份后幂等补齐缺失的空运行目录。

交互执行使用分阶段终端界面：成功阶段隐藏底层命令噪声，失败阶段展开完整诊断；`managed` 模式的最终完成卡片显示 `https://<域名>/admin/`，`external` 模式明确要求从既有反向代理的入口访问 `/admin/`。`NO_COLOR=1` 仅关闭 ANSI 颜色，不改变步骤、错误或安全语义。

域名和入口模式写入 `/home/ccpa/config.env`，待领取的首次管理员凭据临时写入 `/home/ccpa/bootstrap-admin.key`。首次交互部署先检测 Nginx、同域名站点与证书，再选择 `managed` 或 `external`；无交互部署必须明确传入 `--ingress managed|external`。旧版本位于 `/etc/cpac/` 的两个文件会先经过一致性校验，再迁移到解析后的实际运维根目录并删除旧副本；任何冲突都会停止部署。`managed` 模式才会安装/启动 Nginx 和 Certbot、把 Codex CPA Pool 专属站点指向 `127.0.0.1:<CPA_PUBLIC_PORT>` 并申请或复用证书。新站点带 `# Managed by Codex CPA Pool run.sh` 标记，旧托管标记继续用于识别既有站点；同名未托管站点会失败关闭，绝不覆盖。`external` 模式不安装、不启动、不改动 Nginx/Certbot，不申请证书，也不对公网发起健康检查；操作者将既有反向代理指向 `127.0.0.1:<CPA_PUBLIC_PORT>`，保留 `Host`、`X-Forwarded-*`，支持 WebSocket/SSE 和 3600 秒流式超时，并自行验证 `<既有入口>/__health` 返回 `200`。 Nginx 配置与 `external` 提示均读取既有 `target.env` 的 `CPA_PUBLIC_PORT`；新安装默认为 `18317`，即 `127.0.0.1:18317`，既有环境使用其实际端口。

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

`docker-compose.yml` 与 `release-manifest.json` 必须来自本次选择的同一个发布包。主密钥必须与控制库匹配，`active-gateway.conf` 必须只选择 `blue` 或 `green`，`logs/gateway/` 必须允许镜像内 UID `10001` 写入。`run.sh` 的内部目标动作不会初始化新目标、导入退役 JSON、替换 OAuth，或沿符号链接读写运行数据；首次初始化只由同一脚本在未发布的临时根目录调用镜像内 `cpa-bootstrap` 完成。

成功部署会在各阶段后报告可验证结果，并在带完整左右边框的完成卡片中汇总版本变化、Control/Web/Gateway/Edge 镜像更新或复用、Gateway 槽位切换和旧槽排空、Admin/Web/Edge 与五个后台任务容器动作、升级备份及入口模式。`external` 入口会明确标记 Nginx/Certbot 未修改，同时仍给出按所记录域名生成的站点和管理员登录链接。

## 镜像发布

```sh
make -f scripts/build.mk verify
make -f scripts/build.mk images VERSION=v2.0.0 PLATFORM=linux/amd64
make -f scripts/build.mk publish VERSION=v2.0.0 IMAGE_PREFIXES=ghcr.io/owner
make -f scripts/build.mk package VERSION=v2.0.0
```

四个组件均按源码摘要构建不可变标签。发布开始时只生成一次四组件摘要计划，并通过远端
Manifest/Config 判断 `reuse`、`promote` 或 `build`：已存在且标签一致的组件不会重建，只有内容
标签的组件直接在 Registry 内创建版本标签，真正缺失的组件才进入 Buildx Bake。Bake 会并行复用
共享阶段并将内容、版本标签一起推送；版本标签不匹配时在移动任何 `latest` 前失败。所有 Registry
的四组件不可变标签完成并重新校验后，才远端移动 `latest`，全过程不通过本机 `pull/tag/push`
搬运镜像层。`cpa-releasectl` 负责 Manifest、发布描述、远端镜像标签解析、归档安全和隐私检查。
目标先用镜像标签完成非执行式身份校验，只有 Control 镜像与不可变标签一致后，才允许运行其中的
`cpa-releasectl` 读取 Manifest。

## Test 应用顺序

目标环境文件从 `.env.example` 生成并保存在仓库外。四个镜像必须使用发布描述中的 `:sha256-<源码摘要>` 标签。`CPA_CONFIRM_DEPLOY_ROOT`、首次接管确认和 Edge 维护确认在示例中故意留空，必须由操作者针对本次目标显式填写。正式控制面的端口、镜像、Compose 身份和所有权参数只来自该 `target.env`；配置中心生成的 `state/compose.env` 只服务业务 CPA 账号容器。

普通升级执行：

```sh
make -f scripts/build.mk target-config TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-pull TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-verify-images TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-activate TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-up-core TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-up-writers TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-smoke TARGET_ENV=/absolute/path/to/test.env
```

通知进程随 `up-writers` 常驻启动，普通安装、升级与回滚都会检查其容器、网络、镜像及调度心跳。配置中心的通知开关只控制自动发送；关闭通知或未配置 Webhook 时，进程保持待命。原 `up-notifications` 入口保留兼容。

配置中心分别展示通知开关、后台调度心跳、最近发送成功和下次发送时间。心跳超过 3 分钟未更新时显示中断提示并隐藏下次发送时间；手动发送成功不会改变调度状态。通知容器健康检查只判断调度心跳，历史发送错误在页面展示，不阻塞应用升级。发送计划按系统时区计算；超过补发窗口的漏发不会自动补发。

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
