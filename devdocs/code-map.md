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

- `internal/launcher/cli.go`：参数解析、命令帮助与 Shell 补全。
- `cmd/elbot/main.go`：进程信号、启动模式分派与退出码。
- `cmd/elbot/main_test.go`：命令分派、Service 启停与配置锁测试。
- `internal/app/`：应用启动、资源装配与关闭。
- `internal/app/entry.go`：启动阶段、组件登记、运行等待与错误汇总。
- `internal/app/environment.go`：运行模式、配置路径与配置独占锁。
- `internal/app/environment_test.go`：配置路径与跨进程锁测试。
- `internal/app/resources.go`：配置加载与共享资源构造。
- `internal/app/wiring.go`：业务总管构造、跨模块接线与生命周期登记；汇总模块配置定义，构造并注入 Doctor。
- `internal/app/shutdown.go`：关闭步骤、统一期限与资源释放。
- `internal/app/client.go`：客户端调用与回退结果转换。
- `internal/agent/`：Agent 总管；`supervisor.go` 提供公开入口，`deps.go` 定义消费契约，`wiring.go` 装配，`lifecycle.go` 启停，`signals.go` 管订阅。
- `internal/tool/supervisor.go`、`wiring.go`、`lifecycle.go`：分别负责 Tool 对外入口、装配与启停。
- `internal/hook/`：Hook 总管、外部桥接、内部组件装配与启停。
- `internal/platform/`：平台总管、装配与启停；`config.go` 汇总配置定义。
- `internal/background/supervisor.go`、`deps.go`、`wiring.go`、`lifecycle.go`：分别负责后台总管、消费契约、组件装配与启停。
- `internal/background/execution/`：后台输入准备与 Agent 转接；`cron/` 管调度，`elnis/` 管事件，`maintenance/` 管清理。

<!-- locator:contextinfo -->
## 公共上下文事实

- `internal/contextinfo/`：会话、身份、执行、模型与后台来源事实。
- `internal/platform/agent.go`、`internal/platform/cli/message.go`：平台消息投影与 CLI 入站来源。
- `internal/security/`：身份解析、权限与风险策略。
- `internal/agent/dialogue/execution_context.go`：模型事实与接管上下文刷新。
- `internal/request/manager.go`、`internal/turn/execution.go`：请求关联与执行快照；`internal/session/binding.go` 提供公开绑定，`internal/session/share/binding.go` 定义内部契约，`internal/session/binding/` 管绑定与准入。

<!-- locator:signal -->
## 信号与订阅

- `internal/signal/`：泛型信号、订阅、调度与关闭。
- `internal/events/notification.go`、`platform.go`、`logging.go`：分别定义通知、平台连接与日志事件。
- `internal/agent/contracts.go`：公开 Hook、状态、绑定及后台请求契约。
- `internal/agent/signals.go`：Agent 信号与通知、重试、清理、接管订阅。
- `internal/background/cron/lifecycle.go`：平台连接通知队列与启停。
- `internal/session/naming/signals.go`、`naming/service.go`：分别负责命名事件与命名任务管理。
- `internal/modelmgr/signals.go`、`logging.go`：分别负责模型重试订阅与日志。
- `internal/agent/share/logging.go`、`internal/background/share/logging.go`：所属业务日志发布。

<!-- locator:config -->
## 配置、资产与日志

- `internal/config/`：配置加载、默认资产、配置声明与检查。
- `internal/config/definition.go`、`provider.go`：分别负责配置规则查询与校验、厂商配置加载。
- `internal/config/inspect.go`、`inspect_toml.go`：分别负责诊断装配与模块类型／未知字段检查、根文档字段与规则检查；模块规则并入根文档统一检查。
- `internal/doctor/`、`internal/command/builtin/doctor.go`：配置诊断与命令入口。
- `internal/logging/logging.go`、`signals.go`：分别负责日志中心与订阅。
- `internal/logging/record.go`、`reader.go`：分别负责日志格式化与查询。
- [配置文档](../docs/configuration.md)。

<!-- locator:agent-chat -->
## Agent 对话

