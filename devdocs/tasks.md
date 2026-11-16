
- [ ] 前端显示正在调用工具名字和参数，结束后直接原地改写结果，保留名字和参数，内容和之前一样。需要同步改后端api，平台消息如[tool]xxx统一成control操作，raw提供原始内容，消息平台裁剪为文本，参考command_result_dispatch.go逻辑，给一个共享函数裁剪，也可直接拿raw blocks
- [ ] 加入phase压缩中
- [ ] notify，调用工具私聊场景应该发出正在调用xx。建议逻辑链路：工具调用时发送notify通知（带有参数是否同时llm在说话），发给Delivery，来判断当前消息平台是群聊私聊，群聊就不管，私聊且llm没有说话时，就显示[tool]正在调用xxx之类的，普通消息平台还顺便截断。而如果是web端，就转为api发往（看看是http还是ws），而且是完整的内容，带有工具名字、参数


## 业务线

把以下包和5条业务线一样并列
- [ ] control
- [ ] storage
- [ ] logging
- [ ] session 

## 代码精简

目标：精简所有过度防御、无用（或者多余）变量、重复逻辑、过度抽象
构造的无意义、强行找茬测试均删
防御代码：只对竞态、io、网络等不可控地方进行防御，内部代码逻辑都假定不可能出错，真出错就交给报错方便排查
每次看一个包时，列举所有if err之类的判断
生命周期：启动失败直接报错退出；全部启动成功后才负责关闭
- [ ] dialogue包
- [ ] platform包
- [ ] tool包
- [ ] hook 包
- [ ] background包
- [ ] completion包
- [ ] fileops包
- [ ] workspace包
- [x] llm包
- [ ] media包
- [ ] storage包
- [ ] session包
- [x] command包
- [ ] control包
- [ ] delivery包

## Python 代码执行与工具编排

- [ ] 提供单一 Python 代码执行工具，LLM 通过工具 schema 提交代码、运行模式和临时依赖：
  - `code`：必填，Python 源码；可在代码中直接以异步函数调用当前上下文可用的 ElBot 工具，例如 `a = await foo(i=i)`。
  - `mode`：可选，`system` 或 `uv`；省略时按 Workspace 自动选择，有 uv 项目用项目环境，否则用系统 Python。
  - `packages`：可选字符串数组，本次临时使用的第三方包；仅 uv 模式支持，不修改项目依赖文件。
- 支持循环、结果处理及 `asyncio.gather` 并行调用；工具调用由 Python runner 通过 IPC 回到当前 ElBot Tool Runtime 执行，不静态解析代码猜测调用。
- 按需启动系统 Python，每次执行结束后退出，不常驻 Python worker；同一段代码中的多次工具调用共用该进程。
- 执行目录固定为当前 Workspace。uv 项目模式使用项目环境；无项目而显式指定 `uv` 时使用临时环境；`system` 模式使用系统 Python。
- 临时依赖的解析或下载需明确告知并受控。
- Python 执行风险按 Shell 最高风险处理；嵌套 ElBot 工具调用仍须经过既有 Tool Runtime 权限、确认与审计。

## 不一定实现

- [ ] CLI TUI 支持 Markdown 渲染。
- [ ] CLI 用户名和助手显示名可配置。
- [ ] CLI 支持本地图片输入。
- [ ] 周期 Cron 支持过期时间。


## 暂缓

- MCP 接入：暂不考虑，使用 Skill。
- 子 Agent：暂不考虑，信息交流会带来噪声或损失。
