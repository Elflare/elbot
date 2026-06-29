# ElBot 文档

## docs 文档速查

- `docs/README.md`：用户文档首页，写阅读顺序和文档维护约定。
- `docs/getting-started.md`：快速开始，写环境、配置 API Key、启动 CLI 和首次对话。
- `docs/configuration.md`：配置说明，写配置文件职责、路径规则、Provider、密钥、数据目录和平台配置。
- `docs/commands.md`：命令速查，写常用 slash 命令、参数和示例。
- `docs/concepts.md`：核心概念，写 Chat/Work、工具发现、Session、Hook、Cron、Skill、安全等用户需要理解的概念。
- `docs/hooks.md`：Hook 文档，写规则 Hook 配置、action 类型、segments 多段输出、exec stdout 模式、角色分区、控制字段和表情提取示例。
- `docs/elnis.md`：Elnis 监听枢纽介绍，写 Elnis、Elwisp、Elvena、事件模式和能力边界。
- `docs/elnis-usage.md`：Elnis 配置与使用，写启用配置、curl 示例、Elvena 字段和投递安全边界。
- `docs.en/` / `README.md` / `CHANGLOG.en.md`：英文用户文档镜像，由 GitHub Actions 自动翻译生成；不要手动修改。
- `scripts/translate_docs.py`：用户文档增量翻译脚本。

## devdocs 开发文档速查

- `devdocs/` 是开发资料，只给维护者/Agent 看，不走自动翻译。
- `devdocs/tasks.md`：实现任务，按里程碑拆分任务、状态和后续事项。
- `devdocs/elnis-elwisp.md`：Elnis/Elwisp 监听枢纽架构、Elvena 协议草案和分阶段实现规划。

## Go 文件速查

需要改代码时，先按下面的职责定位文件，避免全项目乱翻。

### 入口与启动

- `cmd/elbot/main.go`：程序入口；调用 `internal/launcher` 解析命令，创建根 context；`elbot cli` 或 `elbot -c` 进入远程 CLI 客户端，其余模式调用 `internal/app.Run`。
- `internal/launcher/cli.go`：手写解析 `run`、`cli`、`service run`、`completion` 和远程 CLI `-c/--client`，并生成 bash/zsh/fish/nushell/powershell 补全。
- `internal/app/app.go`：应用装配入口；加载配置、日志、SQLite、LLM、Agent、Tool、Platform、Hook、Output、Cron 等依赖，并按运行模式启动平台 runtime/Cron。
- `internal/app/cli_client.go`：远程 CLI 客户端启动入口；加载配置，解析 CLI client profile/token，并连接服务端复用 TUI。
- `internal/app/service_marker.go`：Linux service pid marker；供默认启动判断是否切到 CLI-only。

### Cron 与维护任务

- `internal/cron/manager.go`：中央 Cron Runtime；基于 `robfig/cron/v3` 调度持久化 job，提供 handler 注册、job upsert/disable/delete、启动加载、执行日志、运行状态更新、同 job 防并发和未启动 Stop 的安全返回。
- `internal/cron/service.go`：LLM 可编排 cron 服务。
- `internal/maintenance/maintenance.go`：系统维护任务；集中注册维护类 Cron，提供日志、Session、sandbox 和聊天历史清理。

### Agent 编排

- `internal/agent/core.go`：Agent 主结构与消息入口；装配核心依赖, 分发 slash 命令和普通输入，处理 context Actor/Scope fallback 与命令权限审计。
- `internal/agent/input.go`：普通输入分发辅助；处理 idle 过期、平台引用 fork、归档拒聊、`@tool:<name>` 工具预载、LLM 打断追加、工具 pending 输入和高风险工具确认命令。
- `internal/agent/completion.go`：平台补全辅助；暴露中央补全服务，兼容旧平台的文本补全入口。

