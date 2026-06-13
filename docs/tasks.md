# 任务拆分

## 说明

本文档按里程碑拆分 ElBot 的实现任务。每个里程碑应尽量保持可运行、可验证。

任务顺序以 MVP 为目标，不追求一次性实现完整设计。

## Milestone 0：项目骨架

目标：项目可以启动，具备基础配置和日志。

### 初始化 Go 项目

- [x] 创建 Go module。
- [x] 创建基础目录结构。
- [x] 创建 `cmd/elbot/main.go`。
- [x] 创建 `internal/app`。
- [x] 添加基础启动流程。

### 配置系统雏形

- [x] 定义配置结构体。
- [x] 支持从文件加载配置。
- [x] 支持模型 API Key、Base URL、Model 名称配置。
- [x] 支持 SQLite 路径配置。

### 日志系统雏形

- [x] 添加统一 logger。
- [x] 支持日志等级配置。
- [x] 启动时打印版本、配置路径、数据库路径。
- [x] 日志写入固定文件目录，并默认清理 30 天前日志。
- [ ] 后续支持配置日志保留天数。

## Milestone 1：LLM Adapter

目标：能够通过 OpenAI-compatible API 完成一次流式对话。

### 定义 LLM 类型

- [x] 定义 `LLMMessage`。
- [x] 定义 `ChatRequest`。
- [x] 定义 `ChatResponse`。
- [x] 定义 `Usage`。
- [x] 定义 `LLM` interface。

### 实现 OpenAI-compatible Adapter

- [x] 实现请求 payload 构建。
- [x] 实现 HTTP Client。
- [x] 实现流式 SSE 响应解析。
- [x] 解析 `choices[0].delta.content`（流式增量）。
- [x] 解析 `tool_calls` delta 并累积。
- [x] 解析流式最后一个 chunk 中的 token usage。
- [x] 处理 HTTP 非 200 错误。
- [x] 提取常见 API 错误信息。

### 最小验证

- [x] 使用固定 system/user message 调用模型。
- [x] 在 CLI 流式输出模型回复。

### 后续修正项

- [x] `endpoint()` 使用 `strings.TrimRight(baseURL, "/")` 拼接 `/chat/completions`。
- [x] 支持 usage 单独出现在 `choices: []` 的流式 chunk 中。
- [x] 明确 `stream_options.include_usage` 的默认注入或配置注入策略。
- [x] 流式解析错误应能反馈给调用方。

## Milestone 2：CLI 入口与模型管理

目标：通过 CLI 与 ElBot 交互对话，支持模型切换与查看。

### CLI Platform Adapter

- [x] 定义 `PlatformAdapter` interface。
- [x] 实现 CLI 输入循环。
- [x] 将用户输入转换为内部消息。
- [x] 将 Agent 输出打印到终端。
- [x] 支持退出命令 `/exit`。

### Agent 最小闭环

- [x] 实现最小 Agent。
- [x] 接收 CLI 消息。
- [x] 调用 LLM。
- [x] 返回流式回复。

### 模型管理

- [x] 支持 `/model <name>` 切换模型（编号或名称）。
- [x] 支持 `/checkmodel [query]` 查看/搜索模型。
- [x] LLM 接口扩展 `ListModels`。
- [x] OpenAI adapter 实现 `/models` API 拉取。
- [x] API 拉取与手动配置 models 合并去重。

### 模型命令语义

- [x] `/model <name or number>` 支持按模型名或列表编号切换当前对话模型。
- [x] `/checkmodel [query]` 支持查看全部模型或按关键词搜索模型。
- [x] 模型列表合并 Provider `/models` API 返回结果与手动配置结果，并按模型名去重。
- [x] 命令输出应标记当前正在使用的对话模型。

## Milestone 2.5：配置文件布局与路径整合

目标：统一配置文件职责、加载入口和路径解析规则，为上下文压缩、工具、安全、平台等后续配置扩展打基础。

### 配置文件结构

- [x] 定义默认主配置入口 `config/app.toml`。
- [x] 定义 Provider 配置文件 `config/providers.toml`。
- [x] `app.toml` 保存 `[config_files]`、`[storage]`、`[runtime]`、`[context]` 等应用级配置。
- [x] `providers.toml` 保存 `[global_default]`、`[providers.*]` 和 `[model_metadata.context_windows]` 等模型供应商相关配置；当前选用模型、命名模型和默认新会话模式由 `state.toml` 管理。
- [x] 不兼容旧的根目录 `config.toml`。
- [x] 不兼容根目录 `providers.toml` 默认入口。

### 路径解析

- [x] `--config` 指向主配置文件。
- [x] 相对配置文件路径基于主配置文件所在目录解析。
- [x] SQLite 路径基于主配置文件所在目录解析。
- [x] 启动日志打印主配置路径、Provider 配置路径和最终数据库路径。

### 配置加载

- [x] 从 `app.toml` 加载应用级配置。
- [x] 从 `[config_files]` 加载 `providers.toml` 和热切换 `state.toml`。
- [x] 合并应用级配置和 Provider 配置。
- [x] 移除旧单文件配置加载逻辑。
- [x] 保持配置结构简单唯一。

### 配置示例参考