- `internal/agent/{input,execution,confirmation,hooks,output,tools}/`：分别负责输入、执行、确认、扩展点、输出与工具桥接。
- `internal/agent/share/dialogue.go`、`hooks.go`：分别定义公共对话协作契约与 Hook 载荷。
- `internal/agent/share/status.go`、`execution/status.go`、`dialogue/model_call.go`：分别定义模型完成、执行状态与视觉降级事实。
- `internal/agent/input/message.go`、`input.go`：分别负责消息分发与输入准备。
- `internal/agent/background.go`：后台执行、复制、清理与接管查询；`input/background.go` 准备后台输入，`input/background_deps.go` 定义其依赖。
- `internal/background/execution/runner.go`、`runner_deps.go`：分别负责后台输入转接与执行依赖。
- `internal/agent/dialogue/runner.go`、`preparation.go`：分别负责单轮编排与输入准备。
- `internal/agent/dialogue/materials.go`：消息投影与模型请求材料；公共编解码见 `internal/agent/share/dialogue.go`。
- `internal/agent/dialogue/model_call.go`、`system_prompt.go`：分别负责公共模型调用协作与 Prompt 来源。
- `internal/agent/dialogue/message_store.go`、`commit.go`、`reply_commit.go`：分别负责消息存储、工具结果提交与回复提交。
- `internal/agent/chat/`：Chat Loop、请求、历史、摘要与 seed。
- `internal/agent/responses/loop.go`、`model_call.go`、`continuation.go`：分别负责 Responses 循环、模型调用与续接恢复。
- `internal/agent/responses/native_call.go`：原生模型调用、流处理与归档。
- `internal/agent/responses/context.go`、`input.go`、`persistence.go`：分别负责原生窗口、请求组装与持久化。
- `internal/agent/responses/tool_inputs.go`、`input_format.go`：分别负责工具声明输入与输入格式。
- `internal/agent/share/segments.go`、`internal/agent/input/inbound_media.go`：分别定义消息片段与处理入站媒体。

<!-- locator:protocol-routing -->
## 协议路线

- `internal/agent/routes/`：按协议登记对话、压缩与派生能力。
- `internal/agent/wiring.go`：Agent 组件与协议路线装配；资源构造见 `internal/app/resources.go`。
- `internal/agent/share/dialogue.go`、`internal/contextmgr/compact_contract.go`：分别定义对话与压缩契约；`internal/session/deps.go` 定义材料依赖，`internal/session/contracts.go` 定义公开材料结果。
- `internal/llm/origin.go`、`internal/modelmgr/compatibility.go`：分别定义模型归属与切换兼容性。
- `internal/llm/api_type.go`、`internal/tool/registry/availability.go`：分别定义协议类型与工具可用性。

<!-- locator:commands -->
## 命令与补全

- `internal/command/`、`internal/command/builtin/register.go`：命令路由与契约；后者负责内置命令注册。
- `internal/command/format.go`：根据前缀、命令名与参数组装文本；`internal/agent/wiring.go`、`internal/hook/wiring.go` 绑定配置首个前缀，注入 `CommandText` 能力。`command.Info.Args` 仅保存参数语法。
- `internal/command/parse.go`：命令解析结果与最长前缀匹配；Router 复用解析，`internal/platform/wiring.go` 绑定配置并向 QQ 适配器、引用恢复组件注入 `ParseCommand`，子包不传递前缀列表。
- `internal/command/builtin/`、`internal/agent/input/command_executor.go`：分别负责命令实现与执行协调。
- `internal/command/completion.go`：补全 token、候选与替换范围。
- `internal/command/builtin/log.go`、`usage.go`：分别负责日志查询与用量统计。
- `internal/command/builtin/request.go`：请求命令与请求树展示；收集原始请求详情，由 `/requests` 和 `/status` 的展示出口选择性组装。
- `internal/completion/`：结构化补全；`tool.go` 定义工具查询契约，`internal/agent/wiring.go` 装配查询上下文。
- `internal/agent/input/file_rollback.go`、`internal/agent/share/file_rollback.go`、`internal/command/builtin/rollback.go`：分别负责撤销准入、绑定契约与撤销命令。
- [命令文档](../docs/commands.md)。

<!-- locator:request-turn -->
## Request、Turn 与状态

