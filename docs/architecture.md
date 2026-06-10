# 架构设计

## 总体目标

ElBot 采用分层架构。Agent Core 不直接依赖具体平台、模型供应商或存储实现，各外部能力通过接口接入。

核心目标：

- 平台可替换。
- 模型可替换。
- 工具可扩展。
- 存储可迁移。
- 权限与安全策略统一处理。
- 默认运行目标覆盖 Windows 与 Linux。

跨平台约定：

- MVP 阶段优先选择不依赖 CGO 的组件，降低 Windows 与 Linux 间的构建差异。

## 分层结构

### Application Layer

应用启动层，负责组装配置、日志、存储、LLM、平台适配器、Agent Core 和工具运行时。

典型目录：

```text
cmd/elbot/
internal/app/
```

### Platform Layer

平台适配层，负责接收外部消息并转换为统一内部消息格式。

职责：

- 接收消息。
- 发送消息。
- 解析引用、附件、@、用户与群信息。
- 将平台输入转换为 Agent 可处理的消息。
- 将平台特性映射为统一命令或内部请求。
- 维护平台消息 ID 与内部消息 ID 的映射。

MVP 可先实现 CLI Adapter。

典型目录：

```text
internal/platform/
internal/platform/cli/
internal/platform/qq/
```

### Agent Core

Agent 核心层，负责处理用户输入、统一命令、构建 Prompt、调用 LLM、调度工具、写入上下文。

职责：

- 统一 Slash 命令解析与分发。
- Session 上下文加载。
- 会话模式处理。
- Prompt 组装。
- LLM 调用。
- 工具调用循环。
- 中断与插入消息处理。
- 回复生成。

典型目录：

```text
internal/agent/
```

### LLM Layer

LLM 适配层，负责屏蔽不同供应商 API 差异。

职责：

- Chat Completions 流式请求。
- 模型配置解析。
- 工具调用格式转换。
- 错误解析。
- Token 使用量解析。
- 流式响应解析。

典型目录：

```text
internal/llm/
internal/llm/openai/
```

### Tool Runtime

工具运行时负责工具注册、发现、调用和结果处理。

职责：

- 工具注册。
- 工具元信息管理。
- `discover_tool` 实现。
- 工具参数校验。
- 工具调用分发。
- 工具风险等级检查。
- 工具结果写回上下文。

典型目录：

```text
internal/tool/
internal/tool/builtin/
```

### Session & Storage Layer

会话与存储层负责持久化 Session、Message、Tool Call、Memory、Audit Log 等数据。

职责：

- Session 创建、恢复、手动归档。
- Session 取消归档。
- Session 置顶与取消置顶。
- Session 手动删除。
- Session 过期清理。
- Message 写入与上下文读取。
- Fork 关系维护。
- 话题标题存储。
- 工具调用记录。
- 审计日志记录。

Session 清理策略：

- 自动清理默认保留最近 30 天内更新过的普通 Session。
- 自动清理直接硬删除过期 Session，不先归档。
- 手动归档的 Session 以 `archived_at IS NOT NULL` 表示永久保存，不参与自动清理。
- 手动置顶的 Session 在列表中优先展示，也不参与自动清理。
- 删除 Session 时应级联删除其 messages 和平台消息映射。

典型目录：

```text
internal/session/
internal/storage/
internal/storage/sqlite/
```

### Context Management Layer

上下文管理层负责控制发送给 LLM 的上下文视图，避免直接把无限增长的完整消息历史塞入模型窗口。

职责：

- 加载当前 Session 的上下文视图。
- 估算上下文 token 数。
- 获取或推断模型 context window。
- 判断是否触发被动上下文压缩。
- 调用压缩模型生成 summary。
- 保存上下文压缩 checkpoint。
- 组装“最新 summary + summary 之后的新消息”的上下文视图。

上下文压缩不删除原始消息，也不创建新 Session。完整历史仍由 Storage 保存，压缩只影响后续请求 LLM 时的上下文加载策略。

典型目录：

```text
internal/session/
internal/prompt/
```

实现时避免使用 `internal/context` 作为包名，以免和 Go 标准库 `context` 混淆。

### Memory Layer

记忆层负责常驻记忆、长期记忆和记忆整理。

职责：

- 常驻记忆注入。
- 长期记忆检索与写入。
- 记忆重要度和作用域管理。
- 后续支持定时整理。（通过Cron插件实现，只留出接口即可）

MVP 可仅预留接口。

典型目录：

```text
internal/memory/
```

### Security Layer

安全层负责权限、风险等级、危险操作确认和审计。

职责：

- Actor 与 Role 管理。
- 命令权限检查。
- 工具权限检查。
- 危险操作确认。
- Session 级确认状态。
- 审计记录。

典型目录：

```text
internal/security/
```

### Hook Layer

Hook 层负责在关键流程前后执行扩展逻辑。

Hook 点包括：

- BeforeReceive
- AfterReceive
- BeforeLLM
- AfterLLM
- BeforeToolCall
- AfterToolCall
- BeforeSend
- AfterSend
- OnError
- OnPlatformConnected（连上消息平台时）

Hook 可提供空实现。同一个事件需要有优先级区分。

典型目录：

