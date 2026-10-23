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
- `internal/app/` 正处于骨架重建阶段；入口和环境准备已写入类型与函数的占位骨架，其余四个文件仅有包声明与职责注释，尚无可运行入口。13 个原生产文件和 12 个原测试文件均保存在 `*.go.bak` 中；职责设计见 [App 包设计](app-design.md)。
- `internal/app/entry.go`：启动参数、业务线总管集合，以及应用入口、业务线启动和运行等待的占位骨架。
- `internal/app/environment.go`：本地运行模式、配置路径定位和按配置取得服务运行占用的占位骨架。
- `internal/app/resources.go`：按已定位的路径加载配置，准备共享资源并明确所有权。
- `internal/app/wiring.go`：五条线总管的创建与公开能力接线。
- `internal/app/shutdown.go`：取消、等待退出、统一退出预算及资源清理顺序。
- `internal/app/cli_client.go`：独立 CLI 客户端运行路径及回退处理。
- `internal/agent/module/`：Dialogue 构造、Session／Request／Turn 所有权、线内接线、通知订阅与关闭。
- `internal/tool/module/`：共享工具目录、执行核心、预加载、Skill／Memory／文件服务。
- `internal/hook/module/`：Hook 核心、控制与插件进程。
- `internal/platform/module/`：平台适配器、发送与通知、API 能力、补全和运行生命周期。
- `internal/background/module/`：后台执行、维护任务、Cron、Elnis 恢复与 runtime。

<!-- locator:contextinfo -->
## 公共上下文事实

- `internal/contextinfo/`：Conversation、Actor、Execution、Model 定义与存取。
- `internal/platform/platform.go`、`internal/platform/cli/message.go`：平台消息投影与 CLI 来源。
- `internal/security/`：身份解析、权限与风险策略。
- `internal/agent/dialogue/execution_context.go`：模型事实与接管上下文刷新。
- `internal/request/manager.go`、`internal/turn/execution.go`、`internal/session/binding.go`：执行关联、快照与绑定。

<!-- locator:signal -->
## 信号与订阅

- `internal/signal/`：泛型信号、订阅快照、有界调度、结果、关闭及设施诊断。
- `internal/events/notification.go`、`platform.go`、`logging.go`：全局通知、平台连接与日志契约。
- `internal/agent/events/`：Agent 实例事实信号。
- `internal/agent/module/signals.go`、`agent_notifications.go`、`model_signals.go`：通知、重试、会话清理与接管订阅。
- `internal/agent/platform_signals.go`、`internal/cron/platform_signals.go`：平台连接事件消费与队列生命周期。
- `internal/session/naming_signals.go`、`naming_logging.go`、`naming_lifecycle.go`：命名信号、日志与退出。
- `internal/modelmgr/signals.go`、`logging.go`：模型重试信号与日志。
- `internal/agent/logging.go`、`internal/background/logging.go`：所属业务日志发布。

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

- `internal/agent/*_deps.go`：各消费者的依赖契约。
- `internal/agent/message.go`、`input.go`、`background.go`：消息分发、普通输入与后台准备。
- `internal/agent/dialogue/runner.go`、`preparation.go`：公共单轮、组件构造、输入准备与终态失败报告。
- `internal/agent/dialogue/materials.go`：公共消息投影、片段编解码、归档与请求媒体准备。
- `internal/agent/dialogue/model_call.go`、`system_prompt.go`：公共模型 Hook、调用观察与 Prompt 来源。
- `internal/agent/dialogue/message_store.go`、`commit.go`、`reply_commit.go`：消息、工具批次及最终回复提交。
- `internal/agent/chat/`：Chat Loop、请求、历史、摘要与 seed。
- `internal/agent/responses/loop.go`、`model_call.go`、`continuation.go`：Responses 循环、续链、pending 与恢复。
- `internal/agent/responses/native_call.go`：原生调用、流增量、参数归档与终态保存。
- `internal/agent/responses/context.go`、`input.go`、`persistence.go`：原生窗口重建、完整请求组装与提交。
- `internal/agent/responses/tool_inputs.go`、`input_format.go`：工具定义与输入格式。
- `internal/agent/segments.go`、`inbound_media.go`：有序图文与入站媒体。

