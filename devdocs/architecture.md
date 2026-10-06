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
5. app 创建共享服务、provider 注册表和命令 Router；Agent 校验各客户端的私有接口，绑定业务能力及归属描述，登记源协议 Compactor 并封闭注册表，随后注册内置命令和连接信号、补全与平台目录；接线成功前不启动平台。
6. app 层按运行模式启动平台 runtime，并在平台启动后异步启动 Cron runtime。

设计边界：

- app 层负责装配，不承载业务逻辑。
- launcher 只做命令行解析，不直接初始化复杂依赖。
- 平台 adapter 只处理平台输入输出，不直接驱动 LLM。
- `app.Run` 保持默认生产入口；需要替换启动阶段或做隔离测试时，使用 `NewRunner(Dependencies)` 注入分组工厂。
- 共享 Session、Request、Turn、模型、上下文、工具状态、文件、发送、通知及命令实例由 app 创建。Agent 的 `New(ctx, cfg, deps)` 要求注入必需依赖，不补建服务或注册内置命令；测试装配位于测试文件中。
- app 在平台启动前安装 Session 前台接管、活动会话查询和 Hook 唤醒／执行观察回调；这些执行回调遵守原有同步准入约束，生命周期通知另走信号。`New(ctx, cfg, deps)` 组合内部组件，Prompt、命令执行器和补全直接使用所属依赖。
- Foundation／Runtime 工厂即使返回错误，也返回已取得资源的 Lifecycle。Runner 接管部分构建的清理责任，不启动后续阶段；延迟 Skill 加载同时提供取消上下文和实际完成信号。
- 关闭时应用上下文立即取消 Cron handler、Session 命名和追加确认等待，并停止新调度。Runner 先对订阅调用 `BeginClose`，断开连接、关闭队列接收并唤醒等待入队的生产者，再等待平台与 Foundation 的 `StopCron(ctx)` 结束，随后等待队列、追加确认及其在途提示、命名和 Hook runtime 退出、等待 Skill 加载结束，最后关闭 SQLite 和日志。重复停止等待同一完成结果，关闭后迟到的启动不能重新开放调度。
- 平台退出、Cron 和后续清理共享 30 秒预算。预算到期停止等待；平台、Cron 或回调仍在运行时跳过其依赖的显式释放，交给进程退出，不启动后台收尾链。正常取消／关闭预算耗尽不视为应用失败，Cron 正常取消不报告任务失败；真实错误继续返回。

<!-- locator:contextinfo -->
## 公共上下文事实

- `contextinfo` 唯一定义并存取 Conversation、Actor、Execution、Model 四组事实，各 getter 返回快照及是否存在；缺失不补入口身份或全局模型。公共包只依赖 context，不持有配置、凭据、服务或原生载荷。
- Conversation 携带 Source、Identity、平台消息／回复 ID 和 `PlatformData any`；平台会话 ID 不等于 ElBot Session ID。Actor 携带解析后的 ElBot 身份、角色和平台群角色，权限仍由 security／ToolRun 判定，角色事实不授予权限。
- 平台 `MessageContext` 嵌入 Conversation，安装时将其移入公共 context，私有上下文仅保存正文、Sender 等平台能力；读取时从公共事实重建投影。本地 CLI scanner／TUI 只安装公共 Conversation。Prompt 直接读取公共事实，不展开平台扩展。
- Execution 由 Session、Request、Turn 在建立关联时提供 SessionID、当前 RequestID、ParentRequestID、RootRequestID、RunID、Attempt；工具／Hook 子请求有自己的 RequestID，保留主请求 RootRequestID。这些值不代替 Binding.Valid、活动执行或 attempt 校验。
- Model 在主对话选择确定后，由固定 Selection 与 provider 绑定描述提供 provider、model、protocol；前台接管采用新选择时更新，普通全局模型切换不改在途事实。选择前及无主对话的入口不虚构 Model。
- `PlatformData` 由平台定义私有类型，优先只放公共字段无法表达的必要信息。发布后不改写；可变数据由生产者制作稳定快照，公共层不通用深拷贝、不序列化扩展。连接引用保留平台管理的生命周期，不代表持久投递地址。
- OneBot／Telegram 从公共会话信息恢复目标，QQ 官方从公共消息 ID 与扩展恢复回复。远程 CLI 扩展保存原连接引用：默认回复只到原连接，断开即失败；显式用户／管理员目标才按原有多连接规则发送。
- 前台接管一起刷新 Conversation、Actor、原绑定及整份平台上下文；新来源缺失的事实明确清除，本地 CLI 同样清除后台 Sender 与消息残留。执行自身的取消保持不变。

