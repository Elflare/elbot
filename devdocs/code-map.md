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
- `internal/app/app.go`、`internal/app/runner.go`、`internal/app/dependencies.go`：生产入口、基础阶段与五条业务线的生命周期契约；Runner 持有统一退出预算。
- `internal/app/foundation.go`、`internal/app/models.go`、`internal/app/services.go`：配置、SQLite、日志、provider 客户端和共享模型／媒体／权限／诊断基础服务；媒体启动恢复也在此。
- `internal/app/wiring.go`：按 Platform → Tool → Hook → Dialogue → Background 创建上层模块，仅按各自声明供给能力。
- `internal/app/platforms.go`：Platform 上层工厂与运行模式映射。`internal/app/integrations.go`：跨线回调、入口、补全和目录接线，以及成功后的统一启动顺序。
- `internal/app/lifecycle.go`：按 Platform → Background → Dialogue → Hook → Tool 关闭各组，再释放共享模型；活跃组未退出则保留后续依赖。
- `internal/agent/module/{module,deps,lifecycle}.go`：Dialogue 上层构造、外部依赖声明、所属资源关闭；组合 Input／Execution／命令和单轮协议，拥有 Session／Request／Turn，安装线内同步回调。
- `internal/agent/module/signals.go`、`agent_notifications.go`、`model_signals.go`：Dialogue 通知、重试、会话清理与前台接管订阅；接管通知连接 ExecutionCoordinator，订阅统一释放。
- `internal/tool/module/{module,deps,lifecycle}.go`：唯一工具目录、核心 Manager、预加载／状态、Skill／Memory／文件服务的构造及所属后台加载等待；不依赖其他上层模块。
- `internal/hook/module/{module,deps,lifecycle}.go`：Hook 核心、控制与插件进程，初次 Reload 在跨线回调接好后启动；Dialogue 自行创建 Request／身份适配的 HookBridge。
- `internal/platform/module/{module,deps,lifecycle}.go`、`platform_caller.go`：平台适配器、发送／通知、运行和关闭，平台 API 能力解析与补全／目录对接；module 维护运行平台集合，服务模式全部平台退出后仍等待外部停止。
- `internal/background/module/{module,deps,lifecycle}.go`：后台执行对接、维护任务、Cron manager/service、Cron 工具注册、Elnis 恢复／服务／HTTP workers，关闭时不释放借用的 Dialogue／Tool／基础服务。

<!-- locator:contextinfo -->
## 公共上下文事实

- `internal/contextinfo/`：Conversation、Actor、Execution、Model 定义与独立存取。
- `internal/platform/platform.go`、`internal/platform/cli/message.go`：平台消息投影与本地 CLI 来源。
- `internal/security/`：身份解析、权限与风险策略。
- `internal/agent/dialogue/execution_context.go`：固定模型事实、接管时刷新来源与身份；RefreshExecution 从 Execution 内存快照应用接管字段，保留调用方的新工具状态，不逐工具查询会话。
- `internal/request/manager.go`、`internal/turn/execution.go`、`internal/session/binding.go`：执行关联的实际建立与有效性。

<!-- locator:signal -->
## 信号与订阅

