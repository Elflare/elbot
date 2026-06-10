# MVP 范围

## 目标

MVP 目标是实现一个可运行、可流式对话、可持久化、可恢复、可调用基础安全工具的最小 ElBot 系统。

MVP 优先验证以下核心链路：

- 配置加载与日志启动。
- CLI 或本地入口交互。
- OpenAI-compatible 流式 LLM 对话。
- 模型查看与模型切换。
- Session 与 Message 的 SQLite 持久化。
- 历史 Session 查看与恢复。
- 工作模式、聊天模式与基础工具调用闭环。

## 必须实现

### 配置文件布局

默认主配置入口为 `config/app.toml`，Provider 配置为 `config/providers.toml`。应用级配置与模型供应商配置分离，配置内相对路径基于主配置文件所在目录解析。


### CLI 或本地入口

提供最小交互入口，用于发送用户消息、接收模型流式回复、执行内置命令和退出程序。

### LLM Adapter

实现 OpenAI-compatible Chat Completions Adapter。

MVP 要求：

- 支持流式对话。
- 支持流式内容增量解析。
- 支持工具调用 delta 解析与累积。
- 支持 token usage 解析。
- 支持常见 API 错误解析。
- 支持模型列表拉取。


### 模型管理

MVP 期间命令体系至少支持：

- `/model <name or number>` 切换当前对话模型。
- `/checkmodel [query]` 查看或搜索可用模型。

模型列表来源包括 Provider `/models` API 和手动配置的模型列表，合并后去重展示。

### SQLite 对话存储

使用 SQLite 存储 Session 与 Message，支持程序重启后恢复必要对话历史。

MVP 优先实现：

- Session 创建。
- Message 写入。
- 当前 Session 记录。
- 按 Session 加载消息。
- 基础数据库结构升级机制。

### 统一命令

Slash 命令由 Agent Core 统一处理，CLI 或其他平台只负责把平台输入转换为内部消息。

MVP 期间命令体系至少支持：

- `/new` 开启新对话。
- `/status` 查看当前对话基础状态。
- `/sessions [关键词]` 查看或搜索历史 Session。
- `/resume <编号>` 恢复 Session。
- `/model <name or number>` 切换当前模型。
- `/checkmodel [query]` 查看或搜索模型。
- `/exit` 退出 CLI。

`/status` 只要求展示 MVP 已实现的基础状态，例如 Session ID、title、mode、status、当前模型、message count、创建时间、最后对话时间、最后一次 ask 和最后一次 answer 预览。

后续版本可扩展展示压缩模型、上下文窗口、token 消耗、active request、工具使用情况和子 Agent 状态。

### Session 管理与恢复

MVP 支持：

- 创建新 Session。
- 获取当前 Session。
- 查看历史 Session。
- 搜索历史 Session。
- 恢复指定 Session。
- 后续消息继续写入恢复后的 Session。

Session 清理、归档、置顶和删除确认放入后续里程碑。

### 工作模式与聊天模式

MVP 支持工作模式与聊天模式的基础语义：

- 工作模式默认开启。
- 工作模式注入工具发现能力和工具名称列表。
- 聊天模式保持纯对话，不注入工具。
- 工作模式切换到聊天模式时，需要新建 Session。
- 聊天模式可以切换到工作模式。

### `discover_tool`

实现工具发现工具。

MVP 要求：

- 未指定工具名称时，返回可用工具名称与简介。
- 指定工具名称时，返回该工具的完整定义。

### 最小 Tool Runtime

实现工具注册、发现、调用、参数校验和结果回传。

MVP 内置安全工具：

- `echo`
- `get_current_time`
- `calculator`

### 工具调用闭环

支持模型调用工具、系统执行工具、工具结果写回上下文、再次请求模型生成最终回复。

## 后续里程碑能力

### 上下文压缩

上下文压缩在后续 Milestone 实现，用于长会话场景中控制发送给 LLM 的上下文视图。

### 任意 Fork

Fork 在后续 Milestone 实现，用于从历史消息创建会话分支。

### 危险确认

风险等级、权限系统和危险确认在后续 Milestone 实现，由 Security Layer 作为硬约束统一处理。MVP 内置工具均为安全工具。

### Session 清理、归档、置顶和删除

Session 自动清理、手动归档、置顶和删除确认在后续 Milestone 实现。

### QQ 完整适配

QQ 接入可在核心链路稳定后实现。

### MCP 工具接入

MCP 工具接入在后续扩展阶段实现。工具运行时接口需预留扩展空间。

### 子 Agent 系统

子 Agent 系统在后续扩展阶段实现。后续版本可扩展展示子 Agent 状态。

### 复杂工具组

复杂工具组在后续扩展阶段实现，MVP 保留扩展空间。

### Cron 记忆整理

定时记忆整理在后续扩展阶段实现。

### 长期记忆检索

完整长期记忆系统在后续扩展阶段实现。MVP 可先实现常驻记忆注入或留空。

### 多平台支持

MVP 仅需一个稳定入口，优先 CLI 或本地入口。

## 验收标准

### 基础流式对话可用

用户通过 CLI 或本地入口发送消息后，系统能够调用 LLM，并以流式方式返回回复。

### 模型管理可用

用户可以通过 `/checkmodel [query]` 查看或搜索模型，并通过 `/model <name or number>` 切换当前对话模型。

### 会话可持久化

用户对话能够写入 SQLite。程序重启后，可通过 `/sessions` 和 `/resume <编号>` 找回并继续对话。

### 状态查看可用

用户可以通过 `/status` 查看当前 Session 的基础状态。

### 模式切换可用

用户可使用工作模式和聊天模式，且两种模式的工具注入行为符合设计。

### 工具发现可用

模型可通过 `discover_tool` 获取工具列表或指定工具详情。

### 工具调用闭环可用

模型能够调用至少一个内置安全工具，系统能执行工具并将结果交回模型生成最终回复。
