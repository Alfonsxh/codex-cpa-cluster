# 配置中心

## 范围

React 配置中心通过 Go Admin 的细粒度接口读取和修改组织设置，不再把全部系统状态装入一个大响应。当前页面入口是 `/admin/configuration`，契约以 [OpenAPI](../api/openapi.yaml) 和 `internal/admin` 为准。

## 页面分类与保存

配置中心保留六个同级分类，系统设置紧接品牌与身份：

| 分类 | 内容 |
|---|---|
| 品牌与身份 | 名称、Logo、公开地址、邮箱后缀、Key 前缀与客户端导出 |
| 系统设置 | 系统时区、Portal 登录有效期、管理密钥、初始密码与推理强度配色 |
| 请求与账号 | 上游代理、重试、会话保持、账号切换、供应端口、监听、镜像与日志 |
| 用量与额度 | 默认周额度、模型与推理倍率、采集、官方额度查询与用量维护 |
| 通知设置 | 企业微信 Webhook、发送计划与额度预警 |
| 数据与审计 | 安全归档、存储状态与审计记录 |

常用分区默认展开，次要分区可按需展开。搜索覆盖所有配置字段和独立操作；选中结果会切换分类、展开分区并定位到对应控件。旧的 `?group=...&key=...` 和 `?section=access|backups|storage|audit` 链接继续有效，字段定位优先于旧分类。

切换分类保留草稿。底部保存栏显示全部未保存项、涉及分类及生效范围；保存与撤销均作用于全部分类。只有改动的字段会提交，重建账号、重启采集器等操作保留影响确认与错误反馈。数字范围和生效方式仍来自服务端配置目录，Portal 有效期使用秒。

Logo、访问凭据、Webhook 和用量清零保留独立操作流程，不包含在底部的统一保存中。配色位于系统设置，模型与推理倍率位于用量与额度；两者使用原有配置键。六类分区由前端组织，不改变配置接口、数据库或已有配置值。

## 配置接口

| 接口 | 用途 | 安全行为 |
|---|---|---|
| `GET /admin/api/onboarding` | 汇总首次配置的真实完成状态 | 只返回状态和数量，不返回 Secret 或账号生命周期状态 |
| `PUT /admin/api/onboarding/preferences` | 保存可跳过配置项的处理状态 | CSRF；只允许已知配置项 |
| `GET /admin/api/settings/configuration` | 按组读取可编辑配置定义、当前值和生效方式 | 管理会话；秘密只返回是否已配置 |
| `POST /admin/api/settings/configuration` | 按键保存通用配置 | CSRF；类型、范围和枚举校验 |
| `GET/PUT /admin/api/settings/general` | 品牌、公开地址、邮箱域、Key 前缀和客户端导出默认值 | PUT 需要 CSRF 与 `confirm=save` |
| `GET /admin/api/settings/workspace` | 存储文件存在性、权限、账号备份摘要和脱敏审计 | 不读取或返回文件秘密内容 |
| `GET/PUT /admin/api/settings/notifications` | 通知调度与阈值 | Webhook 永不在读取响应中回显 |
| `POST /admin/api/settings/initial-password` | 设置之后新用户的初始密码 | 只返回配置状态，不回显密码 |
| `POST/DELETE /admin/api/settings/logo` | 上传或恢复品牌 Logo | 文件类型、大小与 SVG 安全校验 |
| `POST /admin/api/settings/management-key` | 轮换管理密钥 | 成功后当前管理会话立即失效 |

完整 API Key、管理密钥、密码和 Webhook 不进入 URL、React Query 缓存、Local Storage 或 Session Storage。一次性凭据只存在于当前 Mutation/Modal 内存，关闭后清理。

`/admin/setup` 只复用配置中心的安全写接口，不再跳转到 CPA、OAuth 或用户创建流程。引导完成状态以 Go Admin 的实时读取为准；浏览器只保存当前会话内的页面选择，不持久化完成状态或秘密。可跳过项目的状态写入控制面设置，因此换浏览器后仍保持一致。

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

用户额度的加权 Token 按“模型倍率 × 推理强度倍率”计算，只在 Collector 接收新事件时写入，不按当前配置重算历史事件。`gpt-6-astra` 的默认模型倍率为 `4`，当前已知的其他模型以及未匹配模型的默认倍率为 `1`；模型名优先取事件的 `model` 字段，仅当 `model` 为空时回退到 `alias`，匹配时忽略大小写和首尾空格。未来新增模型在没有专属配置前使用“其他 / 未匹配模型”倍率。

## 品牌和公开配置

`GET /site-config.json` 仅返回公开品牌、允许登录的企业邮箱后缀与客户端导出字段，不包含 Key 前缀、管理密钥、Secret digest 或其他私有设置。邮箱后缀需要在未登录的使用中心登录页展示，因此属于显式公开白名单。自定义 Logo 通过 `/branding/logo` 输出；SVG 会拒绝脚本、事件处理器、外部资源、实体、`foreignObject` 和危险 URL。

## 验证

```sh
go test ./internal/admin ./internal/branding ./internal/controlplane ./internal/notifications
npm --prefix frontend test
npm --prefix frontend run test:e2e -- --grep "configuration"
make -f scripts/build.mk verify
```

修改设置契约时必须同时更新 `api/openapi.yaml`、生成的 Go/TypeScript 类型、`internal/admin`、`frontend/src/api`、页面测试和 Go Preview fixture。