- [x] `config/app.toml` 中保留 `[config_files]` 示例，指向 `providers.toml`。
- [x] `config/app.toml` 中保留 `[storage]` 示例，说明 SQLite 相对路径基于主配置文件所在目录解析。
- [x] `config/app.toml` 中保留 `[context]` 示例，包含压缩开关、触发比例、目标比例、压缩模型和默认 context window。
- [x] `config/app.toml` 中保留 `[session.cleanup]` 示例，包含自动清理开关、保留天数和启动时清理开关。
- [x] `config/state.toml` 中保留 `[session] default_mode`、`[mode_models]` 和 `[naming_model]` 示例，用于运行态热切换。
- [x] `config/providers.toml` 中保留 `[model_metadata.context_windows]` 示例，用于 API 或 metadata 无法提供窗口信息时手动配置。

### 验证

- [x] 默认 `config/app.toml` 可以启动。
- [x] `--config` 指定其他主配置文件可以启动。
- [x] 从不同 CWD 启动时相对路径解析一致。

## Milestone 3：SQLite 存储

目标：Session 与 Message 可以持久化。

### SQLite 初始化

- [x] 选择 SQLite driver。
- [x] 使用 `modernc.org/sqlite`，保持 Windows/Linux 无 CGO 构建。
- [x] 建立数据库连接。
- [x] 启用 SQLite foreign keys。
- [x] 实现数据库结构升级机制（migration）。
- [x] 创建 `schema_migrations` 表。
- [x] 使用本地时间并按 RFC3339Nano 存储时间字段。

### Session 表

- [x] 创建 `sessions` 表。
- [x] `sessions` 支持 `archived_at`。
- [x] `sessions` 支持 `pinned_at`。
- [x] 创建当前查询所需的 Session 作用域/更新时间索引。
- [x] 不创建 pinned/updated_at 专用索引；pin 是低频小集合标记，避免为高频 updated_at 写入增加长期索引维护成本。
- [x] 实现 `SessionRepository.Create`。
- [x] 实现 `SessionRepository.Get`。
- [x] 实现 `SessionRepository.Update`。
- [x] 实现 `SessionRepository.ListByActor`。
- [x] Session 查询默认按 actor、platform 和 platform_scope_id 隔离。
- [x] CLI 作为最高权限入口，可跨平台查看 Session，并在列表中标明平台与作用域。
- [x] 预留 Session 级联删除能力。

### Message 表

- [x] 创建 `messages` 表。
- [x] 创建 `platform_message_map` 表。
- [x] 实现 `MessageRepository.Append`。
- [x] 实现 `MessageRepository.Get`。
- [x] 实现 `MessageRepository.ListBySession`。
- [x] 实现平台消息映射接口。

### Agent 接入存储

- [x] 用户消息写入数据库。
- [x] 助手回复写入数据库。
- [x] 每次请求加载当前 Session 上下文。

## Milestone 4：统一命令与 Session 基础

目标：支持基础命令、查看和恢复历史会话。

### 统一命令

- [x] 将 `/` 命令解析从平台适配层收敛到 Agent Core。
- [x] 定义统一 CommandRouter。
- [x] 定义统一 CommandHandler。
- [x] 平台输入可传递同一套 Slash 命令。
- [x] 对话中也可用命令，且不影响对话进行，不打断，不发送给llm
- [x] 若/命令没有匹配项，发出提醒而不是发给llm
- [x] /help查看常用命令
- [x] /help 基于已注册命令信息自动生成，不手写命令清单。
- [x] 支持 `/help <command>` 查看命令参数详情，命令注册时通过 `Info.Help` 提供详细说明。
- [x] 可以配置前缀，比如可以添加-开头也能识别
- [x] 命令 Router 只负责解析、注册、分发和冲突检测，不硬编码具体命令。
- [x] 具体命令各自提供名称、用法、描述和 handler，`/help` 从注册信息自动生成。
- [x] 内置命令也通过 Module 机制注册，按 Help、Model、Session 等功能域组织。
- [x] 不使用 `init()` 隐式自注册；新增功能域通过显式挂载 Module 扩展。

### 统一命令语义

- [x] CLI、QQ、Web、本地 API 等平台共享同一套 Slash 命令语义。
- [x] 平台适配层负责把平台输入转换为 Agent Core 可处理的消息。
- [x] 命令处理沉淀在 Agent Core 及其依赖的 Session、Request、Tool、Security 等核心模块。
- [x] 平台侧可根据自身能力提供补全、按钮、引用消息等交互增强，并映射到统一命令或内部请求。
- [x] CLI 支持简单命令名 Tab 补全，当前补全来源为已注册命令和 alias。
- [x] 后续用更模块化的补全服务替代当前 Agent 辅助方法，由 app 层装配并注入具体平台。
- [ ] 后续支持命令参数补全，例如 `/model` 补全模型名、`/resume` 补全会话编号。
- [ ] 后续支持普通用户能使用/sessions /new /fork /messages /resume 处理自己的sessiosn

### Session Service 基础

- [x] 定义 `SessionService`。
- [x] 实现 `GetOrCreateCurrent`。
- [x] 实现 `Create`。
- [x] 实现 `Resume`。
- [x] 实现 `List`。
- [ ] 实现 `SetMode`。