- `internal/request/`、`internal/turn/`：分别负责请求树与取消、单轮状态与执行协调。
- `internal/turn/supervisor.go`、`contracts.go`、`wiring.go`：公开业务入口、契约与内部组件装配；`execution.go` 管逻辑执行身份、接管和最终结果。
- `internal/turn/lifecycle/`：唯一持有状态表与锁，管理 attempt、pending、工具计数及确认登记；`confirmation/` 管等待与超时，使用 `share/` 中登记结果携带的回调修改状态。
- `internal/agent/share/confirmation.go`：追加确认词识别与等待策略契约；`internal/command/builtin/session_format.go`：会话与工具计数展示。
- `internal/agent/execution/execution.go`、`execution_run.go`：前后台执行、接管与跨轮收尾。
- `internal/agent/execution/execution_admission.go`、`execution_input.go`、`execution_lifecycle.go`：分别负责执行准入、输入处理与关闭。
- `internal/processenv/environment.go`、`process_unix.go`、`process_windows.go`：分别负责进程环境与命令构造、Unix 进程组及 Windows 进程树管理。
- `internal/agent/confirmation/confirmation.go`、`risk_confirmation.go`：分别负责风险确认协调与交互提示。
- `internal/runtime/`、`internal/agent/execution/status.go`：分别定义运行状态与记录执行状态／用量。

<!-- locator:tool -->
## Tool Runtime、工具状态与文件

- `internal/tool/supervisor.go`、`lifecycle.go`：分别负责 Tool 公开入口与加载／关闭生命周期。
- `internal/tool/registry/`、`execution/`、`builtin/`：工具目录、执行核心与内置工具。
- `internal/tool/share/`：公共定义、schema 构造与纯数据辅助。
- `internal/tool/share/definition.go`、`risk.go`：分别定义工具接口与风险；`prepared.go` 保存准备结果，执行回调并统一截断独立日志正文。
- `internal/tool/wiring.go`：装配内置工具的显式依赖，向编辑与撤销工具注入同一文件操作服务。
- `internal/tool/builtin/shell.go`：准备命令、解析一次 Bash AST 并执行；`shell_warnings.go`、`shell_risk.go`、`shell_sandbox.go` 复用 AST，分别负责文件保护与警告、风险分类、受限路径检查。
- `internal/tool/builtin/cron_contract.go`：Cron 工具的存储与管理契约。
- `internal/tool/cron_bridge.go`：Cron 存储适配、模型校验与任务变更通知。
- `internal/tool/chat_history/manager.go`：历史查询组件与 Backend 契约；`query.go` 管查询，`media.go` 管媒体选择与获取。
- `internal/tool/chat_history_bridge.go`：聊天历史存储与媒体能力适配。
- `internal/tool/config_bridge.go`：Elwisp 创建工具的配置读取适配。
- `internal/tool/resident_memory_bridge.go`：常驻记忆的身份范围与存储适配。
- `internal/tool/builtin/elwisp_creator.go`、`resident_memory.go`：分别负责 Elwisp 创建指引与常驻记忆工具。
- `internal/tool/builtin/chat_history.go`、`get_media.go`、`view_image.go`：分别负责历史查询、媒体获取与看图工具；`cron.go` 管任务定义工具。
- `internal/tool/discovery/resolve.go`：工具发现请求与依赖解析。
- `internal/tool/state/state.go`：会话工具状态的读取、更新准备与提交。
- `internal/tool/state/discovered.go`：已发现工具声明的规范化、合并与排序。
- `internal/tool/preload/preload.go`、`tags.go`：分别负责工具／Skill 预载与标签配置。
- `internal/tool/preload/background.go`：后台工具声明、状态与提示材料预载。
- `internal/agent/input/tool_directive.go`、`internal/agent/share/tool_state.go`：输入指令与状态提交。
- `internal/tool/builtin/file_tools_ast.go`、`file_tools_directory.go`：分别负责 AST 查询与目录搜索。
- `internal/tool/builtin/long_memory.go`：长期记忆格式、索引与同步。
- `internal/tool/builtin/web_extract.go`：网页正文提取与缓存。
- `internal/fileops/binding.go`、`commit.go`：操作绑定与提交校验。
- `internal/fileops/edit_service.go`、`rollback_service.go`：分别负责编辑与撤销的准备、预览和提交。
- `internal/fileops/preview.go`：文件修改预览、批准内容快照与修订校验。
- `internal/fileops/file.go`、`atomic_write.go`、`rollback.go`：分别负责文件操作结果、原子写入与撤销备份。
- `internal/workspace/workspace.go`、`resolve.go`、`state.go`：分别负责工作目录、路径解析与状态编码；`internal/session/workspace.go` 提供公开入口，`internal/session/control/workspace.go` 管持久化。
- `internal/workspace/context.go`：调用路径范围与目录能力的上下文传递。
- `internal/workspace/sandbox.go`：沙盒目录组合与受限路径规则。
- `internal/tool/share/media.go`：工具媒体输入、导入与结果契约。
- `internal/tool/media/runtime.go`：工具媒体入口与 Media 数据转换。
- `internal/tool/media/call.go`：调用状态、引用持有、释放重试与临时资源清理。
- `internal/tool/media/files.go`、`cache.go`：分别负责媒体文件导入导出与共享缓存。
- `internal/tool/builtin/send_file.go`：文件发送工具。

