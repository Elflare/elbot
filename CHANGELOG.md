# Changelog

All notable changes to ElBot will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).


## Unreleased

### Added

- 重构AgentSkill：去掉py wrapper，直接使用shell执行对应sklll，同时支持在Agentkill根目录添加 `ELBOT_SKILL.toml` 注册为普通工具，方便 LLM 直接调用结构化参数。
- 新增隐藏元工具 `agent_skill`，用于读取或写入 AgentSkill 的 `ELBOT_SKILL.toml`，写入前校验配置并在成功后 reload。
- 首次运行会生成 `skills/agent/agent_skill_creator/SKILL.md`，用于说明如何把 AgentSkill 注册为普通工具。
- 首次运行会生成 `skills/go/write_elbot_hook/SKILL.elyph`，用于按需求编写 ElBot 规则 Hook。
- 新增 `/usage` 命令：从审计日志聚合 token 消耗，支持按模型/天/会话汇总，快捷参数 `-d` 天数、`-m` 模型、`-s` 会话。
- 新增 `workspace` 工具：设置当前前台 Session 的共享工作目录，路径类工具会基于该目录解析相对路径。首次切换到含 `AGENTS.md` 或 `AGENT.md` 的目录时，会自动附带说明文件内容；文件超过 64 KiB 时会提示缩短。
- 新增 `[platform_files]` 配置，统一控制平台入站文件最大保存大小和下载超时。
- QQ OneBot 支持自动保存私聊超级管理员入站文件；纯文件消息只回复保存路径或过大提示，不唤起 LLM，群文件不自动保存。
- `/requests` 命令现在展示每个 turn 的当前运行阶段（preparing/llm/tool/sending）和阶段耗时，可区分 LLM 慢还是平台发送卡住。
- 内联预载支持工具简写 `@t:<name-or-tag>` 和 Skill 简写 `@s:<name>`，并兼容中文全角冒号 `：`。
- Hook rules 新增 `require_wakeup` 配置；`platform.message.received` 规则可设置为 `false` 监听未 at、未命中唤起词、未回复机器人的普通群消息，同时不会自动唤起命令或 LLM。

### Changed

- `web_extract` 工具的代理参数从 `disable_proxy` 改为 `proxy`：不填时使用 `WEB_EXTRACT_PROXY` 或系统代理环境，填 `disabled` 禁用代理，填 URL 使用指定代理。
- `send_file` 工具改为使用 `source` 参数发送文件，支持本地路径、`file://` URI 和 HTTP(S) URL，并会按 MIME/扩展名自动将图片作为图片消息发送。
- AgentSkill 不再通过 `python_skill_run` 固定包装执行 Python 脚本；没有 `ELBOT_SKILL.toml` 时保持说明型 Skill，可按文档使用 shell 等通用工具；说明型 AgentSkill 不读取 `SKILL.md` 风险，工具化后以 `ELBOT_SKILL.toml` 的 `risk` 为准。
- Skill 扫描改为启动后延迟执行，并在 `discover_tool` 首次使用时兜底确保扫描，减少启动阻塞。
- Session 闲置过期改为 `[session.idle_expiration]` 四项配置，分别控制群聊/私聊下普通用户和超级管理员的当前 Session 过期时间；默认群聊所有用户过期，私聊超级管理员不过期。
- `shell` 工具移除 `path` 参数，命令默认在当前 workspace 下执行；后台任务仍限制在各自 sandbox 内。
- `read_file`、`edit_file`、`send_file` 的相对路径改为基于当前 workspace 解析；绝对路径仍可临时使用并返回 warning。
- `llm_usage` 审计事件从 debug 级别改为 info 级别，默认 `log_level=info` 即可记录 token 消耗数据。
- QQ OneBot、QQ 官方、Telegram 平台断线重连改为指数退避（3s 起，翻倍，封顶 10s）并日志降级：连续失败只在首次记 warn，恢复后记 info，不再每轮刷屏。
- 平台媒体输出支持在 `path` 中识别 `base64://`、`file://`、`http://`、`https://` 源；普通本地路径仍按平台默认方式处理。
- qq 官方收到图片现在直接使用url而不是base64

