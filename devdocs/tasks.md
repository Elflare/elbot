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

- [日志入口](../internal/events/logging.go)：`Log` 同步发布，`AsyncLog` 异步发布；均无返回值，发布失败交给信号诊断。`LogIdentityFromContext(ctx)` 直接提取执行身份，不增加错误日志包装；已建立会话的 SessionID 不判空、不补默认值，RootRequestID 允许为空。
- [通知入口](../internal/events/notification.go)：`Notify`、`LogAndNotify` 复用现有信号。日志与通知独立发布，通知接纳、路由和投递失败在对应消费处记录，不递归通知、不改变业务结果。
- [CLI 多连接发送](../internal/platform/cli/remote_server.go)：决定继续的连接失败就地记录；最后一个连接失败且此前没有成功时直接返回该错误，不合并先前已处理的错误。

### 已完成：任务报告边界

- 前台任务由 [Supervisor.HandleMessage](../internal/agent/supervisor.go) 统一接住输入、命令、执行、Dialogue 和收尾的终止错误并直接报告。Input、Execution、Dialogue 和存储层只返回终止错误。
- 更新执行事实的协调层显式返回最新 `context.Context`，保留会话、请求、模型和通知目标；后台调用返回上下文保留调用方生命周期，不携带已结束请求的取消状态。无“已报告”标记，也不把上下文塞进错误包装。
- 最终报告保留完整原错误；取消记 Canceled，暂停、接管和被替代的 attempt 保留各自语义。模型用量和成功回执单独记录事实，不重复附带终止错误。重试事件仍由公共 Retry 负责。
- Cron 的 [runJob](../internal/background/cron/manager.go) 负责整次调度任务报告；恢复任务在各自入口报告。Elnis 的入站任务、[RunLLMEvent](../internal/background/elnis/llm.go) 和报告恢复分别拥有自己的最外层边界；决定继续保存失败状态时，先处理业务错误，保存再失败只传播保存错误。
- Cron 下一次运行时间保存、Request 取号失败只返回，由各自任务入口报告。后台日志直接使用公共日志入口，不保留单行转发包装。

### 已完成：继续与终止的处理

| 位置 | 当前职责 |
|---|---|
| [Hook 流水线](../internal/hook/pipeline/manager.go) | 普通故障就地记日志、通知，保留前面成功的修改，不接纳失败 Hook 的返回值，继续后续 Hook；复用已有只读原文，不增加全事件快照。整次任务取消向上传递；error.occurred 自身失败只记录，不递归通知。 |
| [请求准备](../internal/agent/dialogue/model_call.go) | 必要准备和用户消息保存失败立即返回，停止模型请求；普通 Hook 失败由流水线处理。 |
| [回复提交](../internal/agent/dialogue/reply_commit.go) | 发送失败独立处理后仍保存模型回复和成功回执；保存失败立即返回，阻止附加输出；附加输出失败独立处理。不在结果结构中保留等待合并的发送、保存错误。 |
| [发送层](../internal/agent/output/output.go) | 需上抛的发送错误只返回；思考展示等决定忽略的故障处理后结束传播。成功回执独立记录。 |
| append 超时提示、压缩提示 | 发送失败就地异步记录后继续；取消记 Info，不额外通知或返回。 |
| Cron／Elnis 格式纠正请求 | 执行成功且未被接管后才解析 JSON。Cron 执行失败原样返回；Elnis 执行失败记录原因并保存失败状态。只有实际解析失败才进入格式错误处理。 |
| [MessageStore](../internal/agent/dialogue/message_store.go)、[SQLite](../internal/storage/sqlite/dialogue_repository.go)、Chat／Responses 工具提交 | 保存失败直接返回；保留事务回滚与资源释放。 |
| Chat／Responses 压缩、[工具状态](../internal/tool/state/state.go)、[Skill 预载](../internal/tool/preload/preload.go)、压缩 seed 消费 | 终止错误交给任务入口，不在下层重复报告。 |
| [命名 fallback](../internal/session/naming/title.go) | 有 fallback 时先记录首选失败，再尝试 fallback；fallback 失败只返回自身错误。没有 fallback 时直接返回首选错误。 |
| [入站媒体关联](../internal/agent/input/inbound_media.go)、[回执缓存](../internal/platform/dispatch/media.go) | 逐项失败在决定继续的位置独立记录，不收集或合并错误。 |

### 已完成：并发、归档与资源收尾

- [工具执行](../internal/tool/execution/runner.go)：工具业务结果、输出故障、Hook 故障分别处理。准备记录和结果保存失败属于终止错误；立即停止派发、取消并等待已启动工作退出，保留首个终止错误，不继续提交其余结果。
- Responses [普通请求](../internal/agent/responses/native_call.go) 与[压缩](../internal/agent/responses/compact.go)：请求、归档为显式阶段。请求成功而归档失败时返回归档错误；请求已失败时，归档故障独立记录，仍返回原请求错误。Chat／Responses 不用 defer 以取消错误覆盖已发生的其他错误。
- [工具文件写入](../internal/tool/media/files.go)、[Hook 子进程](../internal/hook/rules/exec.go)、[worker](../internal/hook/runtime/worker.go) 和[媒体释放](../internal/hook/runtime/media.go)：保留关闭、终止和等待退出；次要收尾故障独立记录，不覆盖或合并主错误。失败媒体 lease 保留后续清理机会。
- 上述任务链路的测试已迁移，并覆盖原错误只报告一次、完整上下文、Hook 失败后继续、发送失败仍保存、保存失败停止后续业务、并发退出及请求／归档同时失败。定向竞态检查通过。
- 全仓测试仍有改前已存在的失败：Agent 命令通知接线、空回复／缓冲回复、协议公共接线、App doctor；不将这些既有失败标为本轮通过。

## 不一定实现

- [ ] CLI TUI 支持 Markdown 渲染。
- [ ] CLI 用户名和助手显示名可配置。
- [ ] CLI 支持本地图片输入。
- [ ] 周期 Cron 支持过期时间。


## 暂缓

- MCP 接入：暂不考虑，使用 Skill。
- 子 Agent：暂不考虑，信息交流会带来噪声或损失。
