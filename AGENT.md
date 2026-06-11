# ElBot 文档

## docs 文档速查

- `docs/plan.md`：总体规划，写愿景、原则、路线和待决策问题；不写实现细节。
- `docs/mvp.md`：MVP 范围，写首版交付边界、暂不实现内容和验收标准。
- `docs/architecture.md`：架构设计，写分层、模块职责、目录结构和核心流程。
- `docs/interfaces.md`：Go 内部接口草案，写 LLM、Tool、Agent、Storage、Platform、Security、Hook 等边界。
- `docs/tasks.md`：实现任务，按里程碑拆分任务、状态和后续事项。


## Go 文件速查

需要改代码时，先按下面的职责定位文件，避免全项目乱翻。

### 入口与启动

- `cmd/elbot/main.go`：程序入口；解析启动参数、创建根 context，并调用 `internal/app.Run`。
- `internal/app/app.go`：应用装配入口；加载配置、日志、SQLite、LLM、Agent、Tool、Platform、Hook、Output、Cron 等依赖并启动平台 runtime；Hook 插件错误按非致命处理，启动期通知会在 Agent 就绪后补发。

### Cron 与维护任务

- `internal/cron/manager.go`：中央 Cron Runtime；基于 `robfig/cron/v3` 调度持久化 job，提供 handler 注册、job upsert/disable/delete、启动加载、执行日志、运行状态更新、同 job 防并发和未启动 Stop 的安全返回。
- `internal/cron/service.go`：LLM 可编排 cron 服务；管理用户 cron metadata，支持 once/周期、direct/LLM 触发、missed once 补投递、LLM cron JSON 结果解析与失败通知。
- `internal/maintenance/maintenance.go`：系统维护任务；集中注册维护类 Cron，提供日志清理、过期 Session 清理和 artifact 沙盒清理。

### Agent 编排

- `internal/agent/core.go`：Agent 主结构与消息入口；装配核心依赖，分发 slash 命令和普通输入，处理 Actor/Scope fallback 与命令权限审计。
- `internal/agent/input.go`：普通输入分发辅助；处理 idle 过期、平台引用 fork、归档拒聊、LLM 打断追加、工具 pending 输入和高风险工具确认命令。
- `internal/agent/completion.go`：平台补全辅助；处理 CLI 命令、`/fork` 参数和风险确认阶段补全。TODO：后续抽成独立补全服务。
- `internal/agent/chat.go`：普通对话主流程；加载上下文、构建 Prompt、驱动 LLM/工具循环，最终平台输出先跑 `agent.turn.output.prepared`，流式输出用最终文本 replace，随后持久化本轮 transcript。
- `internal/agent/hooks.go`：Agent Hook 与输出接入；生成事件上下文，运行 Hook，提供 assistant 输出预处理，并把 Hook/工具输出意图交给 Output Manager 发送。
- `internal/agent/chat_llm.go`：LLM 调用与消息转换辅助；处理 Hook 后请求、流式响应、多模态转换、reasoning/usage/runtime 日志；流式最终 replace 由对话主流程在输出 Hook 后完成。
- `internal/agent/chat_tools.go`：工具执行与确认辅助；处理工具调用、风险确认、transcript、工具调用记录和 schema 注入。
- `internal/agent/risk_confirmation.go`：风险确认阶段命令定义与文案；统一生成 `/detail`、`/confirm`、`/confirmtool`、`/confirmall`、`/reject`、`/stop` 及别名的提示、补全和识别。
- `internal/agent/cron.go`：Agent 后台 cron runner；绕过 slash 命令解析，使用 cron 专用 session 静默运行 LLM，注入统一 sandbox 下的 `cron/` 工具 context（默认 `data/sandbox/cron`），要求最终 JSON 由 cron service 解析；cron session 写 `title_renamed=true` 避免自动命名覆盖。
- `internal/agent/cron_tools.go`：cron 工具确认特例；后台 cron shell 非 critical 自动确认，critical 直接回 tool message 提醒用相对路径/低风险命令且不等待用户。
- `internal/agent/prompt.go`：Soul Prompt Builder；加载 `SOUL.md`，合并常驻记忆、工具名称提示和压缩摘要，避免生成多条 system prompt。
- `internal/agent/tools.go`：Agent Tool Runtime 注入与命令依赖实现；维护工具 Registry、skill scanner，并把工具 schema provider 和工具名称 provider 接入 Prompt Builder。
- `internal/agent/tool_cache.go`：Session 级已发现工具 schema 缓存；discover 到的工具按 Session 保存，工具名持久化到 Session metadata，后续 work 请求用稳定顺序注入 top-level tools。
- `internal/agent/session_metadata.go`：Session metadata 编解码辅助；当前用于保存已 discover 工具名和最近一次 LLM usage，使 `/status` 在 `/resume` 后仍可显示最近 token 状态。
- `internal/agent/tool_transcript.go`：工具调用历史持久化辅助；保存 assistant tool_calls 与 tool result，提供 user 多模态 segments metadata helper，并在持久化 discover 结果时压缩 schema，避免未来上下文膨胀。
- `internal/agent/context.go`：Agent 上下文压缩依赖实现；维护 context 配置、压缩模型、ContextLoader、WindowResolver、Compressor、最近 usage 和待压缩标记，并提供 `/compact` 与 `/status` 所需能力；最近 usage 会写入 Session metadata 供恢复会话后展示。