```text
internal/hook/
```

### Config & Observability Layer

配置与可观测性层负责配置加载、日志、指标和消耗统计。

默认主配置入口为 `config/app.toml`。Provider 配置位于 `config/providers.toml`，由主配置中的 `[config_files]` 引用。

配置文件职责：

- `app.toml`：配置文件引用、存储路径、运行时参数、上下文压缩策略等应用级配置。
- `providers.toml`：当前对话模型、模型供应商、模型默认参数、模型上下文窗口元信息等模型相关配置。

配置内相对路径基于主配置文件所在目录解析，避免从不同 CWD 启动时写入不同数据位置。

职责：

- 配置文件加载。
- 拆分配置文件路径解析。
- 模型配置管理。
- 上下文压缩配置管理。
- 工具配置管理。
- 日志输出。
- Token 与耗时统计。

典型目录：

```text
internal/config/
internal/logging/
internal/metrics/
```

## 推荐目录结构

```text
cmd/
  elbot/
    main.go
internal/
  app/
  agent/
  config/
  hook/
  llm/
    openai/
  logging/
  memory/
  metrics/
  platform/
    cli/
    qq/
  security/
  session/
  storage/
    sqlite/
  tool/
    builtin/
pkg/
docs/
```

## 核心调用流程

### 请求管理与当前 Turn

Agent Core 维护运行态 Request Manager 和 Turn Coordinator。

- Request Manager 只保存内存中的 active requests，负责登记 LLM、工具、压缩和子 Agent 等运行中请求，并提供按请求、按 Session 和全局取消能力。请求完成、取消或超时后应清理 active 状态。
- Turn Coordinator 记录当前 Session 的运行阶段、原始用户输入、运行中追加的 pending 输入和工具使用次数。LLM 处理中收到普通新消息时，当前 LLM 请求会被取消，并进入追加确认状态。
- 追加确认前，当前 turn 原始输入和补充输入都不作为稳定历史提交；确认后合并为一条用户消息，取消后全部丢弃。
- 工具调用循环中收到普通新消息时不打断工具，消息进入 pending 输入，并在当前循环下一次 LLM 请求前合并注入。
- `/stop` 默认取消当前 Session 下活动，`/stopall` 预留本进程全局取消入口，未来可接入超级管理员权限和跨平台控制。

### CLI TUI 地基

交互式 CLI 使用最小 Bubble Tea TUI，非 TTY 输入继续使用 scanner fallback。

- TUI 仅属于 CLI 平台适配层，Agent 不依赖 Bubble Tea。
- Agent 输出通过平台 `Send` 投递到 TUI 输出区。
- 用户输入提交后由 TUI 在 goroutine 中调用 `HandleMessage`，避免 LLM 流式输出阻塞输入。
- 第一版只提供输出区、输入区、基础退出能力、命令补全、输入历史和自动换行；Markdown 渲染后续可用 Glamour 接入，需处理流式输出中的未闭合代码块和局部重绘问题。

### 普通对话流程

1. Platform 接收用户消息。
2. Platform 将消息转换为内部 `InputMessage`。
3. Agent Core 判断输入是否为统一 Slash 命令。
4. 若为命令，则分发给对应核心服务并返回命令结果。
5. 若为普通消息，Agent Core 获取或创建当前 Session。
6. Storage 写入用户消息。
7. Context Loader 加载当前 Session 的上下文视图。
8. Context Manager 估算 token，并在达到配置比例时执行被动压缩。
9. 若发生压缩，则保存 summary checkpoint 并重新加载上下文视图。
10. Prompt Builder 组装消息。
11. LLM Adapter 发送流式请求。
12. Agent Core 接收模型流式回复并聚合最终助手消息。
13. Storage 写入助手消息。
14. Platform 发送回复。

### 工具调用流程

1. 工作模式下，Prompt 中注入 `discover_tool` 和工具名称列表。
2. LLM 调用 `discover_tool` 获取工具详情。
3. LLM 调用目标工具。
4. Tool Runtime 校验参数，并调用 Security Layer 执行权限、风险等级和危险确认检查。
5. Security Layer 作为安全硬约束；实现时可复用 Hook 机制精简代码，安全判定以 Security Layer 为准。
6. 若允许执行，则调用工具。
7. 工具结果写入上下文。
8. Agent Core 再次调用 LLM 生成最终回复。

### Fork 流程

1. 用户引用历史消息，或执行 `/fork <message_id>`。
2. Session Service 校验消息归属和权限。
3. 创建新 Session，记录 `parent_session_id` 和 `fork_from_message_id`。
4. 当前会话切换到新 Fork。
5. 后续消息写入该 Fork。
6. 上下文加载时合并 Fork 源之前的上下文视图与当前分支消息；若 Fork 来源范围内已有压缩摘要，则遵守上下文压缩规则使用“最新 summary + summary 之后的新消息”。

## MVP 取舍

### 必须稳定的模块

- LLM Adapter。
- Session Service。
- SQLite Storage。
- Tool Runtime。
- Security 基础边界。

### 可先简化的模块

- Platform 先使用 CLI。
- Memory 先预留接口。
- Hook 先预留接口。
- Metrics 先记录基础日志。
- QQ 适配后置。