<!-- locator:protocol-routing -->
## 协议路线

- `internal/agent/routes/`：按 APIType 登记 Loop／Compactor／Material。
- `internal/agent/module/module.go`：协议业务能力装配；App 原客户端工厂实现保存在 `internal/app/models.go.bak`，新资源准备职责见 `internal/app/resources.go`，当前尚未实现。
- `internal/agent/dialogue/loop.go`、`internal/contextmgr/compact_contract.go`、`internal/session/material.go`：公共循环、压缩和材料契约。
- `internal/llm/origin.go`、`internal/modelmgr/compatibility.go`：持久化归属与兼容判断。
- `internal/llm/api_type.go`、`internal/tool/availability.go`：模型 API 类型与工具可用性。

<!-- locator:commands -->
## 命令与补全

- `internal/command/`、`internal/command/builtin/register.go`：Router、命令契约与注册。
- `internal/command/builtin/`、`internal/agent/command_executor.go`：命令实现、权限、冲突、结果通知与 Continuation。
- `internal/command/builtin/log.go`、`usage.go`：日志查询、正文展示和用量统计。
- `internal/completion/`、`internal/agent/completion.go`：结构化补全与查询依赖。
- `internal/agent/file_rollback.go`、`internal/command/builtin/rollback.go`：文件撤销准入与命令。
- [命令文档](../docs/commands.md)。

<!-- locator:request-turn -->
## Request、Turn 与状态

- `internal/request/`、`internal/turn/`：请求树、取消与执行确认清理、stopping／attempt 收尾通知、阶段、pending、确认与 Execution。
- `internal/agent/execution.go`、`execution_run.go`：前后台执行、接管与跨轮收尾。
- `internal/agent/execution_admission.go`、`execution_input.go`、`execution_lifecycle.go`：准入、输入、追加确认与关闭。
- `internal/processenv/environment.go`、`process_unix.go`、`process_windows.go`：子进程环境和命令构造、平台进程组／树终止及实际退出判定；Skill、Shell、Hook、编译和文件搜索复用。
- `internal/agent/confirmation.go`、`risk_confirmation.go`：风险确认、来源日志与提示。
- `internal/runtime/`、`internal/agent/status.go`：运行状态、usage 唯一内存事实、恢复快照、执行归属校验与状态发布。

<!-- locator:tool -->
## Tool Runtime、工具状态与文件

- `internal/tool/`、`internal/tool/builtin/`：工具注册、schema、执行器与内置工具。
- `internal/toolrun/state.go`、`preload.go`、`background.go`：Session 工具状态、前后台共用的工具／Skill 预加载与后台白名单。
- `internal/agent/tool_directive.go`、`tool_cache.go`：输入指令与状态提交。
- `internal/tool/builtin/file_tools_ast.go`、`file_tools_directory.go`：AST 识别、目录候选与缓存、rg 枚举及流式搜索。
- `internal/tool/builtin/long_memory.go`：长期记忆文件、索引与同步。
- `internal/tool/builtin/web_extract.go`：正文缓存与同键在途请求。
- `internal/fileops/binding.go`、`commit.go`：操作绑定与提交校验。
- `internal/fileops/edit_service.go`、`rollback_service.go`：编辑／撤销准备、预览与提交。
- `internal/fileops/file.go`、`atomic_write.go`、`rollback.go`：结果计算、revision 校验写入、目标锁与撤销备份。
- `internal/workspace/`、`internal/session/workspace.go`、`internal/sandbox/`：工作目录、持久化与后台路径限制。
- `internal/tool/media_runtime.go`：单次工具调用的媒体持有与导出。

<!-- locator:tool-flow -->
## 工具调用链路

