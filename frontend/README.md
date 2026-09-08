# React 前端

Admin 管理账号、用户和配置；Portal 提供入口；Usage 展示个人用量。Native 账号页需要管理身份，仅允许符合回环 URL 白名单的管理链接。

## 开发

安装依赖、后端代理和热更新见[开发指南](../docs/development.md#前端热更新)。在本目录执行：

```sh
npm ci
npm run typecheck
npm test
npm run build
```

## 页面与数据

- 使用 Ant Design 基础组件，React Hook Form/Zod 处理表单；复用已有导航、表格、弹窗和反馈。
- 页面仅读取当前视图所需接口，覆盖加载、空数据、错误、成功和提交失败状态。
- TanStack Query 管理请求生命周期；缓存不是业务事实或授权依据。默认缓存为零，使用中心等页面可按用户和时间范围显式缓存。
- `/overview/summary` 只返回汇总计数；用户额度单独读取，Gateway 生效以 Collector 快照为准。
- `/site-config.json` 只返回公开品牌、邮箱域和客户端导出字段；配置接口以 [OpenAPI](../api/openapi.yaml) 为准。

## 凭据与验证

管理密钥、CSRF Token、API Key 不进入 URL、Local Storage 或 Session Storage。新建、轮换及重置产生的完整凭据只保存在一次性 Mutation/Modal 中，关闭即清理。

配置页面交互见[配置中心](../docs/configuration-center.md)。布局与行为由 [Playwright 矩阵](e2e/visual.spec.ts)覆盖桌面、窄屏、手机、主题与异常状态；确认变化符合要求后再更新基准图。
