# 升级与切换

Python v1 首次切换到 Go v2 必须先完成 [保留数据迁移方案](python-to-go-migration.md) 中的兼容转换、演练和受控接管。下述日常升级入口不提供旧 Python 部署导入能力。

管理中心左下角采用单行展示：`管理 API 已鉴权 | 当前版本`，版本入口无边框和底色；窄屏沿用左下角入口，并为底部版本栏预留内容空间。有正式版更新时，版本位置替换为黄色心跳圆点和“有版本更新”；系统开启减少动态效果时停止心跳动画。有更新时点击提示直接打开对应发布说明，不再展示版本详情弹窗。自动检查间隔为 15 分钟；检查失败仍保留已读取的当前版本，版本无法识别时显示“版本未知”。更新候选仅接受非 Draft、非 Prerelease 且标签严格符合 `v数字.数字.数字` 的 GitHub Latest Release。该入口仅在管理中心展示，检查更新不会执行部署。

## 日常升级

已经初始化的目标直接执行：

```sh
sudo /home/ccpa/run.sh
```

从 Codex CPA Pool 的 GitHub Releases 下载升级脚本，安装器可读取历史发布归档。以下路径以新默认运维根目录 `/home/ccpa` 为例；历史 `/home/cpac` 安装继续原址运行，执行命令时使用其实际根目录，不迁移运行数据。新 `CPAP_*` 参数与配置兼容旧 `CPAC_*`；升级只替换既有 `target.env` 的 Control、Web、Gateway、Edge 四个镜像字段，保留目标身份、网络、端口与维护确认配置，Nginx 配置和 `external` 提示使用该文件的实际 `CPA_PUBLIC_PORT`（`18317` 仅为新安装默认值）。指定历史 Release 附带旧安装器时，会保留当前 Pool 安装器，避免退回旧安装协议。

脚本自动读取 `/home/ccpa/config.env` 中已记录的真实运行目录、域名和入口模式，获取最新 Release 并校验其部署脚本，在 `/home/ccpa/backups/` 创建 root-only 备份，再执行正式蓝绿部署。安装或升级成功后，真实运行目录自动保存为 `CPAP_DEPLOY_ROOT`；以后无需在命令行重复输入。修改域名或入口模式会保留该字段。升级成功后会删除旧版 `/home/ccpa/deploy.sh` 入口。升级绝不因缺少 Nginx、证书或不同域名站点而改变既有入口模式：`external` 始终不触碰 Nginx/Certbot 且跳过公网检查；`managed` 只更新本项目自己托管的站点。旧版无标记站点不会被自动认领，保留旧站点时应先执行 `sudo /home/ccpa/run.sh ingress set external`。旧版本的 `/etc/cpac/config.env` 和待领取管理员凭据会先安全迁移到解析后的实际运维根目录。

对于已有的自定义部署目录，可以在确认数据与 `target.env` 属于该部署后，将 `CPAP_DEPLOY_ROOT=/srv/ccpa-runtime` 加入现有 `config.env`，保留原有域名和入口字段。脚本只读取字段，不执行配置内容。已记录的目录优先于新旧默认目录；目录缺失、配置重复或环境变量指向不同部署时会停止，不会重新初始化其他目录。配置文件使用普通非软链接文件，权限保持 `0600`。可用 `--config /absolute/path/config.env` 选择另一份运维配置。

执行 `sudo /home/ccpa/run.sh --tag` 可读取 `.deploy-initialized` 中的当前版本，只查询正式 GitHub Releases，并列出所有更高版本。交互终端选择序号后才会进入升级流程；非交互环境只打印当前版本和候选版本，不改配置、不拉镜像、不升级。没有候选时会明确提示当前已是最新版本；尚未初始化的环境必须直接使用 Latest Release，或通过 `sudo /home/ccpa/run.sh --tag v2.0.0` 明确指定版本。指定 Tag 必须对应包含完整附件的 GitHub Release。兼容入口 `--version` 仍可使用，但新操作统一使用 `--tag`。需要切换入口时必须显式确认 `sudo /home/ccpa/run.sh ingress set managed|external`，再执行日常部署。

