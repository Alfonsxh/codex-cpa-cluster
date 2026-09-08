# Telegram 正式版发布通知

本地发布入口在 GitHub Release 公开后调用 `scripts/telegram-release.mjs`，向指定群或频道发送
发布摘要。无需常驻进程，也不依赖 GitHub Actions。它与配置中心的企业微信用量报告独立。
普通代码 push、单独推送 Tag、CI、Draft、RC、alpha、beta 和带构建后缀的版本不会发送通知。

## 发布者配置

配置由发布工作站保存，必须在 Git 工作区之外。默认目录为
`~/.config/codex-cpa-pool/telegram-release/`；支持 `XDG_CONFIG_HOME`，也可通过
`CPAP_TELEGRAM_CONFIG` 指定 `config.json` 的绝对路径。目录权限为 `0700`，配置和 Token 文件为
`0600`，均由当前用户持有，不允许符号链接。配置、Token 和消息回执不能加入 Git、发布附件或日志。

同目录的 `bot-token` 只包含 BotFather 提供的 Token。`config.json` 的字段如下，示例值须替换：

```json
{
  "enabled": true,
  "repository": "owner/repository",
  "repository_id": 123456,
  "chat_id": "-1001234567890",
  "bot_username": "YourReleaseBot",
  "min_version": "v2.0.2"
}
```

`repository_id` 从 `gh api repos/owner/repository --jq .id` 获取。`chat_id` 使用 Telegram
`getChat` 返回的稳定数字 ID，不使用可被改名或转让的群用户名作为发送地址。先核对群名称、类型
及 Bot 身份；公开群通常允许普通 Bot 成员发言，频道则需要管理员的发布权限。

`min_version` 是首次启用版本，阻止默认补发历史 Release。关闭 `enabled` 或不创建配置文件时，
发布流程继续，但明确报告通知未启用。缺失凭据、身份不符或权限不足会使通知预检失败。
需要网络代理时，可在私有配置中添加 `proxy_url`，使用不含账号密码的 HTTP/HTTPS 代理地址；
未指定时沿用 curl 的标准代理环境变量。

## 内容与发布流程

Agent 审核从上一个正式版到本次提交的变更，为准确的版本和提交创建 GitHub Draft。
发布说明必须包含唯一且非空的 `## 社群摘要` 和 `## 升级提示`，另外可以包含更新详情、验证和下载。
群通知只读取前两段，不自动生成新的功能声明；过长的通知会在发布预检阶段被拒绝。

```markdown
## 社群摘要
- **账号管理**：一句话介绍用户可感知的功能。

- **通知服务**：一句话介绍修复或体验改进。

- **安装升级**：一句话介绍升级变化。

## 升级提示
在原运维目录运行：
`./run.sh`

保留必要的兼容性与数据说明，分成短句。
```

群消息使用加粗版本标题、“本次更新”和“升级提示”分区，并附“发布详情”和“安装升级”按钮，
链接固定到本版本。摘要推荐 3–5 个短条目，用粗体标出功能名称，避免重复版本号和开场介绍。
升级命令单独一行，必要的数据保留和行为变化用短句说明。

发送使用 Telegram HTML 模式，仅将 Markdown 粗体和行内代码转换成受控的 `<b>` / `<code>`，
列表显示为圆点。其他 HTML 与特殊字符均转义为普通文字；不接受发布说明直接注入 HTML。
普通旧版摘要仍可展示，但短条目更适合手机阅读。

```sh
make -f scripts/build.mk release-check VERSION=v2.0.2 IMAGE_PREFIX=ghcr.io/owner
make -f scripts/build.mk release VERSION=v2.0.2 IMAGE_PREFIX=ghcr.io/owner
```

预检会核对 Bot 身份、目标权限和 Draft 说明。实际通知要求版本严格符合 `v数字.数字.数字`
（不允许前导零），Release 已公开、非 Draft、非 Prerelease，且仓库 ID 匹配并公开。
程序下载必要附件，验证 SHA-256、归档、安装脚本、发布描述、部署环境和 Tag 提交的一致性，
并读取四个组件远端镜像的三个身份标签。任一检查失败都不会发送消息。

## 预览、回执与重试

以下操作只面向已有公开 Release，不重新构建或发布版本：

```sh
make -f scripts/build.mk release-notify VERSION=v2.0.2 NOTIFY_ACTION=preview
make -f scripts/build.mk release-notify VERSION=v2.0.2 NOTIFY_ACTION=status
make -f scripts/build.mk release-notify VERSION=v2.0.2
```

`preview` 核对发布产物并输出消息内容，`status` 只读取本地回执。默认操作 `send` 在验证后发送。
回执位于配置目录下的 `deliveries/`，按仓库 ID、版本、目标群 ID 唯一记录，同时校验 Release ID
和 Bot ID，保存内容摘要、尝试次数、状态及成功消息 ID。并发操作使用目录锁；成功的重复操作
返回已有回执，不再次发送。历史回执应随发布工作站配置一并保留。

Telegram 明确返回限流时，按 `retry_after` 等待，最多尝试三次；单次等待超过 60 秒时留待以后
重试。明确拒绝会记为 `failed`，修复后可再次执行 `send`。超时、无效响应或进程中断可能发生在
Telegram 已接收消息之后，因此 `unknown` 和 `pending` 都禁止自动重发。先核对目标群消息和
回执；不要为重试直接删除回执。遗留锁中的 `owner.json` 提供进程信息，确认进程已退出后才可
清理对应锁，清理锁不会解除未知回执的保护。

已有成功通知需要纠正内容时，先审核并更新 GitHub Release 说明，再显式执行：

```sh
make -f scripts/build.mk release-notify VERSION=v2.0.2 NOTIFY_ACTION=edit
```

这会编辑原消息，不新增公告。通知失败不会回滚 GitHub Release；发布命令会明确报告“Release
已发布但通知未完成”并返回失败状态，此时应处理通知回执，而不是重建同一版本。

离线回归：`node --test scripts/telegram-release.test.mjs` 与
`sh scripts/test-local-release.sh`。本地完整发布验收仍由 `release-verify` 和 `release` 执行；
生产节点安装升级是另一项操作。
