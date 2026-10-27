# ElBot 代码地图

按 locator 查代码入口；调用链和关键约束见[架构说明](architecture.md)。

## 定位方法

1. 从 [AGENTS.md 的 locator 表](../AGENTS.md#快速定位流程) 选择任务关键词。
2. 搜索对应章节，例如：

```bash
rg -n '^<!-- locator:tool-flow -->$' devdocs/code-map.md devdocs/architecture.md
```

3. 用 `nrf <文件> -s <起始行> -e <结束行>` 读取命中章节。
4. 定位到 Go 文件后，已知符号用 `nls inspect <文件> <符号>`；未知符号先用 `nls ds <文件>`。

<!-- locator:startup -->
## 启动与装配

- `cmd/elbot/main.go`、`internal/launcher/cli.go`：程序入口、参数与运行模式。
- `internal/app/` 六文件按下层接口已存在来实现公共流程，五条业务线已提供根包总管，配置独占已实现；Platform 下层与接收方契约、媒体和模型构造入口及进程入口适配仍待完成。13 个原生产文件和 12 个原测试文件保存在 `*.go.bak` 中；职责与调用契约见 [App 包设计](app-design.md)。
- `internal/app/entry.go`：启动参数、局部阶段列表、统一阶段执行与耗时、Lifecycle／可选 Waiter、唯一总管集合、按顺序启动与按能力等待。
- `internal/app/environment.go`：验证运行模式，定位配置并转为解析符号链接后的绝对路径；TUI／Service 通过 `gofrs/flock` 非阻塞取得对应 `.lock` 文件的独占锁。
- `internal/app/environment_test.go`：配置定位、路径别名、配置替换保存、跨进程互斥、正常及异常退出后的重新取锁测试，可独立运行。
- `internal/app/resources.go`：加载配置、构造共享资源并保留部分失败结果；向 `NewMediaCenter`、`NewModelManager` 交付完整配置，媒体与模型侧自行初始化；独立历史库由 App 持有并在业务使用方退出后关闭。
- `internal/app/wiring.go`：用具体类型构造各线、连接公开能力并登记到唯一集合；构造失败保留非 nil 部分总管，Platform 按目标接口调用。
- `internal/app/shutdown.go`：用 `shutdownStep` 声明清理顺序，`runShutdownStep` 按统一 30 秒期限返回结果枚举，`runShutdownSteps` 顺序执行并汇总；普通失败后继续，超时停止后续清理，资源关闭后最后释放配置锁。
- `internal/app/client.go`：加载配置并调用 `platform.RunClient`，消费 `ClientResult`；明确运行直接返回错误，自动尝试将可回退结果转为信号，实际本地启动由进程入口决定。
- `internal/agent/`：`Supervisor` 是 App 对接入口；`supervisor.go` 定义总管与公开能力，`deps.go` 声明共享资源和 `ToolCapabilities`、`HookCapabilities`、`PlatformCapabilities` 三类业务接口，`wiring.go` 拆分配置、构造线内组件并接线，`lifecycle.go` 管理启停，`signals.go` 持有通知与状态订阅。
- `internal/tool/supervisor.go`、`wiring.go`、`lifecycle.go`：Tool 对外入口、组件装配与生命周期。
- `internal/hook/`：Hook 总管、外部桥接与生命周期；装配内部规则、流水线、控制与插件进程。
- `internal/platform/`：根部已建立对外与装配骨架，发送路由和通知管理位于内部子包；`module/`、`builtin/` 保留待拆分的旧实现并标注删除，调用边界尚未接通。
- `internal/background/supervisor.go`、`deps.go`、`wiring.go`、`lifecycle.go`：对外总管、借用依赖、最小能力注入与生命周期协调。cronBridge 转换存储，elnisBridge 转换存储与媒体。根部持有 Tool／平台订阅，显式启动 Cron Manager，Elnis 就绪后放行 Cron 平台通知；Wait 及时上报整线致命故障，BeginClose 取消组件与订阅，Done 等待实际退出，不公开内部 Runner。
- `internal/background/execution/`：任务目录、后台来源准备与 Agent 公开执行入口转接；`cron/`、`elnis/`、`maintenance/` 承接各业务。

<!-- locator:contextinfo -->
## 公共上下文事实

- `internal/contextinfo/`：Conversation、Actor、Execution、Model、BackgroundKind 定义与存取；后台来源事实不包含路径或路径限制。
- `internal/platform/platform.go`、`internal/platform/cli/message.go`：平台消息投影与 CLI 来源。
- `internal/security/`：身份解析、权限与风险策略。
- `internal/agent/dialogue/execution_context.go`：模型事实与接管上下文刷新。
- `internal/request/manager.go`、`internal/turn/execution.go`、`internal/session/binding.go`：执行关联、快照与绑定。

<!-- locator:signal -->
## 信号与订阅

- `internal/signal/`：泛型信号、订阅快照、有界调度、结果、关闭及设施诊断。
- `internal/events/notification.go`、`platform.go`、`logging.go`：全局通知、平台连接与日志契约。
- `internal/agent/contracts.go`：公开 Hook 回调及载荷、状态和绑定快照、后台请求结果（复制元数据为 JSON 原始值）；不暴露内部组件或 Binding 句柄。
- `internal/agent/signals.go`：Agent 信号源构造及通知、重试、会话清理与接管订阅。
- `internal/background/cron/lifecycle.go`：Cron 平台连接通知的启动前合并、独立消费队列与关闭等待。
- `internal/session/naming_signals.go`、`naming_logging.go`、`naming_lifecycle.go`：命名信号、日志与退出。
- `internal/modelmgr/signals.go`、`logging.go`：模型重试信号与日志。
- `internal/agent/share/logging.go`、`internal/background/share/logging.go`：所属业务日志发布。

<!-- locator:config -->
## 配置、资产与日志

- `internal/config/`：配置读取、默认资产、模块声明、provider／state／tag 与只读检查。
- `internal/config/definition.go`、`provider.go`：配置规则与厂商文件加载。
- `internal/doctor/`、`internal/command/builtin/doctor.go`：配置诊断与命令入口。
- `internal/logging/logging.go`、`signals.go`：日志中心、订阅、分类队列与文件资源。
- `internal/logging/record.go`、`reader.go`：JSONL 格式化、限长、反向查询和坏行报告。
- [配置文档](../docs/configuration.md)。

<!-- locator:agent-chat -->
## Agent 对话

- `internal/agent/{input,execution,confirmation,hooks,output,tools}/`：输入、执行、确认、Agent 自有扩展点、输出与工具桥接；依赖契约随消费者放置。
- `internal/agent/share/dialogue.go`、`hooks.go`：多子包共用的循环、提交、材料、消息转换和接管提示，以及 Hook 载荷与上下文辅助；独用消费接口留消费者，不反向依赖实现子包。
- `internal/agent/share/status.go`、`execution/status.go`、`dialogue/model_call.go`：模型完成、执行状态、视觉降级的内部事实定义。
- `internal/agent/input/message.go`、`input.go`：消息分发与普通输入准备。
- `internal/agent/background.go`：后台执行、复制、清理与接管查询公开入口，以及调用方和 Agent 的取消连接；`input/background.go`、`background_deps.go`：后台身份、会话、工具准备与独用消费契约。
- `internal/background/execution/runner.go`、`runner_deps.go`：任务目录与后台来源准备，以及仅含 Agent.RunBackground 的消费契约。
- `internal/agent/dialogue/runner.go`、`preparation.go`：公共单轮、组件构造、输入准备与终态失败报告。
- `internal/agent/dialogue/materials.go`：消息归档与请求媒体准备；共享投影与片段编解码在 `share/dialogue.go`。
- `internal/agent/dialogue/model_call.go`、`system_prompt.go`：公共模型 Hook、调用观察与 Prompt 来源。
- `internal/agent/dialogue/message_store.go`、`commit.go`、`reply_commit.go`：消息、工具批次及最终回复提交。
- `internal/agent/chat/`：Chat Loop、请求、历史、摘要与 seed。
- `internal/agent/responses/loop.go`、`model_call.go`、`continuation.go`：Responses 循环、续链、pending 与恢复。
- `internal/agent/responses/native_call.go`：原生调用、流增量、参数归档与终态保存。
- `internal/agent/responses/context.go`、`input.go`、`persistence.go`：原生窗口重建、完整请求组装与提交。
- `internal/agent/responses/tool_inputs.go`、`input_format.go`：工具定义与输入格式。
- `internal/agent/share/segments.go`、`internal/agent/input/inbound_media.go`：有序图文与入站媒体。

<!-- locator:protocol-routing -->
## 协议路线

- `internal/agent/routes/`：按 APIType 登记 Loop／Compactor／Material。
- `internal/agent/wiring.go`：消费完整配置与外部业务能力，装配线内组件和协议路线；App 资源准备见 `internal/app/resources.go`。
- `internal/agent/share/dialogue.go`、`internal/contextmgr/compact_contract.go`、`internal/session/material.go`：公共循环、压缩和材料契约。
- `internal/llm/origin.go`、`internal/modelmgr/compatibility.go`：持久化归属与兼容判断。
- `internal/llm/api_type.go`、`internal/tool/registry/availability.go`：模型 API 类型与工具可用性。

<!-- locator:commands -->
## 命令与补全

- `internal/command/`、`internal/command/builtin/register.go`：Router、命令契约与注册。
- `internal/command/builtin/`、`internal/agent/input/command_executor.go`：命令实现、权限、冲突、结果通知与 Continuation。
- `internal/command/completion.go`：命令通用补全 token、候选与替换范围辅助。
- `internal/command/builtin/log.go`、`usage.go`：日志查询、正文展示和用量统计。
- `internal/completion/`、`internal/agent/input/completion.go`：结构化补全与查询依赖。
- `internal/agent/input/file_rollback.go`、`internal/agent/share/file_rollback.go`、`internal/command/builtin/rollback.go`：文件撤销准入、共享执行绑定与命令。
- [命令文档](../docs/commands.md)。

<!-- locator:request-turn -->
## Request、Turn 与状态

- `internal/request/`、`internal/turn/`：请求树、取消与执行确认清理、stopping／attempt 收尾通知、阶段、pending、确认与 Execution。
- `internal/agent/execution/execution.go`、`execution_run.go`：前后台执行、接管与跨轮收尾。
- `internal/agent/execution/execution_admission.go`、`execution_input.go`、`execution_lifecycle.go`：准入、输入、追加确认与关闭。
- `internal/processenv/environment.go`、`process_unix.go`、`process_windows.go`：子进程环境和命令构造、平台进程组／树终止及实际退出判定；Skill、Shell、Hook、编译和文件搜索复用。
- `internal/agent/confirmation/confirmation.go`、`risk_confirmation.go`：风险确认、来源日志与提示。
- `internal/runtime/`、`internal/agent/execution/status.go`：运行状态、usage 唯一内存事实、恢复快照、执行归属校验与状态发布。

<!-- locator:tool -->
## Tool Runtime、工具状态与文件

- `internal/tool/registry/`、`execution/`、`builtin/`：工具目录、执行核心与内置工具。
- `internal/tool/share/`：公共定义、schema 构造与纯数据辅助。
- `internal/tool/share/definition.go`、`prepared.go`、`risk.go`：Tool 的必需 PrepareCall 入口；PreparedCall 固定参数、完整风险、预览和执行回调，提供 Preview、RiskAssessment、Call 与参数匹配检查。
- `internal/tool/builtin/cron_contract.go`：Builtin 消费的 CronRepository、CronSaveRequest、创建／更新请求与管理结果，由根部 bridge 实现，不引用 Storage、Config 或 Background 类型。
- `internal/tool/cron_bridge.go`：转换 Tool 与 Storage 的任务数据和持久化格式，桥接模型校验；写入成功后发布根部 CronChangedEvent，提供 ObserveCronChanged。工具由 wiring 注册，注册及信号由 Tool 生命周期释放。
- `internal/tool/chat_history/manager.go`：历史组件构造与 Backend 契约；`query.go` 统一检查搜索范围：前台禁止显式 platform／scope，后台必须同时提供；user 仅去除首尾空白，以 SenderQuery 交查询后端同时匹配 ID 或昵称，不解析特殊写法。`media.go` 处理媒体选择、序号与批次获取策略。
- `internal/tool/chat_history_bridge.go`：直接查询 Storage，通过 Storage 编解码读取历史事实，转换组件数据和 Media 历史输入，并注入按目标平台解析媒体的函数。Builtin 根据结构化 Reply 组织引用展示。
- `internal/tool/config_bridge.go`：按装配路径读取配置，向 Builtin 的 ElwispCreatorConfigProvider 提供精简配置摘要与读取错误；创建指引与告警由 Builtin 生成。
- `internal/tool/resident_memory_bridge.go`：实现 Builtin 的 ResidentMemoryService，转换调用者范围、常驻记忆数据及未找到错误。
- `internal/tool/builtin/elwisp_creator.go`、`resident_memory.go`：各自声明所需服务与数据契约，组织工具参数、指引和展示。
- `internal/tool/builtin/chat_history.go`、`get_media.go`、`view_image.go`：历史工具 schema、参数解析与展示，历史查询及媒体选择交给 chat_history；`cron.go` 负责任务规则、更新合并与展示，只调用注入的 CronRepository 和模型校验函数。
- `internal/tool/discovery/resolve.go`：解析发现请求及依赖；`Resolve` 接收已筛选对象，返回展示数据与本次使用的对象集合。
- `internal/tool/state/state.go`：完整工具声明的解码、会话读取、提交与纯元数据补丁准备；`PrepareUpdate` 供 Execution 随工具结果提交，根部只转发对外状态能力。
- `internal/tool/state/discovered.go`：已发现工具声明的规范化、合并与排序。
- `internal/tool/preload/preload.go`、`tags.go`：工具／Skill 预加载、动态说明与标签配置；运行资格由 Registry 筛选。
- `internal/tool/preload/background.go`：展开后台名称／标签，将本地预载与已适配的 ELwisp 声明统一生成待提交状态和提示材料；根部 `Supervisor.PrepareBackground` 转发请求。
- `internal/agent/input/tool_directive.go`、`internal/agent/share/tool_state.go`：输入指令与状态提交。
- `internal/tool/builtin/file_tools_ast.go`、`file_tools_directory.go`：AST 识别、目录候选与缓存、rg 枚举及流式搜索。
- `internal/tool/builtin/long_memory.go`：长期记忆格式、索引与同步；更新复用 fileops 的文件预览和提交。
- `internal/tool/builtin/web_extract.go`：正文缓存与同键在途请求。
- `internal/fileops/binding.go`、`commit.go`：操作绑定与提交校验。
- `internal/fileops/edit_service.go`、`rollback_service.go`：编辑／撤销准备、预览与提交。
- `internal/fileops/preview.go`：PrepareFile、PrepareFileEdit 与 PrepareFileWrite 准备业务转换、编辑协议和整文件写入；PreparedFile 固定存在状态、真实目标、批准内容与 revision，统一 diff 和复核提交，支持新建冲突检查与相同内容写入。
- `internal/fileops/file.go`、`atomic_write.go`、`rollback.go`：结果计算、revision 校验写入、目标锁与撤销备份。
- `internal/workspace/workspace.go`、`resolve.go`、`state.go`：工作目录契约、路径解析与状态编解码；`internal/session/workspace.go` 负责持久化适配。
- `internal/workspace/context.go`：PathScope 与目录存取能力的上下文传递；`workspace.go` 优先选择调用级目录，未指定时读取 Session 或进程目录。
- `internal/workspace/sandbox.go`：NormalizeRestrictedPath 统一受限相对路径规则，SandboxDir 计算沙盒子目录，ResolveSandboxRelativePath 组合受限绝对路径；不创建目录、组织提示或管理文件生命周期。
- `internal/tool/share/media.go`：MediaInput、MediaImportOptions、MediaInfo 与历史媒体逐项结果；包含工具需要的参数、JSON 解码约束和通用信息，ID 校验由媒体调用链承担。
- `internal/tool/media/runtime.go`：统一媒体入口、导入参数及精简结果转换，提供 IDPrefix、ValidID；历史媒体使用 Media 自有 HistoryMessage 和显式解析函数，不携带 Platform 运行类型。媒体中心和缓存根目录为私有依赖。
- `internal/tool/media/call.go`：调用状态、引用持有、释放重试与临时资源清理。
- `internal/tool/media/files.go`、`cache.go`：调用目录及文件导入导出、共享缓存使用与刷新；缓存保留期由维护任务管理。
- `internal/tool/builtin/send_file.go`：消费路径事实和 Tool 媒体能力，组织文件名称、MIME、提示与发送输出。

<!-- locator:tool-flow -->
## 工具调用链路

- `internal/agent/dialogue/tool_execution.go`：协议共用的 ToolRun 入口与提交器。
- `internal/agent/tools/toolrun_adapter.go`：Hook、子请求、确认与状态桥接。
- `internal/tool/execution/runner.go`：Manager 的批次流程，顺序准备和确认、并发执行、完成队列、逐项提交与开始／结束日志组装。
- `internal/agent/dialogue/commit.go`、`internal/agent/responses/persistence.go`：固定结果槽、逐项最终结果、批次封存与原生恢复快照。
- `internal/tool/execution/manager.go`：私有持有构造时注入的依赖，固定调用对象；原生直接调用也先准备再执行；`schema.go` 组合基础与已发现声明。
- `internal/tool/execution/prepared.go`：取得原生或远程工具的完整准备结果；`executor.go` 复核调用身份与取消状态，只执行 PreparedCall 并转换结果；`elwisp_exec.go` 分离远程请求准备与发送，固定地址、请求体和超时。
- `internal/tool/logging.go`：完整工具日志发布。
- `internal/storage/tool_batch.go`、`internal/storage/sqlite/tool_batch.go`、`dialogue_repository.go`：稳定批次关联、原槽更新、结果／统计／媒体／发现状态原子提交；`tool_call_repository.go` 提供调用统计查询。

<!-- locator:skill -->
## Skill 与 ELyph

- `internal/elyph/`、`rules.go`：语言解析、lint、公共规则卡与展示判定。
- `internal/tool/discovery/`：发现入口、摘要、详情、语言需求与激活状态更新。
- `internal/agent/dialogue/preparation.go`、`model_call.go`、`internal/tool/execution/runner.go`：输入与工具结果的规则卡组装。
- `internal/storage/dialogue.go`、`internal/storage/sqlite/dialogue_repository.go`、`internal/contextmgr/state.go`：规则展示状态提交与压缩重置。
- `internal/tool/skill/manager.go`：私有持有技能目录、注册目录、媒体入口和基础环境；协调扫描候选、构造工具、替换注册、更新活动目录与删除技能，重载、删除和 manifest 写入共用写入锁。
- `internal/tool/skill/scanner.go`、`catalog.go`：扫描器只读取文件并生成候选记录；catalog 保存已成功注册的活动记录，两者均为 Skill 包内实现。
- `internal/tool/skill/command_tool.go`、`go_runner.go`：准备命令／Go 调用的参数、风险、目标与操作预览；执行阶段读取技能环境并准备媒体，GoRunner 固定所选活动记录。
- `internal/tool/skill/skill_md_parser.go`、`elbot_skill_manifest.go`：SKILL.md 元数据与正文解析、ELBOT_SKILL.toml 解析校验与 schema 构造。
- `internal/tool/skill/create_el_skill.go`、`modify_el_skill.go`、`finalize_el_skill.go`：原生 Skill 创建、修改和 finalize 的准备、文件提交、编译及重载协调；文件预览与提交复用 fileops，`go_source.go` 处理源码规范化、格式化及校验。
- `internal/tool/skill/read_el_skill.go`：原生 Skill 读取工具，准备时固定路径，执行时读取内容。
- `internal/tool/skill/process.go`：公共进程执行、有界输出、取消错误、Go 编译器查找、版本探测与编译；保留创建和 finalize 各自的 Go 查找规则。
- `internal/tool/skill/media.go`：媒体参数准备与 stdout 结果导入。
- `internal/config/assets.go`：内置 Skill 说明。

<!-- locator:hook -->
## Hook 与插件

Hook 根包装配内部组件，子包通过 share 共享定义并接收外部能力，不反向导入根包。

- `internal/hook/supervisor.go`、`deps.go`：公开 Commands 标准命令与最小外部消费能力，内部组件只通过私有消费接口持有，不向调用方暴露。
- `internal/hook/wiring.go`、`lifecycle.go`：配置拆分、装配规则／流水线／运行时、构建候选并交付提交闭包、阶段关闭与实际退出等待。
- `internal/hook/agent_bridge.go`：注册 Agent 回调组，转换业务参数与 Hook Event；显式传递唤醒、只读策略及逐 handler 登记回调。
- `internal/hook/platform_signals.go`：持有平台连接订阅和逐平台队列，处理全局 Hook 输出；不登记 Agent 请求。
- `internal/hook/context_to_event.go`：fillEventFromContext 将 contextinfo 与 platform 的来源、参与者、消息和引用转换并补入 Hook Event。
- `internal/hook/share/event.go`、`match.go`、`call_policy.go`、`registration.go`、`runtime.go`：内部共享事件、匹配、调用快照、注册与查询契约；RuntimeInfo 的模式和状态是展示字符串。
- `internal/hook/share/runtime_config.go`、`control.go`：插件配置、执行模式、校验及 PipelineControl、RuntimeControl、RuntimeCaller 协作契约；运行状态枚举留在 runtime。
- `internal/hook/pipeline/manager.go`、`options.go`：续接优先、普通规则与错误钩子的执行流程，用 mergeLogResult 统一聚合来源结果；CallOptions 显式传递逐次策略，构造时注入日志发布与续接能力。
- `internal/hook/rules/`：规则配置与一次性 exec；`rule.go` 逐动作校验只读调用策略；工具和平台调用、日志与通知通过注入能力完成，Runtime 仅借用 Handle、MediaRequest、SharedRequest。
- `internal/hook/control/service.go`：列表、重载及进程管理；接收根部 Loader 交付的 ReloadCandidate，通过查询快照与 Commit 协调提交，不构造流水线。
- `internal/hook/control/command.go`：`/hooks` 参数、展示与补全，消费本包管理能力并借用根部准入；`share/logging.go` 提供内部日志截断。
- `internal/hook/runtime/`：进程、RPC 与 waiting；`runtime/routes.go` 选中实际续接后登记请求并返回来源 LogResult；`runtime/media.go` 定义所需的最小私有媒体接口并管理租约。
- `internal/hook/runtime/manager.go`、`worker.go`：登记并等待启动、进程、读取、插件请求、重载与异步停止的实际退出；`lifecycle_test.go` 覆盖关闭、脱离索引的临时 worker 与忽略取消的调用。
- `internal/hook/media.go`、`logging.go`：媒体中心契约与来源日志／通知发布；`share/output/`：输出与消息转换；`share/protocol/`：hook.v2 帧与编解码。
- `internal/hook/agent_bridge_test.go`、`supervisor_test.go`、`wiring_test.go`：桥接转换、并发启动、配置加载和关闭／平台队列边界用例；`pipeline/continuation_test.go` 验证续接顺序、短路和错误不递归。
- `internal/hook/command_test.go`：命令与补全的关闭准入、在途等待及切片隔离；`internal/agent/hook_integration_test.go` 以外部测试包验证真实总管接线、重载后的输出与日志。
- `internal/agent/hook_bridge.go`：对外单组回调注册与逐个 handler 的请求登记。
- `internal/agent/hooks/points.go`：内部回调组与事实组合；`hooks.go`：回调调度、输入事实准备和在途生命周期。公开回调在 Agent 根部，内部载荷在 `share/hooks.go`。
- [Hook 文档](../docs/hooks.md)。

<!-- locator:output -->
<!-- locator:notification -->
## 输出、投递与通知

- `internal/delivery/delivery.go`：输出意图、目标、回执及公共 `MessageStream`、`ReasoningStream` 契约；`internal/platform/dispatch/` 负责发送路由与媒体准备。
- `internal/platform/notification/manager.go`：通知信号、目标、绑定校验与平台队列；`internal/notification/rules/` 保留 Agent 使用的提示规则。
- `internal/platform/module/notifications.go`：通知管理器及平台订阅装配。
- `internal/platform/{cli,qq-onebot,qqofficial,telegram}/notifications.go`：平台通知协议与实际发送。
- `internal/agent/output/output.go`、`internal/agent/execution/turn_output.go`、`execution_output.go`：输出 Hook、前后台策略、接管刷新与流式回执合并。
- `internal/agent/dialogue/output.go`、`reply_commit.go`：正文／思考消费契约与最终回复提交。

<!-- locator:platform -->
## 平台适配

- `internal/platform/{supervisor,deps,wiring,lifecycle}.go`：总管、借用依赖、完整配置装配与生命周期；部分构造可清理，停止接入与最终资源释放分离。下层目标契约见 [App 包设计](app-design.md#platform-下层目标契约)。
- `internal/platform/{contracts,agent,send,public_api,client,config}.go`：公开契约、Agent 输入与展示转接、发送、两类 API、媒体解析、客户端分类转换及配置汇总；按下层已实现来调用，整包仍待迁移。
- `internal/platform/share/`：内部契约与共享辅助；`adapter_api.go` 定义调用契约的职责，`config.go`、`backoff.go` 提供共享辅助，`adapter.go` 的 AdapterDeps 接收历史写入、身份查询、两项别名能力及引用恢复函数；运行生命周期与 API 契约仍待接通。
- `internal/platform/dispatch/{router,media}.go`：复用平台注册表执行发送与显式目标媒体解析；ResolveMedia 不回退到当前或默认平台。`calls.go` 承接 Elvena 平台调用与映射实现，尚未拆入各适配器。
- `internal/platform/{telegram,qq-onebot}/capability_api.go`：通用能力的平台实现骨架。
- `internal/platform/refcontext/refcontext.go`：引用消息恢复与上下文处理；Options 持有 Store 和仅含 GetByPlatformMessage 的历史消费接口，单次请求和结果使用 share 契约，由根部闭包注入适配器。
- `internal/platform/platform.go`、`module/`、`builtin/`：保留尚未拆分的旧实现，文件头标注迁移后删除；不作为新对外边界。
- `internal/platform/cli/`：本地／远程 CLI 与 TUI；`ui.go`、`status.go`、`reasoning.go`、`status_format.go` 分别负责展示目标、状态合并、思考流和状态文案。
- `internal/platform/contracts.go`：客户端分类与纯消息事实。公开 MessageContext 仅含原文、原生载荷和引用，存取复制可变内容。
- `internal/platform/public_api.go`：公开平台 API 与 ResolveMedia，转换媒体输入后交 dispatch 路由，不另建平台能力表。
- `internal/platform/share/{message,reference}.go`：内部路由上下文、片段、引用恢复契约与引用格式化。根部 `agent.go` 转换 Agent 输入，RestoreMessageContext 恢复公开事实及内部路由，不改写目标公共会话事实。
- `internal/agent/contracts.go`、`share/input.go`：Agent 自有 MessageRequest 与内部输入上下文；平台恢复由根部注入，后台入口清除继承的平台状态。
- `internal/storage/chat_history.go`：ChatSegment、ChatReply 和持久化编解码，保留片段顺序与来源清洗规则。
- `internal/media/{types,platform}.go`：自有 PlatformMedia、HistoryMessage；负责媒体缓存、关联和批量获取，通过注入函数解析平台来源，不依赖 Platform。
- `internal/platform/{lifecycle,contracts,agent}_test.go`：生命周期、公开事实隔离独立测试，以及输入转换与投影测试；本轮契约迁移的测试留到 review 运行。
- `internal/platform/qq-onebot/`：协议转换、forward 展开、长消息与回执。
- `internal/platform/qqofficial/`、`internal/platform/telegram/`、`internal/platform/headless/`：各平台 runtime。
- `internal/platform/qqofficial/reference.go`：QQ 引用索引解析、消息 ID 关联、事件原文转换及入站／发送回执索引登记。

<!-- locator:session -->
## Session

- `internal/session/service.go`、`types.go`：服务与领域接口。
- `internal/session/binding.go`、`coordination.go`、`signals.go`：current、准入锁与绑定变化。
- `internal/session/origin.go`、`promotion.go`、`background.go`：模型归属、前台接管及事实查询、后台准备。
- `internal/session/fork.go`、`background_copy.go`、`material.go`：Fork、副本与源材料契约。
- `internal/session/compact.go`、`lifecycle.go`、`expiration.go`：压缩结果创建、生命周期与过期。
- `internal/session/naming.go`、`naming_lifecycle.go`、`title.go`：命名调度、退出与生成。
- `internal/background/share/takeover.go`：Cron 与 Elnis 共用的接管查询函数契约，由根部注入 Agent 公开方法。

<!-- locator:context -->
## 上下文、Prompt 与压缩

- `internal/contextmgr/`：历史、窗口查询、用量、seed 状态与压缩分派。
- `internal/agent/execution/execution_compact.go`、`context_usage.go`：压缩生命周期、交接及用量。
- `internal/agent/chat/compact.go`、`compressor.go`：Chat 摘要。
- `internal/agent/responses/compact.go`、`branch.go`：原生压缩与分支材料。
- `internal/agent/dialogue/system_prompt.go`、`prompt.go`：公共 Prompt；`internal/agent/share/dialogue.go`：跨路线共用的接管提示。
- `internal/agent/chat/transcript.go`：Chat 历史与 Prompt Builder。

<!-- locator:llm -->
## 模型服务与 LLM 客户端

- `internal/modelmgr/`：模型选择、兼容性、目录、状态与重试信号。
- `internal/modelmgr/providers.go`、`selection.go`、`context_window.go`：厂商快照、固定选择与窗口缓存。
- `internal/llm/`：独立文本、模型目录、消息／工具／Usage 及 Extra 契约。
- `internal/llm/httpclient/`：HTTP、重试、SSE 与超时。
- `internal/llm/chatcompletions/`、`internal/llm/responses/`：独立协议客户端。
- `internal/llm/responses/client.go`、`tools.go`、`compact.go`：请求冻结、工具权限与原生压缩。
- `internal/llm/responses/stream.go`、`error.go`、`response.go`：原生流、协议错误与用量解析。

<!-- locator:storage -->
## Storage 与 SQLite

- `internal/storage/storage.go`、`session_metadata.go`：领域模型、repository 契约与 metadata。
- `internal/storage/sqlite/`、`db_init.go`、`migrations.go`：存储实现、空库最新结构与已有数据库升级；`db_init.go` 的结构快照同时记录已包含的迁移版本。
- `internal/storage/dialogue.go`、`internal/storage/sqlite/dialogue_repository.go`：对话／原生提交与材料契约。
- `internal/storage/sqlite/session_repository.go`：Session 字段合并与同值写入跳过。
- `internal/storage/sqlite/native_snapshot.go`、`native_material.go`：不可变快照、seed 与原生窗口批量读取。
- `internal/storage/sqlite/media_history.go`、`media_lifecycle.go`：历史媒体关联、启动恢复与清理认领。
- `internal/storage/sqlite/chat_history_repository.go`：聊天历史查询与保留时间清理，包含平台引用索引到真实消息 ID 的持久化映射。搜索始终限定平台和聊天范围，SenderQuery 非空时以括号包裹的 OR 条件执行发送者 ID 精确匹配或昵称字面包含匹配，昵称 LIKE 模式转义通配符。

<!-- locator:elnis -->
## Elnis / Elvena / Elwisp

- `internal/elvena/`、`internal/background/elnis/types.go`：公共协议与 Elnis 类型；工具声明直接使用 Tool 根包公开类型。
- `internal/background/elnis/http.go`：HTTP 构造、路由、协议与连接跟踪。
- `internal/background/elnis/lifecycle.go`：同步监听就绪、队列与 workers、运行故障观察、实际退出和批量终态收尾。
- `internal/background/elnis/service.go`：去重分发与中断执行恢复。
- `internal/background/elnis/llm.go`、`direct.go`、`store.go`：模型／直接流程与事件状态。
- `internal/background/elnis/auth.go`、`prepare.go`、`targets.go`：鉴权、规范化与目标裁决。
- `internal/background/elnis/repository.go`：Elnis 独用事件仓储、最小记录及写入请求；根部 wiring.go 转换 Storage 记录与错误。
- `internal/background/elnis/media.go`：四项媒体消费能力、输入物化与报告附件受限导入；根部适配媒体中心，导入仅返回稳定 ID。
- `internal/background/elnis/outbox.go`：报告准备、有序投递与恢复，事务由仓储执行。
- `internal/storage/sqlite/elnis_event_repository.go`：事件仓储与引用事务。
- `internal/background/share/`：公共后台类型、JSON 结果、接管检查与日志发布；report.go 提供报告构造及 Cron／Elnis 共用的 ReportMessageMap、MapReportReceipt。
- [内部架构](elnis-elwisp.md)、[功能说明](../docs/elnis.md)、[配置与协议示例](../docs/elnis-usage.md)。

## 媒体中心

- `internal/media/manager.go`、`resolver.go`：导入、公开读取与入站物化。
- `internal/media/request.go`：完整模型请求的媒体持有、交付选择与签名 URL。
- `internal/media/reference.go`、`lifecycle.go`：引用、存活保护与物理清理。
- `internal/media/platform.go`、`history.go`：平台媒体、历史批量获取与跨库对账。
- `internal/storage/sqlite/media_repository.go`：元数据与签名缓存。
- `internal/tool/builtin/chat_history.go`、`get_media.go`、`view_image.go`：历史查询、媒体获取与 Responses 看图。
- `internal/platform/dispatch/media.go`：输出媒体准备与回执缓存。

<!-- locator:cron -->
## Cron 与维护

- `internal/background/cron/`：调度、执行、报告与投递恢复；任务定义管理归 Tool。
- `internal/background/cron/service.go`、`manager.go`：任务运行、调度、按原任务 ID 记账与整体日志。
- `internal/background/cron/repository.go`、`model.go`：独用持久化契约、任务快照与 JSON 模型；根部 wiring.go 实现存储转换。
- `internal/background/cron/refresh.go`：只登记名称的刷新入口，首次加载协调与运行期串行重新读库。
- `internal/background/cron/lifecycle.go`：平台通知启动前合并、启动后消费和退出等待。
- `internal/background/cron/models.go`、`delivery.go`、`recovery.go`：任务模型、逐项投递与连接恢复。
- `internal/background/maintenance/maintenance.go`：维护清理、处理器注册与任务配置；通过最小接口和函数使用外部能力，不启动调度器。
- `internal/background/share/maintenance.go`：维护组件与 Cron 共用的 MaintenanceJob 配置。
- `internal/background/cron/maintenance.go`：维护回调与共享配置适配，复用 Cron 的任务校验、启用和禁用。
- `internal/background/execution/runner.go`：准备任务目录与后台来源，再调用 Agent.RunBackground；`internal/security/security.go` 的 `Permission.CheckTool` 决定工具拒绝、直接执行或交互确认，`internal/tool/execution/runner.go` 执行该决定。
- `internal/tool/builtin/cron.go`、`internal/storage/sqlite/cron_job_repository.go`：工具入口与仓储。

<!-- locator:docs -->
## 文档

- `docs/`：中文用户文档；`devdocs/`：当前内部架构、代码入口与未完成任务。
- `CHANGELOG.md`：中文变更记录；`scripts/translate_docs.py`：翻译脚本。
- `docs.en/`、`README.md`、`CHANGELOG.en.md` 为英文镜像或自动翻译产物，不手动修改。
