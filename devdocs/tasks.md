# 任务清单

## 信号底座与通知统一

实施顺序为信号底座 → 现有通知管理器 → 平台和业务迁移。设计与接口见[信号执行与结果](signals-and-logging.md#信号执行与结果)，职责图见[输出与发送](architecture.md#输出与发送)。

- [x] `Signal[T,R]`、同步实际结果、异步 Emission、每信号有界并发、按 key 排队、超时及真实退出；无等待 worker 的结果转交。
- [x] 通知实体、现有 Manager 全局订阅与每平台信号；平台只订阅发送，满队列 WARN，保留原来源和部分回执。
- [x] 业务全局信号发布；删除旧通知对象、发送入口及通知依赖注入；Chat 承接 Hook 正文、Cron／Elnis 报告和媒体。
- [x] 原子订阅快照、精简重复 context／结果分配、独立有界诊断；正确性、取消、隔离、资源上限与性能测试覆盖。
- [x] 全仓正常回归及 race 通过；最终诊断、平台和 Hook 取消改动另做定向 race 回归。完成独立性能复测，结果如下。


## 信号与日志系统整改

详细设计见 [信号与日志系统设计](signals-and-logging.md)。本文集中记录实施进度、待办、验收与验证结果。

状态：步骤 1、步骤 2 和步骤 3（3.1／3.2）已完成。来源结果、业务与日志分离、Hook 统一发布、调用与发送独立记录，以及本次输入拒绝、正文限长和进程取消边界修复已落实并完成静态复审和已修改路径的定向验证。已确认设计见[设计文档的步骤3](signals-and-logging.md#步骤3)。本轮未运行全仓测试或构建，不自动提交。

### 实施约束

- 按新设计整改实现。Go 修改运行 gofmt 并静态核对调用和契约；只对已完成且已修改的代码运行定向测试，不测试未完成部分，不运行全仓测试或构建。测试代码同步维护，既有通过结果不代替本次验证。
- 以已确认的新设计为准，旧代码只用于定位业务分支和迁移点，不以旧字段或旧行为反推目标契约；发现不符时整改实现。
- 所有日志只用 Category、Level、Module、EventName、Type、Content、ContentLimit、Identity、ResultStatus 九字段；时间由中心自动记录。底层字段注释写明用途、零值和约束。
- 公共日志采用九字段封闭契约。原始错误留在业务中，来源把错误原因和必要诊断写入唯一正文 Content，并显式填写 Identity；日志回调、信号和中心不猜业务结果，日志与 notify 分开。
- 普通日志优先 text，按 Unicode 字符截断，默认 256，使用计入上限的单字符 `…` 标记；需要字段查询和统计的来源填写 JSON 并自行控制体积。JSON 不按 ContentLimit 截断，新记录统一写 JSONL。
- 沿用现有包，接口按已确认契约及步骤 3 结论整改；不新增共享错误分类器、取消判断器、错误树检查设施或任意业务对象转换器。具体名称见[已确认的位置与实施接口](signals-and-logging.md#已确认的位置与实施接口)，未讨论的新名称仍需确认。测试代码的名称和实现可自行决定。
- 保留重试与权限规则、平台连接全局信号、同步业务持久化、队列和共同 30 秒退出预算，不改写历史日志，不自动提交。
- 每完成一部分新代码，立即将 architecture 和 code-map 的对应说明更新为实际实现并删除过时内容，不保留新旧两套说明。未完成项只在本清单跟踪；文档只描述当前契约与职责。此次不修改 CHANGELOG、AGENTS.md 或英文镜像。

| 步骤 | 工作内容 | 验收条件 |
| --- | --- | --- |
| 1 | 先更新设计及任务文档，再整改公共日志底层 | 九字段及注释完整，枚举受验证，text／JSON 统一写入 JSONL |
| 2 | 迁移全部业务来源、相关实例事件、Reader 和查询命令 | 来源明确结果，组合失败不丢失，必要业务 JSON 可查询，普通日志优先 text |
| 3 | 整体代码复审、已确认问题整改与文档收尾 | 字段、分包、方法调用、职责边界和旧链路清理通过静态复审；验证安排另行明确后记录实际结果，文档同步且无矛盾 |

### 步骤 1：契约与公共链路

- [x] 先将全部已确认要求、九字段表、枚举、身份存在性、职责流程图及落盘示例写入 `signals-and-logging.md`，同步调整本清单和设计链接。
- [x] 在现有位置实现九字段封闭契约与定义注释，使用唯一公共发布入口。
- [x] 实现 `LogCategory`、`LogContentType`、`ResultStatus` 整数枚举及 `Valid`／`String`，非法契约返回提交错误并走独立 stderr。
- [x] 实现 `LogIdentity` 五个 `*string`，验证业务显式填写、nil 省略、显式空值保留和发布副本；即使 context 带有身份也不补值。时间由中心接收时、等待入队前记录。
- [x] 保持信号、类别队列和等级过滤，最终按 Type 脱敏，text 按字符限长，JSON 保持有效且数值精确，统一写 JSONL 并检查 64 KiB 硬上限。
- [x] 覆盖枚举、非法 JSON、非负长度、身份、过滤后格式化、Unicode 截断、嵌套脱敏、原生 JSON 正文、记录上限及实际写入；直接检查 JSONL 文件，保留 Reader 历史格式测试、背压和关闭回归。
- [x] 完成 gofmt、events／logging／signal 包测试及相关 race 测试，记录实际验证结果。

### 步骤 2：全部来源迁移

- [x] 明确公共九字段保持单一整体 ResultStatus，具体原因、各操作事实和错误写入 Content，不增加次结果、原因码或统一子结果结构；正文优先 text，查询统计需要时使用 JSON，补充组合记录示例。`/log --level` 仍按 Level 筛选；异常用量不静默按零或排除，统一 null 展示的后续整改见步骤 3。
- [x] 明确并实现命名统一结束事件、`--raw` 独立控制原始展示、普通展示不显示类型，以及损坏 JSONL 行继续查询并提示的行为；同步设计文档和展示示例。
- [x] 逐个检查 Agent、工具、Session、模型客户端与 Model Manager、Hook、平台、发送、Cron、Elnis、媒体、命令、通知、维护及 App 的直接发布点和来源事件。
- [x] 在业务发生成功、取消、拒绝、跳过或失败的位置明确日志结果；使用实例事件的来源传递对应事实。日志回调只按来源事实构造记录，移除从错误链重新猜测结果的逻辑；普通提示与进度允许不声明结果。
- [x] 明确九字段只约束日志信号；业务及 notify 保留专有字段和原始对象。日志由实际操作来源组织，保留独立事实、部分结果与整体结果语义。
- [x] Usage 与协议解析保留字段存在性和整数精度，未知用量不变成零或调用失败；各次业务请求独立完成，保留各阶段和持久化事实。
- [x] 公共 Usage 改为四个可空 int64；Chat 用量解析保留精度、明确零及非法别名回退；Agent／contextmgr 用量快照深复制计数，contextmgr／runtime 展示保留独立未知项。
- [x] Chat 请求与首次系统消息日志改为九字段 text 契约，来源显式填写身份，以 Content 作为唯一正文。
- [x] Responses 用量容错解析与请求日志迁移；压缩状态／存储用量改为可空 int64，新增保留数据的 SQLite NULL 迁移。独立存储迁移测试通过，整包回归通过。
- [x] 模型重试由 HTTP 来源声明结果、Model Manager 发布身份快照，日志回调不再检查取消错误链；媒体初始化与 delivery 通知失败日志迁移九字段契约。
- [x] Session 命名仅使用 Scheduled／Completed 信号，由 NamingCompletedEvent 表达流程结束；覆盖准备失败、回退、用户抢先改名和关闭时终态发布。
- [x] 工具日志完成公共九字段基础迁移，批次失败不伪造逐工具完成记录；业务与日志分离及发布职责迁移在 3.1 实施。
- [x] 工具业务解析和原生／远程执行保留 ResultStatus、Error、ExecutionStarted、部分结果及策略拒绝事实；预载日志采用九字段及明确 skipped。独立日志正文和调用日志结果按 3.1 新契约迁移，不投影业务状态。
- [x] 迁移回复发送／提交日志；业务所需返回值和事件保留专有字段。保留发送／保存顺序和部分回执，流回执合并，媒体缓存失败只降级 WARN，不重发。
- [x] 工具业务解析保留策略拒绝、查找和准备失败的原始事实，覆盖 Runner 与直接执行入口；日志结果与等级按 3.1 独立调用边界整改。
- [x] 修复调用取消掩盖持久化失败，以及调用成功、保存失败被误报上游失败的链路；保留 Chat 部分输出且返回空错误时的真实结果。
- [x] 组合记录基础迁移保留独立操作事实、原始错误与部分结果；单条记录仅有一个所属操作的整体结果，未执行事实不补造。命名兜底标题保存成功时整体成功；工具调用与后续操作的独立记录按 3.1 整改。
- [x] 所有来源填写九字段并显式提供有关联的 Identity；原始错误留在业务处理链路，日志正文写明错误原因和必要诊断；日志与 notify 各自组织输出。
- [x] 普通运行日志和组合操作说明优先 text；`llm_usage`、需要业务字段筛选的审计记录及 Elnis 业务事件使用来源构造的 JSON，来源先控制 JSON 体积。
- [x] 迁移 Reader 兼读 JSONL 和历史格式，迁移 `/log`、`/audit`、`/elwisp`、`/usage`；按 Type 解析，JSON 业务条件在计入数量前匹配，不覆盖外层公共字段。异常用量按项传播到分组／总计，不默默当零或排除；未知项统一保留 null 的展示整改见步骤 3。
- [x] 按已确认 LogQueryResult／LogEntry／LogQuery 迁移 Reader 与消费者；损坏 JSONL 继续查询并提示实际扫描的数量与结果可能不完整，坏行不参与统计或有效数量上限。Raw 坏行最多 10 条整行且总计 256 KiB，标注无法判断是否匹配并报告省略数量。
- [x] `/log`、`/audit`、`/elwisp` 新增 `--raw`，与等级筛选独立；`-d/-i/-w/-e` 和 `--level` 仅设置最低等级。普通展示时间、等级、事件、内容和结果，不显示类型；正文独立排版、JSON 格式化、未声明结果明确展示。同步帮助、补全、中文命令文档及解析／展示测试。
- [x] 保留重试次数、延迟与回退等业务事实，重试机制仍归客户端；回归无通知订阅者不漏日志、过期重试通知不显示、Hook 原始失败不重复记录。
- [x] 通过真实业务入口验证各操作结果及错误原因，再验证落盘等级、正文、身份及查询；运行相关包测试，整仓回归通过，整体复审归步骤 3。

### 步骤 3：整体复审、整改与收尾

具体要求及职责图见[设计文档的步骤3](signals-and-logging.md#步骤3)。下列逐项标记本轮代码落实状态；勾选不代表执行了构建或测试。步骤 2 的勾选记录已完成的迁移和既有回归，复审发现的缺口在本节关闭。静态复审结合已完成改动的定向测试，未完成部分不测；待办直接维护于本节，验证记录只写实际执行结果。步骤 3 仅拆为下列 3.1 和 3.2，共同复审与文档维护要求适用于两部分。

共同复审与文档维护要求：

3.1 的非 Hook 范围与 3.2 的 Hook 范围均已完成来源、公共链路、字段、分包、方法调用及生命周期静态复审，并结合已修改完成路径的定向验证闭合。

- [x] 同时复查全部来源的业务逻辑、九字段及底层注释、落盘内容、包依赖、文件职责、方法调用和生命周期边界；排除 App 日志转发、Logger 注入旁路及未讨论的新命名，按新设计清理旧逻辑和方法。
- [x] 沿真实业务调用链静态核对成功、纯取消、包装取消、最终超时、拒绝、跳过、重试、最终失败及取消／拒绝伴随独立失败；核对来源事实、终态时机、日志字段和查询消费者。
- [x] 静态核对等级过滤、text／JSON 正文、敏感数据脱敏及整数精度、Unicode 限长与 `…`、64 KiB 上限和外层字段保护；核对错误只读所有权、身份副本、不补 context 及入队前记时。
- [x] 静态核对两类预载跳过事件、审计与用量查询、反向读取与历史格式、损坏行提示、raw 与等级独立、普通展示不显示类型及命名统一终态；复查装配、清理、背压、关闭、stderr、共同 30 秒预算和平台连接消费者隔离。
- [x] 后续代码修改运行 gofmt；只测试已完成且已修改的代码，不测试未完成部分或运行全仓测试、构建；不以既有通过结果代替本次静态复审，也不在仅修改 Markdown 时运行 Go 测试。
- [x] 每部分代码完成后立即同步 architecture、code-map 和受影响中文用户文档，删除旧或矛盾说明；核对设计与实现，检查链接、locator 及禁改说明。本次不修改 CHANGELOG、AGENTS.md 或英文镜像，不自动提交；事项实际完成后再勾选。

#### 3.1 其他 bug 修复

- [x] 删除没有生产订阅者的 PersistenceFailed、ToolCallCompleted、ReplyDelivered、ReplyCommitted，以及仅供日志转换的 ConfirmationChanged、UserInputReceived、TurnTimedOut、ToolDenied 链路及其定义、装配、复制、发射、专用字段和方法；确认来源直接发布，实际持久化和必要业务返回值继续保留。
- [x] 明确后台日志与业务边界：RunBackground 保留业务 RunResult 与 error；模型、回复、工具由所属模块记录，不为日志给 Dialogue、ReplyCommitter、turn.Execution 增加全链返回。Cron／Elnis 不从后台业务错误树重建下层日志；本层报告生成、事件终态及保存分别记录，接管使未执行报告 skipped。保留请求停止与 Cron 补执行规则，已删除共享分类函数，不新增替代分类器。
- [x] Cron Handler 返回独立 LogResult 与原始 error，调度器直接使用来源声明；不新增 canceled 布尔值。删除无生产消费者的 LastError、调度器推导及数据库读写，使用 SQLite 迁移删除 last_error 列；保留计划时间、运行计数、启用与 DeliveryState／DeliveryToken 补执行状态，错误原因写来源 Content。
- [x] 请求登记、调用记录保存、发现状态保存和 ToolCommitter 各阶段返回来源 LogResult 与原始业务 error；Runner 直接组织所属操作记录，不从 error 推导保存结果。维护清理方法直接返回来源 LogResult，Cron Handler 透传；媒体并行删除先保留各操作结果再合并错误，独立失败不被取消覆盖。
- [x] 确认来源直接填写日志结果：通过 succeeded、拒绝 rejected、纯停止 canceled、过期及最终超时 failed，以上使用 INFO；提示发送的独立故障使用 ERROR。业务仍沿原流程执行或停止，日志结果不改变上层终态；等待及命令意图不提前声明终态。
- [x] 普通工具调用只按实际调用边界记录：已运行时，包括缺 key、shell 非零及业务返回错误，调用日志为 INFO／succeeded；未调用起来的调用故障为 ERROR／failed。调用前正常拒绝、取消、跳过使用相应结果及 INFO；业务 Result、原始 error 和 Segments 不因日志结果改写。
- [x] Tool.Call 返回业务 Result、独立 logContent string 和原始 callErr，具体工具不填写日志 Status 或 Level；两处 ExecutionResult 以 LogResult.Content 传递正文，未进入调用由入口声明结果及原因，已进入调用由 Runner 统一填写 INFO／succeeded。含义一致的 Content 可复用，不从 Message／Segments 自动提取。
- [x] internal/toolrun/logging.go 的 logTool 只接收 ctx 与完整 record 并发布；Runner 的局部函数 newRecord 复用基础九字段和当次身份，recordCall 补调用正文、查询事实及体积控制，两类调用记录复用同一次耗时，Execute 不发布日志。调用返回后、后续发送／完成 Hook／保存前发布，context 已取消也保留实际调用记录；其他操作继续按来源 LogResult 组织独立记录。职责图见[工具业务与日志分离](signals-and-logging.md#工具业务与日志分离)。
- [x] 新增 internal/background/logging.go 的 LogBackground，统一 Agent 后台准备与 Cron／Elnis 自身记录的发布；logTool 统一工具发布入口。两者只接收完整九字段，取消后仍发布已发生事实；精简重复正文、身份和双类别组装，模型／回复／工具不重复走后台管理口。
- [x] 工具调用 Content 增加 run_source：foreground／cron／elnis，与工具实现 source 分开。Runner 每次刷新 context 后、调用前固定 runSource；接管前的在途调用保持原来源，接管后的新调用为 foreground，具体工具及发布管理口不判断来源。
- [x] /log 新增 --run_source，复用 ContentFields，与 -t、等级、时间、正文及 raw 取交集；Reader 只匹配 JSON 键或 toolrun/tool_call text 的固定开头标记，旧记录缺失来源不推断。同步参数校验、帮助、选项与值补全及中文命令文档。
- [x] 工具调用与后续发送、完成 Hook、保存分别表达所属操作；保留原始错误及部分回执，不把后续失败反向解释为调用未成功。实际保存日志等保存返回，未执行操作不补结果。
- [x] 非 Hook 来源完成业务与日志返回分离：仅业务接口保留业务结果与 error，需要日志消费者的边界独立返回 LogResult。平台、发送、媒体引用准备、请求登记、记录／transcript 保存、回复提交和关联按所属来源声明结果，Receipt 仅供业务，具体工具只填正文，Runner 统一调用状态。回复与流式提交在错误合并前保留阶段事实，独立失败不被取消覆盖。RunBackground、Dialogue 和 turn.Execution 等已在所属模块记录的业务接口不添加整条日志返回链；Hook 准备／完成桥接归 3.2。
- [x] 完成 Hook 仅处理 ExecutionStarted=true，预检失败、拒绝及执行入口复查拒绝不触发；实际保存操作的日志等待保存返回，不伪造未执行事实。
- [x] 工具输出、回复与流式提交直接使用来源结果，纯取消保留 canceled，独立故障保留 failed，不从合并错误重新分类，不回改已运行的工具调用记录；Hook 准备／完成的来源结果接口与日志迁移统一归 3.2。
- [x] Responses 保留尚未收到 output_item.done 的文本及 refusal 增量；错误或取消时，业务结果、完成事实、日志和响应保存保留已接收部分，不伪造完整响应。
- [x] 补齐 Elnis 运行成功后终态保存失败、媒体准备失败且失败状态保存成功等来源日志；分别保留业务失败和 persistence_error，避免返回错误被 worker 忽略而漏记。
- [x] Hook 工具审计补齐 actor_id：有 Session 时跟随触发 Session 的执行 Actor，无 Session 时从 contextinfo 获取，不借 OwnerID 或默认身份补造触发者；Elnis 失败及组合记录保留已知名称、来源、模式、事件和 tags 等查询字段。
- [x] 修复 CallObservation.Publish 在整份 Usage 缺失时省略 token 键的问题：四个计数键均写 JSON null，明确零写 0，实际请求计数不丢失；继续保留可空 int64 和整数精度，不新增字符串用量或状态类型。
- [x] 日志查询和 `/usage` 对未知及受影响的分组／总计对应项统一保留 null，删除转为 unknown 的展示代码；保留已知数字千分位、已知耗时秒数、非法别名回退、未知传播及溢出检查，不把未知当零或排除。

#### 3.2 Hook 结果判定与日志统一整理

- [x] 确认 `internal/hook/logging.go` 的 `LogHook(ctx context.Context, record events.LogRecord)`；只解除取消并发布完整九字段，不接业务对象、不补身份、不分类错误。
- [x] 同步改造 Hook 准备／完成桥接的日志返回边界（含 PrepareToolCall、CompleteToolCall、PrepareAssistant 及其调用者）：由 Hook 来源明确 ResultStatus 和 Content，业务数据及原始 error 独立返回；纯取消与独立失败在来源保留，Runner／回复提交不再从 Hook error 推导日志结果。使用独立 LogResult，业务消费者显式丢弃；不重复转发 Hook 已发布记录。
- [x] Handler／HandlerFunc、Run 返回 Event、LogResult、error，Notify 返回 LogResult、error；Noop／未处理明确 skipped。来源在包装或合并业务错误前保留所属操作事实，不汇总独立工具或发送日志。
- [x] 全部 Hook 发布点改走 LogHook；来源继续组装完整记录，删除旧发布方法、重复转发及仅用于日志的错误分类。覆盖流水线、rules、配置／注册、进程／重载／主动日志／stderr、协议、桥接和自身媒体释放；保留业务通知与清理预算。
- [x] 独立发布 Audit JSON 的 hook_tool_call 与 hook_tool_log，同一实际 tool_call_id 关联；runtime 沿用 ID 生成方式，rules 使用 storage.NewID。已进入工具调用始终 INFO／succeeded，使用独立 logContent，立即发布；发送使用来源 LogResult，不补造未执行成功，failed 与 nil error 并存仍保留。
- [x] 发送接口返回 Receipt、LogResult、error，App 直接传递；Actor 固定于授权默认值注入前。补齐发送查询事实、事件补全与示例，定向验证调用顺序、唯一 ID、Actor、业务控制和资源释放。

- [x] runtime 工具请求参数解析失败、工具名为空仍发布普通 hook_tool_call，使用 INFO／rejected，正文说明未进入 Tool.Call；保留原业务错误，不补造未知工具名或调用 ID。
- [x] 日志 text 不在来源提前截断，统一交中心；JSON 仍由来源限长，修正工具参数、发送正文及平台诊断的 Unicode 截断与 … 标记，原业务数据及返回 LogResult 正文保留。编码后遵守 64 KiB 硬上限，超限明确报告设施失败。
- [x] exec 使用 cancelRequested 与平台内 execExitCanceled 保留宿主终止及真实退出事实；纯取消 canceled、超时 failed，独立信号／退出／IO／协议故障不被取消覆盖。Worker 在同一锁内先固定退出事实再关闭等待通道，请求与启动等待读取已知故障，保留原业务错误及清理／重启流程。

### 验证记录

3.2 已修改完成路径的定向验证：

- 本次收尾的 runtime／rules 定向测试通过，覆盖无效参数 INFO 记录、无补造 ID、Unicode 与 JSON 转义体积、完整 text 返回、真实退出事实先于等待唤醒、宿主取消及独立信号退出伴随取消；exec 握手、输出、超时、取消和子进程清理等相关既有测试通过。Worker 与 exec 终止边界的定向 race 测试通过。Windows 终止分支仅静态核对，当前环境未执行 Windows 测试；未运行全仓测试或构建。

- Hook、runtime、rules、control、builtin、Agent、Toolrun、App、command/builtin 共 9 个包的定向测试通过；一次性 exec 的握手、输出、取消与子进程清理另行通过定向测试，回环 socket 测试在沙箱外运行。未运行全仓测试或构建。
- 新增回归覆盖取消后发布与身份副本、来源结果独立于业务 error、同一点独立失败保留、调用先于发送、独立正文、实际 Actor、唯一调用 ID、无输出不发送、未进入调用、部分送达、failed 与 nil error 并存、协议取消伴随写入故障、重载结果与业务替换分离及媒体关闭预算。
- 沿来源、发布、发送及消费者复查：Hook 直接 EmitLog 只剩 logging.go；无通用 Merge、错误分类类型或 App 结果丢弃转发。送达事实显式使用 snake_case；来源 Unicode 限长，公共中心与身份复制契约保持。gofmt、git diff --check、链接／locator／禁改文件检查通过。

已完成改动的 3.1 定向验证：

- 3.1 收尾静态复审已完成非 Hook 范围：核对来源九字段与注释、snake_case、身份与错误所有权、分包和方法调用；沿调用／发送／回复／关联／Cron／Elnis 边界检查结果与业务分离，同时核对中心、查询、用量、预载、背压、关闭预算及平台消费者隔离。Hook 范围的复审与验证见上文 3.2。
- 本轮 newRecord、显式空 SessionID、接管后当次身份、Audit／Runtime 同一耗时与批次失败回归通过；媒体引用覆盖跳过、实际事务、缺失引用回滚、取消和幂等。公共契约只在 EmitLog 验证，中心精度、脱敏、限长、过滤、背压及关闭定向回归通过。
- Cron 触发、Elnis direct／报告投递／恢复新增回归通过：来源 failed 与业务 nil error 并存、包装取消、混合失败及发送／保存中的独立失败；原业务返回、事件状态、报告重试、部分关联和接管行为保留。定向测试仅覆盖本轮相关已修改完成路径的 9 个包，未运行全仓测试、构建或未完成的 Hook 测试。

- 后台公共发布管理口及工具统一入口定向验证通过：取消后保留来源记录、身份不从 context 补值、复用记录发布到不同类别互不覆盖。工具三种 run_source、来源未知不补造、真实前台接管前后及同批次新调用的来源固定验证通过。
- /log --run_source 的解析、缺值／非法值、帮助与补全、text／JSON 精确匹配、正文伪装标记、条件交集、数量上限及 raw 回归通过；Elnis 事件终态不按 dialogue 合并错误重新分类的回归通过。仅运行 background、toolrun、logging、command/builtin、agent、cron、elnis 已修改且完成路径的定向测试，未运行未完成部分或全仓测试、构建。

- 仅对本轮已修改且已完成的路径运行定向测试，未运行全仓测试或构建，也未运行未完成部分的回归。
- 工具只返回独立正文、Runner 统一调用状态与发布的定向回归通过，覆盖已进入调用后取消、根本未进入调用、业务错误与独立正文。请求／记录／transcript 来源接口、Chat／Responses 提交顺序及轮次上限回归通过；维护清理来源结果、媒体并行删除、取消、重试及取消伴随独立失败回归通过。
- 保存来源回归覆盖来源 failed 且业务 error=nil、包装取消及混合失败；Runner 保留来源 Content 与结果，不改写业务流程。仅业务消费者显式丢弃 LogResult，不再二次推导日志结果。
- 工具返回及调用日志、Cron 来源结果与最终状态保存、发送路由、通知接口、平台发送及部分回执的定向回归通过；相关 App 装配和工具调用补全接口回归通过。
- 新增回归先复现回复／流式提交的失败被后续取消覆盖、关联包装取消判定、Cron 忽略 failed 与 nil error 并存、发送管理器因日志结果吞业务错误，再验证修复。实际 CLI 多连接回归验证部分发送保持原业务成功，同时 LogResult 保留失败原因和送达数量。
- 回复发送／保存顺序、部分消息关联、流式回执合并、媒体缓存失败不重发、Elnis 报告关联和重试业务回归通过。已运行 gofmt 与 git diff --check；本节验证覆盖 3.1；3.2 的复审与验证结果见上文。

步骤 2 验证结果：

- `go build ./...`、`go test ./...` 通过；集成测试所需本机 HTTP／TCP 监听在沙箱外运行。
- `go test -race ./internal/agent/... ./internal/app ./internal/toolrun ./internal/elnis ./internal/platform/cli` 通过。最新回执合并、模型保存失败、取消计数与工具批次测试另行通过针对性 race 回归；命令身份变更通过 builtin race 回归。
- 工具真实入口覆盖成功、部分失败、取消、拒绝、确认超时、取消／拒绝伴随独立保存失败；批次失败不伪造逐工具完成。
- Chat 覆盖流关闭但未发送错误块的取消，保留部分输出及空错误返回，实际请求仍计入 llm_usage；Responses 覆盖上游成功但终态保存失败，保留输出且不标为 ProviderError。回复覆盖取消发送伴随保存失败，以及 Replace／Finish 部分回执去重、输出索引合并和切片副本。
- App 状态测试先暴露可空用量浅拷贝问题，再验证深复制修复；Agent 身份、命名终态、日志与通知独立、依赖边界和真实 JSONL 查询回归通过。
- logging、builtin、hook／runtime、cron、Telegram、dispatch、SQLite、通知、维护、QQ 官方、OneBot、Hook 规则／内置模块、session、contextmgr、modelmgr、LLM 协议及 runtime 的相关 race 回归通过；覆盖损坏行、查询前过滤、raw、未知用量、大整数、别名回退、保存组合失败和身份。
- 已运行 gofmt 与 `git diff --check`；测试结果不能替代文档与实现的一致性复审，未自动提交。

步骤 1 验收结果：

- `go test ./internal/events ./internal/logging ./internal/signal` 通过。
- `go test -race ./internal/events ./internal/logging ./internal/signal` 通过。
- 非法类别诊断回归已先复现空字符串问题，再验证整数值输出；部分身份缺失、短写后继续处理且不递归发布、满队列等待前记时的测试通过。满队列测试使用标准库 `testing/synctest` 控制调度和时间。
- 已运行 gofmt；文档链接、章节引用、locator、禁改说明及 `git diff --check` 检查通过。
- 步骤 1 契约覆盖单一整体 ResultStatus、独立 Level 过滤及 text／JSON 正文约定。

## 其他待办

- [ ] CLI TUI 支持 Markdown 渲染。
- [ ] CLI 用户名和助手显示名可配置。
- [ ] CLI 支持本地图片输入。
- [ ] 周期 Cron 支持过期时间。
- [ ] 从公共config包获取命令前缀，所有提示消息自动组装

## 待确认与暂缓

- [ ] 临时hook或者一次性hook
- MCP 接入：暂不考虑，建议使用SKill
- 子 Agent：暂不考虑，信息交流必然伴随噪声或者损失