### `/new`

- [x] 支持 `/new` 开启新对话。
- [x] `/new` 创建新 Session。
- [x] 当前 Session 保留，不删除、不归档。
- [x] 后续消息进入新 Session。
- [x] 创建后返回新 Session 基本信息。

### `/status`

- [x] 支持 `/status` 查看当前对话状态。
- [x] 展示 Session ID、title、mode、status。
- [x] 展示 archived_at/pinned_at 派生状态。
- [x] 展示当前模型。
- [x] 展示 message count 和对话轮次。
- [x] 展示创建时间和最后对话时间。
- [x] 展示最后一次 ask 和 answer 预览。
- [x] active request、token 消耗、上下文窗口、工具使用情况等未完成项暂时显示 `TODO`。
- [ ] 后续版本可扩展展示子 Agent 状态。

### `/resume` 与 `/sessions`

- [x] CLI 支持 `/sessions`。
- [x] 非 CLI 平台执行 `/sessions` 时只展示当前平台和作用域下的 Session。
- [x] CLI 执行 `/sessions` 时可查看所有平台 Session，并展示平台与作用域。
- [x] CLI 支持 `/sessions <关键词>` 搜索。
- [x] 展示历史 Session 标题和消息摘要。
- [x] CLI 支持 `/resume <编号>`。
- [x] 恢复后后续消息进入指定 Session。

### `/resume` 与 `/sessions` 展示语义

- [x] `/resume` 无参数时展示可恢复的历史 Session 列表。
- [x] Session 列表应展示标题和前几轮消息摘要。
- [x] 摘要格式可采用 `u: 你好 / b: 我是 bot… / u: 今天天气… / ……`。
- [x] `/resume <编号>` 使用当前列表编号恢复指定 Session。

### 话题命名雏形

- [x] Session 默认标题从首条用户消息截断生成。
- [x] 预留命名 LLM 接口。

### 话题命名规则

- [x] 优先使用专门的命名 LLM 生成 Session 标题。
- [x] 未配置命名 LLM 时，使用主 LLM 生成标题。
- [x] 命名 LLM 不可用时，使用首条用户消息截断作为默认标题。
- [ ] 支持 `/rename <session编号|或者标题或者sessionid> <title>` 手动重命名 Session，并避免后台自动命名覆盖用户标题。标题若有重复的则弹出提示，让用户换成编号或者id

## Milestone 4.1：请求管理基础

目标：基础 LLM 请求、后续压缩请求和工具调用请求均可被查看、取消和超时清理。

### 请求管理

- [x] 维护 active requests。
- [x] 支持查看当前请求列表。
- [x] 支持停止指定请求。
- [x] 支持停止当前 Session 下全部请求。
- [x] 支持 `/status` 展示 active request 状态。
- [x] 支持 `/stop` 停止当前请求。
- [x] `/stop` 默认停止当前 Session 下全部 LLM、工具和子 Agent 活动。
- [x] 支持请求超时。
- [x] 请求取消后清理状态。
- [x] 预留接口，在工具调用循环中，用户直接发送消息不打断，而是在当前循环下一次 LLM 调用中附带这个消息。（若是多条，则合并为一条消息）。
- [x] 预留 /stopall，未来可能多平台同时对话或者在群聊中，不同群员进行对话，超级管理员无论在哪个平台都可以直接 stopall。
- [x] 交互式 CLI 引入最小 Bubble Tea TUI 地基，支持输出区与输入区分离、用户输入回显、输入历史、命令补全和自动换行。
- [x] CLI TUI 支持 PgUp/PgDn/Home/End/Ctrl+U/Ctrl+D 键盘滚动长输出，并启用鼠标事件支持分区滚动与 copy mode。

- [x] CLI TUI 支持 Tab 补全候选窗、上下选择候选，且 Esc/Ctrl+C 会先关闭候选窗或清空输入，再次按才退出。
- [x] CLI TUI 支持鼠标分区滚动和 Vim-like copy mode：Alt+h/Alt+l 进入聊天/通知复制模式，hjkl/v/V/y 选择复制，`/` 在当前区域搜索。
- [ ] CLI TUI 后续支持 Markdown 渲染，可考虑接入 Glamour。后续能配置用户名和助手名配置




## Milestone 5：会话模式与 Prompt Builder

目标：支持工作模式、聊天模式和基础 Prompt 组装。

### 模式字段

- [x] Session 增加 `mode`。
- [x] 默认模式为 `work`。
- [x] Prompt Builder 根据模式组装内容。

### Prompt 组装内容

- [x] Prompt Builder 支持注入 Soul 人格设定；System Prompt 来源为 `config/SOUL.md`。
- [x] Prompt Builder 预留当前会话模式参数，但不拼进 System Prompt。
- [x] Prompt Builder 预留常驻记忆接口，后续由 Memory 插件提供。
- [x] Prompt Builder 预留工具 schema 注入接口；工具发现不写入 System Prompt。
- [x] Prompt Builder 支持注入当前 Session 上下文。
- [ ] 后续通过独立上下文消息或专用 builder 字段注入时间、环境等运行时信息，不污染 Soul System Prompt。

