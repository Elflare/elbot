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
3. 普通运行进入 `internal/app.Run`，由默认 `Runner` 执行；远程 CLI 进入 `internal/app` 的 CLI client 入口。
4. Runner 按 Environment、Foundation、Models、Platforms、Runtime、Integrations 阶段装配配置、日志、SQLite、LLM、Agent、Tool、Platform、Hook、Output、Cron 和 Elnis。
5. app 创建共享服务和命令 Router，创建 Agent 后注册内置命令，再连接信号、补全与平台命令目录；注册完成前不启动平台。
6. app 层按运行模式启动平台 runtime，并在平台启动后异步启动 Cron runtime。

设计边界：

- app 层负责装配，不承载业务逻辑。
- launcher 只做命令行解析，不直接初始化复杂依赖。
- 平台 adapter 只处理平台输入输出，不直接驱动 LLM。
- `app.Run` 保持默认生产入口；需要替换启动阶段或做隔离测试时，使用 `NewRunner(Dependencies)` 注入分组工厂。
- 共享 Session、Request、Turn、模型、上下文、工具状态、文件、发送、通知及命令实例由 app 创建。Agent 的 `NewWithOptions` 要求注入必需依赖，不补建服务或注册内置命令；测试装配位于测试文件中。
- app 在平台启动前安装 Session 前台接管、活动会话查询和 Hook 唤醒／执行观察回调；这些执行回调遵守原有同步准入约束，生命周期通知另走信号。Agent 自身的 Prompt、命令执行器和补全组件由 Agent 组装。
- Foundation／Runtime 工厂即使返回错误，也返回已取得资源的 Lifecycle。Runner 接管部分构建的清理责任，不启动后续阶段；延迟 Skill 加载同时提供取消上下文和实际完成信号。
- 关闭时应用上下文立即取消 Cron handler 并停止新调度。Runner 先通过 Foundation 的 `StopCron(ctx)` 等待异步启动及在途执行（含状态保存）真正结束，再断开信号并关闭队列、关闭 Hook runtime、等待 Skill 加载结束，最后关闭 SQLite 和日志。重复停止等待同一完成结果，关闭后迟到的启动不能重新开放调度。
- 平台退出、Cron 和后续清理共享 30 秒预算。预算到期停止等待；平台、Cron 或回调仍在运行时跳过其依赖的显式释放，交给进程退出，不启动后台收尾链。正常取消／关闭预算耗尽不视为应用失败，Cron 正常取消不报告任务失败；真实错误继续返回。

<!-- locator:chatinfo -->
<!-- locator:signal -->
## 公共聊天信息与信号

- `chatinfo.Info` 携带每条消息的 Source、Identity、平台消息／回复 ID 和 `PlatformData any`；平台会话 ID 不等于 ElBot Session ID。权限仍由 security 判定，公共身份不授予权限。
- 平台 `MessageContext` 嵌入 Info，公共消息标识只有一个所有者；安装时同步提供公共快照，本地 CLI scanner／TUI 只安装公共信息。Prompt 直接读取 Info 并格式化公共字段，不再通过 ConversationMeta 中转，也不展开平台扩展。
- `PlatformData` 由平台定义私有类型，优先只放公共字段无法表达的必要信息。发布后不改写；可变数据由生产者制作稳定快照，公共层不通用深拷贝、不序列化扩展。连接引用保留平台管理的生命周期，不代表持久投递地址。
- OneBot／Telegram 从公共会话信息恢复目标，QQ 官方从公共消息 ID 与扩展恢复回复。远程 CLI 扩展保存原连接引用：默认回复只到原连接，断开即失败；显式用户／管理员目标才按原有多连接规则发送。
- `signal.Signal[T]` 锁内取得订阅快照，锁外依次调用或提交执行器；一次性连接最多投递一次，入队失败也消耗连接。断开不撤销已有快照或任务，可变事件数据由发布方形成稳定快照。
- 异步连接显式选择 FollowEmit（保留发射取消）或 FollowExecutor（仅保留值）。Shutdown 独立选择 CancelPending（默认丢弃积压并取消在途）或 Drain（限时尝试完成）；底层取消始终优先。
- 有界串行队列默认容量 256，满时拒绝入队；同队列 FIFO、不同队列独立。入队成功不代表执行或投递成功，Done 只表示 worker 实际结束。预期取消不记录失败，合并错误中的真实失败仍记录。
- 平台 Connected 信号由 app 按平台分配独立队列，以 FollowExecutor + CancelPending 连接既有 Agent Hook／Cron 补发链路；连接事件无聊天来源。信号不替代事务、可改写 Hook 流水线或可靠投递状态。

<!-- locator:config -->
## 配置与运行数据

配置约定：