<!-- locator:tool-flow -->
## 工具调用链路

- `internal/agent/dialogue/tool_execution.go`：协议共用的 ToolRun 入口与提交器。
- `internal/agent/tools/toolrun_adapter.go`：Hook、子请求、确认与状态桥接。
- `internal/tool/execution/runner.go`：工具批次编排、确认、并发执行与结果处理。
- `internal/agent/dialogue/commit.go`、`internal/agent/responses/persistence.go`：分别负责公共结果提交与 Responses 原生状态保存。
- `internal/tool/execution/manager.go`：执行依赖与直接调用入口；`schema.go` 组合工具声明。
- `internal/tool/execution/prepared.go`：调用准备；`executor.go` 执行原生调用，`elwisp_exec.go` 准备并执行远程调用。
- `internal/tool/logging.go`：完整工具日志发布。
- `internal/storage/tool_batch.go`、`internal/storage/sqlite/tool_batch.go`、`dialogue_repository.go`：分别定义工具批次、保存结果与提交对话事务；`tool_call_repository.go` 查询调用统计。

<!-- locator:skill -->
## Skill 与 ELyph

- `internal/elyph/`、`rules.go`：ELyph 解析与 lint；`rules.go` 管公共规则卡。
- `internal/tool/discovery/`：工具发现、详情加载与激活。
- `internal/agent/dialogue/preparation.go`、`model_call.go`、`internal/tool/execution/runner.go`：输入与工具结果的规则卡组装。
- `internal/storage/dialogue.go`、`internal/storage/sqlite/dialogue_repository.go`、`internal/contextmgr/state.go`：规则展示状态提交与压缩重置。
- `internal/tool/skill/manager.go`：技能加载、注册、重载与修改协调。
- `internal/tool/skill/scanner.go`、`catalog.go`：分别负责候选扫描与活动技能目录。
- `internal/tool/skill/command_tool.go`、`go_runner.go`：分别负责命令技能与 Go 技能执行。
- `internal/tool/skill/skill_md_parser.go`、`elbot_skill_manifest.go`：分别解析 SKILL.md 与 ELBOT_SKILL.toml。
- `internal/tool/skill/create_el_skill.go`、`modify_el_skill.go`、`finalize_el_skill.go`：分别负责原生技能创建、修改与编译注册；`go_source.go` 处理源码格式与校验。
- `internal/tool/skill/read_el_skill.go`：原生技能读取工具。
- `internal/tool/skill/process.go`：技能进程执行与 Go 编译。
- `internal/tool/skill/media.go`：媒体参数准备与 stdout 结果导入。
- `internal/config/assets.go`：内置 Skill 说明。

<!-- locator:hook -->
## Hook 与插件