### Fixed

- 修复 `workspace` 工具设置目录时不支持 `~`、`~/path` 和 Windows `~\path` 主目录路径的问题。
- QQ OneBot 私聊文件段缺少 `url` 时会调用 `get_file`；若返回下载地址则保存到 ElBot，若只返回 OneBot 本地路径则直接提示该路径。
- 修复 Hook rules `exec` action 在 Windows 下固定依赖 `sh` 导致执行失败的问题；现在 `command` 会直接按程序和参数执行。
- QQ OneBot 入站 @ 消息现在会优先显示群名片，其次普通昵称，格式为 `[at 名字 qq:<id>]`，无法获取时才回退 QQ 号。
- 修复 Windows 下 `shell` 工具回退到 PowerShell 时中文输出可能乱码的问题。
- 修复 Windows 下无 bash 时 shell 命令的 bash AST 解析失败导致风险分类、沙盒校验、目录切换拦截和警告分析全部异常的问题；PowerShell 环境下跳过 AST 解析，风险分类直接返回高风险需用户确认。
- OneBot 发送图片失败时，不再出现可见 fallback，但仍会记录日志。



## [v0.3.0-alpha - 2026-07-01]

### Changed

- 同一轮多个工具调用的 `[tool]` 预览会合并为一条消息发送，减少平台刷屏。
- Elvena LLM 事件和 LLM Cron 支持 `session_mode=chat|work` 选择后台 Session 模式，默认仍为 `work`。
- `/detail` 高风险工具调用详情支持工具自定义纯文本展示；未自定义时仍会把 JSON 参数格式化成更易读的多行展示，字符串里的 `\n` 会显示为真实换行。
- `edit_file` 的高风险确认详情现在会按文件、模式和编辑步骤展示替换、新增、删除、匹配等操作。
- `edit_file` 不再向 LLM 暴露 `dry_run` 参数；系统会在用户确认前自动预检并生成 diff，预检失败不会进入确认或写入文件。
- `modify_el_skill` 现在复用 `edit_file` 的 `edits` 编辑说明与执行能力，并在确认前预检编辑、ELyph 语法和 no-op 修改，在高风险确认详情中展示预检 diff。
- 更新 `ELyph` 版本至 v3
- qq heartbeat ack 和 qqofficial gateway resumed 不再记录log
- read_el_skill 现在依赖modify_el_skill，方便执行可能的修改
- 现在不在启动elbot的时候校验ELyph语法，免得拖慢启动速度
- ELyph `**`/`~` 文本末尾冒号现在作为 warning 返回给 `create_el_skill`/`finalize_el_skill`，不再阻断创建或 finalize。
- `modify_el_skill` 修改 `SKILL.elyph` 后不再自动 reload；修改完成后需调用 `finalize_el_skill` 生效。
- 工具结果支持统一 `Warnings` 输出，用于提示 LLM 后续优先使用更合适的工具。
- `read_file`/`shell` 读取 EL Skill 文件时会提示使用 `read_el_skill`；`edit_file` 或 shell 直接修改 EL Skill 文件会在确认或执行前被拒绝，需改用 `modify_el_skill`。
- 常驻记忆和长期记忆源文件纳入通用 FileGuard 保护；读取会提示使用记忆工具，通用文件工具或 shell 直接写入会被拒绝。
- hook log日志不再重复记录

### Fixed

- `long_memory_write` 的 `update` 支持填字段更新 meta，并新增 `content_edits` 复用 `edit_file` 的编辑操作修改正文；确认前会自动预检并展示 diff。
- 修复 `edit_file` 使用 `create=true` 创建新文件时，目标父目录不存在会在确认写入阶段失败的问题。
- `response_timeout_seconds` 现在控制整轮用户请求总时长，默认 `0` 表示不限时；单次 LLM 流式请求只由首包和 idle 超时控制。

