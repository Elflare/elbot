注：此重构不改用户文档
需要理解整体架构：读 `devdocs/architecture.md`。architecture-principles必读

# App 包设计

App 使用六文件组织公共运行流程，按下层接口已存在来编写调用；下层按照这些契约实现。五条业务线已提供根包总管，配置独占已实现；媒体和模型构造入口及进程入口适配仍待完成。完整应用的编译与运行验证需待这些接口接入。

## 职责与边界

App 负责共享资源、总管构造、跨线接线和启停协调。每条业务线接收完整配置，在内部拆分配置、创建组件并持有自己的订阅；App 不取得内部 Registry、Manager、Session 或执行协调器，不增加工厂或兼容层。权限管理及现行调用方统一使用 `security.Permission`。

| 文件 | 职责 |
|---|---|
| `entry.go` | 局部启动阶段列表、统一业务线集合、部分失败清理、按能力等待及耗时统计 |
| `environment.go` | 验证 TUI／Service 模式，定位并规范化配置路径，取得同配置本地实例的独占锁 |
| `resources.go` | 加载配置、构造共享资源并交付依赖；失败仍交回已取得的资源 |
| `wiring.go` | 构造五条线总管，交付共享资源和公开业务能力；连接 Hook、Platform 与 Agent |
| `shutdown.go` | 停止新工作、按结果枚举协调清理；全部步骤共享独立的 30 秒期限 |
| `client.go` | 明确运行客户端和自动尝试；消费平台分类结果，返回错误或回退信号 |

```mermaid
flowchart TB
    E["entry.go<br/>入口、计时、启动与等待"] --> ENV["environment.go<br/>模式、配置路径与配置锁"]
    E --> R["resources.go<br/>共享资源构造与所有权"]
    E --> W["wiring.go<br/>总管构造与能力接线"]
    E --> S["shutdown.go<br/>统一期限、结果分类与清理顺序"]
    S -.->|最后释放配置锁| ENV
    R --> M["媒体与模型服务<br/>接收完整配置，自行初始化"]
    W --> A["agent.Supervisor<br/>公开业务能力"]
    W --> O["其他四条线<br/>总管接口"]
    C["client.go<br/>消费结果、决定回退"] --> P["Platform.RunClient<br/>连接选择、运行与结果分类"]
```

## 运行与所有权

环境阶段按显式路径、`ELBOT_CONFIG_FILE`、默认位置的优先级定位配置，转为绝对路径并解析符号链接。`gofrs/flock` 对规范化配置路径追加 `.lock` 后执行非阻塞 `TryLock`；TUI 和 Service 都必须取得锁，同配置已被占用或取锁失败时立即返回错误。不同配置分别取得锁，纯客户端不取得本地实例锁。

锁句柄以 `io.Closer` 交给 `Run` 持有，在资源构造前取得，由统一退出流程最后释放。锁文件保留，避免配置替换保存或删除锁文件造成互斥失效；进程退出时由操作系统释放锁。统一退出超时后不提前释放锁，等待进程退出。

`Run` 用局部 `startupStep` 列表声明 environment、resources、wiring、start，由 `runStartupSteps` 统一检查取消、执行、标注错误并记录成功阶段耗时。阶段先保存已取得的资源，再返回错误；失败停止后续阶段，统一进入清理。

App 消费 `Lifecycle`（Start、BeginClose、Close）和可选的 `Waiter`（Wait），下层通过方法集实现，不反向依赖 App。唯一的 `businessLines` 集合保存名称、总管和启动顺序，具体类型只在装配时接线。构造后立即登记非 nil 的部分结果；按 Tool → Agent → Hook → Platform → Background 启动。

`waitBusinessLines` 遍历集合筛选 Waiter，按实际数量并发等待；首个结果结束运行等待，取消并回收其余等待，保留独立故障。没有参与者时等待外部取消。Wait 必须响应等待上下文，取消等待不停止业务线。Platform 自行解释 TUI／Service 的退出语义；Background 的致命故障及时返回，实际清理由 Close 等待。

环境、资源、装配或启动失败均进入同一退出流程。入口先取消运行上下文，再以 `context.WithoutCancel` 创建统一清理期限。纯外部取消视为正常停止，混合错误中的独立故障仍保留；运行和清理错误合并返回。

退出先请求各线停止新工作，等待平台接入与后台任务退出，再关闭 Agent、Hook、Tool，最后释放平台发送能力。随后关闭模型管理、独立历史库、共享存储、日志和配置锁。媒体与权限没有独立关闭步骤。

