# Changelog

本项目从 `v1.1.0` 开始维护面向使用者的变更记录。完整提交历史可在 GitHub 查看。

## Unreleased

### Added

- 个人使用中心新增 7/30/90 天每日用量趋势，支持总用量与“模型 + 推理强度”组合维度，并沿用现有 SQLite 统计口径。
- 增加仅在全部账号无安全迁移目标时可用、需要精确确认的单账号代理投影恢复接口。

### Changed

- 使用中心趋势按范围与维度独立请求、取消和缓存；图表使用 ECharts，Tooltip 保持单列且最多显示十项。
- 移动端使用中心恢复页面级滚动，趋势收起后立即释放账号明细空间。
- 目标机统一入口由 `deploy.sh` 更名为 `run.sh`；README 提前提供一条可同时用于安装和升级的 `curl` 命令，发布附件、自更新、测试与内部 `make run` 入口同步更名。
- `run.sh` 支持 `curl | sudo sh` 管道启动：内部原子安装脚本并重新连接交互终端，README 不再要求用户管理部署目录。
- README 重构为 GitHub 项目首页，增加主题自适应品牌、状态徽章、产品预览、快速开始、核心能力、精简架构和文档导航。
- `run.sh` 新增 `--tag` Release 选择入口：无参数时列出高于当前部署版本的正式 Releases 并支持交互选择，指定 Tag 时直接选择对应 Release；保留 `--version` 兼容参数。
- CPAC 版本提醒改为只比较本地部署版本与 GitHub Latest Release Tag，不再拉取或发布 GHCR release metadata 镜像。

## v1.1.4 - 2026-08-19

### Added

- 面向使用者的快速开始、架构、部署、升级、备份和故障排查文档。
- GitHub 社区协作与安全报告文件。
- 不依赖 GitHub Actions Runner 的本地验证与发布入口。
- SQLite 中的应用发布 `pending`/`applied` 状态与 CPA 候选/已应用镜像身份。
- 权限为 `0600`、由控制面原子生成的 `state/compose.env` Compose 投影。

### Changed

- README 从内部运维手册调整为产品入口，复杂运行细节迁移到 `docs/`。
- GitHub Actions 关闭 Push 和 Pull Request 自动触发，仅保留手动入口。
- 管理页手动刷新会等待新的后端聚合结果，不再继续显示旧缓存状态。
- 应用升级逐个保留业务 CPA 的发布前不可变镜像身份，完整验收后才推进当前版本。
- CPA 镜像界面同时展示语义版本与短 SHA，移动标签只作为显式拉取通道。

## v1.0.14 - 2026-08-18

- 作为 GitHub 原生发布流程改造前的兼容基线。
- 此前 `v1.0.x` 版本的详细变化保留在 Git 提交历史中。