- `internal/agent/chat.go`：普通对话主流程；加载上下文、构建 Prompt、驱动 LLM/工具循环；user 与已完成工具 transcript 会阶段性落库，最终平台输出先跑 `agent.turn.output.prepared`，流式输出用最终文本 replace，正常完成后保存最终 assistant。
- `internal/agent/hooks.go`：Agent Hook 与输出接入；生成事件上下文，运行 Hook，提供 assistant 输出预处理，并把 Hook/工具输出意图交给 Output Manager 发送。
- `internal/agent/chat_llm.go`：LLM 调用与消息转换辅助；处理 Hook 后请求、流式响应错误通知、多模态转换、reasoning/usage/runtime 日志；流式最终 replace 由对话主流程在输出 Hook 后完成。
- `internal/agent/turn_output.go`：Agent turn 输出适配；区分前台/后台的流式输出、中间输出、工具预览、Notice、reasoning 和 runtime status 发布。
- `internal/agent/chat_tools.go`：工具执行与确认辅助；处理工具调用、风险确认、transcript、工具调用记录和 schema 注入。
- `internal/agent/request_context.go`：Agent request context helper；在当前 turn context 中传递父 request ID，供工具 request 建立父子关系。
- `internal/agent/risk_confirmation.go`：风险确认阶段命令定义与文案；统一生成 `/detail`、`/confirm`、`/confirmtool`、`/confirmall`、`/reject`、`/stop` 及别名的提示、补全和识别。
- `internal/agent/cron.go`：Agent 通用后台 runner；绕过 slash 命令解析，使用 discard sender 静默运行后台 LLM，按 background kind 注入 sandbox、按 session_mode 创建/续跑后台 Session、预加载工具并写入 metadata；保留 `RunCronMessage` 薄适配。
- `internal/agent/cron_tools.go`：后台工具确认特例；cron/Elnis sandbox shell 非 critical 自动确认，critical 直接回 tool message 提醒用相对路径/低风险命令且不等待用户。
- `internal/agent/prompt.go`：Prompt Builder；通过 System Prompt Manager 生成单条 system prompt，并处理历史消息、工具 transcript、多模态 metadata 和压缩摘要。
- `internal/agent/system_prompt.go`：System Prompt Manager；按优先级收集 Soul、工具名称和 tag prompt 等内建 system prompt 片段。
- `internal/agent/system_prompt_sources.go`：内建 system prompt source；提供 Soul 和工具名称提示片段。
- `internal/agent/tools.go`：Agent 工具运行态与命令依赖适配；集中维护 ToolRun manager、tool Registry、skill scanner、工具配置和 tool tag 配置入口。
- `internal/agent/toolrun_adapter.go`：Agent 到 ToolRun 的能力适配；提供 Hook、Request、确认、输出、审计、工具记录和 session 缓存桥接。
- `internal/agent/toolrun_prompt_provider.go`：ToolRun 到 Prompt Builder 的工具名称/schema provider 适配。
- `internal/agent/tool_cache.go`：Session 级已发现工具 schema 缓存；discover 或有效 `@tool:` 预载到的工具按 Session 保存，工具名和激活 tag 持久化到 Session metadata，后续 work 请求用稳定顺序注入 top-level tools。
- `internal/agent/tool_directive.go`：聊天内联 `@tool:<name-or-tag>`/`@skill:<name>` 预处理；工具/tag 持久化注入 schema，Skill 将文档内容加入本轮用户消息并注入运行 wrapper，不存在/不可用值保留为普通文本并提示。
- `internal/agent/tool_tag_config.go`：工具 tag 配置 source；按文件状态缓存读取 `tool_tags.toml`，合并内置 tag 与配置 tag，并为已激活 tag 提供 system prompt 片段。

- `internal/agent/status.go`：Agent 运行状态发布 helper；保存每个 Session 最新 runtime snapshot，并通过平台可选接口推送给 CLI TUI 等状态展示层。

- `internal/agent/session_metadata.go`：Session metadata 编解码辅助；当前用于保存已 discover 工具名、已激活 tool tag 和最近一次 LLM usage，使 `/status` 在 `/resume` 后仍可显示最近 token 状态。
- `internal/agent/tool_transcript.go`：工具调用历史持久化辅助；保存 assistant tool_calls 与 tool result，提供 user 多模态 segments metadata 和 turn message 落库 helper，并在持久化 discover 结果时压缩 schema，避免未来上下文膨胀。
- `internal/agent/context.go`：Agent 上下文压缩依赖实现；维护 context 配置、压缩模型、ContextLoader、WindowResolver、Compressor、最近 usage 和待压缩标记，并提供 `/compact` 与 `/status` 所需能力；最近 usage 会写入 Session metadata 供恢复会话后展示。
- `internal/agent/model.go`：模型命令依赖实现；集中维护模型运行态、provider client 缓存、`/models` 运行期列表缓存、模型切换和 `state.toml` 写入；`/model` 默认按消息上下文对应 Session mode 选择目标，但设置全平台共享。