- `internal/agent/model.go`：模型命令依赖实现；维护 chat/work、compact、naming 模型状态，支持模型切换、搜索、列表拉取，并写入 `state.toml`。

### Agent 内置命令

- `internal/agent/commands/register.go`：命令注册地基；定义 `Registrar`、`Module`、`Deps`、命令工厂/命令组、默认模块列表、额外模块注入入口和可选审计回调。未来内置插件可实现 `Module` 注册命令。
- `internal/agent/commands/help.go`：`/help` 命令；无参从 Router 生成命令列表，`/help <command>` 展示命令详细参数说明。
- `internal/agent/commands/model.go`：模型命令；实现 `/model`、`/checkmodel`、`/models`，支持 chat/work/compact/naming 模型查看与切换。
- `internal/agent/commands/compact.go`：`/compact` 命令；触发当前 Session 主动上下文压缩。
- `internal/agent/commands/session.go`：Session 命令；组合注册列表、生命周期、恢复、Fork、模式切换和 `/status` 等会话命令。
- `internal/agent/commands/request.go`：请求管理命令；实现 `/requests`、`/stop` 和 `/stopall`。
- `internal/agent/commands/log.go`：日志查看命令；实现 `/log`、`/audit`，支持常用过滤条件和 Debug 原始日志展示。
- `internal/agent/commands/tool.go`：工具命令；实现 `/tools` 查看已注册工具，并预留 external skill 的 reload/uninstall/remove 入口。

### 通用命令框架

- `internal/command/types.go`：命令系统基础类型；定义 `Info`、`Request`、`Result` 和 `Handler` interface；`Info.Help` 用于命令级详细帮助。
- `internal/command/handler.go`：函数式命令 handler 适配器；用 `NewFunc` 快速把函数包装成 `Handler`。
- `internal/command/router.go`：命令 Router；处理 prefix 解析、注册冲突检测、alias、分发、命令列表、命令详情查找、命令名补全和批量注册辅助；后续参数补全应抽到更模块化的补全服务。

### Request 与 Turn 运行态

- `internal/request/manager.go`：active request 管理器；登记 LLM、工具、压缩和子 Agent 请求，支持列表、按请求取消、按 Session 取消、全局取消、超时和完成清理。
- `internal/turn/manager.go`：当前 turn 协调器；记录 Session 运行阶段、原始用户输入、pending 追加消息、确认/取消 token、工具使用计数、compact 阶段和高风险工具确认等待状态；工具阶段普通输入不打断工具，会进入 pending 并在下一次 LLM 调用前注入。


### 配置与日志

配置约定：默认配置查找顺序为 `--config`、`ELBOT_CONFIG_FILE`、平台配置目录（Windows `%APPDATA%/ElBot/app.toml`；Linux XDG `~/.config/elbot/app.toml`）、最后回退源码目录 `config/app.toml`。静态配置在 `app.toml`，Provider 列表在同目录 `providers.toml`，运行时热切换状态在同目录 `state.toml`；Hook/插件配置固定放在同目录 `plugins/<plugin-name>.toml`，app 层不解析插件专属字段。Provider key 推荐用 `api_key_env`，读取优先级为系统环境变量 > 配置目录 `.env`。