<!-- locator:signal -->
## 信号与订阅

- `signal.Signal[T]` 锁内取得订阅快照，锁外依次调用或提交执行器；一次性连接最多投递一次，入队失败也消耗连接。断开不撤销已有快照或任务，可变事件数据由发布方形成稳定快照。
- 异步连接显式选择 FollowEmit（保留发射取消）或 FollowExecutor（仅保留值）。Shutdown 独立选择 CancelPending（默认丢弃积压并取消在途）或 Drain（限时尝试完成）；底层取消始终优先。
- 有界串行队列默认容量 256，默认满时拒绝入队；普通、审计及命名日志显式启用 `WaitForCapacity`，满时等待容量，按成功入队顺序写入，不丢弃、不绕过队列。`BeginClose` 立即停止接收并唤醒等待者；已入队日志使用 FollowExecutor + Drain。入队成功不代表写入或投递成功，写入失败、日志入队被关闭拒绝及预算耗尽时未排空的日志明确诊断；Done 只表示 worker 实际结束。正常取消不报告业务失败，合并错误中的真实失败仍记录。
- Agent 的类型化事实、modelmgr 的共享模型重试和 Session 的命名信号由 app 订阅，事件携带发布时的稳定快照。核心 Usage、工具数据库记录、消息提交与回执关联仍直接执行；可改写 Hook 和需要返回结果的调用不改成旁路信号。
- 平台 Connected 信号由 app 为每个平台的 Hook、Cron 恢复分别订阅并分配独立队列，使用 FollowExecutor + CancelPending。用户 Hook 的阻塞、错误和截断不影响 Cron；补跑、投递状态和任务互斥仍由 Cron 管理。连接事件无聊天来源，信号不替代事务、可改写 Hook 流水线或可靠投递状态。

<!-- locator:config -->
## 配置与运行数据

配置约定：

- 静态配置：`app.toml`。
- Provider 配置：同目录 `providers.toml`；`api_mode` 仅接受 chat／response，省略为 chat，启动与只读诊断共用校验。app 据此构造实际客户端。
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
2. Agent 委托 messageHandler 处理唤醒、入站媒体与平台 Hook；commandExecutor 分发命令，inputCoordinator 准备普通输入，执行与确认组件处理 pending 和确认响应。
3. executionCoordinator 在准入内检查目标 provider 能力、登记会话归属并启动 attempt，dialogue.Runner 加载单轮材料并按 provider 取得路线；协调器登记 Request 后，Chat 的每轮执行对象构建 Prompt，公共 Preparer 执行准备 Hook，MessageStore 保存用户输入，再由 chat.Caller 调用模型。
4. LLM 返回文本、reasoning 或 tool call。
5. 如果有 tool call，chat.Loop 通过公共 ToolExecutor、既有 ToolRun 和 toolRunDeps 执行工具；公共 MessageStore 保存工具 transcript，Chat 组织后续请求并继续循环。
6. 最终输出前，dialogue.Runner 通过 ExecutionView 刷新接管来源和 Session，发布 sending 状态，再调用公共 ReplyCommitter。
7. ReplyCommitter 执行最终输出 Hook，通过 Output／outputSender 发送并按直接／缓冲路径完成 assistant 落库及实际回执关联；Runner 返回单轮结果，由 executionCoordinator 继续执行收尾。

关键约定：

