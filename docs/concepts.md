# 核心概念

本文档解释 ElBot 的主要概念，帮助理解为什么它这样组织对话、工具、上下文和扩展能力。

## Agent Core

Agent Core 是 ElBot 的对话与编排中心，负责：

- 接收平台输入。
- 解析 slash 命令。
- 管理 Session 上下文。
- 构建 Prompt。
- 调用 LLM。
- 处理工具调用循环。
- 统一输出最终回复。

平台适配层只负责把平台消息转换为统一输入，以及把输出意图发送回平台。Agent Core 不依赖具体平台。

## Chat / Work 双模式

ElBot 把对话分成两种模式：

| 模式 | 适合场景 | 工具 |
| --- | --- | --- |
| `chat` | 闲聊、陪伴、轻量问答、低成本对话 | 不注入工具 |
| `work` | 搜索、文件、命令、Cron、Skill 等任务 | 启用工具发现与工具调用 |

这样做的目的：

- 普通聊天不为工具 schema 支付上下文成本。
- 工作任务可以使用更强模型和工具能力。
- 两种模式可以配置不同模型。

运行时可以使用 `/chat` 和 `/work` 切换模式。

## 工具发现

ElBot 不会在每轮 work 对话中默认注入所有工具的完整 schema。

默认流程是：

1. Agent 只提供 `discover_tool` 和当前可用工具名称。
2. LLM 判断需要哪个工具。
3. LLM 调用 `discover_tool` 获取工具详情。
4. Agent 把被发现的工具 schema 注入后续请求。
5. LLM 再调用具体工具。

这种机制可以减少普通任务中的无效上下文开销，也能降低无关工具干扰。

## 内联预载

用户可以在普通输入中提前指定本轮要用的工具或 Skill。

- `@tool:<name-or-tag>`：提前注入工具或工具 tag 的 schema。
- `@skill:<name>`：把指定 Skill 文档内容加入本轮用户消息，并注入该 Skill 需要的运行 wrapper。Skill 本体不是 top-level tool schema。

示例：

```text
@tool:web 帮我查一下今天的新闻摘要
@tool:files 读取这个配置并解释
按这个技能处理文件 @skill:docx
```

有效指令会被剥离；工具 schema 和 Skill wrapper 会持久化到当前 Session 的工具缓存。无效值会保留为普通文本并提示。多个 ELyph Skill 同时注入时，ELyph 规则说明只会加入一次。

## Session

Session 是 ElBot 的持久化会话单位。它保存：


- 会话标题和模式。
- 用户消息与 assistant 回复。
- 上下文压缩 checkpoint。
- 工具调用记录。
- 平台隔离信息。

常用操作：

- `/new` 创建新 Session。
- `/sessions` 查看历史 Session。
- `/resume` 恢复 Session。
- `/archive` 归档 Session。
- `/pin` 置顶 Session。
- `/delete` 删除 Session。

CLI 是本地高权限入口，可以跨平台查看 Session；其他平台默认只看到当前平台和作用域下的 Session。

## Fork

Fork 用于从历史 assistant 消息处创建新的对话分支。

流程：

1. 用 `/messages` 找到可 fork 的 assistant message ID。
2. 用 `/fork <message_id>` 创建分支 Session。
3. 新 Session 使用 fork 截止点之前的上下文继续对话。

Fork 不会删除或修改原 Session。

## 上下文压缩

长对话会逐渐接近模型上下文窗口。ElBot 支持上下文压缩：

- 自动压缩：由 `[context] compact_enabled` 和 `compact_trigger_ratio` 控制。
- 手动压缩：使用 `/compact`。

压缩后的上下文视图通常由“最新摘要 + 摘要之后的新消息”组成。

重要约定：压缩不删除原始消息，只改变后续发给 LLM 的上下文视图。

## Prompt 与 Soul

ElBot 从 `SOUL.md` 加载 Agent 的基础 System Prompt。

Prompt Builder 会把以下内容组合成最终请求：

- Soul Prompt。
- 当前平台和 Actor 信息。
- 常驻记忆。
- 工具名称提示。
- 压缩摘要。
- 会话历史。

工具发现、常驻记忆和时间等动态信息不会硬编码进 `SOUL.md`。

## 记忆

ElBot 将记忆分成两类：

| 类型 | 用法 |
| --- | --- |
| 常驻记忆 | 短小、稳定、每轮都可能有用的信息，会被注入 Prompt。 |
| 长期记忆 | 更长、更复杂的信息，以 Markdown 为源数据，通过工具按需搜索。 |

常驻记忆内部保存为 core 和 normal 两段：core 用于核心信息，修改需要高风险确认；normal 用于普通信息，可追加、覆盖或清空。注入 Prompt 时不会暴露分段标题，而是合并成一段自然文本。

长期记忆使用 Markdown 文件作为人类可读源数据，SQLite FTS 作为可重建搜索索引。

## Tool Runtime

Tool Runtime 管理工具的注册、发现、权限、风险评估和执行。

当前内置能力包括：

- Web 搜索与网页提取。
- Workspace：设置当前 Session 的共享工作目录。
- 文件读写。
- Shell 命令。
- 聊天历史查询。

- 常驻记忆和长期记忆。
- Cron 管理。
- 文件发送。
- Skill 创建、读取、修改和运行。
- `elwisp_creator`：为超级管理员返回创建 Elwisp 的协议说明、配置片段、脚手架和测试清单。

