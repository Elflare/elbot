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
- `internal/app/foundation.go`、`models.go`：配置／存储基础设施和 provider 客户端。
- `internal/app/services.go`、`runtime.go`：共享服务创建、内置命令注册、Cron／Tool／Hook／Agent 装配及 Session／Hook 执行回调接线；先完成注册和接线再开放平台入口。
- `internal/app/platforms.go`、`integrations.go`：平台运行、Elnis 和平台能力接线；同目录还包含远程 CLI client 与 service marker。
- `internal/app/signals.go`、`lifecycle.go`：订阅、队列和状态 worker 的统一所有权，平台 Hook／Cron 独立订阅及命名、Hook、Skill 的退出等待；Runner 先 BeginClose 唤醒背压生产者，再共享预算清理部分启动资源。
- `internal/app/foundation.go`、`runner.go`：独立 StopCron 取消并等待启动及在途任务，完成后才释放 runtime／Hook 和存储；超时保留存活任务依赖。

常用搜索：

```bash
rg -n "func Run|service run|completion|--client|RunCron" cmd internal/app internal/launcher
```

<!-- locator:contextinfo -->
## 公共上下文事实

- `internal/contextinfo/conversation.go`、`actor.go`、`execution.go`、`model.go`：Conversation、Actor、Execution、Model 唯一事实定义；平台扩展不序列化，角色不授予权限，执行 ID 不代替有效性检查。
- `internal/contextinfo/context.go`：四组事实的独立 With／From／Without 存取，显式区分缺失与零值，不补入口或全局默认值。
- `internal/platform/platform.go`：嵌入公共 Conversation 的平台消息上下文、正文和 Sender；私有上下文不重复保存 Conversation，读取时重建投影。
- `internal/platform/cli/message.go`：scanner／TUI 共用的本地身份入口。
- `internal/security/security.go`、`context.go`：身份解析、权限／风险判断及 Policy；Actor 事实和角色定义归 contextinfo。
- `internal/request/manager.go`、`internal/turn/execution.go`、`internal/session/binding.go`：在实际关联建立时发布 Request／RootRequest、Run／Attempt、Session 事实，活动状态与原绑定仍由领域服务校验。
- `internal/agent/dialogue/execution_context.go`：前台接管刷新公共来源／身份并清除缺失事实，按固定选择与绑定发布主对话 Model。

<!-- locator:signal -->
## 信号与订阅

- `internal/signal/`：泛型信号、连接句柄、有界串行执行器、可选 WaitForCapacity 背压，以及独立的取消生命周期、BeginClose 和关闭策略。
- `internal/agent/events/`：Agent 类型化事实、Signals 和 Usage／Receipt 快照；`agent.go` 暴露 Signals 订阅入口。
- `internal/app/agent_signals.go`、`agent_logging.go`、`log_record.go`：普通／审计日志的背压订阅、字段映射和实际写入错误处理。
- `internal/app/agent_notifications.go`、`agent_status.go`、`model_signals.go`、`naming.go`：过程／失败通知、按目标合并状态、共享模型重试和命名日志订阅。
- `internal/platform/signals.go`：平台 Connected 事件及发布接口；连接归 app 持有。

<!-- locator:config -->
## 配置、资产与日志

适用任务：配置读取、默认配置资产、provider/state/tool_tags 合并、日志写入/轮转/读取。

先看：

- `internal/config/config.go`、`configuration.go`：启动入口及与诊断共用的读取、默认值、合并和来源记录。
- `internal/config/assets.go`：默认资产、文件必要性与示例声明。
- `internal/config/definition.go`、`config_definition.go`、`provider.go`：规则类型、核心配置声明、api_mode 默认值及启动／诊断共享校验；平台与 Hook 的 `config_definition.go` 由 app 显式装配。
- `internal/config/inspect.go`、`inspect_toml.go`：只读诊断、TOML 检查和内置内容比较。
- `internal/doctor/`：`doctor.go` 调用配置检查入口，`report.go` 按文件生成错误/提示报告。
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

