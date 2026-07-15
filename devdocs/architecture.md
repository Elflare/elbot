# ElBot 架构说明

本文写系统如何流转、模块如何协作。只想找代码入口时，优先看 `devdocs/code-map.md`。

定位方法：

```bash
rg -n "locator:tool-flow" devdocs/architecture.md
```

<!-- locator:startup -->
## 启动与装配链路

简化链路：

1. `cmd/elbot/main.go` 创建根 context，并把命令行交给 launcher。
2. `internal/launcher/cli.go` 解析 `run`、`cli`、`service run`、补全和远程 CLI 参数。
3. 普通运行进入 `internal/app.Run`，远程 CLI 进入 `internal/app` 的 CLI client 入口。
4. app 层加载配置、日志、SQLite、LLM、Agent、Tool、Platform、Hook、Output、Cron 等依赖。
5. app 层按运行模式启动平台 runtime 和 Cron runtime。

设计边界：

- app 层负责装配，不承载业务逻辑。
- launcher 只做命令行解析，不直接初始化复杂依赖。
- 平台 adapter 只处理平台输入输出，不直接驱动 LLM。

<!-- locator:config -->
## 配置与运行数据

配置约定：

- 静态配置：`app.toml`。
- Provider 配置：同目录 `providers.toml`。
- 运行时模型状态：同目录 `state.toml`。
- 工具 tag 配置：同目录 `tool_tags.toml`。
- 用户可编辑资产：配置目录下的 `memories.toml`、`long_memory/`、`skills/`、`plugins/`。
- Hook 配置：入口为配置目录 `plugins/hooks.toml`；被引用插件使用 `plugins/<plugin-id>/hook.toml`，持久 Hook 在其中声明 `[plugin.runtime]`。
- Provider key 推荐用 `api_key_env`，读取优先级是系统环境变量高于配置目录 `.env`。

默认配置查找顺序：

1. `--config`
2. `ELBOT_CONFIG_FILE`
3. 平台配置目录

<!-- locator:agent-chat -->
## Agent 对话链路

普通输入简化链路：

1. 平台 adapter 收到消息并交给 Agent。
2. Agent core 判断 slash 命令、普通输入、工具 pending 输入和风险确认命令。
3. 普通对话加载 Session 上下文、构建 Prompt、选择模型并调用 LLM。
4. LLM 返回文本、reasoning 或 tool call。
5. 如果有 tool call，Agent 进入工具执行链路；工具结果写入 transcript 后继续 LLM 循环。
6. 生成最终 assistant 输出前，先跑输出预处理 Hook。
7. Output Manager 负责实际发送，成功后保存最终 assistant 消息。

关键约定：

- user 与已完成工具 transcript 会阶段性落库。
- 流式输出最终由对话主流程用最终文本 replace。
- 发送前会发布 `sending` phase，便于 `/requests` 区分 LLM 慢还是平台发送慢。
- 普通输入在工具阶段不会打断工具，会进入 pending 并在下一次 LLM 调用前注入。

<!-- locator:commands -->
## 命令链路

Slash 命令链路：

1. Agent core 识别命令前缀。
2. `internal/command/router.go` 负责解析命令名、alias、参数文本和分发。
3. `internal/agent/commands/` 的模块注册具体命令。
4. 命令通过 deps 访问 Session、模型、Hook、工具、日志、请求管理等能力。
5. 平台补全通过中央 completion 服务组合命令名、命令参数、风险确认、fork message ID 和 `@tool:` 候选。

约定：

- 新命令优先做成 `internal/agent/commands/` 模块。
- 命令详细帮助写在 `command.Info.Help`。
- 用户可见命令变化要同步 `docs/commands.md` 和 `CHANGELOG.md`。

<!-- locator:request-turn -->
## Request 与 Turn 状态

职责分层：

- Request manager 管 active request 树、父子关系、取消、超时和完成清理。
- Turn manager 管单个 Session 当前 turn 的阶段、原始输入、pending 追加、确认状态和工具计数。
- Runtime status 是状态快照，供 CLI 状态栏、`/requests` 和日志展示。

运行约定：

- 长耗时操作应登记 request，结束时清理。
- 能被用户取消的 LLM、工具、压缩、后台 Agent 请求应挂入 request 树。
- 当前阶段变化要更新 runtime status，避免 `/requests` 只能看到“卡住”。

<!-- locator:tool-flow -->
## 工具调用链路

