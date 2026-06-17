# ElBot

中文 | [English](README.md)

ElBot 是一个使用 Go 编写的轻量级 Agent/Chatbot 框架，目标是在保留可扩展性的同时，尽量降低运行成本、上下文成本和维护复杂度。
支持普通聊天、工具调用、Hook 扩展、长期任务调度、持久化会话与上下文压缩，适合个人助理、平台机器人和可编排自动化助手等场景。

## 特色

### 0. 轻量高效的 Go 实现

ElBot 当前本地启动耗时 <10ms（N5105，sata固态），常驻内存约 30MB。

### 1. 极致节省 Token 的工具发现机制

研究表明，许多普通用户仍主要将 LLM 类产品用作更高级的搜索引擎、写作助手和倾听对象，频繁工具调用并不是所有对话的常态。
参考：Chatterji et al., *How People Use ChatGPT*, NBER, 2025；Yan et al., *ShareChat: A Dataset of Chatbot Conversations in the Wild*, arXiv:2512.17843, 2025。

ElBot 不会在每轮对话中默认注入所有工具的完整 schema，而是仅暴露 `discover_tool` 和当前可用工具名称。模型需要使用工具时，先按需发现工具详情，再由 Agent 注入对应 schema。极大程度减少无效上下文开销。

个人日常使用，一次请求token消耗：

work模式：<1000

chat模式：<500

缓存命中： >90%

### 2. Chat / Work 双模式

ElBot 区分 chat 模式与 work 模式。chat 模式完全移除工具，更适合日常闲聊、陪伴、轻量问答和低成本对话；work 模式则启用工具发现与工具调用能力，用于复杂工作。
两种模式可以独立配置模型，让低成本模型承担闲聊，让强模型专注处理复杂任务。

### 3. 可扩展的 Hook 系统

ElBot 内置 Hook Layer，可在 Agent 输入、LLM 请求、LLM 响应、平台发送、平台连接等关键事件点插入扩展逻辑。
Hook 可以修改消息、追加输出意图、调用低风险工具或注入常驻记忆。内置规则 Hook、表情 Hook 和常驻记忆 Hook，也支持后续扩展独立插件。

### 4. 完善的日志与审计系统

ElBot 区分普通运行日志与审计日志，支持结构化字段记录、日志查询、审计查询、请求状态查看和运行期调试。普通日志用于定位运行问题，审计日志用于追踪工具调用等关键行为。

### 5. 常驻记忆与长期记忆分层

ElBot 分为常驻记忆和长期记忆。常驻记忆只保存短小、稳定、真正需要每轮注入的信息，减少 token 消耗；更长、更复杂的记忆不强制自动注入，而是由 LLM 在需要时通过 `long_memory` 工具主动发现和查询。

长期记忆使用人类可读的 Markdown 文件作为源数据，同时用 SQLite FTS 作为可重建的搜索索引。兼顾透明性和检索效率。

相比全自动 RAG 或图检索长期记忆，ElBot 的记忆设计更显式、更可控。

### 6. 普通 Cron 与 LLM Cron

ElBot 内置 Cron Runtime 和 LLM 可编排 Cron 服务。普通 Cron 可以按计划直接发送固定内容；LLM Cron 则允许用任务描述驱动模型执行。

### 7. ELyph：面向 LLM 协作的任务表示

ElBot 引入 ELyph Task Notation（任务表示法），用于描述 LLM Cron 与原生 skill。ELyph 的目标是减少自然语言任务描述中的歧义，用更短、更稳定的结构表达输入、输出、步骤、条件和约束。相比随意 Markdown，ELyph 更适合 LLM 之间复用和传递任务，也便于 lint、审计和后续工具化处理。

### 8. Elnis / Elwisp / Elvena：让 ElBot 掌控一切

传统 Agent 通常只会等待用户输入；Cron 只能响应时间。Elnis 让 ElBot 多了一种触发方式：外部事件。

Elnis 是 ElBot 的监听枢纽；Elwisp 是分布在各处的监听器，负责观世界。

服务器变化、RSS 更新、Webhook 告警、日志变化、游戏事件、本地脚本甚至其他计算机设备，都可以由 Elwisp 转成 Elvena 事件送入 Elnis。ElBot 再统一决定是否记录、调用 LLM 分析或执行后台任务，最终通知用户。

### 9. 可由 LLM 创建的 EL Skill

ElBot 内置 `create_el_skill` 元工具，允许 LLM 将可复用经验沉淀为 EL Skill。

### 10. 兼容互联网 Python Skill

除了原生 El Skill，ElBot 也兼容常见的 Python 外置 skill 结构。自动扫描 Python skill 的 `SKILL.md` 或 `SKILL.elyph`，读取名称、描述、适用场景和风险等级，并通过隐藏包装工具执行。

### 11. 多平台与富输出抽象

ElBot 抽象了平台层与输出层，目前支持 CLI 、QQ OneBot 和 QQ Official，并预留扩展其他平台的空间。

### 12. 会话、Fork 与上下文压缩

ElBot 内置持久化 Session 服务，支持会话恢复、归档、置顶、fork、删除、分页查看和平台隔离。

### 13. 安全策略与风险确认

ElBot 的工具系统内置风险等级、权限判断和高风险确认流程。普通用户只能发现和调用低风险范围内的工具，超级管理员调用高风险工具时也需要确认。

## 使用方法

开发期可以直接从源码启动：

```bash
go run ./cmd/elbot --config config/app.toml
```

常用启动方式：

```bash
elbot              # 自动模式：Linux 检测到 service 时进入本地 CLI-only，否则完整前台启动
elbot run          # 完整前台：CLI + 已启用平台 + Cron
elbot cli          # 本地 CLI-only：只启动 CLI，不启动平台和 Cron
elbot service run  # Linux/headless 服务模式：不启动 CLI，启动已启用平台和 Cron
```

Shell 补全可通过 `elbot completion <shell>` 生成，支持 `bash`、`zsh`、`fish`、`nushell`、`powershell` 和 `auto`。

最小使用流程：

1. 在 `config/providers.toml` 配置 OpenAI-compatible Provider。
2. 通过系统环境变量或配置目录 `.env` 设置 `api_key_env` 对应的 API Key。
3. 在 `config/state.toml` 选择默认 `chat` / `work` 模式和模型。
4. 启动后输入 `/help` 查看命令，或直接开始对话。

详细说明见：

- [快速开始](docs/getting-started.md)
- [配置说明](docs/configuration.md)
- [命令速查](docs/commands.md)
- [核心概念](docs/concepts.md)
- [Elnis 监听枢纽](docs/elnis.md)
- [Elnis 配置与使用](docs/elnis-usage.md)

开发计划和任务拆分已移到 [devdocs](devdocs/)。

## 开发状态

ElBot 仍在快速开发中，接口、配置和内部实现可能继续调整。当前更适合作为个人 Agent/机器人框架探索使用。

文档维护策略：README 只保留项目入口和最小启动路径；用户文档放在 `docs/`，开发计划和内部资料放在 `devdocs/`。新增用户可见功能时，优先更新对应专题文档。
