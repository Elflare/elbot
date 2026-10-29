# 架构优化
总体原则：
1. 从上往下层设计。
2. 把app包梳理，把所有要改动的文件重命名为原名.bak
3. 把要写的文件创建好（名字需讨论）
4. 每个文件只写字段、函数名字和签名和注释，函数内容就写几句注释，大致说他应该干什么，逻辑就直接写pass（字段、函数、签名需讨论）
5. 所有架构搭完后，可以参考对应的bak文件（可能名字要改，但是可以参考），把自己职责相关的内容填进去
6. 先不管下层内容，也不管还没写完代码的报错

## App 包架构梳理

App 的公共流程和 Agent 对接已实现，资源服务及其他业务线按已确定的调用契约接入，下层按照 App 调用实现。职责与契约见 [App 包设计](app-design.md)。

- [x] 按六文件职责组织入口、环境、共享资源、接线、退出和客户端流程。
- [x] 向媒体与模型构造入口交付完整配置，内部初始化由所属服务负责。
- [x] 客户端消费 Platform 的分类结果，不在 App 解析连接地址或重新判断失败性质。
- [x] 退出步骤使用结果枚举和共用批量执行函数，保持统一 30 秒预算与部分失败清理。
- [x] 统一权限管理类型及现行调用方为 `Permission`。
- [x] 测试覆盖资源部分失败、环境参数和退出辅助逻辑；期限测试使用虚拟时间。
- [x] Tool、Platform、Background 根部目标总管与 Platform.RunClient 分类契约已实现；Platform 剩余下层接线见专节。
- [x] 收窄 Agent 的 Hook 边界：声明根包 `agent.Points`，提供 `RegisterHooks`、`StartHookRequest`，业务流程仅调用 Agent 自有扩展点；Hook 仅提供标准管理命令，由 Agent 注册。
- [x] Hook 根包实现 ConnectAgent：注册并持有一组回调，转换业务参数与 Hook Event 并处理结果转换；逐 handler 登记回调只传 context 与名称，不持有 Agent 内部组件。
- [x] Hook 根部 context_to_event.go 负责外部事实转换；pipeline 组织续接、普通规则及错误钩子，构造时注入日志和 runtime 续接能力；注册查询契约与事件辅助逻辑归 share。
- [x] Hook.Start 串行完成初始加载和平台订阅，重复启动等待完成；已补并发启动回归用例。
- [x] Hook 根包持有 PlatformConnected 订阅和逐平台队列，管理全局 Hook 输出与真实退出；全局平台工作不登记 Agent 请求。根包桥接与生命周期用例已通过。
- [x] Hook.BeginClose 保留 Agent 收尾能力；Close 解绑并等待已接纳调用和平台工作，再关闭 runtime，Done 等待实际退出。
- [x] Hook 子包接入根包装配的能力：runtime.Route 接收登记回调并返回来源 LogResult；根部 Loader 创建带日志、无续接的候选 pipeline，control 仅接收候选信息与提交闭包；rules/runtime 消费工具、发送、日志、通知及所需媒体能力，rules 使用按平台名称调用的 API；内部不反向导入根包。
- [x] pipeline.Manager 作为具体执行器，将只读策略传给 rules 逐动作校验；share 保留调用快照、配置与执行模式、查询结果及最小协作接口；runtime 保留运行状态枚举及私有媒体接口。
- [x] Hook 子包不直接依赖兄弟实现；共享输出转换和协议归 share/output、share/protocol，候选准备与提交隔离回归通过。
- [x] 管理命令位于 Hook control，内部使用 share 查询类型；根部 Commands 仅交付标准 Handler。
- [x] Hook 及管理命令测试通过，真实 Agent–Hook–Tool 接线覆盖命令、补全及输出回调。
- [x] 完成媒体 NewMediaCenter 下层构造、公开契约与消费者接线。
- [x] 完成模型 NewModelManager 的完整配置构造、部分失败交回与关闭契约。
- [x] TUI／Service 按规范化配置路径取得独占锁，退出最后释放；纯客户端不占锁。
- [ ] 适配进程入口的新 App API 与 `client` 命名。
- [ ] 下层接口补齐后，验证完整资源构造、App 启停和客户端链路。

退出辅助函数与共享资源关闭测试可通过临时 overlay 隔离缺失的下层调用，使用实际函数，不提供假总管；完整应用暂不能构建运行。App 的 13 个原生产文件和 12 个原测试文件保存在 `*.go.bak` 中，不参与构建。

