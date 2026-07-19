# ElBot 代码地图

本文用于快速定位“某类任务应该看哪些文件”。需要理解调用链路时，读 `devdocs/architecture.md`。

定位方法：

```bash
rg -n "locator:tool" devdocs/code-map.md
```

原则：

- 同一职责集中在目录内时，只写目录。
- 只有核心入口、跨目录桥接、容易混淆或独特文件才单独列出。
- 每节只写入口和边界，不展开完整架构说明。

<!-- locator:startup -->
## 启动、运行模式与装配

适用任务：命令行入口、运行模式判断、app 层依赖装配、远程 CLI client/service 入口。

先看：

- `cmd/elbot/main.go`：程序入口。
- `internal/launcher/cli.go`：命令行解析和补全生成。
- `internal/app/app.go`、`runner.go`、`dependencies.go`：稳定启动入口、分阶段 Runner 和可替换依赖组。
- `internal/app/foundation.go`、`models.go`、`runtime.go`：配置/存储基础设施、模型客户端，以及 Cron/Tool/Hook/Agent 核心装配。
- `internal/app/platforms.go`、`integrations.go`：平台运行、Elnis 和平台能力接线；同目录还包含远程 CLI client 与 service marker。

常用搜索：

```bash
rg -n "func Run|service run|completion|--client|RunCron" cmd internal/app internal/launcher
```

<!-- locator:config -->
## 配置、资产与日志

适用任务：配置读取、默认配置资产、provider/state/tool_tags 合并、日志写入/轮转/读取。

先看：

- `internal/config/`
- `internal/logging/`
- `docs/configuration.md`

常用搜索：

```bash
rg -n "ELBOT_CONFIG_FILE|providers.toml|state.toml|tool_tags.toml|TextHandler|audit" internal/config internal/logging docs/configuration.md
```

<!-- locator:agent-chat -->
## Agent 对话流程

适用任务：普通聊天主流程、LLM 调用、流式输出、Prompt、system prompt、工具 transcript、pending 输入、风险确认。

先看：

- `internal/agent/core.go`：Agent 状态和构造装配；构造参数集中在 `Options`。
- `internal/agent/message.go`：消息入口、slash/普通输入分发和用户错误通知。
- `internal/agent/command_runtime.go`：命令权限、Turn 冲突、通知和 continuation 的统一编排。
- `internal/agent/input.go`：普通输入预处理、命令 continuation、pending 和风险确认入口。
- `internal/agent/segments.go`：平台入站 Segment 与 LLM Segment 转换。
- `internal/agent/options.go`、`logging.go`、`identity.go`：运行配置、日志和 Actor/Scope 解析。
- `internal/agent/chat.go`：普通对话主流程。
- `internal/agent/chat_llm.go`：LLM 调用和消息转换。
- `internal/agent/chat_tools.go`：工具执行与确认。
- `internal/agent/turn_output.go`：turn 输出适配。
- `internal/agent/prompt.go`：Prompt 构建。
- `internal/agent/system_prompt*.go`：system prompt 来源和组合。
- `internal/agent/tool_transcript.go`：工具 transcript 持久化。

常用搜索：

```bash
rg -n "Handle|Run|Prompt|tool_calls|reasoning|usage|pending|prepared" internal/agent
```

<!-- locator:commands -->
## Slash 命令与补全

适用任务：新增/修改 slash 命令、命令帮助、命令参数补全。

先看：

- `internal/agent/commands/`：内置命令实现。
- `internal/agent/commands/register.go`：命令模块注册入口。
- `internal/agent/commands/session_*.go`：按模式、核心、导航、生命周期和格式化拆分的 Session 命令；共享状态由 `SessionCommandState` 按 Scope 隔离。
- `internal/command/`：通用命令框架和 Router。
- `internal/completion/`：平台补全服务。
- `docs/commands.md`：用户侧命令文档。

常用搜索：

```bash
rg -n "Register|Info\{|Help:|Complete|Alias|/requests|/model" internal/agent/commands internal/command internal/completion docs/commands.md
```

<!-- locator:request-turn -->
## Request、Turn 与运行状态

适用任务：active request 树、取消/停止/超时、阶段展示、工具 pending、确认状态、runtime status。

先看：