### `/work` 与 `/chat`

- [x] 支持 `/work`。
- [x] 支持 `/chat`。
- [x] 工作模式切换到聊天模式时，如果已经产生对话，则不能切换，而是提示新建 Session。
- [x] 聊天模式可切换到工作模式。
- [x] 工具注入从切换后的下一次用户消息生效。
- [x] 两种模式模型可以分别设置，并写入 `state.toml` 的 `[mode_models]`。命名模型也由 `state.toml` 的 `[naming_model]` 管理。

### Prompt 注入约定

- [x] 工作模式不把 `discover_tool` 写入 System Prompt，后续通过 Tool Schema 注入。
- [x] 工作模式预留 Tool Schema 注入接口；Tool Runtime 完成后注入 `discover_tool` 与可用工具稳定名称。
- [x] 模型需要使用工具时，应优先根据工具名称调用 `discover_tool` 查询工具详情。
- [x] 若模型仅凭名称无法判断用途，应先获取全部工具名称与简介，再进一步查询目标工具。
- [x] 聊天模式切换到工作模式后，工具注入从切换后的下一次用户消息生效，以便复用 LLM 缓存。

## Milestone 6：上下文压缩

目标：支持主动与被动上下文压缩，在保留完整历史的前提下降低 LLM 请求上下文长度。

### 配置

- [x] 支持启用/禁用被动压缩。
- [x] 支持配置压缩触发比例，默认 `0.8`。
- [x] 支持配置压缩 Provider。
- [x] 支持配置压缩模型。
- [x] 支持配置默认 context window 兜底值。
- [x] 支持配置模型 context window 元信息。

### 数据层

- [x] 创建 `context_summaries` 表。
- [x] 保存压缩覆盖的消息范围。
- [x] 保存压缩摘要、压缩模型和厂商返回 token 用量。
- [x] 保存触发原因：`manual` 或 `auto`。
- [x] 支持查询当前 Session 最新压缩摘要。

### 上下文窗口与 token

- [x] 实现模型 context window 解析。
- [x] 优先从 Provider `/models` API 或 metadata 获取 context window。
- [x] API 或 metadata 无法提供时，读取手动配置的模型 context window。
- [x] 无法识别时使用默认 context window。
- [x] 实现消息 token 显示，若 API 没有返回则显示 unknown。

### 上下文加载

- [x] 实现 ContextLoader。
- [x] 加载当前 Session 最新 summary。
- [x] 加载 summary 之后的新消息。
- [x] 保留原始 messages，不删除历史。

### 主动压缩

- [x] CLI 支持 `/compact`。
- [x] `/compact` 调用压缩模型生成 summary。
- [x] 压缩完成后保存 checkpoint。
- [x] 压缩后后续请求使用新上下文。

### 被动压缩

- [x] 基于厂商返回 usage 判断下一轮是否需要压缩，未返回则显示 unknown。
- [x] 达到 context window 的配置比例后标记下次用户输入前自动压缩。
- [x] 压缩后重新构建上下文再调用主模型。
- [x] 压缩失败时给出明确错误或降级策略。

### 上下文窗口状态

- [x] `/status` 展示当前 tokens 消耗。
- [x] `/status` 展示 context window。
- [x] `/status` 展示窗口使用比例。
- [x] `/status` 展示是否接近压缩阈值。

### 模型命令

- [x] 支持 `/model --compact <name or number>` 切换压缩模型。
- [x] 支持 `/model --naming <name or number>` 切换命名模型，并写入 `state.toml` 的 `[naming_model]`。
- [x] 支持 `/model -c <name or number>` 作为短写。
- [x] 支持 `/model -n <name or number>` 作为命名模型短写。
- [x] 支持 `/checkmodel --compact [query]` 查看或搜索压缩模型候选。
- [x] 模型列表展示当前对话模型、当前压缩模型和当前命名模型标记。

## Milestone 7：Fork

目标：支持从历史消息创建分支。

### 数据层支持

- [x] `sessions` 支持 `parent_session_id`。
- [x] `sessions` 支持 `fork_from_message_id`。
- [x] 实现 Fork 上下文加载所需的 bounded message 和 summary 查询。

### Fork Service

- [x] 实现 `SessionService.Fork`。
- [x] 校验来源消息存在。
- [x] 校验来源消息必须是助手消息。
- [x] 校验用户对来源 Session 有权限。
- [x] 创建新 Session 并切换当前 Session。

### Fork 上下文加载语义

- [x] Fork 上下文读取父 Session 中截至来源消息的上下文视图。
- [x] 若来源范围内已有适用压缩摘要，则使用“最新 summary + summary 之后的新消息”。
- [x] 若 Fork 点早于任何压缩摘要覆盖范围，则使用原始消息。
- [x] 多级 Fork 递归读取父 Session，直到根 Session。

### CLI 命令

