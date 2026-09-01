# 配置中心

## 范围

React 配置中心通过 Go Admin 的细粒度接口读取和修改组织设置，不再把全部系统状态装入一个大响应。当前页面入口是 `/admin/configuration`，契约以 [OpenAPI](../api/openapi.yaml) 和 `internal/admin` 为准。

## 配置接口

| 接口 | 用途 | 安全行为 |
|---|---|---|
| `GET /admin/api/onboarding` | 汇总首次设置和推荐设置的真实完成状态 | 只返回状态、数量和阻塞原因，不返回 Secret |
| `PUT /admin/api/onboarding/preferences` | 保存“稍后继续”和推荐项跳过列表 | CSRF；只允许已知推荐项，必需项不能跳过 |
| `GET /admin/api/settings/configuration` | 按组读取可编辑配置定义、当前值和生效方式 | 管理会话；秘密只返回是否已配置 |
| `POST /admin/api/settings/configuration` | 按键保存通用配置 | CSRF；类型、范围和枚举校验 |
| `GET/PUT /admin/api/settings/general` | 品牌、公开地址、邮箱域、Key 前缀和客户端导出默认值 | PUT 需要 CSRF 与 `confirm=save` |
| `GET /admin/api/settings/workspace` | 存储文件存在性、权限、账号备份摘要和脱敏审计 | 不读取或返回文件秘密内容 |
| `GET/PUT /admin/api/settings/notifications` | 通知调度与阈值 | Webhook 永不在读取响应中回显 |
| `POST /admin/api/settings/initial-password` | 设置之后新用户的初始密码 | 只返回配置状态，不回显密码 |
| `POST/DELETE /admin/api/settings/logo` | 上传或恢复品牌 Logo | 文件类型、大小与 SVG 安全校验 |
| `POST /admin/api/settings/management-key` | 轮换管理密钥 | 成功后当前管理会话立即失效 |

完整 API Key、管理密钥、密码和 Webhook 不进入 URL、React Query 缓存、Local Storage 或 Session Storage。一次性凭据只存在于当前 Mutation/Modal 内存，关闭后清理。

`/admin/setup` 复用配置中心、账号管理和用户管理的既有写接口。引导完成状态以 Go Admin 的实时读取为准；浏览器只保存当前会话内的页面选择，不持久化完成状态或秘密。推荐项的跳过状态和“稍后继续”版本写入控制面设置，因此换浏览器后仍保持一致。

## 存储边界

| 存储 | 内容 | 规则 |
|---|---|---|
| `state/control-plane.sqlite3` | 配置值、账号/路由/Key 元数据、加密秘密和审计状态 | 唯一控制面事实来源 |
| `state/usage.sqlite3` | 用量、用户会话、额度策略和调整 | 与控制面库分离，避免高频事件争用 |
| `secrets/control-plane.key` | 控制面秘密主密钥 | 必须为普通文件、权限 `0600`、与数据库成对备份 |
| `configs/`、`state/gateway/` | 上游配置和 Go Gateway 运行快照 | 由 Go 服务生成，不是人工配置入口 |

Go Admin 只打开既有目标。正式部署必须先存在两份 SQLite 和匹配主密钥；发布脚本不会导入退役控制文件或创建新的生产数据。

## 生效模型

| 模式 | 行为 |
|---|---|
| `live` | 后续读取立即使用新值，不重建数据面 |
| `accounts` | 由账号生命周期流程校验并应用到相关上游账号 |
| `quota` | 下一次 Collector 快照后由 Gateway 执行 |
| `deployment` | 只由目标发布流程应用，不由普通页面刷新触发 |

账号自动切换只允许 `off` 和 `active`。用户周额度的系统默认、个人策略、追加和清零均保留独立审计语义；页面只在进入对应区域时读取破坏性操作影响范围。

## 品牌和公开配置

`GET /site-config.json` 仅返回公开品牌与客户端导出字段，不包含允许邮箱域、Key 前缀、管理密钥或 Secret digest。自定义 Logo 通过 `/branding/logo` 输出；SVG 会拒绝脚本、事件处理器、外部资源、实体、`foreignObject` 和危险 URL。

## 验证

```sh
go test ./internal/admin ./internal/branding ./internal/controlplane ./internal/notifications
npm --prefix frontend test
npm --prefix frontend run test:e2e -- --grep "configuration"
make verify
```

修改设置契约时必须同时更新 `api/openapi.yaml`、生成的 Go/TypeScript 类型、`internal/admin`、`frontend/src/api`、页面测试和 Go Preview fixture。
