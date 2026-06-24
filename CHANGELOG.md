# Changelog

All notable changes to ElBot will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed
- Provider 配置重构：删除未使用的 `[global_default]`，删除 `[model_metadata.context_windows]` 全局模型窗口表；模型级 `context_window` 和 `extra_payload` 统一收到 `[providers.<name>.model_configs."<model>"]` 下，按 `provider/model` 查找，避免跨 provider 同名模型冲突。
- Provider 新增 `proxy` 字段，支持 HTTP/SOCKS5 代理。

### Fixed
- 修复 OpenAI-compatible 流式响应中途断开但缺失 `[DONE]` 时被当作正常结束的问题；现在会明确通知 LLM 响应中断。

### Changed
- LLM 建连/HTTP 可重试失败时通过 Notice 显示当前重试次数。
- `finalize_el_skill` 工具风险等级由 high 降为 medium。
- `edit_file` 的 `*_match` 操作新增 `match_mode` 与 `index` 参数：`match_mode=line` 时按单行前缀匹配整行（容忍行首缩进，规避换行符匹配出错），`content`（默认）保持精确子串语义；多处匹配时可通过 `index` 选择第几处，未传 `index` 报错并列出所有匹配位置。

## [v0.1.0-alpha] - 2026-06-24

ElBot 的首个预发布版本。轻量 Agent/Chatbot 框架，目标是个人助手、平台机器人与可编排的自动化助手。

### Added

- **轻量内核**：Go 实现，本地启动 <10ms，常驻内存约 30MB。
- **Chat/Work 双模式**：chat 模式关闭工具，适合日常聊天与低成本对话；work 模式启用工具发现与调用，两模式可独立配置模型。
- **工具发现机制**：默认只暴露 `discover_tool` 与工具名，按需注入完整 schema，减少无效上下文开销。
- **Session 服务**：持久化会话，支持恢复、归档、置顶、Fork、删除、分页与平台隔离；长对话自动上下文压缩。
- **Hook Layer**：在 Agent 输入、LLM 请求/响应、平台发送等关键点插入扩展逻辑；内置规则 Hook、表情 Hook 与常驻记忆 Hook。
- **标准 Cron 与 LLM Cron**：标准 Cron 按计划直发固定内容；LLM Cron 用 ELyph 任务描述驱动模型执行，支持一次性与周期任务、missed 补跑与广播。
- **ELyph Task Notation**：结构化任务描述语言，用于 LLM Cron 与原生 Skill，减少自然语言歧义。
- **原生与外置 Skill**：`create_el_skill` 元工具支持 LLM 创建原生 EL Skill（纯 ELyph 或附带 Go 源码并编译）；兼容 agentskills.io 风格外置 AgentSkill（附带 Python 脚本）。
- **Elnis 监听枢纽**：接收 Elwisp 通过 Elvena HTTP 协议投递的外部事件，支持 record/direct/llm 三种模式与多目标投递。
- **多平台适配**：CLI（含 client/server 分离与远程连接）、QQ OneBot v11、QQ 官方机器人、Telegram Bot API。
- **安全策略**：工具风险分级、角色权限校验、高风险确认流程与后台 shell 轻量沙盒。
- **记忆系统**：常驻记忆（core/normal 分层）按平台与 actor 注入；长期记忆基于 Markdown 源数据与 SQLite FTS 检索。
- **日志与审计**：运行日志、审计日志与 Elnis 日志分离，支持结构化字段与按日期轮转。
- **SQLite 持久化**：Session、消息、上下文摘要、工具调用记录、Cron job、Elnis 事件统一存储。

### Known Limitations

- MCP 工具、子 Agent 与完整多模态（语音、视频、文件真实模型输入）尚未实现。
- 接口、配置与内部实现仍可能调整，更适合作为个人 Agent/bot 框架探索使用。
