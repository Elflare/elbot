<!-- manual-header:start -->
<div align="center">

<img src="assets/elbot-logo.svg" alt="ElBot Logo" width="94" height="94" align="middle" />&emsp;<img src="assets/elbot-wordmark.svg" alt="ElBot" width="102" height="38" align="middle" />

让 ElBot 感知一切。

[![token节约](https://img.shields.io/badge/token%E8%8A%82%E7%BA%A6-365F8A?style=flat)](docs/concepts.md) [![事件驱动](https://img.shields.io/badge/%E4%BA%8B%E4%BB%B6%E9%A9%B1%E5%8A%A8-487DB5?style=flat)](docs/elnis.md) [![丰富扩展](https://img.shields.io/badge/%E4%B8%B0%E5%AF%8C%E6%89%A9%E5%B1%95-5B7190?style=flat)](docs/hooks.md)

中文 | [English](README.md)

</div>

## 简介

ElBot 是一个使用 Go 编写的轻量级 Agent/Chatbot 框架，目标是在保留可扩展性的同时，降运行成本和上下文成本。
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

**按需发现工具，节省 Token**： 多数用户把 LLM 当作搜索引擎、写作助手和倾听对象，频繁工具调用并非对话常态[^1][^2]。

ElBot 不在每轮对话中注入所有工具的完整 schema，只暴露 `discover_tool` 和当前可用工具名称；模型需要时再发现详情，由 Agent 注入对应 schema。

**Chat / Work 双模式**：两种模式可独立配置模型，闲聊用低成本模型，复杂任务用强模型。

| 模式   | 工具               | 适用场景                                 | 第一次请求 Token 消耗      |
| ------ | ------------------ | ---------------------------------------- | -------------------------- |
| `chat` | 不注入             | 闲聊、陪伴、轻量问答、低成本对话         | <500 （后续缓存命中98%+）  |
| `work` | 启用工具发现与调用 | 搜索、文件、命令、Cron、Skill 等复杂任务 | <1000 （后续缓存命中95%+） |

> 注：使用`Responses`协议后，发现工具使用`additional_tools`不再破坏缓存，后续缓存中命中会更高。

**常驻记忆与长期记忆分层**： 常驻记忆只存短小稳定的每轮注入信息，内部区分需确认修改的 core 与可整理的 normal；更长记忆由 LLM 按需通过 `long_memory` 查询，底层用 Markdown 源数据和 SQLite FTS，兼顾透明与检索效率。

### 二、强大可扩展

**可扩展的 Hook 系统**： Hook Layer 覆盖 Agent 输入、LLM 请求、LLM 响应、平台发送、平台连接等事件点，可修改消息、追加输出意图、调用脚本。插件可用**任意语言**编写。

**普通 Cron 与 LLM Cron**： 普通 Cron 按计划发送固定内容；LLM Cron 用任务描述驱动模型执行，适合需要分析、归纳或使用工具的定时任务。

**ELyph 任务表示法**： 用结构化语法描述 LLM Cron 与原生 Skill 的输入、输出、步骤、条件和约束，比自然语言 Markdown 更短、更少歧义，便于 LLM 复用传递，也便于 lint、审计和工具化。

**可由 LLM 创建的 EL Skill**： 元工具 `create_el_skill` 让 LLM 把可复用经验沉淀为 EL Skill，创建时校验 ELyph 语法，可选附带 Go 源码并编译。

**兼容外置 AgentSkill**： 配置后可将符合 agentskills.io 风格的外置 AgentSkill 脚本作为工具使用。

### 三、Elnis 事件感知系统

**第三种触发方式**： 传统 Agent 只等待用户输入，Cron 只能响应时间；Elnis 让 ElBot 还能被外部事件驱动。

**监听枢纽**： Elnis 是监听枢纽，Elwisp 是分布各处的外部监听器。系统告警、RSS 更新、Webhook、游戏事件等外部信息经 Elwisp 送入 Elnis，交由 ElBot 处理并返回。详见 [Elnis 监听枢纽](docs/elnis.md)。

### 四、灵活部署与完善会话

**多平台与富输出抽象**： ElBot 抽象了平台层与输出层，目前支持 CLI、QQ OneBot、QQ Official 和 Telegram。平台层可扩展，其他平台按适配器接入。

**CLI 客户端/服务端分离**： ElBot 既可作为客户端也可作为服务端。 **前端自由定制**：可自行制作任意形态的界面，下图仅作示意（除 TUI 外为概念性 mockup，不代表最终 UI）。

<p align="center">
  <img src="https://raw.githubusercontent.com/Elfreese/elbot-showcase/refs/heads/main/assets/frontend/web-light.png" width="500" />
  <img src="https://raw.githubusercontent.com/Elfreese/elbot-showcase/refs/heads/main/assets/frontend/web-dark.png" width="500" />
  <br>
  <img src="https://raw.githubusercontent.com/Elfreese/elbot-showcase/refs/heads/main/assets/frontend/web-status.png" width="500" />
</p>

完整前端代码及更多截图见 [elbot-showcase/frontend](https://github.com/Elfreese/elbot-showcase/tree/main/frontend)。

**会话、Fork 与上下文压缩**： 内置持久化 Session 服务，支持会话恢复、归档、置顶、Fork、删除、分页查看、平台隔离、压缩等功能。

### 五、安全可靠

**安全策略与风险确认**： 工具按风险分级并结合角色权限判断。普通用户只能发现和调用允许范围内的工具，超管调用高风险工具同样需确认。

**轻量沙盒隔离**： 后台 Shell 执行受 AST 级沙盒约束，后台任务拥有独立 sandbox 工作目录。

**日志与审计**： 运行日志、Elwisp 日志与审计日志分离，支持结构化字段与筛选查询；权限拒绝、工具调用、危险确认和 Cron 投递均计入审计。

---

初次使用：配好 Provider 和 API Key 即可开始对话。详细说明见：

- [快速开始](docs/getting-started.md)
- [配置说明](docs/configuration.md)
- [命令速查](docs/commands.md)
- [核心概念](docs/concepts.md)
- [Elnis 监听枢纽](docs/elnis.md)
- [Elnis 配置与使用](docs/elnis-usage.md)
- [前端 API](docs/frontend-api.md)

## 开发状态

ElBot 仍在快速开发中，接口、配置和内部实现可能继续调整。当前更适合作为个人 Agent/机器人框架探索使用。

[^1]: Chatterji et al., [_How People Use ChatGPT_](https://www.nber.org/papers/w34255), NBER Working Paper 34255, 2025.
[^2]: Yan et al., [_ShareChat: A Dataset of Chatbot Conversations in the Wild_](https://arxiv.org/abs/2512.17843), arXiv:2512.17843, 2025.