- user 与已完成工具 transcript 会阶段性落库；前置 Hook 绑定的当前消息在调用 LLM 前以最终 segments 落库。
- 多模态消息的 `segments` 保存原始结构；`content` 由 segments 生成可读文本投影。请求 OpenAI-compatible 模型时，再按每条消息的图片顺序临时插入对应文本标签，不向 segment JSON 增加派生字段。
- 公共 ReplyCommitter 用最终展示文本调用输出适配器完成流式 replace／finish；历史正文与原始模型文本不受展示 Hook 改写影响。
- 直接输出先发送及投递延迟 outputs，再落库、关联；缓冲输出先落库，再发送、关联及投递延迟 outputs。提交失败仍保留实际 assistant 回执和持久化结果，不重发已成功内容；只用回执中的完整平台、Scope 和消息 ID 关联，关联失败记录但不终止对话。
- 发送前会发布 `sending` phase，便于 `/requests` 区分 LLM 慢还是平台发送慢。
- 普通输入在工具阶段不会打断工具，会以 text/image segments 进入 pending；下一次 LLM 调用前已有的 pending 会合并注入当前轮，最终 LLM 调用期间新到达的 pending 则在当前轮正常结束后作为新用户消息自动开启下一轮。
- chat.Loop 拥有 Prompt Builder；系统提示来源与构建器归 dialogue，每个 turn 从 Soul、工具提示、工具标签和当前 actor 的常驻记忆构建一次 system message，该消息只在当前 turn 内复用，不进入会话历史。
- 单轮结果区分完成、暂停、停止、取消、失败与 attempt 已失效，保留提交事实、Usage、模型和计时。回复成功后，协调器依次处理 Touch、Usage、完成状态、压缩提示、pending 交接、Execution 结果和命名；提交期间进入追加确认也不会丢失已成功提交的用量。
- 单轮材料加载在 Request 登记前，Prompt／Hook 和用户消息落库在登记后；Request 及 attempt 清理由执行协调器负责，dialogue.Runner 不结束跨轮 Execution。
- 登记后的 Prompt、工具准备、`llm.turn.prepared`、媒体归一与用户消息落库使用 Turn Request context；准备阶段检查取消，Hook 子请求继承父请求 ID。状态发布和已完成回复提交沿用执行 context，保证已提交事实不被模型请求取消吞掉。

Agent 根包、公共单轮层和协议路线按消费接口直接组合，不持有 Agent 或绑定 Agent 的回调：

| 组件 | 当前职责与状态 |
|---|---|
| messageHandler | 唤醒判断、按需解析入站媒体、平台消息 Hook、命令／普通输入分发和入口错误通知。 |
| inputCoordinator | 普通输入 Hook、工具／Skill 预加载、命令 continuation 和闲置过期检查；锁外准备、锁内提交与原 binding 复核保持原边界。 |
| backgroundRunner | 后台身份、来源、sandbox、Session 与资源预加载准备，委托执行协调器等待最终结果。 |
| commandExecutor | 直接连接 Router、身份、执行、确认、输入和输出组件，处理权限、Turn 冲突与 continuation，不回调 Agent。 |
| fileCommandPreparer | 文件命令权限、原绑定、workspace 与提交准入；文件操作仍归 FileOps。 |
| executionCoordinator | 统一前后台准入、attempt、Request 生命周期、追加确认、pending 续跑、压缩交接和执行完成；拥有追加确认等待任务的生命周期，后台入口等待真实 Execution 结果，不复制领域状态。 |
| dialogue.Runner | 公共材料加载、路线选择、登记后准备与用户保存、Outcome 收敛和回复提交；不结束跨轮 Execution。 |
| chat.Loop / preparedLoop | 保存每轮 Chat messages、tools、usage 及摘要标记，组织历史、模型／工具循环、pending 和接管后刷新。 |
| dialogue.MessageStore / ToolExecutor | 公共用户、pending、工具 transcript 写入及 ToolRun 接入；工具等待与状态继续归原有领域服务。 |
| chat.Caller | Chat 请求生成、流消费与视觉回退，接收固定模型选择快照。 |
| dialogue.CallProcessor | 共同请求／响应 Hook、工具过滤、媒体中心调用和模型事实；媒体持有／解析／释放由 media.Manager 实现。 |
| confirmationCoordinator | 处理风险确认响应、等待及结果转换，拥有自动确认记录与锁；等待对象仍归 Turn，超时策略与追加确认、Session 过期共用。 |
| toolRunDeps | 直接组合工具执行所需服务、确认组件和执行视图，登记工具子请求，同步写工具调用记录和发现状态。 |
| identityResolver | 拥有入口默认身份和安全策略，统一 Actor／Scope／CLI 判断；无来源 Hook 使用不带入口默认值的来源身份解析。 |
| hookBridge | 使用 Hook manager/router、Request、身份和 Media，负责事件补全、可改写 Hook、continuation、请求观察和错误链路；失败日志与提示发布为事实。 |
| statusRecorder | 同步校验执行归属、合并和保存快照，拒绝旧 attempt，发布带单调版本的 StatusChanged；Agent 查询仍同步读取本地快照。 |
| outputSender | 使用共享 Dispatcher、通知服务和 hookBridge，处理普通／流式输出、发送 Hook、preview、notice 和 reasoning；向通知规则提供窄 SendAssistant 能力。 |
| dialogue.ReplyCommitter | 消费消息仓库、输出准备接口和本轮 Output，处理最终 Hook、空回复、延迟 outputs、发送／落库顺序、部分成功及回执关联。 |
| dialogue.ExecutionView / executionTurnOutput | 读取已有 Execution 的接管身份、刷新 Session，并选择前后台输出；保留原请求取消链，接管时清除后台路由、模型和 sandbox 覆盖。 |
| toolRunPromptProvider | 直接使用 ToolRun 和 identityResolver 查询 schema 与工具名。 |

