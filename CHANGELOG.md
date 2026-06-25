# Changelog

All notable changes to ElBot will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Elvena v3 动作通道：Elnis 支持 `calls`，首批支持 raw 平台 API 以及 `message.recall`、`member.mute`、`chat.leave` capability，未支持的可以直接调用消息平台api；Hook rules 可通过 `exec` action 执行脚本，并用 `stdout=elvena` 经内部 Elvena Bus 触发 Elnis direct/LLM/calls；direct calls-only 请求不会额外发送消息。
- `edit_file` 的 `*_match` 操作新增 `match_mode` 与 `index` 参数：`match_mode=line` 时按单行前缀匹配整行（容忍行首缩进，规避换行符匹配出错），`content`（默认）保持精确子串语义；多处匹配时可通过 `index` 选择第几处，未传 `index` 报错并列出所有匹配位置。
- Hook rules 新增角色分区与平铺控制字段：`roles`、`actor_roles`、`group_roles`、`consume`、`stop_propagation`；平台消息 Hook 输出现在会发送，`consume=true` 可阻止后续命令/LLM 处理。
- Hook rules `send` action 新增 `segments` 列表，支持多类型多段输出（text/image/file/emoticon，含 url/path/base64），格式与 Elvena segment 统一。
- Hook rules `exec` action 新增 `outputs` stdout 模式，脚本 stdout 解析为 JSON 并提取 `outputs` 数组和可选 `text`；设 `field` 时 `text` 覆写对应字段，不设时不修改原文。
- 平台入站上下文新增统一群身份 `owner/admin/member/unknown`，QQ OneBot 和 Telegram 会映射群主/管理员/普通成员。
- Hook 平台上下文现在填充当前平台消息 ID `platform.message_id` 与引用/回复目标消息 ID `platform.reply_to_message_id`，便于规则 Hook 处理引用消息，例如撤回被引用消息。

### Changed

- Provider 配置重构：删除未使用的 `[global_default]`，删除 `[model_metadata.context_windows]` 全局模型窗口表；模型级 `context_window` 和 `extra_payload` 统一收到 `[providers.<name>.model_configs."<model>"]` 下，按 `provider/model` 查找，避免跨 provider 同名模型冲突。
- Provider 新增 `proxy` 字段，支持 HTTP/SOCKS5 代理。
- 表情 Hook 从内嵌插件改为规则 Hook 示例，不再内置 emoticon 插件和 `emoticon.toml` 资产。
- LLM 建连/HTTP 可重试失败时通过 Notice 显示当前重试次数。
- `finalize_el_skill` 工具风险等级由 high 降为 medium。

### Fixed
- 修复 Elnis/Hook exec 触发 Elvena `calls` 时未注入平台 API caller，导致 `platform api callers are not configured` 的问题。
- 修复 OpenAI-compatible 流式响应中途断开但缺失 `[DONE]` 时被当作正常结束的问题；现在会明确通知 LLM 响应中断。
- 修复 Hook exec stdin JSON 使用 Go 默认大写字段名导致外部脚本无法用小写 key 读取 event 字段的问题；Hook event 及相关 payload 结构体统一加 JSON tag。


## [v0.1.0-alpha] - 2026-06-24

ElBot 的首个预发布版本。轻量 Agent/Chatbot 框架，目标是个人助手、平台机器人与可编排的自动化助手。

### Added

- **轻量内核**：Go 实现，本地启动 <10ms，常驻内存约 30MB。
- **Chat/Work 双模式**：chat 模式关闭工具，适合日常聊天与低成本对话；work 模式启用工具发现与调用，两模式可独立配置模型。
- **工具发现机制**：默认只暴露 `discover_tool` 与工具名，按需注入完整 schema，减少无效上下文开销。
- **Session 服务**：持久化会话，支持恢复、归档、置顶、Fork、删除、分页与平台隔离；长对话自动上下文压缩。
- **Hook Layer**：在 Agent 输入、LLM 请求/响应、平台发送等关键点插入扩展逻辑；内置规则 Hook 与常驻记忆 Hook。
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
