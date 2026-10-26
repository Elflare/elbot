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
- [ ] 实现 Tool、Platform、Background 的目标总管及 Platform.RunClient 结果契约，补齐跨线接线。
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
- [ ] 实现媒体 NewMediaCenter、模型 NewModelManager 的构造契约。
- [ ] 实现按配置区分的服务独占运行占用。
- [ ] 适配进程入口的新 App API 与 `client` 命名。
- [ ] 下层接口补齐后，验证完整资源构造、App 启停和客户端链路。

退出辅助函数与共享资源关闭测试可通过临时 overlay 隔离缺失的下层调用，使用实际函数，不提供假总管；完整应用暂不能构建运行。App 的 13 个原生产文件和 12 个原测试文件保存在 `*.go.bak` 中，不参与构建。

## Tool 执行子包

- [x] Execution 使用 Manager 承担执行流程，Tool 根部转发公开能力；内部直接依赖 Registry、Discovery、State、Share，不反向依赖 Tool 根包。
- [x] Share 提供准备契约；单次调用固定工具对象，统一风险与权限决定，保留批次并发、确认、日志和逐项提交。
- [x] Builtin 的 Config／常驻记忆外部依赖由 Tool 根部适配；长期记忆更新复用公共文件预览，媒体导入参数和 ID 辅助通过 Tool 入口访问。
- [ ] Skill 完成依赖整理后，运行真实工具的集成测试及 Tool 根部整链路验证；文件编辑／撤销批次测试位于 Builtin，配置与常驻记忆真实适配测试位于 Tool 根部。

## Tool 历史与后台管理对接

- [x] Builtin 的聊天工具接入 chat_history；查询范围、过滤和媒体选择归组件，Platform 历史及媒体获取由 Tool 根部适配，删除 Builtin 直连历史存储的逻辑。
- [x] Cron 工具通过 Tool 自有的 CronRepository、CronSaveRequest 和 CronInfo 访问根部 bridge；模型校验单独注入，Storage 类型与持久化编码不进入 Builtin 或 Share。
- [x] 聊天历史与媒体的真实 SQLite 集成测试归 Tool 根部，Builtin 测试使用内部组件及契约；移除依赖旧 Runtime 装配的测试路径。
- [x] Tool 根部直接请求 Storage 读写 Cron，转换结果并发布任务定义变更；真实 SQLite 集成测试归 Tool 根部，Builtin 使用内部契约替身。
- [x] Background 根部四文件收敛为对外门面、七项依赖、装配和生命周期框架；移除内部 Runner 的公开转发及 Wait，内部接入按目标调用标注 TODO。
- [x] Background 根部按消费方职责注入最小接口或函数，按需复用 Agent、Tool 等根包公开类型；cronBridge 仅保留存储转换，elnisBridge 仅保留存储与媒体转换，注释采用当前架构原则。
- [ ] 落实 Background 子包消费契约和根部存储、媒体转换：Cron 接入会话复制函数；维护组件接入会话、历史清理函数和媒体清理接口；Elnis 接入发送、平台 API 与模型选择。跨业务不引用内部子包或 share，本业务兄弟子包通过 share 协作。
- [x] Agent 根包提供后台执行、会话复制与过期清理；复制元数据使用 JSON 原始值，输入组件通过最小能力完成准备。
- [ ] 实现 Platform 的公开发送、API 和历史清理能力，并接通 Background 子包对 Agent 会话复制及清理方法的调用。
- [ ] 接通 Tool.ObserveCronChanged 到内部 Cron 的刷新入口；初次任务加载前建立订阅，协调启动期间的变更并重新读库刷新调度。
- [ ] 收口维护配置与 Cron 启动职责：SetupCron 只配置任务，移除其内部 Manager.Start 调用，由根部显式启动 Manager。
- [ ] 实现 Cron Service.Start 与平台通知缓存契约：Start 前不触发 worker，前置步骤成功后由根部启动消费者；消费者就绪即可返回，不等待缓存任务执行完。验证启动前不执行、成功后消费、失败或关闭后不放行、关闭清理缓存及重复启动不重复创建消费者。
- [ ] 实现 Background 内部同步启动就绪、停止接入及实际退出契约，迁移旧总管测试后验证部分构造失败、并发启停、订阅释放和统一清理期限；当前不测试未实现的内部流程。
- [ ] 收口 Background 内部的持久化类型与读取边界，移除内部模型对 Storage 元数据类型的别名；完成后验证 Cron 通知到调度的整链路。

## 媒体中心对接

Tool 媒体层已统一向工具返回 `share.MediaInfo`，图片宽高、实际格式、存储位置和清理状态不属于工具契约。媒体中心后续使用自身公开类型，不能反向依赖 Tool 或其 Share。