- `internal/agent/agent.go`、`assembly.go`：Agent 薄入口及唯一 `New(ctx, cfg, deps)` 装配；共享服务只注入实际消费者，完整 Dependencies 仅在装配使用，内置命令在 app 注册。
- `internal/agent/message.go`、`wakeup.go`：messageHandler 的唤醒、平台消息 Hook、slash／普通输入分发和入口错误通知；Agent 消息及 Hook 唤醒方法为薄委托。
- `internal/agent/command_runtime.go`：commandExecutor 直接组合执行、确认、输入和输出组件，处理权限、Turn 冲突、通知和 continuation。
- `internal/agent/input.go`、`tool_directive.go`：inputCoordinator 的普通输入 Hook／预加载锁外准备、命令 continuation 和闲置过期；准入复核与 pending 分发交执行协调，风险响应交确认组件。
- `internal/agent/background.go`：backgroundRunner 的身份、来源、sandbox、Session 和资源预加载准备，再交 executionCoordinator 等待最终结果。
- `internal/agent/segments.go`：平台入站 Segment 与 LLM Segment 转换；文字不变时保留原段，去除唤醒词或工具指令时只替换变化的文本跨度，保留周围图文位置。
- `internal/agent/inbound_media.go`：messageHandler 在实际消费前连接平台 resolver 与 Media Center，处理大小校验、历史关联和不可用降级。
- `internal/tool/builtin/chat_history.go`：当前聊天历史查询与媒体位置/下载状态展示，查询不下载；`get_media.go`：显式选定媒体位置获取，仅返回文本 ID，单次最多 5 次未入库媒体获取尝试。
- `internal/media/platform.go`：共享平台导入、历史媒体位置关联与本地 ID 查询；`manager.go`、`image.go`：统一媒体入库和持久化前图片压缩；`resolver.go`：LLM 媒体解析与传输选择；`history.go`：跨库历史 owner 分页对账。
- `internal/storage/sqlite/media_history.go`：主库历史媒体关联与引用事务，区别于机器人发送输出索引。
- `internal/delivery/dispatch/media.go`：发送前归一、发送副本解析与有序媒体回执缓存。
- `internal/agent/config.go`、`dependencies.go`、`logging.go`：行为配置、必需依赖校验、过期配置转换和入口审计；runtime context 单独传入，不提供 Agent setter。
- `internal/agent/identity.go`：identityResolver 拥有入口默认身份与安全策略；区分普通入口和无默认身份的 Hook 来源解析。
- `internal/agent/toolrun_prompt_provider.go`：直接注入 ToolRun 与身份解析的 Prompt provider。
- `internal/agent/dialogue/runner.go`、`loop.go`、`preparation.go`：公共单轮契约、Request 登记前加载、登记后准备／保存、Outcome 和回复提交；`internal/agent/chat/loop.go`、`preparation.go`、`run_loop.go`：Chat 私有状态、历史与摘要组织及模型／工具循环。
- `internal/agent/dialogue/reply_commit.go`：公共提交输入／结果、最终 Hook、空回复、发送／落库顺序、延迟 outputs 和实际回执关联。
- `internal/agent/dialogue/model_call.go`：共同模型 Hook、权限过滤与事实；`internal/agent/chat/model_call.go`：Chat 请求、流消费与视觉回退；`internal/modelmgr/context.go`：请求选择及后台覆盖；`internal/media/request.go`：请求素材解析、持有和释放。
- `internal/agent/dialogue/tool_execution.go`、`pending.go`：共同 ToolRun、schema 和 pending 消费；`tool_display.go`：根包的工具／确认展示帮助函数；`toolrun_adapter.go`：直接组合服务的 toolRunDeps，负责工具 Hook、子请求、文件上下文、确认适配、调用记录和状态提交。
- `internal/agent/confirmation.go`、`background_tools.go`：confirmationCoordinator 的前后台风险确认、响应、自动确认记录及共享超时策略；等待状态仍归 Turn。
- `internal/agent/turn_output.go`：只依赖发送与状态组件的前后台 turn 输出适配。
- `internal/agent/tools.go`：系统提示来源装配；`internal/agent/chat/transcript.go`：Chat Prompt Builder、历史消息转换和摘要注入，Soul 与常驻记忆来源归公共层。
- `internal/agent/dialogue/system_prompt*.go`、`prompt.go`、`tool_tag_prompt.go`：Soul、常驻记忆、工具提示等共同来源和组合；`foreground_notice.go`：请求末尾的接管 user 提示，不加入 system prompt。
- `internal/agent/dialogue/message_store.go`：用户／pending／工具 transcript 写入和业务 metadata 编解码。
- `internal/agent/dialogue/commit.go`：MessageCommitter、原 binding／活跃 attempt 提交准入、调用前普通文本及不可变 ToolPair 构建与同步提交。
- `internal/agent/responses/loop.go`、`model_call.go`：原生业务 Loop、固定选择、pending／停止／接管、无服务端存储时的完整历史及旧链失效后的一次完整回放；`native_call.go`：实际请求归档、原生流／终态及新内容检测；`input.go`：新增输入和多模态 function_call_output 编码；`initial_inputs.go`：首次失败后待提交输入完整性校验；`foreground_notice.go`：原生接管提示排队、复用及末尾排序。
- `internal/agent/responses/persistence.go`：工具副作用前的原生提交、展示 ToolPair 与原生结果／历史快照事务，以及仅限原生层的中断调用结尾。
- `internal/agent/responses/context.go`：seed 根、checkpoint 链与冻结结果引用的窗口重建、完整性和媒体校验；`branch.go`：历史 Fork／后台复制材料；`continuation.go`：分支新增结果的媒体解析；`compact.go`：当前模型原生压缩、整个返回窗口及素材关联。
- `internal/agent/responses/store.go`：从持久化请求／响应判定存储与续链能力，供主对话及分支材料准备复用。