- `internal/config/config.go`：配置模型与加载逻辑；按 CLI/env/平台目录/source fallback 解析 `app.toml`，读取并合并 app/provider/state 配置，解析相对路径和 `api_key_env`，包含 sandbox/artifact 与 S3/R2 预留配置。
- `internal/logging/logging.go`：日志地基；创建运行日志与审计日志的 `slog.Logger`，`Manager` 统一持有 `elbot-YYYY-MM-DD.log`、`audit-YYYY-MM-DD.log` 文件、暴露日志目录和可配置旧日志清理入口。
- `internal/logging/reader.go`：结构化文本日志读取器；解析 `slog.TextHandler` 输出，支持 `/log`、`/audit` 的时间、等级、字段、msg、latest message 文本和条数过滤，并放宽单行读取上限以支持较大的 Debug 请求体。
- `config/app.toml`：应用主配置；保存 storage、runtime、context、commands、tools、security、session cleanup、view、platform、soul 等静态设置。
- `config/providers.toml`：Provider 示例配置；保存供应商、模型列表、extra payload 和模型元信息，公开配置只写 `api_key_env` 不写真实 key。
- `config/state.toml`：热切换状态；保存当前模型选择和默认新会话模式。
- `config/SOUL.md`：System Prompt 来源文件；两种模式都从这里读取，不把工具发现、常驻记忆、时间或平台信息硬编码进 System Prompt。
- `config/plugins/`：Hook/插件配置目录。

### Hook Layer

- `internal/hook/hook.go`：Hook 基础包；定义事件点、已知点校验、payload、Handler/Manager、注册模块、匹配规则和按优先级串行执行的事件流水线。

- `internal/hook/builtin/register.go`：随程序发布的 Hook 插件注册入口；组合规则插件、表情插件和常驻记忆 Hook，app 层传配置目录、日志、安全策略、工具 Registry、resident memory store、审计和可选通知回调。
- `internal/hook/rules/rules.go`：TOML Rule Hook 插件；支持优先级、文本改写、output 和低风险工具调用，配置错误会记录日志并尽量通知消息区。
- `internal/hook/plugins/emoticon/emoticon.go`：表情 Hook；匹配 LLM 输出中的 `[[表情名]]`，随机发送本地图片并删除 token。
- `internal/hook/plugins/resident_memory/resident_memory.go`：常驻记忆 Hook；每 turn 注入当前 platform + actor 的常驻记忆和临时用户名。

### Hook/插件约定

- 插件配置固定在 `config/plugins/<plugin-name>.toml`，相对 `config/app.toml`。
- Hook/Tool/插件不要直接发平台消息，统一返回 `output.Output` 意图，由 Agent 交给 Output Manager。

### Output Layer

- `internal/output/output.go`：平台无关输出意图与发送管理；定义 text/image/file/at/emoticon 等输出类型、fallback 文本和统一发送入口。

### Security Layer

- `internal/security/security.go`：安全层地基；定义 `Actor`、`Role`、`RiskLevel` 和 `Policy`，按平台超级管理员 ID 识别角色，按风险阈值判断工具可用性与超级管理员确认需求。
- `internal/security/context.go`：在 context 中传递当前 Actor 与 Security Policy，供工具发现和执行风险评估使用。

### Context Management

- `internal/contextmgr/contextmgr.go`：上下文管理地基；实现 ContextLoader、Fork 分支上下文加载、context window 解析、厂商 usage 状态格式化和压缩器；默认窗口来自 provider 模型元信息。

### LLM