- [x] 支持 `/messages [page]` 列出当前有效上下文中可 Fork 的助手消息 ID 和短预览，Fork 分支会包含父会话截至 Fork 点的上下文。
- [x] `/messages` 只列助手消息，不显示角色；预览过长时截断，并支持分页。
- [x] `/fork <message_id>` 支持按当前有效上下文中的助手 message ID 做 Tab 补全。
- [x] 支持 `/fork <message_id>`。
- [x] Fork 后打印新 Session ID，并展示最近聊天记录。
- [x] 后续消息写入 Fork 分支。
- [x] `/sessions [page] [keyword]` 支持分页查看 Session 列表。
- [x] `/resume --page <page>` 支持分页查看可恢复列表，`/resume <编号>` 仍用于恢复当前列表编号。

## Milestone 8：Tool Runtime 与 `discover_tool`

目标：工具可以注册、发现和调用。

### Tool Runtime 能力约定

- [x] 支持工具注册与卸载。
- [x] 维护工具元信息，包括名称、简介、参数定义、调用约束和风险等级。
- [x] 工具调用应支持参数校验、超时控制、错误处理和结果回传。
- [x] 工具调用结果应进入上下文，供后续 LLM 请求使用。
- [x] Tool Runtime 为后续 MCP、skill 工具接入预留扩展点。skill 放 `skills/` 文件夹，目录约定为 Python 外置 skill 使用 `skills/py/<skill>/SKILL.md`，可选 `SKILL.elyph` 覆写 Agent 可读说明；Go skill 使用 ElBot 原生 `skills/go/<skill>/SKILL.elyph`，可选 Go binary。
- [x] 发现工具能一次传多个，返回值统一使用 `tools` 数组。
- [x] 增加 Go Tool Builder，降低内置工具和未来 Go 插件的 schema 编写成本。
- [x] 工具元信息支持默认隐藏和依赖工具声明，隐藏只控制 Prompt 暴露，不作为安全边界。
- [x] Session 内已发现工具 schema 会在后续 work 请求中通过 top-level tools 稳定注入，并将已发现工具名持久化到 Session metadata。

### Tool 类型

- [x] 定义 `Tool` interface。
- [x] 定义 `ToolSchema`。
- [x] 定义 `ToolInfo`。
- [x] 定义 `ToolCallRequest`。
- [x] 定义 `ToolResult`。

### Tool Registry

- [x] 实现工具注册。
- [x] 实现工具卸载。
- [x] 实现工具列表。
- [x] 实现按名称查询。
- [x] 实现 `discover_tool`。

### 内置工具

- [x] 实现受限 `shell` 工具，接口保留通用 `cmd`，当前代码只允许执行 `ls`，后续接入权限确认后再开放更多命令。
- [ ] send_file 工具暂时先支持base64发送，未来更新s3/r2上传发送

### Prompt 接入

- [x] 工作模式注入 `discover_tool`。
- [x] 工作模式注入工具名称列表，工具名称提示与 Soul 合并为单条 system message。
- [x] 聊天模式不注入工具。


## Milestone 9：工具调用闭环

目标：模型可以调用安全工具并基于结果继续回答。

### LLM 错误处理与兜底

- [ ] 支持网络错误重试。
- [ ] 支持请求超时控制。
- [ ] 支持不可重试错误识别。
- [x] 支持 API 错误信息提取。
- [x] 面向用户返回简洁错误提示。
- [x] 工具调用结束后模型未给出总结时，提供兜底总结。

### Tool Call 解析

- [x] 从 LLM response 中解析 tool calls。
- [ ] 将 tool calls 写入 `tool_calls` 表。
- [x] 校验工具是否存在。
- [x] 校验参数格式。
- [x] 支持用户在聊天中@工具直接把完整schema注入到prompt中


### Tool 执行

- [x] 调用目标工具。
- [x] 捕获工具错误。
- [x] 将工具结果转换为 LLM tool message。
- [x] 将工具调用消息写入数据库，保存 assistant tool_calls 与 tool result。
- [x] 将工具执行情况写入数据库。记录工具调用次数统计
- [x] 有多个相互依赖的工具时，名字默认只注入主工具的名字，llm搜索了主工具，返回主工具和其依赖工具。如果相互依赖就都注入

### 循环控制

- [x] 工具执行后再次调用 LLM。
- [x] 设置最大工具轮次，配置项为 `[tools].max_rounds_per_turn`，限制多轮工具循环而不是同一轮多个 tool calls。
- [x] 达到上限时请求模型总结，未执行的 tool calls 会收到 skipped tool message，并用无工具请求要求模型总结当前进度。
- [x] 模型无总结时提供兜底回复。

### OpenAI-compatible 工具消息支持

- [x] `toOpenAIMessages` 支持 assistant message 中的 `tool_calls`。
- [x] `toOpenAIMessages` 支持 tool message 中的 `tool_call_id`。
- [x] `toOpenAIMessages` 支持必要的 `name` 字段。

### 工具执行预览与 pending 输入

- [x] 工具执行过程发送 preview/notification，CLI 先通过 `SendPreview` 独立接口输出，其他平台 fallback 到普通聊天区。
- [x] 模型调用工具前若没有自然语言说明，自动发送正在调用工具的兜底 preview；`discover_tool` 说明也提示模型调用工具前先简短说明准备做什么。
- [x] 工具执行接入 Request Manager，`/requests` 可见，`/stop` 可取消。
- [x] 工具执行期间普通用户消息不打断工具，进入 pending，并在下一次 LLM 调用前作为补充输入注入。
- [x] CLI TUI 不再用手写 `---` 分隔 assistant 输出，换行不会自动结束同一轮回复。