### Agent 内置命令

- `internal/agent/commands/register.go`：命令注册地基；定义 `Registrar`、`Module`、`Deps`、命令工厂/命令组、默认模块列表、额外模块注入入口和可选审计回调。未来内置插件可实现 `Module` 注册命令。
- `internal/agent/commands/help.go`：`/help` 命令；无参从 Router 生成命令列表，`/help <command>` 展示命令详细参数说明。
- `internal/agent/commands/model.go`：模型命令；实现 `/model`、`/checkmodel`、`/models`，支持 chat/work/elwisp/compact/naming 模型查看、切换和 `/model` 参数补全，`/models --fresh` 可强制刷新模型列表缓存。
- `internal/agent/commands/compact.go`：`/compact` 命令；触发当前 Session 主动上下文压缩。
- `internal/agent/commands/session.go`：Session 命令；组合注册列表、生命周期、恢复、Fork、模式切换等会话命令。
- `internal/agent/commands/request.go`：请求管理命令；实现 `/requests`、`/stop` 和 `/stopall`。
- `internal/agent/commands/log.go`：日志查看命令；实现 `/log`、`/audit`、`/elwisp`，支持常用过滤条件和 Debug 原始日志展示。
- `internal/agent/commands/tool.go`：工具命令；实现 `/tools` 查看已注册工具，并预留 external skill 的 reload/uninstall/remove 入口。
- `internal/agent/commands/hook.go`：Hook 命令；实现 `/hooks` 列出已注册 Hook、查看某个 Hook 详情和 `/hooks reload` 热重载全部 Hook。

### 通用命令框架

- `internal/command/types.go`：命令系统基础类型；定义 `Info`、`Request`、`Result`、`Handler` 和可选参数补全接口；`Info.Help` 用于命令级详细帮助。

- `internal/command/handler.go`：函数式命令 handler 适配器；用 `NewFunc` 快速把函数包装成 `Handler`。
- `internal/command/router.go`：命令 Router；处理 prefix 解析、注册冲突检测、alias、分发、命令列表、命令详情/handler 查找和基础命令名补全。
- `internal/completion/`：平台补全服务；组合 Router、命令参数、风险确认、`/fork` message ID 和 `@tool:` 工具名等补全 source，支持局部替换，app 层注入到支持补全的平台。
- `internal/directive/`：聊天内联指令解析小包；当前提供 `@tool:`/`@skill:` 解析、剥离和补全 token 规则，共享给 Agent 预处理和补全 source。

### Request 与 Turn 运行态

- `internal/request/manager.go`：active request 管理器；登记 turn、LLM、工具、压缩和子 Agent 请求，记录父子 request 关系，支持列表、查询、按请求取消、按 Session 取消、全局取消、超时和完成清理。

- `internal/runtime/status.go`：运行状态快照与格式化 helper；描述阶段、usage等结构化状态，供 CLI 状态栏和未来日志/命令复用。
- `internal/turn/manager.go`：当前 turn 协调器；记录 Session 运行阶段、原始用户输入、pending 追加消息、确认/取消 token、工具使用计数、compact 阶段和高风险工具确认等待状态；工具阶段普通输入不打断工具，会进入 pending 并在下一次 LLM 调用前注入；请求异常结束时清理非确认追加状态，避免残留 tool pending。


### 配置与日志

配置约定：默认配置查找顺序为 `--config`、`ELBOT_CONFIG_FILE`、平台配置目录（Windows `%APPDATA%/Roaming/ElBot/app.toml`；Linux XDG `~/.config/elbot/app.toml`）；平台配置不存在时由内置 assets 自动生成，已有配置不覆盖。静态配置在 `app.toml`，Provider 列表在同目录 `providers.toml`，运行时热切换状态在同目录 `state.toml`，工具 tag 配置在同目录 `tool_tags.toml`；用户可编辑资产集中在配置目录：`memories.toml`、`long_memory/`、`skills/`、`plugins/`，SQLite/logs/sandbox 等运行数据仍按各自配置或默认数据目录存放。Hook/插件配置固定放在同目录 `plugins/<plugin-name>.toml`，规则 Hook 使用 `plugins/hooks.toml`，app 层不解析插件专属字段。Provider key 推荐用 `api_key_env`，读取优先级为系统环境变量 > 配置目录 `.env`。