常用搜索：

```bash
rg -n "Handle|Run|Prompt|tool_calls|reasoning|usage|pending|prepared" internal/agent
```

<!-- locator:protocol-routing -->
## 协议路线登记

- `internal/agent/routes/route.go`、`registry.go`：按 provider 登记 Binding{Origin, Client, Loop, Compactor, Material}、独立登记源协议压缩／材料准备能力、封闭及按能力查询；不拥有运行状态。
- `internal/agent/assembly_routes.go`、`assembly.go`、`internal/app/services.go`：app 创建共享注册表及上下文服务，Agent 校验预登记绑定的 Origin／Client，补齐内置路线并封闭；能力接线通过后才开放运行。
- `internal/agent/dialogue/loop.go`、`internal/contextmgr/compact_contract.go`、`internal/session/material.go`：消费方的小查询接口及不解释协议载荷的材料交接契约；公共层不导入注册表实现或具体路线。
- `internal/llm/protocol.go`、`origin.go`：协议标识及纯归属描述；`internal/modelmgr/selection.go`、`service.go`：固定 Provider／Model／Client 快照、启动配置生成的不可变 ProviderOrigins。`internal/app/models.go` 按 provider.api_mode 构造原生客户端。

<!-- locator:commands -->
## Slash 命令与补全

适用任务：新增/修改 slash 命令、命令帮助、命令参数补全。

先看：

- `internal/command/builtin/`：内置命令实现。
- `internal/command/builtin/register.go`：命令模块注册入口。
- `internal/command/builtin/request.go`：请求展示、停止与补全；普通用户限定当前 Session，取消前复核原绑定，超级管理员保留全局范围。
- `internal/command/builtin/session_*.go`：按模式、核心、导航、生命周期和格式化拆分的 Session 命令；共享状态由 `SessionCommandState` 按 Scope 隔离。
- `internal/command/`：通用命令框架和 Router。
- `internal/completion/`：平台结构化补全服务；`internal/agent/completion.go` 直接注入 Router、Session、身份和工具服务，CLI 统一使用该服务并保留本地文件补全优先级。
- `docs/commands.md`：用户侧命令文档。
- `internal/command/builtin/doctor.go`：超级管理员 `/doctor` 入口，通过 `Deps.Doctor` 调用诊断服务。
- `internal/command/builtin/rollback.go`：超管 `/rollback` 参数、展示、补全与审计，直接调用 fileops；`internal/agent/file_rollback.go` 的 fileCommandPreparer 提供权限、原绑定、workspace 与提交准入。

常用搜索：

```bash
rg -n "Register|Info\{|Help:|Complete|Alias|/requests|/model" internal/command/builtin internal/command internal/completion docs/commands.md
```

<!-- locator:request-turn -->
## Request、Turn 与运行状态

适用任务：active request 树、取消/停止/超时、阶段展示、工具 pending、确认状态、runtime status。

先看：