## [v0.2.0-alpha - 2026-06-27]

### Added

- Elvena v3 动作通道：Elnis 支持 `calls`，首批支持 raw 平台 API 以及 `message.recall`、`member.mute`、`chat.leave` capability，未支持的可以直接调用消息平台api；Hook rules 可通过 `exec` action 执行脚本，并用 `stdout=elvena` 经内部 Elvena Bus 触发 Elnis direct/LLM/calls；direct calls-only 请求不会额外发送消息。
- `edit_file` 的 `*_match` 操作新增 `match_mode` 与 `index` 参数：`match_mode=line` 时按单行前缀匹配整行（容忍行首缩进，规避换行符匹配出错），`content`（默认）保持精确子串语义；多处匹配时可通过 `index` 选择第几处，未传 `index` 报错并列出所有匹配位置。
- Hook rules 新增角色分区与平铺控制字段：`roles`、`actor_roles`、`group_roles`、`consume`、`stop_propagation`；平台消息 Hook 输出现在会发送，`consume=true` 可阻止后续命令/LLM 处理。
- Hook rules `send` action 新增 `segments` 列表，支持多类型多段输出（text/image/file/emoticon，含 url/path/base64），格式与 Elvena segment 统一。
- Hook rules `exec` action 新增 `outputs` stdout 模式，脚本 stdout 解析为 JSON 并提取 `outputs` 数组和可选 `text`；设 `field` 时 `text` 覆写对应字段，不设时不修改原文。
- 平台入站上下文新增统一群身份 `owner/admin/member/unknown`，QQ OneBot 和 Telegram 会映射群主/管理员/普通成员。
- Hook 平台上下文现在填充当前平台消息 ID `platform.message_id` 与引用/回复目标消息 ID `platform.reply_to_message_id`，便于规则 Hook 处理引用消息，例如撤回被引用消息。
- `/hooks` 命令：列出所有已注册 Hook、查看某个 Hook 详细配置、热重载全部 Hook（修改 `hooks.toml` 后无需重启即可生效）。

### Changed

- LLM 请求超时配置改为 `first_chunk_timeout_seconds`、`stream_idle_timeout_seconds`、`response_timeout_seconds`，旧 `timeout_seconds` 已移除；默认首个流式事件等待 180 秒、流式 idle 60 秒、整次响应不限总时长。
- Provider 配置重构：删除未使用的 `[global_default]`，删除 `[model_metadata.context_windows]` 全局模型窗口表；模型级 `context_window` 和 `extra_payload` 统一收到 `[providers.<name>.model_configs."<model>"]` 下，按 `provider/model` 查找，避免跨 provider 同名模型冲突。
- Provider 新增 `proxy` 字段，支持 HTTP/SOCKS5 代理。
- 表情 Hook 从内嵌插件改为规则 Hook 示例，不再内置 emoticon 插件和 `emoticon.toml` 资产。
- LLM 建连/HTTP 可重试失败时通过 Notice 显示当前重试次数。
- `finalize_el_skill` 工具风险等级由 high 降为 medium。

### Fixed

- 修复长工具链会被 Agent 内部 5 分钟默认请求超时静默停止的问题；整轮超时时现在会提示用户。
- 修复 QQ OneBot 发图片/表情/文件时 API 超时可能取消 WebSocket 写入并触发断线重连的问题；媒体发送失败时会尝试发送同目标文字提示。
- 修复 OpenAI-compatible 流式响应中途断开但缺失 `[DONE]` 时被当作正常结束的问题；现在会明确通知 LLM 响应中断。
- 修复 OpenAI-compatible 流式请求使用单一 HTTP 超时导致模型首字慢或长输出超过 60 秒时被错误中断的问题。


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