## 已有运行目录的软链接入口

默认运行目录是 `/home/ccpa/runtime`。如果数据已部署在其他目录，可以保留真实目录，并让 `runtime` 指向它。例如，确认 `/srv/ccpa-runtime` 是已初始化的部署，且 `/home/ccpa/runtime` 尚不存在后：

```sh
sudo ln -s /srv/ccpa-runtime /home/ccpa/runtime
sudo /home/ccpa/run.sh --tag
```

使用前需安装包含运行目录入口解析和配置持久化功能的 `run.sh`。直接运行和新版脚本的管道入口都会先读取已保存的目录并解析真实路径，版本查询、备份、部署身份校验和 Compose 操作均使用该真实路径；脚本自更新后继续沿用它。选择附带旧安装器的 Release 时，保留当前安装器，避免丢失软链接和已保存目录的支持。也可以通过 `CPAP_DEPLOY_ROOT` 显式传入指向同一部署的入口。

断链、循环链接、指向文件或文件系统根目录的链接会在写入前被拒绝。尚未记录目录时，新旧默认入口指向同一真实目录视为同一部署，指向不同目录则需明确选择；记录后不再受其他历史目录影响。运维目录与 `run.sh`、数据库、密钥及运行目录内部的非软链接要求继续保留；既有 `target.env` 中的路径和身份字段不会因入口变化而改写。

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
make -f scripts/build.mk verify
npm --prefix frontend run test:e2e
make -f scripts/build.mk images VERSION=v2.0.0 PLATFORM=linux/amd64
make -f scripts/build.mk publish VERSION=v2.0.0 IMAGE_PREFIXES=ghcr.io/owner
make -f scripts/build.mk package VERSION=v2.0.0
```

`control`、`web`、`gateway` 和 `edge` 由各自源码摘要生成不可变标签；发布工具一次计算四组件摘要，
复用 Registry 中标签一致的内容，仅用 Buildx Bake 构建缺失组件，并在四组件准备完成后才远端移动
`latest`。中断后重试不会重新拉取或重建已验证组件。目标使用同一 `release-manifest.json` 校验镜像
组件标签。CI 只校验和打包，不持有或连接目标。

## Test 顺序

1. 记录升级前数据库 `quick_check`、Schema、关键行数、活动槽和真实请求结果。
2. 由统一脚本通过 SQLite Backup API 生成两份 `quick_check=ok` 的一致性数据库副本，并与匹配主密钥、OAuth 和账号配置一起生成可恢复备份；归档不得混入运行中的 WAL/SHM 文件。
3. 将同一发布包的 `docker-compose.yml` 与 `release-manifest.json` 放入 Test 目标目录，再使用仓库外 Test `target.env` 执行 `config`、`pull` 和 `verify-images`；只接受发布描述中的源码摘要标签，并拒绝不匹配的 Compose 副本、符号链接运行目录、缺失活动槽或不可判定的 Compose Hash。
4. 确认既有 Writer 已停止后，完成受控所有权激活。
5. 执行 `up-core`：更新非活动 Gateway、切换新请求、等待旧槽排空，再更新旧槽；随后执行 `up-writers` 和 `smoke`。通知另行批准。
6. 验证 Admin、Portal、使用中心、浏览器矩阵和同一个真实 API Key 的模型、非流式 Responses、SSE。
7. 对比升级前后数据库事实，确认用量只增不减且 API Key/路由未被重建。

```sh
make -f scripts/build.mk target-pull TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-verify-images TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-activate TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-up-core TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-up-writers TARGET_ENV=/absolute/path/to/test.env
make -f scripts/build.mk target-smoke TARGET_ENV=/absolute/path/to/test.env
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
