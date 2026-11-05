## 报错链路清理

核心：1. 有的是错误了记一下，然后继续执行，此时调用日志信号记一下，然后继续，不会返回错误，也不合并。2. 有的是错误了返回，不再会继续执行，他就不该发信号，而是返回后上层统一发，而且因为这种类型都是直接报错就返回，所以也不存在合并错误的情况。
只有两种处理：

# 非致命：这里决定继续，就在这里处理完
def 下层():
    try:
        可选操作()
    except Error as err:
        log_signal(err)
        # 需要通知时 notify(err)
        # 不返回这个错误，不留到后面合并

    继续执行()

# 致命：失败直接往上传，不报告、不继续
def 下层():
    必要操作()     # 失败直接抛出

def 中间层():
    下层()         # 原样向上传递
    后续操作()     # 下层失败就不会执行

# 最上层：覆盖整条链路
def 消息入口(ctx):
    try:
        ctx, input = 准备输入(ctx)
        ctx, result = 执行任务(ctx, input)
        return ctx
    except Error as err:
        report(ctx, err)
        return err

dialogue 若在 执行任务 内部，也在这个最外层捕获范围里。
- 决定继续的错误：处理完就结束错误传播，删除继续执行后又返回、合并的逻辑。
- 决定终止的错误：立即返回，删除下层和中间层的错误报告，最终由最上层统一报告。
以上不动
---

### 已完成：公共报告与通知投递

- [日志入口](../internal/events/logging.go) `Log` 同步发布，`AsyncLog` 异步发布；均无返回值，发布失败交给底层诊断。身份字段使用字符串，有值直接写入，无值写空字符串，不复制指针或补上下文。
- [通知入口](../internal/events/notification.go) 提供 `Notify`、`LogAndNotify`，复用两条现有信号；通知接纳失败只记日志，两次发布互不阻断。内容、身份、目标及取消策略由来源决定。
- 普通通知发布已接入 `Notify`；重试通知不再返回投递错误，Hook 直接接入公共日志、通知入口，通知回调无返回值。业务层各处 `ReportFailure` 及终止错误的报告位置仍待下列整改。
- [通知路由](../internal/platform/notification/manager.go) 和平台通知订阅各自处理本阶段故障后结束，不再返回或合并已处理的错误，不递归通知；投递故障在消费处直接构造通知日志并调用 `AsyncLog`，不借用聊天日志包装；路由故障携带执行身份；取消、过期 binding、正常关闭保留退出语义。
- [CLI 发送](../internal/platform/cli/remote_server.go) 的单连接 write 只返回；握手及补全发送失败由处理处记录客户端、操作和执行身份；通知逐连接失败记录后继续。普通多连接发送中，决定继续的失败当场记录；若最后一个连接也失败且此前没有成功，直接返回该错误，不重复记录、不合并前面的错误。

### 统一报告边界

- 每条任务调用链只在最上层统一捕获终止错误，覆盖输入准备、执行、Dialogue 及收尾；不拆成输入、执行等阶段分别报告。
- 删除 Input、Execution、Dialogue 各处分支中的 `ReportFailure`。下层和中间层只返回终止错误，不发失败日志或通知；最上层报告后可将错误作为任务结果返回，外围不再重复报告。
- [ReportFailure](../internal/agent/share/logging.go) 必须记录完整错误，替换只有 `dialogue terminated` 的日志；保留原错误内容及错误链，不重复拼接厂商详情。
- 保留出错任务的 Session、Request、模型和通知目标上下文；具体传递方式待实现前确定，不能以此为由改成多层报告，也不增加“已报告”标记。
- 取消、接管、暂停沿用各自的结果语义，取消仍记录 Canceled，不伪装成普通业务失败。
- 正常业务事实与错误报告分开：模型用量、成功回执等事实保留，不能借事实记录重复报告终止错误。公共 Retry 继续负责决定重试时的事件；最终失败交回最上层。

### 已确认整改项