- `internal/signal/signal.go`、`executor.go`：`Signal[T,R]`、原子订阅快照、每信号并发／容量／超时选项、同步和异步发布。`result.go`：实际结果、部分回执、`Resolved` 单结果构造及无等待 worker 的完成转交。
- `internal/signal/queue.go`：有界并发、按 key 排队、取消回收容量；`connection.go`、`shutdown.go`：断开、取消和真实退出；`diagnostics.go`：独立有界 stderr 诊断。
- `internal/events/notification.go`：纯文本通知实体、目标、回执及全局 `NotificationRequested`。
- `internal/events/logging.go`：九字段公共日志契约、整数枚举、`LogIdentity`、`LogSubmitted` 和 `EmitLog`；发布入口验证契约，仅复制业务填写的身份，不从 context 补值。
- `internal/logging/signals.go`：日志中心自身订阅、接收记时、分类队列、消费过滤、写入及短写检查和停止准入，不重复发布入口已完成的契约校验。关闭与文件资源由 `logging.go` 管理。
- `internal/background/logging.go`：LogBackground 作为 Agent 后台准备与 Cron／Elnis 来源的公共发布管理口，只接收完整九字段并 EmitLog；不接业务对象、不补身份、不推导下层结果。模型、回复和工具继续归所属日志路径。
- `internal/agent/events/`：Agent 实例事实信号。
- `internal/events/platform.go`：平台连接全局事实契约；各适配器直接发布。
- `internal/agent/platform_signals.go`、`internal/cron/platform_signals.go`：Hook 协调模块与 Cron 自行订阅、按平台排队并关闭；`StartPlatformEvents` 在装配完成后启动订阅，`BeginClose`／`Close`／`Done` 管理退出。
- `internal/agent/logging.go`：复制来源显式提供的固定身份并提交完整记录，不维护日志投影订阅。输入、超时、确认、权限拒绝、模型、工具和回复在实际操作来源组织事实日志，主对话终止失败归 dialogue.Runner。`internal/session/naming_logging.go`、`internal/modelmgr/logging.go` 保留有实际消费者的来源事件及同步日志回调，独立于通知订阅。
- `internal/session/naming_signals.go`：Scheduled／Completed 两种命名信号，完整终态字段定义在 `types.go`；`naming_lifecycle.go` 等待生产者退出后断开日志连接。`internal/modelmgr/signals.go` 的重试事件固定 Identity 并携带 HTTP 来源声明的尝试结果。
- `internal/agent/module/signals.go`、`agent_notifications.go`、`model_signals.go`：Dialogue 通知、模型重试与会话清理订阅及生命周期；模型提示跟随 CallContext，失败事实由来源解除取消。CLI 展示订阅归 `platform/cli/ui.go`。

<!-- locator:config -->
## 配置、资产与日志

- `internal/config/`：读取、默认资产、模块声明、provider／state／tag 及只读检查。
- `internal/config/definition.go`、`internal/config/provider.go`：配置规则、客户端模式校验与独立厂商文件加载；重载沿用启动时的文件路径及主配置目录密钥解析规则。
- `internal/doctor/`：配置诊断报告；`internal/command/builtin/doctor.go`：超管命令入口。
- `internal/logging/`：日志中心订阅全局记录，分类排队、写入、轮转与读取；`signals.go` 按业务指定等级过滤，`record.go` 负责 text 的 Unicode 字符限长、JSON 正文按来源顺序原生嵌入、JSONL 编码及含行尾的 64 KiB 上限检查。文件写入器只由中心管理。
- `internal/logging/reader.go`：反向分块兼读 JSONL／历史文本；外层与 Content 业务条件分别匹配后计入上限；run_source 对 JSON 键和 toolrun/tool_call 的固定 text 开头精确匹配，不扫描工具正文或补造旧来源；LogQueryResult 返回有效记录、扫描坏行数量及有界原文样本，保留读取范围、取消和单行上限。
- [配置文档](../docs/configuration.md)。

<!-- locator:agent-chat -->
## Agent 对话

- `internal/agent/*_deps.go`：各业务消费者的专属依赖与消费接口；较小组件的声明位于对应业务文件。中层业务构造不安装跨服务回调；线内接线归各业务线组合包，跨线接线归 App，不提供集中 Agent 构造。
- `internal/agent/message.go`、`internal/agent/input.go`、`internal/agent/background.go`：消息分发、普通输入与后台准备；入站和输入入口仅为自己执行的准备操作报告失败，转交错误不补报；后台准备日志交 LogBackground，RunBackground 保留业务结果与 error。
- `internal/agent/dialogue/runner.go`、`internal/agent/dialogue/preparation.go`：公共单轮及内部 Preparer／ToolExecutor／MessageStore／提交／Prompt 构造、加载与准备；Runner 归一取消、暂停和替换后，通过 ReportFailure 统一终态失败日志与通知，入口复用该报告函数；`internal/agent/chat/loop.go`、`internal/agent/responses/loop_deps.go` 声明各自的协议构造输入。
- `internal/agent/dialogue/materials.go`：协议无关的完整消息投影、公共片段 codec、ArchiveMaterials／PrepareMaterials；Chat／Responses 共用完整请求的媒体解析入口，委托 media 统一统计总量及选择交付方式；稳定 ID 归档与临时请求副本分离，缺图通知；`message_store.go` 保留完整正文与 segments，`commit.go` 只把已提交投影交给循环。
- `internal/agent/dialogue/model_call.go`、`internal/agent/dialogue/system_prompt.go`：共同模型 Hook 与 Prompt 来源；CallObservation 分别持有业务 Event 和日志 LogRecord，发布时固定身份和用量副本，Usage 整份缺失时仍写出四个 null 计数键。
- `internal/agent/chat/`：Chat Loop、请求、历史、摘要与 seed；完整请求的媒体统一交 Dialogue 解析，恢复耗尽返回最终原因，取消时保留部分输出。
- `internal/agent/responses/loop.go`、`internal/agent/responses/model_call.go`：Responses Loop、续链、pending／接管及恢复；input.go 合并未展开的前缀与新增输入，context.go 将完整请求的公共材料一次性交给 Dialogue 解析，continuation.go 只提取未展开的续接片段。恢复重放重新解析完整请求，准备和调用终止失败返回 Dialogue。
- `internal/agent/responses/native_call.go`：实际原生调用、冻结参数与有序输入引用及终态保存；按输出项、内容索引保留 text／refusal 增量与原生字段，完成事实覆盖对应部分，中断内容不成为完成响应或可执行工具调用。
- `internal/agent/responses/context.go`、`internal/agent/responses/persistence.go`：从已提交输入／输出重建原生窗口与原子提交，不从历史 HTTP 请求恢复；公共内容交 Dialogue 处理，原生 items 由 Responses 组装。
- `internal/agent/responses/tool_inputs.go`、`internal/agent/responses/input_format.go`：冻结工具定义与输入／seed 格式。
- `internal/agent/dialogue/commit.go`、`internal/agent/dialogue/reply_commit.go`：消息事务准入、ToolBatch 与最终回复。
- `internal/agent/segments.go`、`internal/agent/inbound_media.go`：有序图文与入站媒体。