- [ ] 整理媒体中心的公开结果：通用媒体信息与内部存储记录分离，查询、导入及历史媒体结果不泄漏后端和对象位置；Tool 在自身入口转换为 Share 数据。
- [ ] 明确图片有效性校验接口，保持内容可解码及 MIME 校验语义；当前 Tool 的 ValidateImage 使用中心 ImageMetadata。图片宽高和实际格式继续在媒体中心内部消费。
- [ ] 收敛调用期引用与已持有媒体的读取能力：支持同一调用复用、失败释放重试及中断恢复；Tool 当前通过中心公开方法管理临时引用，内部持有存储记录供 OpenHeld 使用。
- [ ] Background／Elnis 整理媒体对接，移除旧 Tool 根部 MediaRuntime 引用，不能改为依赖 Tool 内部媒体包。

## Background 目录与依赖

- [x] 根部按总管、依赖、装配、生命周期及 Cron 数据契约拆分；内部业务收进 execution、cron、elnis、maintenance，共享定义与辅助能力归 share。
- [x] 移除旧 module 和 Background 持有的 Cron 工具注册；公共结果使用方依赖 share。
- [ ] 实现根部所消费的 Platform 公开能力及 Background 内部目标接口，验证 App 已声明的总管接线。
- [ ] 修复已确认的子包编译阻塞：execution 仍将 Tool share 状态交给使用 Tool 根包类型的 Agent 辅助入口，Elnis 仍引用已删除的 tool.MediaRuntime；按目标边界迁移后继续验证根部接线与整条业务线。

## 后台工具统一预载

统一入口为 Tool 根部 `Supervisor.PrepareBackground(ctx, tool.BackgroundPreloadRequest)`，返回 `tool.BackgroundPreload`。请求包含 `SessionID`、`Names`、`AllowedToolNames`、`ELwispTools`；本地名称可指向工具、Skill 或标签，外部声明沿用 `tool.ELwispToolDeclaration`。Agent 提交返回的 `Update`，预载不写会话状态；Background Runner 通过 Agent.RunBackground 触发准备与执行。

- Cron、Elnis 及以后新增的后台任务，统一由 Agent 调用 Tool 根部 `PrepareBackground` 准备工具；不得直接访问 Tool 内部 Registry、PreloadService 或另建工具获取入口。
- [x] Cron 提供本地预载名单；Elnis 授权并适配 `ELwispTools`，补齐内部字段 `CanonicalName`、`EventKey`。
- [x] Background Runner 只接入 Agent.RunBackground，Elnis 只借用 Tool.BackgroundSelections；新会话由 Agent 预载并提交，续跑沿用已提交集合。
- [x] State 统一保存完整 `DiscoveredTool` 声明，删除名称补查目录路径；后台旧声明转换入口已移除。
- [ ] 完成 Cron／Elnis／Background 整链路测试；当前受 Elnis 旧媒体类型及尚未实现的目标契约阻塞。App 已通过 Tools 交付 Tool 总管，内部能力分配由 Background 负责。

# 任务清单

- [ ] CLI TUI 支持 Markdown 渲染。
- [ ] CLI 用户名和助手显示名可配置。
- [ ] CLI 支持本地图片输入。
- [ ] 周期 Cron 支持过期时间。
- [ ] 从公共 config 包获取命令前缀，提示消息自动组装。

## 暂缓

- MCP 接入：暂不考虑，使用 Skill。
- 子 Agent：暂不考虑，信息交流会带来噪声或损失。

## Agent 边界接入

- [x] 公开 Hook 载荷与订阅事件独立定义在根部；内部共享循环、提交、消息与 Hook 契约放 Share，具体实现留所属子包。
- [x] 子包不依赖兄弟实现；根部注入实际需要的外部能力、模型选择函数与 Workspace 工厂。
- [x] Agent 的 11 个子包测试通过。
- [x] Hook.ConnectAgent 使用 agent.Points 与根包嵌套类型；Agent 通过 Commands 注册 Hook 命令，管理结果留在 Hook 内部。
- [x] Command 与 Completion 使用 Tool 根包查询契约；删除无生产调用的 Notification HookFailure 辅助与测试。
- [x] Agent 根包编译通过，Supervisor 整链路及订阅生命周期回归通过。
- [x] Agent 实现 RunBackground、CopyBackground、CleanupSessions，input 承接后台准备；调用方准备目录与后台来源，仍负责报告解析和重试。
- [x] Elvena 请求、Elnis 工具声明及 Background 共享请求统一使用 Tool 根包类型；Runner 仅准备目录、注入后台来源并调用 Agent.RunBackground，工具声明 JSON 排除内部关联字段。