App 用 `shutdownStep` 记录步骤名称和关闭调用，`runShutdownSteps` 顺序执行并汇总结果；`runShutdownStep` 负责单步等待和统一期限判断。下层 `Close` 保持返回 error，App 内部的 `stopResult` 将控制状态与错误详情分开：

| 状态 | 含义与处理 |
|---|---|
| `stopCompleted` | 步骤完成，继续清理 |
| `stopFailed` | 步骤返回错误，保留错误并继续清理 |
| `stopTimedOut` | App 的统一期限耗尽，保留此前错误并停止后续清理 |

业务自身返回超时错误不等于 App 的统一期限耗尽。关闭调用即使忽略取消，App 也只等待到统一期限；超时后不继续释放可能仍被使用的依赖。

共享存储及独立的历史 SQLite 均由 App 构造、持有和关闭。Platform 直接写历史库，Tool 根部直接查询；Agent、Media 借用单条历史身份查询，Background 借用过期清理。App 通过 `media.NewMediaCenter(ctx, cfg, store)` 交付完整配置和共享存储，媒体侧负责后端、凭据、配置应用和中断恢复；通过 `modelmgr.NewModelManager(ctx, cfg)` 交付完整配置，模型侧负责协议客户端、请求参数和 providers 重载。两个构造入口均返回资源及错误，App 接收部分结果并统一清理，不拆解内部初始化。App 在业务线构造前注入 Media.History 并执行历史引用对账，查询失败阻止启动；历史库在全部业务使用方退出后关闭，部分构造失败与正常退出共用清理流程。

## 对外调用契约

`agent.NewSupervisor(ctx, cfg, deps)` 接收 Store、ChatHistory、Models、Media、Permission、Logs 及 Tool、Hook、Platform 能力，其中 Hook 仅通过 `Commands() []command.Handler` 提供管理命令，由 Agent 使用标准注册入口接入。公开业务入口为 `HandleMessage`、`RunBackground`、`CopyBackground`、`CleanupSessions`、`SessionTakenOver`、`RegisterHooks`、`StartHookRequest`、`Complete`、`Commands`、`ObserveStatusChanged`、`ObserveBindingChanged`；生命周期使用 `Start`、`BeginClose`、`Close`、`Done`。Hook 的 `ConnectAgent` 注册一组 `agent.Points` 回调并持有解除函数，每个实际 handler 通过 `StartHookRequest` 登记。其他接收方的 `ConnectAgent` 负责自身适配、订阅和释放。其余四条线通过根包 `Supervisor`、`SupervisorDeps`、`NewSupervisor` 及各自生命周期接口接入。
各条线的对外文件放各条线的根部，内部业务放子包。Agent 的公开回调和事件定义在 contracts.go，内部共享载荷放 share，消费者按实际需要接收最小能力。Agent 根包与子包、Hook、Command、Completion 已通过相关回归；真实 Agent–Hook–Tool 总管接线覆盖命令注册、规则补全和输出回调。Hook 消费 Agent 与 Tool 根部类型，管理查询类型留在 Hook 内部。

Platform 根部按目标下层契约实现。NewSupervisor 接收完整配置及 Mode、Store、ChatHistory、Media、Permission、Logs，持有适配器、通知连接和 UI；历史库属于 App，Platform 只借用。部分失败仍交回总管。公开发送、原生 API 和媒体解析转交内部组件，不暴露 Repository 或 Router。失败通知已接通；配置定义统一由根部 ConfigDefinitions 提供。

Tool 根部创建媒体入口、技能目录与基础环境并一次注入 Skill Manager；Builtin、Skill、发现及 ELwisp 均接入必需的准备／执行契约，Skill 内部不依赖 Tool 根包。Cron 工具只使用 Tool 内部契约，根部 bridge 接入 Storage 和模型校验并发布任务定义变更；Background 根部持有订阅，Cron Manager 合并名称并串行读库刷新。Elnis 已通过自身媒体契约接入媒体中心。Background 整包已完成生命周期接线；完整 App 仍需媒体、模型构造入口及进程入口接通。

Background 根部按四文件组织：`supervisor.go` 定义对外总管并持有组件，`deps.go` 声明 Store、ChatHistory、Models、Media、Logs、Agent、Tools、Platform 借用依赖，`wiring.go` 负责完整配置装配与边界适配，`lifecycle.go` 协调 Start、Wait、BeginClose、Close、Done。根部不公开内部 Runner 或 Cron CRUD；Cron 工具注册和管理归 Tool。

