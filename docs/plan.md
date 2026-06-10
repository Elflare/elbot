# ElBot 计划

## 简介

ElBot 是一个使用 Go 语言实现的 LLM Agent，目标是提供可扩展、可控制、可持久化的对话与工具执行能力。

本文档只描述总体方向、设计原则、能力边界和阶段路线。具体实现任务见 `tasks.md`，接口草案见 `interfaces.md`。


## 设计原则

### Agent 核心与平台解耦

Agent Core 不依赖具体消息平台。QQ、CLI、本地 Web、本地 API 或其他 IM 平台均作为适配层接入。

### 模型供应商解耦

LLM Adapter 屏蔽不同模型供应商、接口格式、流式响应、工具调用格式和错误结构之间的差异。ElBot 当前统一使用流式对话链路

### 工具按需发现

默认不向模型注入完整工具定义。工具通过稳定名称和发现机制按需查询，降低上下文占用并减少无关工具干扰。

### 默认安全

涉及文件、命令、网络、权限、批量操作等高风险能力时，权限、风险等级和危险确认由 Security Layer 作为硬约束统一处理。Hook 可作为实现复用和扩展机制，安全判定以 Security Layer 为准。

### 历史可追溯

对话、工具调用、危险确认、权限拒绝、Fork、恢复、记忆修改和关键错误应具备可追溯能力。压缩只影响发送给 LLM 的上下文视图，不删除原始历史。

### 配置入口清晰

配置系统采用唯一主配置入口，并区分应用级配置与 Provider 配置，避免模型、存储、运行时和上下文配置分散到多套隐式规则中。

## 核心能力域

### Agent Core

Agent Core 负责统一处理用户输入、Slash 命令、Session 上下文、Prompt 组装、LLM 调用、工具调用循环、中断和回复生成。

### LLM Adapter

LLM Adapter 负责适配 OpenAI-compatible 等模型接口，统一流式响应、工具调用、模型列表、错误解析和 token usage 解析。

### Platform Adapter

Platform Adapter 负责接收平台消息、发送回复、解析平台特性，并将平台输入转换为 Agent Core 可处理的统一消息。

### Session 与上下文

ElBot 应支持对话持久化、恢复、Fork、上下文加载与上下文压缩。上下文压缩保存为 Session 内 checkpoint，后续请求使用“最新压缩摘要 + 摘要之后的新消息”的上下文视图。

### Tool Runtime

Tool Runtime 负责工具注册、发现、参数校验、调用分发和结果回传。工具权限、风险等级和危险确认由 Security Layer 统一约束。

### Security

Security Layer 负责 Actor、Role、Action、Resource、权限判断、风险等级、危险确认和审计入口。高风险能力必须经过统一安全链路。

### Memory 与 Soul

Memory 用于管理常驻记忆、长期记忆和后续记忆整理能力。Soul 用于描述 Agent 的身份、表达风格、行为边界和长期状态，并作为独立模块参与 Prompt 组装。

### Hook

Hook 用于在关键流程前后执行扩展逻辑。Hook 可以帮助精简代码，但不替代核心安全约束。

### Observability

ElBot 应记录关键运行事件、模型调用消耗、工具调用情况、错误和审计信息，便于排错、统计和安全追踪。

## 阶段路线

### MVP Core

优先完成最小可运行对话系统：配置、日志、CLI 或本地入口、流式 LLM 对话、模型查看与切换、SQLite Session/Message 持久化、会话恢复和基础状态查看。

### MVP Agent

在最小对话系统之上完成基础 Agent 能力：工作模式、聊天模式、工具发现、最小 Tool Runtime、内置安全工具和工具调用闭环。

### P1：会话与安全增强

继续完善上下文压缩、Fork、Session 清理、归档、置顶、请求管理、风险等级、危险确认、权限系统、审计和消耗统计。

### P2：平台与长期能力

接入 QQ 等消息平台，完善 Memory、Soul、Hook、平台引用消息映射和长期运行能力。

### P3：扩展生态

按需要扩展 MCP、工具组、子 Agent 和更复杂的多 Agent 协作能力。

## 非目标

- Agent Core 保持平台无关，具体消息平台通过 Platform Adapter 接入。
- 配置系统保持唯一主入口和明确引用关系。
- 安全判定以 Security Layer 为准，Hook 用于扩展与复用流程。
- MVP 优先保证可运行、可验证和核心链路稳定。

## 待决策问题

### 子 Agent 系统

- 是否支持子 Agent 系统。
- 子 Agent 是否拥有独立模型。
- 子 Agent 是否拥有独立 Prompt。
- 子 Agent 是否拥有独立工具组。
- 子 Agent 是否共享主 Agent 记忆。
- 主 Agent 如何委派任务。
- 子 Agent 结果如何回传与汇总。

### 请求停止粒度

- `/stop` 是否始终停止当前 Session 下全部 LLM、工具和子 Agent 活动。
- 未来是否支持只停止某一个子 Agent 或子任务。

### 工具组

- 工具组是否作为必选能力。
- 工具组主要用于组织展示、权限控制、discover_tool 范围缩小，还是子 Agent 工具隔离。
### 权限角色

- 当前阶段按个人使用场景实现，默认只有超级管理员。
- 后续是否引入普通用户、Session 所有者、管理员等细分角色。
- 后续角色之间如何划分命令、工具、记忆、配置和危险确认权限。