- `internal/config/config.go`：配置模型与加载逻辑；按 CLI/env/平台目录解析 `app.toml`，读取并合并 app/provider/state/tool_tags 配置，解析相对路径和 `api_key_env`，包含 LLM 流式超时、整轮 response timeout、重试、sandbox、Elnis 配置；Provider 支持 proxy，`models` 补充模型列表，`model_configs` 配置模型的 context_window 和 extra_payload。

- `internal/config/assets.go`：首次运行默认配置资产。


- `internal/elvena/`：Elvena 公共协议层；定义 v2/v3 request/response、Origin、Dispatcher/Bus、direct 输出转换和 calls 能力映射，供 Elnis HTTP 与 Hook exec 共用。
- `internal/elnis/types.go`：Elnis/Elvena 协议类型别名；复用 `internal/elvena` 请求、响应、事件模式、状态和 calls 类型。
- `internal/elnis/service.go`：Elnis 接收服务主入口；实现 HTTP token Handle 与 Elvena Dispatcher，负责授权、去重后分发 record/direct/llm。
- `internal/elnis/prepare.go`：Elnis 事件准备；校验 v2/v3 请求、session_mode、规范化 target/tool/calls、生成事件 key/hash 和持久化字段。
- `internal/elnis/auth.go`：Elnis token、Elwisp 和工具授权。
- `internal/elnis/targets.go`：Elnis 投递目标解析、禁用规则和 enabled platform 展开。
- `internal/elnis/direct.go`：Elnis direct 分发；发送 content/segments，执行 raw/capability calls，并记录回执映射。
- `internal/elnis/llm.go`：Elnis LLM 后台任务、session_mode 传递、结果重试、报告投递和模型选择。
- `internal/elnis/segments.go`：Elnis segment 下载、URL/data URI 校验和 LLM segment 转换。
- `internal/elnis/store.go`：Elnis 重复事件、完成状态和事件日志属性 helper。
- `internal/elnis/background.go`：Elnis 后台 actor、sandbox subdir 和平台辅助 helper。
- `internal/elnis/http.go`：Elnis HTTP runtime；提供 `POST /elvena/v2/events` 和 `GET /healthz`，支持 body 限制、token 提取、JSON 响应和 LLM 事件队列 worker。


- `internal/logging/logging.go`：日志地基；创建运行日志、审计日志和 Elnis 日志的 `slog.Logger`，`Manager` 统一持有按日期懒轮转的 `elbot-YYYY-MM-DD.log`、`audit-YYYY-MM-DD.log`、`elnis-YYYY-MM-DD.log` writer，暴露日志目录和可配置旧日志清理入口。
- `internal/logging/reader.go`：结构化文本日志读取器；解析 `slog.TextHandler` 输出，支持 `/log`、`/audit` 的时间、等级、字段、msg、latest message 文本和条数过滤，并放宽单行读取上限以支持较大的 Debug 请求体。

### Hook Layer

- `internal/hook/hook.go`：Hook 基础包；定义事件点、已知点校验、payload、控制字段、正则捕获上下文、Handler/Manager、注册模块、匹配规则和按优先级串行执行的事件流水线；`Registration` 携带 `Detail` 供查看，`DefaultManager` 提供 `List()`/`Reset()` 支持 `/hooks` 命令列出与热重载。

- `internal/hook/builtin/register.go`：随程序发布的 Hook 插件注册入口；组合规则插件和常驻记忆 Hook，app 层传配置目录、日志、安全策略、工具 Registry、resident memory store、Elvena Dispatcher、审计和可选通知回调。
- `internal/hook/rules/rules.go`：TOML Rule Hook 插件，读取 `plugins/hooks.toml`，支持角色分区、输出控制、文本/发送（含 segments 多段输出）/工具/exec action、模板渲染和字段覆写。
- `internal/hook/rules/exec.go`：规则 Hook exec action；默认在 `plugins/` 目录执行 shell 命令，支持 stdin JSON payload、stdout capture/send/outputs/elvena/ignore。
- `internal/hook/plugins/resident_memory/resident_memory.go`：常驻记忆 Hook；每 turn 注入当前 platform + actor 的常驻记忆和临时用户名。

