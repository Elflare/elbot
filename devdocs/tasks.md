## 当前任务：
总体目标与业务契约
根因修复 elbot 在“无当前会话”时额外发送字面量 "nil" 的问题；修在生产端和统一发送边界，不在 CLI、前端、QQ、OneBot 等消费端分别补丁。
统一业务结果与故障边界：
业务成功 / 拒绝 / 失败 -> operationresult.Result[T]
程序故障 / 内部 Bug / IO 故障 -> Go error
operationresult.Status 是 elbot 内部业务状态，不是 HTTP 状态或 wire code。业务拒绝不再通过 nil Result、Go error、sentinel 或旧错误分类猜测来表达。
业务边界只在成功时读取 Data；用户可见拒绝、失败由 Result.Message 表达。Go error 到最终发送边界时使用 err.Error()。
Result 只用于需要到达用户可见边界的业务链；纯内部协调不强制 Result 化。
Handler、Router、业务生产端使用 Result 值；平台/API 边界只按结构化 Result 展示或映射协议响应，不重新猜业务语义。
HTTP code、wire code 留在 API/management 适配层，不能塞进内部 Status。
修改与验证纪律
当前阶段只改业务代码；暂不写测试、不运行测试、不编译。
修改前说明范围；只做必要的手术式修改，不擅自扩大改造、不增加无契约依据的兜底。
Go 文件修改后运行 gofmt；最终业务迁移完成后再做静态核对。
注：若链路不涉及发往用户的通知或者消息，就没必要无脑全用operationresult，局部的普通error就够
一条链路一条链路做，每做完一条回查旧兼容层和 Go error / Result 分界。

以上已完成。现在开始review。看看是否符合架构原则、边界职责是否清晰，有没过度防御、有没旧逻辑代码或者强行兼容. 特别是过度防御：1. try :
xxx
except a:
xx
except b:
xx
...
以上是最蠢的，注意这种情况，可以直接改成：
Try：
xxx
except 大的：
return traceback.format_exc() #go类似方法可以直接把err内容返回，能让知道是什么错
2. 若链路不涉及发往用户的通知或者消息，就没必要无脑全用operationresult，局部的普通error就够
3. 逻辑上不会出错的禁止防御性编程。
review目标：1. 旧测试删或者改造。2. 新测试指测必要且复杂功能或者容易出现竞态问题的地方，简单、无意义测试都删。
3. 减少代码，越精简越好，用最少代码完成功能，只留必要防御，多余防御全删

- [ ] 前端显示正在调用工具名字和参数，结束后直接原地改写结果，保留名字和参数，内容和之前一样。需要同步改后端api，平台消息如[tool]xxx统一成control操作，raw提供原始内容，消息平台裁剪为文本，参考command_result_dispatch.go逻辑，给一个共享函数裁剪，也可直接拿raw blocks
- [ ] 加入phase压缩中
- [ ] notifyf，调用工具私聊场景应该发出正在调用xx。建议逻辑链路：工具调用时发送notify通知（带有参数是否同时llm在说话），发给Delivery，来判断当前消息平台是群聊私聊，群聊就不管，私聊且llm没有说话时，就显示[tool]正在调用xxx之类的，普通消息平台还顺便截断。而如果是web端，就转为api发往（看看是http还是ws），而且是完整的内容，带有工具名字、参数
## 不一定实现

- [ ] CLI TUI 支持 Markdown 渲染。
- [ ] CLI 用户名和助手显示名可配置。
- [ ] CLI 支持本地图片输入。
- [ ] 周期 Cron 支持过期时间。


## 暂缓

- MCP 接入：暂不考虑，使用 Skill。
- 子 Agent：暂不考虑，信息交流会带来噪声或损失。