| 位置 | 当前问题 | 修改方向 |
|---|---|---|
| [Hook 流水线](../internal/hook/pipeline/manager.go)：run、notify、notifyError | 普通 Hook 失败先记录再返回；通知型 Hook 继续执行后又合并返回；error.occurred 回调被混同于用户通知 | 普通故障就地记日志、发用户通知，保留前面成功的修改、丢弃失败 Hook 的修改，继续后面的 Hook；不返回或合并该错误。整次任务取消仍向上传递；error.occurred 不替代用户通知，也不递归报告自身失败。 |
| [请求准备](../internal/agent/dialogue/model_call.go)：PrepareCall | 请求 Hook 失败后保存，再合并错误 | 普通 Hook 故障在流水线处理完；必要准备或用户消息保存失败立即返回，不记录失败信号，不执行后续模型请求。 |
| [回复提交](../internal/agent/dialogue/reply_commit.go)：Commit | 发送完全失败跳过保存；部分失败与保存错误合并，保存后又返回发送错误 | 发送失败独立记录、通知，继续保存模型回复及成功回执；保存失败立即返回并停止后续操作；保存成功继续附加输出，附加输出失败独立处理。不保留等待合并或再次返回的发送错误。 |
| [发送层](../internal/agent/output/output.go)：StartStream、observeDelivery | 正文增量、Finish、Reset 失败记录后返回；最终发送也先记录失败再返回 | 需要上抛的发送错误只返回，由调用方决定继续或终止。思考展示等已决定忽略的故障处理完不返回；保留成功发送事实。 |
| [MessageStore](../internal/agent/dialogue/message_store.go)、[SQLite Commit](../internal/storage/sqlite/dialogue_repository.go) | defer 记录失败并返回，使用 persisting、level 等状态分配日志责任 | 删除失败报告及其专用状态，保存失败直接返回；保留事务回滚和必要资源释放。 |
| [Chat 工具提交](../internal/agent/dialogue/commit.go)、[Responses 工具提交](../internal/agent/responses/persistence.go) | 准备、投影、保存失败时 defer 发日志后返回 | 终止错误直接返回，不由提交层报告。 |
| [Chat 压缩](../internal/agent/chat/compressor.go)、[Responses 压缩](../internal/agent/responses/compact.go) | 失败自己记录、通知后返回，调用入口再次报告 | 删除压缩内部失败报告，交给自动压缩所属任务或命令所属任务的最上层。 |
| [命名 fallback](../internal/session/naming/title.go) | 首选失败后继续却未独立记录；fallback 失败时合并两个错误 | 决定继续 fallback 时记录首选失败并结束其传播；fallback 失败只返回自身错误。没有 fallback 时不制造第二个错误与首选失败合并。 |
| [工具执行](../internal/tool/execution/runner.go) | 工具执行、输出发送和完成 Hook 错误被合并；保存失败后继续收集错误 | 工具业务结果、输出投递故障、Hook 故障分别处理；普通 Hook 故障不阻止工具执行，输出失败记录后继续。记录准备和保存失败属于终止错误，不转换成普通工具结果。 |
| [命令入口](../internal/agent/input/command_executor.go) | 命令失败仍先通知再返回，报告边界待统一 | 命令错误直接返回最上层；通知投递错误独立处理，不叠加到业务错误。 |
| [入站媒体关联](../internal/agent/input/inbound_media.go)：associateInboundHistory | 逐项失败记录、继续，最后合并返回 | 每项关联失败记录后继续，删除错误收集和 Join；前置查询等直接返回的错误不在下层报告。 |
| [发送回执缓存](../internal/platform/dispatch/media.go)：cacheMediaReceipt | 逐项缓存失败后继续，最后合并给上层记录 | 在决定继续的位置逐项记录，删除错误集合和返回；不改变实际发送结果。 |
| [工具状态](../internal/tool/state/state.go)：Commit、DiscoveredToolsForSession | 状态保存和读取失败都在 defer 中记录后返回 | 删除下层失败日志，原错误向上传递。 |
| [Skill 预载](../internal/tool/preload/preload.go)：PrepareSkills | 详情读取失败记录 skill_preload_failed 后返回 | 删除下层失败报告，预载失败直接返回。 |
| [压缩 seed 消费](../internal/agent/chat/context_seed.go)：consumeContextCompactSeed | 消费失败先记录再返回 | 删除下层失败报告，直接返回并停止后续业务。 |

### 需要明确拆开控制流程的位置

- **工具并发执行**：[runner.go](../internal/tool/execution/runner.go) 的 await 和结果消费循环在保存失败后仍收集、合并后续错误；Prepared 保存失败还会生成普通工具失败结果后继续。首个终止错误发生后停止派发和业务处理，取消并等待已启动工作退出，返回该终止错误。等待退出属于必要收尾，不是继续执行业务，不以取消错误覆盖或合并原错误。
- **Responses 请求归档**：[native_call.go](../internal/agent/responses/native_call.go)、[compact.go](../internal/agent/responses/compact.go) 在 defer 中编码、归档，失败时记录并覆盖请求错误。请求结果与归档必须成为明确的两个阶段，不通过 defer 偷改返回错误，不合并错误；失败请求归档的职责及归档再次失败时的处理需进一步确定。
- **必要资源收尾**：[工具文件写入](../internal/tool/media/files.go)、[Hook 子进程执行](../internal/hook/rules/exec.go)、[Hook worker](../internal/hook/runtime/worker.go)、[Hook 媒体释放](../internal/hook/runtime/media.go) 仍存在关闭、终止、释放错误的合并。逐处确定收尾失败是记录后继续，还是本次唯一终止错误；不机械删除关闭、回滚或等待退出操作，不借收尾继续执行后续业务。
- 同步迁移依赖旧行为的测试：下层失败不发报告；最上层只报告一次且包含原错误；普通 Hook 失败后继续；发送失败仍保存；保存失败阻止后续业务；并发失败停止派发但等待退出；取消不被额外错误合并成失败。

## 不一定实现

- [ ] CLI TUI 支持 Markdown 渲染。
- [ ] CLI 用户名和助手显示名可配置。
- [ ] CLI 支持本地图片输入。
- [ ] 周期 Cron 支持过期时间。


## 暂缓

- MCP 接入：暂不考虑，使用 Skill。
- 子 Agent：暂不考虑，信息交流会带来噪声或损失。