- `internal/request/`
- `internal/turn/manager.go`、`execution.go`：阶段、pending、确认及跨请求的逻辑执行身份与结果；`BeginRiskConfirmation` 在发提示前登记 attempt 与响应通道，等待对象按 Turn／通道身份处理响应与清理。
- `internal/agent/execution.go`、`execution_run.go`：executionCoordinator 的前后台执行、接管、attempt／Request 生命周期及提交后收尾；后台等待跨追加确认和压缩的真实执行结果。
- `internal/agent/execution_input.go`：输入接受、追加打断／确认／过期和 pending 分发。
- `internal/agent/execution_lifecycle.go`：executionCoordinator 拥有的追加确认等待任务，原来源保留、应用取消、登记／关闭互斥以及在途过期提示退出等待；Agent.Close／Done 薄委托，app 负责关闭接线。
- `internal/agent/dialogue/execution_context.go`、`internal/agent/execution_output.go`：ExecutionView 刷新来源与 Session，executionTurnOutput 切换前后台输出并转换后台报告；保留原请求取消，不拥有第二份执行状态。
- `internal/agent/execution_admission.go`：Scope／Session 准入、原绑定与模式复核、输入解析和 Turn 启动检查。
- `internal/runtime/`
- `internal/agent/status.go`：statusRecorder 校验 attempt 归属、同步保存／查询及发布带版本状态；`internal/app/agent_status.go` 合并最新展示值，调度终态并隔离原连接目标。
- `internal/contextinfo/execution.go`、`internal/request/manager.go`：当前、父与主 Request ID 的公共关联快照；Request 生命周期仍由 Manager 管理。
- `internal/agent/risk_confirmation.go`：高风险确认命令文案和识别。

常用搜索：

```bash
rg -n "Phase|Request|Cancel|pending|confirm|runtime status|sending" internal/request internal/turn internal/runtime internal/agent
```

<!-- locator:tool -->
## Tool Runtime、工具发现与内置工具

适用任务：内置工具、工具注册、schema、风险等级、确认详情、工具发现、工具缓存、工具 tag、文件/shell/web/cron/memory 工具。

先看：

- `internal/tool/`：Tool Runtime 核心类型、builder、discover、executor 和工具可用性判断。
- `internal/tool/media_runtime.go`：shell/Skill 的显式媒体准备、调用期引用、sandbox 导出缓存与受控结果导入；缓存复用刷新 ModTime，沿用既有 sandbox 清理。
- `internal/tool/runtimeinfo/`：工具运行期常用信息入口，如配置路径、sandbox、文件发送配置、时间源和规则卡转发。
- `internal/toolrun/`：工具调用中间层、工具视图、命名解析、风险确认，以及实际执行前的 Session 工具参数媒体引用。
- `internal/tool/builtin/`：内置工具。
- `internal/tool/builtin/file_tools_ast.go`：`read_file` 的 Go/Shell AST 名称搜索与结果渲染。
- `internal/fileops/service.go`、`edit_service.go`：命令与工具共享的编辑／撤销服务、调用绑定、确认预检与提交准入；`internal/tool/builtin/file_rollback.go` 保留工具协议及风险确认。
- `internal/agent/dialogue/tool_execution.go`：共同工具消费能力与轮次配置；`internal/agent/tools.go`：内部装配辅助；不保存第二份 Session 工具状态。
- `internal/agent/toolrun_*.go`：Agent 到 ToolRun 的桥接。
- `internal/toolrun/state.go`：Session 工具发现、schema、tag 和规则卡状态的统一读取与事务提交；`discovery.go` 解析发现结果与 wrapper 激活，`cache.go` 负责缓存项归一，`schema.go` 负责调用快照隔离。
- `internal/toolrun/preload.go`、`tags.go`：独立预加载服务，共享前后台工具发现、Skill 激活、标签配置读取及查询；只返回待提交状态和展示材料。
- `internal/toolrun/background.go`：后台缓存过滤和 schema 白名单；Manager 在准备 Hook 后按 Session 模式限制工具解析。
- `internal/agent/tool_cache.go`：工具状态服务的调用适配，成功提交后更新调用期 Session 快照。
- `internal/agent/tool_directive.go`：inputCoordinator 的 `@tool:` / `@skill:` 输入解析、统一提交与通知编排。
- `internal/agent/dialogue/tool_tag_prompt.go`：将工具服务提供的标签提示放入 work 模式 Prompt。
- `internal/security/`：工具权限和风险策略。
- `internal/fileops/{file,encoding,text}.go`：文件生命周期、编码与通用文本处理。
- `internal/fileops/{edit,match,diff}.go`：原子编辑解析、目标匹配与 unified diff。
- `internal/fileops/rollback.go`：有容量上限的内存备份、会话有效期、目标锁、revision/路径校验与原始字节恢复。

