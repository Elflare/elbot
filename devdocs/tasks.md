## 报错链路清理

核心：1. 有的是错误了记一下，然后继续执行，此时调用日志信号记一下，然后继续，不会返回错误，也不合并。2. 有的是错误了返回，不再会继续执行，他就不该发信号，而是返回后上层统一发，而且因为这种类型都是直接报错就返回，所以也不存在合并错误的情况。

| 位置 | 当前不符合的行为 |
|---|---|
| Hook 执行：pipeline/manager.go (internal/hook/pipeline/manager.go:129) | 失败先记日志，再返回；Agent 随后把普通 Hook 故障当成对话失败。通知型 Hook 则逐个继续执行，最后又把错误合并返回。两类处理混在一起。 |
| Hook 通知：notifyError (internal/hook/pipeline/manager.go:102) | 这里只触发 error.occurred Hook，不等于给用户发送通知。不能把现有调用当作已经完成你要求的 sendnotify。 |
| 请求准备与回复提交：PrepareCall (internal/agent/dialogue/model_call.go:223)、Commit (internal/agent/dialogue/reply_commit.go:65) | 请求 Hook 失败后保存，再合并错误；回复部分发送失败后保存，再合并错误。应该按步骤分别处理，不把失败攒到最后。 |
| 发送层：output.go (internal/agent/output/output.go:140) | 正文增量、Finish、Reset 失败会先记录日志，再把错误返回；最终发送也通过 observeDelivery 记失败日志后返回。是否中止应由接收结果的那一层决定并报告。 |
| 保存层：MessageStore (internal/agent/dialogue/message_store.go:44)、SQLite Commit (internal/storage/sqlite/dialogue_repository.go:18) | 在 defer 中记录失败日志，同时返回错误。还用 persisting 等状态区分由哪层记录，增加了交接逻辑。按新规则，失败直接返回即可。 |
| 工具记录准备与保存：Chat committer (internal/agent/dialogue/commit.go:87)、Responses committer (internal/agent/responses/persistence.go:18) | 多处准备、投影失败都是 defer 发日志后返回，和保存层存在相同问题。 |
| Responses 请求归档：native_call.go (internal/agent/responses/native_call.go:135)、compact.go (internal/agent/responses/compact.go:185) | 请求已有错误时仍执行归档；归档又失败，会先记日志，再用归档错误覆盖原错误。虽然没有 Join，但依然把两个阶段的错误处理纠缠在一起。 |
| 压缩：Chat (internal/agent/chat/compressor.go:63)、Responses (internal/agent/responses/compact.go:118) | 压缩失败自己记日志、发通知，然后返回；自动压缩入口或 /compact 命令入口又会报告。这里存在明确的重复通知路径。 |
| 命名 fallback：title.go (internal/session/naming/title.go:27) | 首选模型失败后继续 fallback，却没有在决定继续时独立记录该失败；fallback 成功会丢掉前次失败，fallback 失败则合并返回。 |
| 工具执行：runner.go (internal/tool/execution/runner.go:78) | 将工具执行错误、输出发送错误、完成 Hook 错误合并到同一个结果；保存失败后又继续收集错误并 Join。普通 Hook 故障还会阻止该工具执行。 |
| 命令入口：command_executor.go (internal/agent/input/command_executor.go:111) | 命令失败先发通知，再将命令错误与通知发送错误合并返回。通知失败不该叠加成业务错误。 |
| 入站媒体关联：inbound_media.go (internal/agent/input/inbound_media.go:101) | 每项失败都记日志、继续处理，最后又把这些错误合并返回，导致已经处理过的失败再次上抛。 |
| 发送回执缓存：dispatch/media.go (internal/platform/dispatch/media.go:121) | 逐项缓存失败后继续，最后合并返回给上一层记录。应当在决定继续的位置分别记录，不收集错误树。 |
| CLI 多连接发送：remote_server.go (internal/platform/cli/remote_server.go:287) | 单连接写失败先记日志再返回，批量发送层继续尝试，全部失败时再合并返回。单连接和批量层的报告责任没有分清。 |
| 重试通知投递：signals.go (internal/modelmgr/signals.go:48) | 通知投递失败已经记日志，仍返回给信号系统；这是不会中止模型请求的旁路失败，应该处理完就结束。 |

还有三个跨层问题：

1. 最终错误没有唯一报告入口。
   Input、Execution、Dialogue 都散布着 ReportFailure 后返回的代码；另一些分支只有返回。平台又通常忽略 handler 返回值，所以当前既有重复报告，也有缺少通知的路径。
2. 统一报告日志依赖下层先记录详情。
   ReportFailure (internal/agent/share/logging.go:28) 的日志只写 dialogue terminated，原错误只放进通知。迁移到上层统一报告时，这里必须记录完整错误，否则删掉下层日志就会丢失原因。
3. 模型调用事实与错误报告没有分开。
   CallProcessor (internal/agent/dialogue/model_call.go:69) 在 Retry 判断是否继续之前，就发布带错误的调用记录；最终失败又触发错误 Hook，再向上返回。用量、调用结果这些事实可以记录，但“这次失败继续重试”与“整个任务终止”的报告必须由作出决定的层负责。


## 不一定实现

- [ ] CLI TUI 支持 Markdown 渲染。
- [ ] CLI 用户名和助手显示名可配置。
- [ ] CLI 支持本地图片输入。
- [ ] 周期 Cron 支持过期时间。


## 暂缓

- MCP 接入：暂不考虑，使用 Skill。
- 子 Agent：暂不考虑，信息交流会带来噪声或损失。
