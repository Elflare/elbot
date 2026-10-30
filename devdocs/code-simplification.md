# 代码精简

注：本次重构不更新用户文档。除非用户要求。
更新进度同步在## 已完成精简中。

## 代码量基线

统计日期：2026-10-29。本节是逐包精简前的统计快照，用于选择审查目标；行数不代表代码质量，也不能直接证明存在冗余。

统计口径：

- 每个目录作为独立 Go 包，子包单独统计，不向父包合并。
- 主排序排除 `_test.go`，按非测试文件的平均物理行数降序排列；行数包含注释和空行。
- 使用 `rg --files` 枚举 Go 文件，遵循其默认忽略规则，排除 vendor、testdata 和带标准生成标记的代码；本次未发现生成代码。
- 下表记录前 20 名，包路径均省略 `internal/`。

| 排名 | 包 | 文件数 | 总行数 | 平均行数/文件 |
|---:|---|---:|---:|---:|
| 1 | delivery | 1 | 479 | 479.0 |
| 2 | turn | 2 | 932 | 466.0 |
| 3 | hook/runtime | 8 | 2,836 | 354.5 |
| 4 | tool/builtin | 21 | 7,311 | 348.1 |
| 5 | memory/resident | 1 | 345 | 345.0 |
| 6 | hook/rules | 8 | 2,640 | 330.0 |
| 7 | config | 8 | 2,611 | 326.4 |
| 8 | launcher | 1 | 291 | 291.0 |
| 9 | storage/sqlite | 17 | 4,768 | 280.5 |
| 10 | platform/refcontext | 1 | 280 | 280.0 |
| 11 | hook/pipeline | 2 | 528 | 264.0 |
| 12 | hook/control | 2 | 490 | 245.0 |
| 13 | background/cron | 11 | 2,693 | 244.8 |
| 14 | elyph | 3 | 733 | 244.3 |
| 15 | logging | 4 | 971 | 242.8 |
| 16 | platform/cli | 21 | 4,712 | 224.4 |
| 17 | platform/qq-onebot | 11 | 2,413 | 219.4 |
| 18 | command/builtin | 18 | 3,827 | 212.6 |
| 19 | request | 1 | 212 | 212.0 |
| 20 | tool/preload | 3 | 608 | 202.7 |

全仓共 93 个含非测试 Go 文件的包，579 个非测试 Go 文件、91,639 行；另有 490 个测试文件、81,252 行。

`internal/app` 已完成一轮精简，在本次统计中排第 51：6 个非测试文件、719 行，平均 119.8 行/文件。

## 逐包审查记录

按包记录具体问题、精简建议、讨论结论与验证结果，确认方案后再实施。任务入口见 [任务清单](tasks.md)。

当前进度：`internal/app`、`internal/delivery`、`internal/turn`、`internal/tool/builtin`、`internal/memory/resident`、`internal/hook/rules`、`internal/storage/sqlite` 已完成一轮审查与调整；`internal/config` 已完成第三轮精简；`internal/hook/runtime` 已完成第三轮精简；`internal/hook/control`、`internal/background/cron` 已完成一轮精简，其余包进度分别记录。

### delivery