## Tool 执行子包

- [x] Execution 使用 Manager 承担执行流程，Tool 根部转发公开能力；内部直接依赖 Registry、Discovery、State、Share，不反向依赖 Tool 根包。
- [x] Share 提供准备契约；单次调用固定工具对象，统一风险与权限决定，保留批次并发、确认、日志和逐项提交。
- [x] Builtin 的 Config／常驻记忆外部依赖由 Tool 根部适配；长期记忆更新复用公共文件预览，媒体导入参数和 ID 辅助通过 Tool 入口访问。
- [ ] Skill 完成依赖整理后，运行真实工具的集成测试及 Tool 根部整链路验证；文件编辑／撤销批次测试位于 Builtin，配置与常驻记忆真实适配测试位于 Tool 根部。

## Tool 历史与后台管理对接

- [x] Builtin 的聊天工具接入 chat_history；查询范围、过滤和媒体选择归组件，Storage 历史查询及媒体获取由 Tool 根部适配，删除 Builtin 直连历史存储的逻辑。
- [x] Cron 工具通过 Tool 自有的 CronRepository、CronSaveRequest 和 CronInfo 访问根部 bridge；模型校验单独注入，Storage 类型与持久化编码不进入 Builtin 或 Share。
- [x] 聊天历史与媒体的真实 SQLite 集成测试归 Tool 根部，Builtin 测试使用内部组件及契约；移除依赖旧 Runtime 装配的测试路径。
- [x] Tool 根部直接请求 Storage 读写 Cron，转换结果并发布任务定义变更；真实 SQLite 集成测试归 Tool 根部，Builtin 使用内部契约替身。
- [x] Background 根部四文件负责对外门面、借用依赖、装配和生命周期；不公开内部 Runner，Wait 及时上报致命运行故障。
- [x] Background 根部按消费方职责注入最小接口或函数，按需复用 Agent、Tool 等根包公开类型；cronBridge 仅保留存储转换，elnisBridge 仅保留存储与媒体转换，注释采用当前架构原则。
- [x] Cron 使用自有 JobRepository、记录和 JSON 模型，由根部转换 Storage；会话复制与共享接管查询接入 Agent，移除旧 Service 管理入口。
- [x] Elnis 使用独用 EventRepository、Media 契约与发送、平台 API 函数；根部转换存储及媒体类型，共用回执映射归 share。
- [x] Agent 根包提供后台执行、会话复制与过期清理；复制元数据使用 JSON 原始值，输入组件通过最小能力完成准备。
- [x] 历史库归 App，Platform 直接写、Tool 直接读；Background 清理直接注入历史仓库 DeleteBefore。Platform 公开发送与 API 的剩余接线见下节。
- [x] Tool.ObserveCronChanged 接入 Cron.RefreshJob；通知只登记名称，启动与运行期串行重新读库，覆盖修改、禁用、删除、同名重建及读取期间再变更。
- [x] 维护组件使用会话、历史清理函数及媒体 Cleanup 最小接口；共享维护配置由 share.MaintenanceJob 表达，Cron 适配回调与任务配置，根部负责接线。历史引用在媒体清理时统一对账。
- [x] SetupCron 只配置维护任务，由根部显式启动 Manager；注册、配置、清理结果传递和配置不启动调度的回归已覆盖。
- [x] Cron Service.Start 前按平台合并最新通知且不启动 worker；启动后独立队列消费，覆盖重复启动、取消、关闭清缓存、等待真实回调及 Close 超时。
- [x] Background 同步启动就绪、停止接入及实际退出契约已接通；总管测试覆盖部分构造、并发启动、禁用恢复、监听失败、借用资源所有权及阻塞订阅退出。App 验证统一清理期限。
- [x] Cron 运行记账仅写 LastRunAt、UpdatedAt，SQLite 原子累加次数；不覆盖启用状态、下次运行时间或同名重建任务。
- [x] 真实 SQLite 验证根部 Cron 转换、旧令牌回执映射、报告完成 JSON，以及 Tool 公共通知到调度的链路；测试随 Background 整包运行。
- [x] Elnis 使用最小事件／报告记录和独立写入请求；真实 SQLite、媒体中心及事务集成测试归 Background 根部，Elnis 行为测试使用自身契约替身。

## 历史直读直写与入站迁移

