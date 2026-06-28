# 命令速查

ElBot 的 slash 命令由 Agent Core 统一处理，CLI、QQ、后续平台共享同一套语义。默认命令前缀是 `/`，可在 `app.toml` 的 `[commands]` 中配置。

## 帮助

| 命令 | 作用 |
| --- | --- |
| `/help` | 查看可用命令列表。 |
| `/help <command>` | 查看某个命令的详细帮助。 |

示例：

```text
/help
/help model
/help log
```

## 模型

| 命令 | 作用 |
| --- | --- |
| `/models` | 查看模型列表。 |
| `/models --fresh` 或 `/models --refresh` | 强制刷新模型列表缓存。 |
| `/model <编号或名称>` | 切换当前 Session 模式使用的模型。 |
| `/model --chat <模型>` | 切换 chat 模式模型。 |
| `/model --work <模型>` | 切换 work 模式模型。 |
| `/model --elwisp1 <模型>` | 切换 Elnis elwisp1 模型槽位。 |
| `/model --elwisp2 <模型>` | 切换 Elnis elwisp2 模型槽位。 |
| `/model --elwisp3 <模型>` | 切换 Elnis elwisp3 模型槽位。 |
| `/model --compact <模型>` | 切换上下文压缩模型。 |
| `/model --naming <模型>` | 切换 Session 自动命名模型。 |
| `/checkmodel [关键词]` | 查看或搜索模型。 |

模型参数可以是列表编号、模型名或 `provider/model`。

示例：

```text
/models
/models --fresh
/models --refresh
/model 2
/model --work deepseek/deepseek-chat
/model --chat openai/gpt-4o-mini
/model --elwisp2 openai/gpt-4.1
/checkmodel deepseek
```

## 模式

| 命令 | 作用 |
| --- | --- |
| `/chat` | 切换到 chat 模式，或创建新的 chat Session。 |
| `/work` | 切换到 work 模式，或创建新的 work Session。 |

说明：

- `chat` 模式不注入工具，适合闲聊、陪伴和低成本问答。
- `work` 模式启用工具发现和工具调用。
- 已有历史的 work Session 不能直接切到 chat；需要先 `/new`，再 `/chat`。

## Session

| 命令 | 作用 |
| --- | --- |
| `/new` | 创建并切换到新 Session。 |
| `/status` | 查看当前 Session 状态。 |
| `/sessions [关键词]` | 列出或搜索可见 Session。 |
| `/resume [编号或session_id]` | 恢复历史 Session。 |
| `/archives [页码] [关键词]` | 查看已归档 Session。 |
| `/archive [编号或session_id] --confirm` | 归档 Session，默认当前 Session。 |
| `/unarchive [编号或session_id]` | 取消归档 Session，默认当前 Session。 |
| `/pin [编号或session_id]` | 置顶 Session，默认当前 Session。 |
| `/unpin [编号或session_id]` | 取消置顶 Session，默认当前 Session。 |
| `/rename [编号或session_id|当前标题] <新标题>` | 重命名当前或指定 Session。 |
| `/delete <编号或session_id> --confirm` | 永久删除 Session。 |
| `/clean --confirm` | 删除过期且未归档、未置顶的 Session。 |

示例：

```text
/new
/status
/sessions
/sessions project
/resume 1
/rename 我的新会话标题
/archive --confirm
/delete 2 --confirm
```

说明：

- `/sessions` 展示的编号可被 `/resume`、`/archive`、`/pin`、`/delete` 等命令复用。
- CLI 作为本地高权限入口，可以跨平台查看 Session；非 CLI 平台默认只查看当前平台和作用域下的 Session。
- 删除是永久操作，需要显式 `--confirm`。

## Fork

| 命令 | 作用 |
| --- | --- |
| `/messages [页码]` | 列出当前 Session 中可用于 fork 的 assistant message ID。 |
| `/fork <message_id>` | 从指定 assistant 消息创建分支 Session。 |

示例：

```text
/messages
/fork msg_xxx
```

Fork 会保留原会话，并从指定 assistant 消息位置创建新的上下文分支。

## 请求管理

| 命令 | 作用 |
| --- | --- |
| `/requests` | 查看当前进程中的 active request。 |
| `/stop [request_id]` | 停止指定请求；不传参数时停止当前 Session 的请求。 |
| `/stopall` | 停止当前进程中的所有 active request。 |

示例：

```text
/requests
/stop
/stop req_xxx
/stopall
```

## 上下文压缩

| 命令 | 作用 |
| --- | --- |
| `/compact` | 手动压缩当前 Session 上下文。 |

说明：

- 自动压缩由 `[context] compact_enabled` 和 `compact_trigger_ratio` 控制。
- 压缩只影响发送给 LLM 的上下文视图，不删除原始消息历史。

