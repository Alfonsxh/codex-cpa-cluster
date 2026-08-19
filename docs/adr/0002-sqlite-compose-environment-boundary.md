# ADR 0002: SQLite 与 Compose 环境边界

- Status: Accepted

## Context

旧版 `.env` 同时保存宿主机启动项、人工配置、应用发布版本、CPA 更新通道和当前镜像。
拉取移动的 `:latest` 后，标签可能已经指向候选镜像，而 `.env` 无法表达“已拉取但尚未
验收”；应用发布也可能因此意外重建业务 CPA 到新镜像。

## Decision

- `.env` 只保存 `DEPLOY_ROOT`、`INSTANCE_NAME`、`COMPOSE_PROJECT_NAME` 和
  `DOCKER_NETWORK_NAME`。
- `state/control-plane.sqlite3` 保存期望配置、CPA 候选/已应用镜像身份，以及应用
  `pending`/`applied` 发布状态。
- `state/compose.env` 是权限为 `0600` 的原子生成 Compose 投影，不接受人工维护。
- CPA 的移动标签只作为拉取通道；验收后使用仓库 digest 或本机 image ID 应用。
- 应用发布若需对账业务 CPA，逐服务沿用其发布前 image ID；完整验收前不推进应用的
  `applied` 版本。

## Consequences

- 版本检查和 CPA 镜像界面可从 SQLite 同时展示语义版本与 SHA。
- 更新 `:latest` 不再等同于应用新 CPA 版本，应用发布也不再隐式推进 CPA。
- Compose 命令必须同时读取 `.env` 与 `state/compose.env`；缺少生成投影时应先运行
  `codex-cpa render`。
- 旧混合 `.env` 首次迁移会生成 `state/legacy.env` 备份，随后只保留启动项。

## Alternatives considered

- 继续原地修补 `.env`：不能事务化表达候选、已应用和发布中状态。
- 把宿主机路径也放进 SQLite：容器在定位数据库之前就需要部署根路径，形成启动环依赖。
- 直接运行 `:latest`：无法可靠回滚，也会把“拉取”和“应用”合并成一个不可审计动作。

## Maintenance notes

涉及配置定义、部署记录、CPA 镜像更新或 Compose 插值时，同步更新
`scripts/cliproxy.py`、`scripts/deploy-release.sh`、`compose.env.example`、配置中心文档和
相关迁移/发布测试。