<!-- locator:protocol-routing -->
## 协议路线

- `internal/agent/routes/`：按 APIType 登记 Loop／Compactor／Material 的注册表，不保存厂商或客户端。
- `internal/agent/module/module.go`、`internal/app/models.go`：协议业务能力装配，以及启动／重载共用的具体客户端工厂；协议 Loop 在准入时检查客户端能力。
- `internal/agent/dialogue/loop.go`、`internal/contextmgr/compact_contract.go`、`internal/session/material.go`：公共消费与材料交接契约。
- `internal/llm/origin.go`、`internal/modelmgr/compatibility.go`：持久化归属与纯兼容判断。
- `internal/llm/client.go`：独立文本调用契约与可空 int64 用量计数；`internal/llm/chatcompletions/response.go` 保留精确整数、明确零及别名回退。`internal/agent/events/events.go` 的 `CloneUsage` 复制用量指针快照。
- `internal/llm/chatcompletions/logging.go`：以九字段 text 契约记录请求摘要、最新消息和首次系统消息，来源显式提供身份，保留媒体内嵌数据预处理。
- `internal/llm/api_type.go`、`internal/tool/availability.go`：模型 API 类型与工具可用性；`contextinfo.Model.APIType` 承载当前主对话模型事实。

<!-- locator:commands -->
## 命令与补全

- `internal/command/`、`internal/command/builtin/register.go`：Router、命令契约与注册。
- `internal/command/builtin/`：内置命令；`internal/agent/command_executor.go`：权限、冲突、命令结果的同步通知及发送成功后的 Continuation。
- `internal/command/builtin/log.go`：运行、审计及 Elwisp 日志的筛选、展示、帮助和补全；formatLogContent 按 JSON token 原顺序展示，解码字符串并保留整数精度，raw 与等级独立。工具调用由 /audit -t 和 --run_source 查询；/log 不提供工具筛选。Hook 按模块筛选，坏行单独提示。`usage.go` 按 Content 聚合 llm_usage，未知或溢出按项传播。
- `internal/completion/`、`internal/agent/completion.go`：公共结构化补全与专属查询依赖；平台直接接收补全服务。
- `internal/agent/file_rollback.go`：文件命令准入；`internal/command/builtin/rollback.go`：撤销命令。
- [命令文档](../docs/commands.md)。

<!-- locator:request-turn -->
## Request、Turn 与状态

