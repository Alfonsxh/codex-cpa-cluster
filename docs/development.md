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

前端只代理当前页面需要的细粒度接口。默认把 Admin 请求转发到本机 Go Preview：

```sh
go run ./cmd/test-preview --address 127.0.0.1:8896 --root .
CPA_DEV_PROXY_TARGET=http://127.0.0.1:8896 npm --prefix frontend run dev
```

也可以把 `CPA_DEV_PROXY_TARGET` 指向已授权的 Test Admin。不要代理 Production 写接口。

## 常用门禁

```sh
make generate-api
make check-generated-api
make verify
npm --prefix frontend run test:e2e
```

`make verify` 包含：

1. Shell 语法、Go 格式与 OpenAPI 生成一致性。
2. `go vet`、Go 单元测试和竞态测试。
3. React 类型检查、Vitest 和生产构建。
4. 发布隐私扫描。
5. 正式 Compose 与隔离 Test Compose 校验。
6. 已移除运行时残留检查与 `git diff --check`。

## 隔离数据面

```sh
make test-build
make test-up
make test-smoke
make test-faults
make test-down
```

故障演练覆盖上游不可用、无效 Key、损坏鉴权快照、Edge 非法槽位、蓝绿切换和 SSE 排空。

## Writer 所有权

所有写进程必须同时持有 `runtime-writer` 与自己的 Worker Lease。Generation 变化后，旧进程写入返回 `ErrLeaseLost`。

```sh
go test -count=1 -run '^TestWriterLeaseGenerationTransferFencesStaleOwner$' ./internal/ownership
go test -count=1 -run '^TestGoWorkerLeaseGroupTransfersAllScopesAndRejectsDuplicate$' ./internal/ownership
```

不得绕过所有权直接修改 SQLite，也不得用本地 Test 通过代替真实 API Key 的 `/v1/responses` 验收。