- 静态配置：`app.toml`。
- Provider 配置：同目录 `providers.toml`。
- 运行时模型状态：同目录 `state.toml`，启动时加载；运行中通过模型命令保存，不自动重载外部修改。
- 工具 tag 配置：同目录 `tool_tags.toml`。
- 用户可编辑资产：配置目录下的 `memories.toml`、`long_memory/`、`skills/`、`plugins/`。
- Hook 配置：入口为配置目录 `plugins/hooks.toml`；被引用插件使用 `plugins/<plugin-id>/hook.toml`，持久 Hook 在其中声明 `[plugin.runtime]`。
- Provider key 推荐用 `api_key_env`，读取优先级是系统环境变量高于配置目录 `.env`。

默认配置查找顺序：

1. `--config`
2. `ELBOT_CONFIG_FILE`
3. 平台配置目录

配置要求由所属模块声明：默认可选，必需项显式标记，示例仅免除模板补齐。`config.Load` 与只读 `config.Inspector` 共用读取、路径、默认值和合并流程；条件校验复用配置自身方法。平台和 Hook 描述由 app 显式装配，配置包不导入具体模块。

`internal/doctor` 仅将配置层结果按文件生成报告，由 app 创建并经命令 `Deps.Doctor` 注入 `/doctor`。必要问题报错，可选缺项及内置 Skill 差异提示；内容比较忽略全部 CR/LF，TOML 仍按原文解析。仅有 Elnis 相关条目时附说明链接。诊断只读磁盘，不读取密钥、初始化文件或启动外部运行时；命令仅超级管理员可用，不自动修复、不调用 LLM。

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

- user 与已完成工具 transcript 会阶段性落库；前置 Hook 绑定的当前消息在调用 LLM 前以最终 segments 落库。
- 多模态消息的 `segments` 保存原始结构；`content` 由 segments 生成可读文本投影。请求 OpenAI-compatible 模型时，再按每条消息的图片顺序临时插入对应文本标签，不向 segment JSON 增加派生字段。
- 流式输出最终由对话主流程用最终文本 replace。
- 发送前会发布 `sending` phase，便于 `/requests` 区分 LLM 慢还是平台发送慢。
- 普通输入在工具阶段不会打断工具，会以 text/image segments 进入 pending；下一次 LLM 调用前已有的 pending 会合并注入当前轮，最终 LLM 调用期间新到达的 pending 则在当前轮正常结束后作为新用户消息自动开启下一轮。
- Prompt Builder 每个 turn 从 Soul、工具提示、工具标签和当前 actor 的常驻记忆构建一次 system message；该消息只在当前 turn 内复用，不进入会话历史。

<!-- locator:commands -->
## 命令链路

Slash 命令链路：

1. `internal/agent/command_runtime.go` 的命令执行器识别命令前缀，并统一处理权限、Turn 冲突和用户通知。
2. `internal/command/router.go` 负责解析命令名、alias、参数文本和分发。
3. app 将 `internal/command/builtin/` 的模块注册到共享 Router。
4. 命令通过 deps 直接访问 Session、模型、上下文、Hook、工具 Registry／Skill Manager、日志 Reader、文件及请求管理服务。Session 列表编号按 Scope 保存在命令模块的展示状态中。
5. 命令可通过 `command.Result.Continuation` 请求在指定 Session 中继续处理一条普通输入；模式切换、历史限制等策略先由 Session 服务完成，Agent core 不识别具体 Session 命令名。
6. 平台补全通过中央 completion 服务组合命令名、命令参数、风险确认、fork message ID 和 `@tool:` 候选。

约定：

- 新命令优先做成 `internal/command/builtin/` 模块。
- 手动压缩、Scope 解析、运行状态查询和文件提交准入通过窄接口／回调接入 Agent，不将 Agent 作为领域服务转发器。
- 会改变或切换 Session 的命令必须声明 `command.Info.SessionEffect`，命令执行器据此处理压缩和 pending 确认冲突，不维护命令名白名单。
- `/stop` 的请求编号、ID 及补全对普通用户只使用当前 Session；超级管理员保留全局管理。取消前在 Session 准入内复核原绑定和目标请求，切离再恢复也不能复用旧绑定。补全 Router 将解析后的 Actor 传给命令参数补全。
- Session 规则放在 `session.Service`；命令只解析参数和格式化结果，Agent 只编排命令与普通输入。
- 命令详细帮助写在 `command.Info.Help`。
- 用户可见命令变化要同步 `docs/commands.md` 和 `CHANGELOG.md`。

<!-- locator:request-turn -->
## Request 与 Turn 状态

职责分层：

- Request manager 管 active request 树、父子关系、取消、超时和完成清理。
- Turn manager 管单个 Session 当前 turn 的阶段、原始输入、pending 追加、确认状态和工具计数；Execution 保存跨追加确认与自动压缩的稳定 RunID、前台接管上下文和一次性结果。具体请求另有 attempt 身份，迟到完成不能清除续接的新 Turn；已接收的 pending 在续接间隙保留忙碌状态，自动压缩交接也预留下一轮执行。
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
3. prepared Hook 只能改写 arguments；ToolRun 用最终参数做工具视图、命名解析、foreground-only 过滤、权限和风险确认，并把同一参数回灌当前 assistant tool call。
4. Tool Runtime 执行具体工具，并按 Actor/Policy 做风险兜底校验。
5. 已进入实际执行阶段的工具结果以 text/image segments 通过完成 Hook；工具发现状态完成提交后，再统一记录最终结果并写入 transcript；纯文本只存 `content`，多模态结果额外存 `segments`。执行前失败或拒绝不触发完成 Hook。
6. 如果工具有输出意图，交给 Output Manager 发送，而不是工具直接发平台消息。