简化链路：

1. LLM 返回 tool call。
2. Agent 进入工具执行阶段并记录工具调用请求。
3. ToolRun 做工具视图、命名解析、foreground-only 过滤、权限和风险确认。
4. Tool Runtime 执行具体工具，并按 Actor/Policy 做风险兜底校验。
5. 工具结果转换成 LLM tool message，写入 transcript。
6. 如果工具有输出意图，交给 Output Manager 发送，而不是工具直接发平台消息。

关键约定：

- 风险等级用于内部权限和确认，不暴露给 LLM。
- `Result.Content` 或 typed `Result.Segments` 回灌 LLM。
- `Result.Data` 只供内部结构化消费，不进入 tool message。
- 图片和文件必须显式返回 segment。
- Hook、Tool、插件都不要直接发平台消息，统一返回输出意图。

<!-- locator:tool -->
## Tool Runtime 与工具发现

Tool Runtime 负责注册、schema、权限、风险、确认详情、用户侧 tags 和工具结果。

工具视图由 ToolRun 提供：

- 管理 session 工具缓存。
- 合并 native/Elwisp 工具。
- 按前台/后台过滤 foreground-only 工具。
- 处理工具名解析、风险确认和批量工具预览。

`discover_tool` 的特殊约定：

- 查询普通工具时，返回“已发现工具”文本，并把完整 schema 放在结构化 Data 供 Agent 注入 top-level tools。
- 查询说明型 AgentSkill 会激活 `agent_skill` 元工具。
- 查询工具化 AgentSkill 会注入其 top-level schema。
- 查询 Go skill 会按需激活 `go_skill_run`。

<!-- locator:skill -->
## Skill 架构

Skill 分三类：

- AgentSkill：`skills/agent/<name>/SKILL.md`，可选 `ELBOT_SKILL.toml` 做文档可见性限制或工具化。
- 原生 EL Skill：使用 `SKILL.elyph` 描述任务和规则，可选 Go 源码。
- Go skill：`skills/go/<name>/SKILL.elyph`，可选编译产物，通过隐藏 wrapper 执行。

关键链路：

1. Skill scanner 扫描配置目录下 `skills/`。
2. Catalog 记录名称、详情格式、风险、根目录、binary 和工具化状态。
3. `discover_tool` 暴露 Skill 详情或激活对应 wrapper。
4. 原生 EL Skill 创建/修改后需要 finalize，执行 lint、gofmt、build 和 reload。

<!-- locator:hook -->
## Hook 链路

普通 Hook Manager 按事件点和优先级串行执行 Handler，`hook/control.Service` 作为 `/hooks` 的独立管理入口，组合普通 Manager、持久 Runtime 和配置 loader。

常见来源：

- 规则 Hook：读取 `plugins/hooks.toml`。
- exec action：按 `hook.v2` 一次性 Pipe 协议执行，默认在 `plugins/` 目录执行 shell 命令。
- 持久 Hook：在插件 `hook.toml` 的 `[plugin.runtime]` 声明；Hook runtime 管进程生命周期、双向 RPC、waiting 路由和进程内 SharedState。
- 常驻记忆 Hook：每 turn 注入当前 platform + actor 的常驻记忆和临时用户名。

约定：

- Hook 配置先加载到候选 Manager，并完成持久 Runtime 配置校验；候选构建或校验失败时保留当前活动 Hook。提交时先一次性替换 Runtime worker 索引，再原子替换普通 Hook handler 快照。
- 持久进程启动仍是异步生命周期，reload 提交后可短暂处于 `starting`，进程后续失败由既有状态和重启策略处理。
- Hook 可返回控制字段和输出意图。
- Hook 不直接发平台消息。
- 输出预处理 Hook 运行在 assistant 最终发送前。
- 命中 waiting 租约的消息在常规平台 Hook 后、命令和主 LLM 前交给持久 Hook；`/cancel` 只取消该路由执行，不停止进程。
- Hook 用户文档优先看 `docs/hooks.md`。

<!-- locator:output -->
## Output 与发送链路

输出层把 Agent、Hook、Tool、Elnis 等来源的输出意图统一发送到平台。

职责：

- 定义 text/image/file/at/reply/emoticon 等平台无关输出类型。
- 提供媒体源前缀、fallback 文本、delivery timing 元数据。
- 统一处理发送回执、流式发送和普通发送。

约定：