- [x] App 构造并持有独立历史库，向各线注入实际需要的读写、身份查询或清理能力；业务使用方退出后再关闭历史库。
- [x] Storage 定义 ChatSegment、ChatReply 与编解码；远程适配器直接入库，Tool 根部直接查询并转换内部消息，删除 Platform 历史包装和旧公开类型。
- [x] Agent.HandleMessage 接收自有 MessageRequest；输入、唤醒、媒体物化与历史关联使用自有契约，生产代码不导入 Platform。
- [x] Platform.RestoreMessageContext 由 Agent 根部注入，后台入口清除继承的平台状态，前台接管恢复平台事实与路由；普通后台输出和默认通知静默，显式目标投递保留。
- [x] Platform 根部历史能力裁剪为写入、单条身份查询和两项别名方法，refcontext 只接收单条查询；删除总管中转字段。OneBot、QQ Official、Telegram 已使用自有最小 Deps，Share 提供 CommonConfig 与最小历史读写契约；三个远程适配器均已移除完整或闲置 Store。Telegram 只借用 Append 与引用恢复函数，旧共享 AdapterDeps、旧构造及完整历史字段已删除，不增加兼容层。
- [x] 本轮 Agent、Tool、Media、refcontext、dispatch 测试夹具接入新契约；相关回归通过，覆盖真实 SQLite 范围与昵称匹配、引用及媒体顺序、缓存与额度、入站切片隔离和延迟物化、接管取消链及后台静默／显式投递。Agent 根部内部测试单独通过；App 实际生命周期与历史库清理的隔离 race 回归通过。
- [x] 模型构造接入后，App 整包和资源构造测试通过 race；Agent–Hook 真实接线及 Background 历史清理借用所有权回归通过。

## Platform 剩余接线

- [x] Share 定义 Runtime／MessageHandler、可选原生 API 与 NotificationReceiver 契约；NotificationTarget 保留来源回复路由及显式目标转换行为。各适配器已接入内部契约。
- [x] CLI 本地 Adapter 与 RemoteServer 接入 share.Runtime、MessageHandler；真实管道、Linux 伪终端和 WebSocket 覆盖同步启动、并发启停、接入停止后保留发送、实际退出及超时重试；取消观察时保留回调收尾前已记录的独立故障。
- [x] Headless 接入无状态 share.Runtime，删除旧 Run 和反向依赖；保留 Service 主发送占位，生命周期、取消上下文、重复及并发调用和发送失败契约通过 race 回归。
- [x] OneBot 接入 share.Runtime、MessageHandler、Share 通知与原生 API，删除旧 New／Run 和反向依赖。race 覆盖停止接入后的发送、临时拨号取消、已建立临时连接保留、在途写帧取消隔离、关闭重试、重连隔离、原生 API 透传及既有消息行为。
- [x] QQ Official 接入 Share 运行、入站与通知契约及自有最小 Deps；移除旧 New／Run、完整历史字段、闲置配置与非法依赖。race 回归覆盖网关重连分类、心跳同步与退出、并发启停、独立故障保留、停止接入后的发送、在途 HTTP 取消及别名入库等待、关闭超时重试、令牌刷新等待取消与 401 单次重试。
- [x] Telegram 接入 Share 运行、入站、通知和原生 API 契约；使用自有最小 Deps，删除旧 New／Run 与 share/adapter.go。race 覆盖异步连接、并发启停、实际退出、收尾发送、完整操作取消、关闭超时重试、独立连接池清理、可取消流式等待、身份快照、命令目录并发、引用与历史、配置边界及 原生 API 透传。
- [x] CLI 移除旧 agent/events 与 Platform 根包依赖，接入自有事件、ConnectUI 和函数式补全；通过事件版本、部分 UI 所有权、订阅释放、后台静默和原连接回复回归；换绑清除旧待发状态，在途发送按序完成，真实 WebSocket 背压覆盖换绑和展示关闭后连接保留，UI 关闭可超时重试。命令信息由补全入口消费，不保存闲置目录。
- [x] CLI 客户端实现分类结果，根部仅转换；验证指定连接、默认本机拨号失败回退、远端与认证失败不回退、取消及已连接后断线退出。CLI 与四个相关组件通过 race，跨根部接线仍待整包验证。
- [x] dispatch.Router 按显式平台转发 CallPlatformAPI；通用发送、流式与媒体能力保留固定方法，引用恢复沿用入站链路。媒体回执通过根部注入 SaveMediaOutput；删除字符串能力分发表、撤回／禁言／退群映射及 dispatch 旧 Elvena 执行代码。
- [x] notification.Manager 实现 NotifyPlatformFailure，复用既有异步信号；根部选择存活平台和去重管理员目标。Platform 根包、notification、Share 与 Agent/execution 的 race 回归通过，覆盖实际路由、停止接入后通知、取消排队及在途工作、等待回调退出和超时重试；旧上下文测试已按当前公开事实契约清理。
- [x] Elvena Call 只包含 Platform／API／Params；Elnis direct 按既有平台选择规则直接转发原生调用，保留顺序执行、遇错停止及 calls-only 行为，无能力映射或兼容包装。Elvena、Background 根包及全部子包、dispatch 的 race 回归通过，覆盖 HTTP 原生请求、失败与取消、真实 SQLite 接线、去重恢复和借用资源生命周期。
- [x] Event 由 Elnis/types.go 定义并注释字段用途，不保留闲置 SegmentPaths；targets.go 的 toDeliveryTarget 与 media.go 的 directText 承担投递准备。Elvena 仅保留协议与来源文件，无闲置结果类型、总线或投递辅助文件。
- [ ] 更新 Elwisp 生成提示与相关用户示例，停止生成已删除的能力名；保留 Hook platform.call 原生链路，平台选择仍由调用方负责。用户文档随对应迁移阶段更新。
- [x] 删除旧 platform.go、module/、builtin/ 及旧实现测试，不保留兼容别名；Doctor 使用外部测试包，通过 Platform 根部 ConfigDefinitions 获取配置定义；Platform 根包及全部子包、Doctor 的 race 回归通过。
- [ ] 补充 Platform 原生 API、客户端结果与真实历史接线集成验证；App 整包已通过 race，进程入口适配后仍需完整应用回归；组件隔离测试不替代真实总管接线和整包通过。