## 工具与 Skill

| 命令 | 作用 |
| --- | --- |
| `/tools` | 列出已注册工具和外置 Skill。 |
| `/tools reload` | 重新扫描并加载 Skill。 |
| `/tools remove <name> --confirm` | 删除外置 Skill 及其目录。 |
| `/tools uninstall <name> --confirm` | 等同于 remove。 |

示例：

```text
/tools
/tools reload
/tools remove my_skill --confirm
```

LLM 在 work 模式下可以通过 `discover_tool` 按需发现工具详情。聊天中也可以用 `@tool:<name-or-tag>` 预载工具，或用 `@skill:<name>` 把 Skill 文档加入本轮消息并预载对应运行 wrapper。

内置工具 `elwisp_creator` 可帮助超级管理员生成 Elwisp 配置、Elvena 事件模板、监听器脚手架和测试命令；它本身无参数、无副作用，实际写文件或运行命令仍会继续使用文件工具和 shell 工具并走对应风险确认。

## Hook

| 命令 | 作用 |
| --- | --- |
| `/hooks` | 列出所有已注册 Hook。 |
| `/hooks <name>` | 查看某个 Hook 的详细配置。 |
| `/hooks reload` | 清空并重新注册所有 Hook（规则 Hook、常驻记忆、内置 Hook）。 |

示例：

```text
/hooks
/hooks rules.greet
/hooks reload
```

说明：

- Hook 包括规则 Hook（`plugins/hooks.toml`）、常驻记忆 Hook 和内置 Cron Hook。
- `reload` 会重新读取 `hooks.toml` 并重建所有 Hook 注册，修改配置后无需重启即可生效。
- `/hooks` 为超级管理员命令。

## 日志和审计

| 命令 | 作用 |
| --- | --- |
| `/log [options]` | 查询运行日志。 |
| `/audit [options]` | 查询审计日志。 |
| `/elwisp [name] [options]` | 查询 Elnis/Elwisp 事件日志。 |

常用选项：

| 选项 | 作用 |
| --- | --- |
| `-n, --limit <n>` | 返回条数，默认 5。 |
| `--days <n>` | 读取最近 n 天日志，默认 1。 |
| `--level <level>` | 最低等级：`debug`、`info`、`warn`、`error`。 |
| `-d, -i, -w, -e` | 等级快捷方式。 |
| `--since <time>` | 只看某时间之后，例如 `2h`、`30m`、`2026-06-03`。 |
| `--until <time>` | 只看某时间之前。 |
| `--msg <text>` | 按 msg 字段过滤。 |
| `--contains <text>` | 按文本、参数、结果或 raw 内容过滤。 |

`/audit` 额外支持：

| 选项 | 作用 |
| --- | --- |
| `--event <name>` | 按审计事件过滤，例如 `tool_call`、`llm_usage`、`permission_denied`。 |
| `--risk <level>` | 按风险等级过滤。 |
| `--actor <id>` | 按 actor ID 过滤。 |
| `--session <id>` | 按 Session ID 过滤。 |
| `--tool <name>` | 按工具名过滤。 |

`/elwisp` 查询 `elnis-YYYY-MM-DD.log`，额外支持：

| 选项 | 作用 |
| --- | --- |
| `[name]` | 按 Elwisp 名称过滤，等同于 `--name`。 |
| `--name <name>` 或 `--elwisp <name>` | 按 Elwisp 名称过滤。 |
| `--source <source>` | 按事件来源过滤。 |
| `--id <id>` 或 `--source-id <id>` | 按外部事件 ID 过滤。 |
| `--mode <record|direct|llm>` | 按事件模式过滤。 |
| `--event-key <key>` | 按 Elnis event key 过滤。 |
| `--event-id <id>` | 按内部 Elnis event ID 过滤。 |
| `--token <name>` | 按 token name 过滤，不包含 token 原文。 |

示例：

```text
/log
/log -w -n 10
/log --msg startup --days 3
/audit --event tool_call --risk high -n 10
/audit --actor cli:local --since 24h
/elwisp
/elwisp server-watchdog -n 20
/elwisp --source minecraft-main --mode llm --since 2h
```

## 高风险工具确认

当工具调用触发高风险确认时，Agent 会提示可用确认命令，例如：

| 命令 | 作用 |
| --- | --- |
| `/detail` | 查看待确认工具调用详情；支持工具自定义纯文本详情，未自定义时显示格式化后的参数。 |
| `/confirm` | 确认当前待确认工具调用。 |
| `/confirmtool` | 确认当前工具。 |
| `/confirmall` | 确认当前批次全部待确认工具。 |
| `/reject` | 拒绝当前待确认工具调用。 |
| `/stop` | 停止当前请求。 |

具体提示以运行时输出为准。