### Hook/插件约定

- 插件配置固定在配置目录的 `plugins/<plugin-name>.toml`，相对当前 `app.toml`。
- Hook/Tool/插件不要直接发平台消息，统一返回 `output.Output` 意图，由 Agent 交给 Output Manager。

### Output Layer

- `internal/background/`：后台 LLM 执行公共类型与结果 helper；定义 cron/Elnis 共用 `RunRequest`、`RunResult`、background kind、session_mode、最终 JSON 结果解析/格式重试文案和 report segment 输出构建。
- `internal/delivery/delivery.go`：平台无关输出意图与发送管理；统一定义 text/image/file/at/reply/emoticon 等输出类型、fallback 文本、delivery timing 元数据，以及发送回执、流式发送和发送入口。

### Security Layer

- `internal/security/security.go`：安全层地基；定义 `Actor`、ElBot 内部 `Role`、平台群身份 `GroupRole`、`RiskLevel` 和 `Policy`，按平台超级管理员 ID 识别角色，按风险阈值判断工具可用性与超级管理员确认需求。

- `internal/security/context.go`：在 context 中传递当前 Actor 与 Security Policy，供工具发现和执行风险评估使用。

### Context Management

- `internal/contextmgr/contextmgr.go`：上下文管理地基；实现 ContextLoader、Fork 分支上下文加载、context window 解析、厂商 usage 状态格式化和压缩器；默认窗口来自 provider 模型元信息。

### LLM

- `internal/llm/llm.go`：LLM 抽象类型；定义带稳定 JSON descriptor 的 `MessageSegment`（text/image/file）、消息、请求、流式 chunk、reasoning delta、usage、模型 metadata、tool call、tool schema 和 `LLM` interface。
- `internal/llm/segment.go`：内部统一 MessageSegment 工具；区分 `SegmentsTextOnly` 与 `SegmentsContentText`，提供 text segment 原位 prepend/append/regex replace、图片/文件提取、latest user 和 system message helper。
- `internal/llm/openai/openai.go`：OpenAI-compatible adapter；实现流式 chat completions、多模态转换、模型列表、SSE、usage/reasoning/tool call delta、首包/idle 超时、重试通知、缺失 `[DONE]` 的断流检测和错误解析。



### Tool Runtime

- `internal/tool/tool.go`：Tool Runtime 核心类型与 Registry；管理工具注册、查询、schema、权限、风险评估、风险确认详情、用户侧 tags 和执行结果结构。
- `internal/tool/detail.go`：工具/Skill detail 渲染 helper；按结构化格式去重共享规则卡并拼接 detail 内容。
- `internal/tool/sandbox.go`：工具执行轻量 sandbox context；传递统一 sandbox root、当前工作目录和后台运行 kind，提供后台相对路径解析，只随本次 context 传播，不写入 Session。