关键约定：

- 风险等级用于内部权限和确认，不暴露给 LLM。
- 单次工具调用的预检、确认详情和实际执行沿用派生的工具 context；编辑固定参数、解析路径、实际目标、存在状态及内容 revision，撤销另外固定备份编号。执行时原绑定失效、目标或内容变化即拒绝；workspace 本身不维护变更版本，绝对路径不变或切回后状态一致可继续。
- `Result.Content` 或 typed `Result.Segments` 回灌 LLM。
- `Result.Data` 只供内部结构化消费，不进入 tool message。
- 图片和文件必须显式返回 segment。
- OpenAI Chat Completions 的 tool message 只发送文本；同一批工具图片在所有 tool message 后派生为一条带 `tool.name/tool_call_id` 说明的 user 多模态消息，且每张图片前临时插入含序号和可复用 URL 的文本标签；这些派生内容都不持久化。
- Hook、Tool、插件都不要直接发平台消息，统一返回输出意图。

<!-- locator:tool -->
## Tool Runtime 与工具发现

Tool Runtime 负责注册、schema、权限、风险、确认详情、用户侧 tags 和工具结果。

工具视图由 ToolRun 提供：

- `toolrun.StateService` 统一读取和提交 Session 的 `discovered_tools`、`tool_cache`、`tool_tags`、`shown_rule_card_formats`；metadata 是唯一事实来源，调用方只持有快照。
- 独立的 `toolrun.PreloadService` 负责前后台发现、Skill 激活、标签文件读取及标签查询，返回待提交状态和展示材料；Agent 保留输入解析、提交与输出时序。预加载服务不写 Session，工具执行仍由 Manager 负责。
- 合并 native/Elwisp 工具。
- 按前台/后台过滤 foreground-only 工具。
- 处理工具名解析、风险确认和批量工具预览。
- 同一输入的 `@tool`／`@skill` 预加载、一次工具发现或后台预加载，各自在一次 StateService 提交中合并所有工具状态；后台原生和 Elwisp 工具一起合并。成功才更新调用快照并报告注入成功。失败保留原工具状态并返回错误，已经完成的工具动作不回滚或自动重跑。
- Agent 在输入 Hook 和预加载前拒绝压缩期输入；耗时准备在锁外，提交前重新取得原绑定准入，检查取消、绑定、模式和压缩状态。纯指令输入同样检查；拒绝时不保存缓存、tag 或规则卡展示状态，也不报告注入成功。准入规则不进入 PreloadService／StateService。
- 前台 chat 不预加载 Skill、不读写工具状态，也不注入工具标签提示。LLM 请求与响应边界丢弃强行注入的 schema／tool calls，不进入工具循环。
- 后台 Session 固定为 `background`。首次创建时将显式工具、外部工具、Skill runner 及必要依赖一并交 StateService 提交；同 Session 的续跑和格式重试只恢复此状态，忽略新工具参数。首轮没有工具即始终没有工具，直到前台接管。
- 后台 schema 只取允许的缓存项，禁止 `discover_tool`、`workspace` 和 ForegroundOnly 工具；请求 Hook 不能扩大 schema 集合。工具调用在准备 Hook 改名之后、风险评估和副作用之前按缓存白名单检查，不回退全 Registry。
- `tool_list_names` 优先匹配工具／Skill 名，再匹配标签；只有显式选择的标签注入提示，直接选择工具不附带关联标签。Elnis 与 Agent 使用同一 PreloadService 展开标签，每个根工具仍受 `allowed_tools` 限制，执行前再次按已授权根工具过滤。任务正文中的 `@tool`／`@skill` 不扩大权限。
- schema 对外返回独立副本，Hook 对嵌套参数的修改仅作用于本次调用；工具注册与 Skill 生命周期仍由原 Runtime 管理。
- 恢复读取已有状态；Fork 按指定范围继承历史，不复制父 Session 工具状态或最近用量。

`discover_tool` 的特殊约定：

- 查询普通工具时，返回“已发现工具”文本，并把完整 schema 放在结构化 Data 供 Agent 注入 top-level tools。
- 查询说明型 AgentSkill 会激活 `agent_skill` 元工具。
- 查询工具化 AgentSkill 会注入其 top-level schema。
- 查询 Go skill 会按需激活 `go_skill_run`。
- `read_file`、`edit_file` 依赖隐藏的 `rollback_file`；依赖展开仍执行超管权限和前台限制，tag 为 `files`。

文件编辑与撤销由 app 创建唯一共享 `fileops.Service`，注入内置 Runtime、Agent 和命令。`fileops.RollbackManager` 从编辑的原始读取保留字节，成功写入后才登记，按实际目标串行化编辑与撤销；每个 Scope 的当前 Session 中每个目标只保留一份。内存上限为 256 MiB/1024 条，超限淘汰最旧记录。备份不进入工具结果、Session metadata 或存储层。

