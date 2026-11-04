<!-- manual-header:start -->
<div align="center">

<img src="assets/elbot-logo.svg" alt="ElBot Logo" width="94" height="94" align="middle" />&emsp;<img src="assets/elbot-wordmark.svg" alt="ElBot" width="102" height="38" align="middle" />

让 ElBot 感知一切。

[![token节约](https://img.shields.io/badge/token%E8%8A%82%E7%BA%A6-365F8A?style=flat)](docs/concepts.md) [![事件驱动](https://img.shields.io/badge/%E4%BA%8B%E4%BB%B6%E9%A9%B1%E5%8A%A8-487DB5?style=flat)](docs/elnis.md) [![丰富扩展](https://img.shields.io/badge/%E4%B8%B0%E5%AF%8C%E6%89%A9%E5%B1%95-5B7190?style=flat)](docs/hooks.md)

中文 | [English](README.md)

</div>

## 简介

ElBot 是一个使用 Go 编写的轻量级 Agent/Chatbot 框架，目标是在保留可扩展性的同时，尽量降低运行成本、上下文成本和维护复杂度。
支持普通聊天、工具调用、事件感知、Hook 扩展、长期任务调度、持久化会话等，适合个人助理、平台机器人和可编排自动化助手等场景。

<!-- manual-header:end -->

## 特色

### 一、轻量高效

**极致轻量的 Go 实现**：

| 指标           | 数值                            |
| -------------- | ------------------------------- |
| 本地启动耗时   | ~10ms（N5105，SATA 固态上测试） |
| 常驻内存       | ~50MB                           |
| 二进制文件大小 | <30MB                           |

**极致节省 Token 的工具发现**： 研究表明，许多普通用户仍主要将 LLM 类产品用作更高级的搜索引擎、写作助手和倾听对象，频繁工具调用并不是所有对话的常态。
参考：Chatterji et al., _How People Use ChatGPT_, NBER, 2025；Yan et al., _ShareChat: A Dataset of Chatbot Conversations in the Wild_, arXiv:2512.17843, 2025。

ElBot 不会在每轮对话中默认注入所有工具的完整 schema，而是仅暴露 `discover_tool` 和当前可用工具名称。模型需要使用工具时，先按需发现工具详情，再由 Agent 注入对应 schema。极大程度减少无效上下文开销。

**Chat / Work 双模式**：两种模式可独立配置模型，让低成本模型承担闲聊，让强模型专注处理复杂任务。

**常驻记忆与长期记忆分层**： 常驻记忆只保存短小、稳定、真正需要每轮注入的信息，并在内部区分需确认修改的 core 与可整理的 normal；更长、更复杂的记忆由 LLM 按需通过 `long_memory` 查询。长期记忆使用 Markdown 源数据和 SQLite FTS，兼顾透明性和检索效率。

| 模式   | 工具               | 适用场景                                 | 第一次请求 Token 消耗      |
| ------ | ------------------ | ---------------------------------------- | -------------------------- |
| `chat` | 不注入             | 闲聊、陪伴、轻量问答、低成本对话         | <500 （后续缓存命中98%+）  |
| `work` | 启用工具发现与调用 | 搜索、文件、命令、Cron、Skill 等复杂任务 | <1000 （后续缓存命中95%+） |


> 注：使用`Responses`协议后，发现工具使用`additional_tools`不再破坏缓存，后续缓存命中会更高

### 二、强大可扩展

**可扩展的 Hook 系统**： ElBot 内置 Hook Layer，可在 Agent 输入、LLM 请求、LLM 响应、平台发送、平台连接等关键事件点插入扩展逻辑。Hook 可以修改消息、追加输出意图、调用脚本等。Hook 支持用**任意语言**编写插件。

**普通 Cron 与 LLM Cron**： ElBot 内置 Cron Runtime 和 LLM 可编排 Cron 服务。普通 Cron 按计划直接发送固定内容；LLM Cron 用任务描述驱动模型执行，适合需要分析、归纳或使用工具的定时任务。

**ELyph 任务表示法**： ELyph 用于描述 LLM Cron 与原生 Skill。目标是减少自然语言任务描述中的歧义，用更短、更稳定的结构表达输入、输出、步骤、条件和约束。相比随意 Markdown，ELyph 更适合 LLM 之间复用和传递任务，也便于 lint、审计和工具化处理。

**可由 LLM 创建的 EL Skill**： ElBot 内置 `create_el_skill` 元工具，允许 LLM 将可复用经验沉淀为 EL Skill。创建时自动校验 ELyph 语法，可选附带 Go 源码并编译。

**兼容外置 AgentSkill**： ElBot 兼容遵从 agentskills.io 风格的外置 AgentSkill。可通过配置将任意 AgentSKill 脚本作为工具使用。

### 三、Elnis 事件感知系统

传统 Agent 通常只会等待用户输入；Cron 只能响应时间。Elnis 让 ElBot 多了一种触发方式：外部事件。

Elnis 是 ElBot 的监听枢纽，Elwisp 是分布在各地的外部监听器，Elvena 是统一的 JSON over HTTP 事件协议。三者协作，让外部世界的任何信号如服务器告警、RSS 更新、Webhook、游戏事件、甚至外部计算机信息都能送入 Elnis，再交由 ElBot 处理并返回。

详细说明见 [Elnis 监听枢纽](docs/elnis.md)。

### 四、灵活部署与完善会话

**多平台与富输出抽象**： ElBot 抽象了平台层与输出层，目前支持 CLI、QQ OneBot、QQ Official 和 Telegram，并预留扩展其他平台的空间。

**CLI 客户端/服务端分离**： 支持任何电脑使用 ElBot 作为客户端连接 ElBot 服务端。 **前端自由定制**，可以随便制作自己喜欢的前端界面。以下截图展示不同的前端形态，除 TUI 外均为概念性 HTML mockup，不代表最终 UI。

<p align="center">
  <img src="https://raw.githubusercontent.com/Elfreese/elbot-showcase/main/frontend/assets/frontend_1.png" width="260" />
  <img src="https://raw.githubusercontent.com/Elfreese/elbot-showcase/main/frontend/assets/frontend_2.jpg" width="260" />
  <img src="https://raw.githubusercontent.com/Elfreese/elbot-showcase/main/frontend/assets/tui.png" width="260" />
</p>

更多截图见 [elbot-showcase/frontend](https://github.com/Elfreese/elbot-showcase/tree/main/frontend)。

**会话、Fork 与上下文压缩**： 内置持久化 Session 服务，支持会话恢复、归档、置顶、Fork、删除、分页查看、平台隔离、压缩等功能。

### 五、安全可靠

**安全策略与风险确认**： 工具系统内置风险等级、角色权限判断和高风险确认流程。普通用户只能发现和调用低风险工具；超级管理员调用高风险工具时也需逐项确认。

**轻量沙盒隔离**： 后台 Shell 执行受到 AST 级沙盒约束。后台任务拥有独立 sandbox 工作目录，降低误操作影响。

**完善的日志与审计**： 区分运行日志、Elwisp 日志与审计日志，支持结构化字段、日志查询、审计查询和运行期调试。

---

初次使用：配好 Provider 和 API Key 即可开始对话。详细说明见：

- [快速开始](docs/getting-started.md)
- [配置说明](docs/configuration.md)
- [命令速查](docs/commands.md)
- [核心概念](docs/concepts.md)
- [Elnis 监听枢纽](docs/elnis.md)
- [Elnis 配置与使用](docs/elnis-usage.md)
- [前端 API](docs/frontend-api.md)

开发计划和任务拆分： [devdocs](devdocs/)。

## 开发状态

ElBot 仍在快速开发中，接口、配置和内部实现可能继续调整。当前更适合作为个人 Agent/机器人框架探索使用。
