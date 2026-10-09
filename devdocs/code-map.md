# ElBot 代码地图

按 locator 查职责入口；调用链和关键约束见 [架构说明](architecture.md)。同目录内的实现优先从目录定位，跨模块桥接单独列出。

## 定位方法

1. 从 [AGENTS.md 的 locator 表](../AGENTS.md#快速定位流程) 选择任务关键词，例如工具调用用 `locator:tool-flow`。
2. 在代码地图和架构说明中搜索对应章节：

```bash
rg -n '^<!-- locator:tool-flow -->$' devdocs/code-map.md devdocs/architecture.md
```

3. 按输出的文件和行号，用 `nrf <文件> -s <起始行> -e <结束行>` 读取命中章节。找代码入口看代码地图，理解调用链看架构说明。
4. 定位到 Go 文件后，已知符号用 `nls inspect <文件> <符号>`；未知符号先用 `nls ds <文件>`。

<!-- locator:startup -->
## 启动与装配

- `cmd/elbot/main.go`、`internal/launcher/cli.go`：程序入口、命令行与运行模式。
- `internal/app/app.go`、`internal/app/runner.go`、`internal/app/dependencies.go`：生产入口、分阶段装配与替换工厂。
- `internal/app/foundation.go`、`internal/app/models.go`：配置、存储与 provider 客户端。
- `internal/app/services.go`、`internal/app/runtime.go`：共享服务、启动媒体清理与 Elnis 恢复协调、Agent／Tool／Hook／Cron、命令注册与执行回调。
- `internal/app/platforms.go`、`internal/app/integrations.go`：平台、Elnis 与外部宿主接线。
- `internal/app/signals.go`、`internal/app/lifecycle.go`：订阅所有权和启动失败／退出清理。

<!-- locator:contextinfo -->
## 公共上下文事实

- `internal/contextinfo/`：Conversation、Actor、Execution、Model 定义与独立存取。
- `internal/platform/platform.go`、`internal/platform/cli/message.go`：平台消息投影与本地 CLI 来源。
- `internal/security/`：身份解析、权限与风险策略。
- `internal/agent/dialogue/execution_context.go`：固定模型事实、接管时刷新来源与身份。
- `internal/request/manager.go`、`internal/turn/execution.go`、`internal/session/binding.go`：执行关联的实际建立与有效性。

<!-- locator:signal -->
## 信号与订阅

- `internal/signal/`：类型化信号、有界执行器、背压和关闭策略；`diagnostics.go` 直接向 stderr 报告设施故障。
- `internal/events/logging.go`：九字段公共日志契约、整数枚举、`LogIdentity`、`LogSubmitted` 和 `EmitLog`；发布入口验证契约，仅复制业务填写的身份，不从 context 补值。
- `internal/logging/signals.go`：日志中心自身订阅、接收记时、分类队列、消费过滤、写入及短写检查和停止准入；非法类别诊断保留枚举数值。关闭与文件资源由 `logging.go` 管理。
- `internal/agent/events/`：Agent 实例事实信号。
- `internal/events/platform.go`：平台连接全局事实契约；各适配器直接发布。
- `internal/agent/platform_signals.go`、`internal/cron/platform_signals.go`：Hook 协调模块与 Cron 自行订阅、按平台排队并关闭；`StartPlatformEvents` 在装配完成后启动订阅，`BeginClose`／`Close`／`Done` 管理退出。
- `internal/agent/logging.go`：输入、超时、确认和拒绝事件的同步日志回调；模型、工具、持久化和回复日志由实际操作来源组织。`internal/session/naming_logging.go`、`internal/modelmgr/logging.go` 保留来源明确结果的同步日志回调，独立于通知订阅。
- `internal/session/naming_signals.go`：Scheduled／Completed 两种命名信号，完整终态字段定义在 `types.go`；`naming_lifecycle.go` 等待生产者退出后断开日志连接。`internal/modelmgr/signals.go` 的重试事件固定 Identity 并携带 HTTP 来源声明的尝试结果。
- `internal/app/signals.go`、`internal/app/agent_notifications.go`、`internal/app/agent_status.go`、`internal/app/model_signals.go`：App 通知、状态与重试通知订阅及其生命周期；通用 `newQueue`／`connectSignal` 归 `signals.go`，模型文件只放通知接线，状态投影入队时深复制用量。

<!-- locator:config -->
## 配置、资产与日志

- `internal/config/`：读取、默认资产、模块声明、provider／state／tag 及只读检查。
- `internal/config/definition.go`、`internal/config/provider.go`：配置规则与客户端模式校验。
- `internal/doctor/`：配置诊断报告；`internal/command/builtin/doctor.go`：超管命令入口。
- `internal/logging/`：日志中心订阅全局记录，分类排队、写入、轮转与读取；`signals.go` 按业务指定等级过滤，`record.go` 负责 text／JSON 脱敏、Unicode 字符限长、原生 JSON 正文、JSONL 编码及含行尾的 64 KiB 上限检查。文件写入器只由中心管理。
- `internal/logging/reader.go`：反向分块兼读 JSONL／历史文本；外层与 Content 业务条件分别匹配后计入上限；LogQueryResult 返回有效记录、扫描坏行数量及有界原文样本，保留读取范围、取消和单行上限。
- [配置文档](../docs/configuration.md)。

<!-- locator:agent-chat -->
## Agent 对话

- `internal/agent/agent.go`、`internal/agent/assembly.go`：薄入口与组件装配。
- `internal/agent/message.go`、`internal/agent/input.go`、`internal/agent/background.go`：消息分发、普通输入与后台准备。
- `internal/agent/dialogue/runner.go`、`internal/agent/dialogue/preparation.go`：公共单轮加载、准备与结果。
- `internal/agent/dialogue/model_call.go`、`internal/agent/dialogue/system_prompt.go`：共同模型 Hook 与 Prompt 来源；CallObservation 分别持有业务 Event 和日志 LogRecord，发布时固定身份和用量副本。
- `internal/agent/chat/`：Chat Loop、请求、历史、摘要与 seed；取消时保留部分输出。
- `internal/agent/responses/loop.go`、`internal/agent/responses/model_call.go`：Responses Loop、续链、pending／接管及恢复。
- `internal/agent/responses/context.go`、`internal/agent/responses/persistence.go`：完整原生窗口与提交。
- `internal/agent/responses/tool_inputs.go`、`internal/agent/responses/input_format.go`：冻结工具定义与输入／seed 格式。
- `internal/agent/dialogue/commit.go`、`internal/agent/dialogue/reply_commit.go`：消息事务准入、ToolPair 与最终回复。
- `internal/agent/segments.go`、`internal/agent/inbound_media.go`：有序图文与入站媒体。

<!-- locator:protocol-routing -->
## 协议路线

- `internal/agent/routes/`：provider 能力绑定、源协议能力与封闭注册表。
- `internal/agent/assembly_routes.go`、`internal/app/models.go`：业务路线和客户端装配。
- `internal/agent/dialogue/loop.go`、`internal/contextmgr/compact_contract.go`、`internal/session/material.go`：公共消费与材料交接契约。
- `internal/llm/origin.go`、`internal/modelmgr/compatibility.go`：持久化归属与纯兼容判断。
- `internal/llm/client.go`：独立文本调用契约与可空 int64 用量计数；`internal/llm/chatcompletions/response.go` 保留精确整数、明确零及别名回退。`internal/agent/events/events.go` 的 `CloneUsage` 复制用量指针快照。
- `internal/llm/chatcompletions/logging.go`：以九字段 text 契约记录请求摘要、最新消息和首次系统消息，来源显式提供身份，保留媒体内嵌数据预处理。
- `internal/llm/api_type.go`、`internal/tool/availability.go`：模型 API 类型与工具可用性；`contextinfo.Model.APIType` 承载当前主对话模型事实。

<!-- locator:commands -->
## 命令与补全

- `internal/command/`、`internal/command/builtin/register.go`：Router、命令契约与注册。
- `internal/command/builtin/`：内置命令；`internal/agent/command_runtime.go`：权限、冲突和 Continuation。
- `internal/command/builtin/log.go`：运行、审计及 Elwisp 日志的筛选、展示、帮助和补全；raw 与等级独立，坏行提示，Hook 按模块筛选。`usage.go` 按 Content 聚合 llm_usage，未知或溢出按项传播。
- `internal/completion/`、`internal/agent/completion.go`：公共结构化补全与 Agent 接线。
- `internal/agent/file_rollback.go`：文件命令准入；`internal/command/builtin/rollback.go`：撤销命令。
- [命令文档](../docs/commands.md)。

<!-- locator:request-turn -->
## Request、Turn 与状态

- `internal/request/`、`internal/turn/`：请求树、取消、阶段、pending、确认与 Execution。
- `internal/agent/execution.go`、`internal/agent/execution_run.go`：前后台执行与单轮收尾。
- `internal/agent/execution_admission.go`、`internal/agent/execution_input.go`：准入、输入、追加确认与续跑。
- `internal/agent/execution_lifecycle.go`：追加确认等待及关闭。
- `internal/agent/confirmation.go`、`internal/agent/risk_confirmation.go`：风险确认协调与文案。
- `internal/runtime/`、`internal/agent/status.go`：状态类型、同步保存与观察事件。

<!-- locator:tool -->
## Tool Runtime、工具状态与文件

- `internal/tool/`、`internal/tool/builtin/`：注册、schema、Runtime 执行器与内置工具。
- `internal/toolrun/`：视图、路由、权限风险、确认及调用参数媒体引用。
- `internal/toolrun/state.go`、`internal/toolrun/preload.go`、`internal/toolrun/background.go`：状态提交、预加载与后台白名单；预载失败与跳过由来源记录身份、原因和结果。
- `internal/agent/tool_directive.go`、`internal/agent/tool_cache.go`：输入指令和状态提交编排。
- `internal/fileops/`：共享文件操作、编辑、目标锁和撤销备份。
- `internal/workspace/`、`internal/session/workspace.go`、`internal/sandbox/`：工作目录、持久化与后台路径限制。
- `internal/tool/media_runtime.go`：工具／Skill 媒体准备与导出缓存。

<!-- locator:tool-flow -->
## 工具调用链路

- `internal/agent/dialogue/tool_execution.go`：路线共用的 ToolRun 入口与协议提交器，批次准备失败只记批次日志。
- `internal/agent/toolrun_adapter.go`：Hook、子请求、确认、调用记录与状态提交桥接；RecordToolCall 保存完整工具记录并返回保存错误。
- `internal/toolrun/runner.go`：单工具流程与终态日志，记录实际执行的 transcript 保存结果及其他独立错误。
- `internal/toolrun/toolrun.go`：工具解析与原生／远程路由，ResolvedTool 使用明确结果与原始错误。
- `internal/tool/executor.go`、`internal/toolrun/elwisp_exec.go`：原生／远程执行，保留部分结果、原始错误和真实 ExecutionStarted 边界；PolicyDeniedError 定义在 tool 执行器。
- `internal/agent/dialogue/message_store.go`、`internal/storage/tool_pair.go`：transcript 与调用／结果关联。
- `internal/storage/sqlite/tool_call_repository.go`：调用记录持久化。

<!-- locator:skill -->
## Skill 与 ELyph

- `internal/elyph/`：语言解析与 lint。
- `internal/tool/skill/`：扫描、catalog、创建、finalize、reload 和 runner。
- `internal/tool/skill/agent_manifest.go`、`internal/tool/skill/go_source.go`：AgentSkill manifest 与 Go 源码／编译。
- `internal/tool/skill/media.go`：媒体参数与 stdout 媒体。
- `internal/config/assets.go`：内置 Skill 说明。

<!-- locator:hook -->
## Hook 与插件

- `internal/hook/event.go`、`internal/hook/manager.go`：事件与普通 Hook 流水线；执行日志使用九字段，Hook 点、插件、工具及变更前后事实写入正文，业务事件保留专有字段。
- `internal/hook/rules/`、`internal/hook/control/`：规则、一次性 exec 与管理入口；工具和平台调用审计使用 JSON 正文，日志结果由调用来源填写，工具返回错误时仍保留已取得的结果用于日志。
- `internal/hook/runtime/`、`internal/hook/protocol/`：持久进程、RPC、waiting 和 hook.v2 帧。
- `internal/hook/call_policy.go`：消费方工具调用只读策略。
- `internal/hook/media.go`、`internal/hook/output/`：媒体 API 与输出协议。
- `internal/agent/hooks.go`、`internal/processenv/`：Agent 桥接与进程环境。
- [Hook 文档](../docs/hooks.md)。

<!-- locator:output -->
<!-- locator:notification -->
## 输出、投递与通知

- `internal/delivery/`、`internal/delivery/dispatch/`：输出意图、Target／Receipt、平台路由与媒体准备。
- `internal/notification/`：通知意图、来源校验与规则；无来源通知通过九字段 text 日志记录，通知对象与日志契约独立。
- `internal/agent/output.go`、`internal/agent/turn_output.go`、`internal/agent/execution_output.go`：发送 Hook、前后台策略与接管输出；流替换与结束回执合并去重，保留部分成功和独立切片。
- `internal/agent/dialogue/reply_commit.go`：最终回复落库、发送顺序与回执关联。

<!-- locator:platform -->
## 平台适配

- `internal/platform/platform.go`、`internal/platform/builtin/`：公共契约与装配；连接成功直接发布 `events.PlatformConnected`，不暴露实例连接信号。
- `internal/platform/media.go`、`internal/platform/refcontext/`：历史 segments、敏感来源清洗和引用恢复。
- `internal/platform/cli/`：本地／远程 CLI 与 TUI。
- `internal/platform/qq-onebot/`：协议转换、forward 展开、长消息转发及回执。
- `internal/platform/qqofficial/`、`internal/platform/telegram/`、`internal/platform/headless/`：各平台 runtime。

<!-- locator:session -->
## Session

- `internal/session/service.go`、`internal/session/types.go`：服务与领域接口。
- `internal/session/binding.go`、`internal/session/coordination.go`、`internal/session/signals.go`：current、准入锁与绑定变化。
- `internal/session/origin.go`、`internal/session/promotion.go`、`internal/session/background.go`：原生归属、前台接管与后台准备。
- `internal/session/fork.go`、`internal/session/background_copy.go`、`internal/session/material.go`：Fork、副本与源材料契约。
- `internal/session/compact.go`、`internal/session/lifecycle.go`、`internal/session/expiration.go`：压缩结果创建、生命周期与过期。
- `internal/session/naming.go`、`internal/session/naming_lifecycle.go`、`internal/session/title.go`：命名调度、退出与文本生成。
- `internal/background/takeover.go`：后台修正／投递前的持久化接管检查。

<!-- locator:context -->
## 上下文、Prompt 与压缩

- `internal/contextmgr/`：历史、窗口、用量、seed 状态和压缩分派。
- `internal/agent/execution_compact.go`、`internal/agent/context_usage.go`：压缩生命周期、交接及用量。
- `internal/agent/chat/compact.go`、`internal/agent/chat/compressor.go`：Chat 摘要实现。
- `internal/agent/responses/compact.go`、`internal/agent/responses/branch.go`：原生压缩与分支材料。
- `internal/agent/dialogue/system_prompt.go`、`internal/agent/dialogue/prompt.go`、`internal/agent/dialogue/foreground_notice.go`：共同 Prompt 及接管提示。
- `internal/agent/chat/transcript.go`：Chat 历史与 Prompt Builder。

<!-- locator:llm -->
## 模型服务与 LLM 客户端

- `internal/modelmgr/`：固定选择、兼容性、目录缓存、状态保存与重试信号。
- `internal/llm/`：独立文本、模型目录、消息／工具／Usage 及 Extra 校验。
- `internal/llm/httpclient/`：传输、重试、SSE 与超时。
- `internal/llm/chatcompletions/`、`internal/llm/responses/`：独立协议客户端。
- `internal/llm/responses/client.go`、`internal/llm/responses/tools.go`、`internal/llm/responses/compact.go`：请求冻结、store 偏好、工具权限与原生压缩。
- `internal/llm/responses/stream.go`、`internal/llm/responses/error.go`：原生流事件与协议错误解析，协议诊断由来源写入 Content。`response.go` 容错解析可空用量，`client.go` 使用九字段 text 契约记录请求。

<!-- locator:storage -->
## Storage 与 SQLite

- `internal/storage/storage.go`、`internal/storage/session_metadata.go`：领域模型、repository 契约及 metadata 保留。
- `internal/storage/sqlite/`、`internal/storage/sqlite/migrations.go`：存储实现与数据库迁移；`nullable_context_summary_tokens` 保留摘要数据、外键及索引，使未知用量可保存为 NULL。
- `internal/storage/dialogue.go`、`internal/storage/sqlite/dialogue_repository.go`：ToolPair／原生提交事务与 checkpoint 比较。
- `internal/storage/sqlite/native_snapshot.go`、`internal/storage/sqlite/native_material.go`：不可变调用快照、seed 与新 Session 原子保存。
- `internal/storage/sqlite/media_history.go`、`internal/storage/sqlite/media_lifecycle.go`：历史媒体关联、媒体临时引用恢复与清理认领。

<!-- locator:elnis -->
## Elnis / Elvena / Elwisp

- `internal/elvena/`、`internal/elnis/types.go`：公共协议、来源与 Elnis 类型。
- `internal/elnis/http.go`、`internal/elnis/service.go`：HTTP runtime、队列、去重分发与中断执行恢复裁决；业务日志在 JSON 正文中保留查询键和标签，Session 关联写入 Identity。
- `internal/elnis/llm.go`、`internal/elnis/direct.go`、`internal/elnis/store.go`：模型／直接投递流程与状态存储，来源分别记录业务及保存失败。
- `internal/elnis/auth.go`、`internal/elnis/prepare.go`、`internal/elnis/targets.go`：鉴权、规范化与投递裁决。
- `internal/elnis/media.go`、`internal/elnis/outbox.go`：媒体、持久化投递与恢复。
- `internal/storage/sqlite/elnis_event_repository.go`、`internal/background/`：事件仓储、恢复状态与引用释放的原子执行，以及公共后台类型。
- [内部架构](elnis-elwisp.md)、[功能说明](../docs/elnis.md)、[配置与协议示例](../docs/elnis-usage.md)。

## 媒体中心

- `internal/media/`：导入、图片处理、请求／发送解析、引用与清理。
- `internal/media/platform.go`、`internal/media/history.go`：平台媒体、历史批量获取（缓存、去重与尝试预算）及跨库历史对账。
- `internal/tool/builtin/chat_history.go`、`internal/tool/builtin/get_media.go`：历史查询与显式媒体获取。
- `internal/tool/builtin/view_image.go`：Responses 看图工具，来源／图片序号选择、本地权限及多模态结果。
- `internal/delivery/dispatch/media.go`：输出媒体准备和回执缓存；缓存失败发布 WARN 日志，保留实际发送回执和错误，不触发重发。

<!-- locator:cron -->
## Cron 与维护

- `internal/cron/`：任务 CRUD、调度、执行、报告与投递恢复。
- `internal/cron/service.go`、`internal/cron/manager.go`：来源直接构造九字段日志；运行说明用 text，可筛选审计用 JSON。调度执行的最终结果在状态保存后发布，正文保留执行、重新读取和保存的独立错误。
- `internal/cron/models.go`、`internal/cron/delivery.go`、`internal/cron/recovery.go`：任务模型、逐项投递及连接恢复。
- `internal/maintenance/`：维护任务；清理日志直接填写结果、text 正文和已有执行身份，取消与超时区分。
- `internal/agent/background.go`、`internal/agent/background_tools.go`：公共后台执行与确认。
- `internal/tool/builtin/cron.go`、`internal/storage/sqlite/cron_job_repository.go`：工具入口与仓储。

<!-- locator:docs -->
## 文档

- `docs/`：中文用户文档；`devdocs/`：当前内部架构、定位入口和任务清单。
- `CHANGELOG.md`：中文变更记录；`scripts/translate_docs.py`：翻译脚本。
- `docs.en/`、`README.md`、`CHANGELOG.en.md` 为英文镜像或自动翻译产物，不手动修改。