`/rollback` 直接调用文件服务的列表与按编号撤销入口，负责展示和审计。Agent 的 `PrepareFileCommand` 捕获原 Binding、workspace 与提交准入；文件服务查找编号并复用路径、revision 和提交校验，不能在执行时改用新的当前绑定。

`workspace` 提供工作目录契约与路径解析，组合 `sandbox` 的后台限制；`session.WorkspaceStore` 通过 SessionID 和仓储读取最新状态，以短事务更新 workspace 及说明文件提示记录，不持有 Agent 或回写共享行快照。未知 metadata 保留原值，损坏数据拒绝读写；后台初始化不覆盖已有 workspace。

文件提交顺序为目标路径锁 → Scope → SessionID → 备份状态锁，Session 准入期间不等待目标锁。读取、内容校验、diff 和临时输出准备在 Session 准入外；提交内复核原绑定、取消、解析目标、权限和文件身份／stat，再完成最终替换、写入或删除及备份登记。Session 切换、删除、清理和 `/stop` 与提交互斥，先进入提交则完成操作，失效或取消先发生则拒绝。普通 Turn 非 idle 仍禁止切换；idle 下 `/rollback` 在提交准入内再次检查忙闲。后台编辑共用目标锁及 SessionID 准入，不登记前台撤销备份。外部进程不参与这些锁。

### 媒体引用与清理

媒体本体按 SHA-256 去重，`media_references` 是引用事实来源，不维护整数计数。消息追加/替换、Session 删除、fork、工具参数、Cron 报告状态、Elnis 排队 ID/outbox、输出关联与引用通过事务及 SQLite 触发器同步。工具通过确认并即将执行时，ToolRun 递归检查最终参数的 JSON 值，将完整且有效的媒体 ID 作为 `session_tool` owner 原子关联到当前 Session；同一 Session/媒体幂等，失败执行仍保留，执行前拒绝或跳过不关联。新 fork 按检查点边界继承父消息、祖先 fork 及此前的工具参数媒体；Chat History 的原始平台 URL/file ID 不构成中心引用。读取、LLM 请求和发送期间另有临时引用。

媒体中心在持久化及对外返回副本时统一规范化名称和来源：名称只保留跨平台 basename，平台文件 ID 只保留不透明 ID，来源 URL 不保留用户信息、query、fragment 或 Telegram token 路径。实际下载仍使用清洗前的调用参数；旧记录按需清洗返回副本，不批量回写或重算媒体 ID。

`ImportReader` 在导入硬上限校验后、内容哈希和后端写入前统一检查图片；字节数超过 `[media]` 阈值或边长达到限制时，压缩为白底 JPEG，只保存压缩内容并返回对应 ID、名称、MIME 和大小。入站消息与历史关联直接保存该 ID。`ResolveForLLM` 物化请求副本，按持久化媒体大小选择 base64/S3/hybrid。S3 后端按需初始化，配置不可用只告警并使实际远端操作失败，不阻止应用启动。

聊天历史查询不下载媒体：`search_chat_history` / `get_chat_history_around` 按媒体顺序展示编号和 `[图片 media:未下载]` 或已有媒体 ID。`get_media(message_id=[...], media_index=[[...],...])` 限定当前平台/scope；序号从 1 开始，省略索引时每消息取首个媒体。只有显式获取才下载，单次最多尝试 5 个未入库媒体，失败计数，缓存和同次重复位置不额外占额度；结果为纯文本，不返回图片内容。

Chat History 库只保存清洗后的原始来源。主库 `media_history` 保存历史记录内部 ID、平台/scope/消息 ID、媒体位置及媒体 ID，并以触发器同事务维护 `chat_history` owner 引用。回复历史消息时按非文本媒体位置和当前历史内部 ID 恢复仍有效的媒体 ID，不下载或重新解析来源；缺失位置继续使用原有来源。入站实际消费和 get_media 共用导入与关联规则；文本工具结果中的 ID 本身不建立 Transcript 媒体引用。删除历史成功后释放关联；启动和媒体清理前按主库已有关联分页核对历史 owner，消息不存在或内部 ID 已替换才释放，历史库故障保守停止。关联替换使用新的 owner ID，避免延迟对账删掉新关联。最后引用释放后继续遵循 1 小时孤儿宽限期，不扫描/下载全部历史。

Elnis direct 实际投递才导入中心，不生成 sandbox 下载副本；LLM 输入在执行时物化。workspace 普通文件不入库，报告附件准备投递时安全导入，outbox 只保存稳定 ID。Agent 通用发送边界以结构化回执建立平台/scope/消息 ID/segment index 到 kind/media ID 的有限期有序映射，重复媒体位置独立保留；Elnis 不再单独缓存发送关联。