- `internal/request/`、`internal/turn/`：请求树、取消、阶段、pending、确认与 Execution。
- `internal/agent/execution.go`、`internal/agent/execution_run.go`：前后台执行与单轮收尾，消费 Session 接管事实；`internal/turn/execution.go` 保存一致的接管上下文及会话快照。
- `internal/agent/execution_admission.go`、`internal/agent/execution_input.go`：准入、输入、启动冲突后的重新分流、追加确认与续跑。
- `internal/agent/execution_lifecycle.go`：追加确认等待及关闭。
- `internal/agent/confirmation.go`、`internal/agent/risk_confirmation.go`：风险确认协调、来源日志与文案；`internal/turn/manager.go` 的 RiskConfirmationWait.Wait 在实际状态转换处返回明确 ResultStatus，保留业务响应／接受 bool，区分失效等待、取消和超时。
- `internal/runtime/`：状态数据与耗时计算；`internal/agent/status.go`：同步校验执行归属、保存、发布已提交状态，接管后刷新来源。CLI 文案归 `platform/cli/status_format.go`。

<!-- locator:tool -->
## Tool Runtime、工具状态与文件

- `internal/tool/`、`internal/tool/builtin/`：注册、schema、Runtime 执行器与内置工具。各工具自行组织日志摘要并选择 128／256 字符预算；`internal/tool/tool.go` 的 TruncateLogContent 仅提供调用方主动使用的 Unicode 截断，不自动处理结果。
- `internal/toolrun/`：视图、路由、权限风险、确认及调用参数媒体引用。
- `internal/toolrun/state.go`、`internal/toolrun/preload.go`、`internal/toolrun/background.go`：状态提交、预加载与后台白名单；预载失败与跳过由来源记录身份、原因和结果。
- `internal/agent/tool_directive.go`、`internal/agent/tool_cache.go`：输入指令和状态提交编排。
- `internal/fileops/`：共享文件操作、编辑、目标锁和撤销备份。
- `internal/workspace/`、`internal/session/workspace.go`、`internal/sandbox/`：工作目录、持久化与后台路径限制。
- `internal/tool/media_runtime.go`：工具／Skill 媒体准备与导出缓存。

<!-- locator:tool-flow -->
## 工具调用链路

- `internal/agent/dialogue/tool_execution.go`：路线共用的 ToolRun 入口与协议提交器，批次准备失败只记批次日志。
- `internal/agent/toolrun_adapter.go`：Hook、子请求、确认、调用记录与状态提交桥接；请求登记、RecordToolCall 和发现状态保存各自返回独立 LogResult 与原始错误，Runner 不再推导这些操作的结果。
- `internal/toolrun/runner.go`：Runner 借用共享 Manager 顺序准备和确认、并发执行、按原调用顺序处理完成并 Flush 整批保存；newRecord 复用基础九字段与当次身份，recordCall 在 Execute 返回后填写调用日志结果、等级和查询事实，仅发布一份 audit JSON。参数日志限 256 字符，工具返回的独立摘要不再截断。调用前固定 runSource 写入 JSON，接管不改写已开始调用；后续操作分别使用所属来源 LogResult。
- `internal/toolrun/toolrun.go`：工具业务解析及原生／远程调用中间层，Execute 保留业务结果、原始错误、ExecutionStarted 和独立正文；未进入调用时声明明确结果及原因，不组装或发布调用日志。
- `internal/toolrun/logging.go`：logTool 统一接收 ctx 和完整 LogRecord，直接 EmitLog；工具调用记录和编排操作记录均由 Runner 来源组装；全局队列和落盘归 internal/logging。
- `internal/tool/executor.go`、`internal/toolrun/elwisp_exec.go`：原生／远程执行入口，保留业务 Result、原始 error 与真实 ExecutionStarted；LogResult.Content 传递工具独立正文；未进入调用的结果在入口明确，已进入调用的日志结果交 Runner 统一填写。PolicyDeniedError 只表达业务拒绝事实。
- `internal/agent/dialogue/message_store.go`：对话写入边界，返回独立 LogResult 和原始 error；来源声明保存结果，ToolCommitter 各阶段继续传递 LogResult，业务提交及检查点规则不解析日志正文。`internal/storage/tool_batch.go`、`internal/storage/sqlite/tool_batch.go`：调用数组／结果数组关联和完整批次校验。
- `internal/storage/sqlite/tool_call_repository.go`：调用记录持久化。

<!-- locator:skill -->
## Skill 与 ELyph

- `internal/elyph/`：语言解析与 lint。
- `internal/tool/skill/`：扫描、catalog、创建、finalize、reload 和 runner。
- `internal/tool/skill/command_tool.go`、`internal/tool/skill/runner.go`：执行 AgentSkill TOML 命令与 Go Skill，每次读取技能 `.env` 并合并基础子进程环境；Manager／scanner 为命令工具传递启动环境。
- `internal/tool/skill/agent_manifest.go`、`internal/tool/skill/go_source.go`：AgentSkill manifest 与 Go 源码／编译。
- `internal/tool/skill/media.go`：媒体参数与 stdout 媒体。
- `internal/config/assets.go`：内置 Skill 说明。