- `internal/request/`
- `internal/turn/`
- `internal/runtime/`
- `internal/agent/status.go`：Agent runtime status 发布。
- `internal/agent/request_context.go`：父子 request context。
- `internal/agent/risk_confirmation.go`：高风险确认命令文案和识别。

常用搜索：

```bash
rg -n "Phase|Request|Cancel|pending|confirm|runtime status|sending" internal/request internal/turn internal/runtime internal/agent
```

<!-- locator:tool -->
## Tool Runtime、工具发现与内置工具

适用任务：内置工具、工具注册、schema、风险等级、确认详情、工具发现、工具缓存、工具 tag、文件/shell/web/cron/memory 工具。

先看：

- `internal/tool/`：Tool Runtime 核心类型、builder、discover、executor、sandbox/workspace helper。
- `internal/tool/runtimeinfo/`：工具运行期常用信息入口，如配置路径、sandbox、文件发送配置、时间源和规则卡转发。
- `internal/toolrun/`：工具调用中间层、工具视图、命名解析、风险确认。
- `internal/tool/builtin/`：内置工具。
- `internal/tool/builtin/file_tools_ast.go`：`read_file` 的 Go/Shell AST 名称搜索与结果渲染。
- `internal/agent/tools.go`：Agent 工具运行态和命令依赖适配。
- `internal/agent/toolrun_*.go`：Agent 到 ToolRun 的桥接。
- `internal/agent/tool_cache.go`：Session 级工具 schema 缓存。
- `internal/agent/tool_directive.go`：`@tool:` / `@skill:` 预处理。
- `internal/agent/tool_tag_config.go`：工具 tag 配置。
- `internal/security/`：工具权限和风险策略。
- `internal/utils/fileops/`：文件读写编辑底层能力。

常用搜索：

```bash
rg -n "discover_tool|NewBuilder|Risk|Confirm|ToolRun|Result\{|Outputs|workspace|shell" internal/tool internal/toolrun internal/agent
```

<!-- locator:tool-flow -->
## 工具调用链路相关文件

适用任务：LLM tool call 到工具执行、工具结果回灌 LLM、工具调用记录、工具确认、批量预览。

先看：

- `internal/agent/chat_tools.go`：Agent 工具执行主入口。
- `internal/toolrun/`：执行前解析、过滤、确认和预览。
- `internal/tool/executor.go`：Tool Runtime 执行适配。
- `internal/tool/tool.go`：Tool 核心类型。
- `internal/agent/tool_transcript.go`：tool message/transcript 落库。
- `internal/storage/sqlite/tool_call_repository.go`：工具调用记录持久化。

常用搜索：

```bash
rg -n "ToolCall|tool message|transcript|ToolCallRecord|confirm|preview" internal/agent internal/tool internal/toolrun internal/storage
```

<!-- locator:skill -->
## Skill 与 ELyph

适用任务：AgentSkill 解析或工具化、ELyph parser/linter、原生 EL Skill 创建/修改/finalize、Go skill 扫描/编译/运行。

先看：

- `internal/elyph/`：ELyph 语言层。
- `internal/tool/skill/`：Skill 解析、扫描、catalog、创建、修改、finalize、runner。
- `internal/tool/skill/agent_manifest.go`：AgentSkill 工具化 manifest。
- `internal/tool/skill/go_source.go`：原生 Go skill 源码维护和编译。

常用搜索：

```bash
rg -n "SKILL.elyph|ELBOT_SKILL|AgentSkill|go_skill_run|finalize|Lint|Catalog" internal/elyph internal/tool/skill
```

<!-- locator:hook -->
## Hook 与插件

适用任务：Hook 事件、控制字段、注册、列表、热重载、规则 Hook TOML、exec action、hook.v2 协议、持久 Hook、SharedState、常驻记忆 Hook。

先看：