输出缓存到期释放引用，待重试和 Session 引用独立保留。最后引用释放后记录 orphaned_at，无引用资源再次使用会刷新时间；宽限期固定 1 小时。清理认领与引用添加在 SQLite 写事务中互斥，认领后禁止新引用，并按记录中的实际存储位置选择后端；双副本先删除远端再删除本地，全部成功后才移除记录。所需后端不可用或任一删除失败时保留 deleting 状态供幂等重试。共享 Manager 按完整媒体 ID 对导入、按需上传和物理删除互斥，不同 ID 的后端操作可并行；锁条目在持有者和等待者全部退出后回收，等待支持 context 取消。按需上传到签名完成期间持有临时引用。每轮清理最多并发处理 4 个对象，单对象仍按远端、本地、数据库顺序删除；清理轮次之间单独互斥，不阻塞其他 ID 的导入。取消后停止派发并等待已启动任务退出，未完成对象保留删除状态供重试。

媒体维护任务复用 sandbox 清理时间表，独立于按文件年龄清理的 workspace 任务；已发送缓存复用 retention_days，非正值不缓存。启动时在任务运行前恢复 Hook/Skill/request 临时引用，并将无法恢复的内存队列事件标记失败；持久化 outbox 保留。清理前只读检查消息、outbox、输出、Cron 报告及 fork 历史的缺失引用和悬空 owner，一致性异常时保守停止并报告。Elnis 终态写入使用独立于调用取消的有界清理 context，使失败状态和事件引用释放在同一事务内完成。

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

Reload 由 Skill Manager 串行执行：scanner 先构建并验证完整候选集，registry 在单次写锁内替换 Agent/Go Skill 快照，成功后再替换 catalog；任一步失败均保留旧运行快照。`agent_skill` 写入 `ELBOT_SKILL.toml` 后若 reload 失败，会在同一管理事务内恢复原文件。

Skill 媒体处理发生在具体工具的执行阶段，权限/风险评估不导出文件。`tool.MediaRuntime` 复用 Media Center API 管理显式输入、调用期引用和临时导出；ToolRun 会递归识别参数 JSON 中完整的媒体 ID 以建立 Session 引用，但不扫描自由文本，也不递归替换任意字符串参数。shell 解析 `media_inputs` 并注入调用级 `ELBOT_MEDIA_N`；Go runner 处理 `payload.media_inputs`；TOML 工具只处理 `type=media` 的顶层参数，对 LLM 投影为字符串 schema。

shell 导出缓存位于 sandbox 的 `media-inputs/`，按内容 ID 命名，首次导出原子发布，复用时刷新 ModTime，直接沿用 sandbox 时间清理。Go/TOML 保持 Skill 根目录为 cwd，媒体输入使用调用专属子目录和相对路径。stdout 媒体段通过 `os.Root` 校验、导入并生成稳定 ID，随后清理调用目录和引用；落库沿用 message/tool_result 引用事务。普通文本中的 ID 不触发转换。

<!-- locator:hook -->
## Hook 链路

普通 Hook Manager 按事件点和优先级串行执行 Handler，`hook/control.Service` 作为 `/hooks` 的独立管理入口，组合普通 Manager、持久 Runtime 和配置 loader。

常见来源：

- 规则 Hook：读取 `plugins/hooks.toml`。
- exec action：按 `hook.v2` 一次性 Pipe 协议执行，默认在 `plugins/` 目录直接启动 argv 命令，不隐式经过 shell；取消或超时时终止该次调用的完整进程树。
- 持久 Hook：在插件 `hook.toml` 的 `[plugin.runtime]` 声明；Hook runtime 管进程生命周期、双向 RPC、waiting 路由和进程内 SharedState。

约定：

- Hook 配置先加载到候选 Manager，并完成持久 Runtime 配置校验；候选构建或校验失败时保留当前活动 Hook。提交时先一次性替换 Runtime worker 索引，再原子替换普通 Hook handler 快照。
- 持久进程启动仍是异步生命周期，reload 提交后可短暂处于 `starting`，进程后续失败由既有状态和重启策略处理。
- 所有进程 Hook 共用启动时构建的环境快照：进程环境优先补充配置 `.env`，PATH 按进程目录在前、`.env` 目录在后合并；argv 首项也用该 PATH 解析。
- Hook 可返回控制字段和输出意图。
- Go Hook 通过事件提供宿主 `MediaAPI`；进程 Hook 通过 `media.import`、`media.read`、`media.export` 和 `media.metadata` 使用媒体。稳定 `media` 引用可跨消息传递，Host 仅在发送边界导出为临时文件；临时 Hook 引用在过期或 runtime 关闭时释放，外部 Hook 不接触 SQLite、媒体根目录或 S3 凭据。
- 入站消息的唤起状态在 Agent 消息入口计算一次并随 context 贯穿处理链；后续 Hook 不根据已改写的 user 文本或 assistant 输出重新推断。
- `llm.messages` 对普通 Hook 只读并以深拷贝提供；turn Hook 只能修改当前初始 user，request Hook 只能修改本次请求前新 drain 的 pending。
- 进程 Hook 可用 `message.segments` 替换当前绑定消息；用户/pending 修改在请求前落库，工具完成 Hook 的修改进入 transcript 和后续 LLM 请求。
- Hook 不直接发平台消息。
- 输出预处理 Hook 运行在 assistant 最终发送前。
- 命中 waiting 租约的消息在常规平台 Hook 后、命令和主 LLM 前交给持久 Hook；`/cancel` 只取消该路由执行，不停止进程。
- Hook 用户文档优先看 `docs/hooks.md`。