- [x] 后续实现 CLI TUI 独立通知区，避免 preview 混入聊天 transcript。

## Milestone 10：权限系统与危险确认

目标：高风险工具不会未经许可执行。

### 权限系统

- [x] 定义 `Actor`。
- [x] 定义 `Role`。
- [x] 定义 `Action`。
- [x] 定义 `Resource`。
- [x] 实现 `Authorizer`。
- [x] 当前阶段默认只有超级管理员、普通用户。
- [x] 给工具分级，超级管理员全部能用，普通用户只能用哪些级别的。

### 权限控制对象

- [x] 控制平台命令访问。
- [x] 控制工具发现与工具调用。
- [x] 控制危险操作确认。


### 风险等级

- [x] 定义 `RiskLevel`。
- [x] Tool 可根据参数返回风险等级。
- [x] 低风险工具直接执行。
- [x] 高风险工具进入确认流程。
- [x] shell 工具基于 bash AST 做参数级风险解析，并在确认提示显示风险原因。
- [x] 普通用户可能强行注入Discover_tool不能发现的工具，所以执行时应该判断风险等级和用户级别兜底

### 确认流程

- [ ] 创建 `confirmation_requests` 表。
- [x] 支持确认单次操作。`/confirm`
- [x] 支持当前 Session 内自动确认同工具操作。
- [x] 支持当前 Session 内自动确认所有操作。`/confirmall`
- [x] `/reject <原因>` 拒绝操作并记录原因。原因可不填。
- [x] `/stop` 取消并停止本次对话。前方已实现
- [x] 高风险工具确认、拒绝和停止操作写入日志。
- [x] 确认结果写入审计日志。

## Milestone 11：Session 生命周期增强

目标：支持 Session 归档、置顶、删除和过期清理。

### Session Service 增强

- [x] 实现 `Archive`不同消息平台archive区分，cli能看所有的archive，其他平台只能看到自己的archive。。
- [x] 实现 `Unarchive`。同上
- [x] 实现 `Pin`。不同消息平台pin区分，cli能看所有的pin，其他平台只能看到自己的pin。
- [x] 实现 `Unpin`。同上
- [x] 实现 `Delete`。
- [x] 实现 `CleanupExpired`。
- [x] 非超级管理员 session 超时自动过期，用户下次对话时自动 new；手动引用自己的 bot 回复时可 fork。

### Session 清理与删除

- [x] 支持配置自动清理开关。
- [x] 支持配置 Session 保留天数，默认 30 天。
- [x] 支持启动时自动清理过期 Session。
- [x] 自动清理直接硬删除过期 Session，不先归档。
- [x] `archived_at IS NOT NULL` 的 Session 不参与自动清理。
- [x] pinned Session 不参与自动清理。
- [x] 支持 `/clean` 手动清理过期 Session。
- [x] 支持 `/delete <编号>` 手动删除指定 Session。
- [x] 删除和清理前要求用户确认。

### Session 归档与置顶

- [x] 支持 `/archive <编号>`。
- [x] 支持 `/archives` 列出所有归档session
- [x] 支持 `/archives <关键词>` 搜索归档session
- [x] 支持 `/unarchive <编号>`。
- [x] 归档通过 `archived_at` 表示永久保存。
- [x] 支持 `/pin <编号>`。
- [x] 支持 `/unpin <编号>`。
- [x] pinned Session 在列表中置顶。
- [x] `/sessions` 列表显示 archived/pinned 状态。


### CLI 体验预留

- [x] 支持基础命令自动补全能力。
- [x] 将 CLI 补全改为独立补全服务或平台能力装配，不再通过 Agent 辅助方法暴露。
- [x] 支持 fish/readline 风格的候选提示和上下选择；参数补全仍待后续 source 扩展。


## Milestone 12：审计与消耗统计

目标：关键行为可追踪。

### 审计日志

- [x] 创建专用结构化审计日志文件 `logs/audit-YYYY-MM-DD.log`，当前不落 `audit_logs` 表。
- [x] 支持 `/audit` 查看审计日志，默认最近 5 条，并支持按事件、风险、时间、用户、Session、工具和文本筛选。
- [x] 记录 LLM 请求消耗与响应耗时摘要。
- [x] 记录工具调用，工具次数统计仍使用 `tool_call_records`。
- [x] 记录危险确认等待、确认、自动确认、拒绝和停止。
- [x] 记录权限拒绝。
- [x] 记录 Session 恢复与 Fork。
- [x] 记录记忆写入、修改和删除。
- [x] 记录关键错误与异常。

### 消耗统计

- [x] 使用专用审计日志记录模型调用 usage，当前不落 `usage_records` 表。
- [x] 写入 LLM 厂商返回的 token usage。
- [x] 记录缓存命中相关 token 字段。
- [x] 写入请求耗时。
- [ ] 支持按 Session 查询消耗。
- [ ] 支持按模型查询消耗。
- [x] `/status` 展示当前 Session token 消耗。
- [x] 支持 `/log` 查看普通运行日志，默认最近 5 条、最低 info 级别，并支持时间、等级和文本筛选。