- `internal/hook/supervisor.go`、`deps.go`：分别负责 Hook 公开入口与消费契约。
- `internal/hook/wiring.go`、`lifecycle.go`：分别负责 Hook 装配与启停。
- `internal/hook/config.go`：汇总 Hook 内部模块的只读配置检查定义。
- `internal/hook/agent_bridge.go`：Agent 回调注册与 Hook 事件转换。
- `internal/hook/platform_signals.go`：平台连接订阅与全局 Hook 输出。
- `internal/hook/context_to_event.go`：公共上下文到 Hook 事件的转换。
- `internal/hook/share/event.go`、`match.go`、`call_policy.go`、`registration.go`、`runtime.go`：分别定义事件、匹配、调用策略、注册与运行查询契约。
- `internal/hook/share/runtime_config.go`、`control.go`：分别定义插件配置与组件协作契约。
- `internal/hook/pipeline/manager.go`、`options.go`：分别负责 Hook 流水线与调用选项。
- `internal/hook/rules/`：规则配置与执行；`config.go` 逐文件加载校验并统一确定名称与动作来源，`rules.go` 单次注册规则并提供日志身份构造，`rule.go` 归一化角色、统一角色判断并编排动作及只读校验；`toml_error.go` 包装解析器原生诊断。
- `internal/hook/control/service.go`：插件查询、重载与进程管理。
- `internal/hook/control/command.go`：Hook 管理命令与补全；`internal/hook/share/logging.go` 管日志摘要。
- `internal/hook/runtime/`：插件进程、RPC 与续接；`runtime/routes.go` 管 waiting 路由、一次性到期定时器及撤销计数，并提供续接与工具审计共用的日志身份构造；`runtime/media.go` 管媒体租约、释放通知和有上限的清理退避。
- `internal/hook/runtime/manager.go`、`worker.go`：分别负责运行任务登记、并发停止与实际退出等待，以及插件进程、可取消协议写入和统一关闭预算；`lifecycle_test.go` 测试运行时关闭，`behavior_test.go` 覆盖管道阻塞、续接取消及 waiting 到期竞态。
- `internal/hook/runtime/expiry.go`：KV／媒体共用的带索引最小堆；`shared.go` 管 KV 续期与到期删除，`manager.go` 的 `maintain` 共用调度循环实现、独立运行 KV／媒体维护并等待退出；`expiry_test.go` 验证调度、退避、错误日志与关闭。
- `internal/hook/runtime/maintenance_bench_test.go`：使用实际到期索引测量 KV／媒体维护、续期与到期成本；逐项定时器保留为调度成本对照，不包含真实媒体 I/O。
- `internal/hook/media.go`、`logging.go`：分别负责媒体契约与日志通知；`share/output/` 转换输出，`share/protocol/` 编解码 hook.v2。
- `internal/hook/agent_bridge_test.go`、`supervisor_test.go`、`wiring_test.go`：分别测试 Agent 桥接、总管与装配；`pipeline/continuation_test.go` 测试续接。
- `internal/hook/command_test.go`：命令与补全测试；`internal/agent/hook_integration_test.go`：Agent／Hook 集成测试。
- `internal/agent/hook_bridge.go`：Hook 回调注册与子请求登记。
- `internal/agent/hooks/points.go`：内部回调组与事实组合；`hooks.go` 管调度，`internal/agent/share/hooks.go` 定义载荷。
- [Hook 文档](../docs/hooks.md)。

<!-- locator:output -->
<!-- locator:notification -->
## 输出、投递与通知

- `internal/delivery/delivery.go`：输出、目标、回执与发送契约，`DeliveryInfo` 投递信息、发送前校验、来源日志与降级文案；`internal/platform/dispatch/` 管发送路由和媒体准备。
- `internal/platform/notification/manager.go`：通知目标、校验与队列；`internal/notification/rules/` 定义 Agent 提示规则。
- `internal/platform/wiring.go`：平台装配、通知管理器与故障通知接线。
- `internal/platform/{cli,qq-onebot,qqofficial,telegram}/notifications.go`：平台通知协议与实际发送。
- `internal/agent/output/output.go`、`internal/agent/execution/turn_output.go`、`execution_output.go`：分别负责输出 Hook、单轮输出与跨轮输出。
- `internal/agent/confirmation/confirmation.go` 生成确认正文与 `delivery.Action`；`internal/platform/{qqofficial,telegram}/keyboard.go` 将操作列表转换为平台按钮，不解析正文。
- `internal/agent/dialogue/output.go`、`reply_commit.go`：分别定义正文／思考输出能力与提交回复。

<!-- locator:platform -->
## 平台适配