根部按消费方职责注入最小接口或函数，按需复用提供方根包的公开类型。Runner 的执行接口只包含 Agent.RunBackground，并接收沙盒根目录；Cron 分别接收模型最小接口、发送和会话复制函数；维护组件已接收日志最小接口、会话与历史清理函数、媒体清理接口；维护配置通过 share.MaintenanceJob 交给 Cron，注册回调直接使用函数，由根部接线。历史引用统一在媒体清理时对账。Elnis 分别接收工具声明展开接口、发送、平台 API 和模型选择函数，模型函数只返回 ModelSelection。cronBridge 将 Storage 转换为 Cron 自有 JobRepository 记录，保留原始 JSON 由 Cron 逐条解释；elnisBridge 将 Storage 事件与报告转换为 Elnis 自有 EventRepository 记录，通过 Media 的四项能力物化输入、导入附件和关联事件引用；导入只交付媒体 ID。Cron 与 Elnis 共用 share.ReportMessageMap 和 MapReportReceipt，回执映射仅使用实际 SentMessages。Cron 与 Elnis 共用 share.SessionTakenOver 查询契约，由根部注入 Agent 的公开方法。App 已通过 SupervisorDeps.Agent 交付总管，无需额外的 ConnectAgent。

Background 构造阶段建立外部订阅；Start 依次完成恢复、维护任务配置、Cron Manager 首次加载与启动、启用的 Elnis 就绪，最后调用 Cron Service.Start 放行平台通知。SetupCron 只配置任务。Manager 在调度启动前排空启动期间登记的任务变更，运行期间串行刷新，收尾只记录原任务 ID 的运行事实，次数由 SQLite 原子累加。Cron 旧管理入口已移除，定义管理归 Tool。

平台通知在 Service.Start 前按平台保留最新上下文，不创建 worker；启动后同平台串行、不同平台独立。启动失败不放行，关闭时清理缓存并停止接纳；Service.Start 不等待恢复任务执行完。BeginClose 取消工作并对订阅调用 BeginClose；Close 使用 App 清理期限，Done 等待组件、订阅回调及运行观察实际退出。

Elnis 的 `http.go` 构造 HTTP 路由和协议处理，`lifecycle.go` 管理同步监听就绪、队列、workers 和退出。关闭先停接入、取消并关闭连接，等待真实 handler、Serve 和 workers 退出，再批量失败化 accepted／queued／running，原子释放其媒体引用，保留持久化报告。数据库收尾隔离运行取消并限时 5 秒；未启动的 Runtime 不额外写入。故障由 Runtime.Wait → Background.Wait → App 上报；根部先发起整线关闭，Wait 不等待收尾。Close 只返回清理结果，单个事件或报告失败不升级为整线故障。

具体停止原因由产生结果的组件解释：Elnis 判断正常 HTTP 关闭与致命故障，Background 决定整线关闭，App 协调应用退出。等待上下文取消只代表结束本次观察。Background 的启动／等待取消路径使用已取消上下文向 Runtime 取得已发布结果，不依赖观察协程转存的时机，不解析 HTTP 错误；Runtime 发布启动结果时也保留已记录的运行故障。结果读取之后发生的新故障不属于本次观察范围。

Agent 公开 RunBackground、CopyBackground、CleanupSessions、SessionTakenOver，均连接调用方与 Agent 的取消信号。后台输入准备位于 input，复制、清理和接管查询复用自有 Session。复制元数据使用 map[string]json.RawMessage，接管查询只返回事实；Background 不取得会话存储记录。执行、会话清理、Cron 复制及共享接管查询已接通；Platform 公开能力已通过根部接入下层。App 等待后台调用退出后再关闭 Agent。

Elvena 请求、Elnis 声明生成与展开、Background 共享请求统一使用 Tool 根包公开类型。工具声明的 JSON 标签保持协议字段名和空值省略规则，CanonicalName、EventKey 由 Elnis 生成，不参与外部声明 JSON。Runner 只准备任务目录与后台来源，并调用 Agent.RunBackground；会话、权限、模型和工具预载归 Agent，报告解析和重试归 Cron、Elnis。