- `internal/hook/event.go`：Hook 点、事件 payload 和 Handler 基础类型。
- `internal/hook/output/`：规则、一次性 exec 与 runtime 共用的输出协议、校验和 delivery 转换。
- `internal/hook/protocol/`：进程 Hook 共用的 `hook.v2` 帧、ID 校验和 `event.handle` 公共结果字段。
- `internal/hook/match.go`：Hook 条件匹配、字段读取和模板值。
- `internal/hook/manager.go`：普通 Hook 注册、排序、执行与原子 handler 快照替换。
- `internal/hook/control/`：`/hooks` 的列表、重载和持久进程生命周期管理入口。
- `internal/hook/builtin/`、`internal/hook/plugins/`：内置 Hook 注册与内置插件。
- `internal/hook/rules/`：规则 Hook；`rules.go` 提供类型和模块入口，`config.go`/`toml_error.go` 负责配置加载与诊断，`rule.go`/`action.go`/`exec.go` 负责规则及 Action 执行，`detail.go` 负责列表详情。
- `internal/hook/runtime/`：Worker Hook 配置、进程、双向 Pipe RPC、waiting 路由、工具桥接和进程内 SharedState。
- `internal/agent/hooks.go`：Agent 的 Hook 执行、上下文和 continuation 接入。
- `internal/agent/output.go`：Agent 的 Output Manager 与平台 sender 接入。
- `docs/hooks.md`：用户侧 Hook 文档。

常用搜索：

```bash
rg -n "Event|Handler|Control|plugins/hooks.toml|exec|hook.v2|runtime|SharedState|resident" internal/hook internal/agent docs/hooks.md
```

<!-- locator:output -->
## Output、Delivery 与发送

适用任务：输出意图结构、文本/图片/文件/at/reply/emoticon 发送、流式输出、notice、reasoning、runtime status。

先看：

- `internal/delivery/`：平台无关输出意图和发送管理。
- `internal/agent/turn_output.go`：Agent turn 输出适配。
- `internal/agent/output.go`：Agent 的 Hook/工具输出意图和平台 sender 接入。
- `internal/platform/platform.go`：平台发送抽象。

常用搜索：

```bash
rg -n "Output|SendChat|SendNotice|Stream|Reasoning|emoticon|receipt" internal/delivery internal/agent internal/platform
```

<!-- locator:platform -->
## 平台适配

适用任务：平台输入解析、平台发送、CLI/TUI、远程 CLI、OneBot、QQ 官方、Telegram。

先看：

- `internal/platform/platform.go`：平台抽象。
- `internal/platform/config.go`：平台配置解码。
- `internal/platform/builtin/`：内置平台装配。
- `internal/platform/cli/`：本地/远程 CLI 和 TUI。
- `internal/platform/cli/tui.go`、`tui_mouse.go`、`tui_copy.go`：TUI 主模型、鼠标交互与 copy mode。
- `internal/platform/qq-onebot/`
- `internal/platform/qqofficial/`
- `internal/platform/telegram/`
- `internal/platform/headless/`

常用搜索：

```bash
rg -n "PlatformAdapter|SendChat|MessageSegment|Actor|Scope|remote|websocket|long polling" internal/platform
```

<!-- locator:session -->
## Session

适用任务：session 创建/恢复/列表/归档/置顶/删除、Fork、模式切换、命名、过期清理、平台隔离、cron session 可见性。

先看：

- `internal/session/service.go`、`types.go`：Session 服务主体和领域请求/结果类型。
- `internal/session/mode.go`：模式激活和 work 历史限制。
- `internal/session/lifecycle.go`、`query.go`、`fork.go`、`expiration.go`：生命周期、查询、Fork 和闲置过期策略。
- `internal/session/naming.go`：异步 Session 命名。
- `internal/agent/session_metadata.go`：Session metadata 编解码。
- `internal/agent/workspace.go`：Agent workspace 持久化适配。
- `internal/tool/workspace.go`：工具 workspace context 和路径解析。

常用搜索：

```bash
rg -n "Fork|Archive|Pinned|Expire|SessionMode|metadata|workspace|cron:" internal/session internal/agent internal/tool
```

<!-- locator:context -->
## Context、Prompt 与压缩

适用任务：上下文加载、压缩摘要、context window、usage 展示、system prompt source。

先看：

- `internal/contextmgr/`：按加载/Fork、窗口、usage、压缩器和摘要 prompt 拆分的上下文基础能力。
- `internal/agent/context_runtime.go`、`context_compact.go`、`context_seed.go`、`context_usage.go`：Agent 上下文运行态、独立 Session 压缩编排、首消息 seed 物化与 usage/动态阈值。
- `internal/agent/prompt.go`：Prompt Builder。
- `internal/agent/system_prompt*.go`：system prompt 管理和来源。
- `internal/llm/segment.go`：MessageSegment helper。

