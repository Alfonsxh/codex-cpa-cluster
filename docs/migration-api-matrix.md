# v1 到 Go v2 API 迁移矩阵

本文件由 `python3 scripts/migration-route-matrix.py --write` 从当前 Python、Gin、OpenAPI 与
`api/migration-route-map.json` 生成。不要手工维护表格内容。

## 当前结论

| 原路径已实现 | 能力拆分/合并 | 明确移除 | 尚未迁移 | Go 已注册 | OpenAPI 已记录 |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 57 | 8 | 7 | 0 | 78 | 77 |

`尚未迁移` 必须归零后，才能把“所有接口都已迁移”作为完成结论。`明确移除` 只允许记录已有
产品决策；不能用来掩盖实现缺口。

## v1 路由逐项追踪

| v1 方法与路径 | 状态 | Go v2 方法与路径 | 说明 |
| --- | --- | --- | --- |
| `GET /admin/api/accounts` | 原路径已实现 | `GET /admin/api/accounts` | — |
| `POST /admin/api/accounts` | 原路径已实现 | `POST /admin/api/accounts` | — |
| `POST /admin/api/accounts/clear-auth` | 原路径已实现 | `POST /admin/api/accounts/clear-auth` | — |
| `POST /admin/api/accounts/delete` | 原路径已实现 | `POST /admin/api/accounts/delete` | — |
| `POST /admin/api/accounts/policy` | 能力已拆分/合并 | `POST /admin/api/accounts/update` | 账号启用、默认账号和故障转移目标合并到账号更新事务。 |
| `POST /admin/api/accounts/rebalance` | 原路径已实现 | `POST /admin/api/accounts/rebalance` | — |
| `POST /admin/api/accounts/reset-quota` | 原路径已实现 | `POST /admin/api/accounts/reset-quota` | — |
| `POST /admin/api/accounts/update` | 原路径已实现 | `POST /admin/api/accounts/update` | — |
| `GET /admin/api/accounts/usage-breakdown` | 原路径已实现 | `GET /admin/api/accounts/usage-breakdown` | — |
| `GET /admin/api/images/cliproxy` | 原路径已实现 | `GET /admin/api/images/cliproxy` | — |
| `GET /admin/api/jobs` | 原路径已实现 | `GET /admin/api/jobs` | — |
| `POST /admin/api/jobs/cancel` | 原路径已实现 | `POST /admin/api/jobs/cancel` | — |
| `POST /admin/api/keys/create` | 能力已拆分/合并 | `POST /admin/api/users` | 新用户、统一 API Key 和初始登录凭据改为一个补偿式生命周期事务。 |
| `POST /admin/api/keys/revoke` | 能力已拆分/合并 | `POST /admin/api/users/revoke` | 按旧 label 停用改为按用户停用全部当前统一 Key。 |
| `POST /admin/api/keys/rotate` | 原路径已实现 | `POST /admin/api/keys/rotate` | — |
| `GET /admin/api/logs` | 原路径已实现 | `GET /admin/api/logs` | — |
| `GET /admin/api/native-accounts` | 原路径已实现 | `GET /admin/api/native-accounts` | — |
| `POST /admin/api/operations` | 原路径已实现 | `POST /admin/api/operations` | — |
| `GET /admin/api/operations/impact` | 原路径已实现 | `GET /admin/api/operations/impact` | — |
| `GET /admin/api/overview` | 能力已拆分/合并 | `GET /admin/api/overview/summary`<br>`GET /admin/api/accounts`<br>`GET /admin/api/runtime/services`<br>`GET /admin/api/runtime/jobs` | 旧聚合接口拆成按页面需要加载的摘要、账号和运行时接口。 |
| `GET /admin/api/overview/catalog` | 能力已拆分/合并 | `GET /admin/api/accounts`<br>`GET /admin/api/users`<br>`GET /admin/api/teams` | 账号、用户和团队目录独立分页或按需加载。 |
| `GET /admin/api/overview/usage` | 原路径已实现 | `GET /admin/api/overview/usage` | — |
| `GET /admin/api/release` | 原路径已实现 | `GET /admin/api/release` | — |
| `DELETE /admin/api/session` | 原路径已实现 | `DELETE /admin/api/session` | — |
| `GET /admin/api/session` | 原路径已实现 | `GET /admin/api/session` | — |
| `POST /admin/api/session` | 原路径已实现 | `POST /admin/api/session` | — |
| `GET /admin/api/settings` | 能力已拆分/合并 | `GET /admin/api/settings/general`<br>`GET /admin/api/settings/configuration`<br>`GET /admin/api/settings/notifications` | 旧全量设置响应拆成通用设置、完整配置目录和通知设置；页面进入对应工作区时才请求。 |
| `POST /admin/api/settings/configuration` | 原路径已实现 | `POST /admin/api/settings/configuration` | — |
| `POST /admin/api/settings/initial-password` | 原路径已实现 | `POST /admin/api/settings/initial-password` | — |
| `DELETE /admin/api/settings/logo` | 原路径已实现 | `DELETE /admin/api/settings/logo` | — |
| `POST /admin/api/settings/logo` | 原路径已实现 | `POST /admin/api/settings/logo` | — |
| `POST /admin/api/settings/management-key` | 原路径已实现 | `POST /admin/api/settings/management-key` | — |
| `POST /admin/api/settings/notification-webhook` | 原路径已实现 | `POST /admin/api/settings/notification-webhook` | — |
| `POST /admin/api/settings/notification-webhook/clear` | 原路径已实现 | `POST /admin/api/settings/notification-webhook/clear` | — |
| `DELETE /admin/api/tags` | 明确移除 | — | 产品确认不提供标签管理；仅保留旧表用于数据兼容。 |
| `GET /admin/api/tags` | 明确移除 | — | 产品确认不提供标签管理；仅保留旧表用于数据兼容。 |
| `POST /admin/api/tags` | 明确移除 | — | 产品确认不提供标签管理；仅保留旧表用于数据兼容。 |
| `PUT /admin/api/tags` | 明确移除 | — | 产品确认不提供标签管理；仅保留旧表用于数据兼容。 |
| `DELETE /admin/api/teams` | 原路径已实现 | `DELETE /admin/api/teams` | — |
| `GET /admin/api/teams` | 原路径已实现 | `GET /admin/api/teams` | — |
| `POST /admin/api/teams` | 原路径已实现 | `POST /admin/api/teams` | — |
| `PUT /admin/api/teams` | 原路径已实现 | `PUT /admin/api/teams` | — |
| `GET /admin/api/teams/usage` | 原路径已实现 | `GET /admin/api/teams/usage` | — |
| `GET /admin/api/teams/usage-breakdown` | 原路径已实现 | `GET /admin/api/teams/usage-breakdown` | — |
| `GET /admin/api/users` | 原路径已实现 | `GET /admin/api/users` | — |
| `POST /admin/api/users` | 原路径已实现 | `POST /admin/api/users` | — |
| `POST /admin/api/users/delete` | 原路径已实现 | `POST /admin/api/users/delete` | — |
| `GET /admin/api/users/detail` | 能力已拆分/合并 | `GET /admin/api/users`<br>`GET /admin/api/users/quota`<br>`GET /admin/api/users/usage-breakdown` | 用户列表、周额度和用量明细在打开对应页面或抽屉时独立请求。 |
| `DELETE /admin/api/users/quota` | 原路径已实现 | `DELETE /admin/api/users/quota` | — |
| `GET /admin/api/users/quota` | 原路径已实现 | `GET /admin/api/users/quota` | — |
| `PUT /admin/api/users/quota` | 原路径已实现 | `PUT /admin/api/users/quota` | — |
| `POST /admin/api/users/quota-actions` | 原路径已实现 | `POST /admin/api/users/quota-actions` | — |
| `POST /admin/api/users/reset-password` | 原路径已实现 | `POST /admin/api/users/reset-password` | — |
| `POST /admin/api/users/revoke` | 原路径已实现 | `POST /admin/api/users/revoke` | — |
| `PUT /admin/api/users/tags` | 明确移除 | — | 产品确认不提供用户标签操作。 |
| `POST /admin/api/users/tags/batch` | 明确移除 | — | 产品确认不提供批量标签操作。 |
| `PUT /admin/api/users/team` | 原路径已实现 | `PUT /admin/api/users/team` | — |
| `POST /admin/api/users/team/batch` | 原路径已实现 | `POST /admin/api/users/team/batch` | — |
| `GET /admin/api/users/usage-breakdown` | 原路径已实现 | `GET /admin/api/users/usage-breakdown` | — |
| `GET /branding/logo` | 原路径已实现 | `GET /branding/logo` | — |
| `POST /my-keys/api` | 明确移除 | — | 旧邮箱查 Key 接口在 v1 已返回 410；v2 仅允许使用中心会话读取当前用户 Key。 |
| `GET /site-config.json` | 原路径已实现 | `GET /site-config.json` | — |
| `GET /usage/api` | 原路径已实现 | `GET /usage/api` | — |
| `GET /usage/limits` | 原路径已实现 | `GET /usage/limits` | — |
| `GET /usage/me` | 能力已拆分/合并 | `GET /usage/me/profile`<br>`GET /usage/me/accounts` | 个人身份、Key 与账号用量拆成独立接口，避免登录后一次加载全部数据。 |
| `PUT /usage/me/group` | 原路径已实现 | `PUT /usage/me/group` | — |
| `POST /usage/me/key/rotate` | 原路径已实现 | `POST /usage/me/key/rotate` | — |
| `PUT /usage/me/password` | 原路径已实现 | `PUT /usage/me/password` | — |
| `GET /usage/me/route` | 原路径已实现 | `GET /usage/me/route` | — |
| `GET /usage/me/usage-breakdown` | 原路径已实现 | `GET /usage/me/usage-breakdown` | — |
| `DELETE /usage/session` | 原路径已实现 | `DELETE /usage/session` | — |
| `POST /usage/session` | 原路径已实现 | `POST /usage/session` | — |

## Go 已注册但 OpenAPI 未记录

- 无

## 验证

```bash
python3 scripts/migration-route-matrix.py --check
python3 scripts/migration-route-matrix.py --require-complete
```

第一条检查源码、映射与本文同步；第二条额外要求 API 缺口和 OpenAPI 漏项都归零。
