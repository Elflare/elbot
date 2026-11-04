# ElBot 代码地图

按主题定位主要目录与文件；职责边界和关键约束见[架构说明](architecture.md)。

## 定位方法

从 [AGENTS.md](../AGENTS.md#快速定位流程) 选择 locator，搜索后只读命中章节：

```bash
rg -n '^<!-- locator:tool-flow -->$' devdocs/code-map.md devdocs/architecture.md
```

用 `nrf <文件> -s <起始行> -e <结束行>` 读取；Go 符号已知用 `nls inspect`，未知先用 `nls ds`。注明“路径相对”时，该节路径以其为基准；条目以目录开头时，随后裸文件名相对该目录。

<!-- locator:startup -->
## 启动与装配

- `cmd/elbot/main.go`：进程入口、信号与退出码。
- `internal/launcher/cli.go`：参数、帮助与 Shell 补全。
- `internal/app/`：`entry.go` 启动；`environment.go` 运行环境与配置锁；`resources.go` 共享资源；`wiring.go` 总管装配；`shutdown.go` 关闭；`client.go` 客户端回退。
- `internal/{agent,tool,hook,platform,background}/`：各业务根包；`supervisor.go` 对外入口，`deps.go` 消费契约，`wiring.go` 装配，`lifecycle.go` 启停。

<!-- locator:contextinfo -->
## 公共上下文事实

- `internal/contextinfo/`：会话、身份、执行、模型与后台来源。
- `internal/security/`：身份、权限与风险策略。
- `internal/platform/agent.go`：平台输入转换；`internal/platform/cli/message.go`：CLI 来源。
- `internal/agent/dialogue/execution_context.go`：执行上下文与接管刷新。
- `internal/request/manager.go`、`internal/turn/execution.go`：请求关联、执行快照。
- `internal/session/binding.go`、`internal/session/binding/`：绑定公开入口、准入实现。

<!-- locator:signal -->
## 信号与订阅

- `internal/signal/`：信号、订阅与分发。
- `internal/events/`：`notification.go` 通知；`platform.go` 连接；`logging.go` 日志事件。
- `internal/agent/`：`contracts.go` 公开事件契约；`signals.go` 订阅。
- `internal/modelmgr/`：`signals.go` 重试订阅；`logging.go` 日志。
- `internal/agent/share/logging.go`、`internal/background/share/logging.go`：业务日志发布。

<!-- locator:config -->
## 配置、资产与日志

- `internal/config/`：`config.go` 主配置；`assets.go` 默认资产；`definition.go` 配置规则；`provider.go` 厂商配置。
- `internal/config/`：`inspect.go` 诊断装配；`inspect_toml.go` TOML 检查。
- `internal/doctor/`、`internal/command/builtin/doctor.go`：诊断服务、命令入口。
- `internal/logging/`：`logging.go` 日志中心；`signals.go` 订阅；`record.go` 格式；`reader.go` 查询。
- [配置文档](../docs/configuration.md)。

<!-- locator:agent-chat -->
## Agent 对话

路径相对 `internal/agent/`：

- `input/`：`message.go` 输入分发；`input.go` 输入准备；`background.go` 后台输入；`inbound_media.go` 入站媒体。
- `execution/`：`execution.go` 执行协调；`execution_run.go` 跨轮运行；`execution_admission.go` 准入；`execution_input.go` 输入处理；`execution_lifecycle.go` 关闭。
- `dialogue/`：`runner.go` 单轮编排；`preparation.go` 准备；`materials.go` 消息投影；`model_call.go` 公共模型协作。
- `dialogue/`：`message_store.go` 消息存储；`commit.go` 工具结果提交；`reply_commit.go` 回复提交。
- `chat/`：Chat 循环、历史与摘要。
- `responses/`：`loop.go` 循环；`model_call.go` 模型调用；`native_call.go` 原生流与归档；`continuation.go` 续接恢复。
- `responses/`：`context.go` 原生窗口；`input.go` 请求；`persistence.go` 持久化；`tool_inputs.go` 工具声明；`input_format.go` 输入格式。
- `share/`：`dialogue.go` 公共对话契约；`hooks.go` Hook 载荷；`segments.go` 消息片段。
- `background.go`：后台执行、复制、清理及接管查询。

<!-- locator:protocol-routing -->
## 协议路线

- `internal/agent/routes/`、`internal/agent/wiring.go`：能力登记、路线装配。
- `internal/llm/`：`origin.go` 模型归属；`api_type.go` 协议类型。
- `internal/modelmgr/compatibility.go`：模型切换兼容性。
- `internal/contextmgr/compact_contract.go`、`internal/session/deps.go`：压缩、派生材料契约。
- `internal/tool/registry/availability.go`：工具运行资格。

<!-- locator:commands -->
## 命令与补全

- `internal/command/`：`router.go` 路由；`parse.go` 解析；`format.go` 组装；`completion.go` 补全。
- `internal/command/builtin/`：`register.go` 注册；其他命令按同名文件定位。
- `internal/command/builtin/`：`log.go` 日志入口；`log_query.go` 查询解析；`log_format.go` 通用展示；`audit_format.go` 审计展示；`usage.go` 用量统计。
- `internal/agent/input/command_executor.go`：执行协调。
- `internal/agent/input/file_rollback.go`、`internal/command/builtin/rollback.go`：撤销准入、撤销命令。
- `internal/completion/`：结构化补全与工具候选。
- [命令文档](../docs/commands.md)。

<!-- locator:request-turn -->
## Request、Turn 与状态

- `internal/request/`：请求树、取消与超时。
- `internal/turn/`：`supervisor.go` 入口；`contracts.go` 契约；`execution.go` 逻辑执行；`lifecycle/` 状态管理；`confirmation/` 确认等待。
- `internal/agent/confirmation/`：`confirmation.go` 确认协调；`risk_confirmation.go` 风险提示。
- `internal/agent/execution/status.go`、`internal/runtime/`：状态记录、状态定义。
- `internal/processenv/`：`environment.go` 进程环境；`process_unix.go`、`process_windows.go` 进程树管理。

<!-- locator:tool -->
## Tool Runtime、工具状态与文件

路径相对 `internal/`：

- `tool/registry/`、`tool/execution/`、`tool/builtin/`：工具目录、执行核心、内置工具。
- `tool/contracts.go`、`tool/supervisor.go`：公开工具定义、批量注册与统一查询入口；`tool/wiring.go`、`tool/builtin/wiring.go`：公共组件与 Builtin 内部接线。
- `tool/share/`：`definition.go` 定义；`risk.go` 风险；`prepared.go` 准备结果；`media.go` 媒体契约。
- `tool/builtin/`：`shell.go` Shell；`shell_warnings.go` 文件警告；`shell_risk.go` 风险；`shell_sandbox.go` 路径限制。
- `tool/builtin/`：`file_tools_ast.go` AST 查询；`file_tools_directory.go` 目录搜索。
- `tool/builtin/long_memory/`：`tools.go` 工具入口；`store.go` 记忆写入；`index.go` 索引同步；`files.go` 文件格式；`search.go` 检索与展示。
- `tool/builtin/web/`：`extract.go` 网页提取入口；`extract_fetch.go` 提取请求与响应；`extract_html.go` 直抓文本与链接；`extract_cache.go` 提取缓存；`search.go` 网页搜索。
- `tool/builtin/`：`chat_history.go` 历史；`get_media.go` 媒体获取；`view_image.go` 看图；`send_file.go` 文件发送。
- `tool/builtin/`：`cron.go` 任务；`resident_memory.go` 常驻记忆；`elwisp_creator.go` Elwisp 创建指引。
- `tool/`：`cron_bridge.go` Cron 适配；`chat_history_bridge.go` 历史适配；`config_bridge.go` 配置适配；`resident_memory_bridge.go` 记忆适配。
- `tool/chat_history/`：`manager.go` 查询入口；`query.go` 查询；`media.go` 媒体选择。
- `tool/discovery/resolve.go`：发现与依赖解析。
- `tool/state/`：`state.go` 状态更新；`discovered.go` 已发现声明。
- `tool/preload/`：`preload.go` 预载；`tags.go` 标签；`background.go` 后台预载。
- `agent/input/tool_directive.go`、`agent/share/tool_state.go`：工具指令、状态提交契约。
- `fileops/`：`edit_service.go` 编辑；`rollback_service.go` 撤销；`preview.go` 预览；`binding.go` 绑定；`commit.go` 提交；`atomic_write.go` 原子写入；`rollback.go` 备份。
- `workspace/`：`workspace.go` 目录；`resolve.go` 解析；`context.go` 调用范围；`sandbox.go` 沙盒；`state.go` 状态。
- `session/workspace.go`、`session/control/workspace.go`：工作目录入口、持久化。
- `tool/media/`：`runtime.go` 媒体适配；`call.go` 调用资源；`files.go` 文件导入导出；`cache.go` 缓存。

<!-- locator:tool-flow -->
## 工具调用链路

- `internal/agent/dialogue/tool_execution.go`：公共 ToolRun 入口。
- `internal/agent/tools/toolrun_adapter.go`：Hook、子请求、确认与状态适配。
- `internal/tool/execution/`：`runner.go` 批次编排；`manager.go` 直接调用；`prepared.go` 准备；`executor.go` 原生执行；`elwisp_exec.go` 远端执行；`schema.go` 工具声明。
- `internal/agent/dialogue/commit.go`、`internal/agent/responses/persistence.go`：结果提交、原生持久化。
- `internal/storage/tool_batch.go`、`internal/storage/sqlite/tool_batch.go`：批次契约、批次存储。
- `internal/tool/logging.go`：工具日志发布。

<!-- locator:skill -->
## Skill 与 ELyph

- `internal/elyph/`：ELyph 解析、lint 与规则卡。
- `internal/tool/discovery/`：发现、详情与激活。
- `internal/tool/skill/`：`manager.go` 协调；`scanner.go` 扫描；`catalog.go` 活动目录。
- `internal/tool/skill/`：`command_tool.go` 命令技能；`go_runner.go` Go 技能；`process.go` 进程；`media.go` 媒体。
- `internal/tool/skill/`：`skill_md_parser.go` SKILL.md；`elbot_skill_manifest.go` manifest。
- `internal/tool/skill/`：`create_el_skill.go` 创建；`modify_el_skill.go` 修改；`finalize_el_skill.go` 编译注册；`read_el_skill.go` 读取。

<!-- locator:hook -->
## Hook 与插件

路径相对 `internal/hook/`：

- `agent_bridge.go`、`context_to_event.go`：Agent 桥接、上下文转换。
- `platform_signals.go`、`logging.go`：平台订阅、日志。
- `pipeline/manager.go`、`pipeline/options.go`：流水线、调用选项。
- `rules/`：`config.go` 配置；`rules.go` 注册；`rule.go` 规则编排；`action.go` 动作；`exec.go` 短期进程。
- `runtime/`：`manager.go` 运行生命周期；`worker.go` 进程与 RPC；`routes.go` waiting 路由；`shared.go` KV；`media.go` 媒体租约；`expiry.go` 到期队列。
- `control/service.go`、`control/command.go`：插件管理、管理命令。
- `share/`：`event.go` 事件；`control.go` 协作；`runtime.go` 运行查询；`runtime_config.go` 插件配置；`output/` 输出；`protocol/` 协议。
- Agent 侧：`internal/agent/hook_bridge.go` 桥接；`internal/agent/hooks/` 扩展点。
- [Hook 文档](../docs/hooks.md)。

<!-- locator:output -->
<!-- locator:notification -->
## 输出、投递与通知

- `internal/delivery/delivery.go`：输出、目标、回执与发送契约。
- `internal/platform/dispatch/`：`router.go` 发送路由；`media.go` 媒体准备；`calls.go` 原生 API。
- `internal/platform/notification/manager.go`、`internal/notification/rules/`：通知队列、提示规则。
- `internal/agent/output/output.go`、`internal/agent/dialogue/reply_commit.go`：输出 Hook、回复提交。
- `internal/agent/execution/`：`turn_output.go` 单轮输出；`execution_output.go` 跨轮输出。
- `internal/platform/{cli,qq-onebot,qqofficial,telegram}/notifications.go`：平台通知。
- `internal/platform/{qqofficial,telegram}/keyboard.go`：平台按钮。

<!-- locator:platform -->
## 平台适配

路径相对 `internal/platform/`：

- `contracts.go`、`agent.go`、`send.go`、`public_api.go`、`client.go`、`config.go`：公开契约、Agent 转接、发送、原生 API／媒体、客户端、配置。
- `registration.go`、各适配器 `definition.go`：根部登记与构造入口、各平台静态声明。
- `share/`：平台内部契约；`definition.go` 静态声明类型，`notification.go` 通知接入与目标转换。
- `refcontext/refcontext.go`：引用恢复。
- `cli/`：`cli.go` 本地入口；`lifecycle.go` 本地启停；`remote_lifecycle.go` 远程启停；`remote_server.go` 远程服务。
- `cli/`：`client.go` 连接选择；`remote_client.go` 远程通信；`tui.go` 终端交互；`ui.go` 订阅；`status.go` 状态；`reasoning.go` 思考流。
- `qq-onebot/`：`adapter.go` 入口；`lifecycle.go` 启停；`message.go` 入站；`send.go` 发送；`transport.go` WebSocket；`capability_api.go` 原生 API；`forward.go` 合并转发。
- `qqofficial/`：`adapter.go` 入口；`lifecycle.go` 启停；`gateway.go` 网关；`message.go` 入站；`send.go` 发送；`client.go` HTTP；`token.go` 鉴权；`reference.go` 引用。
- `telegram/`：`adapter.go` 入口；`lifecycle.go` 启停；`message.go` 入站；`send.go` 发送；`stream.go` 流；`commands.go` 命令同步；`capability_api.go` 原生 API。
- `headless/`：`headless.go` 宿主占位；`notifications.go` 宿主通知写日志。
- `internal/storage/chat_history.go`：历史契约与编解码。

<!-- locator:session -->
## Session

路径相对 `internal/session/`：

- 根部：`operations.go` 会话操作；`background.go` 后台准备；`branch.go` 派生；`binding.go` 绑定；`origin.go` 归属；`workspace.go` 目录；`naming.go` 命名；`id.go` 发号。
- `binding/`：`manager.go` 绑定；`coordination.go` 准入；`signals.go` 事件。
- `control/`：`current.go` 当前会话；`promotion.go` 接管；`background.go` 后台；`query.go` 查询；`management.go` 管理；`expiration.go` 过期。
- `branch/`：`service.go` 材料准备；`fork.go` 分支；`copy.go` 副本；`compact.go` 压缩派生。
- `naming/`：`service.go` 任务；`naming.go` 流程；`title.go` 标题；`signals.go` 事件。
- `share/`：`session.go` 会话事实；`binding.go` 绑定；`origin.go` 归属；`id.go` 来源与前缀。
- `internal/storage/sqlite/session_repository.go`：会话存储。

<!-- locator:context -->
## 上下文、Prompt 与压缩

- `internal/contextmgr/`：`service.go` 上下文服务；`state.go` 窗口状态；`compact_contract.go` 压缩契约。
- `internal/agent/execution/`：`execution_compact.go` 压缩交接；`context_usage.go` 用量。
- `internal/agent/dialogue/`：`system_prompt.go`、`prompt.go` 公共 Prompt。
- `internal/agent/chat/`：`transcript.go` 历史与 Prompt；`compact.go`、`compressor.go` 摘要。
- `internal/agent/responses/`：`compact.go` 原生压缩；`branch.go` 分支材料。

<!-- locator:llm -->
## 模型服务与 LLM 客户端

- `internal/modelmgr/`：`manager.go` 总管；`client.go` 客户端装配；`providers.go` 厂商快照；`selection.go` 模型选择；`context_window.go` 窗口。
- `internal/llm/`：公共模型、消息、工具与用量契约。
- `internal/llm/httpclient/`：HTTP、SSE、重试与超时。
- `internal/llm/chatcompletions/`：Chat 协议。
- `internal/llm/responses/`：`client.go` 请求；`tools.go` 工具；`compact.go` 压缩；`stream.go` 流；`error.go` 错误；`response.go` 响应解析。

<!-- locator:storage -->
## Storage 与 SQLite

- `internal/storage/`：`storage.go` 领域契约；`session_metadata.go` 元数据；`id.go` 发号；`dialogue.go` 对话材料。
- `internal/storage/sqlite/`：`store.go` 连接；`db_init.go` 建库；`migrations.go` 迁移；`id.go` 序号。
- `internal/storage/sqlite/`：`session_repository.go` 会话；`dialogue_repository.go` 对话事务；`tool_call_repository.go` 工具统计；`chat_history_repository.go` 平台历史。
- `internal/storage/sqlite/`：`native_snapshot.go` 原生快照；`native_material.go` 窗口材料；`media_history.go` 历史媒体；`media_lifecycle.go` 回收。

<!-- locator:elnis -->
## Elnis / Elvena / Elwisp

- `internal/elvena/`：`types.go` 公共协议；`origin.go` 来源。
- `internal/background/elnis/`：`http.go` HTTP；`auth.go` 鉴权；`prepare.go` 输入；`targets.go` 目标；`types.go` 内部事件。
- `internal/background/elnis/`：`lifecycle.go` 启停；`service.go` 分发与恢复；`direct.go` 直接调用；`llm.go` 模型流程；`store.go` 状态。
- `internal/background/elnis/`：`repository.go` 存储契约；`outbox.go` 报告投递；`media.go` 媒体。
- `internal/storage/sqlite/elnis_event_repository.go`：事件与报告事务。
- [架构](architecture.md#elnis--elvena--elwisp)、[功能](../docs/elnis.md)、[配置与协议](../docs/elnis-usage.md)。

<!-- locator:media -->
## 媒体中心

- `internal/media/`：`api.go` 入口；`types.go` 类型；`reference.go` 持有／引用；`signals.go` 使用结束信号。
- `internal/media/object/`：`manager.go` 对象；`import.go` 导入；`read.go` 读取；`image.go` 图片；`reference.go` 引用；`cleanup.go` 回收；`history.go` 历史；`remote.go` 远端副本。
- `internal/media/transfer/`：`manager.go` 交付；`usage.go` 使用记录；`request.go` 模型交付；`output.go` 发送准备；`platform.go` 平台获取。
- `internal/media/backend/`：`local.go` 本地；`s3.go` S3；`remote.go` 后端构造。
- `internal/media/share/`：`types.go` 内部类型；`contracts.go` 组件契约。
- `internal/storage/sqlite/media_repository.go`、`internal/storage/sqlite/media_lifecycle.go`：媒体存储、回收。

<!-- locator:cron -->
## Cron 与维护

- `internal/background/cron/`：`manager.go` 调度；`service.go` 运行；`execution.go` 执行；`refresh.go` 变更刷新；`lifecycle.go` 启停。
- `internal/background/cron/`：`repository.go` 存储契约；`model.go` 任务；`models.go` 模型；`delivery.go` 投递；`recovery.go` 恢复。
- `internal/background/execution/runner.go`：后台输入与 Agent 转接。
- `internal/background/share/`：`result.go` 报告解析；`report.go` 附件与回执；`takeover.go` 接管契约。
- `internal/background/maintenance/maintenance.go`、`internal/background/cron/maintenance.go`：维护任务、Cron 适配。
- `internal/tool/builtin/cron.go`、`internal/storage/sqlite/cron_job_repository.go`：任务工具、任务存储。

<!-- locator:docs -->
## 文档

- `AGENTS.md`：操作与维护规则；`devdocs/architecture.md`：架构；`devdocs/tasks.md`：候选及暂缓任务。
- `docs/`：中文用户文档；`CHANGELOG.md`：中文变更记录。
- `scripts/translate_docs.py`、`.github/workflows/docs-translate.yml`：翻译脚本、自动化入口。
- `docs.en/`、`README.md`、`CHANGELOG.en.md`：自动翻译产物；仅 README 的 `manual-header` 页首区域分别手动维护。