- `internal/platform/{supervisor,deps,wiring,lifecycle}.go`：分别负责平台总管、消费契约、装配与启停。
- `internal/platform/{contracts,agent,send,public_api,client,config}.go`：分别负责公开契约、Agent 转接、发送、原生 API／媒体、客户端与配置。
- `internal/platform/share/`：平台内部契约；`runtime_contracts.go` 管运行和入站，`adapter_api.go` 管原生 API，`notification.go` 管通知，`config.go` 管配置，`history_contracts.go` 管历史，`backoff.go` 管退避。
- `internal/platform/dispatch/router.go`：平台注册、目标选择与发送路由。
- `internal/platform/dispatch/calls.go`：按平台转发原生 API 请求。
- `internal/platform/dispatch/media.go`：平台媒体解析、出站准备与回执关联。
- `internal/platform/telegram/capability_api.go`：Telegram 原生 API 入口。
- `internal/platform/refcontext/refcontext.go`：引用消息恢复与上下文组装。
- `internal/platform/cli/`：本地／远程 CLI；`lifecycle.go` 管本地启停，`remote_lifecycle.go` 管远程启停，`cli.go`、`remote_server.go` 管输入与发送。
- `internal/platform/cli/event_contracts.go`：CLI 状态与绑定契约；`ui.go` 管订阅，`status.go` 管展示状态，`reasoning.go` 管思考流，`status_format.go` 管状态文案。
- `internal/platform/cli/client.go`：客户端连接选择；`remote_client.go` 管远程通信，`tui.go` 管终端交互。
- `internal/platform/contracts.go`：平台公开消息事实与客户端结果。
- `internal/platform/public_api.go`：公开平台 API 与媒体解析入口。
- `internal/platform/share/{message,reference}.go`：分别定义内部消息与引用契约；`internal/platform/agent.go` 转换输入及恢复平台上下文。
- `internal/agent/contracts.go`、`share/input.go`：分别定义 Agent 公开请求与内部输入上下文。
- `internal/storage/chat_history.go`：聊天历史片段、引用及持久化编解码。
- `internal/media/types.go`、`transfer/platform.go`、`object/history.go`：分别定义媒体公开类型、编排平台媒体获取与管理历史关联。
- `internal/platform/{lifecycle,contracts,agent}_test.go`：分别测试生命周期、公开契约与 Agent 输入转换。
- `internal/platform/qq-onebot/adapter.go`：OneBot 适配器构造与依赖。
- `internal/platform/qq-onebot/{lifecycle,send}.go`：分别负责 OneBot 启停与消息发送。
- `internal/platform/qq-onebot/{message,transport,capability_api}.go`：分别负责入站消息、WebSocket 传输与原生 API。
- `internal/platform/qq-onebot/{conversion,forward,attachment,notifications,config_definition}.go`：分别负责片段转换、合并转发、附件解析、通知与配置定义。
- `internal/platform/qqofficial/adapter.go`：QQ Official 适配器与依赖。
- `internal/platform/qqofficial/lifecycle.go`：QQ Official 启停与资源清理。
- `internal/platform/qqofficial/{config,gateway,message,send}.go`：分别负责配置构造、网关通信、入站消息与发送。
- `internal/platform/qqofficial/{client,token,attachment,keyboard,types,notifications,config_definition}.go`：分别负责 HTTP、令牌、附件、键盘、协议类型、通知与配置定义。
- `internal/platform/telegram/adapter.go`：Telegram 适配器与依赖。
- `internal/platform/telegram/{lifecycle,send,commands}.go`：分别负责 Telegram 启停、发送与命令同步。
- `internal/platform/telegram/{config,message,stream,client}.go`：分别负责配置、入站消息、流式发送与 HTTP 客户端。
- `internal/platform/telegram/{format,keyboard,types,notifications,config_definition}.go`：分别负责文本格式、键盘、协议类型、通知与配置定义。
- `internal/platform/headless/headless.go`：Service 模式的无界面平台占位。
- `internal/platform/qqofficial/reference.go`：QQ 引用索引与真实消息 ID 映射。

<!-- locator:session -->
## Session

- `internal/background/share/takeover.go`：后台接管查询契约。

文件路径相对 `internal/session/`：

| 位置 | 文件职责 |
|---|---|
| 根部 | `supervisor.go` 总管；`deps.go` 消费契约；`wiring.go` 装配；`lifecycle.go` 启停；`contracts.go` 公开契约；`operations.go` 会话操作；`background.go` 后台准备；`branch.go` 派生；`binding.go` 绑定；`origin.go` 归属；`workspace.go` 工作目录；`naming.go` 命名；`signals.go` 事件 |
| `binding/` | `manager.go` 绑定管理；`coordination.go` 执行准入；`signals.go` 绑定事件 |
| `control/` | `service.go` 组件与启停；`current.go` 当前会话；`promotion.go` 恢复与接管；`background.go` 后台准备；`query.go` 查询；`management.go` 管理；`expiration.go` 过期与清理；`origin.go` 归属；`workspace.go` 工作目录 |
| `branch/` | `service.go` 材料准备；`fork.go` 前台分支；`copy.go` 后台副本；`compact.go` 压缩派生 |
| `naming/` | `service.go` 任务管理；`naming.go` 命名流程；`title.go` 标题生成；`signals.go` 命名事件 |
| `share/` | `session.go` 会话事实与访问规则；`binding.go` 绑定契约；`origin.go` 归属规则 |

- `internal/storage/sqlite/session_repository.go`：会话存储；`internal/platform/refcontext/refcontext.go`：引用回复恢复。

- `internal/agent/execution/status.go`、`confirmation/confirmation.go`、`hook_bridge.go`：执行状态、自动确认与 Hook 请求管理。

<!-- locator:context -->
## 上下文、Prompt 与压缩