- 普通发送统一使用 `MessageSender`；`ValidateOutputs` 返回批次目标，`SendTo` 复用结果，显式目标优先。
- `Output.Delivery` 使用值类型 `DeliveryInfo` 保存发送时机与 Hook 来源；复制输出后的修改互不影响，时机可重新设为立即发送。
- 表情构造使用 `Emoticon(id, name)`，名称在公共发送校验边界检查为非空；Hook 表情只取 `name`，降级显示 `[表情: 名称]`。
- 降级文案集中在 `FallbackText`，媒体共用标签拼接逻辑；来源日志保留身份、状态与部分回执语义，取消隔离由 `events.EmitLog` 负责。
- 用户协议见 [Hook 文档](../docs/hooks.md)，内部职责见 [代码地图](code-map.md#输出投递与通知)。

### hook/runtime

- 活动事件统一按 token 保存 `invocation`，同时承载工具授权、触发事实和取消能力；事件完整收尾后回收。同路由多个 Worker 均可取消，工具次数耗尽或其他进程误用 token 不影响原调用的取消能力。
- 临时 Worker 按插件与路由索引；waiting 直接持有所属 Worker，统一查询和过期处理。租约覆盖、停止及退出只清理对应实例；事件输出和消息校验成功后才发布 waiting。
- RPC 回包在同一把锁内取走 pending 项并投递一次，退出只关闭剩余请求；重复回包不阻塞。路由锁在持有者和等待者全部离开后回收，Worker 状态在最后一个活动调用结束后恢复 ready。
- 媒体租约仅使用 `busy` 表示独占，清理和重试共用日志入口；共享 KV 的 Get/Delete 共用 key 解析，Set/CAS 共用参数解析、TTL 校验及写入路径，先清理过期项再执行 LRU 淘汰，JSON 压缩同时完成校验。
- 普通后台任务通过 `startTask` 原子检查关闭并登记收尾计数；持锁登记的进程退出、调用与 waiting 定时器保留各自原子边界。配置替换与整体关闭共用 `stopWorkers` 并发停止并汇总错误。
- KV 与媒体通过 `maintain` 共用调度循环实现，但各自独立运行；清理后读取下一期限，保留到期索引、唤醒、媒体重试及关闭等待，媒体 I/O 不阻塞 KV 到期。
- 消息修改后统一同步工具结果，保留 segments 优先级及空修改语义；工具授权复用审计阶段已选定的 invocation。当前共 9 个生产文件，无新增包、文件或公共接口。
- 工具审计只确定一次触发者身份，授权仍独立核验 token 并登记次数；续接与工具审计共用 `logIdentity`，调用前通用失败共用局部 `failCall`。Worker 就绪等待及 RPC 失败共用 `withFault` 补充当前故障，插件明确返回的错误保持独立。

### tool/builtin

- `PreparedCall.Call` 统一将独立日志正文限制为 1024 rune；工具回调生成摘要，业务结果与原始错误完整返回。准备失败及执行前拒绝仍由执行层限制日志长度。
- Shell 在准备阶段解析一次 Bash AST，复用于目录切换、文件保护与警告、风险分类和受限路径检查；保持检查顺序、解析失败及 PowerShell 的风险策略。
- 文件搜索与 Cron 写操作各自共用结果和日志收尾；删除无意义嵌套块、无消费者字段和辅助函数。
- Shell、文件与 Web 工具显式接收构造依赖；编辑与撤销工具共用装配层注入的 `fileops.Service`。实现仍位于原有 21 个生产文件，工具参数、业务输出与持久化格式保持现有契约。

### memory/resident

- Store 直接使用上游规范身份，按平台与 ActorID 精确匹配；路径判空集中在加载入口，空 TOML 交给解析器处理。追加内容非空、空记录删除等业务判断保留。
- 读取共用锁内缓存；修改先作用于单条记录副本，校验通过且需要落盘后只复制一次列表，保存成功后直接更新缓存。保留外部文件变化检测及原有写盘方式。
- 修改回调直接返回目标部分的长度校验结果，core 与 normal 共用校验函数；内容清理不再重复处理同一输入。校验先于无变化判定，未修改部分超限不影响操作。

### hook/rules

- 每个配置文件先归一化再校验，保留错误顺序与插件隔离；合并后一次确定最终名称、记录重命名并向动作传递来源与环境。
- 每条规则只注册一次，普通动作与 Worker 共用角色判断；角色别名在加载时归一化，各角色数组内取 OR、数组间取 AND，空数组不限制。
- 运行日志共用 `logIdentity`，保留缺失身份为 nil、事件 SessionID 优先及 Actor 选择；配置加载日志直接记录全局事件。
- 动作直接使用 `Pattern`，合并相同文本字段分支，复用输出目标与日志截断；不含模板起始标记的字符串直接返回。逐动作只读校验、命令校验与进程生命周期保持原有边界。
- TOML 诊断保留解析器提供的位置、字段、片段与原始错误链，删除英文错误字符串匹配和源码扫描猜测。仍为原有 8 个生产文件。
- 插件相对路径在拼接前完成范围校验；进程启动成功后的退出路径各执行一次 Wait 和 stderr Flush。stderr 行清理与空行过滤集中在日志入口，失败收尾只读取一次尾部；快捷输出直接比较结构体零值，协议错误正文供日志复用并保留错误链。

### hook/control

- 重载直接在 HookReload 持锁执行，停止统一走 StopHook，删除单调用方重载包装和无生产调用方的停止包装。
- 列表在 formatHookList 按类型生成头部，共用描述、活动数和换行；runtime 活动数（含零值）优先，普通插件按规则汇总。
- 保留准备与提交阶段校验、候选快照、插件名优先查询及原有输出格式；补全忽略未使用的 context 返回值，列表删除重复去空白和循环变量复制。

### config

- `applyState` 在合并状态时返回实际覆盖的字段路径，配置合并与诊断来源共用同一组条件；模型槽整项覆盖，命名与压缩模型任一字段非空即覆盖。
- `LoadEnvFile` 直接负责 dotenv 解析，`ConfigEnv` 通过 `LoadDotEnv` 查询；保留进程环境优先、首个重复键生效及缺失文件返回空集合的行为。
- TOML 解析错误统一由 `wrapDecodeError` 包装；厂商配置在定义的 Apply 中直接赋值，维护任务共用 `MaintenanceCleanupConfig`。无消费者的 `ModelConfigs` 及冗余私有包装已删除。
- `ruleAt` 通过局部递归只访问匹配前缀的分支；`CheckRules` 递归传递完整路径与 Topic，直接生成诊断，不再改写已有消息。
- 模块子树只解码一次，按声明检查类型与未知字段；模块规则统一由根文档检查，诊断直接使用完整字段路径，不展示重编码子树的行列号。资产排序只读遍历包内资产列表。
- 资产定位以资产声明提供默认路径，字段类型错误仍阻断定位；删除无效路径错误缓存与来源空前缀记录，由 Source 统一回退主配置来源。
- 平台路径初始化直接返回现有入口的结果；会话超时在默认配置中初始化，在应用默认值时归零负数，保留显式零禁用语义。
- 状态模型槽只查询一次存在性；诊断通过 ContainsFunc 提前跳过重复或被缺失父节点覆盖的条目；厂商刷新通过 loadTOML 单次解码。
- 仍为原有 8 个生产文件；公开接口、配置格式、默认值与默认资产保持现有契约。

### storage/sqlite

- 主库与聊天历史库共用现有 store.go 中的 openDB 初始化连接，各自负责建表、迁移及失败关闭；保留单连接和外键设置。
- 原生待处理输入复用扫描函数，工具批次复用已解码 metadata，时间空值统一使用 nullTime；聊天历史反转使用 slices.Reverse，删除 Cron 无效参数和 nil 分支。
- 媒体回收使用 ClaimOrphans／FinishDelete，删除无业务调用的 DeleteOrphans 及接口声明。Session 列表在错误路径关闭 rows，并在读取消息摘录前显式释放连接。
- 回归先复现损坏时间导致 List 返回后仍占用唯一连接，修复后验证连接释放及后续查询。已执行 gofmt，Storage 包测试、SQLite 竞态测试和 go test ./... 全仓测试均通过。
- 第二轮直接绑定布尔 SQL 参数，媒体 JSON 单次解码，分批查询使用 slices.Chunk；输入消费和消息摘录的固定条件移至循环外，错误返回复用已有 rows 关闭。新增非法媒体 JSON 事务回滚、无 checkpoint 消费拒绝及 500／501 条分批回归；gofmt、Storage 包测试与 SQLite 竞态测试通过。

### hook/pipeline

- run／notify 共用 invoke 管理 handler context、Observe 和 active 登记；正常、失败及 panic 路径均释放资源，保留各自事件传递、错误和日志策略。
- 插件替换一次遍历收集候选顺序与注册，使用 slices.Insert 保持插入位置；List 按规则名一次统计活动执行。唤醒默认值直接由执行分支处理，输出标记原地修改，消息日志文本只提取一次。
- 回归覆盖执行结束取消、父 context 隔离、Observe 收尾、panic 释放、插件新增与删除及排序、同名活动计数、Stop 取消和输出长度缩短。gofmt、go test ./internal/hook/... 与 go test -race ./internal/hook/pipeline 均通过。

### logging

- 查询先区分 JSON 与旧文本格式，只执行对应解析，行首尾空白只处理一次；保留旧文本兼容、原始记录和损坏行判定。只读查询 map 保持 nil，等级与保留天数各在现有边界归一化一次。
- 数组筛选、反向扫描分别使用 slices.ContainsFunc、slices.Reverse 和 min；格式化函数删除未使用的 context 参数。公共接口、日志格式、扫描边界及关闭资源顺序保持不变。
- 复用现有混合格式、损坏行、查询条件、跨块扫描、UTF-8 与大小上限、背压和关闭回归。gofmt、go test ./internal/logging ./internal/command/builtin 和 go test -race ./internal/logging 均通过。

### background/cron

- direct／LLM 共用 runTask 准备与投递流程，保留原操作标签；报告正文统一存于 state.Report，模型选择在 modelForTask 完成校验与回退，直接复用共享 Runner 和 JSON 解析器。
- 十处日志身份提取共用 cronLogIdentity，发送日志保留报告 SessionID 优先；任务和发送完成日志各自统一构造，由源头结果决定事件名与级别，业务错误独立返回。
- 错误返回删除重复判断、变量复制与嵌套；目标解析删除未使用参数，管理员 ID 复用入口归一化结果。保留恢复重读、逐项接管检查、条件提交、迟到回执、取消收尾和生命周期同步。

## 已完成精简

| 包 | 完成情况 | 非测试 Go 行数 |
| --- | --- | --- |
| `internal/background/cron` | 合并执行和完成日志，统一身份提取，删除重复返回值、包装与检查。 | 2,693 → 2,413，减少 280 行（约 10.4%），仍为 11 个生产文件。 |
| `internal/logging` | 删除重复解析、归一化和无用分配，简化扫描与匹配循环，移除未使用参数。 | 971 → 955，减少 16 行（约 1.6%），仍为 4 个生产文件。 |
| `internal/hook/pipeline` | 共用 handler 执行收尾并补齐 context 取消，简化插件替换、活动计数及重复处理。 | 本轮 522 → 499，减少 23 行；相对初始基线 528 行累计减少 29 行（约 5.5%），仍为 2 个生产文件。 |
| `internal/storage/sqlite` | 已完成第二轮；共用连接和扫描，删除遗留接口与重复转换、校验及关闭，简化分批和循环判断。 | 本轮 4,717 → 4,708，减少 9 行；相对初始基线累计减少 60 行（约 1.3%），仍为 17 个生产文件；连同 Storage 接口声明累计减少 61 行。 |
| `internal/app` | 已完成一轮；本文件未登记具体改动及对比数据。 | 基线为 719 行。 |
| `internal/delivery` | 统一发送与投递信息契约、复用目标校验结果、收拢降级文案并清理重复日志处理。 | 479 → 415，减少 64 行（约 13.4%）。 |
| `internal/turn`（含子包） | 完成生命周期与确认等待职责拆分、新接口及调用方迁移、刷新与过期原子裁决；删除旧接口测试，保留有效行为覆盖。 | 932 → 1,010，增加 78 行（约 8.4%）；当前共 11 个生产文件，含装配与内部契约，未实现总代码量下降。 |
| `internal/hook/runtime` | 已完成第三轮；共享 RPC 按参数分组，收拢日志身份、调用前失败及 Worker 故障补充，删除重复触发者身份推导。 | 本轮 2,995 → 2,950，减少 45 行（约 1.5%），仍为 9 个生产文件；近两轮共减少 101 行，相对初始基线 2,836 行仍增加 114 行。 |
| `internal/tool/builtin` | 统一日志截断、复用 Shell AST、合并分支收尾、清理无效状态并显式注入依赖。 | 7,311 → 6,988，减少 323 行（约 4.4%），仍为 21 个生产文件。 |
| `internal/memory/resident` | 删除重复身份与路径检查，合并修改和长度校验，缓存仅在提交修改前复制列表。 | 345 → 315，减少 30 行（约 8.7%），仍为 1 个生产文件。 |
| `internal/hook/rules` | 已完成第二轮；统一路径与 stderr 入口校验，删除无效等待状态，快捷输出直接比较零值，协议日志复用错误正文。 | 本轮 2,152 → 2,141，减少 11 行；相对初始基线 2,640 行累计减少 499 行（约 18.9%），仍为 8 个生产文件。 |
| `internal/hook/control` | 合并列表展示与活动数统计，删除冗余重载、停止包装及重复处理。 | 490 → 459，减少 31 行（约 6.3%），仍为 2 个生产文件。 |
| `internal/config` | 已完成第三轮；删除无效路径状态与重复兜底，内联会话超时默认值，简化诊断分支，厂商刷新单次解码。 | 本轮 2,580 → 2,542，减少 38 行；相对初始基线 2,611 行累计减少 69 行（约 2.6%），仍为 8 个生产文件。 |

delivery 本轮连同 Hook 调用方共净减 70 行非测试 Go 代码，未拆分文件。回归覆盖表情名称、发送时机重设与复制隔离、目标选择与冲突、Hook 来源、媒体降级及日志与业务结果独立；已执行 `gofmt`，相关包测试与 `go test ./...` 通过。

turn 已完成五项 review 修复：统一 `Supervisor`、attempt 与 `Snapshot` 契约；`BeginAppend` 一次登记返回等待句柄；`RiskDecision`／`RiskResult` 分离用户动作与等待终态；生命周期锁内通过 `Expire` 裁决待处理刷新；确认词与计数展示分别归 Agent、Command。删除 `turn/manager.go` 及重复旧等待测试，收紧内部可选参数，清除迁移占位。

turn 回归保留输入与媒体合并、工具计数、停止后等待真实退出、提前回复和迟到清理；使用 `testing/synctest` 覆盖旧计时器触发后的刷新竞争及再次过期。调用方验证批准范围、拒绝、停止、过期、取消及提示发送失败。已执行 `gofmt`；相关包测试、`go test ./...` 及 Turn、Agent confirmation／execution／dialogue 的竞态检查通过。

hook/runtime 本轮基线为 2,995 行，保留既有关闭、可取消写入、到期维护及消息修改语义。共享 RPC 测试改为连续操作表，覆盖各方法响应、无关字段忽略、CAS 的 expected 缺省与 null 区别；审计测试补充事件 session 优先、context 回退及身份缺失，协议失败测试验证已有 Worker 故障不会污染插件明确返回的错误。测试代码净增 7 行，本轮 Go 代码合计净减 38 行。

hook/runtime 已执行 `gofmt`、`go test ./internal/hook/...` 及 `GORACE=atexit_sleep_ms=0 go test -race ./internal/hook/runtime`，全部通过。

tool/builtin 本轮连同 Tool 内部调用方共净减 332 行非测试 Go 代码。回归覆盖日志边界与完整业务结果、Shell 检查顺序与解析失败、Cron 四种写操作的结果和错误；相关构造调用及 Hook 日志测试已迁移。已执行 `gofmt`，`go test ./...` 通过。

Tool 竞态检查中除 execution 外的包均通过，无数据竞争报告。execution 的 `TestRunConcurrentFailuresKeepOriginalResultOrderAndFlushOnce` 存在调度顺序断言不稳定，使用改动前执行边界的 Go overlay 已复现；本轮未调整执行调度。初次竞态检查还出现取消完成断言失败，单独重复检查及最终整包检查未再次出现。

memory/resident 保留原有行为覆盖，新增拒绝写入不污染缓存与其他用户、无变化不落盘及时间戳不变、空文件与外部编辑内容追加的回归。测试代码增加 121 行，本轮 Go 代码合计增加 91 行。已执行 `gofmt`；resident、Tool 根包及 Agent dialogue／execution 包测试，以及 `GORACE=atexit_sleep_ms=0 go test -race ./internal/memory/resident` 均通过。

background/cron 回归覆盖 direct／LLM 操作标签、日志身份缺省与报告 SessionID 覆盖、完成／失败／取消日志单次报告，以及模型快照、前台接管、恢复、部分回执和混合失败。生产代码净减 280 行，测试代码净增 54 行，Go 代码合计净减 226 行；已执行 `gofmt`、`go test ./internal/background/...`、`GORACE=atexit_sleep_ms=0 go test -race ./internal/background/cron` 和本轮 diff 检查，均通过。

hook/control 回归覆盖空列表、独立规则、普通插件活动数汇总、runtime 活动数与描述优先级、多插件状态独立和 Unicode 截断；停止测试直接验证 StopHook。生产代码净减 31 行，测试代码增加 24 行，Go 代码合计净减 7 行。已执行 `gofmt`，`go test ./internal/hook/...` 与本轮 diff 检查通过。

config 回归覆盖状态覆盖与来源一致性、规则继承、模块类型与未知字段检查、资产路径缺省及错误引用、超时缺省与显式禁用、父子诊断顺序、厂商解析错误位置和凭据优先级。第三轮生产代码净减 38 行，测试代码增加 77 行，Go 代码合计增加 39 行；已执行 `gofmt`，`go test ./internal/config ./internal/doctor` 及本轮 diff 检查通过。

hook/rules 回归先复现并修复多角色命中时重复执行、Worker 角色条件绕过和角色别名未归一化；覆盖跨文件最终命名与动作环境、日志身份缺失和事件覆盖、普通文本与动作模板、TOML 原生诊断及错误链。首轮测试代码净增 207 行、Go 代码合计净减 281 行；第二轮补充路径越界与归一化、stderr 分段与空行、快捷输出冲突、协议错误链与日志一致性回归，测试净增 67 行、Go 代码合计增加 56 行。已执行 `gofmt`，`go test ./internal/hook/rules`、`go test -race ./internal/hook/rules` 与 `go test ./internal/hook/...` 均通过。