## 媒体中心对接

Media [根部与下层契约](app-design.md#media-根部与下层契约)、四个子包与消费者接线均已实现。模型构造及 App 整包已通过 race；全仓编译仍有 launcher 旧运行模式常量引用和 security 测试角色类型不匹配，这两项不属于媒体改造。

- [x] 保留 15 个生产文件和 12 个测试文件的 `.bak` 备份；按根部与 object／transfer／backend／share 组织实现。
- [x] 声明 MediaCenter、完整配置构造、四字段值类型 MediaInfo、Info／ValidateImage、Reference 及公开 HeldMedia。移除旧入口，不提供兼容别名。
- [x] 声明 object 最小仓储、调用期 History 参数、内部持有 SignedURL、后端位置接口与可返回错误的交付清理回调；gofmt 和五个 Media 包的声明编译检查通过。
- [x] 实现根部公开入口、类型转换、持有包装、参数 ID 解析与完整配置装配；历史错误共用身份，保留部分结果、清理回调和错误。凭据与构造提前失败测试通过 race。
- [x] 实现下层后端构造与 object 中断恢复；验证成功装配、参数 ID 完整执行及构造后注入 History 的对账。
- [x] 实现导入、查询、图片校验与处理、对象持有及引用释放重试、远端副本和签名缓存、历史关联及物理回收。
- [x] 实现模型请求准备、平台来源及发送准备、历史批量获取，以及部分准备失败和可重试清理。
- [x] App、Agent、Tool、Hook、Platform、Background 使用 MediaCenter／Info／MediaInfo／HeldMedia 与 func() error 清理契约；Tool 保留调用内集合与导出缓存。
- [x] 外部格式保持，Hook、Elnis、后台报告在入口一次分类；内部传递明确来源或 MediaID，公开导入统一接收逐次 ImportLimits。
- [x] Cron 持久化明确来源、按同结构恢复发送；SQLite 引用触发器与审计按 Source.media 工作，替换和失败回滚回归通过。
- [x] 真实存储、持有及释放重试、对象并发、历史批量与对账、签名缓存、消费者及总管接线回归。

## Background 目录与依赖

- [x] 根部按总管、依赖、装配、生命周期四文件组织；Cron 独用数据契约放在 cron 子包；内部业务收进 execution、cron、elnis、maintenance，共享定义与辅助能力归 share。
- [x] 移除旧 module 和 Background 持有的 Cron 工具注册；公共结果使用方依赖 share。
- [ ] 完成 Platform 剩余下层公开能力接线，验证完整 App 总管接线。
- [x] Elnis 与 Background 整包构建，实际 elnisBridge 与 cronBridge 运行真实存储集成测试。
- [x] Elnis Runtime 生命周期与 Supervisor 测试已接通；Background 全包通过 race 检查，真实 SQLite 覆盖关闭批量终态、媒体引用与 outbox 保留。

## Agent 边界接入

- [x] 公开 Hook 载荷与订阅事件独立定义在根部；内部共享循环、提交、消息与 Hook 契约放 Share，具体实现留所属子包。
- [x] 子包不依赖兄弟实现；根部注入实际需要的外部能力、模型选择函数与 Workspace 工厂。
- [ ] 入站契约迁移后，在 review 阶段重新验证 Agent 子包。
- [x] Hook.ConnectAgent 使用 agent.Points 与根包嵌套类型；Agent 通过 Commands 注册 Hook 命令，管理结果留在 Hook 内部。
- [x] Command 与 Completion 使用 Tool 根包查询契约；删除无生产调用的 Notification HookFailure 辅助与测试。
- [ ] 入站契约迁移后，在 review 阶段迁移 Supervisor 测试夹具并验证整链路及订阅生命周期。
- [x] Agent 实现 RunBackground、CopyBackground、CleanupSessions，input 承接后台准备；调用方准备目录与后台来源，仍负责报告解析和重试。
- [x] Elvena 请求、Elnis 工具声明及 Background 共享请求统一使用 Tool 根包类型；Runner 仅准备目录、注入后台来源并调用 Agent.RunBackground，工具声明 JSON 排除内部关联字段。

## modelmgr
- [x] 根包 ModelManager 接收完整配置，自行构造客户端、请求参数与 providers 重载；删除旧 Options／New 工厂入口。
- [x] 重试信号改为观察入口，消费者持有连接；部分构造结果与关闭超时续接已验证。
- [x] 模型、App 与相关消费者通过 race；跨包测试迁移到真实本地 HTTP 和 providers 文件。

## session包
- [x] binding 实现绑定身份、版本、条件及批量解绑、可取消重入准入、空闲锁项回收、锁外事件与可续等关闭；binding／share race 回归通过。
- [ ] 重构对接新代码
- [ ] control 阶段实现事件驱动的绑定局部定时器：独立于 UpdatedAt，激活、输入及执行真实收尾续期，忙碌时不清理；到期解绑，下一条输入再创建。
- [ ] branch 经 share.BindingCoordinator 接入 control 的切换编排；根部注入权限查询与同步释放，补齐执行快照、自动确认、撤销记录及命名缓存回收。已在对应文件留 TODO，完整约定见 [Session 设计](app-design.md#主动过期与跨模块释放待对应阶段实现)。

## 后台工具统一预载

统一入口为 Tool 根部 `Supervisor.PrepareBackground(ctx, tool.BackgroundPreloadRequest)`，返回 `tool.BackgroundPreload`。请求包含 `SessionID`、`Names`、`AllowedToolNames`、`ELwispTools`；本地名称可指向工具、Skill 或标签，外部声明沿用 `tool.ELwispToolDeclaration`。Agent 提交返回的 `Update`，预载不写会话状态；Background Runner 通过 Agent.RunBackground 触发准备与执行。

- Cron、Elnis 及以后新增的后台任务，统一由 Agent 调用 Tool 根部 `PrepareBackground` 准备工具；不得直接访问 Tool 内部 Registry、PreloadService 或另建工具获取入口。
- [x] Cron 提供本地预载名单；Elnis 授权并适配 `ELwispTools`，补齐内部字段 `CanonicalName`、`EventKey`。
- [x] Background Runner 只接入 Agent.RunBackground，Elnis 只借用 Tool.BackgroundSelections；新会话由 Agent 预载并提交，续跑沿用已提交集合。
- [x] State 统一保存完整 `DiscoveredTool` 声明，删除名称补查目录路径；后台旧声明转换入口已移除。
- [x] Cron／Elnis／Background 内部接线与存储链路通过整包测试；App 通过 Tools 交付 Tool 总管，内部能力分配由 Background 负责。共享资源构造与 App 整包已通过 race；完整进程链路仍待入口适配。


## 不一定实现

- [ ] CLI TUI 支持 Markdown 渲染。
- [ ] CLI 用户名和助手显示名可配置。
- [ ] CLI 支持本地图片输入。
- [ ] 周期 Cron 支持过期时间。
- [ ] 从公共 config 包获取命令前缀，提示消息自动组装。


## 暂缓

- MCP 接入：暂不考虑，使用 Skill。
- 子 Agent：暂不考虑，信息交流会带来噪声或损失。