工具结果可以回灌给 LLM，也可以返回平台无关的输出意图，由 Agent 统一发送。工具结果中的 `Warnings` 会回灌给 LLM，用于提示后续优先使用更合适的工具，例如用 `read_file` 代替 shell `cat`。前台 work Session 可用 `workspace` 设置共享工作目录，所有需要路径的工具会基于该目录解析相对路径；cron/Elnis 后台任务仍使用各自 sandbox。EL Skill、常驻记忆和长期记忆源文件由 FileGuard 保护：读取会提示使用对应专用工具，通用文件工具或 shell 直接写入会被拒绝。


## 安全策略

ElBot 的工具系统包含风险等级和权限控制。

核心规则：

- 风险等级用于内部权限与确认，不直接暴露给 LLM。
- 普通用户只能发现和调用允许风险范围内的工具。
- 超级管理员调用高风险工具时也需要确认。
- 权限拒绝、危险确认和工具调用会进入审计日志。

CLI 默认本地用户 `local` 是超级管理员。

## Hook

Hook Layer 用于在关键流程前后扩展行为，例如修改消息、追加输出意图、调用低风险工具或注入常驻记忆。规则 Hook 支持条件匹配、多段输出、exec 脚本和角色分区。

重要约定：Hook 不替代 Security Layer，安全判定仍以 Security Layer 为准。完整配置和示例见 [Hook](hooks.md)。


## Output Layer

Output Layer 定义平台无关的输出意图，例如：

- 文本。
- 图片。
- 文件。
- at。
- reply。
- 表情。

Agent、Hook 和 Tool 不应该直接依赖具体平台发送消息，而是返回 output intent，由 Output Manager 统一发送。

## Cron

ElBot 包含两层 Cron 能力：

| 类型 | 说明 |
| --- | --- |
| Direct Cron | 按计划直接发送固定内容。 |
| LLM Cron | 按任务描述驱动模型执行，并可使用工具。 |

后台 Cron 有独立 Session 和 sandbox 约束。LLM Cron 可以通过 `session_mode` 选择后台 Session 模式：默认 `work`，传 `chat` 时不注入工具 schema，适合不需要工具的低成本任务。LLM Cron 可以通过 `tool_list_names` 预注入工具名或 Skill 名：普通工具会注入 schema，Skill 会把说明注入后台任务 prompt，并自动注入对应 runner。后台任务中的所有路径参数都应使用当前任务工作目录内的相对路径；LLM Cron 的最终 JSON 可通过 `report_segments` 附带当前任务工作目录内的图片或文件相对路径。超级管理员在平台里引用回复 LLM Cron 的通知消息时，会自动 resume 到对应后台 Session 继续对话；普通用户引用时只会作为普通引用文本处理。

## Elnis / Elwisp / Elvena

Elnis 是 ElBot 的监听枢纽，用于接收外部事件。Elwisp 是外部子监听器，负责观察服务器、Webhook、RSS、日志或脚本输出等外部世界。Elvena 是 Elwisp 向 Elnis 投递事件的协议，也是 Hook exec 等内部触发源复用的动作协议。


它们的分工是：Elwisp 观测一切，Elnis 管理一切，ElBot 掌控最终执行与投递。

Elnis 不作为聊天平台，也不替代 Cron。Cron 处理“按时间触发”的任务，Elnis 处理“按外部事件触发”的任务。完整介绍见 [Elnis 监听枢纽](elnis.md)，配置和请求示例见 [Elnis 配置与使用](elnis-usage.md)。

## Skill 与 ELyph

ElBot 支持 Skill 扩展，并引入 ELyph Task Notation（任务表示法）描述可复用任务。

Skill 类型：

- AgentSkill：放在 `skills/agent/<skill>/`，遵从或兼容 agentskills.io 风格 `SKILL.md`，也可用 `SKILL.elyph` 覆写 Agent 可读说明；当前可通过 `python_skill_run` 执行附带 Python 脚本。
- Go Skill：放在 `skills/go/<skill>/`，使用 `SKILL.elyph` 描述任务；存在二进制时可通过 `go_skill_run` 执行，并从 stdin 接收 JSON payload。


ELyph 的目标是用更短、更稳定的结构表达输入、输出、步骤、条件和约束，减少自然语言任务描述的歧义。读取或修改 Go Skill 的 `SKILL.elyph` / `main.go` 应使用 `read_el_skill` / `modify_el_skill`；通用文件工具或 shell 直接修改这些文件会被拒绝。完整语法见 [ELyph 任务表示法](elyph.md)。


## 平台适配

Platform Adapter 负责接入具体平台。当前主要包括：

- CLI。
- QQ OneBot。
- QQ 官方机器人。

平台适配层负责：

- 接收入站消息。
- 解析文本、图片、文件、at、reply 等平台特性。
- 转换为 Agent Core 的统一输入。
- 发送 Output Layer 的统一输出意图。

## 日志与审计

ElBot 区分：

| 类型 | 用途 |
| --- | --- |
| 运行日志 | 排查启动、模型请求、平台连接、持久化等运行问题。 |
| 审计日志 | 追踪权限拒绝、工具调用、危险确认、Cron 投递等关键行为。 |

可以用 `/log` 和 `/audit` 在运行时查询。

## 开发期约定

ElBot 仍在快速开发中：

- 内部接口可能调整。
- 配置和命令可能变化。
- 用户文档优先覆盖稳定使用路径。
- 详细开发计划和任务拆分放在 [`../devdocs/`](../devdocs/)。