常用搜索：

```bash
rg -n "ContextLoader|Compress|Window|System Prompt|MessageSegment|usage" internal/contextmgr internal/agent internal/llm
```

<!-- locator:llm -->
## LLM Adapter

适用任务：LLM 抽象、OpenAI-compatible 请求、SSE、usage、reasoning、tool call delta、多模态消息转换。

先看：

- `internal/llm/`：LLM 抽象和 MessageSegment。
- `internal/llm/openai/`：OpenAI-compatible adapter。
- `internal/agent/model.go`：模型运行态、模型切换、provider client 缓存。
- `internal/agent/chat_llm.go`：Agent LLM 调用适配。

常用搜索：

```bash
rg -n "ChatCompletion|Stream|SSE|reasoning|usage|ToolCall|MessageSegment|Models" internal/llm internal/agent
```

<!-- locator:storage -->
## Storage 与 SQLite

适用任务：领域模型、repository interface、migration、session/message/context summary/chat history/cron/elnis/tool call 持久化。

先看：

- `internal/storage/storage.go`：领域模型和 repository interfaces。
- `internal/storage/id.go`、`internal/storage/time.go`：通用 ID/时间 helper。
- `internal/storage/sqlite/`：SQLite store、migration 和 repository 实现。

常用搜索：

```bash
rg -n "Migration|Repository|Upsert|List|Archive|Fork|ToolCall|CronJob|ElnisEvent" internal/storage
```

<!-- locator:elnis -->
## Elnis / Elvena / Elwisp

适用任务：Elvena 协议类型、Elnis HTTP/鉴权/去重/事件准备、direct/llm 投递、segments、background、Elwisp 文档或指南工具。

先看：

- `internal/elvena/`：公共协议层。
- `internal/elnis/`：Elnis HTTP、鉴权、准备、投递和后台任务；`outbox.go` 负责 LLM 报告持久化投递、重试与恢复。
- `internal/storage/sqlite/elnis_event_repository.go`：Elnis event 与 report outbox 的事务、claim、receipt 和完成状态持久化。
- `internal/background/`：cron/Elnis 共用后台 LLM 类型与结果 helper。
- `internal/tool/builtin/elwisp_creator.go`：Elwisp 创建指南工具。
- `docs/elnis.md`
- `docs/elnis-usage.md`
- `devdocs/elnis-elwisp.md`

常用搜索：

```bash
rg -n "Elvena|Elwisp|/elvena/v2/events|direct|segments|session_mode|background" internal/elvena internal/elnis internal/background docs devdocs
```

<!-- locator:cron -->
## Cron 与维护任务

适用任务：中央 Cron Runtime、LLM 可编排 cron 服务、维护类清理任务、cron 工具。

先看：

- `internal/cron/service.go`：Cron Service 装配、CRUD 与公开入口。
- `internal/cron/model.go`：任务 Metadata、Delivery 状态类型、校验与规范化。
- `internal/cron/execution.go`：Direct/LLM 执行、报告生成和 JSON 格式重试。
- `internal/cron/delivery.go`：逐目标逐输出发送、状态持久化、降级与 receipt mapping。
- `internal/cron/recovery.go`：平台连接跟踪、过期 once 扫描与补发入口。
- `internal/maintenance/`
- `internal/agent/cron*.go`：Agent 后台 runner 和后台工具确认特例。
- `internal/tool/builtin/cron.go`：cron 内置工具。
- `internal/storage/sqlite/cron_job_repository.go`：cron job 持久化。

常用搜索：

```bash
rg -n "Cron|Job|Schedule|RunCron|maintenance|include_completed|tool_list_names" internal/cron internal/maintenance internal/agent internal/tool internal/storage
```

<!-- locator:docs -->
## 文档与变更记录

适用任务：用户文档、开发文档、自动翻译流程、changelog。

先看：

- `docs/`：中文用户文档。
- `devdocs/`：维护者/Agent 开发文档。
- `scripts/translate_docs.py`：用户文档增量翻译脚本。
- `CHANGELOG.md`：中文变更记录。

不要手动修改：

- `docs.en/`
- `README.md`
- `CHANGELOG.en.md`

常用搜索：

```bash
rg -n "locator:|CHANGELOG|docs.en|translate" AGENT.md docs devdocs scripts
```