常用搜索：

```bash
rg -n "discover_tool|NewBuilder|Risk|Confirm|ToolRun|Result\{|Outputs|workspace|shell" internal/tool internal/toolrun internal/agent
```

<!-- locator:tool-flow -->
## 工具调用链路相关文件

适用任务：LLM tool call 到工具执行、工具结果回灌 LLM、工具调用记录、工具确认、批量预览。

先看：

- `internal/agent/dialogue/tool_execution.go`：Agent 工具执行主入口。
- `internal/toolrun/`：执行前解析、过滤、确认和预览。
- `internal/tool/executor.go`：Tool Runtime 执行适配。
- `internal/tool/tool.go`：Tool 核心类型。
- `internal/agent/dialogue/message_store.go`：tool message/transcript 同步落库。
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
- `internal/tool/skill/media.go`：Go payload/TOML 媒体参数的执行副本转换和 stdout 媒体结果解析；`internal/config/assets.go` 中的 `defaultAgentSkillCreatorSkillMD` 是 Agent Skill Creator 的内置说明。
- `internal/tool/skill/go_source.go`：原生 Go skill 源码维护和编译。

常用搜索：

```bash
rg -n "SKILL.elyph|ELBOT_SKILL|AgentSkill|go_skill_run|finalize|Lint|Catalog" internal/elyph internal/tool/skill
```

<!-- locator:hook -->
## Hook 与插件

适用任务：Hook 事件、控制字段、注册、列表、热重载、规则 Hook TOML、exec action、hook.v2 协议、持久 Hook 和 SharedState。

先看：

- `internal/hook/event.go`：Hook 点、事件 payload 和 Handler 基础类型。
- `internal/hook/media.go`：Go Hook 使用的宿主 Media API；`internal/hook/runtime/media.go`：hook.v2 媒体 RPC、安全导出和临时引用生命周期。
- `internal/hook/output/`：规则、一次性 exec 与 runtime 共用的输出协议、消息图片 segment 规范化、校验和 delivery 转换。
- `internal/hook/protocol/`：进程 Hook 共用的 `hook.v2` 帧、ID 校验和 `event.handle` 公共结果字段。
- `internal/processenv/`：Shell 与进程 Hook 共用的环境分层、PATH 补充和可执行文件解析；`internal/hook/process.go` 保留 Hook 侧适配入口。
- `internal/hook/match.go`：Hook 条件匹配、字段读取和模板值。
- `internal/hook/manager.go`：普通 Hook 注册、排序、执行与原子 handler 快照替换。
- `internal/hook/call_policy.go`：消费方注入的工具调用只读策略；manager、Agent Hook 桥及规则 action 间复核，不依赖协议包。
- `internal/hook/control/`：`/hooks` 的列表、重载和持久进程生命周期管理入口。
- `internal/hook/builtin/`：内置规则 Hook 注册入口。
- `internal/hook/rules/`：规则 Hook；`rules.go` 提供类型和模块入口，`config.go`/`toml_error.go` 负责配置加载与诊断，`rule.go`/`action.go`/`exec.go` 负责规则及 Action 执行，`exec_process_*.go` 负责一次性 exec 的跨平台进程树终止，`detail.go` 负责列表详情。
- `internal/hook/runtime/`：Worker Hook 配置、进程、双向 Pipe RPC、waiting 路由、工具桥接和进程内 SharedState。
- `internal/agent/hooks.go`：hookBridge 负责 Hook 执行、事件补全、continuation、同步 Request 观察和错误处理；对外接线保留 Agent 薄委托。
- `internal/agent/output.go`：outputSender 编排发送 Hook 并调用共享发送服务；文件内另保留由对话主流程调用的 Session 消息关联。
- `internal/delivery/dispatch/media.go`：平台发送前解析 Hook/Tool 输出中的 media，并清理受控临时导出。
- `docs/hooks.md`：用户侧 Hook 文档。

常用搜索：

```bash
rg -n "Event|Handler|Control|plugins/hooks.toml|exec|hook.v2|runtime|SharedState|message.segments|llm.messages" internal/hook internal/agent docs/hooks.md
```

<!-- locator:output -->
<!-- locator:notification -->
## Output、Delivery 与发送

适用任务：输出意图结构、文本/图片/文件/语音/at/reply/emoticon 发送、流式输出、notice、reasoning、runtime status。