- `internal/contextmgr/`：历史、窗口、用量与压缩分派。
- `internal/agent/execution/execution_compact.go`、`context_usage.go`：压缩生命周期、交接及用量。
- `internal/agent/chat/compact.go`、`compressor.go`：Chat 摘要。
- `internal/agent/responses/compact.go`、`branch.go`：原生压缩与分支材料。
- `internal/agent/dialogue/system_prompt.go`、`prompt.go`：公共 Prompt；`internal/agent/share/dialogue.go` 定义接管提示。
- `internal/agent/chat/transcript.go`：Chat 历史与 Prompt Builder。

<!-- locator:llm -->
## 模型服务与 LLM 客户端

- `internal/modelmgr/`：模型选择、兼容性、目录与状态。
- `internal/modelmgr/manager.go`、`client.go`：分别负责模型总管与客户端装配。
- `internal/modelmgr/providers.go`、`selection.go`、`context_window.go`：分别负责厂商快照、模型选择与窗口缓存。
- `internal/agent/deps.go`、`internal/background/deps.go`、`internal/tool/deps.go`：各模块消费的模型能力契约。
- `internal/contextmgr/service.go`：上下文服务与模型消费契约；`internal/session/deps.go` 定义命名依赖，`internal/session/wiring.go` 装配。
- `internal/llm/`：模型、消息、工具与用量公共契约。
- `internal/llm/httpclient/`：HTTP、重试、SSE 与超时。
- `internal/llm/chatcompletions/`、`internal/llm/responses/`：分别实现 Chat 与 Responses 协议。
- `internal/llm/responses/client.go`、`tools.go`、`compact.go`：分别负责 Responses 请求、工具权限与原生压缩。
- `internal/llm/responses/stream.go`、`error.go`、`response.go`：分别负责 Responses 流、错误与响应解析。

<!-- locator:storage -->
## Storage 与 SQLite

- `internal/storage/storage.go`、`session_metadata.go`：分别定义存储领域契约与 Session 元数据。
- `internal/storage/sqlite/`：SQLite 实现；`store.go` 的 openDB 共用主库与聊天历史库连接初始化，`db_init.go` 管新库结构，`migrations.go` 管升级。
- `internal/storage/dialogue.go`、`internal/storage/sqlite/dialogue_repository.go`：分别定义对话材料契约与实现提交事务。
- `internal/storage/sqlite/session_repository.go`：Session 查询与字段合并更新。
- `internal/storage/sqlite/native_snapshot.go`、`native_material.go`：分别负责原生快照与窗口材料读取。
- `internal/storage/sqlite/media_history.go`、`media_lifecycle.go`：分别负责历史媒体关联与媒体回收。
- `internal/storage/sqlite/chat_history_repository.go`：聊天历史查询、清理与平台引用索引。

<!-- locator:elnis -->
## Elnis / Elvena / Elwisp

- `internal/elvena/types.go`、`origin.go`：分别定义公共协议与来源。
- `internal/background/elnis/types.go`：Elnis 事件类型。
- `internal/background/elnis/http.go`：HTTP 路由与连接管理。
- `internal/background/elnis/lifecycle.go`：Elnis 启停、工作队列与故障观察。
- `internal/background/elnis/service.go`：去重分发与中断执行恢复。
- `internal/background/elnis/llm.go`、`store.go`：模型流程与事件状态。
- `internal/background/elnis/direct.go`：平台原生调用与直接投递。
- `internal/background/elnis/auth.go`、`prepare.go`、`targets.go`：分别负责鉴权、输入规范化与目标选择。
- `internal/background/elnis/repository.go`：Elnis 事件存储契约。
- `internal/background/elnis/media.go`：事件媒体导入与报告附件；`internal/background/share/report.go` 提供公共附件处理。
- `internal/background/elnis/outbox.go`：报告持久化、投递与恢复。
- `internal/storage/sqlite/elnis_event_repository.go`：事件仓储与引用事务。
- `internal/background/share/`：后台共享契约；`result.go` 解析与序列化报告，`report.go` 处理附件与回执。
- [内部架构](elnis-elwisp.md)、[功能说明](../docs/elnis.md)、[配置与协议示例](../docs/elnis-usage.md)。

<!-- locator:media -->
## 媒体中心