- `internal/tool/builder.go`：Go Tool Builder；用于声明工具描述、风险、隐藏、superadmin-only、用户侧 tags、依赖和常用参数 schema，Object 参数默认允许任意 JSON 字段，减少内置工具与包装工具手写 schema 的成本。
- `internal/tool/discover.go`：`discover_tool` 内置工具；无参列出可见工具/skill 简介，有 `name`/`names` 时普通工具仅返回“已发现工具”文本并把完整 schema 留在结构化 Data 供 Agent 注入 top-level tools，外置 AgentSkill/Go skill 返回 markdown/ELyph detail 且 ELyph 规则卡按格式去重；查询 AgentSkill/Go skill 会通过内部 metadata 激活隐藏包装工具 `python_skill_run`/`go_skill_run`。
- `internal/tool/provider.go`：Tool Runtime 到 Agent Prompt/LLM schema 的旧 provider 适配；保留给显式外部 provider 兼容，默认工具视图由 `internal/toolrun` 提供。
- `internal/toolrun/`：工具调用中间层；维护 session 工具缓存、native/Elwisp 工具视图、命名解析、权限风险确认、工具自定义确认详情、tool call 生命周期编排、批量工具预览和失效提示；后台 session 不注入默认 `discover_tool`，后台 shell schema 使用相对路径，Elwisp 外部工具通过 HTTP JSON POST 执行。
- `internal/tool/executor.go`：工具执行器；把模型产生的 `llm.ToolCallRequest` 转换为 Tool Runtime 调用，执行前按 Actor/Policy 做风险等级兜底校验，并把结果转换为 LLM tool message。
- `internal/tool/builtin/runtime.go`：内置工具 Runtime；集中创建 Tool Registry、常驻记忆 store、Skill Manager、文件发送 helper 和内置工具私有路径；`memories.toml`、`long_memory/`、`skills/` 默认在配置目录下。
- `internal/tool/builtin/register.go`：内置工具注册细节；由 builtin Runtime 调用，统一注册 `discover_tool`、常驻记忆、长期记忆、cron、`send_file`、聊天历史、web 搜索/提取、shell、`elwisp_creator`、skill 包装工具和 Go 元 skill。
- `internal/tool/builtin/file_manager.go`：本地文件发送准备 helper；解析本地路径、文件名、MIME 和 Windows/MSYS 路径，不复制文件。
- `internal/tool/builtin/send_file.go`：内置发文件工具；仅超管可用，支持 `path`/`file` 参数，后台和前台都直接发送解析后的本地文件路径。
- `internal/tool/builtin/chat_history.go`：内置聊天历史工具；按当前 platform/scope 搜索、查看上下文和引用回复平台聊天记录，用户侧 tag 为 `chat`。
- `internal/tool/builtin/long_memory.go`：全局长期记忆工具组；可见入口 `long_memory` 依赖隐藏的 `long_memory_search`/`long_memory_write`，仅超管可用；Markdown 文件是源数据，SQLite FTS 是可重建索引，搜索/分类前会轻量同步并提示手改格式损坏文件。
- `internal/tool/builtin/cron.go`：内置 cron 工具组；可见主工具 `cron` 依赖隐藏的 `cron_query`/`cron_write`，查询为 medium 风险，写操作为 high 风险，全部仅超级管理员可用；LLM cron 可传 `tool_list_names` 和 `session_mode`；列表默认隐藏已完成 cron，传 `include_completed=true` 才显示历史完成项。
- `internal/tool/builtin/env.go`：内置工具环境变量读取 helper；优先读 OS env，缺失时读取配置目录 `.env`，用于 Tavily/Jina API key。
- `internal/tool/builtin/web_search.go`：Tavily 搜索工具；返回 answer、来源链接和摘要，并依赖 `web_extract`，用户侧 tag 为 `web`。
- `internal/tool/builtin/web_extract.go`：Jina Reader/标准库网页提取工具；支持代理、分段读取和进程内缓存，用户侧 tag 为 `web`。
- `internal/tool/builtin/file_tools.go`：文件读写工具包装层；`read_file` 返回带行号文本和文件哈希，支持 grep 子串搜索；`edit_file` 支持行编辑、match/anchor（`match_mode` 区分 content 精确子串与 line 行前缀整行，`index` 选择多处匹配）、创建新文件、unified diff 和风险确认详情；底层读写编辑能力来自 `internal/utils/fileops`。
- `internal/tool/builtin/elwisp_creator.go`：内置 Elwisp 创建指南工具；无参数返回配置感知的精简 Elnis/Elvena/ELyph 任务卡，提示 LLM 创建 Elwisp 所需协议、约束和配置注意事项，并依赖 read_file/edit_file/shell。
- `internal/tool/builtin/shell.go`：内置 shell 工具；接口保留通用 `cmd`，可执行任意 shell 命令，用户侧 tag 为 `agent`，调用前通过风险评估与高风险确认流程拦截；后台 sandbox context 下会创建目录并把 shell cwd 固定到 sandbox。

- `internal/tool/builtin/shell_risk.go`：shell/bash 命令风险分类器；使用 `mvdan.cc/sh/v3/syntax` 解析 AST，识别管道、重定向、命令替换、动态命令、删除、提权、下载即执行等风险并返回风险原因。
- `internal/tool/builtin/shell_sandbox.go`：后台 shell 轻沙盒 AST 校验；检查重定向和常见路径参数中的绝对路径、`..` 逃逸、动态路径与 `cd`，违规时把风险提升为 critical。
- `internal/elyph/`：ELyph Task Notation 语言层；提供规则卡、AST/diagnostic、parser/linter，供原生 skill 创建、扫描和 LLM cron 任务复用。
- `internal/tool/skill/parser.go`：通用 `SKILL.md` 解析器；兼容 agentskills.io 风格外置 AgentSkill 常见 YAML front matter 的 `name`、`description`、`when_to_use`、`risk`，并提供目录名和正文首段 fallback；未写 risk 时默认 high。