先看：

- `internal/delivery/`：平台无关输出意图、Target、Receipt、校验和发送契约。
- `internal/delivery/dispatch/router.go`、`media.go`：共享平台路由、可选流式／状态能力、媒体准备和回执缓存；普通失败与部分成功都保留实际结果。
- `internal/notification/manager.go`：通知意图、来源／Binding／Sender 覆盖捕获、取消／失效检查、无来源 service 日志策略；不另建发送器或媒体实现。
- `internal/notification/rules/`：平台连接 Hook 输出、Hook 失败、模型重试和执行错误文案；`vision.go` 拥有视觉降级提示去重，复用窄 SendAssistant 能力保留输出 Hook。
- `internal/app/services.go`、`integrations.go`、`signals.go`：共享发送／通知服务装配、外部宿主接入和平台连接执行器。
- `internal/agent/turn_output.go`、`execution_output.go`：前后台发送策略与接管输出适配；后台保持静默但同步记录状态。
- `internal/agent/output.go`：outputSender 的普通／流式发送和输出 Hook，保留部分成功回执；不负责 assistant 历史提交。
- `internal/agent/dialogue/reply_commit.go`：最终回复提交与 Session 消息关联，只消费回执中的实际平台、Scope 和消息 ID；错误结果保留实际发送与落库事实。
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
- `internal/platform/media.go`：Chat History 原始有序 segments 编解码与敏感来源清洗。
- `internal/platform/refcontext/`：按输出索引、Chat History、平台兜底恢复引用；自己 Session 的最后一条 assistant 自动 Resume，较早 assistant 自动 Fork。
- `internal/platform/config.go`：平台配置解码。
- `internal/platform/builtin/`：内置平台装配。
- `internal/platform/cli/`：本地/远程 CLI 和 TUI。
- `internal/platform/cli/tui.go`、`tui_mouse.go`、`tui_copy.go`：TUI 主模型、鼠标交互与 copy mode。
- `internal/platform/qq-onebot/`：OneBot 输入归一化、纯文本长消息合并转发、发送回执和媒体解析；`message.go` 定义协议数据并解码消息，`conversion.go` 统一消息类型映射、媒体字段提取和占位文案，按普通输入／转发节点场景生成内部消息段；`forward.go` 为私聊直发和显式引用共用的一层 forward 展开，组织节点署名和保序的图文展示段。
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
- `internal/session/binding.go`、`signals.go`、`coordination.go`、`activity.go`：当前绑定、锁外变化信号、Scope／SessionID 短准入及忙闲检查。
- `internal/session/promotion.go`：后台可见性、永久前台归属和在途接管入口；通过注入的纯检查在永久变更前校验 work 模型兼容性。
- `internal/session/background.go`：后台 Session 创建／复用、模式、标题和后台身份 metadata，不修改前台 current，不处理工具状态。
- `internal/session/compact.go`：CreateCompacted 的来源／绑定复核、归属及模式继承、命名字段与保存；只为前台结果激活 current。
- `internal/session/origin.go`：llm_origin 解码、首次主对话准入登记及源会话继承；不从当前配置推断持久化来源，不以公共执行 ID 代替原绑定。
- `internal/storage/session_metadata.go`、`sqlite/session_repository.go`：metadata 原值保留与 Session 原子字段更新。
- `internal/background/takeover.go`：后台修正及投递入口的持久化接管检查。
- `internal/session/mode.go`：模式激活和 work 历史限制。
- `internal/session/lifecycle.go`、`query.go`、`fork.go`、`expiration.go`：生命周期、查询、Fork 和闲置过期策略。
- `internal/session/background_copy.go`：后台广播副本的来源复核、创建与历史复制，不激活前台 current。
- `internal/session/material.go`：来源材料准备／查询契约、显式 Chat 展示材料及窄原子保存请求；Fork／Copy 在准入锁外准备，在交接时复核来源。
- `internal/session/naming.go`、`naming_lifecycle.go`、`naming_signals.go`：异步命名、应用级取消及准备／生成退出等待，发布 Scheduled／Completed／Failed；app 接入日志消费者。
- `internal/contextmgr/state.go`、`internal/toolrun/state.go`：分别解释上下文与工具 metadata，更新时保留其他模块及未知字段。
- `internal/session/workspace.go`：workspace 持久化适配、原子字段更新及原绑定检查；`commit.go`：原绑定的短提交准入。
- `internal/workspace/`：workspace 契约、context、metadata 状态与统一路径入口；`internal/sandbox/sandbox.go`：后台运行上下文及路径限制。