### 审计快速查询

- [x] 在任意平台，管理员都可以通过 `/audit` 查看。
- [x] 支持参数，如最近几条、按风险筛选、按用户 ID 筛选等。


## Milestone 13：Hook 基础

目标：关键流程可扩展，并为后续平台接入提供统一 Hook 点。

### Hook 接口

- [x] 定义 HookManager。
- [x] 实现空 HookManager。
- [x] 接入 BeforeReceive（实现为 `platform.message.received` 与 `agent.input.prepared`）。
- [x] 接入 AfterReceive（由输入流水线后的 Agent 处理阶段承接，当前无单独只读点）。
- [x] 接入 BeforeLLM（实现为 `llm.request.prepared`）。
- [x] 接入 AfterLLM（实现为可修改内容的 `llm.response.received`）。
- [x] 接入 BeforeToolCall（实现为 `tool.call.prepared`）。
- [x] 接入 AfterToolCall（实现为可修改结果的 `tool.call.completed`）。
- [x] 接入 BeforeSend（实现为 `agent.output.prepared`）。
- [x] 接入 AfterSend（实现为 `platform.message.sent`）。
- [x] 接入 OnError（实现为 `error.occurred`）。
- [x] 接入 OnPlatformConnected（实现为 `platform.connected`）。

### Hook 扩展注册方案

- [x] 方案 1：Go 内置 Hook Module 注册。新增 Hook Module/Registrar 地基，内置 Hook 通过 Go 代码显式注册到 HookManager，适合稳定、可测试、随程序发布的扩展。
- [ ] 方案 2：外部 Hook 目录扫描（预留，可能不做）。目录暂定 `hooks/<name>/HOOK.toml + command`，通过 stdin/stdout JSON 协议收发 Hook Event；用于未来用户免编译扩展，但存在安全、调试、跨平台和性能成本，是否实现后续再定。
- [ ] 方案 2：若未来实现，外部 Hook 默认应显式启用、限制超时，并复用 Hook priority 顺序；不使用 Go plugin，避免 Windows/Linux 兼容和动态加载维护成本。
- [ ] 临时hook


## Milestone 14：QQ 适配

目标：将核心能力接入 QQ。

### QQ Platform Adapter

- [x] 先实现 OneBot 正向 WebSocket universal。
- [x] 实现消息接收。
- [x] 实现文本发送。
- [x] 解析用户 ID。
- [x] 解析群 ID。
- [x] 解析引用消息，并以短文本上下文注入当前用户独立 Session。
- [x] 映射 bot 平台消息 ID 到内部 assistant Message ID。

### QQ 命令映射

- [x] 映射所有/命令。
- [x] 映射引用消息 Fork：引用 bot 回复发送 `/fork` 时转换为内部 assistant message ID，权限仍由 Session 访问控制兜底。
- [x] 普通引用回复自动 Fork：只有引用自己 Session 中可 fork 的 bot assistant 消息才自动 fork；引用其他用户或不可 fork 消息时转成纯文本引用上下文。

## Milestone 15：记忆与 Soul

目标：实现人格与基础记忆。

### 记忆语义约定

- [x] 常驻记忆保持短小且高优先级，每次注入 Prompt。
- [x] 常驻记忆支持增删改查。
- [x] 长期记忆使用 Markdown 源文件 + SQLite FTS 索引，由 LLM 通过工具主动增删改查。

- [ ] 记忆/el skill整理通过 Cron 类插件定时触发。（暂时不做）
 - [ ] 当日记忆没有更新时，跳过整理。
- [x] 常驻记忆长度限制（默认400字以内）
- [x] 实现不同用户有自己的常驻记忆，并按平台隔离；同一平台同一用户共享一段文本，不按群聊/私聊拆分。

### Soul

- [x] 定义 Soul 配置。
- [x] 加载 Soul Prompt。
- [x] Prompt Builder 注入 Soul。

### 常驻记忆

- [x] 用 TOML 记录常驻记忆，每个平台用户一条文本，固定路径为 `config/memories.toml`。
- [x] 实现常驻记忆读取。
- [x] Prompt Builder 动态注入常驻记忆；chat 模式只注入记忆文本，不注入工具。
- [x] 实现常驻记忆增删改查工具；仅 work 模式通过 `discover_tool` 发现并调用。

### 长期记忆

- [x] 长期记忆使用 Markdown 源文件 + SQLite FTS 索引。
- [x] 实现长期记忆写入工具。
- [x] 实现长期记忆查询工具。
- [x] 实现长期记忆更新工具。
- [x] 实现长期记忆删除工具。


## Milestone 16：后续扩展

### ELyph 与原生 skill

- [x] 定义 ELyph Task Notation 地基：使用 `SKILL.elyph` 作为 ElBot 原生 skill 的强结构说明文件；保留 `$user`、`$assistant` 固定变量；输入用 `<-`，输出用 `->`，`=>` 用于推导，`~` 表示禁止，`**` 表示约束，控制结构使用 `?if(...) {}`、`?else {}` 和 `each($item in $items, limit=N) {}`。
- [x] 新增独立 `internal/elyph` 包，拆分 parser/AST/diagnostic/rule card 等语言层职责；skill runtime 只消费 ELyph，不拥有语言实现。
- [x] 实现 ELyph parser/linter 基础校验：`#skill <name>`、保留变量不可重定义、条件/循环块括号配对、`each` 必须带正整数 limit；校验失败时返回可让 LLM 重写的诊断信息。
- [x] 在 `discover_tool` 返回 ELyph skill detail 时统一前置短规则卡；普通 Markdown skill 不注入 ELyph 规则，平时 Prompt 不常驻注入。