- `internal/tool/skill/catalog.go`：skill catalog；记录 AgentSkill/Go skill 的名称、详情格式、风险、根目录和 Go binary 路径，供隐藏包装工具按名称查找。

- `internal/tool/skill/creator.go`：`create_el_skill` 内置元工具；用结构化参数 `name/description/risk/elyph/go_source` 创建 ElBot 原生 skill，写入 `SKILL.elyph`，可选写入 `main.go` 并编译，创建前用 ELyph parser/linter 校验，成功后自动 reload；不再要求 LLM 拼 `SKILL.md` front matter，未提供源码时创建纯 ELyph 文本 skill。
- `internal/tool/skill/modify_el_skill.go`：`read_el_skill`/`modify_el_skill` 。
- `internal/tool/skill/finalize_el_skill.go`：`finalize_el_skill` 内置元工具；完成原生 EL Skill 修改，校验 `SKILL.elyph`，对 `main.go` 执行 gofmt、`package main` 校验、`go build` 和 reload，并把格式化/编译错误作为结果返回给 LLM。
- `internal/tool/skill/elyph_warnings.go`：ELyph warning 结果拼接 helper；把非阻断诊断追加到 skill 工具成功结果。
- `internal/tool/skill/go_source.go`：原生 Go skill 源码维护 helper；提供 gofmt、`package main` 校验、Go 可执行文件解析和 `go build` 编译能力，供 `finalize_el_skill` 和创建流程复用。
- `internal/tool/skill/descriptor.go`：skill 描述对象；让 AgentSkill/Go skill 可被 `discover_tool` 查到详情，按结构化 detail 暴露内容、格式和规则卡；skill 本体不作为可直接调用 schema 暴露。
- `internal/tool/skill/scanner.go`：skill 文件系统扫描与 reload；主程序默认根目录为配置目录下 `skills/`；AgentSkill 使用 `agent/<skill>/SKILL.md`，可选 `SKILL.elyph` 覆写 Agent 可读说明；Go skill 必须使用 `go/<skill>/SKILL.elyph`，可选 binary；同步新增/删除 skill 并更新 catalog。
- `internal/tool/skill/runner.go`：隐藏包装工具实现；`python_skill_run` 固定在 AgentSkill 目录用 `uv run python` 执行附带 Python 脚本，`go_skill_run` 选择 Go skill binary 并把必填 `payload` 对象 JSON 写入 stdin；执行错误会区分启动/超时/进程失败并回传 stdout/stderr，风险按目标 skill 的 `risk` 评估。
- `internal/utils/fileops/`：公共文本文件辅助包；提供编码识别、文本读写、行编辑、match/anchor 编辑（content 精确子串与 line 行前缀两种模式，支持 index 选择多处匹配）、diff、sha256 和原子写入，供文件工具和 skill 维护工具复用。

### Tool 约定

- 风险等级只用于内部权限与确认，不暴露给 LLM。
- 工具结果回灌 LLM 使用 `Result.Content` 或 typed `Result.Segments`；图片/文件必须显式返回 segment。
- `Result.Data` 仅供内部结构化消费，不进入 tool message。
- Tool/插件发送消息统一返回 `Result.Outputs`，由 Agent/Output Manager 发送。

### 平台适配