<!-- locator:hook -->
## Hook 与插件

- `internal/hook/event.go`、`internal/hook/manager.go`：事件与普通 Hook 流水线；Handler／Run 返回 Event、LogResult、error，Notify 返回 LogResult、error，未处理 skipped。来源明确结果，流水线不从包装错误分类；点、插件和变更事实写入正文。
- `internal/hook/logging.go`：LogHook 只接收完整九字段，解除取消后 EmitLog；不补身份、不组织正文、不判断业务。
- `internal/hook/rules/`、`internal/hook/control/`：规则、一次性 exec 与管理入口；action.go 生成唯一工具调用 ID，接收独立 logContent 并立即发布调用记录；exec.go 使用发送来源 LogResult 发布独立 hook_tool_log，handleProtocolRequest／readV2Response 在包装错误前传递来源结果，响应写入失败保留此前取消事实；Receipt 仅业务使用。日志 text 交中心截断，JSON 字符串由来源限长；exec_process_unix.go／exec_process_windows.go 保留宿主终止事实，execExitCanceled 核对原生退出状态。配置、协议和 stderr 来源均交 LogHook 发布。
- `internal/hook/runtime/`、`internal/hook/protocol/`：持久进程、RPC、waiting 和 hook.v2 帧。tool_bridge.go 分别记录调用和发送，输入无效仍记录 INFO／rejected，保留真实触发 Actor 与独立授权主体；worker.go 声明自身请求及停止结果，先固定退出事实再唤醒等待者，manager.go 透传并管理业务生命周期；routes.go 组织 continuation 点记录；重载 commit 独立返回 LogResult，worker 不从回调 error 判定；media.go 保留清理事实和调用方预算。
- `internal/hook/call_policy.go`：消费方工具调用只读策略。
- `internal/hook/media.go`、`internal/hook/output/`：媒体 API 与输出协议。
- `internal/agent/hooks.go`、`internal/processenv/`：Agent 桥接与进程环境；同步 Hook 结果和错误返回调用方，不自行重复通知；独立异步 Hook 保留入口报告，桥接自身记录走 LogHook。toolrun_adapter.go 的准备／完成接口返回独立 LogResult，output.go 的 PrepareAssistant 透传来源结果。
- [Hook 文档](../docs/hooks.md)。

<!-- locator:output -->
<!-- locator:notification -->
## 输出、投递与通知

- `internal/delivery/`、`internal/delivery/dispatch/`：输出意图、Target／业务 Receipt、平台路由与媒体准备；需要日志结果的发送接口单独返回 events.LogResult。media.go 在媒体准备、解析来源声明结果，包装错误前保留日志正文，发送后补实际消息及输出索引；媒体缓存故障独立记录，不改送达结果或重发。
- `internal/notification/manager.go`：现有通知管理器连接全局信号，解析目标、恢复原来源、校验 binding，持有各平台信号和独立发送队列；满队列异步 WARN、转交结果并管理关闭。`rules/` 组织重试／Hook 文案与视觉回退去重。
- `internal/platform/module/notifications.go`：创建管理器、注册平台并连接适配器；平台退出告警按仍运行的平台和配置超管发布通知。模块 `lifecycle.go` 协调关闭。
- `internal/platform/{cli,qq-onebot,qqofficial,telegram}/notifications.go`：各平台连接通知信号、发送纯文本并返回回执；消息平台经 Delivery.Manager 发送，CLI 保留通知协议并调用 Delivery.LogSendResult，保留等级、原远端连接和部分发送错误。
- `internal/agent/output.go`、`internal/agent/turn_output.go`、`internal/agent/execution_output.go`：发送 Hook、前后台策略与接管输出；SendOutputs 传递独立 LogResult 和原始错误；正文与流式发送另外保留业务 Receipt，发送来源在 Content 中保留消息及输出索引，独立于工具调用日志。流替换与结束回执合并去重，保留部分成功和独立切片。
- `internal/agent/dialogue/reply_commit.go`：最终回复落库、发送顺序与正文业务回执关联；附加输出使用独立 LogResult；终态日志接收实际操作结果，关联及部分送达继续使用 Receipt，不从最终合并错误反推。
- `internal/agent/dialogue/output.go`：正文输出与可选 `ReasoningStream` 消费契约；SendPreview 可变参数按工具传递完整提示，多行正文不拆段，通用流程提示占一段；OutputSender 为每段加一次前缀，合并后执行一次输出 Hook 并发送通知。思考只接收原始增量和结束边界，状态记录独立注入执行组件。`execution_output.go` 每个思考增量刷新接管来源，CLI 实现展示策略。