Elvena 保留公共协议与来源契约，Call 只包含 Platform、API、Params；Elnis 持有内部 Event，targets.go 的 toDeliveryTarget 转换投递目标，media.go 的 directText 组装正文。direct.go 按顺序通过注入的 CallPlatformAPI 转发原始 API、参数与上下文；显式平台优先，否则使用首个解析目标的平台。调用失败或取消时停止后续调用及消息发送，calls-only 不额外发送消息。具体文件归属见 [Elnis 内部架构](elnis-elwisp.md#依赖边界)。

`platform.RunClient(ctx, cfg, name)` 完成连接选择、运行和结果分类，返回 `ClientResult{Kind, Err}`：

| 结果 | 契约 |
|---|---|
| `ClientCompleted` | 正常结束，Err 为 nil |
| `ClientFallback` | 未指定连接名称时，默认连接未配置、被禁用或本机服务拨号失败；Err 保留原始错误 |
| `ClientFailed` | 其他失败，Err 保留原始错误 |

指定连接、远端失败、配置、认证、握手和取消均不归为可回退。App 不解析连接地址或重新判断失败性质。`RunClient` 直接返回 Err；`TryRunClient` 只将 `ClientFallback` 转换为 `ErrClientFallback`，其他结果返回 Err。实际本地启动由进程入口决定。

App 与 Agent 不承接 Doctor，配置诊断由配置侧负责。进程入口尚需适配已确认的 `client` 命名和新 App 参数。

## Platform 下层目标契约

以下为根部与下层已接入的契约。子包不导入根部或兄弟实现，外部不导入 `platform/share`；Media 已移除 Platform 依赖，Tool 历史桥接直接消费 Storage 查询与编解码，平台媒体解析只通过根部最小接口注入。Agent 使用自己的 MessageRequest。

```go
// share/runtime_contracts.go
type Runtime interface {
    Name() string
    Start(context.Context, MessageHandler) error
    BeginClose()
    Wait(context.Context) error
    Close(context.Context) error
    delivery.MessageSender
}
type MessageHandler func(context.Context, string, MessageContext) error
// OneBot 自有依赖；CLI、headless 保留现有构造入口。
qqonebot.NewFromPlatformConfig(map[string]any, qqonebot.Deps) (*qqonebot.Adapter, error)
qqofficial.NewFromPlatformConfig(map[string]any, qqofficial.Deps) (*qqofficial.Adapter, error)
telegram.NewFromPlatformConfig(map[string]any, telegram.Deps) (*telegram.Adapter, error)
refcontext.Apply(context.Context, refcontext.Options, share.ReferenceOptions) (share.ReferenceResult, error)
```

share/config.go 的 CommonConfig 包含 Superadmins、CommandPrefixes、ConfigDir、AttachmentDir、MaxReceiveFileBytes、DownloadTimeoutSecs；share/history_contracts.go 定义 ChatHistoryWriter（Append）和 ChatHistoryReader（GetByPlatformMessage）。OneBot 在 adapter.go 声明自有 Deps，嵌入 CommonConfig，历史只组合这两项能力，另接收 ReferenceResolver、FindByPlatformMessage 和 FindOutputs。根部直接注入存储方法值，不交付完整 Store，不增加查询转发层。

根部 SupervisorDeps.ChatHistory 按实际使用接收 Append、GetByPlatformMessage、MapPlatformMessageAlias、ResolvePlatformMessageAlias；总管不保存仅用于中转的 history 字段。refcontext.Options.ChatHistory 只包含 GetByPlatformMessage。Telegram 的 Deps 嵌入 CommonConfig，历史只接收 ChatHistoryWriter，另接收 ReferenceResolver；旧 share/adapter.go 与根部共用依赖装配已删除。QQ Official 的 Deps 嵌入 CommonConfig，历史只组合 ChatHistoryWriter 和两项别名方法，另接收 ReferenceResolver；不持有完整历史仓库。Storage 参数和返回类型保持一致。

share.ReferenceOptions 只包含 Platform、ScopeID、ActorID、IsSuperadmin、ReplyID、Text、CommandPrefixes、Fetch、Enrich，不携带存储依赖；ReferenceResult、ReferencedMessage 保留现有结果字段，片段使用 share 类型。refcontext.Options 仅包含 Store、ChatHistory，根部装配时固定该依赖值，通过闭包注入引用函数，不新增 Resolver 组件。远程适配器保留 Enabled；所有非 nil 构造结果都纳入所有权，包括未启用与部分失败对象。OneBot、QQ Official、Telegram 已删除旧 New、Run 和完整历史字段，仅保留 NewFromPlatformConfig。QQ Official 的凭据与默认值在构造阶段确定，不保留闲置媒体配置或运行期环境凭据兜底。Telegram 只在启用时解析凭据与代理并创建自有 HTTP 连接池，默认值及格式只在构造阶段确定；禁用对象跳过凭据解析，不创建 HTTP 客户端，仍可正常关闭。

Start 同步完成本地启动，不等待远端连通；与 BeginClose 并发安全，关闭后不再接纳输入。正常停止由适配器返回 nil，独立故障返回 error；Wait 响应观察取消并优先返回已发布结果；独立运行故障已记录而业务仍在收尾时，取消观察仍返回该故障，不提前宣告退出。启动失败仍可 Wait／Close。Close 响应期限且可重试。根部 BeginClose 只停止接入，Wait 等待所有已尝试启动的接入实际退出；服务模式即使全部平台退出也等待外部停止，本地 TUI 退出结束整线。取消等待不停止业务线，也不覆盖已经发布的独立故障。

最终 Close 按 UI → 通知管理与接线 → 适配器清理，不关闭借用的历史库。普通清理错误累计后继续，期限耗尽停在当前步骤并保留后续依赖，重试从该步骤继续；Done 仅在全部自有资源清理实际完成后关闭。发送、展示与通知能力在停止接入后仍保留给尚未退出的使用方；App 保持历史库存活直到全部业务使用方退出。

share/message.go 承接内部 MessageContext、MessageSegment、ReplyContext、Identity、Mention、MediaResolver 以及 WithMessageContext 等路由上下文方法，内部字段沿用旧定义。根部 contracts.go 的公开 MessageContext 独立定义，只含 RawText、PlatformMessage、Reply，引用和片段使用根部自有事实类型；不包含发送器、解析器、会话路由或续接／分叉等运行控制。根部投影和公开存取方法复制原始 JSON 与片段切片，消费者不会共享可变事实；存取不改写 contextinfo。Agent 输入已迁入自有契约，不增加兼容字段。Platform.RestoreMessageContext(ctx, source) 复制或清除平台公开事实与内部路由，不改变目标的取消、期限或 contextinfo；Agent 根部注入该函数，后台入口清除继承的平台状态，前台接管恢复新来源。普通后台输出和默认通知静默，显式目标投递保持可用。

```go
// Agent 根包接收方契约。
HandleMessage(context.Context, agent.MessageRequest) error
type MessageRequest struct {
    Text, RawText string
    GroupRole contextinfo.GroupRole
    BufferAssistantOutput bool
    ForkFromMessageID, ResumeSessionID string
    Segments []MessageSegment
    ContextText string
    ContextSegments []MessageSegment
    Reply ReplyContext
    Bot Identity
    Mentions []Mention
    TriggerKeywords []string
    ResolveMedia func(context.Context, media.PlatformMedia, int64) (delivery.Source, error)
}
type MessageSegment struct { Type, Text, UserID string; Media media.PlatformMedia }
// Media 根包：来源描述，不依赖 Platform。
type PlatformMedia struct {
    Type, MediaID, URL, PlatformFileID, MIMEType, Name string
    Size int64
}
```

Agent 自有 ReplyContext 包含 MessageID、SenderID、SenderName、Text、Segments；Identity 包含 UserID、Username；Mention 包含 UserID、Username、Text。公共会话事实通过 contextinfo 传递。转换不下载媒体，ResolveMedia 保留延迟调用，唤醒与等待续接的物化时机保持不变。

CLI 的本地 Adapter 与 RemoteServer 已实现 share.Runtime；lifecycle.go 管理本地启停，remote_lifecycle.go 管理监听与连接的分阶段释放。Start 同步等待监听绑定或终端就绪；BeginClose 停止新接入并取消已接纳工作，Wait 等待监听、握手和业务交付退出。TUI 与已认证 WebSocket 的展示／协议读取保留到最终 Close，期间不再交付新业务。stdin 管道通过 cancelreader 取消读取，不关闭借用的 os.Stdin；终端使用正常退出消息等待输入读取结束。旧 Run 和 StopAppOnExit 已移除。

Headless 在 headless.go 实现无状态的 share.Runtime，保留 New 和名称 service。Start 只返回传入 context 的错误，不保存或调用 MessageHandler；BeginClose 无操作，Wait／Close 立即成功，观察上下文取消不覆盖完成结果。它没有接入、自有资源或常驻协程，两种发送入口保持拒绝输出。根部将其登记为不启动的主发送占位，只在最终清理时调用 Close；Service 持续运行由根部协调。旧 Run 与对 Platform 根包的依赖已移除。

OneBot 的 lifecycle.go 管理分阶段生命周期：Start 完成本地初始化后异步拨号，连接或读取失败按原退避策略重连；BeginClose 停止输入、重连与新临时拨号，取消已接纳业务及正在进行的临时拨号。Wait 等待接入驱动与业务回调实际退出，已建立连接继续读取 API 响应，断开后不再重连。Close 最终并行释放主连接及临时连接，等待协议读取、在途请求和临时发送退出；观察超时可重试，不关闭借用依赖。

OneBot 的 transport.go 由 newTransport 初始化。请求登记与连接快照、响应投递与 pending 清理分别在同一锁内完成；disconnect 只处理捕获到的故障连接，迟到错误不影响新连接。Transport.Close 永久拒绝新操作并等待真实清理。等待写入资格和响应可取消；帧开始写入后使用隔离调用方取消且有期限的上下文，调用需等帧写完或超时后返回，最终关闭仍可释放连接。send.go 保持发送与回执行为，message.go 通过显式 MessageHandler 参数交付事实并直接写历史。

QQ Official 的 lifecycle.go 统一管理接入和完整发送调用：Start 完成本地初始化后异步连接网关；BeginClose 停止输入、拨号和重连并取消已接纳业务，HTTP 发送保留。Wait 等待网关、心跳及入站处理实际退出，取消观察保留已记录的独立故障；Close 等待接入退出后拒绝新发送、取消在途发送，等待回执别名入库等完整调用退出后释放自有 HTTP 连接池，超时可重试。运行故障不阻止清理，借用历史库不由适配器关闭。gateway.go 同步心跳序号并等待心跳退出，心跳失败关闭对应连接，重连分类决定下一次恢复或重新鉴权。token.go 通过可取消的 refreshGate 串行访问缓存和刷新，取消等待不影响正在执行的刷新；网关停止不必等待收尾发送的令牌请求，HTTP 401 最多刷新并重试一次。消息和通知使用 Share 契约，不提供额外的通用或原生 API。

Telegram 的 adapter.go 定义自有依赖与状态，config.go 提供唯一构造入口；lifecycle.go 管理轮询、入站和完整操作，commands.go 接收命令目录并在锁外同步，send.go 承担普通发送与回执，stream.go 管理流式更新。Start 完成本地启动后异步 getMe 与长轮询，失败沿用退避重试；BeginClose 停止轮询与输入接纳并取消已接纳处理，Wait 等待实际退出。发送、原生 API、媒体解析及每次流式操作登记完整调用，保留到最终 Close；Close 取消在途操作并等待退出，再释放自有连接池，等待超时可重试。流式串行资格等待可取消，闲置流对象不阻塞清理。机器人身份以 share.Identity 快照随消息传递；入站、通知和引用恢复使用 Share 契约，不关闭借用历史库。

CLI SetCompleter 接收 `func(context.Context, completion.Request) ([]completion.Item, error)`；CLI 通过补全入口消费命令信息，没有单独保存命令目录。其他实际命令目录消费者仍接收 `SetCommandCatalog([]command.Info)`。入站每条消息以 share.MessageHandler 交付原文、身份和 Sender，私有目标固定原终端或原连接。

`cli.ConnectUI(cli.AgentEvents)` 返回 UI 和 error，部分 UI 交根部清理。event_contracts.go 定义 CLI 自有 StatusChangedEvent、BindingChangedEvent、BindingSnapshot，由根部转换 Agent 公开事件；订阅回调不接收 context，展示目标取自 Conversation，日志事实取自 Execution。状态发布版本与绑定变更版本分别处理，终态释放待发正文但保留版本水位；换绑／解绑清除旧绑定待发状态并拒绝迟到事件；worker 在锁内取出状态即开始发送，在途发送允许完成，新状态随后串行发送。发送上下文保留事件事实并隔离展示停止，远程写入沿用 15 秒超时。UI 消费已交付的独立快照，持有订阅和展示 worker；BeginClose 停止接纳并清空待发状态，Close 解除订阅并等待在途发送及 worker 实际退出，等待超时可重试。

dispatch.Options.Primary 和 Router 的 primary 字段只接收 `interface { Name() string; delivery.MessageSender }`，RegisterPlatform 使用同一匿名接口。完整 Runtime 仅由总管持有，dispatch 不接管启停。Media 接收 Metadata、ImportURL、ImportFile、ImportBytes、ResolveForOutput 五项能力；SaveMediaOutput 接收 `func(context.Context, storage.MediaOutput) error`，根部直接注入 Store.Media().SaveOutput。媒体准备、发送副本解析、临时资源释放与实际回执关联归 media.go；部分发送仅缓存真实回执；回执身份必填校验归存储入口，dispatch 不重复检查并静默跳过，缓存失败记录警告，不改变发送结果或重发。

通用能力使用 SendChat、SendTo、StartStream、ResolveMedia 等固定方法；引用恢复沿用 refcontext 入站链路，不提供字符串能力分发表。原生入口 `CallPlatformAPI(ctx, platform, api, params)` 返回 `(json.RawMessage, error)`，params 为 `map[string]any`。calls.go 按显式平台取得适配器、释放注册表锁，再通过 share.PlatformAPICaller 转交原始上下文、接口名和参数；未知或不支持的平台报错，不回退当前或默认平台。OneBot、Telegram 的 capability_api.go 保留原生传输，分别返回原生 Data／result；认证、协议封装和操作生命周期归适配器。Hook platform.call 与 Elnis direct 负责各自的平台选择，Elnis direct 直接消费原生调用声明。

通知沿用 New、AddPlatform、ConnectNotifications。share/notification.go 定义 NotificationReceiver 和 NotificationTarget；接收契约只连接发送端，连接由根部持有和释放，目标转换在匹配来源时保留原生回复路由。CLI、OneBot、QQ Official、Telegram 已使用 Share 通知契约。Manager 的 `NotifyPlatformFailure(context.Context, string, error, []events.NotificationTarget)` 使用既有 NotificationRequested 信号逐目标异步提交 ERROR 通知，不等待发送结果、不重试。根部选择仍在运行的其他平台，使用构造时的管理员配置快照去重目标并记录一次故障日志。Platform.BeginClose 只停接入，通知保留到最终 Close；Manager 关闭时停止接纳、取消排队与在途工作，等待回调实际退出，等待超时可重试。

CLI client.go 提供 `RunClient(ctx, rawConfig, configDir, name) ClientResult` 和自身 ClientCompleted／ClientFallback／ClientFailed 分类，根部只转换为公开结果，回退规则见上节。本机判断只接受有效 WebSocket URL 的 localhost 或回环 IP；已连接后断线结束 TUI 并返回失败，不自动重连或回退。客户端统一收回读循环、展示与补全等待，保留独立错误。

Share 的 Runtime、MessageHandler、PlatformAPICaller 和 NotificationReceiver 已定义；运行与入站契约位于 runtime_contracts.go，共同配置及最小历史契约位于 config.go、history_contracts.go，可选原生 API 接口位于 adapter_api.go，不并入 Runtime。Share 不实现生命周期状态机、协议映射或发送调度；CLI 已接入运行、入站与通知契约，Headless 已接入无状态运行契约，OneBot 与 Telegram 已接入运行、入站、通知与原生 API，QQ Official 已接入运行、入站与通知。

```mermaid
flowchart TD
    App --> S["supervisor.go / deps.go：总管与借用依赖"]
    S --> W["wiring.go：构造、注入与资源所有权"]
    S --> L["lifecycle.go：启停与最终清理"]
    S --> A["agent.go：输入转换与展示订阅"]
    S --> P["contracts.go / send.go / public_api.go：公开事实、发送与媒体解析"]
    S --> C["client.go / config.go：结果转换与配置汇总"]
    W --> I["适配器、dispatch、notification、refcontext：内部实现"]
    P --> I
    I --> Share["share：内部协作契约"]
    A --> Provider["Agent / Media 根部：接收方契约"]
```

历史链路使用 Storage 的 `ChatMessage`、查询契约及 `ErrNotFound`；`storage/chat_history.go` 的 `ChatSegment`、`ChatReply` 和编解码函数维护持久化事实。适配器将内部片段转换后直接入库；Tool 根部查询并转换为内部消息，Builtin 仅负责展示。Platform 不保留历史查询、清理包装或历史专用公开类型。Media 的 `PlatformMedia`、`HistoryMessage` 只携带来源和历史身份；批量获取接受包含目标平台的解析函数。根部 `ResolveMedia` 转换参数并转发，dispatch 复用已有平台注册表选择解析器；未知或不支持的平台返回错误，不回退，不另设总管路由表。历史范围规则只在 Tool 历史组件校验，SQL 对昵称执行字面包含匹配。

## 验证与备份

Storage、Media、Tool 根部／历史／媒体／Builtin、Agent 输入／执行／上下文／输出及 Platform 的 share／refcontext／dispatch／notification 相关回归已通过。覆盖真实 SQLite 历史范围、引用与媒体顺序、缓存及获取额度、延迟物化、输入快照、前台接管取消链、后台静默与显式投递。Agent 根部生产文件与内部测试已单独通过；Agent–Hook 集成仍需整包回归。Platform 根包已通过整包 race 测试；App 完整验证仍待媒体／模型构造入口和进程入口适配；Background 根包及全部子包已通过 race 回归。组件或文件级验证不替代整包接线验证。

CLI 整包与 Share、refcontext、dispatch、notification 的 race 回归通过。CLI 覆盖真实管道和 Linux 伪终端的停止／最终关闭、真实 WebSocket 收尾发送、同步监听失败、并发启停及超时重试、原连接回复、事件乱序与绑定失效、WebSocket 背压期间换绑和展示关闭后连接保留、UI 关闭超时重试、回调收尾期间取消观察仍返回已发布故障、部分 UI 所有权、客户端分类及断线退出。CLI 不再依赖旧 agent/events、Platform 根包或兄弟实现；Headless 整包也通过 race，覆盖无状态生命周期、取消上下文、重复及并发调用、发送失败契约；OneBot、Share、refcontext 的 race 回归通过，覆盖分阶段关闭、临时拨号取消、在途写入取消隔离、关闭重试、重连隔离及原生 API 透传；OneBot 不再依赖根包或兄弟实现。QQ Official、Share、refcontext、notification 的 race 回归通过，覆盖实际 HTTP／WebSocket、心跳序号同步及失败退出、恢复与重新鉴权、并发启停、取消观察与独立故障、收尾发送、完整发送及别名入库等待、关闭超时重试。QQ Official 不再依赖根包或兄弟实现。Telegram、Share、refcontext、notification 与 Background/Cron 的 race 回归通过，覆盖异步远端连接、分阶段关闭、完整操作取消与退出等待、流式等待取消隔离、关闭超时重试、独立连接池释放、身份快照、命令目录并发、引用恢复、配置边界及原生 API。Telegram 生产代码与包内测试均不依赖 Platform 根包或兄弟实现；Background 报告测试通过发送契约注入回执。

媒体来源转换、回执顺序与重复关联、部分成功和缓存失败测试位于 dispatch/media_receipt_test.go，使用真实媒体中心与 SQLite；身份缺失覆盖存储校验告警及发送结果保留。Agent 的 SendOutputs 日志测试直接注入发送契约，只验证来源日志透传；回执日志格式由 dispatch 验证。

dispatch、Share、OneBot、Telegram、refcontext、Agent/execution 与 Hook/rules 的 race 回归通过。覆盖显式平台原生转发、参数与结果保留、错误及取消传播、调用期间注册表可写，以及媒体回执函数注入、部分发送、缓存失败不重发和临时资源释放。Platform 根包、notification、Share 与 Agent/execution 的 race 回归通过。通知覆盖故障文案与显式目标、异步返回、慢平台隔离、失败不重试、空目标和取消上下文；根部接线覆盖管理员配置快照、去重、排除故障及已停止平台、停止接入后继续投递和最终释放订阅；关闭覆盖取消排队及在途工作、等待实际退出、超时重试。Elvena、Background 根包及全部子包、dispatch 的 race 回归通过，覆盖原生调用 JSON、平台选择、参数与上下文透传、HTTP 顺序执行、失败及取消后停止、calls-only、目标转换和正文组装，以及真实 SQLite 去重恢复、报告回执、媒体引用与借用资源生命周期。

`environment_test.go` 覆盖配置路径优先级、模式验证、相对路径与符号链接、配置替换保存、不同配置并行、取锁失败，以及 TUI／Service 各组合的跨进程互斥。子进程正常关闭和被终止后，锁文件保留且可重新取锁。环境测试可独立运行：`go test internal/app/environment.go internal/app/environment_test.go`。

资源测试覆盖部分失败所有权、历史库构造失败后保留共享存储和提前取消，不在 App 验证模型协议或重载内部实现；这些构造测试需待媒体／模型入口落地后运行。退出测试覆盖结果分类、执行顺序、普通失败后继续、共享期限、超时后停止后续步骤、存储关闭失败后仍释放日志，以及历史使用方退出前保留历史库、部分失败仍关闭历史库、退出超时不释放历史库。期限测试使用 `synctest` 虚拟时间。

App 的阶段执行、登记、启动、可选等待和退出测试已在临时包中隔离尚未实现的构造调用并通过 race 验证，使用实际函数、共享资源类型和真实历史 SQLite，不提供假生产总管。覆盖任意数量业务线、独立故障保留及阻塞关闭下的统一期限。Background 的历史清理依赖通过根部直接注入仓库，相关测试及借用资源关闭断言已随整包 race 回归通过。完整 App 流程、资源构造及客户端链路仍需下层接口实现后验证。

Platform 根包整包 race 验证覆盖重复并发启停、未启动及部分失败清理、TUI／Service 差异、取消观察与故障保留、接入退出后保留资源、清理超时续接、普通失败继续及故障通知接线。公开事实测试覆盖存取切片隔离、空值、上下文移除，以及平台事实与公共会话事实互不改写；agent_test.go 覆盖输入转换、延迟解析和公开事实投影。原生 API 跨根部接线、客户端结果和真实历史接线仍需补充集成验证；完整 App 与剩余接线验证见 [任务清单](tasks.md#platform-剩余接线)。

同目录保留 13 份原生产文件和 12 份原测试文件的 `*.go.bak`，不参与构建，未恢复旧装配。