- `internal/agent/dialogue/tool_execution.go`：协议共用的 ToolRun 入口与提交器。
- `internal/agent/toolrun_adapter.go`：Hook、子请求、确认与状态桥接。
- `internal/toolrun/runner.go`：顺序准备和确认、并发执行、完成队列、逐项提交与开始／结束日志组装。
- `internal/agent/dialogue/commit.go`、`internal/agent/responses/persistence.go`：固定结果槽、逐项最终结果、批次封存与原生恢复快照。
- `internal/toolrun/toolrun.go`、`elwisp_exec.go`：调用解析、原生／远程路由与执行结果。
- `internal/tool/prepared.go`、`executor.go`：准备能力、调用快照及执行阶段校验。
- `internal/toolrun/logging.go`：完整工具日志发布。
- `internal/storage/tool_batch.go`、`internal/storage/sqlite/tool_batch.go`、`dialogue_repository.go`：稳定批次关联、原槽更新、结果／统计／媒体／发现状态原子提交；`tool_call_repository.go` 提供调用统计查询。

<!-- locator:skill -->
## Skill 与 ELyph

- `internal/elyph/`、`rules.go`：语言解析、lint、公共规则卡与展示判定。
- `internal/tool/detail.go`、`discover.go`：发现摘要、详情与语言需求。
- `internal/agent/dialogue/preparation.go`、`model_call.go`、`internal/toolrun/runner.go`：输入与工具结果的规则卡组装。
- `internal/storage/dialogue.go`、`internal/storage/sqlite/dialogue_repository.go`、`internal/contextmgr/state.go`：规则展示状态提交与压缩重置。
- `internal/tool/skill/`：扫描、catalog、创建、finalize 与 reload。
- `internal/tool/skill/command_tool.go`、`runner.go`：命令／Go 执行、进程环境与有界输出收集。
- `internal/tool/skill/agent_manifest.go`、`go_source.go`：AgentSkill manifest 与 Go 源码／编译。
- `internal/tool/skill/media.go`：媒体参数准备与 stdout 结果导入。
- `internal/config/assets.go`：内置 Skill 说明。

<!-- locator:hook -->
## Hook 与插件

- `internal/hook/event.go`、`manager.go`：事件与普通 Hook 流水线。
- `internal/hook/rules/`、`internal/hook/control/`：规则、一次性 exec、配置与管理。
- `internal/hook/runtime/`、`internal/hook/protocol/`：持久进程、RPC、waiting 与 hook.v2 帧。
- `internal/hook/call_policy.go`：工具调用只读策略。
- `internal/hook/media.go`、`internal/hook/output/`：媒体 API 与输出协议。
- `internal/hook/logging.go`：Hook 日志发布。
- `internal/agent/hooks.go`：Agent Hook 桥接与调用请求登记。
- [Hook 文档](../docs/hooks.md)。

<!-- locator:output -->
<!-- locator:notification -->
## 输出、投递与通知

- `internal/delivery/`、`internal/delivery/dispatch/`：输出意图、目标、回执、发送路由与媒体准备。
- `internal/notification/manager.go`、`rules/`：通知信号、目标、绑定校验、平台队列与提示规则。
- `internal/platform/module/notifications.go`：通知管理器及平台订阅装配。
- `internal/platform/{cli,qq-onebot,qqofficial,telegram}/notifications.go`：平台通知协议与实际发送。
- `internal/agent/output.go`、`turn_output.go`、`execution_output.go`：输出 Hook、前后台策略、接管刷新与流式回执合并。
- `internal/agent/dialogue/output.go`、`reply_commit.go`：正文／思考消费契约与最终回复提交。

<!-- locator:platform -->
## 平台适配

- `internal/platform/platform.go`、`internal/platform/builtin/`：平台契约与装配。
- `internal/platform/media.go`、`internal/platform/refcontext/`：历史 segments、来源清洗与引用恢复。
- `internal/platform/cli/`：本地／远程 CLI 与 TUI；`ui.go`、`status.go`、`reasoning.go`、`status_format.go` 分别负责展示目标、状态合并、思考流和状态文案。
- `internal/platform/module/ui.go`：CLI 状态订阅与思考能力装配。
- `internal/platform/qq-onebot/`：协议转换、forward 展开、长消息与回执。
- `internal/platform/qqofficial/`、`internal/platform/telegram/`、`internal/platform/headless/`：各平台 runtime。
- `internal/platform/qqofficial/reference.go`：QQ 引用索引解析、消息 ID 关联、事件原文转换及入站／发送回执索引登记。