- 业务层返回输出意图，不直接调用平台 adapter。
- 平台 adapter 负责把平台无关输出转换成平台 API。
- 流式输出、notice、reasoning、runtime status 由 Agent turn 输出适配层区分前后台发送。

<!-- locator:platform -->
## 平台适配层

平台层负责输入归一化和输出落地。

输入侧：

- 解析 Actor、Scope、发送目标、群身份、引用、多模态消息段和平台 metadata。
- 按平台规则决定是否触发 Agent。

输出侧：

- 实现统一 `SendChat` / `SendNotice`。
- 返回可携带多条平台消息 ID 的 receipt。
- 支持平台能力差异下的 fallback。

常见平台：

- CLI 本地/TUI 与远程 CLI。
- QQ OneBot v11。
- QQ 官方机器人。
- Telegram。
- headless service 模式。

<!-- locator:session -->
## Session 生命周期

Session 服务管理：

- 当前 session。
- 创建、恢复、Fork。
- 分页列表、置顶、归档、删除、过期清理。
- 模式切换和手动重命名。
- 平台隔离。
- cron session 可见性和 CLI 全平台列表可见性。

约定：

- Agent 入口需要从平台上下文解析 Actor/Scope，缺失时走 fallback。
- Fork 上下文由 Session/Storage 支持，不在平台层拼接。
- 闲置过期策略按群聊/私聊和普通用户/超管选择 TTL。

<!-- locator:context -->
## 上下文管理

上下文管理负责：

- 加载历史消息和 Fork 上下文。
- 解析 context window。
- 按当前模型的 context window 动态判断压缩阈值。
- 格式化厂商 usage 状态。

约定：

- Prompt Builder 只生成单条 system prompt，并组合历史、工具 transcript、多模态 metadata 和摘要。
- 压缩以可取消 request 保护生命周期，仅总结有效对话与成功工具调用。成功后创建并切换到无 Parent/Fork 关系的新 Session，旧 Session 保持不变。
- 新 Session metadata 暂存一次性 compact seed；首条用户输入时，Prompt Builder 将“压缩结果 + 历史用户原话 + 当前输入”物化为单条 user message，成功持久化后消耗 seed。
- 模型选择在 turn 开始时快照；进行中的 `/model` 不改变当前 LLM/工具循环，下一轮按新模型重新解析窗口与阈值。
- System Prompt Manager 按优先级收集 Soul、工具名称、tag prompt 等片段。
- 最近 usage 会写入 Session metadata，恢复会话后可展示。

<!-- locator:storage -->
## Storage 与 SQLite

Storage 抽象定义领域模型和 repository interfaces。

SQLite 实现负责：

- migration。
- Session、Message、ContextSummary。
- 平台聊天历史。
- Cron job。
- Elnis event。
- Tool call record。

约定：

- 新持久化能力先扩展 storage interface，再落 SQLite repository。
- migration 需要可重复检测已应用版本。
- 查询条件要保留平台隔离、归档过滤、Fork 范围等业务约束。

<!-- locator:elnis -->
## Elnis / Elvena / Elwisp

Elnis 是监听枢纽，Elvena 是公共协议层，Elwisp 是外部事件/能力接入形态。

链路：

1. HTTP runtime 接收 `POST /elvena/v2/events`。
2. auth 校验 token、Elwisp 和工具授权。
3. prepare 校验 v2/v3 request，规范化 target/tool/calls，生成事件 key/hash。
4. service 去重后分发 record/direct/llm。
5. direct 发送 content/segments 或执行 raw/capability calls。
6. llm 后台任务按 session_mode 运行，并投递结果报告。

约定：

- 公共协议类型放在 `internal/elvena`，Elnis 复用别名。
- segment 下载和 URL/data URI 校验集中处理。
- 背景 LLM 要使用后台 actor、sandbox subdir 和对应 session_mode。

<!-- locator:cron -->
## Cron 与后台任务

中央 Cron Runtime 负责持久化 job 的调度、注册、upsert、禁用、删除、运行状态和执行日志。

约定：

- 同一 job 防并发。
- Runtime 未启动时 Stop 安全返回。
- 维护类任务集中注册在 maintenance 包。
- LLM cron 通过 Agent 后台 runner 执行，可预注入工具或 Skill。
- cron/Elnis 后台 shell 的非 critical 风险可自动确认，critical 直接返回提醒，不等待用户。