- `internal/llm/llm.go`：LLM 抽象类型；定义带稳定 JSON descriptor 的 `MessageSegment`（text/image/file）、消息、请求、流式 chunk、reasoning delta、usage、模型 metadata、tool call、tool schema 和 `LLM` interface。
- `internal/llm/segment.go`：内部统一 MessageSegment 工具；区分 `SegmentsTextOnly` 与 `SegmentsContentText`，提供 text segment 原位 prepend/append/regex replace、图片/文件提取、latest user 和 system message helper。
- `internal/llm/openai/openai.go`：OpenAI-compatible adapter；实现流式 chat completions、多模态转换、模型列表、SSE、usage/reasoning/tool call delta 和错误解析。


### Tool Runtime

- `internal/tool/tool.go`：Tool Runtime 核心类型与 Registry；管理工具注册、查询、schema、权限、风险评估和执行结果结构。
- `internal/tool/sandbox.go`：工具执行轻量 sandbox context；传递统一 sandbox root、当前工作目录、artifact 目录和 cron 后台状态，只随本次 context 传播，不写入 Session。
- `internal/tool/builder.go`：Go Tool Builder；用于声明工具描述、风险、隐藏、superadmin-only、依赖和常用参数 schema，减少内置工具与包装工具手写 JSON schema 的成本。
- `internal/tool/discover.go`：`discover_tool` 内置工具；无参列出可见工具/skill 简介，有 `name`/`names` 时普通工具仅返回“已发现工具”文本并把完整 schema 留在结构化 Data 供 Agent 注入 top-level tools，外置 skill 返回 markdown/ELyph detail；查询 py/go skill 会通过内部 metadata 激活隐藏包装工具 `python_skill_run`/`go_skill_run`。
- `internal/tool/provider.go`：Tool Runtime 到 Agent Prompt/LLM schema 的 provider 适配；work 模式注入 `discover_tool` 和当前 Actor 可用且未隐藏的工具名称，chat 模式不注入。
- `internal/tool/executor.go`：工具执行器；把模型产生的 `llm.ToolCallRequest` 转换为 Tool Runtime 调用，执行前按 Actor/Policy 做风险等级兜底校验，并把结果转换为 LLM tool message。

- `internal/tool/builtin/runtime.go`：内置工具 Runtime；集中创建 Tool Registry、常驻记忆 store、Skill Manager、Artifact Manager 和内置工具私有路径，让 app 层不关心具体内置工具清单。
- `internal/tool/builtin/register.go`：内置工具注册细节；由 builtin Runtime 调用，统一注册 `discover_tool`、常驻记忆、长期记忆、cron、`send_file`、web 搜索/提取、shell、skill 包装工具和 Go 元 skill。
- `internal/tool/builtin/artifact.go`：artifact 文件暂存 helper；sandbox 内文件直接发送，外部文件复制到统一 sandbox 的 `artifact/` 子目录，做大小、文件名、MIME 和 Windows/MSYS 路径处理，未来可接 S3/R2。
- `internal/tool/builtin/send_file.go`：内置发文件工具；仅超管可用，支持 `path`/`file` 参数，相对路径在 cron 中按 cron sandbox 解析，外部文件确认/自动处理后通过 output.File 发送。
- `internal/tool/builtin/long_memory.go`：全局长期记忆工具组；可见入口 `long_memory` 依赖隐藏 CRUD/Search 工具，仅超管可用；Markdown 文件是源数据，SQLite FTS 是可重建索引，搜索/分类前会轻量同步并提示手改格式损坏文件。
- `internal/tool/builtin/cron.go`：内置 cron 工具组；可见主工具 `cron` 依赖隐藏 CRUD 工具，查询为 medium 风险，增删改停用为 high 风险，全部仅超级管理员可用；`cron_list` 默认隐藏已完成 cron，传 `include_completed=true` 才显示历史完成项。
- `internal/tool/builtin/env.go`：内置工具环境变量读取 helper；优先读 OS env，缺失时读取配置目录 `.env`，用于 Tavily/Jina API key。
- `internal/tool/builtin/web_search.go`：Tavily 搜索工具；返回 answer、来源链接和摘要，并依赖 `web_extract`。
- `internal/tool/builtin/web_extract.go`：Jina Reader/标准库网页提取工具；支持代理、分段读取和进程内缓存。
- `internal/tool/builtin/shell.go`：内置 shell 工具；接口保留通用 `cmd`，可执行任意 shell 命令，调用前通过风险评估与高风险确认流程拦截；cron sandbox context 下会创建目录并把 shell cwd 固定到 sandbox。
- `internal/tool/builtin/shell_risk.go`：shell/bash 命令风险分类器；使用 `mvdan.cc/sh/v3/syntax` 解析 AST，识别管道、重定向、命令替换、动态命令、删除、提权、下载即执行等风险并返回风险原因。
- `internal/tool/builtin/shell_sandbox.go`：cron shell 轻沙盒 AST 校验；检查重定向和常见路径参数中的绝对路径、`..` 逃逸、动态路径与 `cd`，违规时把风险提升为 critical。
- `internal/elyph/`：ELyph Task Notation 语言层；提供规则卡、AST/diagnostic、parser/linter，供原生 skill 创建、扫描和 LLM cron 任务复用。
- `internal/tool/skill/parser.go`：通用 `SKILL.md` 解析器；兼容 Python 外置 skill 常见 YAML front matter 的 `name`、`description`、`when_to_use`、`risk`，并提供目录名和正文首段 fallback；未写 risk 时默认 high。
- `internal/tool/skill/catalog.go`：skill catalog；记录 py/go skill 的名称、详情格式、风险、根目录和 Go binary 路径，供隐藏包装工具按名称查找。
- `internal/tool/skill/creator.go`：`create_el_skill` 内置元工具；用结构化参数 `name/description/risk/elyph/go_source` 创建 ElBot 原生 skill，写入 `SKILL.elyph`，可选写入 `main.go` 并编译，创建前用 ELyph parser/linter 校验，成功后自动 reload；不再要求 LLM 拼 `SKILL.md` front matter，未提供源码时创建纯 ELyph 文本 skill。
- `internal/tool/skill/elyph_modifier.go`：`read_el_skill`/`modify_el_skill` 内置元工具；按行读取 `SKILL.elyph`，并支持完整 `content` 覆盖或 1-based 行 patch 修改，写入前严格校验 ELyph 语法并 reload。