<!-- locator:session -->
## Session

- `internal/session/service.go`、`types.go`：服务与领域接口。
- `internal/session/binding.go`、`coordination.go`、`signals.go`：current、准入锁与绑定变化。
- `internal/session/origin.go`、`promotion.go`、`background.go`：模型归属、前台接管与后台准备。
- `internal/session/fork.go`、`background_copy.go`、`material.go`：Fork、副本与源材料契约。
- `internal/session/compact.go`、`lifecycle.go`、`expiration.go`：压缩结果创建、生命周期与过期。
- `internal/session/naming.go`、`naming_lifecycle.go`、`title.go`：命名调度、退出与生成。
- `internal/background/takeover.go`：后台修正／投递前的接管检查。

<!-- locator:context -->
## 上下文、Prompt 与压缩

- `internal/contextmgr/`：历史、窗口查询、用量、seed 状态与压缩分派。
- `internal/agent/execution_compact.go`、`context_usage.go`：压缩生命周期、交接及用量。
- `internal/agent/chat/compact.go`、`compressor.go`：Chat 摘要。
- `internal/agent/responses/compact.go`、`branch.go`：原生压缩与分支材料。
- `internal/agent/dialogue/system_prompt.go`、`prompt.go`、`foreground_notice.go`：公共 Prompt 与接管提示。
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
- `internal/storage/sqlite/chat_history_repository.go`：聊天历史查询与保留时间清理，包含平台引用索引到真实消息 ID 的持久化映射。

<!-- locator:elnis -->
## Elnis / Elvena / Elwisp

- `internal/elvena/`、`internal/elnis/types.go`：公共协议与 Elnis 类型。
- `internal/elnis/http.go`、`service.go`：HTTP runtime、队列、去重分发与中断执行恢复。
- `internal/elnis/llm.go`、`direct.go`、`store.go`：模型／直接流程与事件状态。
- `internal/elnis/auth.go`、`prepare.go`、`targets.go`：鉴权、规范化与目标裁决。
- `internal/elnis/media.go`、`outbox.go`：媒体、持久化投递与恢复。
- `internal/storage/sqlite/elnis_event_repository.go`：事件仓储与引用事务。
- `internal/background/`：公共后台类型、JSON 结果与接管检查。
- [内部架构](elnis-elwisp.md)、[功能说明](../docs/elnis.md)、[配置与协议示例](../docs/elnis-usage.md)。

## 媒体中心

- `internal/media/manager.go`、`resolver.go`：导入、公开读取与入站物化。
- `internal/media/request.go`：完整模型请求的媒体持有、交付选择与签名 URL。
- `internal/media/reference.go`、`lifecycle.go`：引用、存活保护与物理清理。
- `internal/media/platform.go`、`history.go`：平台媒体、历史批量获取与跨库对账。
- `internal/storage/sqlite/media_repository.go`：元数据与签名缓存。
- `internal/tool/builtin/chat_history.go`、`get_media.go`、`view_image.go`：历史查询、媒体获取与 Responses 看图。
- `internal/delivery/dispatch/media.go`：输出媒体准备与回执缓存。

<!-- locator:cron -->
## Cron 与维护

- `internal/cron/`：任务 CRUD、调度、执行、报告与投递恢复。
- `internal/cron/service.go`、`manager.go`：任务运行、调度与整体日志。
- `internal/cron/models.go`、`delivery.go`、`recovery.go`：任务模型、逐项投递与连接恢复。
- `internal/maintenance/`：维护任务及来源结果。
- `internal/agent/background.go`、`background_tools.go`：公共后台执行与确认。
- `internal/tool/builtin/cron.go`、`internal/storage/sqlite/cron_job_repository.go`：工具入口与仓储。

<!-- locator:docs -->
## 文档

- `docs/`：中文用户文档；`devdocs/`：当前内部架构、代码入口与未完成任务。
- `CHANGELOG.md`：中文变更记录；`scripts/translate_docs.py`：翻译脚本。
- `docs.en/`、`README.md`、`CHANGELOG.en.md` 为英文镜像或自动翻译产物，不手动修改。