<!-- locator:platform -->
## 平台适配

- `internal/platform/platform.go`、`internal/platform/builtin/`：公共契约与装配；连接成功直接发布 `events.PlatformConnected`，不暴露实例连接信号。
- `internal/platform/media.go`、`internal/platform/refcontext/`：历史 segments、敏感来源清洗和引用恢复。
- `internal/platform/cli/`：本地／远程 CLI 与 TUI；`ui.go` 管私有展示目标、状态订阅及关闭，`status.go` 按目标校验版本与合并更新，`reasoning.go` 实现每调用的直接思考流与开始／结束边界，`status_format.go` 格式化状态。
- `internal/platform/module/ui.go`：启动前连接 CLI 状态信号，并提供可选的 `StartReasoning` 能力；平台生命周期协调关闭 UI。
- `internal/platform/qq-onebot/`：协议转换、forward 展开、长消息转发及回执。
- `internal/platform/qqofficial/`、`internal/platform/telegram/`、`internal/platform/headless/`：各平台 runtime。

<!-- locator:session -->
## Session

- `internal/session/service.go`、`internal/session/types.go`：服务与领域接口。
- `internal/session/binding.go`、`internal/session/coordination.go`、`internal/session/signals.go`：current、准入锁与绑定变化。
- `internal/session/origin.go`、`internal/session/promotion.go`、`internal/session/background.go`：原生归属、前台接管与后台准备；promotion 提交后同步发布实例级 ForegroundActivated 信号，接收方仅更新执行内存。
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

- `internal/modelmgr/`：固定选择、兼容性、目录缓存、状态保存与重试信号；`selection.go` 的 Selection 固定客户端、Origin 及窗口快照。
- `internal/modelmgr/providers.go`：厂商重载串行准备并一次发布，旧调用持有旧快照；`context_window.go`：模型窗口解析与快照所属缓存，供 contextmgr 查询。
- `internal/llm/`：独立文本、模型目录、消息／工具／Usage 及 Extra 校验。
- `internal/llm/httpclient/`：传输、重试、SSE 与超时。
- `internal/llm/chatcompletions/`、`internal/llm/responses/`：独立协议客户端。
- `internal/llm/responses/client.go`、`internal/llm/responses/tools.go`、`internal/llm/responses/compact.go`：请求冻结、store 偏好、工具权限与原生压缩。
- `internal/llm/responses/stream.go`、`internal/llm/responses/error.go`：原生流事件与协议错误解析，协议诊断由来源写入 Content。`response.go` 容错解析可空用量，`client.go` 使用九字段 text 契约记录请求。

<!-- locator:storage -->
## Storage 与 SQLite

- `internal/storage/storage.go`、`internal/storage/session_metadata.go`：领域模型、repository 契约及 metadata 保留。
- `internal/storage/sqlite/`、`internal/storage/sqlite/migrations.go`：存储实现与数据库迁移；`nullable_context_summary_tokens` 保留摘要数据、外键及索引，使未知用量可保存为 NULL。
- `internal/storage/dialogue.go`、`internal/storage/sqlite/dialogue_repository.go`：ToolBatch／原生提交事务与 checkpoint 比较。
- `internal/storage/sqlite/native_snapshot.go`、`internal/storage/sqlite/native_material.go`：不可变调用快照、seed 与新 Session 原子保存；seed 空媒体列表统一写 `[]`，`normalize_native_seed_media_ids` 迁移修正已有 `null`。
- `internal/storage/sqlite/media_history.go`、`internal/storage/sqlite/media_lifecycle.go`：历史媒体关联、媒体临时引用恢复与清理认领。

<!-- locator:elnis -->
## Elnis / Elvena / Elwisp