- `internal/tool/skill/descriptor.go`：skill 描述对象；让 py/go skill 可被 `discover_tool` 查到详情，ELyph skill detail 会按需前置短规则卡，Markdown skill 不注入；skill 本体不作为可直接调用 schema 暴露。
- `internal/tool/skill/scanner.go`：skill 文件系统扫描与 reload；默认根目录为 Windows `%APPDATA%/ElBot/skills` 或 Linux XDG data `elbot/skills`；Go skill 必须使用 `go/<skill>/SKILL.elyph`，可选 binary；Python skill 优先读 `SKILL.elyph` 覆写 Agent 可读说明，否则回退 `SKILL.md`；同步新增/删除 skill 并更新 catalog。
- `internal/tool/skill/runner.go`：隐藏包装工具实现；`python_skill_run` 固定在 py skill 目录用 `uv run python` 执行脚本，`go_skill_run` 选择 Go skill binary，读取 `skill_name` 和可选 `timeout_ms` 后把其余顶层字段作为业务 JSON 写入 stdin；包装工具本身隐藏，执行风险按目标 skill 的 `risk` 评估。

### Tool 约定

- 风险等级只用于内部权限与确认，不暴露给 LLM。
- 工具结果回灌 LLM 使用 `Result.Content` 或 typed `Result.Segments`；图片/文件必须显式返回 segment。
- `Result.Data` 仅供内部结构化消费，不进入 tool message。
- Tool/插件发送消息统一返回 `Result.Outputs`，由 Agent/Output Manager 发送。

### 平台适配