<!-- locator:output -->
<!-- locator:notification -->
## Output 与发送链路

输出层把 Agent、Hook、Tool、Elnis 等来源的输出意图统一发送到平台。

职责：

- 定义 text/image/file/record/at/reply/emoticon 等平台无关输出类型。
- 提供媒体源前缀、fallback 文本、delivery timing 元数据。
- `delivery/dispatch.Router` 统一选择平台发送器，执行媒体准备、普通发送、流式发送、reasoning 和 runtime status 的平台调用。
- `notification.Manager` 消费通知意图，复用 Router；`notification/rules` 维护平台连接、插件／Hook、模型和执行错误的通知规则与文案。业务模块决定触发时机，Agent 保留需要 Hook 改写的输出编排。

约定：

- 业务层返回输出意图，不直接调用平台 adapter。
- 平台 adapter 负责把平台无关输出转换成平台 API。
- app 在 Hook 注册及 Agent 创建前装配共享 Router 和通知管理器；Hook／脚本、Cron、Elnis 直接使用 Router，不经 Agent 宿主发送闭包。无来源启动告警在交互模式显示于本地 CLI，在 service 模式记录实际告警内容，不广播管理员。
- 显式目标优先；无显式目标时使用原消息发送器覆盖或 Info 的平台来源。后台丢弃发送器仍有效；原消息信息随任务保存，实际发送使用任务自身的 context，不查询当前 Session 重建旧目标。
- 通知意图携带原 Info、原 Sender 覆盖和按需提供的 Session Binding；过期绑定或取消 context 拒绝发送。同步调用等待实际回执，平台连接沿用独立信号执行器，没有额外通知队列或可靠投递中间件。
- Router 在发送前把 URL/Path/Data 归一为 MediaID，发送副本经 `ResolveForOutput` 临时解析，并释放临时导出。回执按实际成功的输出索引建立媒体关联；多目标 scope 由 adapter 明确提供，缓存期限复用 sandbox retention，非正值不缓存。
- 部分失败同时返回成功 Receipt 与 error；Agent／Cron／Elnis 关联已成功的平台消息，错误仍返回，任务不会因此整体成功。缓存失败只记录日志，不重发平台消息；通知发送失败不再触发通知。
- QQ OneBot 把 record 输出转换为原生语音段；暂不支持 record 的平台使用统一文字 fallback。
- 流式输出、notice、reasoning、runtime status 由 Agent turn 输出适配层区分前后台发送。

<!-- locator:platform -->
## 平台适配层

平台层负责输入归一化和输出落地。

输入侧：

- 解析 Actor、Scope、发送目标、群身份、引用、多模态消息段和平台 metadata。
- 原始有序 segments 写入 Chat History，包括纯媒体消息；过滤 base64、临时本地路径和 token/签名 URL，不保证来源永久有效。
- Agent 统一判断 wakeup，并只读检查 waiting Hook route；仅唤起或 waiting continuation 时物化媒体，普通观察 Hook 不下载。
- Telegram resolver 内使用 token URL和代理，OneBot 按需 get_image/get_file；QQ Official 的事件 URL直接由 Media Center 导入，不引入额外 resolver 层。
- 引用按输出索引 → Chat History → 平台能力恢复有序媒体，图片进入视觉输入，Session 仅保存稳定媒体 ID 与文本投影。同一 actor、平台和 scope 的最后一条 assistant 显式设置 `ResumeSessionID`，使 TTL 清理或 `/new` 清除 current 后仍恢复来源 Session，且不重复注入引用内容；较早 assistant 设置 `ForkFromMessageID` 并保留引用媒体。后台 Resume 和其他用户或 scope 的普通引用规则保持独立。

输出侧：

- 实现统一 `SendChat` / `SendNotice`。
- receipt 同时返回平台消息 ID 和结构化 `SentMessages`（platform/scope/message ID/output indexes），部分成功保留成功项，文本降级不关联媒体。
- 平台发送保持同步回执语义；不得把需要平台消息 ID 或错误的调用改成只入队即成功。
- 支持平台能力差异下的 fallback。

常见平台：

- CLI 本地/TUI 与远程 CLI。
- QQ OneBot v11。
- QQ 官方机器人。
- Telegram。
- headless service 模式。

<!-- locator:session -->
## Session 生命周期

Session 服务唯一管理 current 绑定及其同步失效。绑定只公开 `Scope()`、`SessionID()`、`Valid()`，不提供取消；`CurrentBound` 一并返回持久化快照和原绑定。切离后旧绑定永久无效，切回同一 Session 获得新绑定；未变化的 current 不重复发信号。输入、工具和确认续接传递原绑定，普通旧调用不能重新捕获 current 而复活。

