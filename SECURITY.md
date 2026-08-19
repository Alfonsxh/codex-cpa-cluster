# 安全策略

## 报告漏洞

请不要通过公开 Issue 报告安全漏洞，也不要附带真实管理密钥、用户 Key、OAuth 文件、Webhook、邮箱、私有域名或生产日志。

优先使用 GitHub 的私有漏洞报告入口：

https://github.com/Alfonsxh/codex-cpa-cluster/security/advisories/new

报告中请包含：

- 受影响版本和组件。
- 可复现的最小步骤。
- 预期影响和已知缓解方式。
- 已脱敏的日志或请求样例。

维护者确认问题前，请不要公开利用细节。

## 支持范围

安全修复默认发布到最新稳定版本。涉及不可逆数据库迁移时，Release Notes 会说明最低来源版本和备份要求。

## 部署者责任

- 只通过 HTTPS 暴露公开入口。
- 保持 Gateway、Management 和业务 CPA 的宿主机端口绑定回环地址。
- 将 `secrets/control-plane.key`、管理密钥和 OAuth 文件限制为最小读取权限。
- 定期备份并验证控制面数据库与主密钥可以成对恢复。
- 仅从可信 Registry 拉取 digest 固定的应用镜像。
- Admin 当前拥有 Docker Socket 访问能力，应放置在可信主机并严格保护管理入口。