Agent 只保存对外能力所需的组件引用和信号集合；消息、后台、压缩、接管、文件命令、Scope、状态查询和 Hook 入口均为薄委托。行为由 `Config`、必需服务由 `Dependencies` 在构造时注入，Agent 不提供运行时 setter，不保留共享服务、Prompt 材料或构造中间组件的副本。工具运行配置和确认超时策略由实际消费者共享，输入／后台预加载和补全直接使用 Registry、Preloader；必要诊断与入口审计的 Logger 也直接注入所属组件。

追加确认通过 Turn 的 `AppendWait` 固定本次等待对象；取消或过期只处理该对象，不能移除已恢复的执行或后续确认。executionCoordinator 的 `appendWaitLifecycle` 保留原 context 的来源／binding，取消链由 `New` 的必传 runtime context 提供；不受单次请求结束影响，无过期时限的等待也纳入关闭。Agent 的 `Close(ctx)`／`Done()` 为薄委托，app 在释放依赖前按共享预算等待定时任务和过期提示实际退出；未退出则保留 Hook 等依赖。独立装配也必须提供生命周期 context，关闭时使用同一 Close／Done 约定。

`Agent.Signals()` 暴露输入、模型、工具、确认、拒绝、持久化失败、超时、状态、提示及回复事实；app 的日志订阅保留既有字段、级别和记录次数，消费者错误不改变核心结果。关键提交、Usage、工具记录、Hook 改写结果和取消仍直接处理。

状态展示由 app 按 Session、原来源和实际展示目标保存最新版本，后台 worker 合并发送；同一用户的不同远程 CLI 连接仍是不同目标。发送期间的新状态会再次调度，最后 done/error 不依赖后续事件唤醒、不因队列容量丢失；后台只记录不展示，绑定失效时清理积压。视觉降级去重由通知规则按 Session 管理。

<!-- locator:protocol-routing -->
## 协议路线与压缩分派

- `llm.ProtocolID` 与 Session 的 chat/work/background 模式独立。`Selection` 只固定 Provider、Model、Client；modelmgr 提供从启动配置取得的不可变 Origin 描述，Agent 装配时按 api_mode 校验客户端满足路线所需私有接口。
- Chat Completions 与 Responses 客户端分别位于 llm/chatcompletions、llm/responses；各自编码原生请求和消费流。公共 llm.Client 只提供模型列表及独立 GenerateText，公共消息／工具定义不包含协议 JSON 包装。
- httpclient 仅负责 HTTP、显式代理、可取消重试、SSE 分帧及超时；协议包负责鉴权、API 错误和成功终态。断流不重放请求，Extra 只补充字段，已有字段、受控名称或不同层级重名在发送前拒绝。
- 命名与 Chat 文字摘要使用 GenerateText，可选择任一已配置客户端；Responses 独立调用使用 store=false，不续接主会话或执行工具。当前仅登记 Chat 业务 Loop／Compactor；缺失主对话能力时在自动压缩、保存输入和请求前拒绝。
- `agent/routes.Registry` 按 provider 保存 Binding{Origin, Client, Loop, Compactor}，另登记源协议 Compactor。客户端必需，Loop／Compactor 可缺失；重复绑定、空客户端、未登记的压缩接线、封闭前查询及封闭后修改明确报错。同路线共享无 provider 状态的业务组件，能力查询分别使用目标 provider 或源 Origin。
- dialogue 只依赖 LoopResolver，contextmgr 只依赖 CompactorResolver；注册表依赖公共契约，公共层不导入注册表实现、Agent 根包或具体路线。
- app 在创建上下文服务前建立注册表，Agent 装配内部路线并封闭注册表，验证 Chat 压缩接线成功后返回。注册表不保存执行、会话或工具状态，不提供完整依赖容器。
- `/compact` 与自动阈值继续进入 executionCoordinator，公共 contextmgr.Compact 只从源 Session 的 llm_origin 查询 Compactor，不用摘要目标或当前配置猜来源；旧 provider 删除或配置改变时仍可使用已登记的源协议能力。Chat 私有实现负责历史筛选、摘要提示、模型选择和 seed 准备。公共层负责命名信息与统计，执行／Session 负责创建、继承、保存和交接。
- Chat 保留当前文字摘要与 seed 格式、成功保存后消费 seed 的语义。Responses 原生记录、seed、兼容检查和恢复仍属后续步骤，不能视为已接入。