`PrepareBackground` 创建或复用后台会话，在同一 `Session.Mode` 字段固定 `background`，管理标题及后台身份 metadata，不激活前台 current；复用时保留其他模块字段，拒绝已被前台接管的会话。首次后台工具状态由调用方另交 StateService 提交。

`CreateCompacted` 接收来源 Session ID、预分配的新 ID、标题与已准备的 metadata，在准入内复核来源及前台原绑定，继承归属和模式，统一设置命名字段并保存新会话。前台更新 current，后台不创建前台绑定；保存失败不改变绑定。Session 不依赖 contextmgr，摘要、seed、代数及压缩标题材料仍归上下文服务，执行交接仍归 Agent。Fork 保留来源模式。

仅发布 `BindingChanged{Old, New, Reason}`，覆盖创建、恢复、Fork、重置、删除、过期和记录缺失导致的 current 变化，不发布一般字段或持久化增删事件。删除会失效所有指向该记录的绑定。信号在状态与准入锁释放后发出，允许回调重入。app 持有独立撤销清理队列及订阅，以 `FollowExecutor + CancelPending` 清理指定旧绑定；队列延迟不影响同步失效，关闭沿用共享 30 秒预算。维护任务复用运行中的 Session 服务。

短准入按 Scope → 排序后的 SessionID → 状态锁取得。Scope 保护 current 解析与切换，SessionID 协调 Turn 启动、停止、交接、删除和清理；LLM、工具、Hook、发送和信号回调均在锁外。Session 通过注入的只读执行状态判断忙闲，当前非 idle 时禁止切离，显式删除拒绝执行中的 Session，维护清理跳过忙碌项并在条件删除时复核归档、置顶和时间。不同 Scope 不共用全局准入锁。

后台 Session 只向所属用户的同平台私聊及 CLI 管理入口开放，群聊、频道和未知类型不可列出或直接恢复。首次恢复在原子更新中将归属永久改为前台 Scope、模式改为 `work`，记录 `foreground_origin` 并清除活动后台身份；保留历史、缓存和 workspace，切走或重启不会恢复后台身份。运行中的目标可被空闲前台接管，原 Execution 同步取得前台身份、绑定和输出目标；已发出的请求与工具不重启，后续请求使用 work 模型及前台工具、确认规则。

接管后的原后台任务等待该逻辑执行的最终完成、取消或失败。Cron／Elnis 保存实际 RunID、消息和结果并标记接管，不将其直接算作任务成功；停止 JSON 修正、自动汇报与未开始的补投递。再次使用已接管 SessionID 不会重新设置后台身份，独立定时触发仍创建新后台 Session。

Session 命令的分页选择和维护配置由 `SessionCommandState` 按 Scope 保存。闲置 TTL 按会话类型与角色选择；过期和 `/new` 只清除 current，下一条普通输入才创建记录。恢复刷新活跃时间，Fork 上下文由 Session／Storage 处理。

<!-- locator:llm -->
## 模型服务

- app 构造共享 `modelmgr.Service`，注入 Agent、模型命令及 Elnis 槽位解析。服务唯一持有模式／槽位、compact、naming 选择，provider 客户端和模型目录缓存；不依赖 Agent、Session 或命令包。
- 命令用 Session／Scope 确定当前模式，模型匹配和切换由服务执行。目录按 provider 并行查询，缓存模型与错误，显式刷新；配置模型始终参与合并，编号在筛选前统一分配。目录结果和选择状态以独立快照交付。
- 切换串行构建候选状态，调用 `config.SaveState` 原子替换状态文件后再发布内存状态；失败保留旧选择。写盘不持有状态读锁，读取方继续使用旧快照。状态文件保留原有字段及默认 Session 模式；未配置路径的独立实例仅更新内存。
- `Selection` 固定 provider、模型和客户端。对话固定本次 Turn 选择；压缩固定专用选择或本次对话 fallback；命名同时固定专用选择及 work fallback。Turn／Request Prepared Hook 的 provider/model 只读，Go Handler 的相关修改不回写模型快照；当前消息仍按各 Hook 点的原契约修改。前台接管保留明确的重新选择边界。
- `background` 只是 Session 模式，没有对应模型槽位。默认后台选择 work 模型；Elnis 保留 elwisp1/2/3 槽位及缺省回退 work。Cron 任务可显式指定 provider/model，由共享 modelmgr 校验，不改变全局选择。
- 标题生成与压缩调度留在原模块，不保存独立模型选择。app 将模型服务的重试回调接入 `notification/rules.ModelRetry`；客户端配置在启动后保持不变。

<!-- locator:context -->
## 上下文管理

app 创建共享 `contextmgr.Service`，注入 Agent；服务不持有 Request／Turn 管理器。上下文管理负责：

- 加载历史消息和 Fork 上下文。
- 解析 context window。
- 按当前模型的 context window 动态判断压缩阈值。
- 格式化厂商 usage 状态。
- 管理 `last_usage`、`context_compact` 的编解码和字段更新，准备压缩材料与结果。Agent 保留压缩准入、Request／Turn、取消和新 Session 交接。