常用搜索：

```bash
rg -n "Fork|Archive|Pinned|Expire|SessionMode|metadata|workspace|cron:" internal/session internal/agent internal/tool
```

<!-- locator:context -->
## Context、Prompt 与压缩

适用任务：上下文加载、压缩摘要、context window、usage 展示、system prompt source。

先看：

- `internal/contextmgr/service.go`、`state.go`、`compact.go`、`compact_contract.go`：共同历史查询、用量、阈值、seed 状态与压缩路由契约；`internal/agent/chat/compact.go`、`compressor.go`、`compact_prompt.go`：Chat 历史筛选与摘要实现。
- `internal/agent/responses/compact.go`、`context.go`：Responses 原生压缩、完整窗口／素材准备及独立 seed，不读取 Chat 摘要模型。
- `internal/agent/execution_compact.go`：协调手动／自动压缩的 Request／Turn、取消、绑定准入和会话交接；`context_usage.go`：协调器的用量记录与压缩阈值检查；`internal/agent/chat/context_seed.go`：Chat 的 seed 消耗时机。命令直接读取 contextmgr。
- `internal/agent/chat/transcript.go`：Chat Prompt Builder 与历史组织。
- `internal/agent/dialogue/system_prompt*.go`：共同 system prompt 管理和来源。
- `internal/llm/segment.go`：MessageSegment helper。

常用搜索：

```bash
rg -n "ContextLoader|Compress|Window|System Prompt|MessageSegment|usage" internal/contextmgr internal/agent internal/llm
```

<!-- locator:llm -->
## 模型服务与 LLM Adapter

适用任务：模型列表与切换、运行状态持久化、LLM 抽象、OpenAI-compatible 请求、SSE、usage、reasoning、tool call delta、多模态消息转换。

先看：

- `internal/llm/client.go`、`message.go`、`tool.go`、`segment.go`：独立文本／模型目录契约、Usage、公共业务消息及工具定义；`extra.go` 校验额外字段只能添加、不覆盖或重复。
- `internal/llm/httpclient/client.go`、`sse.go`：公共 HTTP、显式代理、取消、重试、SSE 分帧和超时，不编码协议请求或固定鉴权。
- `internal/llm/chatcompletions/`：Chat 客户端、原生请求编码、chunks、工具参数累积、用量、模型列表和日志。
- `internal/llm/responses/`：Responses 客户端、原生 items／事件／完整响应及未知 JSON 保留，成功／失败／incomplete 终态；GenerateText 是独立调用，不处理业务工具或主会话持久化。
- `internal/llm/responses/client.go`：StoreForModel 读取 provider／模型存储偏好；PrepareRequest 提取并校验 store 配置、合并其余参数并冻结实际编码 JSON，StreamPrepared 发送同一字节快照，供业务路线在 API 前归档；鉴权头不进入快照。
- `internal/llm/responses/compact.go`：独立 Compact 请求冻结、HTTP 调用及整个原生窗口解析；`error.go`：保留 status／code／param，识别明确的旧链失效。
- `internal/modelmgr/service.go`、`selection.go`：共享服务与构造校验，模式／槽位、压缩和命名选择及请求快照。
- `internal/modelmgr/compatibility.go`：按协议／provider 节点名纯比较 CanSwitch；`internal/command/builtin/model.go`：解析候选、只预检受影响槽位并提交全局选择。
- `internal/modelmgr/signals.go`：共享客户端重试事实，供对话／压缩／命名统一消费；订阅归 app 所有。
- `internal/modelmgr/catalog.go`、`state.go`：模型目录缓存、筛选与 provider 错误，串行保存后发布选择状态；原子文件写入复用 `config.SaveState` 和 `fileops`。
- `internal/agent/chat/model_call.go`、`internal/agent/dialogue/model_call.go`：协议调用与共同调用处理；`internal/notification/rules/model.go`：模型重试／降级提示。
- `internal/session/title.go`：标题生成，开始时取得命名与 work fallback 快照，再调用客户端 GenerateText，结果保留回退后的实际 provider／model；`internal/agent/chat/compressor.go` 通过同一文本契约生成 Chat 摘要。

常用搜索：