Responses provider 当前只绑定独立文本客户端，主对话能力缺失时在自动压缩、输入保存和模型请求前拒绝。后续业务接入见 [阶段 16](core-refactor.md#phase-16)。

<!-- locator:commands -->
## 命令链路

Slash 命令链路：

1. `internal/agent/command_runtime.go` 的命令执行器识别命令前缀，并统一处理权限、Turn 冲突和用户通知。
2. `internal/command/router.go` 负责解析命令名、alias、参数文本和分发。
3. app 将 `internal/command/builtin/` 的模块注册到共享 Router。
4. 命令通过 deps 直接访问 Session、模型、上下文、Hook、工具 Registry／Skill Manager、日志 Reader、文件及请求管理服务。Session 列表编号按 Scope 保存在命令模块的展示状态中。
5. 命令可通过 `command.Result.Continuation` 请求在指定 Session 中继续处理一条普通输入；commandExecutor 直接交给 inputCoordinator，模式切换、历史限制等策略由 Session 服务完成。
6. 平台补全统一使用结构化 completion 服务，组合命令名、命令参数、风险确认、fork message ID 和 `@tool:` 候选；CLI 本地文件补全仍优先。

约定：

- 新命令优先做成 `internal/command/builtin/` 模块。
- 手动压缩、Scope 解析、运行状态查询和文件提交准入通过窄接口／回调接入 Agent，不将 Agent 作为领域服务转发器。
- 会改变或切换 Session 的命令必须声明 `command.Info.SessionEffect`，命令执行器据此处理压缩和 pending 确认冲突，不维护命令名白名单。
- `/stop` 的请求编号、ID 及补全对普通用户只使用当前 Session；超级管理员保留全局管理。取消前在 Session 准入内复核原绑定和目标请求，切离再恢复也不能复用旧绑定。补全 Router 将解析后的 Actor 传给命令参数补全。
- Session 规则放在 `session.Service`；命令只解析参数和格式化结果，消息、命令与输入组件负责各自的编排。
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
2. Chat 路线进入工具阶段，通过公共 ToolExecutor 调用既有 ToolRun，由 toolRunDeps 登记工具子请求并记录实际调用。
3. prepared Hook 只能改写 arguments；ToolRun 用最终参数做工具视图、命名解析、foreground-only 过滤、权限和风险确认，并把同一参数回灌当前 assistant tool call。
4. Tool Runtime 执行具体工具，并按 Actor/Policy 做风险兜底校验。
5. 已进入实际执行阶段的工具结果以 text/image segments 通过完成 Hook；工具发现状态完成提交后，再统一记录最终结果并写入 transcript；纯文本只存 `content`，多模态结果额外存 `segments`。执行前失败或拒绝不触发完成 Hook。
6. 如果工具有输出意图，交给 Output Manager 发送，而不是工具直接发平台消息。

关键约定：

- 风险等级用于内部权限和确认，不暴露给 LLM。
- 单次工具调用的预检、确认详情和实际执行沿用派生的工具 context；编辑固定参数、解析路径、实际目标、存在状态及内容 revision，撤销另外固定备份编号。执行时原绑定失效、目标或内容变化即拒绝；workspace 本身不维护变更版本，绝对路径不变或切回后状态一致可继续。
- 风险确认先在 Turn 锁内校验 attempt 并登记状态、响应通道和刷新通道，再在锁外发布等待事实及发送提示；提示发送期间的响应保存在通道中。等待对象固定 Turn 与通道身份，取消或发送失败只清理该对象，迟到清理不影响后续确认。
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
- hookBridge 同步执行 Hook 及 Request 观察，保留派生 context 和结束清理；app 安装唤起判断与观察接口。唤醒前缀处理是显式接收 context 的包内函数。
- Hook 来源优先保留事件显式字段，缺失字段从公共 Info 和显式安全 Actor 补齐，不依赖平台扩展。平台连接等没有聊天来源的事件及其错误 Hook 保留空 Scope／Actor，不填充默认 CLI 身份。
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
- `notification.Manager` 消费通知意图，复用 Router；`notification/rules` 维护平台连接、插件／Hook、模型和执行错误的通知规则与文案。业务模块决定触发时机，outputSender 经 hookBridge 完成发送 Hook，对话主流程负责最终提交。

约定：

- 业务层返回输出意图，不直接调用平台 adapter。
- 平台 adapter 负责把平台无关输出转换成平台 API。
- app 在 Hook 注册及 Agent 创建前装配共享 Router 和通知管理器；Hook／脚本、Cron、Elnis 直接使用 Router，不经 Agent 宿主发送闭包。无来源启动告警在交互模式显示于本地 CLI，在 service 模式记录实际告警内容，不广播管理员。
- 显式目标优先；无显式目标时使用原消息发送器覆盖或 Info 的平台来源。后台丢弃发送器仍有效；原消息信息随任务保存，实际发送使用任务自身的 context，不查询当前 Session 重建旧目标。
- 通知意图携带原 Info、原 Sender 覆盖和按需提供的 Session Binding；过期绑定或取消 context 拒绝发送。同步调用等待实际回执；app 为模型重试、视觉进度及 Hook 失败事实安装旁路消费者，过程提示 FollowEmit，失败事实 FollowExecutor，均使用 CancelPending。请求正常结束不取消已入队的失败事实；消费者复核原绑定与目标，不另建可靠投递中间件。
- Router 在发送前把 URL/Path/Data 归一为 MediaID，发送副本经 `ResolveForOutput` 临时解析，并释放临时导出。回执按实际成功的输出索引建立媒体关联；多目标 scope 由 adapter 明确提供，缓存期限复用 sandbox retention，非正值不缓存。
- 部分失败同时返回成功 Receipt 与 error；Agent／Cron／Elnis 关联已成功的平台消息，错误仍返回，任务不会因此整体成功。缓存失败只记录日志，不重发平台消息；通知发送失败不再触发通知。
- Agent 的 assistant 消息及 Cron／Elnis 的报告关联使用 `Receipt.SentMessages` 提供的实际平台、ScopeID 和消息 ID；不根据触发来源或目标类型自行拼 Scope，缺少完整来源的回执不建立关联。任务指定目标与触发消息的 Info 分别保留各自语义。
- QQ OneBot 把 record 输出转换为原生语音段；暂不支持 record 的平台使用统一文字 fallback。
- QQ OneBot 纯文本发送超过 3000 个 Unicode 字符时按原文分节点，单次调用群聊／私聊 forward API；回复、显式目标、管理员通知和临时连接共用该规则。回执只关联外层 `message_id`，不关联转发资源 ID 或节点；失败不退回分条发送。
- 前后台 turn 输出适配器只依赖 outputSender 和 statusRecorder；接管输出另外使用 executionView 与 Session 工作目录能力。状态始终先同步记录再发布，展示由 app 合并调度；实际回复发送、落库及关联完成后才发布对应观察事实。

<!-- locator:platform -->
## 平台适配层

平台层负责输入归一化和输出落地。

输入侧：

- 解析 Actor、Scope、发送目标、群身份、引用、多模态消息段和平台 metadata。
- 原始有序 segments 写入 Chat History，包括纯媒体消息；过滤 base64、临时本地路径和 token/签名 URL，不保证来源永久有效。
- Agent 统一判断 wakeup，并只读检查 waiting Hook route；仅唤起或 waiting continuation 时物化媒体，普通观察 Hook 不下载。
- Telegram resolver 内使用 token URL和代理，OneBot 按需 get_image/get_file；QQ Official 的事件 URL直接由 Media Center 导入，不引入额外 resolver 层。
- OneBot 普通输入、引用的平台兜底和转发节点共用适配器内部转换层，统一协议类型、媒体字段和占位文案，直接生成 `platform.MessageSegment`，转换期间不获取资源。普通输入保留文件段并提取提及、回复信息；转发节点保留原始文字和图片，其余类型显示占位，不产生当前消息的提及或回复信息。转发资源获取与节点组织独立于类型转换，输出继续通过 `delivery.Output` 编码为 OneBot 消息段。
- `refcontext.Options.Enrich` 在来源恢复及已有内容确定后提供可选的有序引用展示段，默认不改变其他平台行为。QQ OneBot 私聊直接收到 forward 时从事件取得资源 ID，通过 `get_forward_msg` 展开；显式引用时先通过 `get_msg` 恢复原消息，再共用同一展开流程。群聊未引用的 forward 和 Chat History 仅保留 `[forward]`。展开限一层，以 `<forward_message>` 标签包裹，图片沿用正常媒体链路，其他类型及嵌套 forward 使用占位。展示正文不包含协议消息 ID；工具通用的历史行头仍可使用外层消息 ID。
- 引用的 `DisplaySegments` 保留节点内图文顺序，适配器在其后附加当前输入；私聊当前消息的展开内容也只写入 `ContextSegments`／`ContextText`。原始 `Segments` 和 `Reply.Segments` 保留来源媒体位置，展开内部图片不登记为原消息的顶层历史媒体索引。Agent 去除唤醒词或工具指令时保留未改变的文字段和图片位置；存储和厂商请求继续使用有序 segments。
- 引用按输出索引 → Chat History → 平台能力恢复有序媒体，图片进入视觉输入，Session 仅保存稳定媒体 ID 与文本投影。同一 actor、平台和 scope 的最后一条 assistant 显式设置 `ResumeSessionID`，使 TTL 清理或 `/new` 清除 current 后仍恢复来源 Session，且不重复注入引用内容；较早 assistant 设置 `ForkFromMessageID` 并保留引用媒体。后台 Resume 和其他用户或 scope 的普通引用规则保持独立。

输出侧：

- 实现统一 `SendChat` / `SendNotice`。
- QQ OneBot、QQ Official 和 Telegram 的成功发送同时返回平台消息 ID 和结构化 `SentMessages`（platform/scope/message ID/output indexes），覆盖文本、回复、分页及 Telegram 流式完成。部分失败保留成功项及实际目标来源；output indexes 只关联实际发送的媒体，文本降级仍有消息回执但不关联媒体。CLI 保持空回执。
- Telegram 流式分页失败返回已发送页面的回执和错误，不因后续页失败而重新发送整段文本。
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

`llm_origin` 在 Session metadata 独立保存首次主对话准入的 protocol、provider、去除末尾斜线的 base_url，不含模型或凭据；RegisterOrigin 受原 Binding／Session 准入和取消保护，保存失败不继续调用。已有完整归属不随切模型改写，协议不匹配拒绝；压缩、Fork、后台复制继承源归属，独立创建保持未定。旧数据库一次迁移只标记已有 Chat 会话的 protocol，下一次实际 Chat 准入补全 provider／base_url，不伪造历史来源。

`PrepareBackground` 创建或复用后台会话，在同一 `Session.Mode` 字段固定 `background`，管理标题及后台身份 metadata，不激活前台 current；复用时保留其他模块字段，拒绝已被前台接管的会话。首次后台工具状态由调用方另交 StateService 提交。

`CopyBackground` 在来源 Session 准入内复核后台状态，复用后台创建规则并复制历史、清除旧消息引用。Cron 只决定目标归属和业务 metadata；Session 统一设置后台模式与命名标记，副本不改变前台 current，不继承工具或执行状态。来源已被接管时拒绝复制。

命名任务由 Session 的 `StartNaming`、`Close`、`Done` 管理，app 注入应用生命周期并等待实际退出。关闭后不接收新命名，准备阶段和在途生成均纳入退出等待；Turn 结束不取消命名，应用取消后的迟到结果不写标题、不执行 fallback、不报告命名失败。`NamingSignals()` 发布 Scheduled／Completed／Failed，结果携带实际调用的 provider／model，包括 fallback 目标；app 在命名启动前接入有界背压日志消费者。独立文本不改主对话 Model 或 Session 原生归属，标题更新与命名触发仍直接执行。

`CreateCompacted` 接收来源 Session ID、预分配的新 ID、标题与已准备的 metadata，在准入内复核来源及前台原绑定，继承归属和模式，统一设置命名字段并保存新会话。前台更新 current，后台不创建前台绑定；保存失败不改变绑定。Session 不依赖 contextmgr，摘要、seed、代数及压缩标题材料仍归上下文服务，执行交接仍归 Agent。Fork 保留来源模式。

绑定变化发布 `BindingChanged{Old, New, Reason}`，覆盖创建、恢复、Fork、重置、删除、过期和记录缺失导致的 current 变化，不发布一般字段或持久化增删事件。删除会失效所有指向该记录的绑定。信号在状态与准入锁释放后发出，允许回调重入。app 持有独立撤销清理队列及订阅，以 `FollowExecutor + CancelPending` 清理指定旧绑定；队列延迟不影响同步失效，关闭沿用共享 30 秒预算。维护任务复用运行中的 Session 服务。

短准入按 Scope → 排序后的 SessionID → 状态锁取得。Scope 保护 current 解析与切换，SessionID 协调 Turn 启动、停止、交接、删除和清理；LLM、工具、Hook、发送和信号回调均在锁外。Session 通过注入的只读执行状态判断忙闲，当前非 idle 时禁止切离，显式删除拒绝执行中的 Session，维护清理跳过忙碌项并在条件删除时复核归档、置顶和时间。不同 Scope 不共用全局准入锁。

后台 Session 只向所属用户的同平台私聊及 CLI 管理入口开放，群聊、频道和未知类型不可列出或直接恢复。首次恢复在原子更新中将归属永久改为前台 Scope、模式改为 `work`，记录 `foreground_origin` 并清除活动后台身份；保留历史、缓存和 workspace，切走或重启不会恢复后台身份。运行中的目标可被空闲前台接管，原 Execution 同步取得前台身份、绑定和输出目标；已发出的请求与工具不重启，后续请求使用 work 模型及前台工具、确认规则。

最后一轮 LLM 返回后，最终输出和 Turn 完成 Hook 前再次刷新 Execution 来源与输出策略；等待响应期间发生的前台接管同样使用前台 Scope／Actor，并保留原执行的取消链。

接管后的原后台任务等待该逻辑执行的最终完成、取消或失败。Cron／Elnis 保存实际 RunID、消息和结果并标记接管，不将其直接算作任务成功；停止 JSON 修正、自动汇报与未开始的补投递。再次使用已接管 SessionID 不会重新设置后台身份，独立定时触发仍创建新后台 Session。

Session 命令的分页选择和维护配置由 `SessionCommandState` 按 Scope 保存。闲置 TTL 按会话类型与角色选择；过期和 `/new` 只清除 current，下一条普通输入才创建记录。恢复刷新活跃时间，Fork 上下文由 Session／Storage 处理。

<!-- locator:llm -->
## 模型服务

- app 构造共享 `modelmgr.Service`，注入 Agent、模型命令及 Elnis 槽位解析。服务唯一持有模式／槽位、compact、naming 选择，provider 客户端和模型目录缓存；不依赖 Agent、Session 或命令包。
- 命令用 Session／Scope 确定当前模式，模型匹配和切换由服务执行。目录按 provider 并行查询，缓存模型与错误，显式刷新；配置模型始终参与合并，编号在筛选前统一分配。目录结果和选择状态以独立快照交付。
- 切换串行构建候选状态，调用 `config.SaveState` 原子替换状态文件后再发布内存状态；失败保留旧选择。写盘不持有状态读锁，读取方继续使用旧快照。状态文件保留原有字段及默认 Session 模式；未配置路径的独立实例仅更新内存。
- `Selection` 固定 provider、模型和客户端。对话固定本次 Turn 选择；压缩固定专用选择或本次对话 fallback；命名同时固定专用选择及 work fallback。Turn／Request Prepared Hook 的 provider/model 只读，Go Handler 的相关修改不回写模型快照；当前消息仍按各 Hook 点的原契约修改。前台接管保留明确的重新选择边界。
- `background` 只是 Session 模式，没有对应模型槽位。默认后台选择 work 模型；Elnis 保留 elwisp1/2/3 槽位及缺省回退 work。Cron 任务可显式指定 provider/model，由共享 modelmgr 校验，不改变全局选择。
- 标题生成与压缩调度留在原模块，不保存独立模型选择。modelmgr 在共享客户端入口统一发布 `ModelRetrying`，覆盖对话、压缩和命名；app 经独立队列接入 `notification/rules.ModelRetry`，保留单次调用的取消和原来源，过期重试不再提示。客户端配置在启动后保持不变。

<!-- locator:context -->
## 上下文管理

app 创建共享 `contextmgr.Service`，注入 Agent；服务不持有 Request／Turn 管理器。上下文管理负责：

- 加载历史消息和 Fork 上下文。
- 解析 context window。
- 按当前模型的 context window 动态判断压缩阈值。
- 格式化厂商 usage 状态。
- 管理 `last_usage`、`context_compact` 的编解码和字段更新，准备压缩材料与结果。executionCoordinator 负责压缩准入、Request／Turn、取消和新 Session 交接。

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
- 第 18 版 migration 为已有 Session 的缺失 llm_origin 写入 protocol=chat，保留其他 metadata 及已有归属；损坏或非对象 metadata 使整次迁移回滚。新创建的会话不受已执行迁移影响。
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