### 原生 skill 创建

- [x] 上线 `create_el_skill` 原生 skill 创建工具，不保留旧 Go skill 创建工具名兼容。
- [x] `create_el_skill` 使用结构化参数创建原生 skill：`name`、`description`、`risk`、`elyph`、可选 `go_source` 和 `timeout_ms`；不再让 LLM 拼完整 `SKILL.md` front matter。
- [x] `create_el_skill` 写入 `skills/go/<skill>/SKILL.elyph`，可选写入 `main.go` 并编译 Windows `.exe` / Linux 无扩展 binary；没有 Go 源码时允许纯 ELyph 文本 skill。
- [x] 创建前校验 ELyph 与结构化字段一致，创建后自动 reload skill registry。

### 外置 Skill 

- [x] 实现 `skills/py/<skill>/SKILL.md` 扫描，兼容网上常见 md + py skill；Python skill 作为文档型 skill 通过 `discover_tool` 返回 markdown 详情，脚本执行由隐藏包装工具 `python_skill_run` 统一使用 `uv run python`；外置 skill 可在 front matter 写 `risk`，缺省为 high。
- [x] 支持 `skills/py/<skill>/SKILL.elyph` 覆写 Python skill 的 Agent 可读说明；存在 `SKILL.elyph` 时 discover 优先返回 ELyph detail，不存在时回退 `SKILL.md`。
- [x] Go skill 只作为 ElBot 原生 skill 扫描：必须存在 `skills/go/<skill>/SKILL.elyph`，可选存在 binary；执行仍由隐藏包装工具 `go_skill_run` 选择 skill 并把 arguments JSON 写入 stdin。
- [x] 实现 `/tools reload` 热扫描，把新增/删除的外置 skill 同步到 Registry，并保留内置工具和隐藏包装工具。
- [x] 实现 `/tools uninstall <name>` / `/tools remove <name>` 删除外置工具，内置工具不可卸载。


### MCP

- [ ] 设计 MCP 配置格式。
- [ ] 实现 MCP 工具发现。
- [ ] 将 MCP 工具注册到 Tool Runtime。
- [ ] 支持 MCP 工具风险标注。

### 工具组

- [ ] 定义工具组配置。
- [ ] 支持按工具组 discover。
- [ ] 支持工具组权限。
- [ ] 支持子 Agent 限定工具组。
- [ ] 支持用户在聊天中@工具组直接把完整schema注入到prompt中

### 子 Agent

- [ ] 明确是否需要子 Agent。
- [ ] 设计子 Agent Prompt。
- [ ] 设计子 Agent 工具权限。
- [ ] 设计主 Agent 委派机制。
- [ ] 设计结果汇总机制。

### cron 插件

- [x] 实现 `cron` 主工具和隐藏 CRUD 工具；主工具默认注入，查询主工具时自动注入 `cron_create`、`cron_update`、`cron_delete`、`cron_disable`、`cron_get`、`cron_list`。
- [x] cron 仅超级管理员可用；查询工具为 medium 风险不触发确认，创建、更新、删除和停用为 high 风险并走确认流程。
- [x] 支持一次性任务和 5 字段周期 cron；一次性任务使用 `YYYY-MM-DD HH:MM:SS` 输入，当前按分钟级调度。
- [x] 支持 direct 直接消息触发和 llm 后台任务触发；LLM 触发要求 message 使用 `#task <name>` ELyph，运行时注入 ELyph 规则卡，并最终返回 JSON 再按完成状态决定是否通知用户。
- [x] 支持默认来源平台超级管理员私聊，以及广播到 app.toml 中 enabled=true 的所有平台超级管理员；CLI 固定可作为本地超级管理员目标。
- [x] cron session 使用 cron 标题并写入 `title_renamed=true`，避免后台自动命名覆盖；广播时复制 session 到目标平台便于各平台 `/resume` 查看。
- [x] 启动后补跑 missed 的 enabled 一次性 cron，失败写日志并提示 CLI。
- [ ] 可以指定模型，默认使用当前主模型。（暂时是固定主模型，以后配置在state.toml中）
- [ ] 周期cron也支持过期时间

### 多模态

- [x] 核心 LLM 请求改为 `MessageSegment`，支持文本、图片和文件占位。
- [x] OpenAI-compatible adapter 支持图片 `image_url` 请求；模型不支持视觉时自动回滚为文本描述，CLI 同一 Session 只提示一次。
- [x] QQ OneBot 解析图片 segment，并把语音、视频、普通文件统一作为 file segment 文本化处理。
- [ ] 后续支持语音、视频和普通文件的真实处理，不再仅文本化。
- [ ] 后续支持 CLI 图片输入，例如本地路径转图片 segment。

### cli和服务分离

- [ ] cli和服务分离