- `internal/media/api.go`、`wiring.go`、`deps.go`：分别负责媒体公开入口、装配与消费契约。
- `internal/media/types.go`、`reference.go`：分别定义媒体公开类型与持有／引用入口。
- `internal/media/signals.go`：媒体使用结束事件；`signals_test.go` 测试信号与清理。
- `internal/agent/signals.go`、`internal/platform/signals.go`、`internal/tool/signals.go`：各消费者的媒体使用结束事件适配。
- `internal/app/shutdown.go`：应用资源关闭与媒体释放编排。
- `internal/media/wiring_test.go`：媒体配置与构造测试。
- `internal/media/transfer_integration_test.go`：媒体导入、交付与释放集成测试。
- `internal/media/object/manager.go`：对象管理组件；`import.go` 管导入，`read.go` 管读取导出，`image.go` 管图片处理。
- `internal/media/object/reference.go`：对象持有与引用；`cleanup.go` 管回收，`history.go` 管历史关联，`remote.go` 管副本与签名，`locks.go` 管对象锁；`release_test.go` 测试引用释放，`internal/media/transfer/usage_cancel_test.go` 测试取消清理。
- `internal/media/transfer/manager.go`：媒体交付组件；`usage.go` 管使用记录，`request.go` 管模型交付，`output.go` 管发送准备，`platform.go` 管平台获取。
- `internal/media/backend/local.go`：本地存储；`s3.go` 管 S3 传输，`remote.go` 管后端构造；`s3_upload_test.go` 测试上传。
- `internal/media/share/types.go`：内部类型；`helpers.go` 管校验与错误，`contracts.go` 定义组件接口；`helpers_test.go`、`errors_test.go` 测试辅助规则，`internal/media/reference_test.go` 测试引用参数解析。
- `internal/media/backend/missing_test.go`、`object/unavailable_test.go`：分别测试后端缺失识别与对象不可用处理。
- `internal/storage/sqlite/media_repository.go`：媒体元数据与引用；`media_lifecycle.go` 管回收认领。
- `internal/tool/builtin/chat_history.go`、`get_media.go`、`view_image.go`：历史查询、媒体获取与 Responses 看图。
- `internal/platform/dispatch/media.go`：输出媒体准备与回执缓存。
- `internal/hook/share/output/message.go`、`runtime/media.go`、`wiring.go`：分别负责 Hook 媒体来源、租约与装配；`runtime/manager.go` 管调用生命周期，`runtime/media_concurrency_test.go`、`internal/hook/media_order_test.go` 测试并发与顺序。
- `internal/storage/sqlite/db_init.go`、`migrations.go`、`media_lifecycle.go`：分别负责建库、迁移与媒体引用回收；`media_json_test.go` 测试报告媒体引用。
- `internal/testutil/mediatest/`：MediaCenter 测试夹具。

<!-- locator:cron -->
## Cron 与维护

- `internal/background/cron/`：后台任务调度、执行、报告与恢复。
- `internal/background/cron/service.go`、`manager.go`：分别负责任务运行与调度。
- `internal/background/cron/execution.go`：共用执行准备与投递流程，生成 LLM 报告并处理格式修正与接管结果。
- `internal/background/cron/repository.go`、`model.go`：分别定义 Cron 存储契约与任务模型。
- `internal/background/cron/refresh.go`：任务定义变更与调度刷新。
- `internal/background/cron/lifecycle.go`：平台通知消费与启停。
- `internal/background/cron/models.go`、`delivery.go`、`recovery.go`：分别负责任务模型选择、报告投递与恢复。
- `internal/background/maintenance/maintenance.go`：维护任务配置、注册与清理。
- `internal/background/share/maintenance.go`：共享维护任务配置。
- `internal/background/cron/maintenance.go`：维护任务到 Cron 的适配。
- `internal/background/execution/runner.go`：后台输入准备与 Agent 调用；`internal/security/security.go` 管工具权限，`internal/tool/execution/runner.go` 管执行。
- `internal/tool/builtin/cron.go`、`internal/storage/sqlite/cron_job_repository.go`：分别负责 Cron 工具与任务仓储。

<!-- locator:docs -->
## 文档

- `docs/`：中文用户文档；`devdocs/`：当前内部架构、代码入口与未完成任务。
- `CHANGELOG.md`：中文变更记录；`scripts/translate_docs.py`：按一级至三级标题切分并缓存完整小节，自动发布时检查原文版本、合并小节缓存并动态补译；`--commit-each` 要求干净且没有未推送提交的专用工作区。
- `.github/workflows/docs-translate.yml`：翻译任务按分支排队，检出最新分支；默认每个请求翻译一个小节，同时最多两个请求。
- `docs.en/`、`README.md`、`CHANGELOG.en.md` 为英文镜像或自动翻译产物，不手动修改。
