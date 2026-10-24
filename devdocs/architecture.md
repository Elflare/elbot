# ElBot 架构说明

本文说明当前模块职责、调用链和关键约束；文件入口见[代码地图](code-map.md)。

## 定位方法

按[代码地图的定位方法](code-map.md#定位方法) 搜索 locator，只读对应章节。

<!-- locator:startup -->
## 启动与装配

App 按下层接口已存在来实现公共流程，下层按照调用契约实现。Agent 已接入真实总管，配置独占已实现；其他四条线、媒体和模型构造入口、平台客户端接口及进程入口适配仍待完成。完整应用的编译与运行验证需待这些接口接入。原 13 个生产文件和 12 个测试文件保存在同目录的 `*.go.bak` 中，不参与构建。

六文件职责及接口契约见 [App 包设计](app-design.md)。App 加载配置、构造共享资源和各线总管、交付公开能力并协调启停。媒体和模型服务接收完整配置，自行处理后端、凭据、协议客户端、请求参数及恢复／重载；App 不拆解这些初始化细节。共享存储归 App，平台聊天历史及其存储归 Platform；部分失败交回已取得对象统一清理，订阅由接收方持有和释放。权限管理使用 `security.Permission`，App 与 Agent 不承接 Doctor。

TUI 和 Service 在创建资源前，按配置文件的绝对路径及符号链接解析结果取得独立 `.lock` 文件的非阻塞独占锁；同配置重复启动立即报错，不同配置分别运行，纯客户端不取锁。入口持有锁，统一退出流程在资源关闭后释放；锁文件保留，退出超时则由进程退出释放锁。

入口记录启动总耗时和四阶段 debug 统计，按 Tool → Agent → Hook → Platform → Background 启动。Platform.Wait 汇总运行结果，正常停止返回 nil，独立故障和等待超时返回错误。退出先停止新工作、等待使用方收尾，再释放依赖；平台发送与历史查询能力保留到使用方退出。

退出步骤共用独立的 30 秒预算。App 的 `runShutdownStep` 将结果分为完成、失败、超时，`runShutdownSteps` 按序执行并汇总；普通错误保留后继续清理，统一期限耗尽立即停止后续清理。下层 Close 保持返回 error，其业务超时错误不直接等同于 App 的统一期限耗尽。最终错误交回进程入口处理。

客户端通过 `platform.RunClient(ctx, cfg, name)` 一次获得 `ClientResult{Kind, Err}`。Platform 负责连接选择与结果分类；App 的明确运行入口直接返回错误，自动尝试入口只将 `ClientFallback` 转为回退信号。App 不解析 URL 或重新组合回退条件，实际本地启动由进程入口决定。

```mermaid
flowchart TB
    E["app/entry.go<br/>入口、计时、启动与等待"]
    ENV["app/environment.go<br/>运行模式、配置路径与配置锁"]
    R["app/resources.go<br/>共享资源构造与所有权"]
    W["app/wiring.go<br/>总管构造与公开能力接线"]
    S["app/shutdown.go<br/>统一期限、结果分类与清理顺序"]
    C["app/client.go<br/>结果分派与回退信号"]
    E --> ENV & R & W & S
    ENV -->|配置路径| R
    R --> M["媒体与模型服务<br/>接收完整配置，自行初始化"]
    C --> P["Platform.RunClient<br/>连接选择、运行与结果分类"]
    S -.->|最后释放配置锁| ENV
```

<!-- locator:contextinfo -->
## 公共上下文事实

`contextinfo` 定义四组事实，getter 返回快照和存在标记，缺失不补全局默认值。

| 分组 | 内容与来源 |
|---|---|
| Conversation | 平台会话、发送者、消息／回复 ID 与平台私有扩展，由入口提供 |
| Actor | ElBot 身份、角色及平台群角色，由 security 解析 |
| Execution | Session／当前、父、根 Request／Run／Attempt ID，由所属服务建立关联 |
| Model | 固定 Selection 及其 Origin 的 provider、model、protocol |

- 平台会话与 ElBot Session 分开表达；事实不替代权限、binding 或 attempt 校验。公共包不持有凭据、服务或原生载荷。
- MessageContext 安装公共 Conversation，私有 context 保存正文和 Sender；PlatformData 是平台提供的不可变快照。
- 接管替换 Conversation、Actor、Model 与平台来源，清除新来源缺失的事实，保留执行取消链。

<!-- locator:signal -->
## 信号与订阅

来源拥有业务实例信号；关键提交、权限、Hook、用量和工具记录同步完成，信号供事实观察。Dialogue 管通知订阅，CLI 管展示订阅，命名和模型重试由来源维护同步日志连接。

- `Signal[T,R]` 使用原子订阅快照；Emit 等待实际结果，EmitAsync 返回入队错误与可等待结果，回执按订阅顺序排列。Observe 用于无业务返回值的消费者。
- 每个信号默认 8 个执行槽、256 个等待项、30 秒总期限；按 key 串行，满时拒绝。超时取消调用，但回调实际退出前保留执行槽和 key。
- Disconnect 不撤销已取得的快照；Connection.Close 等待已接纳回调退出。信号关闭选择 CancelPending 或 Drain，Done 表示真实退出。
- 平台直接发布全局 PlatformConnected；消费者自行订阅、按平台排队并管理关闭，Agent 不代持 Hook 的平台消费者。事件不缓存或重放，重复连接交业务处理。
- `events.EmitLog` 校验九字段 LogRecord 并复制显式 Identity；发布解除请求取消，身份不从 context 补值。来源决定等级、结果和唯一正文；业务 error、Receipt 与 LogResult 独立传递，nil error 不覆盖日志失败，取消不覆盖独立故障，日志结果不改变重试或业务判断。
- 日志中心接收时记时，以 runtime／audit／elnis 三个容量 256 的队列背压写入，入队等待最多 500 毫秒，关闭排空。text 默认限 256 个 Unicode 字符，JSON 保留原生字段顺序和数值；JSONL 含行尾最多 64 KiB。
- 设施故障直接报告 stderr；短写按 io.ErrShortWrite 处理，后续记录继续。关闭超时保留仍在使用的文件和活动中心名额。
- Reader 按日期与写入位置倒序、有界分块查询，兼读 JSONL 和历史文本；公共字段与正文条件匹配后计数，坏行单独报告。工具来源按固定 run_source 查询，不从正文猜测；用量统计保留整数精度，未知或溢出只影响相关项。

<!-- locator:config -->
## 配置与运行数据

- 配置查找顺序为 `--config → ELBOT_CONFIG_FILE → 平台配置目录`；完整配置见[配置文档](../docs/configuration.md)。
- app.toml 是主配置，同目录 providers.toml 定义客户端；api_mode 为 chat／response，独立于 Session 的 chat／work／background 模式。
- state.toml 持久化命令修改的模型状态，不自动重读外部修改；tool_tags.toml 保存工具标签。记忆、Skill 和插件资产位于配置目录。
- api_key_env 优先读进程环境，再读配置目录 .env。`/models --fresh`／`--refresh` 沿启动路径重读厂商、model_metadata 和密钥，不重读主配置或 state；准备失败保留原快照，远端目录失败按厂商报告。
- 模块声明配置要求，Load 与 Inspector 共用默认值、合并及校验；配置包不依赖具体业务模块。`/doctor` 仅超管可用，只读诊断配置和资产，不初始化文件或启动 runtime。

<!-- locator:agent-chat -->
## Agent 对话链路

`平台输入 → 唤醒／入站 Hook → 命令或输入准备 → 执行准入 → 单轮准备 → 模型／工具循环 → 回复提交 → 跨轮收尾`。

| 层 | 职责 |
|---|---|
| 入口组件 | agent/input 的 MessageHandler 分发，InputCoordinator 准备与预加载，CommandExecutor 执行命令；background/module 的 BackgroundRunner 准备后台资源 |
| ExecutionCoordinator | 准入、attempt／Request、追加确认、pending、压缩交接与 Execution 完成 |
| dialogue.Runner | 选择路线、加载材料、准备输入、收敛 Loop 结果、提交回复与统一失败报告 |
| Chat／Responses Loop | 协议上下文、模型／工具循环、pending 与接管刷新 |
| 公共协作 | Prompt、模型 Hook、ToolRun、消息事务与 ReplyCommitter |

- 准入检查客户端能力和模型兼容性，固定 Selection 并登记会话归属及 attempt；后续准备使用 Request context。普通模型切换不影响在途循环，前台接管刷新模型、工具和来源。
- 工具阶段的输入进入 pending，在下次模型请求前合并；最终模型调用期间收到的 pending 在本轮结束后开启下一轮。
- 用户／pending 和工具记录通过共同提交口保存。ToolBatch 初始化时按模型调用顺序保存 assistant 调用数组、稳定结果 ID 和未知结果槽；最终结果逐项更新原槽，与统计、媒体引用、发现状态同事务提交。参数媒体引用在初始化时按批次去重；Chat 参数变化时，随对应结果补入新增引用与最终参数。全部完成后才续跑模型。
- CommitGate 在事务内复核 binding 与活跃 Execution／attempt；Responses 同时比较 checkpoint，提交原生状态。API 审计独立保存。
- 回复前刷新接管来源；Loop 和回复提交成功后，RecordTurn 一次合并保存活动时间与用量，再处理状态、压缩、pending、Execution 结果及命名。单轮 Runner 不结束跨轮 Execution。

### Agent 包边界

`Supervisor` 的业务出口为 `HandleMessage`、`RegisterHooks`、`StartHookRequest`、`Complete`、`Commands`、`ObserveStatusChanged`、`ObserveBindingChanged`；生命周期出口为 `Start`、`BeginClose`、`Close`、`Done`。内部组件不通过 getter 暴露。`Commands` 返回独立的命令元数据；状态订阅复制用量，绑定订阅只返回 Scope、SessionID 等绑定事实，每个订阅者获得独立快照。调用方只持有自己的订阅连接，不能发射或关闭源信号。

Agent 总管持有模型完成、状态变化、视觉降级三个线内事件信号。`BeginClose` 拒绝两类公开入口的新订阅，但保留信号供在途工作发布收尾事件。App 等待平台接入和后台生产者退出后调用 Agent 的 `Close`；`Close` 等待内部组件收尾，再关闭信号，沿用各订阅的关闭策略。`Done` 等待信号回调实际退出，单独调用 `BeginClose` 不会完成 `Done`，`Close` 超时也不会提前完成它。外部消费者自行管理其异步工作的生命周期。

工具预加载和补全标签由 Tool 能力提供；执行 Schema 统一经工具执行能力生成，提示词只消费工具名称。Agent 不另设自定义 Schema Provider 或 Registry 回退。整链路测试通过真实 `NewSupervisor` 构造，子组件单测在各所属包中维护。

根包 `agent.Supervisor` 为 App 提供 Agent 业务线的构造与管理入口。`NewSupervisor(ctx, cfg, deps)` 在内部拆分配置；`SupervisorDeps` 只接收 Store、Models、Media、Permission、Logs 五项共享资源，以及 Tool、Hook、Platform 三类业务能力。`deps.go` 在消费方声明 `ToolCapabilities`、`HookCapabilities`、`PlatformCapabilities`，提供方总管直接实现接口。Hook 能力只供管理命令使用，执行由提供方注册 Agent 自有回调。内部消费位置同样按所需操作依赖接口，不获取提供方的 Manager、Registry 等组件。Hook 统一注册一组 Points，各消费组件通过本地最小接口声明实际所需的回调；RunnerDeps 仅汇总 Preparer 与 CallProcessor 的能力用于装配，不要求输入、工具或输出回调。App 只传总管并调用接收方的 `ConnectAgent`，适配及订阅由接收方管理；其余四条线的总管实现仍待完成。根包持有 Session、Request、Turn、协议路线及订阅；具体业务放在子包，后台准备由 `background/module` 持有。`share` 提供线内共享定义与辅助能力，不反向依赖 input、execution、dialogue、confirmation、hooks、output 或 tools。

```mermaid
flowchart TD
    A["app/wiring.go<br/>总管构造与跨线能力接线"] --> S["agent/supervisor.go<br/>Agent 总管与公开能力"]
    A --> DEP["agent/deps.go<br/>共享资源和三类消费方能力接口"]
    DEP --> W
    S --> W["agent/wiring.go<br/>拆分配置、组件构造与线内接线"]
    S --> L["agent/lifecycle.go<br/>启动、取消与关闭"]
    S --> G["agent/signals.go<br/>连接、消费与释放订阅"]
    W --> I["agent/input/<br/>消息、命令、补全与输入准备"]
    W --> E["agent/execution/<br/>准入、跨轮执行、状态与输出生命周期"]
    W --> C["agent/confirmation/<br/>风险策略、确认与提示"]
    W --> H["agent/hooks/hooks.go<br/>单组回调调度与在途生命周期"]
    S --> HB["agent/hook_bridge.go<br/>注册入口与逐个 handler 的请求登记"]
    HB --> H
    H --> HP["agent/hooks/points.go<br/>可挂接点与业务输入、结果契约"]
    W --> O["agent/output/<br/>发送、输出 Hook 与回执"]
    W --> T["agent/tools/<br/>工具运行时与 ToolRun 桥接"]
    W --> D["agent/dialogue/、chat/、responses/、routes/<br/>公共单轮与协议路线"]
    I & E & C & H & O & T --> R["agent/share/<br/>身份、输入、片段、绑定与共享契约"]
    B["background/module/runner.go、runner_deps.go<br/>后台准备与依赖契约"] --> R
```

<!-- locator:protocol-routing -->
## 协议路线与原生上下文

- llm/chatcompletions 与 llm/responses 编解码原生协议，httpclient 提供传输；agent/chat 与 agent/responses 各自实现 Loop、Compactor 和材料准备。
- 公共 llm.Client 提供模型列表与独立 GenerateText，独立文本不续主会话或执行工具。Selection 固定客户端、模型、Origin 与窗口快照。
- routes.Registry 按 APIType 登记 Loop／Compactor／Material；主对话按 Selection.Origin 选路线，压缩和分支按持久化源 Origin 选能力。
- CanSwitch 纯比较来源：Chat 可跨 Chat provider；Responses 可在同一 provider 节点内换模型；跨协议或涉及 Responses 的跨 provider 切换拒绝。命令、执行与接管分别在自己的准入边界检查。
- provider 节点名表示厂商身份，URL 变化按已有材料重放；Session 和原生记录保留历史来源。
- Usage 四项计数为可空 int64：nil 未知，零是明确零，不推算缺失总量；快照复制指针，非法用量不使响应失败，Chat 非法或 null 别名继续回退。

### 主调用链

```mermaid
flowchart LR
    E["agent/dialogue/runner.go、contextmgr/service.go、session/fork.go<br/>对话、压缩与分支入口"] --> R["agent/routes/registry.go<br/>按协议查询业务能力"]
    E --> M["modelmgr/selection.go、providers.go<br/>固定选择与厂商快照"]
    R --> C["agent/chat/<br/>Chat 循环、摘要与材料"]
    R --> N["agent/responses/<br/>原生循环、压缩与材料"]
    C --> CC["llm/chatcompletions/<br/>Chat 协议客户端"]
    N --> NC["llm/responses/<br/>Responses 协议客户端"]
    CC & NC --> H["llm/httpclient/<br/>HTTP、SSE、代理与超时"]
```

### 公共协作与事件

Chat／Responses 共用 Dialogue 的 Prompt、Hook、ToolRun、媒体与提交能力，协议私有状态留在各自路线。CallObservation 在厂商响应终止时发布原始 usage、正文摘要和完成事实，先于 response Hook；每次实际请求记录一次 llm_usage，准备失败不计调用，恢复请求分别结束观察。HTTP 重试单独发布重试事实，最终失败统一交 Dialogue。

### Responses

- 业务消息保存完整正文和 segments，原生记录保存 items、推理材料与 checkpoint。请求前归档参数和有序 input_refs；InputIDsJSON 仅含本次新消费输入，审计不保存展开后的 HTTP 正文。
- 请求显式获取 reasoning.encrypted_content，保留未知 items。失败、取消、incomplete 或提前 EOF 不推进 checkpoint；中断增量不成为完成响应或可执行工具调用。
- 每次构建 instructions；默认沿 previous_response_id 发送增量，store=false 时发送完整窗口且不带旧链 ID。主对话默认 store=true，独立文本固定 false。
- base_url 改变或明确旧链失效且尚无新内容时，按已有材料重建；旧链失效只重试一次。网络、鉴权、含糊错误及部分流不触发恢复；重建复用已准备的输入、Hook 结果与 Selection，不重跑历史工具。缺推理或原生材料时拒绝，缺失媒体按公共媒体规则处理。
- 工具定义以 developer additional_tools 输入冻结、随历史回放；同名定义变化要求新会话。请求不发顶层 tools，当前权限与 tool_choice 决定允许集合，响应调用再次校验。
- Hook 不能改工具调用集合、ID、名称和参数；Chat 允许改参数。副作用前保存原生响应及整批固定结果槽；原生输出与展示结果逐项更新，最后一项的事务同时保存不可变快照。
- 工具批次的历史快照供 Fork 使用，不推进活动游标；下一次对话将中断批次一次封存，保留完成结果，其余标为结果未知，不自动重跑工具、完成 Hook 或发送。Fork／后台副本只使用封存批次。
- LoadNativeWindow 在同一读取事务取得 seed、checkpoint 祖先链及关联材料；窗口按已提交输入／输出重建。首个本地 checkpoint 成功才消费 seed，Fork 使用不可变快照；恢复不读取历史 HTTP 正文或重做工具副作用。

<!-- locator:commands -->
## 命令链路

`CommandExecutor → Router → builtin → 领域服务`。Dispatch 返回的命令错误由 CommandExecutor 统一发布一条 ERROR 通知，并保留原始错误；通知发往原会话且不绑定可能失效的旧 session。参数帮助归 command.Info.Help，补全使用 completion，CLI 本地文件补全优先；用户用法见[命令文档](../docs/commands.md)。

- 改变 Session 的命令声明 SessionEffect，执行器处理压缩／确认冲突，Session 服务判断绑定规则。
- 普通用户的请求查询与停止限定当前 Session，超管可管理全局；取消复核原 binding 和目标请求。列表选择按 Scope 隔离。
- 命令结果通过同步通知发送，成功后才交 InputCoordinator 执行 Continuation；发送清除旧 Session binding，保留原会话与连接。

<!-- locator:request-turn -->
## Request、Turn 与状态

- Request 拥有活动请求树、父子关系、取消、超时和清理；LLM、工具、压缩与后台请求均登记。取消或超时只进入 stopping，由执行方的 done 确认工作退出后移除请求。
- Turn 拥有阶段、pending、确认与工具计数；Execution 拥有跨确认／压缩的 RunID、接管快照和一次性结果，attempt 隔离迟到完成。stopping 保留执行归属，AttemptDone 在实际收尾后关闭；输入在会话锁外等待，重新核验 binding 后准入，不重复输入准备。追加确认的续跑同样等待旧 attempt 退出。续接间隙的 pending 仍保持忙碌。
- 首次启动竞争时，AcceptInput 只对尚未执行的输入重新准入，保留 binding 与准备结果，不重跑 Hook；后续执行错误不触发输入重放。
- 风险确认先在锁内登记等待对象，再锁外发提示；响应、取消及迟到清理固定同一等待对象。追加确认等待和过期提示受应用生命周期管理。
- StatusRecorder 同步校验执行归属、保存并发布单调版本；CLI 按 Session 与原始展示目标合并状态，绑定失效清理积压。后台无 CLI 展示目标，接管仅展示之后的状态与增量。
- 当前 usage 唯一由 StatusRecorder 持有；CLI、`/status`、压缩判断和轮末持久化均读此处。首次从已加载的 Session 元数据恢复，厂商返回后立即更新，nil usage 保留已知值；contextmgr 仅负责轮末持久化。工具统计分别显示已持久化累计与当前 Turn，不相加。

<!-- locator:tool-flow -->
## 工具调用链路

`路线 → ToolBatch 初始化 → 顺序准备／确认 → 并发执行 → 按返回顺序处理并逐项提交 → 按原调用顺序续跑模型`。

```mermaid
flowchart LR
    R["toolrun/runner.go<br/>顺序准备、确认与完成队列"] --> W["tool/executor.go、toolrun/elwisp_exec.go<br/>并发执行与实际开始边界"]
    W --> R
    R --> C["agent/dialogue/commit.go、responses/persistence.go<br/>固定槽初始化、逐项提交与原生快照"]
    C --> S["storage/sqlite/tool_batch.go、dialogue_repository.go<br/>结果、统计、媒体和发现状态原子保存"]
    S --> F["session/fork.go、background_copy.go<br/>完整批次复制与 ID 重映射"]
    W --> P["processenv/environment.go<br/>环境快照与受管命令构造"]
    P --> U["processenv/process_unix.go<br/>独立进程组及整组终止"]
    P --> V["processenv/process_windows.go<br/>进程树终止及退出判定"]
```

- ToolRun 按 prepared Hook 后的参数顺序解析、授权、确认和登记，通过一个即启动一个 worker；协调器按返回顺序处理输出、完成 Hook 和最终结果，等待后续准备／确认时仍消费完成队列。共享 Session 状态只由协调器修改，准备与执行持有独立快照；Flush 仅汇总模型上下文，不再写库。
- N 个调用的正常批次有 1 次初始化事务和 N 次结果事务，最后一次同时封存批次与原生快照；不单独写 started 状态。此计数不含工具自身业务写入、模型请求归档、异常恢复和轮末收尾。原始返回至最终结果提交之间中断时，该项仍为未知。
- 单工具失败、拒绝或超时不停止其他调用；整轮取消或确认过期取消并等待所有 worker。完成 Hook 只处理实际执行的结果，Content／Segments 回灌模型，Data 供内部消费。
- 请求拥有的外部命令统一通过 processenv 配置终止行为：Unix 使用独立进程组，Windows 使用 taskkill /T /F；取消后等待命令和 stdout／stderr 收集结束。Skill、Shell、编译、一次性 Hook 和 ripgrep 共用此行为；常驻 Hook 的单次调用仍使用 event.cancel，关闭其进程时复用进程树终止。主动脱离进程组的守护进程和已提交给远端的动作不在取消保证内。
- Tool.Call 返回业务 Result、独立日志正文和原始 error。实际调用前发布 tool_call_start，返回后立即发布 tool_call_end，先于发送、Hook 与持久化；Hook 规则／插件对应 hook_tool_call_started 和 hook_tool_call。开始无结果，结束有摘要与耗时，共享调用 ID；审计为追加文件日志。调用前固定 run_source；已进入调用记 INFO／succeeded，业务错误保留在正文，调用统计保留原始执行结论。
- 参数日志限 512 rune，工具自行将摘要限为 1024 rune；日志不截断业务结果，也不把 Segments 自动转成日志正文。
- PrepareCall 查询 CallPreparer；PreparedCall 固定参数、确认详情与执行回调，仅由本次 Runner 局部持有。Executor 复核身份、可用性和权限后使用执行 context 调用。
- 文件编辑／撤销、长期记忆更新和 Skill 修改复用准备结果，提交前核对目标及 revision；AgentSkill 正文到执行时才读取。
- 每次准备及完成处理前应用 Execution 的内存接管快照，保留后续工具状态；本次调用权限与沙箱固定，输出发送时选择最新去向。发现状态提交成功后才更新快照，保存失败不重跑已完成动作。
- Chat 工具消息只发文本，工具图片派生为同批之后的 user 多模态输入，不持久化派生消息；Responses 编码原生工具结果。

<!-- locator:tool -->
## Tool Runtime、工具状态与文件

Runtime 拥有注册、schema、权限与执行器，ToolRun 拥有当前视图、路由与确认；StateService 解释工具状态，PreloadService 返回待提交状态、材料和语言需求。

- ELyph 通用规则由 elyph/rules.go 提供，工具、Skill 与后台任务声明需求，输入／工具 Runner 在正文 Hook 后组装。首次展示状态与携带规则的消息原子提交，同 Session 共享，压缩新 Session 重置。
- 输入指令、发现与后台首轮预加载在一次提交中合并状态，失败保留原状态；准备在准入锁外，提交复核取消、binding、模式和压缩状态。
- tool.Info.APITypes 限制协议可用性，空集合不限；view_image 仅供 Responses。前台 chat 过滤工具、Skill 和 tag。
- 后台只使用首轮授权状态，续跑不扩大工具集合，禁止 discover_tool、workspace 和 ForegroundOnly 工具。tool_list_names 先匹配工具／Skill，再匹配 tag，仅显式 tag 注入提示。PrepareBackground 按注册类型分流，授权筛选后复用 PrepareTools／PrepareSkills，合并工具附带说明、Skill 文档与状态；ActivateTools 声明的可执行 Skill 直接加载自身 schema，纯文档 Skill 不生成调用接口。
- fileops 统一处理工具及命令的编辑／撤销。context 只保存绑定，准备对象持有快照，提交校验显式传递。准备时短暂锁定目标，等待确认期间不持有文件或锁；预览不暴露批准写入的字节。
- 文件提交锁顺序为目标路径 → Scope → SessionID → 备份状态；核对绑定、取消、权限、目标和 revision 后写批准字节。撤销仅在成功写入后登记，后台编辑不登记前台备份；外部进程不参与锁。/stop、切换和清理与提交互斥：先提交则完成，先取消或失效则拒绝。
- 目录 AST 先按扩展名／标准 Shell shebang 筛选，再完整读取候选，避免全文读取无关文件；Shell 文件优先采用有效 shebang 声明的方言。
- 目录 grep 由 rg 枚举文件并分批流式搜索，完整统计匹配数，只保留展示／index 所需候选，按相对路径和行号排序；大文件警告与实际搜索范围一致。
- 长期记忆同步批量读取索引，按文件名确定重复 ID 归属，先移除失效索引再更新；常规读取有 30 秒冷却，修改／删除找不到 ID 时强制同步，失败允许立即重试。格式错误分类只在当前 store 内存复用，重新打开后重验，不增加数据库字段。
- 网页提取合并同键在途请求，各调用独立取消／等待，无人等待时取消共享获取；强制刷新可加入在途请求，失败保留旧缓存，迟到结果不覆盖新请求。
- shell 收集 stdout／stderr 各保留 256 KiB，超限继续排空并丢弃多余输出。

### 公共资料与媒体边界

数据库是已提交会话的事实来源。Dialogue 负责公共消息投影、片段编解码、归档及请求副本，Chat／Responses 负责协议编码与循环。

```mermaid
flowchart LR
    DB["storage/sqlite/<br/>完整消息、原生材料与引用事务"] <--> D["agent/dialogue/materials.go<br/>投影、归档与请求副本"]
    D <--> M["media/resolver.go、request.go<br/>实体物化、请求持有与交付"]
    D --> C["agent/chat/transcript.go、model_call.go<br/>Chat 消息与调用"]
    D --> R["agent/responses/context.go、input.go<br/>原生窗口与请求组装"]
```

工具结果保留完整正文与 segments，提交成功后才进入活跃上下文。归档保存稳定媒体 ID，请求展开 URL／base64 仅供当次发送，不回写或从 HTTP 正文反向恢复归档。

### 媒体引用与清理

- 媒体按 SHA-256 去重，media_references 是引用事实来源；消息、工具参数、Fork、Cron、Elnis 和输出关联通过事务维护引用，读取／发送另持临时引用。
- MediaCall 按 ID 复用调用内元数据、持有和成功导出；OpenHeld 使用已持有记录，同次调用串行，Close 释放引用，失败释放可重试。
- 工具通过确认后、实际执行前关联 JSON 参数中的完整媒体 ID，不扫描自由文本；调用失败保留引用，执行前拒绝不建引用。
- PrepareMaterials 将完整请求一次交 AcquireForLLM，唯一 ID 只读及持有一次，大小按实际出现次数累计。hybrid 超限全部 S3，否则全部 base64；纯 base64 超限报错，持有持续到该次 HTTP 请求结束。
- 签名 URL 独立持久化；逐对象锁内按需上传和签名，剩余超过 5 分钟复用，否则按 1 小时刷新。签名或缓存故障返回业务出口报告。
- 只有实体确实缺失时生成请求占位文字并通知；权限、取消、超时或仓储故障仍报错。Fork／后台副本 seed 独立持有完整窗口素材，不打开媒体实体。
- 入库清洗名称与敏感来源，图片按限制转白底 JPEG。Chat History 查询不下载，get_media／view_image 限当前平台和 scope；历史批量获取最多尝试 5 个未缓存位置，缓存不占预算。
- 输出回执按媒体位置关联；缓存故障只记 WARN，不改送达事实或触发重发。跨库历史对账复用消息归属查询，查询故障保守停止。
- 最后引用释放后保留 1 小时；清理认领与新增引用互斥，按远端 → 本地 → 记录删除，失败留待重试。相同 ID 的导入／上传／删除互斥，每轮最多并行清理 4 个对象。
- 清理前发现引用异常即停止。启动先清理临时引用，再由 Elnis 裁决历史中断事件，即使禁用 Elnis 也执行；状态与引用释放原子提交，失败阻止启动，持久化 outbox 保留。

<!-- locator:skill -->
## Skill

- AgentSkill 使用 SKILL.md 和可选 ELBOT_SKILL.toml；原生 EL／Go Skill 使用 SKILL.elyph，Go runner 可配编译产物。
- 命令与 Go runner 每次读取技能根 .env；普通变量按技能 .env > 进程环境 > 配置根 .env 合并，PATH 按进程、配置根、技能追加去重。子进程 cwd 为技能根目录，Go 编译用启动时基础环境。
- scanner 构建 catalog，discover_tool 展示详情或激活 wrapper；原生 Skill finalize 执行 lint、gofmt、build 和 reload。
- reload 串行验证候选，再替换 registry 与 catalog，失败保留活动快照；AgentSkill manifest 修改后 reload 失败则恢复文件。
- 媒体在执行阶段准备：shell 用 media_inputs／ELBOT_MEDIA_N，Go 用 payload.media_inputs，TOML 用 type=media 参数；stdout 媒体经 os.Root 校验后导入中心，普通文本 ID 不转换。
- shell 导出缓存跨调用保留，每次调用首次使用时校验文件并刷新时间，由 sandbox 维护任务清理；Go／TOML 使用调用私有目录，结束即删除。同次调用复用成功导出，不缓存失败结果，结束释放媒体引用。
- Skill 命令 stdout 保留 1 MiB、stderr 保留 16 KiB，超限继续排空；成功但 stdout 超限时报错，不解析残缺 JSON。

<!-- locator:hook -->
## Hook

Hook 根包拥有公开管理入口、装配、外部连接和生命周期；pipeline 先调用注入的 runtime 续接能力，再按事件点和优先级串行执行普通规则，并负责触发错误钩子，control 管列表和 reload，runtime 管进程、RPC、waiting 与 SharedState。根包通过最小消费接口调用子包，不暴露内部组件。子包不得反向导入根包；工具、平台、媒体与日志／通知发布能力由根包装配注入。协议见[Hook 文档](../docs/hooks.md)。

根包和 pipeline 已落实目标执行边界，其他子包构造参数、续接接口及剩余共享定义尚待落实。CallOptions 显式携带 Woken、ReadOnlyCalls 和 Observe；Observe 只接收 context 与 handler 名称，桥接从 Agent 事实固定 SessionID，执行方决定实际 handler 的登记起止。根部 fillEventFromContext 只转换并补入外部 context 事实；根包不组织内部执行顺序、不触发执行错误钩子、不重复匹配或执行只读校验。

```mermaid
flowchart TD
    A["agent_bridge.go / platform_signals.go<br/>外部接入与结果转换"] --> S["supervisor.go / lifecycle.go<br/>入口调用生命周期与服务管理"]
    S --> F["context_to_event.go<br/>外部事实转换并补入 Event"]
    S --> P["pipeline/manager.go<br/>续接、普通规则、错误钩子"]
    P -->|wiring.go 注入续接能力| R["runtime/routes.go<br/>waiting 状态与实际续接"]
    S & P & R --> C["share/*.go<br/>内部共享契约与辅助函数"]
```

Hook.Start 串行等待加载与平台订阅完成后才标记启动成功，重复启动不会提前返回。Hook.BeginClose 停止新连接和平台订阅，保留 Agent 收尾回调；Close 解除 Agent 注册、取消并等待根包已接纳的调用及平台队列，再关闭 runtime。Done 等待真实退出，关闭等待超时不提前宣告完成。

Agent 通过 `RegisterHooks(hooks.Points)` 接入一组提供方回调，返回解除注册函数。Points 声明消息、模型、工具和输出阶段，以及入站路由探测和取消能力；参数只包含业务数据，Agent 不构造 Hook 协议 Event。管理命令独立消费 HookService。

- 未注册的处理点原样通过，通知点直接返回；重复注册报错。注册表按值保存，解除函数只影响自身注册，已进入的回调继续完成。Agent 不维护规则优先级或第二套插件流水线。
- 入站先计算唤醒事实、检查原始取消命令，再探测 waiting 并按需准备媒体；消息回调通过 pipeline 完成续接和普通规则执行。Agent 随后发送附加输出、处理消费结果，再检查改写后的取消命令并进入命令／模型流程。唤醒不因改写而重新计算。
- `Facts` 传递源身份、会话快照、唤醒结果和调用只读策略；原平台消息、引用及公共执行事实仍从所属包的 context 读取。初始 user 和新 pending 的编辑边界不变；Hook 协议转换和逐条规则校验归提供方。
- `StartHookRequest` 为一个实际 handler 建立子 Request，保留根请求关联、取消和完成登记。会话尚未绑定时也可登记；不参与 Agent 执行的平台全局工作不使用此入口。
- `BeginClose` 拒绝新注册，但保留在途生产者所需回调。App 等待平台输入和后台生产者后调用 `Close`，Agent 先结束内部生产者，再关闭回调调度和事实信号；`Done` 等待实际退出。


- 规则和持久配置先验证候选再替换，进程异步启动，失败按 runtime 重启策略处理。
- exec 使用 hook.v2 Pipe，默认直接启动 argv；超时／取消终止进程树，结合宿主终止事实与原生退出状态判断结果，不用已取消 context 覆盖独立故障；worker 先固定退出状态和原因，再唤醒等待者。进程环境在启动时固定，进程变量优先于 .env。
- Hook 来源保留显式值，再从公共事实补缺失；唤醒状态仅入口计算。llm.messages 深拷贝且只读，turn／request Hook 分别只改初始 user／新 drain 的 pending；改写保存后才发请求。
- 入站先尝试 waiting 续接，再按 StopPropagation 决定是否执行普通规则；整个阶段在命令与主模型前完成，/cancel 取消路由执行。waiting 状态与续接执行归 runtime，媒体临时引用随租约或 runtime 释放。
- Handler／Run 返回 Event、LogResult、error，Notify 返回 LogResult、error；来源先声明操作结果再包装错误，LogHook 只发布完整记录，Agent 不重复记录。
- 实际工具调用返回后立即记录 hook_tool_call，已运行记 INFO／succeeded；无效参数记 rejected，不补造调用 ID。工具输出发送另记 hook_tool_log，业务与日志结果分离。
- 工具审计 Actor 在授权默认值注入前固定；有 Session 时使用执行 Actor，无 Session 时使用原 contextinfo。输出、失败通知与进程关闭遵守各自来源和生命周期。

<!-- locator:output -->
<!-- locator:notification -->
<a id="output"></a>
## 输出与发送

正文、媒体、Hook 输出和后台报告经 `delivery/dispatch.Router → 平台发送器`；SendChat 回复原会话，SendTo 发送明确目标，返回实际 Receipt、LogResult 与 error。

推理文本流契约由 `delivery.ReasoningStream` 定义，提供 `Append` 和 `Finish`；Agent 消费该输出能力，Platform 实现展示。两侧引用同一公共契约，Platform 无需为推理流返回类型依赖 Agent 的 `dialogue` 包。

- ReplyCommitter 先执行最终展示 Hook 并发送正文，再保存模型原文与成功回执关联，最后发送附加输出。部分发送失败仍保存已有回执；无回执的发送失败直接返回，成功内容不重发。
- 流式回复合并 Replace／Finish 的回执，按平台、scope、消息 ID 去重并合并输出索引，保留部分成功，不共享平台回执切片。
- Dialogue 在归一取消、暂停和替换后统一 ReportFailure，记录 dialogue_failed 并通知一次；进入 Dialogue 前的操作由直接负责的入口使用同一报告函数。Hook、API 用量、工具和投递日志保留各自事实，转交错误不重复兜底。
- 生命周期取消按请求 context 的实际状态判定，不能仅按 error 类型静默；整轮超时由执行层通知。失败通知解除请求取消与旧 binding，保留捕获的原会话，投递失败只记日志。
- NotificationRequested 仅接纯文本；显式目标优先，否则用原 Conversation，通知管理器校验 binding 与后台策略。
- 全局单槽只做有序转交，不等待平台网络发送，避免慢平台阻塞其他平台；各平台独立使用 8 个发送槽、256 个等待项，同目标有序，满时拒绝且不重试。默认 30 秒覆盖路由、排队和发送，不逐层重置期限。
- 工具提示按工具保留完整段落，每段加一次 [tool] 前缀，合并后执行一次输出 Hook 并发送。状态观察不承担业务提交，入队不算送达。

```mermaid
flowchart TD
    P["platform 各适配器<br/>实际发送与回执、来源 LogResult"] --> R["delivery/dispatch/router.go、media.go<br/>路由、媒体处理与送达日志"]
    R --> B["agent/dialogue/reply_commit.go<br/>主对话回复与回执关联"]
    R --> BG["cron/delivery.go、elnis/outbox.go<br/>后台报告与投递状态"]
    B --> T["agent/dialogue/runner.go<br/>主对话终态归一与统一失败通知"]
    R --> O["agent/output/output.go、toolrun/runner.go<br/>所属操作日志"]
    O --> E["events/logging.go<br/>契约校验与发布"]
    T --> E
    E --> L["logging/<br/>分类排队、过滤与落盘"]
```

<!-- locator:platform -->
## 平台适配

平台负责输入归一化、历史与输出落地，支持本地／远程 CLI、QQ OneBot、QQ Official、Telegram 和 headless。

- Platform Module 管理适配器；服务模式各平台退出互不影响，全部退出后仍等待外部停止，直接启动的交互 CLI 拥有应用退出职责。非预期退出记录错误，并异步通知其他运行平台的配置超管。
- CLI 拥有私有展示目标、状态订阅、合并和格式化；远程默认回复固定原连接，断开即失败，显式目标按多连接规则发送。
- 思考流直接接收原始增量与结束边界，每次调用独立，首个有效增量打开展示，结束／失败／取消收尾；逐增量刷新接管来源，不重放历史，展示错误不改变业务结果。
- 原始有序 segments 写入 Chat History 并清洗敏感来源，只在唤醒或 waiting continuation 时物化媒体。引用依次按输出关联、历史、平台能力恢复。
- 相同 actor／platform／scope 的最后一条 assistant 引用触发 Resume，较早回复触发 Fork。
- QQ Official 在适配器内解析 `message_scene.ext`、引用类型 `103` 和一层 `msg_elements`；入站 `msg_idx`、发送回执 `ext_info.ref_idx` 按平台／scope 映射到真实消息 ID，存入 Chat History 库并随历史清理。映射命中后复用公共引用恢复；未命中时只展示事件自带文本与附件，不触发 Resume／Fork。
- OneBot 私聊或显式引用 forward 展开一层，其余情况用占位；纯文本超过 3000 个 Unicode 字符时发合并 forward，失败不退回分条。Telegram 流式分页失败保留已发页回执，不重发整段；CLI 返回空回执。

<!-- locator:session -->
## Session 生命周期

- Session 唯一管理 current 和 Binding；切离后原 binding 永久失效，切回建立新 binding，删除使全部相关绑定失效。
- 准入锁顺序为 Scope → 排序 SessionID → 状态锁；模型、工具、Hook、发送及一般信号在锁外。执行中禁止普通切离／删除，维护跳过忙碌项。
- ForegroundActivated 在接管提交和新绑定建立后、释放准入前同步派发，订阅者只更新执行内存，不做 I/O 或再次获取准入锁；派发失败不回滚已提交接管。BindingChanged 在锁外发布并清理旧展示与撤销记录。
- 首次主对话准入保存 llm_origin 的 protocol、provider、base_url，失败拒绝调用；压缩／Fork／后台副本继承归属。
- 后台准备不激活 current，复用拒绝已接管会话；所属用户同平台私聊或 CLI 管理入口可恢复。永久接管改为前台 Scope／work，保留历史和 workspace，在途调用不重启；后续调用与发送使用刷新来源。
- 接管提示追加为请求末尾的合成 user 消息，解除后台无人值守、自动汇报及强制 JSON 要求，不写入 system prompt 或展示历史。

```mermaid
flowchart LR
    S["session/promotion.go、signals.go<br/>提交接管、发布同步事实"] --> E["agent/execution/execution.go、turn/execution.go<br/>保存一致执行快照"]
    W["agent/signals.go<br/>连接与释放订阅"] -.-> E
    E --> T["agent/dialogue/execution_context.go、agent/tools/toolrun_adapter.go<br/>调用前应用接管快照"]
    E --> O["agent/execution/execution_output.go<br/>发送时刷新去向"]
```

- Fork／后台复制在锁外准备材料，提交复核 binding、checkpoint、pending 和取消；Session、消息、seed 与媒体引用原子保存。Responses 按分叉时的不可变快照取材料，包含对应工具批次结果、排除之后追加的结果，不重跑历史工具；后台副本不激活前台或继承执行／工具状态。
- 命名受应用生命周期管理，Turn 结束不取消；Scheduled 表达调度，Completed 表达终态，取消后不写标题或 fallback。TTL 和 /new 只清除 current，下一条输入再建记录。

<!-- locator:llm -->
## 模型服务与客户端

- modelmgr 唯一持有模式／槽位、命名／压缩选择、厂商运行时与目录／窗口缓存；后台默认 work，Elnis 可选 elwisp1/2/3，Cron 可指定模型。
- 厂商重载串行准备全部客户端后原子发布，新快照拥有独立缓存；在途轮继续用旧快照，旧请求不回写新缓存。模型目录并行查询 provider，编号在筛选前分配，结果返回副本。
- 模型切换先原子保存 state.toml 再发布内存，失败保留旧选择；命名与摘要固定各自选择和 fallback，Prepared Hook 不改模型身份。
- httpclient 管代理、可取消的初始交换重试、SSE 与超时，断流不重放；鉴权、编解码与终态归客户端。Extra 只补字段，重名发送前拒绝；重试通知跟随真实调用 context。

<!-- locator:context -->
## 上下文与压缩

contextmgr 管历史、用量、阈值和压缩分派，通过 Selection 查询 modelmgr 的窗口；协议 Compactor 准备结果，执行层交接，Session 保存新会话。

- 手动／自动压缩共用开关、阈值与当前窗口。压缩中拒绝输入、允许停止，交接复核取消、binding、接管和 checkpoint；先保存新 Session 再切 current，后台仍无 current，继承 workspace 与命名信息并清除旧用量。
- Chat 使用文字摘要模型或对话 fallback，seed 在首条输入保存“摘要＋历史用户原话＋当前输入”后消费。
- Responses 固定当前模型，完整窗口末尾加 compaction_trigger，使用普通流、store=false、tool_choice=none；不带旧链 ID 或正文格式要求。必须返回唯一非空加密 compaction，seed 只含该项与素材引用，首个 checkpoint 成功才消费，根材料保留供恢复。
- 原生压缩失败不降为文字摘要或切换会话；自动压缩交接后继续已接受输入，手动压缩不自动聊天。
- Soul、记忆、工具和显式 tag 提示由公共 Prompt 来源组合，Chat 每轮构建 system message，不入历史。
- Usage 与活动时间在 Turn 收尾合并保存；未返回用量时只更新活动时间，保留原 Usage，保存失败仍保留内存已观测值。摘要与状态计数为可空 int64，未知存 NULL，明确零存 0；迁移保留既有数值，不把历史零值猜成未知。

<!-- locator:storage -->
## Storage 与 SQLite

- storage 定义领域模型与 repository 契约，SQLite 实现消息、Session、历史、工具、Cron、Elnis 与原生状态事务。主库使用单连接，工具状态随整份 Session metadata 合并更新，以保持事务和状态合并简单可靠；实测出现连接等待或编码瓶颈后再调整。
- 主库为空时，在同一事务中直接创建 `db_init.go` 的最新结构并记录已包含的迁移版本；已有数据库及结构快照之后的新版本由 `migrations.go` 补齐升级。普通测试也走这一建库入口，使用各自独立的内存库；迁移和文件重开测试保留专用夹具。
- Session 仓储的 Mutate 在短事务读最新行，只改负责字段，不做外部 I/O 或嵌套仓储操作；持久化值不变则跳过 UPDATE。metadata 保留未知字段和数值精度，损坏数据拒绝读写。
- segments 是结构来源，content 是文本投影；ToolBatch 用 tool_result_message_ids 保存调用与结果数组关联，复制时重映射全部 ID。
- 原生表保存 exchange／input／call／checkpoint／seed，Session metadata 保存归属、版本与引用；关键提交比较旧 checkpoint，原子合并消息、seed 消费与媒体引用。
- native_seeds.media_ids_json 始终为 JSON 数组，无媒体存 []；原生窗口按目标 checkpoint 批量读取，查询保留平台隔离、归档与 Fork 范围。

<!-- locator:elnis -->
## Elnis / Elvena / Elwisp

Elnis 接收事件，Elvena 定义公共协议，Elwisp 提供外部事件与工具；Elnis 复用 BackgroundRunner 和公共发送能力。

接收、去重、outbox 与恢复见 [Elnis 架构](elnis-elwisp.md)；配置和协议示例见[使用文档](../docs/elnis-usage.md)。

<!-- locator:cron -->
## Cron 与后台任务

Cron 拥有持久化任务、调度、执行与投递状态，maintenance 注册维护任务；Cron／Elnis 共用后台 Session、sandbox 和 JSON 结果能力。

- 每次调度新建后台 Session，同轮 JSON 修正复用 Session、首轮工具和固定模型；前台接管后等待真实 Execution 结果，停止后台修正、自动报告与未开始补投递。
- Cron／Elnis 后台 sandbox 的 shell 非 critical 风险自动确认；critical 拒绝执行并返回提醒。
- 任务 provider／model 成对校验，省略更新保留，同时清空回到 work；配置更新返回完整任务并保留运行事实，安排调度仅在下一次运行时间变化时保存。
- Handler 返回来源 LogResult 与原始 error，由调度器在状态保存后发布一份整体运行日志；触发、逐目标投递审计与正常对话日志独立保留。
- once 分开保存任务配置与 Delivery，TaskCompleted 表达任务结论，ReportReady 表达报告可复用，全部投递完成后才禁用。
- 连接恢复只补发已有报告，不重跑 LLM；使用最新配置和逐目标／output 状态，同任务投递互斥可取消。附件失败同轮降级文字，再失败等下次连接。
- 报告落库前可能重新执行，发送成功但回执未落库时可能重复投递。
