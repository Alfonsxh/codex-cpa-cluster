# 贡献指南

感谢你改进 Codex CPA Cluster。提交前请先搜索已有 Issue，较大的架构变化建议先创建讨论 Issue。

## 开发流程

1. 从 `main` 创建短生命周期分支。
2. 只修改解决当前问题所需的最小范围。
3. 为行为变化补充测试和必要注释。
4. 运行 `make verify`。
5. 提交 Pull Request，说明问题、方案、验证和部署影响。

`make verify` 需要 Node.js 22、Go（版本以 `go.mod` 为准）、可用的 Docker daemon，以及
Docker Compose。首次验证前在 `frontend/` 和 `tools/openapi/` 执行 `npm ci`。Go 与 React
代码始终纳入同一质量门禁。

## Pull Request 要求

- 不包含真实域名、邮箱、Key、OAuth、Webhook、私网地址或生产路径。
- 不提交 `state/`、`secrets/`、`auth/`、`logs/`、`configs/` 等运行态目录。
- 数据库或配置格式变化需要说明迁移与回滚策略。
- Gateway、Edge 或公开路径变化需要说明数据面影响。
- UI 变化应提供截图或清晰的人工验证步骤。
- 保持现有用户无损升级，除非 Release Notes 明确声明不兼容边界。

## 提交信息

建议采用简洁的 Conventional Commits 风格：

```text
feat(admin): add account filter
fix(gateway): reject unknown route
docs(deploy): clarify backup order
```

## 安全问题

安全漏洞不要提交公开 Issue，请遵循 [SECURITY.md](SECURITY.md)。
