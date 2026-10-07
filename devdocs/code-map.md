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

- `internal/signal/`：类型化信号、有界执行器、背压和关闭策略。
- `internal/agent/events/`、`internal/platform/signals.go`：Agent 事实与平台连接信号。
- `internal/app/signals.go`、`internal/app/agent_logging.go`、`internal/app/agent_notifications.go`：订阅、统一事件快照关联的日志与通知。
- `internal/app/agent_status.go`、`internal/app/model_signals.go`、`internal/app/naming.go`：状态展示、重试与命名消费者。

<!-- locator:config -->
## 配置、资产与日志

- `internal/config/`：读取、默认资产、模块声明、provider／state／tag 及只读检查。
- `internal/config/definition.go`、`internal/config/provider.go`：配置规则与客户端模式校验。
- `internal/doctor/`：配置诊断报告；`internal/command/builtin/doctor.go`：超管命令入口。
- `internal/logging/`：日志写入、轮转与读取。
- [配置文档](../docs/configuration.md)。

<!-- locator:agent-chat -->
## Agent 对话

- `internal/agent/agent.go`、`internal/agent/assembly.go`：薄入口与组件装配。
- `internal/agent/message.go`、`internal/agent/input.go`、`internal/agent/background.go`：消息分发、普通输入与后台准备。
- `internal/agent/dialogue/runner.go`、`internal/agent/dialogue/preparation.go`：公共单轮加载、准备与结果。
- `internal/agent/dialogue/model_call.go`、`internal/agent/dialogue/system_prompt.go`：共同模型 Hook 与 Prompt 来源。
- `internal/agent/chat/`：Chat Loop、请求、历史、摘要与 seed。
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
- `internal/llm/api_type.go`、`internal/tool/availability.go`：模型 API 类型与工具可用性；`contextinfo.Model.APIType` 承载当前主对话模型事实。

<!-- locator:commands -->
## 命令与补全

- `internal/command/`、`internal/command/builtin/register.go`：Router、命令契约与注册。
- `internal/command/builtin/`：内置命令；`internal/agent/command_runtime.go`：权限、冲突和 Continuation。
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
- `internal/toolrun/state.go`、`internal/toolrun/preload.go`、`internal/toolrun/background.go`：状态提交、预加载与后台白名单。
- `internal/agent/tool_directive.go`、`internal/agent/tool_cache.go`：输入指令和状态提交编排。
- `internal/fileops/`：共享文件操作、编辑、目标锁和撤销备份。
- `internal/workspace/`、`internal/session/workspace.go`、`internal/sandbox/`：工作目录、持久化与后台路径限制。
- `internal/tool/media_runtime.go`：工具／Skill 媒体准备与导出缓存。

<!-- locator:tool-flow -->
## 工具调用链路

- `internal/agent/dialogue/tool_execution.go`：路线共用的 ToolRun 入口。
- `internal/agent/toolrun_adapter.go`：Hook、子请求、确认、调用记录与状态提交桥接。
- `internal/toolrun/toolrun.go`、`internal/tool/executor.go`：实际调用编排与执行。
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

- `internal/hook/event.go`、`internal/hook/manager.go`：事件与普通 Hook 流水线。
- `internal/hook/rules/`、`internal/hook/control/`：规则、一次性 exec 与管理入口。
- `internal/hook/runtime/`、`internal/hook/protocol/`：持久进程、RPC、waiting 和 hook.v2 帧。
- `internal/hook/call_policy.go`：消费方工具调用只读策略。
- `internal/hook/media.go`、`internal/hook/output/`：媒体 API 与输出协议。
- `internal/agent/hooks.go`、`internal/processenv/`：Agent 桥接与进程环境。
- [Hook 文档](../docs/hooks.md)。

<!-- locator:output -->
<!-- locator:notification -->
## 输出、投递与通知

- `internal/delivery/`、`internal/delivery/dispatch/`：输出意图、Target／Receipt、平台路由与媒体准备。
- `internal/notification/`：通知意图、来源校验与规则。
- `internal/agent/output.go`、`internal/agent/turn_output.go`、`internal/agent/execution_output.go`：发送 Hook、前后台策略与接管输出。
- `internal/agent/dialogue/reply_commit.go`：最终回复落库、发送顺序与回执关联。

<!-- locator:platform -->
## 平台适配

- `internal/platform/platform.go`、`internal/platform/builtin/`：公共契约与装配。
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

<!-- locator:storage -->
## Storage 与 SQLite

- `internal/storage/storage.go`、`internal/storage/session_metadata.go`：领域模型、repository 契约及 metadata 保留。
- `internal/storage/sqlite/`、`internal/storage/sqlite/migrations.go`：存储实现与数据库迁移。
- `internal/storage/dialogue.go`、`internal/storage/sqlite/dialogue_repository.go`：ToolPair／原生提交事务与 checkpoint 比较。
- `internal/storage/sqlite/native_snapshot.go`、`internal/storage/sqlite/native_material.go`：不可变调用快照、seed 与新 Session 原子保存。
- `internal/storage/sqlite/media_history.go`、`internal/storage/sqlite/media_lifecycle.go`：历史媒体关联、媒体临时引用恢复与清理认领。

<!-- locator:elnis -->
## Elnis / Elvena / Elwisp

- `internal/elvena/`、`internal/elnis/types.go`：公共协议、来源与 Elnis 类型。
- `internal/elnis/http.go`、`internal/elnis/service.go`：HTTP runtime、队列、去重分发与中断执行恢复裁决。
- `internal/elnis/auth.go`、`internal/elnis/prepare.go`、`internal/elnis/targets.go`：鉴权、规范化与投递裁决。
- `internal/elnis/media.go`、`internal/elnis/outbox.go`：媒体、持久化投递与恢复。
- `internal/storage/sqlite/elnis_event_repository.go`、`internal/background/`：事件仓储、恢复状态与引用释放的原子执行，以及公共后台类型。
- [内部架构](elnis-elwisp.md)、[功能说明](../docs/elnis.md)、[配置与协议示例](../docs/elnis-usage.md)。

## 媒体中心

- `internal/media/`：导入、图片处理、请求／发送解析、引用与清理。
- `internal/media/platform.go`、`internal/media/history.go`：平台媒体、历史批量获取（缓存、去重与尝试预算）及跨库历史对账。
- `internal/tool/builtin/chat_history.go`、`internal/tool/builtin/get_media.go`：历史查询与显式媒体获取。
- `internal/tool/builtin/view_image.go`：Responses 看图工具，来源／图片序号选择、本地权限及多模态结果。
- `internal/delivery/dispatch/media.go`：输出媒体准备和回执缓存。

<!-- locator:cron -->
## Cron 与维护

- `internal/cron/`：任务 CRUD、调度、执行、报告与投递恢复。
- `internal/cron/models.go`、`internal/cron/delivery.go`、`internal/cron/recovery.go`：任务模型、逐项投递及连接恢复。
- `internal/maintenance/`：维护任务。
- `internal/agent/background.go`、`internal/agent/background_tools.go`：公共后台执行与确认。
- `internal/tool/builtin/cron.go`、`internal/storage/sqlite/cron_job_repository.go`：工具入口与仓储。

<!-- locator:docs -->
## 文档

- `docs/`：中文用户文档；`devdocs/`：当前内部架构、定位入口和任务清单。
- `CHANGELOG.md`：中文变更记录；`scripts/translate_docs.py`：翻译脚本。
- `docs.en/`、`README.md`、`CHANGELOG.en.md` 为英文镜像或自动翻译产物，不手动修改。
