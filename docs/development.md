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