```bash
rg -n -m 20 "ChatCompletion|Stream|SSE|reasoning|usage|ToolCall|MessageSegment|ModelList|ResolveMode" internal/llm internal/modelmgr internal/agent
```

<!-- locator:storage -->
## Storage 与 SQLite

适用任务：领域模型、repository interface、migration、session/message/context summary/chat history/cron/elnis/tool call 持久化。

先看：

- `internal/storage/storage.go`：领域模型和 repository interfaces；Message 使用 `content` 作为纯文本快速路径，`segments` 保存可选多模态正文。
- `internal/storage/id.go`、`internal/storage/time.go`：通用 ID/时间 helper。
- `internal/storage/sqlite/`：SQLite store、migration 和 repository 实现。
- `internal/storage/sqlite/migrations.go`：第 18 版为已有 Chat 会话补 protocol 归属，保留未知 metadata，损坏数据使迁移回滚；`origin_migration_test.go` 覆盖升级、回滚及只迁移一次。
- `internal/storage/dialogue.go`、`sqlite/dialogue_repository.go`：共同对话事务、ToolPair 和原生 Exchange／Input／Call／Checkpoint 记录、活动 checkpoint 比较提交和输入消费；migration 19 创建原生表。
- `internal/storage/tool_pair.go`、`sqlite/tool_pair.go`：调用与结果消息关联、角色和身份校验；`sqlite/native_snapshot.go`：原生调用快照与 ToolPair 同事务保存，不推进活动游标，按引用读取历史结果输入。
- `internal/storage/sqlite/native_material.go`：CreateMaterial 原子保存新 Session、复制消息、seed 和媒体引用并复核来源 checkpoint；migration 20 增加有序输入清单、checkpoint 调用快照／根引用及 native_seeds。首个成功 checkpoint 同事务消费 seed，根材料保持可用。

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
- `internal/elnis/media.go`：Elvena 媒体来源校验、按需入库、宿主 workspace 双向读写；通过 `media.Manager.MaterializeWithLimits` 传递本次接收限制，复用共享 Manager；发送回执缓存由 Agent 通用边界处理。
- `internal/media/lifecycle.go`、`reference.go`：引用保护、1 小时孤儿宽限期和最多 4 个对象并发的可重试清理；`locks.go`：按媒体 ID 互斥、可取消等待及空闲锁回收；`concurrency_test.go`：跨对象并行、同对象互斥、上传保护及清理取消/重试验证；`internal/storage/sqlite/media_lifecycle.go`：输出关联、清理认领、启动恢复和只读一致性检查；`media_json_test.go`、`media_fork_test.go` 验证媒体 JSON 引用事务和 fork 检查点范围。
- `internal/storage/sqlite/elnis_event_repository.go`：Elnis event 与 report outbox 的事务、claim、receipt 和完成状态持久化。
- `internal/background/`：cron/Elnis 共用后台 LLM 类型与结果 helper。
- `internal/tool/builtin/elwisp_creator.go`：Elwisp 创建指南工具。
- `docs/elnis.md`
- `docs/elnis-usage.md`
- `devdocs/elnis-elwisp.md`

常用搜索：

```bash
rg -n -m 20 "Elvena|Elwisp|/elvena/v2/events|direct|segments|background" internal/elvena internal/elnis internal/background docs devdocs
```

<!-- locator:cron -->
## Cron 与维护任务

适用任务：中央 Cron Runtime、LLM 可编排 cron 服务、维护类清理任务、cron 工具。

先看：

- `internal/cron/service.go`：Cron Service 装配、CRUD 与公开入口。
- `internal/cron/manager.go`：应用上下文控制的调度与执行生命周期，停止后禁止再启动，重复停止共享实际完成信号。
- `internal/cron/model.go`：任务 Metadata、Delivery 状态类型、校验与规范化。
- `internal/cron/models.go`：任务专用模型成对校验和 work 默认快照，复用 app 注入的 modelmgr 服务。
- `internal/cron/execution.go`：Direct/LLM 执行、报告生成和 JSON 格式重试。
- `internal/cron/delivery.go`：逐目标逐输出发送、状态持久化、降级与 receipt mapping。

- `internal/cron/recovery.go`：平台连接跟踪、过期 once 扫描与补发入口。
- `internal/maintenance/`
- `internal/agent/background.go`、`background_tools.go`：通用后台执行编排、预加载提交及后台工具确认。
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