约定：

- Prompt Builder 只生成单条 system prompt，并组合历史、工具 transcript、多模态 metadata 和摘要。
- 压缩以可取消 Request 和执行身份保护生命周期，仅总结有效对话与成功工具调用。交接在短准入内复核取消、绑定和身份，先使旧 Turn idle，再创建无 Parent/Fork 关系的新 Session；旧记录保留。前台激活新绑定，后台保持无 current 的后台执行，接管事实及 workspace 随交接保留，旧 token 用量不带入。
- 压缩期间拒绝新输入，支持停止；取消先于交接生效时不能切换 current。自动压缩继续此前已接收的输入和工具 pending，手动压缩不自动聊天。
- 新 Session metadata 暂存一次性 compact seed；首条用户输入时，Prompt Builder 将“压缩结果 + 历史用户原话 + 当前输入”物化为单条 user message，成功持久化后消耗 seed。
- 模型选择在 turn 开始时快照；进行中的 `/model` 不改变当前 LLM/工具循环，下一轮按新模型重新解析窗口与阈值。后台转前台后解除后台模型覆盖和强制 JSON／无人值守提示，后续 LLM 调用使用前台身份。
- System Prompt Manager 按优先级收集 Soul、工具名称、tag prompt 等片段。
- 最近 usage 写入 Session metadata，恢复会话后可展示；服务按 Session 隔离观测值并返回副本，保存失败记录日志但仍保留已观测用量。seed 消耗只更新所属字段，压缩交接从最新 metadata 继承其他模块字段并移除旧用量。

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
- Session 写入统一使用 `Mutate(ctx, id, updateFn)`：短事务读取最新行、修改负责字段并返回新快照，回调错误回滚；回调不做 I/O、模型调用或嵌套仓储操作。metadata 使用 RawMessage 保留未知字段及数值精度，解码失败拒绝写入；集合合并、接管条件和手动命名优先检查均基于事务内最新值。
- Message 的 `segments` 是多模态消息的完整结构来源；`content` 是由 segments 生成的纯文本快速路径。仅多模态内容保存 segments，读取时非空 segments 优先，否则直接使用 content。
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
6. llm 后台任务以固定 background 模式运行，并投递结果报告。

LLM 报告使用 SQLite outbox：result 与逐目标、逐 output 的投递项原子落库，投递期间状态为 `result_ready/delivering`，所有 receipt 持久化后才进入 `completed`。Runtime 定时重试未完成项，并在启动时恢复被中断的投递；语义为至少一次。

约定：

- 公共协议类型放在 `internal/elvena`，Elnis 复用别名。
- segment 下载和 URL/data URI 校验集中处理。
- 后台 LLM 使用后台 actor、固定 sandbox subdir 和 background 模式，协议不选择 Session 模式。shell／文件工具的相对路径解析到对应 `elnis/<elwisp>` 沙盒；后台无 workspace 工具、不读取 AGENTS.md，SOUL、常驻记忆、Skill 说明及显式 tag 提示复用现有 Prompt 链路。
- 报告发送不能先写 `completed`；外部平台未提供幂等能力时允许恢复产生重复消息，但不能静默丢失待投递项。

<!-- locator:cron -->
## Cron 与后台任务

中央 Cron Runtime 负责持久化 job 的调度、注册、upsert、禁用、删除、运行状态和执行日志。

约定：

- 维护类任务集中注册在 maintenance 包。
- LLM cron 每次实际调度触发都通过 Agent 后台 runner 创建新 Session；同一轮 JSON 格式重试才复用 Session。
- 一次性 cron 的任务配置与 Delivery 状态分列持久化；Delivery 以 RunID/报告 Session ID 做条件更新，只写仍启用的当前轮次，不能覆盖配置或重新启用任务。
- 已完整投递的任务条件禁用；ReportReady 已持久化的未完整投递任务沿用旧报告补发，不重新执行 LLM。
- 正常触发和平台连接补发共用逐实际收件目标、逐输出状态；`ReportReady` 表示报告可复用，LLM 的 `TaskCompleted` 只记录任务结论。
- 补发读取最新任务配置且只由平台连接触发；附件失败在同次补发中降级为路径或 URL 文字，降级文字失败则等待下次连接，不做周期重试。
- 同任务投递互斥支持 context 取消；补发取消后停止后续目标且不发送失败通知。ReportReady 及逐输出回执决定恢复位置；报告持久化前可能重新执行，发送成功但回执尚未持久化时可能重复投递。
- LLM cron 首轮可指定工具、Skill 或标签，续跑沿用首轮工具状态。
- Cron metadata 保存可选 `model_provider`／`model`，创建和更新必须成对设置；更新省略则保留，两者同时清空则恢复 work 默认。每轮执行解析一次模型，格式重试复用相同模型和 Session；任务专用选择不修改全局模型。
- cron/Elnis 后台 shell 的非 critical 风险可自动确认，critical 直接返回提醒，不等待用户。
