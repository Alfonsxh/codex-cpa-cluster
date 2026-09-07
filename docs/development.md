# 开发与验证

## 依赖

- Go：版本由 `go.mod` 决定。
- Node.js 22：React 与 OpenAPI 生成。
- Docker Compose：隔离数据面和正式 Compose 校验。

```sh
npm ci --prefix frontend
npm ci --prefix tools/openapi
```

## 本地前端

前端只代理当前页面需要的细粒度接口。复制本地环境文件并设置已授权的 Test Admin：

```sh
cp frontend/.env.example frontend/.env
```

在 `frontend/.env` 中配置 `CPA_DEV_PROXY_TARGET` 后，同时启动 Admin、Usage 和 Portal
三个开发服务：

```sh
make frontend-dev-all
```

`frontend/.env` 被 Git 忽略；`frontend/.env.example` 只提供本机 `8318` 示例，不包含
测试目标地址。命令行 `FRONTEND_DEV_UPSTREAM=<URL>` 可临时覆盖文件值。

本机 Go Preview 只支持 Admin 的只读模拟接口。如需使用：

```sh
go run ./cmd/test-preview --address 127.0.0.1:8896 --root .
make frontend-dev FRONTEND_DEV_UPSTREAM=http://127.0.0.1:8896
```

Usage 和 Portal 必须连接完整的 Test Admin 后端。不要代理 Production 写接口。

## 常用门禁

根目录 `Makefile` 只提供本地前端开发与联调。隔离 Test Compose、源码验证、契约生成、
正式构建、发布和目标部署统一通过 `make -f scripts/build.mk <目标>` 调用；
以上命令均从仓库根目录执行。`test-build` 仅构建本地测试环境需要的镜像。

```sh
make -f scripts/build.mk generate-api
make -f scripts/build.mk check-generated-api
make -f scripts/build.mk verify
npm --prefix frontend run test:e2e
```

`make -f scripts/build.mk verify` 包含：

1. Shell 语法、Go 格式与 OpenAPI 生成一致性。
2. `go vet`、Go 单元测试和竞态测试。
3. React 类型检查、Vitest 和生产构建。
4. 发布隐私扫描。
5. 正式 Compose 与隔离 Test Compose 校验。
6. 已移除运行时残留检查与 `git diff --check`。

## 隔离数据面

```sh
make -f scripts/build.mk test-build
make -f scripts/build.mk test-up
make -f scripts/build.mk test-smoke
make -f scripts/build.mk test-faults
make -f scripts/build.mk test-down
```

故障演练覆盖上游不可用、无效 Key、损坏鉴权快照、Edge 非法槽位、蓝绿切换和 SSE 排空。

## Writer 所有权

所有写进程必须同时持有 `runtime-writer` 与自己的 Worker Lease。Generation 变化后，旧进程写入返回 `ErrLeaseLost`。

```sh
go test -count=1 -run '^TestWriterLeaseGenerationTransferFencesStaleOwner$' ./internal/ownership
go test -count=1 -run '^TestGoWorkerLeaseGroupTransfersAllScopesAndRejectsDuplicate$' ./internal/ownership
```

不得绕过所有权直接修改 SQLite，也不得用本地 Test 通过代替真实 API Key 的 `/v1/responses` 验收。

## 发布验收与重试

开发中的检查继续使用 `make -f scripts/build.mk verify` 和
`npm --prefix frontend run test:e2e`。准备发布时，先把最终改动形成干净的本地提交，再执行：

```sh
make -f scripts/build.mk release-verify
# 验收完成后，按仓库提交流程将同一提交推送到 main，并准备版本说明 Draft。
make -f scripts/build.mk release-check VERSION=v2.0.0-rc.50 IMAGE_PREFIX=ghcr.io/owner
make -f scripts/build.mk release VERSION=v2.0.0-rc.50 IMAGE_PREFIX=ghcr.io/owner
```

`release-verify` 可以在本地分支执行，不推送代码、Tag、镜像或 GitHub Release。
它在固定提交的独立快照中运行完整源码和浏览器检查；依赖目录由该快照独占。
直接运行 `release` 也会自动完成相同验收，无需预先重复执行两个检查命令。

成功记录和生产前端产物保存在 Git common directory 的 `release-validation/` 中，
不会进入源码或发布包。缓存绑定整个 Git 源码树、Node/npm/Go/Compose、浏览器文件、
目标平台及相关环境配置。源码、工具链或产物校验变化时重新验收；只有完整验收通过
且文件摘要匹配的静态产物才可交给 Web 镜像。相同源码在浏览器检查失败后，可复用
已经成功的源码检查；镜像或 GitHub 上传失败后重试，也会复用成功的验收及不可变镜像。
版本号、GitHub Draft 和远端镜像仍在每次发布时重新检查。

发布路径直接使用验收生成的前端产物，避免 Docker 再次编译三套页面。
普通 Docker 构建仍支持从源码构建前端。Control 的程序分别放入独立镜像层；
新镜像只推送内容标签，再由 Registry 添加版本标签，既有镜像继续按摘要复用。
只有 `v数字.数字.数字` 形式的规范正式版本可设为 GitHub Latest，RC 等使用 Pre-release。
脚本输出验收、镜像、附件和公开阶段的耗时，便于区分编译与网络等待。

Playwright 默认使用两个 worker；资源紧张时可使用 `CPAP_E2E_WORKERS=1`。
浏览器上下文和接口覆盖按用例隔离，预览服务仅提供只读 fixture。
Vitest 的 Ant Design 交互用例保持按文件串行，避免资源争用导致超时。
同一源码的并发验收由锁保护；若进程被强制终止，确认已退出后可移除错误信息中
标明的遗留锁目录。CI 继续只验证和打包，部署仍由目标环境操作入口执行。

## 使用中心查询性能

近一小时活跃用户查询先固定时间范围，再按账号和规范化邮箱去重，避免 SQLite 为了
账号分组而扫描全部历史索引。查询计划回归覆盖同时存在账号、用户和时间索引的场景；
该优化不创建索引、不迁移数据库，也不改变统计口径。

账号列表可展示最多两个缓存周期内的原生运行状态（默认最多 30 秒），并在第一个
15 秒周期过期后后台刷新。没有缓存、缓存超过上限或账号服务集合变化时，仍等待
新的完整观测。禁用、容器停止、OAuth 缺失和持久化额度状态继续在每次请求中检查；
账号切换和自动分配使用原来的状态读取，不使用扩展的展示缓存。

使用中心的个人数据缓存按登录用户和时间范围隔离。列表、个人摘要在 15 秒内复用，
模型明细在 30 秒内复用；未使用的数据最多保留 5 分钟，过期后后台更新。手动刷新
同时更新列表和已展开明细，并使其他时间范围缓存失效；账号切换同样使列表缓存失效。
API Key 仍按需读取，退出登录和会话失效继续清理缓存。
