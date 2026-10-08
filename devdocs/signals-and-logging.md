# 信号与日志系统设计

本文定义信号与日志系统的职责、契约和处理规则。实施进度、待办及验证结果统一见 [任务清单](tasks.md#信号与日志系统整改)，实际代码职责见 [架构说明](architecture.md#信号与订阅) 和 [代码地图](code-map.md#信号与订阅)。

## 已确认的要求

- 信号机制参考 Godot 的声明、发射和连接方式，是通用能力，未来可以服务日志以外的系统。
- 全局契约集中定义，各业务和中心自行连接。业务日志通过全局日志信号交给日志中心，不经 App 转发，也不逐层注入 Logger、日志发布器或同一全局信号。
- 业务决定日志等级和操作结果，原始错误保留在业务返回值和业务实例事件中。日志的 `Level`、`ResultStatus`、`Content` 各有职责，不能相互机械推导。
- 操作结果由掌握上下文的业务来源确定，来源事件完整传递事实。日志回调、信号和日志中心不通过错误树、错误文案或事件名反推取消、拒绝和失败。
- 日志来源组织正文，将错误原因及必要诊断写入 `Content`；普通日志优先 text，需要业务字段筛选、统计或解析的记录使用 JSON。JSON 由来源构造并先控制体积，公共日志不再传递错误对象或任意 Go 对象。
- 不新增 Go 包，不建设共享错误分类器、取消判断器或错误树检查设施；不为未知需求预先增加抽象。
- 所有包、文件位置、包名、文件名、函数名和变量名必须与用户讨论确认，测试代码除外。已有名称、已确认的新名称和待讨论项必须区分；测试名称和实现可自行决定。
- 本轮只处理已明确的日志和信号整改，不改变重试策略、权限规则、同步业务持久化、队列策略或共同的 30 秒退出预算，不自动提交。
- 所有日志使用统一的九字段封闭契约、同一个发布入口和处理规则；Content 是唯一正文，Identity 由业务显式填写。
- 来源迁移以本文已确认的新设计为准。旧代码用于定位业务分支和迁移点，不能以旧字段、旧日志结构或现有回调的行为反推目标契约；发现不符时整改实现。
- 日志和 notify 各自负责自己的输出。日志中心不决定是否向平台通知，也不生成通知摘要；来源独立组织面向用户的通知。
- 底层定义型字段必须在 Go 注释中说明用途、填写方、零值和约束。
- 本次改动不用再写changelog.me

## 处理链路

```mermaid
flowchart TD
    B["业务操作<br/>在实际分支明确操作结果<br/>保留原始 error<br/>不同操作分别保留事实"]
    B --> E["已有业务实例事件<br/>明确结果与原始错误<br/>保留发生时间和关联身份"]
    E --> P["本模块日志回调<br/>依据明确业务事实选择等级<br/>构造日志记录<br/>不从错误链反推结果"]
    B --> D["业务直接记录<br/>明确提供结果、等级、正文和身份<br/>无需新增实例事件"]
    P --> R
    D --> R
    R["统一 LogRecord：九字段<br/>Category：日志类别枚举<br/>Level：业务指定等级<br/>EventName：稳定事件名<br/>Module：来源模块<br/>Type：text 或 json<br/>Content：来源组织的正文<br/>ContentLimit：text 字符上限<br/>Identity：固定关联身份<br/>ResultStatus：操作结果枚举"]
    R --> EM["events.EmitLog<br/>验证枚举、长度和 JSON 语法<br/>仅复制业务填写的关联身份<br/>不从 context 补值"]
    EM --> S["全局 LogSubmitted 信号<br/>只负责派发"]
    S --> M["日志中心订阅<br/>接收时、入队前自动记录时间<br/>按类别接收"]
    M --> Q["异步队列<br/>保留统一记录及中心接收时间"]
    Q --> FILTER["按业务指定 Level 过滤<br/>不根据结果或正文重新判级"]
    FILTER --> FORMAT["最终文件格式化<br/>按 Type 统一脱敏<br/>text 按字符截断，默认 256<br/>JSON 保持有效，不按 ContentLimit 截断<br/>JSONL 编码及 64 KiB 硬上限检查"]
    FORMAT --> FILE["JSONL 日志文件<br/>JSON Content 直接写为原生值"]
    OWN["业务实例事件中的错误对象发布后只读<br/>可变错误由来源复制具体结构<br/>不靠转字符串制造业务错误快照"]
    OWN -.-> E
    ID["Identity：业务显式填写的 LogIdentity<br/>SessionID / RunID / Attempt<br/>RequestID / RootRequestID<br/>nil 省略，显式空值保留"]
    ID -.-> R
    classDef boundary fill:#e8f5e9,stroke:#388e3c,color:#222
    class B,P,D,EM,FORMAT,OWN boundary
```

“日志回调”是业务事件到 `LogRecord` 的转换代码。它归来源模块、同步执行，不另建异步中转队列。没有业务事件的直接记录点不必为日志新增事件。通知、状态等消费者可独立订阅业务事件。

业务实例事件保留原始错误和事实发生时间。来源在构造日志时将错误原因和必要诊断写入 `Content`，显式填写关联身份；公共日志的时间由中心接收时记录。日志与 notify 各自负责内容组织和输出。

## 信号所有权与依赖边界

| 所有者 | 职责 |
| --- | --- |
| `internal/signal` | 类型化信号、连接、执行器、队列、背压、断开和排空，不认识业务或日志 |
| `internal/events` | 全局信号及数据契约，不持有业务管理器、文件、配置或数据库，不反向依赖业务和日志中心 |
| 来源模块 | 确定业务事实和结果，发布事件，维护自身实例事件及日志回调 |
| 订阅模块／中心 | 自行连接、处理事件，持有并清理自己的连接与队列 |
| App | 服务装配、启动和关闭协调，以及自身通知和状态订阅，不转发全局日志或平台连接事实 |

业务和中心共同依赖显式的全局契约，不建立“字符串事件名 + 任意载荷”的通用事件注册表。保留已有 `Signal[T]`、`Emit`、`Connect`、`Disconnect` 等机制名称和显式生命周期。

全局信号适合多个独立系统共同观察的进程级事实；实例内部状态仍归实例。全局信号不缓存、不重放，不承担持久消息总线职责。定义和发布属于掌握事实的模块或公共契约，消费属于需要该事实的中心，不能为了日志倒置依赖。

| 场景 | 已确认边界 |
| --- | --- |
| 日志提交 | 全局日志信号，日志中心自行订阅 |
| 已有平台连接成功事实 | 全局信号，各平台直接发布，Hook 和 Cron 自行订阅 |
| 系统告警、配置应用完成、后台任务状态 | 适合全局观察的候选，不是本轮新增任务；应用、调度和错误返回仍由业务执行 |
| 模型重试 | 客户端自己执行；保留模型实例信号、来源日志和独立通知，不新增全局重试信号 |
| 会话／请求状态 | 保留实例边界，活跃 `Binding` 等执行对象不能直接搬进全局载荷 |
| 权限、可改写 Hook、Usage 和关键持久化 | 同步完成，不依靠广播保证业务结果 |
| 查询 Reader、维护清理、Elvena 请求派发 | 需要返回结果的调用保持同步调用语义，不改成广播 |

## 公共日志字段与命名

以下是已经确认的目标 `LogRecord` 全部九个字段，定义位于现有 `internal/events/logging.go`。每个字段的 Go 注释必须写明用途、填写方、零值含义和约束，不再增加自由属性或其他扩展入口。

| 字段 | 类型或约束 | 含义与输出 |
| --- | --- | --- |
| `Category` | `LogCategory`，受验证的枚举；来源必填，零值非法 | runtime／audit／elnis，决定日志类别与文件，落为 `category`，不判断业务结果 |
| `Level` | `slog.Level`；来源指定，Go 零值为 INFO | 业务指定的关注等级，落为 `level` |
| `EventName` | `string` | 稳定事件标识，落为 `event` |
| `Module` | `string` | 来源模块，落为 `module` |
| `Type` | `LogContentType`，受验证的枚举；零值为 text | 正文格式，落为 `type`，不猜测正文是否像 JSON |
| `Content` | `string` | 唯一正文；自然文本或来源编码的有效 JSON，落为 `content`；JSON 直接写为原生 JSON 值 |
| `ContentLimit` | `int`；负数非法；零值默认 256 个 Unicode 字符 | 来源控制 text 保留长度；截断标记计入上限，不落盘；JSON 不使用此限制，来源先控制体积 |
| `Identity` | `LogIdentity`；五个 `*string` | 固定关联身份，区分未提供与显式空值，具体见下表 |
| `ResultStatus` | `ResultStatus`，受验证的枚举 | 业务明确的操作结果，非零落为 `result_status`；零值表示未声明，不默认成功 |

公共记录仅含上述九字段。时间由日志中心在接收记录、进入类别队列前自动记录，以 RFC 3339 时间写为 `time`，业务不填写。

### 枚举与稳定标识

`ResultStatus` 采用已讨论的 `uint8` 和 `iota` 枚举形式：

```go
type ResultStatus uint8

const (
    ResultUnspecified ResultStatus = iota
    ResultSucceeded
    ResultFailed
    ResultCanceled
    ResultRejected
    ResultSkipped
)
```

写入时分别映射为不输出结果字段、`succeeded`、`failed`、`canceled`、`rejected`、`skipped`。零值明确为未声明，不默认成功。操作结果日志必须由来源填写；普通提示、启动过程和进度可以不填。失败结果不强制伴随 Go `error`。

`LogCategory` 同样使用 `uint8` 和 `iota`：`LogRuntime=1`、`LogAudit=2`、`LogElnis=3`，分别输出 `runtime`、`audit`、`elnis`。保留已有常量名称及文件类别，零值非法，不能默认为 runtime。

`LogContentType` 使用 `uint8` 和 `iota`：`ContentText=0`、`ContentJSON=1`，分别输出 `text`、`json`，普通文本为默认值，不增加 plain 别名。

三个枚举均提供 `Valid() bool` 和 `String() string`。未声明结果及非法值的 `String()` 返回空字符串；非法值必须由验证拦截，不能静默写入或纠正。

Go 的整数枚举不能阻止强制转换非法数值，因此发布入口仍要验证。非法结果、类别或正文类型，以及负数长度、无效 JSON，均属于契约错误，走设施诊断和提交错误返回，不默默纠正、不推断业务结果、不递归发送日志。

事件名由来源代码显式定义，不从正文替换空格生成，不随展示文案改变。`Module` 与事件名描述来源和事实，不用来猜等级。新增命名仍须按用户要求讨论，不建立全局字符串分类表。

### 身份与正文

- `Identity` 包含 `SessionID`、`RunID`、`Attempt`、`RequestID`、`RootRequestID`，由业务在构造日志时显式填写，写入时展开为下划线字段名。与日志有关的身份由来源负责提供；没有对应身份的日志可留 nil。

| `LogIdentity` 字段 | 类型 | 落盘字段 |
| --- | --- | --- |
| `SessionID` | `*string` | `session_id` |
| `RunID` | `*string` | `run_id` |
| `Attempt` | `*string` | `attempt` |
| `RequestID` | `*string` | `request_id` |
| `RootRequestID` | `*string` | `root_request_id` |

`nil` 表示未提供，落盘时省略；非 nil 且指向空字符串表示明确为空，落盘时保留空字符串。`EmitLog` 仅复制业务填写的指针值，不从 context 补齐身份。来源后续修改自身变量不能改变已发布身份；所有订阅者只读，异步消费不查询当前会话来补身份。

- `Content` 是唯一正文。普通日志尽量使用 text；需要业务字段筛选、统计或解析时，由来源构造 JSON 并明确填写 `Type=json`。不按具体业务扩充公共字段，也不统一把所有日志改成 JSON。
- `Type=text` 按自然文本脱敏，不因外观像 JSON 而解析或结构化改写；`Type=json` 才按 JSON 处理。JSON 递归脱敏保留非敏感数据。正文内的业务键不提升为公共字段，即使包含 `level`、`event` 或 `result_status`，也不能覆盖外层正式字段。
- 原始错误留在业务返回值及实例事件中；日志来源将错误原因与必要诊断组织为正文，不将错误对象放入公共记录。组合操作的单条日志由 ResultStatus 表达来源确定的整体结果，Content 写清各操作事实、具体原因和错误。普通说明优先 text，需要业务字段查询或统计时使用 JSON。
- 不向公共日志提交任意 Go 对象、map、切片、延迟求值器或 `slog.Value`，不建设反射转换器。正文已经是不可变字符串，身份指针由发布入口复制。

### 错误诊断与通知边界

来源在构造 `Content` 时调用错误的文本接口或编码必要 JSON，将错误原因和必要诊断组织为正文。日志中心只处理固定公共字段和 text／JSON 正文，不认识具体协议错误。

协议来源负责选择与错误有关的载荷，不把完整请求、响应正文或媒体数据塞入诊断。错误原因及必要诊断进入业务组织的正文，不参与日志中心的业务结果分类。

日志和 notify 是独立链路。给平台返回的内容或简短说明由通知来源组织，发送决策由业务作出。日志查询需要的展示由查询命令处理。

## 业务结果与错误所有权

### 来源确定结果

业务在实际成功、取消、拒绝、跳过或失败的分支确定结果。来源收到的底层错误由该业务结合上下文解释，不能把缺失的信息留给日志回调补猜。事件类型本身已明确表达的结果可以直接使用，不给所有实例事件统一添加 `ResultStatus` 或 `slog.Level`。

来源日志回调根据完整事实决定业务等级并构造记录。既有业务约定为重试 WARN、最终失败 ERROR、预期取消 INFO、正常策略拒绝 WARN；持续重连、发送降级和待重投递等可恢复情况保持来源的 WARN 语义。导致操作最终失败的超时为失败，特殊场景由所属业务明确判断。日志中心不强制 failed 对应 ERROR，也不根据 canceled 自动降级。

| 场景 | 必须保留的事实 |
| --- | --- |
| 调用成功、保存成功 | 两项均为 succeeded，整体成功 |
| 调用成功、保存失败 | 调用为 succeeded，保存为 failed；整体失败，不能归为上游调用失败 |
| 调用取消、保存成功 | 调用为 canceled，保存为 succeeded；保留调用中断及部分内容已保存的事实 |
| 调用取消、保存失败 | 调用为 canceled，保存为 failed，各自保留错误；整体失败，取消不能覆盖保存故障 |
| 工具策略拒绝 | 解析阶段保留拒绝事实和类型化错误，复用已有 `PolicyDeniedError`；不能降为原因字符串再重建普通错误 |
| 工具拒绝、记录保存成功 | 调用为 rejected，保存为 succeeded；记录已保存不代表工具执行成功 |
| 工具拒绝、记录保存失败 | 调用为 rejected，保存为 failed；整体失败，拒绝不能覆盖保存故障 |
| 工具执行失败、记录保存失败 | 两项均为 failed，两项原始错误和日志中的错误原因均保留 |
| 工具不存在、配置缺失或执行故障 | 与正常策略拒绝区分，不能仅凭 `Success=false` 判为同一种情况 |
| 命名失败、兜底保存成功 | 以获得可用标题为整体目标，ResultStatus 为 succeeded；正文保留生成失败原因、兜底已生效及所用标题 |
| 命名失败、兜底保存失败 | 两项均为 failed，原始命名失败不被兜底失败覆盖 |
| 回复发送成功、持久化失败 | 发送为 succeeded，保存为 failed；整体失败，保留用户已收到消息的事实 |
| 回复持久化成功、随后发送失败 | 保存为 succeeded，发送为 failed；整体失败，保留消息已经保存的事实 |
| 回复发送和持久化成功、消息关联失败 | 发送和保存为 succeeded，关联为 failed；保留各项结果、已发送回执和具体关联错误 |
| 前序失败、后续操作未执行 | 省略未执行操作的结果，不补造成功或 skipped；需要说明流程中断时在正文写明原因 |
| 单项操作部分完成 | 例如多条消息中部分发送失败，保留失败结果及已发送回执，不能抹去已完成部分 |
| Chat 为保留部分输出返回空错误 | 仍显式传递取消或失败事实，不能由 nil 错误推导成功 |
| 重试或视觉回退 | 保留本次尝试失败、次数或回退事实，不把 retrying 增加为结果种类 |

`CallError`、`PersistenceError`、`FallbackError`、`SendError` 是讨论过的业务操作命名示例，不是公共错误类型枚举，也不是必须加到每个结构中的固定字段。各事件最终字段必须与对应结果一起列全、讨论确认后实施。不使用含糊的 `outcome` 或 `err` 作为新公共契约名，不批量重命名无关局部 `err`。

### 整体结果与正文

公共日志仅用 ResultStatus 表达整体结果，保持九字段契约，不增加次结果、原因码或统一子结果结构。Level 表达关注等级，Content 说明具体原因、各操作事实、错误及部分完成情况。

业务事件仍完整传递各操作事实和原始 error，来源在日志输出边界组织正文。正文不要求逐字段复刻业务事件的名称和结构，也不强制为组合操作嵌套各项结果。普通说明优先 text；需要业务字段查询或统计的正文使用来源与消费者约定的 JSON。日志回调和中心不从错误链或正文反推业务结果。

各来源的完整事件字段、类型、存在性和整体结果含义仍需确认，不能沿用旧代码填补契约空白。未执行的操作省略结果；部分完成的操作保留已完成事实，不能由错误为空推导全部成功。

组合记录的整体结果由掌握整个流程的业务来源确定。调用取消或工具拒绝伴随独立保存失败时，ResultStatus 为 failed，正文同时说明两项事实和对应错误。命名生成失败但兜底标题保存成功时，ResultStatus 为 succeeded，正文说明生成失败与兜底事实；Level 可用 WARN 表达降级，不能由结果机械推导等级。

### 业务错误与日志正文

业务返回值和业务实例事件保留原始错误对象、各操作事实和必要的发生时间。来源在日志构造边界把错误原因及必要诊断写入 `Content`；全局日志信号和队列只传统一记录，不再传错误对象，也不把日志文字重新包装成业务错误。

业务实例事件中的错误发布后只读，所有订阅者也只读。来源若持有可变错误结构，必须复制自己掌握的具体结构及其可变成员再发布，不能发布后继续修改。Go 的 `error` 没有通用克隆能力，本设计不承诺自动深复制任意错误类型，也不通过反射拆解或字符串化来伪装成保留原始错误。

业务返回值可以继续携带必要的合并错误，但来源事件不能只剩一个合并错误而丢失各操作事实。日志回调不遍历它来重新分类。记录失败不能替代或覆盖原业务结果。

业务实例事件的错误所有权与日志正文转换分开：原始错误供业务处理，日志、数据库持久化及用户回复分别在各自输出边界生成内容；不能把这些输出文字回读为业务处理所需的原始错误。

## 发布、消费和生命周期

- `LogSubmitted` 在进程生命周期中保持同一实例；生产日志通过 `EmitLog` 发布，不能绕过公共入口直接发射全局日志信号。
- `EmitLog` 验证枚举、非负长度和 JSON 语法，仅复制业务填写的关联身份，不从 context 补值，不写文件、不读取当前日志级别、不创建队列、不接收业务错误对象。
- 公共日志正文是不可变字符串，身份在发布前复制。业务实例事件中的错误遵循上一节的只读所有权约定。
- 不向异步消费者传递业务管理器或活跃执行对象。身份在发布时固定；中心在接收记录、入队前记录时间。队列消费时不重新取当前时间或当前会话覆盖已固定值。
- 日志中心自行订阅，拥有 runtime、audit、elnis 三个串行队列，每个容量 256，满时等待容量，关闭采用 Drain。按成功入队顺序处理，同类别之外不保证总顺序。
- 单次请求取消不撤销已发布事实，也不解除日志背压；中心停止准入唤醒等待者。过滤、正文脱敏、text 限长和文件格式化在中心消费者中执行。
- 进程内只允许一个活动日志中心。初始化失败清理已取得的连接、队列和文件；重复构造不能形成双写；关闭不替换全局信号实例。
- App 先建立日志中心和消费订阅，再启动会发日志的业务。中心建立前的初始化失败直接返回主程序报告。
- 所有退出共享既有的 30 秒预算：停止准入、取消运行并唤醒生产者，等待生产者和消费者，最后关闭已不被使用的资源。超时不能声称排空成功，也不能强行关闭仍被回调使用的文件或依赖；剩余工作交给进程退出处理。
- 全局订阅测试不得并行争用同一入口；多个完整应用实例需要进程隔离。纯格式化、查询及无全局状态的测试可以并行。

各阶段的具体职责如下；图中业务事件保留错误和发生时间，与公共日志接收已组织正文、由中心记时的边界一致。

| 阶段 | 职责 |
| --- | --- |
| 业务来源 | 确定事实、结果、等级、正文类型及内容，显式填写关联身份；text 指定所需长度，JSON 自行控制体积；独立组织 notify |
| `EmitLog` | 验证公共契约，仅复制来源填写的身份，发射统一记录；不从 context 补值 |
| `LogSubmitted` | 只负责派发，不分类、不持久化、不重放 |
| `handleRecord` | 接收时记录时间，按类别入队；消费者按来源等级过滤并调用写入 |
| `formatRecord` | 按 Type 脱敏，text 按字符截断，JSONL 编码和记录硬上限检查 |
| 查询命令 | 按公共字段及已知 JSON 业务契约筛选、统计或展示；所有条件匹配后计入数量上限 |

### 设施故障

信号派发、回调、文件写入、非法公共契约及未完成排空等设施问题直接走独立 stderr，由终端或服务管理器收集，不受运行日志级别过滤，不递归发送日志信号，不依赖业务文件 Logger。

文件写入和短写错误必须被检查并返回；初始化和关闭错误返回生命周期调用方。运行期故障沿既有策略报告，不新增自动重启或一律崩溃的策略；初始化失败返回主程序退出。提交成功不等于持久化成功，正常取消与真实设施失败不能相互覆盖。

## 记录内容与查询

| 项目 | 约定 |
| --- | --- |
| 运行级别 | `runtime.log_level` 只过滤运行日志，中心使用业务提供的等级 |
| 审计与 Elnis | 独立保留事实；`llm_usage` 和 `/usage` 数据不因运行级别升高而丢失 |
| 正文 | 通过等级过滤后统一按 Type 和 ContentLimit 处理，不由中心在非 DEBUG 时额外隐藏或缩短；调试内容由来源明确使用 DEBUG |
| 最终上游失败 | 通过审计保留诊断，不受运行级别过滤；不改变给用户的简短错误提示，不修改上游重试策略 |
| text 长度 | 按 Unicode 字符截断，ContentLimit 为零时默认 256，正数由业务指定；超限保留前 limit−1 个字符并追加单字符 `…`，标记计入上限 |
| JSON 长度 | 业务先控制体积，不按 ContentLimit 截断，保持完整有效 |
| 单条记录 | JSONL 编码后连同行尾最多 64 KiB；超限明确报告设施失败，不写半条记录，不截坏 JSON |
| 脱敏 | 先脱敏再截断，覆盖嵌套结构、凭据、认证头、cookie 和媒体内嵌载荷；保留 token 用量等业务元数据 |
| 文件 | 新记录统一写 JSONL；保留现有文件前缀、按日轮转、保留天数和配置，不改写历史记录 |

来源组织正文；最终写入阶段统一脱敏、text 限长、JSONL 编码和记录硬上限检查。普通正文直接使用 text，需要 JSON 的来源自行编码。`Type=json` 的正文写入原生 JSON 值，数值处理必须保持精度。所有来源使用统一公共契约与发布入口。提交成功只表示准入，异步格式化或写盘失败由设施诊断报告。

runtime、audit、elnis 分别使用 `elbot-日期.log`、`audit-日期.log`、`elnis-日期.log`。Reader 的读取契约包含历史键值文本和 JSONL，包括同一文件内混合的两种格式。正文内的键不提升为公共字段，不能覆盖外层事件、等级、身份或结果。

Reader 保留从文件末尾按 64 KiB 分块读取、日期从近到远和文件内写入位置倒序筛选的行为。达到数量立即停止；读取和处理行时检查取消；打开文件后固定范围，不追赶追加数据，不按事件时间重新排序。支持跨块中文、长行、LF／CRLF、无末尾换行和跨日筛选，旧记录单行读取上限保持 16 MiB，超限报错。

Reader 遇到无法解析的 JSONL 行时继续查询。正常展示跳过损坏行，提示本次实际扫描发现的损坏行数量，以及查询结果或用量统计可能不完整；该数量不代表整个文件或日期范围的损坏总数。原始模式可查看损坏行原文并标注损坏，不能将其作为有效业务记录参与统计。正常数量上限只计入条件匹配的有效记录，损坏行不会提前占满有效记录数量；文件读取失败及超过单行读取上限仍返回错误。

- `/log`、`/audit`、`/elwisp` 使用 `--raw` 开启原始记录展示，只改变展示方式，不改变等级筛选或实际日志记录等级。
- `--level` 按 Level（DEBUG／INFO／WARN／ERROR）设置最低查询等级，不按 ResultStatus 筛选；`-d/-i/-w/-e` 分别是最低等级的快捷方式，均不自动开启原始展示。选项顺序不改变等级与展示方式相互独立的含义。
- 普通展示统一包含时间、等级、事件、内容和结果，不显示 Type。时间、等级、事件和结果集中展示，正文独立排版；text 展示解码后的文本，JSON 正文格式化展示。结果采用便于阅读的说明，零值或历史记录缺少结果时显示“未声明”，不默认成功。Type 仍用于内部解析，原始模式保留文件里的完整物理记录。
- `/log` 的 `-u`、`-a`、`--system` 行为保持。
- `/audit` 不提供 `-u`、`-a`；`--event` 按真实事件名筛选。
- `/log --hook`、`/audit --hook` 按 `module=hook` 与其他条件取交集；帮助与补全使用实际事件，不虚构 `hook` 事件。
- Hook 工具调用使用 `hook_tool_call` 和 `source=rules|plugin` 来源事实；操作结果由来源填写 `ResultStatus`。正常业务状态属性与通用操作结果各自表达对应事实。
- 普通与后台预载跳过分别使用 `skill_wrapper_preload_skipped`、`background_preload_skipped`，结果为 `ResultSkipped`。
- 整体结果从公共字段读取，组合操作的各项结果和错误原因从正文读取，不为历史日志补造字段。命令只按 Type 解码正文；本轮新增展示选项为 `--raw`，帮助、补全和用户命令文档随实现同步更新。
- `/usage` 遇到非法数字、溢出等无法统计的用量值时显示 `unknown`，不按零处理，也不静默排除异常记录。受该值影响的分组和总计对应项显示 `unknown`，其他可确定的统计项正常展示；全部异常时不能显示零用量结论。

### 哪些来源使用 JSON

| 命令 | 需要 JSON 的来源及业务字段 | 可优先使用 text 的记录 |
| --- | --- | --- |
| `/usage` | `llm_usage`：provider、model、prompt_tokens、completion_tokens、total_tokens、cache_hit_tokens、elapsed_ms；按模型、日期、会话筛选和统计 | 用量正文必须可解析，不另建统计存储 |
| `/audit` | 需要按 tool、actor_id、risk 等业务字段筛选或结构化展示的审计记录 | 只需公共事件、模块、身份、结果或全文检索的记录 |
| `/elwisp` | Elnis 业务事件：elwisp_name、source、source_id、mode、event_key、event_id、token_name、tags 等筛选字段 | 不携带上述业务事实的运行提示 |
| `/log` | 只有确实需要结构化读取的正文才填写 JSON | 用户消息、模型回复、系统提示、通知、启动、重连、清理等普通运行日志 |

组合操作按[整体结果与正文](#整体结果与正文)表达，正文类型由查询和统计需求决定，不因包含多个操作就强制使用 JSON。不需要结构化读取的内容尽量用 text。JSON 业务条件必须在计入查询条数之前匹配，不先取满条数再过滤，也不将业务 JSON 字段并入外层公共字段。

### 落盘模拟

以下每行是一条 JSONL 记录。`ContentLimit` 不落盘；没有提供的身份省略，显式空身份输出空字符串，未声明结果不输出。

```jsonl
{"time":"2026-10-07T15:00:00+08:00","category":"runtime","level":"INFO","module":"app","event":"startup","type":"text","content":"ElBot 启动完成。"}
{"time":"2026-10-07T15:00:02+08:00","category":"runtime","level":"ERROR","module":"delivery","event":"notice_output_failed","type":"text","content":"Telegram 通知发送失败：连接超时。","session_id":"sess-demo","request_id":"req-demo","result_status":"failed"}
{"time":"2026-10-07T15:00:03+08:00","category":"audit","level":"INFO","module":"agent","event":"llm_usage","type":"json","content":{"provider":"demo","model":"demo-model","prompt_tokens":1200,"completion_tokens":300,"total_tokens":1500,"cache_hit_tokens":800,"elapsed_ms":2100},"session_id":"sess-demo","request_id":"req-demo","result_status":"succeeded"}
```

JSON Content 直接作为原生 JSON 值写入，避免整段重复转义；text 中的引号和换行仍按 JSON 标准转义。命令展示解码后的内容，原始模式保留物理记录。

#### 组合记录示意

以下示例使用单一整体结果和 text 正文，事件名仅用于示意，不作为新增事件名的确认。公共代码字段 ResultStatus 对应 `result_status`，具体原因和各操作事实由 Content 表达：

```jsonl
{"time":"2026-10-08T15:00:00+08:00","category":"audit","level":"ERROR","module":"agent","event":"example_combined_operation","type":"text","content":"模型调用已取消：context canceled；保存已有输出失败：database is locked。","session_id":"sess-demo","request_id":"req-demo","result_status":"failed"}
{"time":"2026-10-08T15:00:01+08:00","category":"runtime","level":"WARN","module":"session","event":"example_combined_operation","type":"text","content":"自动生成标题失败：上游请求超时；已成功保存兜底标题“新会话”。","session_id":"sess-demo","result_status":"succeeded"}
```

第一条整体失败，正文保留调用取消和保存失败的原因；第二条以获得可用标题为整体成功，正文保留生成失败与兜底事实。text 遵守默认 256 字符和 ContentLimit 约定，来源按所需说明长度设置上限；需要结构化查询的来源可使用 JSON。所有记录仍遵守脱敏及整条 64 KiB 硬上限。

### 查询展示示意

以运行日志中的 `notice_output_failed` 记录为例，普通展示如下。使用 `-d`、`-e` 或 `--level` 只调整最低等级，保持这种展示方式：

```text
2026-10-07 15:00:02  ERROR  notice_output_failed  失败
Telegram 通知发送失败：连接超时。
```

增加 `--raw` 后展示文件原文，Type 等已落盘字段保留在原始记录中：

```jsonl
{"time":"2026-10-07T15:00:02+08:00","category":"runtime","level":"ERROR","module":"delivery","event":"notice_output_failed","type":"text","content":"Telegram 通知发送失败：连接超时。","session_id":"sess-demo","request_id":"req-demo","result_status":"failed"}
```

`/audit` 和 `/elwisp` 使用相同的展示规则。原始展示不会恢复落盘前已经截断或脱敏的内容。

## 来源职责与平台连接

Agent、工具、Session、模型客户端与 Model Manager、Hook、平台、发送、Cron、Elnis、媒体、命令、通知、维护和 App 的直接发布点及实例事件日志回调均遵循统一契约。

Agent、Session、Model Manager 自行维护实例事件的同步日志回调和连接释放。模型重试日志独立于 App 通知订阅；没有通知订阅者也不能漏日志，调用结束后不显示过期重试通知。Hook Manager 记录原始执行失败，Agent 保留通知与自身桥接故障，同一错误不因迁移重复记录。终端交互是用户界面，不强行改成文件日志。

Session 命名保留 Scheduled 信号，以 `NamingScheduledEvent` 表达调度事实；流程结束统一通过 Completed 信号发布 `NamingCompletedEvent`。Completed 表示流程已经结束，不隐含成功，整体 ResultStatus 由来源明确填写，并保留生成、保存、兜底的实际事实和原始错误。命名信号契约只有 Scheduled 和 Completed，不设置独立 Failed 信号或 `NamingFailedEvent`。生成失败但兜底标题保存成功时整体为 succeeded，日志正文仍说明生成失败与兜底事实。

平台连接全局信号属于保留的现有能力：

- `PlatformConnectedEvent{Platform string}`、`PlatformConnected` 位于现有全局契约，携带平台名称，不持有适配器或业务对象，不缓存重放。
- CLI、Telegram、QQ OneBot、QQ Official 在既有连接成功边界直接发布；QQ Official 的 READY 发布，RESUMED 不发布；不扩展 headless、远程 CLI 或断开／恢复等新事件。
- Hook 协调模块和 Cron 自行订阅、管理按平台惰性创建的队列。既有 `StartPlatformEvents` 在装配完成后、平台启动前调用，重复启动不重复连接，关闭后不重启。
- 每个平台队列容量 256、满时拒绝，同平台串行，跨平台和跨消费者隔离。保留发射 context 的值，解除单次调用取消；关闭采用 CancelPending，App 取消时停止消费。
- `BeginClose` 停止准入、断开连接并取消队列；关闭前已取得的发射快照不能在关闭后接纳任务。`Close` 使用共同预算等待，`Done` 表示实际退出。
- Hook 执行及输出、Cron 状态与过期一次性任务补执行保持原业务规则，不新增去重、补跑或调度策略。未退出任务仍使用的依赖保留到进程退出。

## 已有代码位置与实施前待确认项

下面仅列已有位置和职责，作为定位依据，不授权新建、迁移或自行命名文件。当前代码地图继续描述实际实现，不提前写成目标结构。

| 已有位置 | 职责边界 |
| --- | --- |
| `internal/events/logging.go` | 九字段公共日志契约、身份、枚举、全局信号和发布入口 |
| `internal/events/platform.go` | 已有平台连接全局契约 |
| `internal/signal/` | 通用信号、队列及独立设施诊断 |
| `internal/logging/logging.go` | 中心、文件资源、轮转和清理 |
| `internal/logging/signals.go` | 中心订阅、接收时间、队列准入、消费过滤及写入调用 |
| `internal/logging/record.go` | text／JSON 脱敏、字符限长、JSONL 编码与硬上限检查 |
| `internal/logging/reader.go` | 反向分块查询，读取契约包含历史格式和 JSONL |
| `internal/agent/logging.go`、`internal/session/naming_logging.go`、`internal/modelmgr/logging.go` | 来源模块的日志回调，不依赖具体协议包来解释错误 |
| `internal/app/signals.go`、`internal/app/model_signals.go` | App 自身连接管理与模型通知接线，不转发日志 |
| `internal/session/naming_signals.go`、`internal/modelmgr/signals.go` | 来源实例事件契约，连接生命周期由所属服务负责 |
| `internal/agent/platform_signals.go`、`internal/cron/platform_signals.go` | 平台连接消费者的订阅、按平台队列和关闭 |

### 已确认的位置与实施接口

| 名称／签名 | 位置及约定 |
| --- | --- |
| `LogIdentity` | `internal/events/logging.go`，五个已确认的 `*string` 身份字段 |
| `LogCategory`、`LogContentType`、`ResultStatus` | 同一文件中的 `uint8`／`iota` 枚举，分别提供 `Valid() bool` 和 `String() string` |
| `EmitLog(ctx context.Context, record LogRecord) error` | 沿用公共发布入口，返回日志提交错误，不承载业务结果 |
| `handleRecord` | 沿用中心接收方法，在入队前自动记时 |
| `formatRecord(ctx context.Context, record events.LogRecord, at time.Time) ([]byte, error)` | 最终格式化函数，接收中心记录的时间 |
| `defaultContentLimit` | `internal/logging/record.go`，text 默认字符上限 256 |
| `NamingScheduledEvent`、`NamingCompletedEvent` | 沿用 `internal/session/types.go` 与 `internal/session/naming_signals.go` 的位置；Scheduled 表达调度，Completed 统一表达命名流程结束，由来源填写整体结果 |

九字段名称、正文格式和身份表示已经确认，不因实现便利改回含糊字段或增加兜底入口。沿用现有包及文件，不新增包或拆文件；底层字段注释写清职责、零值及处理规则。

后续来源迁移前仍必须向用户列出并确认：

1. 涉及模型调用、工具调用、命名兜底、回复发送与保存等来源事件的完整字段和类型，逐项明确各操作事实与错误的对应关系、存在性、未执行与部分完成的表示及整体结果的业务含义。来源提供对应的日志正文示例；事件字段不要求与正文逐项对应，不能只给出几个错误字段示例，也不能照搬旧实现确定新契约。
2. 受影响日志辅助函数、发布方法及新增或调整的变量名；任何新增、拆分、移动文件都要先给候选位置并讨论。
3. Reader 查询返回接口：如何同时返回有效记录、损坏行数量及原始行信息，以及命令如何取得并展示这些信息。先明确完整类型和字段，不在实现时自行添加生产名称。

确认结果写回具体契约后，再进入相应代码实施；不能把待确认项理解为实现者可自行选择。实施中遇到未约定的新名称时逐个向用户确认，包括内部变量；测试代码的名称和实现可自行决定。

## 设计参考

- [Godot：Using signals](https://docs.godotengine.org/en/stable/getting_started/step_by_step/signals.html)
- [Godot：Signal](https://docs.godotengine.org/en/stable/classes/class_signal.html)
- [Godot：Singletons / Autoload](https://docs.godotengine.org/en/stable/tutorials/scripting/singletons_autoload.html)
- [Godot：Autoloads versus regular nodes](https://docs.godotengine.org/en/stable/tutorials/best_practices/autoloads_versus_regular_nodes.html)

ElBot 借鉴信号的使用和所有权方式，不复制 Godot 节点树和帧循环。Go 的 context、队列、背压、连接清理和退出预算使用本项目的显式生命周期。
