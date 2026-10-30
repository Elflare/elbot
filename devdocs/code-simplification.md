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

当前进度：`internal/app`、`internal/delivery`、`internal/turn`、`internal/tool/builtin`、`internal/memory/resident`、`internal/hook/rules`、`internal/config` 已完成一轮审查与调整；`internal/hook/runtime` 已完成第三轮精简，其余包进度分别记录。

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

### config

- `applyState` 在合并状态时返回实际覆盖的字段路径，配置合并与诊断来源共用同一组条件；模型槽整项覆盖，命名与压缩模型任一字段非空即覆盖。
- `LoadEnvFile` 直接负责 dotenv 解析，`ConfigEnv` 通过 `LoadDotEnv` 查询；保留进程环境优先、首个重复键生效及缺失文件返回空集合的行为。
- TOML 解析错误统一由 `wrapDecodeError` 包装；厂商配置在定义的 Apply 中直接赋值，维护任务共用 `MaintenanceCleanupConfig`。无消费者的 `ModelConfigs` 及冗余私有包装已删除。
- 仍为原有 8 个生产文件；配置格式、默认值、默认资产与诊断机制保持现有契约。

## 已完成精简

| 包 | 完成情况 | 非测试 Go 行数 |
| --- | --- | --- |
| `internal/app` | 已完成一轮；本文件未登记具体改动及对比数据。 | 基线为 719 行。 |
| `internal/delivery` | 统一发送与投递信息契约、复用目标校验结果、收拢降级文案并清理重复日志处理。 | 479 → 415，减少 64 行（约 13.4%）。 |
| `internal/turn`（含子包） | 完成生命周期与确认等待职责拆分、新接口及调用方迁移、刷新与过期原子裁决；删除旧接口测试，保留有效行为覆盖。 | 932 → 1,010，增加 78 行（约 8.4%）；当前共 11 个生产文件，含装配与内部契约，未实现总代码量下降。 |
| `internal/hook/runtime` | 已完成第三轮；共享 RPC 按参数分组，收拢日志身份、调用前失败及 Worker 故障补充，删除重复触发者身份推导。 | 本轮 2,995 → 2,950，减少 45 行（约 1.5%），仍为 9 个生产文件；近两轮共减少 101 行，相对初始基线 2,836 行仍增加 114 行。 |
| `internal/tool/builtin` | 统一日志截断、复用 Shell AST、合并分支收尾、清理无效状态并显式注入依赖。 | 7,311 → 6,988，减少 323 行（约 4.4%），仍为 21 个生产文件。 |
| `internal/memory/resident` | 删除重复身份与路径检查，合并修改和长度校验，缓存仅在提交修改前复制列表。 | 345 → 315，减少 30 行（约 8.7%），仍为 1 个生产文件。 |
| `internal/hook/rules` | 单次注册并统一角色判断，合并配置收尾与日志身份，删除动作冗余字段及 TOML 猜测诊断。 | 2,640 → 2,152，减少 488 行（约 18.5%），仍为 8 个生产文件。 |
| `internal/config` | 状态覆盖与来源记录合一，收拢环境变量读取及 TOML 错误包装，清理无效包装和重复类型。 | 2,611 → 2,579，减少 32 行（约 1.2%），仍为 8 个生产文件。 |

delivery 本轮连同 Hook 调用方共净减 70 行非测试 Go 代码，未拆分文件。回归覆盖表情名称、发送时机重设与复制隔离、目标选择与冲突、Hook 来源、媒体降级及日志与业务结果独立；已执行 `gofmt`，相关包测试与 `go test ./...` 通过。

turn 已完成五项 review 修复：统一 `Supervisor`、attempt 与 `Snapshot` 契约；`BeginAppend` 一次登记返回等待句柄；`RiskDecision`／`RiskResult` 分离用户动作与等待终态；生命周期锁内通过 `Expire` 裁决待处理刷新；确认词与计数展示分别归 Agent、Command。删除 `turn/manager.go` 及重复旧等待测试，收紧内部可选参数，清除迁移占位。

turn 回归保留输入与媒体合并、工具计数、停止后等待真实退出、提前回复和迟到清理；使用 `testing/synctest` 覆盖旧计时器触发后的刷新竞争及再次过期。调用方验证批准范围、拒绝、停止、过期、取消及提示发送失败。已执行 `gofmt`；相关包测试、`go test ./...` 及 Turn、Agent confirmation／execution／dialogue 的竞态检查通过。

hook/runtime 本轮基线为 2,995 行，保留既有关闭、可取消写入、到期维护及消息修改语义。共享 RPC 测试改为连续操作表，覆盖各方法响应、无关字段忽略、CAS 的 expected 缺省与 null 区别；审计测试补充事件 session 优先、context 回退及身份缺失，协议失败测试验证已有 Worker 故障不会污染插件明确返回的错误。测试代码净增 7 行，本轮 Go 代码合计净减 38 行。

hook/runtime 已执行 `gofmt`、`go test ./internal/hook/...` 及 `GORACE=atexit_sleep_ms=0 go test -race ./internal/hook/runtime`，全部通过。

tool/builtin 本轮连同 Tool 内部调用方共净减 332 行非测试 Go 代码。回归覆盖日志边界与完整业务结果、Shell 检查顺序与解析失败、Cron 四种写操作的结果和错误；相关构造调用及 Hook 日志测试已迁移。已执行 `gofmt`，`go test ./...` 通过。

Tool 竞态检查中除 execution 外的包均通过，无数据竞争报告。execution 的 `TestRunConcurrentFailuresKeepOriginalResultOrderAndFlushOnce` 存在调度顺序断言不稳定，使用改动前执行边界的 Go overlay 已复现；本轮未调整执行调度。初次竞态检查还出现取消完成断言失败，单独重复检查及最终整包检查未再次出现。

memory/resident 保留原有行为覆盖，新增拒绝写入不污染缓存与其他用户、无变化不落盘及时间戳不变、空文件与外部编辑内容追加的回归。测试代码增加 121 行，本轮 Go 代码合计增加 91 行。已执行 `gofmt`；resident、Tool 根包及 Agent dialogue／execution 包测试，以及 `GORACE=atexit_sleep_ms=0 go test -race ./internal/memory/resident` 均通过。

config 回归覆盖未覆盖字段保留、部分及空模型槽的整项覆盖、命名与压缩模型覆盖条件，以及诊断来源与最终配置一致。测试代码增加 51 行，本轮 Go 代码合计增加 19 行。已执行 `gofmt`，config、doctor 与 background/maintenance 包测试及 `go test ./...` 全仓测试通过。

hook/rules 回归先复现并修复多角色命中时重复执行、Worker 角色条件绕过和角色别名未归一化；覆盖跨文件最终命名与动作环境、日志身份缺失和事件覆盖、普通文本与动作模板、TOML 原生诊断及错误链。测试代码净增 207 行，本轮 Go 代码合计净减 281 行。已执行 `gofmt`，`go test ./internal/hook/rules`、`go test -race ./internal/hook/rules` 与 `go test ./internal/hook/...` 均通过。
