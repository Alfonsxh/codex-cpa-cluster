# React Web

Admin、Portal 与使用中心统一使用 React 组件层。页面、交互和安全状态由
[`e2e/visual.spec.ts`](e2e/visual.spec.ts) 的桌面、窄屏、移动端、明暗主题及异常状态矩阵约束；
调整基准图前必须先确认行为和布局变化符合产品验收要求。

## Product job

让用户从框架化 Portal 快速进入 Admin 或 Usage；让已登录管理员从 Native 页面安全查看本机业务 CPA；让管理页面在不预取无关账号、用量和日志数据的前提下完成控制面工作。

## Five-anchor requirement card

- Layout：Portal 为品牌头部与双入口卡片；Native 为返回导航、账号网格和添加账号卡；Admin 桌面端为窄侧栏、上下文标题栏和单一内容区，移动端不复制第二套页面。
- Components：Ant Design Card、Button、Badge、Skeleton、Empty、Result、导航、表格、Modal 与反馈；React Hook Form/Zod 负责领域表单。
- Data：Portal 只读取公开品牌配置；Native 账号只有 `id`、`group_enabled` 和仅回环 Host 可选的完整 `management_url`；Admin 使用细粒度目录、用量和设置接口；新版不提供标签管理入口。
- States：品牌降级、加载、空账号、401、请求失败、重试、会话检查、表单校验和 Mutation 成败。
- Interactions：Portal 进入 Admin/Usage；Native 401 引导登录、失败可重试，账号卡只有通过回环 URL 白名单才可点击；Admin 页面只加载当前工作所需 API。

## Data rules

- TanStack Query 只管理请求生命周期，不是业务正确性来源；默认 `staleTime=0`、`gcTime=0`。
- 禁止把管理密钥、CSRF Token 或 API Key 写入 Local Storage、Session Storage 或 URL。
- 新建、轮换和密码重置的完整凭据只进入 `gcTime=0` 的 Mutation 与不可意外关闭的一次性 Modal；关闭后立即清理组件状态。
- 页面只请求当前视图需要的 API；不要用一个 Overview 响应承载账号、用户、用量、任务和日志全集。
- `/overview/summary` 只从一个 SQLite 只读事务返回计数，不查询或返回账号邮箱、用户邮箱、Key、Secret digest、Docker、OAuth、Gateway 或用量事件。
- `/settings/general` 只允许实时生效的品牌与身份字段，采用 SQLite 按键更新保留通知等无关设置；代理、配额和部署配置必须使用各自的专用流程。
- `/site-config.json` 只返回公开品牌与客户端导出字段，明确排除邮箱域名和 Key Prefix；`/admin/api/native-accounts` 需要 Admin 身份且不返回独立端口或账号邮箱。
- `/users/quota` 只读取或修改一个用户的当前周额度策略，不并入分页用户响应；其 Query 与 Mutation
  均使用 `gcTime=0`，Gateway 生效状态以唯一 Collector 后续发布的额度快照为准。
- Ant Design 负责通用表格、Modal、状态反馈和导航；不要在项目中再造同类基础组件。
- 新增页面必须覆盖 loading、empty、error、success 和 mutation failure，不只实现正常数据态。

## Commands

```bash
npm ci --registry=https://registry.npmmirror.com
npm run typecheck
npm test
npm run build
```