- `internal/platform/platform.go`：平台抽象；定义 `PlatformAdapter`、`PlatformHandler`、统一 `SendChat`/`SendNotice` 的 message sender、发送 receipt、平台 `MessageSegment`（text/image/file）和每条入站消息的 Actor/Scope/发送目标上下文；上下文可携带平台解析出的 fork 来源消息与多模态消息段。
- `internal/platform/config.go`：平台配置辅助；把 `app.toml` 中 `[platform.<name>]` 原始 section 解码给适配器自有 Config，并提供关键词前缀剥离 helper。
- `internal/platform/builtin/builtin.go`：内置平台装配；创建 CLI，并把各平台 raw config 交给对应适配器工厂解析，避免全局 config 知道具体平台字段。
- `internal/platform/cli/cli.go`：CLI 平台实现；非 TTY 下读取 stdin，交互式终端下启动 Bubble Tea TUI，处理 `/exit`、转发输入给 Agent；实现统一 `SendChat`/`SendNotice`，聊天进主区，通知进 TUI 通知区或非 TTY `[notice]` fallback；当前 CLI 退出会结束前台应用，后续服务化可调整生命周期接口。
- `internal/platform/qq-onebot/`：QQ OneBot v11 正向 WebSocket 适配；处理私聊/群聊文本、图片、@、reply、关键词触发、引用 fork、消息映射和富输出发送。引用 bot 历史消息会按本地映射/get_msg 解析，必要时自动 fork。


- `internal/platform/cli/tui.go`：Bubble Tea TUI 地基；提供聊天/通知/输入区、异步提交、补全、历史和滚动能力。

### Session 服务


- `internal/session/session.go`：Session 业务服务；管理当前 session、创建、恢复、Fork、分页列表、状态、模式切换、归档/置顶/删除、过期清理、平台隔离、按 `cron:` scope 识别的同平台 cron session 可见性和 CLI 全平台全 owner 列表可见性。

### Storage 抽象

- `internal/storage/storage.go`：存储层领域模型和接口；定义 Session、Message、ContextSummary、PlatformMessageMap、ToolCallRecord、CronJob、查询请求、summary、Store 和 repository interfaces，包含 Fork 上下文、Session 归档/同平台 cron 可见性过滤、过期清理、工具调用统计与中央 Cron 持久化所需接口。
- `internal/storage/id.go`：ID 生成；生成 UUID v4 风格字符串。
- `internal/storage/time.go`：时间工具；统一 RFC3339Nano 格式化和解析。

### SQLite 实现

- `internal/storage/sqlite/context_summary_repository.go`：ContextSummary repository SQLite 实现；写入压缩 checkpoint，查询 Session 最新摘要和 Fork 截止消息适用摘要。
- `internal/storage/sqlite/store.go`：SQLite store 装配；打开数据库、启用 foreign keys、执行 migration，并暴露 session/message/context summary/tool call/cron job repository。
- `internal/storage/sqlite/migrations.go`：SQLite migration；维护 migration 列表、已应用版本查询和 migration 应用流程，包含 Fork session、tool call 与 cron job 表/索引。
- `internal/storage/sqlite/session_repository.go`：Session repository SQLite 实现；负责 session CRUD、列表查询、归档过滤、按 `cron:` scope 查询同平台 cron session、pinned 置顶排序、过期硬删除、平台隔离查询条件和 summary 扫描。
- `internal/storage/sqlite/message_repository.go`：Message repository SQLite 实现；负责消息追加、查询、按 session 列表、按 checkpoint 后列表、Fork 截止范围列表、平台消息映射和反查。
- `internal/storage/sqlite/cron_job_repository.go`：CronJob repository SQLite 实现；负责中央 Cron job 的 upsert、列表、按名称查询、禁用、删除和最近运行状态更新；upsert 在配置未变化时跳过写库，减少启动期无意义 SQLite 写入。
- `internal/storage/sqlite/tool_call_repository.go`：ToolCallRecord repository SQLite 实现；负责每次工具调用记录写入，并按 Session 聚合工具调用次数供 `/status` 展示。


## 推荐阅读顺序

1. `plan.md`
2. `mvp.md`
3. `architecture.md`
4. `interfaces.md`
5. `tasks.md`


**注**：
1. 每次更新代码后，若有go文件修改或者新增，务必在此文档更新说明.注意不能让agent.md文档无脑变长，可以适当精简，每次只精简这次知道的地方，其他的先不管，但是也别精简到自己都看不懂了，这是方便你快速了解项目看的，能理解就够，不用过于详细。
2. 写代码时，暂时不实现的，但是未来可能会有的，要考虑留出接口或者搭好地基，然后注释写个TODO，免得后期一改又全部大改。
3. 开发中不应该考虑兼容，导致代码复杂或者冗余。
4. 整体语言用简体中文。

