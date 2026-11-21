# Changelog

## Unreleased

### Added

- QQ 官方适配器保存发送者头像。
- 前台 `shell` 和 `tool_weave` 现在支持异步。
- 新增前台工具 `ask_user`，默认异步。
- 新增 `tool_weave`，可执行Python代码并调用elbot工具。纯 AgentSkill 可用 `weavable = true` 加入编排。
- AgentSkill TOML 支持 `depends_on`，Skill 标签参与 `@t:` 工具组补全和预载。
- AgentSkill TOML 新增 `use_workspace`，启用后命令和媒体路径以当前 workspace 为基准。
- AgentSkill TOML 工具支持通过 `positional` 数组按指定顺序传入位置参数，可与 `[args]` flag 参数组合使用。
- CLI 远程服务新增静态前端托管：自动识别主配置旁的 `frontend/index.html`。
- 新增大量 CLI 远程协议
- 新增配置文件锁，防止多个实例使用同一份配置文件。
- web 搜索增加 `markdownnew`

### Changed

- Responses 默认发送 `store:false` 和完整原生窗口；显式设置 `store:true` 才启用增量续链。
- 优化 QQ 官方适配器md发送逻辑
- `/usage` 改为只汇总区间内已有的调用，空结果显示 `nil`，取消记录不足提示。用量 API 移除 `available_calls` 和 `range_truncated`。
- `/sessions`、`/resume` 列表和 `/archives` 超管可用 `-a/--all` 查看全部平台。
- 文件预检改用 `go-udiff`，文件前后均不超过 8 MiB 时提供完整 diff，不再限制行数乘积或 diff 输出长度。
- `/messages` 改为原始对话的有界分页：第 1 页最新、页内旧到新，结构化数据保留完整正文及附件名称，包含分支历史且不受压缩摘要影响。
- 追加输入确认现在取消时只支持`/stop`
- `/status` 改为数据库统计，不再为消息计数读取完整历史。
- 人工逐包、逐文件、逐函数、逐字段指导AI重构App到业务层，再次收拢边界、分清职责、精简代码。
- 破坏性更新：常驻记忆改为按平台和用户 ID 分层的 TOML 表，旧文件首次读取时自动备份并迁移，迁移兼容代码将在引入版本之后次版本号递增两次时移除。
- 破坏性更新：更改了hook协议
- 破坏性更新：不再为不支持视觉的模型专门进行回退处理
- 精简用户提示信息和工具schema信息
- 优化log、audit显示，日志文件名字改为`log*`
- 修改多处id格式
- Hook 原生表情输出统一使用必填的 `name`，`text` 不再参与表情构造或降级；纯文本展示统一为 `[表情: 名称]`。
- Hook 事件输出快照的投递信息从 `Meta` 改为 `Delivery` 对象，包含 `Timing`、`HookPoint`、`HookName`、`HookMode`；移除派生字段 `AltText`。
- `/status` 的 active requests 不再重复显示 Session 标题；直接使用 `/requests` 时仍保留该信息。
- 修改了 Elwisp 协议，统一使用 `platform`、`api`、`params` 描述平台原生 API 调用。
- `send_file`工具可以普通用户使用了，仅限媒体id和url
- 优化重试逻辑
- `/resume` 不再使用编号
- `/sessions`、`/archives` 关键词现在需要-k/--keyword
- `#`选择文件现在使用agent workspace中的文件
- 优化工具对agent的警告
- 读文件不再限制200行（默认还是200）
- 优化压缩逻辑
- `read_file` 的 `ast` 模式改为 Go 单文件结构索引，返回声明骨架与行范围，支持限制条目数和按编号展开类型成员；移除 `ast_function`、标识符搜索及 Go／Shell 目录 AST 查询。
- 优化chat hisotry

### Fixed

- 修复官方qq引用回复、@相关问题
- 修复 workspace 清理失效目录或并发切换后仍返回旧目录状态，以及 Git diff 输出超限时吞掉取消错误的问题。
- 文件引用的格式、越界和目标类型拒绝现在显示业务原因并结束输入处理，不再作为程序故障传播。
- 修复首次进入或 `/new` 后，一些命令因没有当前 Session 而报 `not found` 的问题。
- 修复 `/resume` 忽略置顶的问题。
- 在重构中修复一些可能会出现的bug。
- 修复chat历史搜索工具bug。现在后台、前台都能搜了，且互不影响。
- 修复多次压缩重复内容的bug

## 历史 change log 看 release