- `internal/elvena/`、`internal/elnis/types.go`：公共协议、来源与 Elnis 类型。
- `internal/elnis/http.go`、`internal/elnis/service.go`：HTTP runtime、队列、去重分发与中断执行恢复裁决；业务日志在 JSON 正文中保留查询键和标签，Session 关联写入 Identity。
- `internal/elnis/llm.go`、`internal/elnis/direct.go`、`internal/elnis/store.go`：模型／直接投递流程与状态存储，来源分别明确事件终态及保存失败并交 LogBackground，direct 及目标发送传递独立 LogResult 与原始业务错误，业务错误不投影为下层日志结果。
- `internal/elnis/auth.go`、`internal/elnis/prepare.go`、`internal/elnis/targets.go`：鉴权、规范化与投递裁决。
- `internal/elnis/media.go`、`internal/elnis/outbox.go`：媒体、持久化投递与恢复；deliverReport／recoverReports 返回来源 LogResult 和原始 error，失败状态保存及释放保留各阶段事实；恢复不按包装错误猜结果，Receipt 继续用于关联与存档，不以日志结果改变重试规则。
- `internal/storage/sqlite/elnis_event_repository.go`、`internal/background/`：事件仓储、恢复状态与引用释放的原子执行，以及公共后台类型。
- [内部架构](elnis-elwisp.md)、[功能说明](../docs/elnis.md)、[配置与协议示例](../docs/elnis-usage.md)。

## 媒体中心

- `internal/media/`：导入、图片处理、请求／发送解析、引用与清理。
- `internal/media/manager.go`：媒体导入、读取、按需上传及 PresignGet；按媒体 ID 加锁复用签名 URL，临近到期刷新，错误交所属业务出口报告。`internal/storage/sqlite/media_repository.go`：媒体元数据与独立签名缓存读写；`media_signed_urls` 随媒体删除级联清理。
- `internal/media/lifecycle.go`：清理轮次与逐对象删除；生成 cleanup_id 和完整分类计数，异常优先的明细正文最多 50 行；返回来源 LogResult 和原始 error，先保留逐对象结果再合并并行错误，取消不覆盖独立删除失败。
- `internal/media/reference.go`：媒体引用与存活持有；RetainSessionToolArguments 返回事务来源 LogResult 和原始 error，无引用声明 skipped，Runner 不二次分类。
- `internal/media/platform.go`、`internal/media/history.go`：平台媒体、历史批量获取（缓存、去重与尝试预算）及跨库历史对账。
- `internal/tool/builtin/chat_history.go`、`internal/tool/builtin/get_media.go`：历史查询与显式媒体获取。
- `internal/tool/builtin/view_image.go`：Responses 看图工具，来源／图片序号选择、本地权限及多模态结果。
- `internal/delivery/dispatch/media.go`：输出媒体准备和回执缓存；缓存失败发布 WARN 日志，保留实际发送回执和错误，不触发重发。

<!-- locator:cron -->
## Cron 与维护

- `internal/cron/`：任务 CRUD、调度、执行、报告与投递恢复。
- `internal/cron/service.go`、`internal/cron/manager.go`：用户任务通过 Handler 返回独立 LogResult 和原始 error，业务正文不重复发布运行记录；调度器统一添加任务名、handler、耗时并组织九字段记录，结束事件名拼接 handler，正文连续非空行。最终结果在状态保存后发布，保留重新读取和保存的独立错误；触发和投递审计仍用 JSON 独立记录。补执行依据任务及 DeliveryState／DeliveryToken，日志结果不反向控制业务。
- `internal/cron/models.go`、`internal/cron/delivery.go`、`internal/cron/recovery.go`：任务模型、逐项投递及连接恢复；共享投递函数返回目标和来源诊断，恢复入口在最终运行记录中保留正文，逐目标审计独立发布。
- `internal/maintenance/`：维护任务；清理方法组织独立结果、text 正文和原始 error，Cron Handler 直接传递，由调度器统一发布运行记录；取消与超时区分。媒体清理在并行删除错误合并前保留各自结果，独立失败不被取消覆盖。
- `internal/agent/background.go`、`internal/agent/background_tools.go`：公共后台执行与确认。
- `internal/tool/builtin/cron.go`、`internal/storage/sqlite/cron_job_repository.go`：工具入口与仓储。

<!-- locator:docs -->
## 文档

- `docs/`：中文用户文档；`devdocs/`：当前内部架构、定位入口和任务清单。
- `CHANGELOG.md`：中文变更记录；`scripts/translate_docs.py`：翻译脚本。
- `docs.en/`、`README.md`、`CHANGELOG.en.md` 为英文镜像或自动翻译产物，不手动修改。