- `internal/platform/platform.go`：平台抽象；定义 `PlatformAdapter`、`PlatformHandler`、统一 `SendChat`/`SendNotice` 的 message sender、可携带多条平台消息 ID 的发送 receipt、平台 `MessageSegment`（text/image/file/at）和每条入站消息的 Actor/Scope/发送目标/平台群身份上下文；上下文可携带平台解析出的 fork 来源、多模态消息段和少量平台原生 metadata。
- `internal/platform/config.go`：平台配置辅助；把 `app.toml` 中 `[platform.<name>]` 原始 section 解码给适配器自有 Config，并提供关键词前缀剥离 helper。
- `internal/platform/builtin/builtin.go`：内置平台装配；按运行模式组合 CLI、headless 和 enabled 外部平台。
- `internal/platform/headless/headless.go`：service 模式的非交互 primary platform。
- `internal/platform/cli/cli.go`：CLI 平台实现；本地交互模式下读取 stdin/TUI，支持注入补全服务；实现统一 `SendChat`/`SendNotice`，并向 TUI 推送 reasoning 与 runtime status。
- `internal/platform/cli/config.go`：CLI 远程连接配置；解析 `[platform.cli]` 下服务端监听、客户端 profile、默认客户端和 token env。
- `internal/platform/cli/remote_protocol.go`：远程 CLI WebSocket 协议类型；定义 hello/input/completion/output/status/stream/error 等消息。
- `internal/platform/cli/remote_server.go`：服务端侧远程 CLI Adapter；service 模式注册为 `cli` 平台，鉴权并管理多个 CLI client，支持输入、补全、notice/chat/status/reasoning/stream 推送。
- `internal/platform/cli/remote_client.go`：客户端侧远程 CLI runner；`elbot cli`/`elbot -c` 连接服务端，复用 TUI，将输入和补全请求转发到服务端。
- `internal/platform/cli/tui.go`：Bubble Tea TUI 主编排；提供聊天/通知/输入区、历史、滚动、补全候选窗、reasoning 与正文分离显示、状态栏。
- `internal/platform/qq-onebot/`：QQ OneBot v11 正向 WebSocket 适配。
- `internal/platform/qqofficial/`：QQ 官方机器人 C2C 单聊适配；负责 access token、Gateway identify/heartbeat/resume（含 4009 连接过期重连）、默认 Markdown 文本发送、富媒体上传发送、入站附件下载到 sandbox 平台目录、Keyboard 确认按钮和 ARK 预留；配置来自 `[platform.qqofficial]`。
- `internal/platform/telegram/`：Telegram Bot API long polling 适配；负责文本/图片/文件收发、HTTP 代理、默认 HTML 格式化、可选 Rich Message 实验模式、editMessageText 伪流式输出、引用上下文、聊天历史记录、inline keyboard 风险确认按钮和 bot commands 同步；配置来自 `[platform.telegram]`。
- `internal/platform/cli/tui_copy.go`：CLI TUI copy mode；支持鼠标分区滚动、vim模式复制和搜索；`clipboard.go` 默认用系统剪贴板并在 SSH/tmux 等场景走 OSC52 fallback。

### Session 服务


- `internal/session/session.go`：Session 业务服务；管理当前 session、创建、恢复、Fork、分页列表、状态、模式切换、手动重命名、归档/置顶/删除、过期清理、平台隔离、按 `cron:` scope 识别的同平台 cron session 可见性和 CLI 全平台全 owner 列表可见性。

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
- `internal/storage/sqlite/chat_history_repository.go`：ChatHistory repository SQLite 实现；负责平台聊天历史写入、搜索、上下文和清理。
- `internal/storage/sqlite/cron_job_repository.go`：CronJob repository SQLite 实现；负责中央 Cron job 的 upsert、列表、按名称查询、禁用、删除和最近运行状态更新；upsert 在配置未变化时跳过写库，减少启动期无意义 SQLite 写入。
- `internal/storage/sqlite/elnis_event_repository.go`：ElnisEvent repository SQLite 实现；负责 Elwisp 事件创建、按来源 key 去重查询和状态更新。
- `internal/storage/sqlite/tool_call_repository.go`：ToolCallRecord repository SQLite 实现；负责每次工具调用记录写入，并按 Session 聚合工具调用次数供 `/status` 展示。


**注**：
1. 每次更新代码后，若有go文件修改或者新增，有必要的话在此文档更新说明，以方便快速了解项目。注意agent.md现在太长，每次更新时，只介绍每个文件是干啥的即可，多余废话全部删除。如果有让人容易搞混、看不明白的、必要解释的，应写在代码注释里。
2. 写代码时，暂时不实现的，但是未来可能会有的，要考虑留出接口或者搭好地基，然后注释写个TODO。
3. 开发中不应该考虑兼容，导致代码复杂或者冗余。
4. 若有新功能或者修改旧功能，需要更新对应readme（用户侧）和CHANGLOG.md
5. 整体语言用简体中文。


