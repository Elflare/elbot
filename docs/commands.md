# 命令速查

slash 命令由 Agent Core 统一处理，CLI、QQ 等平台共享同一套语义。默认前缀 `/`，可在 `app.toml` 的 `[commands]` 配置。

## 帮助与诊断

| 命令 | 作用 |
| --- | --- |
| `/help` | 可用命令列表。 |
| `/help <command>` | 某个命令的详细帮助，例如 `/help model`。 |
| `/doctor` | 只读检查当前配置及内置 Skill（仅超级管理员）。 |

`/doctor` 使用服务启动时选定的主配置路径，分离配置按 `config_files` 定位。有问题时列出实际文件路径、具体问题、说明和默认模板的 GitHub raw 地址，并生成可复制给 Elbot 的处理请求；复制给能访问这些文件的 Elbot 处理。检查本身不改文件、不自动修复，无问题只返回 `Everything is OK`。范围见 [配置诊断](configuration.md#配置诊断)。

## 模型

| 命令 | 作用 |
| --- | --- |
| `/checkmodel [关键词]`（别名 `/models`） | 查看或搜索模型。 |
| `/models --fresh` / `--refresh` | 重载厂商配置并强制刷新模型列表。 |
| `/model <编号或名称>` | 切换当前 Session 模式使用的模型。 |
| `/model --chat <模型>` / `--work <模型>` | 切换 chat / work 模式模型。 |
| `/model --elwisp<1\|2\|3> <模型>` | 切换 Elnis elwisp1/2/3 模型槽位。 |
| `/model --compact <模型>` | 切换 Chat 文字摘要模型；Responses 原生压缩使用当前对话模型。 |
| `/model --naming <模型>` | 切换 Session 自动命名模型。 |

模型参数可以是列表编号、模型名或 `provider/model`，例如 `/model --work deepseek/deepseek-chat`。

模型选择按模式／槽位全局生效并保存到 `state.toml`；`/model` 未指定目标时使用当前 Session 的模式，无当前 Session 时使用默认模式。

## 模式

| 命令 | 作用 |
| --- | --- |
| `/chat [消息]` | 切换到 chat 模式或新建 chat Session，不注入工具。 |
| `/work [消息]` | 切换到 work 模式或新建 work Session，启用工具发现与调用。 |

- 带消息时先切换再把它作为同一条用户消息发送；模式切换被拒绝时消息不发送（例如 `/chat 随便聊聊`）。
- 已有历史的 work Session 不能直接切到 chat，需要先 `/new`。

## Session

| 命令 | 作用 |
| --- | --- |
| `/new` | 创建并切换到新 Session。 |
| `/status` | 当前 Session 状态。 |
| `/sessions [关键词]` | 列出或搜索可见 Session。 |
| `/resume [编号或 session_id]` | 恢复历史 Session。 |
| `/archives [页码] [关键词]` | 已归档 Session 列表。 |
| `/archive [编号或 session_id] --confirm` | 归档。 |
| `/unarchive [编号或 session_id]` | 取消归档。 |
| `/pin` / `/unpin [编号或 session_id]` | 置顶 / 取消置顶。 |
| `/rename [编号或 session_id\|当前标题] <新标题>` | 重命名。 |
| `/delete <编号或 session_id> --confirm` | 永久删除。 |
| `/clean --confirm` | 删除过期且未归档、未置顶的 Session。 |

- 除 `/rename` 与 `/delete` 外，带 `[编号或 session_id]` 的命令省略参数时作用于当前 Session；编号由 `/sessions`、`/archives` 展示，可被这些命令复用。
- `/resume 1` 直接恢复最近更新的非当前 Session，无需先执行裸 `/resume`；编号按更新时间排列、不受置顶影响，翻页连续。
- CLI 是本地高权限入口，可跨平台查看和管理 Session；非 CLI 平台只看当前平台和作用域。Cron／Elnis 后台会话仅所属用户可同平台私聊恢复，或从 CLI 管理入口恢复，群聊和频道不显示，也无法通过直接 ID、引用消息或解除归档绕过；首次恢复会永久转为前台归属，切走不会恢复后台身份。
- 当前会话处理中时不能执行 `/new`、`/resume`、`/fork`、`/chat`、`/work` 等切换类命令，需要先 `/stop`。
- 删除是永久操作且需要 `--confirm`；执行中的会话不能删除，自动清理也会跳过。

## Fork

| 命令 | 作用 |
| --- | --- |
| `/messages [页码]` | 列出当前 Session 中可用于 fork 的 assistant message ID。 |
| `/fork <message_id>` | 从该 assistant 消息创建分支 Session，原会话不变。 |

## 请求管理

| 命令 | 作用 |
| --- | --- |
| `/requests` | 当前进程的 active request，包括 turn、LLM、tool、hook；turn 显示运行阶段和阶段耗时。 |
| `/stop [request_id\|编号]` | 停止指定请求，编号取自 `/requests`；不传参数时停止当前 Session 的请求。一次性 exec Hook 会结束完整进程树，持久 Worker 只取消当前调用。 |
| `/stopall` | 停止当前进程的所有 active request。 |

## 上下文压缩

| 命令 | 作用 |
| --- | --- |
| `/compact` | 手动压缩当前 Session 上下文。 |

- 自动压缩由 `[context] compact_enabled` 和 `compact_trigger_ratio` 控制。
- 压缩保留历史用户原话、忽略工具返回值，成功后创建并切换到独立的 `原标题 compacted-N` Session，旧 Session 不修改；可用 `/stop` 取消。

## 工具与 Skill

| 命令 | 作用 |
| --- | --- |
| `/tools` | 列出已注册工具和外置 Skill。 |
| `/tools reload` | 重新扫描并加载 Skill。 |
| `/tools remove <name> --confirm`（别名 `/tools uninstall`） | 删除外置 Skill 及其目录。 |

`reload` 先完整扫描并验证候选 Skill，再一次性替换当前集合；存在重名 Skill、与内置工具重名或读取失败时返回错误并保留原有工具集合。

work 模式下 LLM 可用 `discover_tool` 按需发现；聊天中也可用 `@tool:<name-or-tag>`（简写 `@t:`）预载工具，或用 `@skill:<name>`（简写 `@s:`）把 Skill 文档加入本轮并预载运行 wrapper。语法与示例见 [内联预载](concepts.md#内联预载)。

## 文件编辑撤销

仅超级管理员可用。

| 命令 | 作用 |
| --- | --- |
| `/rollback` | 列出可撤销文件：编号、绝对路径、编辑时间、撤销动作。 |
| `/rollback <编号>` | 直接撤销该编号对应的文件编辑。 |

- 每个文件保留最近一次成功的 `edit_file` 修改：已有文件恢复原内容和权限，新建文件删除、父目录保留；撤销后备份消耗，不支持连续撤销或重做。
- 编号绑定具体修改：再次编辑会获得新编号并令旧编号失效，编号在进程内不复用；文件再被修改后会拒绝撤销。撤销要求当前会话没有运行中的任务或压缩（可等待完成或 `/stop`）。
- 备份仅存内存，按用户、平台 Scope 和当前 Session 隔离：切换或清空 Session 后立即失效，切回来也不能恢复，重启同样失效。整个进程最多 256 MiB 原始内容、1024 条记录，超限淘汰最旧记录并在编辑结果中提示；失败与预览不替换已有备份。

## Hook

| 命令 | 作用 |
| --- | --- |
| `/hooks` | 列出所有已注册 Hook。 |
| `/hooks <name>` | 某个 Hook 的详细配置。 |
| `/hooks start <id>` / `stop <id>` | 启动 / 优雅停止一个持久 Hook。 |
| `/hooks restart <id>` | 停止并重新启动一个持久 Hook。 |
| `/hooks reload` | 重读规则和持久 Hook 配置，重新协调进程生命周期。 |

- 规则 Hook 用配置里的 `name`；内置 Hook 用 `builtin.*`，例如 `builtin.cron.missed_once`。
- `Description` 显示在列表和详情里；规则细节只在详情里显示。
- `reload` 重读 `hooks.toml` 和各插件 `hook.toml`，重建 Hook 注册并替换受影响的持久进程。
- `/hooks` 为超级管理员命令。

## 日志和审计

| 命令 | 日志文件 | 作用 |
| --- | --- | --- |
| `/log [options]` | `elbot-YYYY-MM-DD.log` | 运行日志。 |
| `/audit [options]` | `audit-YYYY-MM-DD.log` | 审计日志。 |
| `/elwisp [name] [options]` | `elnis-YYYY-MM-DD.log` | Elnis/Elwisp 事件日志。 |
| `/usage [options]` | 审计日志 | 汇总 token 消耗（仅超级管理员）。 |

选项（“适用”列省略即三者通用）：

| 选项 | 适用 | 作用 |
| --- | --- | --- |
| `-n, --limit <n>` | log/audit/elwisp | 返回条数，默认 5。 |
| `-d, --days <n>` | log/audit/elwisp | 读取最近 n 天，默认 1。 |
| `--level <level>` | log/audit/elwisp | 最低等级：`debug`、`info`、`warn`、`error`。 |
| `-d, -i, -w, -e` | log/audit/elwisp | 等级快捷方式，不改变展示方式。 |
| `--raw` | log/audit/elwisp | 显示文件中的完整原始记录，与等级筛选独立。 |
| `--since <time>` / `--until <time>` | log/audit/elwisp | 时间范围，例如 `2h`、`30m`、`2026-06-03`。 |
| `--msg <text>` | log/audit/elwisp | 按消息正文过滤。 |
| `--contains <text>` | log/audit/elwisp | 按解码后的记录内容或原始记录过滤。 |
| `-u` / `-a` | log | 筛选用户 / 助手事件。 |
| `-s, --system` | log | 筛选并显示 `system prompt` 日志。 |
| `--hook` | log/audit | 筛选 `module=hook` 的记录。 |
| `-t` | audit | 筛选 `event=tool_call` 的工具审计。 |
| `--run_source <来源>` | audit | `foreground`、`cron`、`elnis`；可与 `-t` 及其他条件组合。 |
| `--event <name>` | audit | 例如 `tool_call`、`llm_usage`、`permission_denied`。 |
| `--risk <level>` | audit | 风险等级。 |
| `--actor <id>` / `--session <id>` / `--tool <name>` | audit | 按 actor、Session、工具名过滤。 |
| `[name]`、`--name`、`--elwisp <name>` | elwisp | 按 Elwisp 名过滤，三种写法等价。 |
| `--source` / `--id`（= `--source-id`） | elwisp | 按事件来源 / 外部事件 ID。 |
| `--mode <record\|direct\|llm>` | elwisp | 事件模式。 |
| `--event-key <key>` / `--event-id <id>` | elwisp | Elnis event key / 内部 event ID。 |
| `--token <name>` | elwisp | token name（不含原文）。 |
| `--tag <tag>` | elwisp | 包含指定标签，可重复，要求全部匹配。 |
| `-d, --days <n>` | usage | 汇总最近 n 天，默认 1。 |
| `-n <x[-y]>` | usage | 汇总倒数第 x 到第 y 次模型调用（含两端），序号 1–100000；省略 y 只取第 x 次。 |
| `-m, --model <name>` | usage | 模型名。 |
| `-s, --session <id>` | usage | Session ID。 |
| `--by <key>` | usage | `model`（默认）、`day`、`session`。 |
| `--since <time>` / `--until <time>` | usage | 时间范围。 |

示例：`/log -w -n 10` · `/log --system` · `/audit --event tool_call --risk high` · `/audit -t --run_source cron` · `/elwisp server-watchdog --mode llm --since 2h` · `/usage --by day -d 30` · `/usage -n 2-4 -m gpt-4o`。

- `/usage` 从审计日志的 `llm_usage` 事件聚合，按模型/天/会话分组统计 prompt、completion、total、cache 和耗时；未知、非法或溢出的值显示 `null` 并传递到受影响的分组和总计项，明确的零正常统计，用量未知的调用仍计入次数，扫描遇到损坏记录时提示统计可能不完整。
- 工具参数日志最多 512 rune，工具返回的日志摘要最多 1024 rune，截断省略号计入上限（汉字按 1 rune）；实际调用参数和返回给模型的结果不受日志限长影响。

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

普通用户的确认等待默认在 10 分钟无有效操作后过期；若当前群聊或私聊的 Session TTL 更短，则使用更短的时限。超级管理员不受额外的 10 分钟限制，但仍遵守大于 0 的 Session TTL。执行 `/detail` 会重新计时；过期后工具不会执行，当前处理会停止。

具体提示以运行时输出为准。
