# Hook

Hook 在 ElBot 的关键边界运行：它可以匹配或修改事件、追加输出意图，或把事件交给外部进程。Hook 不直接调用平台发送接口；输出由 ElBot 的 Output Manager 统一落到平台。


## 先选类型

| 需求 | 选择 |
| --- | --- |
| 匹配事件后改文本、追加输出或调用已注册工具 | 规则 Hook |
| 每次事件独立交给外部程序，处理完即退出 | 规则 Hook 的 `exec` action |
| 进程常驻、维护状态、等待后续输入、主动调用 Host 工具 | 持久 Hook |

完整天气助手示例：TODO。

## 配置位置

配置目录的 `plugins/hooks.toml` 是规则入口。它可以直接包含 `[[rules]]`，也可以引用插件目录：

```toml
[[plugins]]
name = "weather"
# 默认读取 plugins/weather/hook.toml
```

`[[plugins]]` 字段：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `name` | 是 | 插件标识和默认目录名；持久 Hook 同时把它作为 worker ID。持久 Hook ID 只能使用小写字母、数字、`-`、`_`；规则插件也建议遵守此格式。 |
| `enabled` | 否 | 是否加载，默认 `true`。修改根配置后需要管理员执行全局 `/hooks reload`。 |
| `path` | 否 | 相对 `plugins/` 的配置路径，默认 `<name>/hook.toml`；不能是绝对路径或逃出 `plugins/`。 |

插件源码、`hook.toml` 和插件私有状态文件放在 `plugins/<id>/`。ElBot 额外创建 `plugins/_shared/` 供所有 Hook 共享文件；它不是插件目录，也不会被扫描。

根 `hooks.toml` 的解析失败会使本次规则配置加载失败；单个被引用插件配置有误时，该插件会被跳过，其他插件照常加载。TOML 使用严格字段校验，未知字段会报错。规则空 `name` 当前会自动命名为 `rule.<序号>`，重复名称会自动追加序号；不要依赖这种兼容行为。

## 规则 Hook

普通规则使用 `[[rules]]`。规则按 `on`、匹配条件、角色和优先级筛选后，依次执行 actions。

### 最小示例

```toml
[[rules]]
name = "notify_qqonebot_connected"
on = "platform.connected"
if = "platform.name"
op = "fullmatch"
value = "qqonebot"
action = "send"
kind = "text"
text = "ElBot 已连接 QQ OneBot。"
target.superadmins = true
```

### 公共字段、顺序与控制

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `name` | 否 | 规则显示名；建议显式填写。 |
| `description` | 否 | `/hooks` 列表和详情使用的说明。 |
| `on` | 是 | Hook 点，见下表。 |
| `priority` | 否 | 数字越小越先执行；默认 `1000`，`0` 也按默认值处理。相同 priority 按加载顺序稳定执行：根规则、`[[plugins]]` 声明顺序、各插件内规则顺序。 |
| `enabled` | 否 | 是否加载，默认 `true`。 |
| `require_wakeup` | 否 | 默认 `true`；主要用于 `platform.message.received`，设为 `false` 可观察未唤起的群消息。 |
| `consume` | 否 | 在 `platform.message.received` 中为 `true` 时，发送当前 outputs 后不再进入命令或主 LLM。 |
| `stop_propagation` | 否 | 为 `true` 时停止当前 Hook 点之后的规则；不停止 Agent 主流程。 |

`require_wakeup = false` 只允许 Hook 观察未唤起消息，不会让主 LLM 自动处理它们。

### Hook 点

| Hook 点 | 时机 | 主要 payload | 可编辑字段 | outputs 会被发送 |
| --- | --- | --- | --- | --- |
| `platform.connected` | 平台连接完成 | `platform` | 无 | 是 |
| `platform.message.received` | 收到用户消息，命令和 LLM 前 | `platform`、`actor`、`message` | `message.text` | 是 |
| `agent.input.prepared` | 输入写入会话前 | `session`、`message` | `message.text` | 否 |
| `llm.turn.prepared` | 整轮 LLM 调用前 | `session`、`llm.messages/tools/provider/model` | `llm.latest_user_text` | 否 |
| `llm.request.prepared` | 每次实际模型请求前 | 同上 | `llm.latest_user_text` | 否 |
| `llm.response.received` | 模型响应完成 | `llm.text/source_text/tool_calls/usage` | `llm.text` | 是 |
| `tool.call.prepared` | 工具调用前 | `session`、`tool` | `tool.arguments` | 否 |
| `tool.call.completed` | 工具调用后 | `session`、`tool.result/error/risk` | `tool.result` | 否 |
| `agent.output.prepared` | 每段 assistant 输出发送前 | `message` | assistant `message.text` | 否 |
| `agent.turn.output.prepared` | 整轮 assistant 最终输出发送前 | `message` | assistant `message.text` | 否 |
| `platform.message.sent` | assistant chat 或 preview 成功发送后的通知 | `message` | 无 | 否 |
| `error.occurred` | Hook/Agent 阶段报错时的通知 | 原事件上下文、`error.message` | 无 | 否 |

`send` action 虽可在所有 Hook 点创建 output intent，但只有上表“outputs 会被发送”的点会消费它。`timing = "after_assistant"` 当前也只有 `llm.response.received` 的输出会真正延后；其他点的输出仍按当前流程立即发送。

最终 assistant 文本通常先经过 `agent.turn.output.prepared`，再经过 `agent.output.prepared`。不要在两个点配置同一 append/prepend 规则，否则文本会被处理两次。

### 匹配条件与角色

条件可写单条件简写，或写 AND 数组，二者不能混用：

```toml
if = "message.intent_text"
op = "fullmatch"
value = "天气"
```

```toml
# 数组中每项都必须匹配。
match = [
  { field = "message.intent_text", op = "startswith", value = "查询天气" },
  { field = "actor.group_role", op = "fullmatch", value = "admin" },
]
```

`always = true` 表示无条件匹配，不能与 `if/op/value` 或 `match` 混用。

| `op` | 字段要求 | 行为 |
| --- | --- | --- |
| `always` | 不写 `field/value` | 始终匹配。 |
| `exists` | 只写 `field` | 字段的文本值非空才匹配；空字符串和当前事件未提供的字段都不匹配。 |
| `contains` | `field/value` | 子串匹配。 |
| `fullmatch` | `field/value` | 整个字段与 value 严格相等。 |
| `startswith` / `endswith` | `field/value` | 前缀或后缀匹配。 |
| `regex` | `field/value` | Go RE2 正则的第一个子串命中。 |

`regex` 不是全字段匹配；需要全字段约束时在正则中使用 `^`、`$`。捕获中 `group.0` 是完整命中，后续分组从 `group.1` 开始。多个 regex 条件的序号从 `0` 开始，只计算 regex 条件本身。

角色字段：

```toml
roles = ["user", "admin"]
actor_roles = ["superadmin"]
group_roles = ["owner", "admin", "member", "unknown"]
```

同一数组内是 OR；`roles`、`actor_roles`、`group_roles` 之间是 AND。`roles` 可混写 Actor role（`user`、`superadmin`）和群角色。

### action 写法与执行顺序

每条规则选择一种形式：

```toml
# 单个内联 action。
action = "append"
field = "llm.text"
text = "\n处理完成。"
```

```toml
# 内联 actions 数组，按数组顺序执行。
actions = [
  { type = "tool", name = "search", tool = "web_search", arguments = "{\"query\":\"{{message.text}}\"}" },
  { type = "send", text = "{{actions.search.result}}" },
]
```

```toml
# TOML array table，按声明顺序执行。
[[rules.actions]]
name = "search"
type = "tool"
tool = "web_search"
arguments = "{\"query\":\"{{message.text}}\"}"

[[rules.actions]]
type = "send"
text = "{{actions.search.result}}"
```

`action = "..."` 不能和 `actions` 混用。`actions = [...]` 与 `[[rules.actions]]` 都使用 `type` 和 `arguments`；`name` 在两种 actions 形式中都可用。内联 `action = "tool"` 的参数字段同样是 `arguments`，不是 `args`。

| 类型 | 主要字段 | 作用 |
| --- | --- | --- |
| `prepend` / `append` | `field`、`text` | 在可编辑文本前后添加内容。 |
| `replace` | `field`、`pattern`、`replace`、`all` | 正则替换；`all = false` 时只替换第一个命中。 |
| `delete` | `field`、`pattern` 或 `text` | 删除全部正则命中。 |
| `send` | 单输出字段或 `outputs` | 追加输出意图。 |
| `tool` | `tool`、`arguments`、可选 `name` | 调用已注册工具。 |
| `exec` | `command`、`cwd`、`timeout_seconds`、`field`、`timing`、可选 `name` | 启动一次性 `hook.v2` 进程。 |

命名的 `tool` / `exec` action 可供后续 action 使用：

```text
{{actions.<name>.result}}
{{actions.<name>.error}}
```

未命名 `tool` 使用工具名作为结果名；未命名 `exec` 使用 `exec`。同一规则内多个未命名 exec 会覆盖同一结果，需模板引用时应显式命名。

### 文本 action

可编辑字段严格取决于 Hook 点，见上方 Hook 点表。其他字段即使可匹配或可作为模板，也不能编辑。

对 `message.text` 和 `llm.latest_user_text`：

- `prepend` / `append` 修改首个或最后一个 text segment；没有 text segment 时新增 text segment；图片和文件保留。
- `replace` / `delete` 只处理 text segment；图片和文件保留。
- exec response 的 `message.text` 覆写会重建为纯 text segment，原图片和文件 segment 会丢失。

## 输出与路径

输出格式分三种；字段不能混写。下面先给容易混淆的字段对照，后续章节再分别展开。

| 概念 | 规则 TOML 单个 `send` | 规则 TOML `outputs` | 一次性 exec `result.outputs` | 持久 Hook JSON `outputs` |
| --- | --- | --- | --- | --- |
| 输出种类 | `kind` | `kind` | `kind` | `kind` |
| `at` 的用户 ID | `text` | `user_id`，为空时回退 `text` | `user_id`，为空时回退 `text` | `text` |
| `reply` 的消息 ID | `path` | `message_id`，为空时回退 `path` | `message_id`，为空时回退 `path` | `reply_to_message_id` |
| 媒体来源 | `path` | `path`、`url`、`base64` | `path`、`url`、`base64` | `path`、`url`；不支持 `base64` |
| target | action 的 `target.*`，snake_case | action 的 `target.*`，snake_case | 不支持；沿用当前事件 | output 自身 `target`，PascalCase |
| timing | action 的 `timing` | action 的 `timing` | exec action 的 `timing` | 不支持 |

### 规则 TOML 的单个 `send`

```toml
action = "send"
kind = "reply"
# reply 的被引用消息 ID 放 path。
path = "{{platform.reply_to_message_id}}"
text = "已收到。"
timing = "immediate"
target.group_id = "123456"
```

单输出支持 `kind`、`text`、`path`、`timing`、`target`。`kind` 默认 `text`：

- `text`：`text` 为文本；
- `image` / `file`：`path` 是媒体来源，`text` 是 fallback；
- `emoticon`：`text` 是表情名，`path` 可指定本地媒体；
- `at`：`text` 是平台原始用户 ID；
- `reply`：`path` 是平台消息 ID，`text` 是回复正文。

### TOML `outputs` 与一次性 exec 的 `outputs`

规则的多段输出使用 `outputs`，一次性 exec response 的 `outputs` 使用相同 segment 格式：

```toml
[[rules.actions]]
type = "send"
timing = "after_assistant"
outputs = [
  { kind = "text", text = "检测到关键词" },
  { kind = "image", path = "assets/alert.png", name = "alert.png" },
  { kind = "at", user_id = "10001" },
  { kind = "reply", message_id = "456", text = "引用回复" },
]
```

| 字段 | 说明 |
| --- | --- |
| `kind` | `text`、`image`、`file`、`emoticon`、`at`、`reply`；默认 `text`。 |
| `text` | 文本、媒体 fallback 或特殊输出的备用字段。 |
| `url` / `path` / `base64` | 图片、文件、表情媒体来源。相对 `path` 以声明该规则的插件目录解析。`base64` 解码后最大 10 MiB。 |
| `name` / `mime_type` | 媒体显示名和 MIME 类型。 |
| `user_id` | `kind = "at"` 的平台原始用户 ID；为空时回退 `text`。 |
| `message_id` | `kind = "reply"` 的平台消息 ID；为空时回退 `path`。 |

一次性 exec 的大媒体应写到插件目录或 `_shared/`，然后返回 `path` 或 `url`；不要把大数据塞进 JSON Pipe。单个 stdout 协议帧最大 16 MiB。

### target 与 timing

规则 TOML 使用 snake_case target：

```toml
target.platform = "qqonebot"
target.scope_id = "group:123456"
target.private_user_id = "10001"
target.group_id = "123456"
target.superadmins = true
```

通常省略 target 以沿用当前事件。`timing` 为 `immediate`（默认）或 `after_assistant`；其实际生效范围见 Hook 点表。

## 一次性 exec 与 hook.v2

### 共同协议原则

一次性 exec 与持久 Hook 都使用 `hook.v2` JSON Lines：stdin 和 stdout 的每行都是一个 JSON 帧；stdout 只能写协议帧，日志写 stderr。Host 发起的 request ID 必须以 `host:` 开头，Hook 发起的 request ID 必须以 `plugin:` 开头，response 必须复用对应 request ID。

两种模式都先处理 `system.init`，成功后再处理 `event.handle`，也都允许 Hook 向 Host 发 request；可用 method 则随运行模式不同。一次性 exec 只处理一个 `event.handle` 后退出；持久 Hook 会持续处理多个事件，并额外接收 `system.shutdown`、可能收到 `event.cancel`。

`exec` 每次匹配启动一个子进程：Host 发送 `system.init`，收到成功 response 后发送 `event.handle`，读取其 response 后等待进程退出。

```toml
[[rules]]
name = "extract_answer"
on = "llm.response.received"
always = true

[[rules.actions]]
name = "extract"
type = "exec"
command = "uv run extract.py"
field = "llm.text"
timeout_seconds = 30
```

### command、cwd 与 timeout

`exec.command` 不隐式经过 shell。它支持单引号、双引号及引号/反斜杠的有限转义；shell 语法应显式调用 `bash -lc`、`sh -c` 或平台解释器。

- `timeout_seconds` 为 `0` 或省略时不额外设置 exec 超时；不能为负数。
- 插件内规则的 `cwd` 默认插件目录；显式 cwd 必须是插件目录内的相对路径。
- 根 `plugins/hooks.toml` 的规则默认在 `plugins/` 目录执行；其 cwd 可为相对路径或绝对路径。
- `exec` 返回的相对媒体 `path` 始终相对声明规则的插件目录；根规则则相对 `plugins/`。

### Host 请求与 Hook response

每个 stdin/stdout 帧是一行 JSON。Host ID 使用 `host:*`，Hook 主动 request ID 使用 `plugin:*`；response 必须复用原 request ID。stdout 只能写协议帧，日志写 stderr。

Host 首先发送：

```json
{"type":"request","id":"host:init","method":"system.init","params":{"version":"hook.v2","runtime":{"plugin_name":"demo","plugin_dir":"C:/elbot/plugins/demo","config_path":"C:/elbot/plugins/demo/hook.toml","rule_name":"demo_rule","cwd":"C:/elbot/plugins/demo"}}}
```

Hook 成功响应：

```json
{"type":"response","id":"host:init","ok":true,"result":{}}
```

随后 Host 发送：

```json
{"type":"request","id":"host:event","method":"event.handle","params":{"event":{"point":"platform.message.received","platform":{"name":"qqonebot","scope_id":"group:123","message_id":"456","reply_to_message_id":"789"},"actor":{"id":"qqonebot:10001"},"message":{"role":"user","segments":[{"type":"text","text":"撤回"}]}},"match":{},"runtime":{"plugin_name":"demo","plugin_dir":"C:/elbot/plugins/demo","config_path":"C:/elbot/plugins/demo/hook.toml","rule_name":"demo_rule","cwd":"C:/elbot/plugins/demo"}}}
```

`event.handle` 成功 response：

```json
{
  "type": "response",
  "id": "host:event",
  "ok": true,
  "result": {
    "status": "completed",
    "matched": true,
    "result": "optional template result",
    "message": {"text": "optional replacement text"},
    "outputs": [{"kind": "text", "text": "hello"}],
    "consume": false,
    "stop_propagation": false
  }
}
```

失败时只返回外层错误：

```json
{"type":"response","id":"host:event","ok":false,"error":"missing platform.reply_to_message_id"}
```

`status` 只能省略或为 `completed`。配置了 action `field` 时，存在的 `message.text` 覆写该字段；空字符串合法，省略 `message` 或 `message.text` 才表示不改。

`matched = false` 表示外部脚本判定规则未命中：该规则本次已产生的文本修改和 outputs 会回滚，剩余 actions 不执行，也不视为错误。

### exec Hook 主动请求 Host

一次性 exec 可向 Host 发 `plugin:*` request：

| method | 作用 |
| --- | --- |
| `platform.call` | 调用当前事件平台的原生 API。 |
| `output.send` | 立即经 Output Manager 发送 outputs。 |
| `message.get_reply` | 读取当前被引用的平台消息 ID。 |
| `message.get` | 保留接口；当前总是返回 `available: false`。 |
| `hook.log` | 写入 Host 日志。 |

`platform.call` 示例：

```json
{
  "type": "request",
  "id": "plugin:recall-1",
  "method": "platform.call",
  "params": {
    "platform": "qqonebot",
    "api": "delete_msg",
    "params": {"message_id": "789"}
  }
}
```

`platform` 可省略，默认当前事件的 `platform.name`；若填写，只能等于当前平台。Host 成功后以相同 ID 回写：

```json
{"type":"response","id":"plugin:recall-1","ok":true,"result":{}}
```

平台 adapter 返回 JSON 时，Host 原样作为 JSON 值放入 `result`；否则作为字符串。Hook 应先依据外层 `ok` 判断通用成功。

## 持久 Hook

持久 Hook 仍是 Hook，不存在独立插件系统或 `/plugins` 命令。它由插件自己的 `hook.toml` 声明，并在 ElBot 启动或 `/hooks reload` 后自动启动。

```toml
[plugin]
name = "weather"
description = "有状态天气助手"
blocked_platform = []
blocked_group = ["qqonebot:123456"]
blocked_id = ["qqonebot:10001"]

[plugin.runtime]
stateful = true
command = "uv run weather_hook.py"
cwd = "."
startup_timeout_seconds = 10
shutdown_timeout_seconds = 5
event_timeout_seconds = 30
max_wait_seconds = 900

[plugin.runtime.restart]
strategy = "on_failure"
initial_delay_seconds = 1
max_delay_seconds = 30

# 无需调用 Host 工具时，整个 [plugin.runtime.tools] 可省略。
[plugin.runtime.tools]
allow = ["web_search"]
background_allow = []

[[rules]]
name = "weather_entry"
on = "platform.message.received"
require_wakeup = false
if = "message.intent_text"
op = "contains"
value = "天气"
# 持久 Hook 的 rules 仅是 event.handle 触发器；不设置 action/actions/consume/stop_propagation。
```

### 插件与 runtime 配置

`[plugin]`：

| 字段 | 说明 |
| --- | --- |
| `name` | 自述名称；建议与根 `[[plugins]].name` 一致。不一致时以根 ID 为准并记录警告。 |
| `description` | `/hooks`、`system.init` 与状态列表使用的简介。 |
| `blocked_platform` | 完全不分发指定平台的事件，例如 `telegram`。 |
| `blocked_group` | 完全不分发指定群的事件，格式 `<platform>:<平台原始群 ID>`。 |
| `blocked_id` | 完全不分发指定用户事件，格式 `<platform>:<平台原始用户 ID>`。 |

阻断三项任一命中即不调用该插件规则或 `event.handle`；其他插件不受影响。匹配精确、不支持通配符；`blocked_group` 同时识别 `group` 和 `supergroup` scope。

`[plugin.runtime]` 以下字段必填：

| 字段 | 说明 |
| --- | --- |
| `stateful` | 必须为 `true`。 |
| `command` | 启动命令，不经过 shell，当前按空白简单拆分，**不解析引号**；参数含空格时请通过脚本包装或避免该路径。 |
| `cwd` | 相对插件目录，不能为绝对路径或逃出插件目录。通常为 `.`。 |
| `startup_timeout_seconds` | 等待 `system.init` 成功 response 的最长秒数，必须大于 `0`。 |
| `shutdown_timeout_seconds` | 等待 `system.shutdown` 和退出的最长秒数，必须大于 `0`。 |
| `event_timeout_seconds` | 单次 `event.handle` 的最长秒数，也限制此次工具上下文，必须大于 `0`。 |
| `max_wait_seconds` | waiting 会话可声明的最长剩余时间，必须大于 `0`。 |

`[plugin.runtime.restart]` 以下字段必填：

| 字段 | 说明 |
| --- | --- |
| `strategy` | `never`、`on_failure`、`always`。当前 `on_failure` 和 `always` 都会在非手动退出后自动重启。 |
| `initial_delay_seconds` | 首次自动重启前的等待秒数，必须大于 `0`。 |
| `max_delay_seconds` | 指数退避上限，必须不小于初始延迟。 |

`[plugin.runtime.tools]` 可省略，省略等价于空 allowlist：

| 字段 | 说明 |
| --- | --- |
| `allow` | 前台 `tool.call` 可使用的工具名；Host 在 `system.init.params.tools` 下发对应 schema。 |
| `background_allow` | 后台可使用的工具名，必须同时出现在 `allow`。 |

持久 trigger rule 复用规则 Hook 的匹配、角色、priority 和 `require_wakeup` 语义。`action` 或 `actions` 会使配置校验失败；`consume`、`stop_propagation` 即使写入也不会由 trigger rule 应用，应改由插件的 `event.handle` response 返回。

worker 状态为 `starting`、`ready`、`running`、`degraded`、`stopping`、`stopped` 或 `failed`。停止时 Host 先请求 `system.shutdown`，超过关闭超时才强制结束进程。

### 协议与输出

持久进程收到的 `system.init.params`：

```json
{
  "version": "hook.v2",
  "hook": {
    "id": "weather",
    "description": "有状态天气助手",
    "plugin_dir": "C:/elbot/plugins/weather",
    "cwd": "C:/elbot/plugins/weather",
    "shared_dir": "C:/elbot/plugins/_shared"
  },
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "web_search",
        "description": "...",
        "parameters": {"type": "object"}
      }
    }
  ]
}
```

`event.handle.params`：

| 字段 | 说明 |
| --- | --- |
| `event` | 当前 Hook 事件。 |
| `match` | trigger rule 的 regex 捕获；无捕获时为空对象。 |
| `continuation` | `true` 表示 waiting 路由捕获的后续消息。 |
| `tool_context` | 本次前台工具调用令牌，必须原样用于前台 `tool.call`。 |

#### 读取用户输入

持久 Hook 不应假定 `event.message.segments` 已去掉唤醒前缀：它保留平台归一化后的原始 segments；`event.message.intent_text` 才是 Host 去除唤醒前缀后计算出的用户意图文本。处理文本指令时，应优先读取 `intent_text`，为空再拼接 text segments：

```python
def message_text(event):
    message = event.get("message") or {}
    intent = str(message.get("intent_text") or "").strip()
    if intent:
        return intent
    return "".join(
        str(segment.get("text") or "")
        for segment in message.get("segments") or []
        if segment.get("type") == "text"
    ).strip()
```

对于由 `platform.message.received` trigger rule 以 `message.intent_text` 匹配后启动的首次调用，插件通常无需再次校验同一唤醒前缀或指令文本；Host 已完成这层路由。waiting continuation 也只会捕获同一 platform、scope 和 actor 的消息。业务参数校验、外部服务授权和插件自身权限判断仍由插件负责。

`event.handle` 成功 response 的 `result`：

| 字段 | 说明 |
| --- | --- |
| `status` | 省略或 `completed` 表示结束；`waiting` 表示继续捕获后续消息。 |
| `conversation_id` | `waiting` 时必填的非空不透明 ID。 |
| `expires_at` | `waiting` 时必填的 RFC 3339 时间，必须晚于当前时间且不超过 `max_wait_seconds`。 |
| `outputs` | 交给 Output Manager 的输出数组。 |
| `consume` | 对首次 trigger 消息为 `true` 时阻止命令和主 LLM。 |
| `stop_propagation` | 为 `true` 时停止当前 Hook 点后续规则。 |

持久 Hook JSON output 使用另一套字段：

```json
{
  "kind": "reply",
  "reply_to_message_id": "456",
  "text": "引用回复",
  "target": {"Platform":"qqonebot","ScopeID":"group:123"}
}
```

| 字段 | 说明 |
| --- | --- |
| `kind` | `text`、`image`、`file`、`emoticon`、`at`、`reply`。 |
| `text` | text 内容、媒体 fallback；`at` 的平台原始用户 ID 也放在此处。 |
| `name` / `alt_text` | 显示名和媒体不可发送时的替代文本。 |
| `url` / `path` / `mime_type` | 媒体来源；相对 `path` 以插件目录解析。持久 Hook output 不支持 `base64`。 |
| `reply_to_message_id` | `reply` 的被引用平台消息 ID。 |
| `target` | 可选显式目标，使用 PascalCase：`Platform`、`ScopeID`、`PrivateUserID`、`GroupID`、`Superadmins`。 |

省略 target 时沿用当前事件目标。规则 TOML target 使用 snake_case，不要混写。

### waiting、并发与取消

同一 `platform + scope_id + actor.id` 在同一个 worker 内串行，不同路由可并行。waiting 租约也以该三元组为 key：

- 正常串行分发中，首个返回 `waiting` 的持久 Hook 持有该路由，后续持久 trigger rule 不会再分发该路由。实现每条路由只存一份租约；若并发事件使多个 Hook 同时竞争，最后写入者覆盖此前租约。因此多个持久 Hook 可能匹配同一路由时，应使用 `priority`、更严格条件或 `stop_propagation` 明确归属。
- waiting 存在时，普通 `platform.message.received` 规则仍会先运行；持久 trigger rule 不会再次触发。
- 常规规则完成且消息未被 `consume` 后，Host 将后续消息以 `continuation = true` 路由给持有租约的 Hook。
- continuation 被路由后，消息不会再进入 slash 命令或主 LLM，即使 response 中 `consume` 为 `false`。
- continuation 返回 `completed` 或省略 `status` 后，Host 立即释放租约；再次返回 `waiting` 则以新 ID、过期时间续期。
- 阻断策略命中、worker 退出、停止或重载会清理该插件租约。

用户发送精确的 `/cancel` 时，Host 取消该路由当前执行或 waiting 会话，并向插件写入无 response 的通知帧：

```json
{"type":"event","method":"event.cancel","params":{"conversation_id":"weather-42"}}
```

`conversation_id` 仅在存在时提供。`/cancel` 不停止进程和插件内存；使用 `/hooks stop <id>` 才停止 worker。

### 工具与共享状态

持久 Hook 自行维护 LLM loop、业务状态和工具使用决策。Host 对 `tool.call` 只校验 allowlist、前后台能力、context 归属、次数、超时、取消和发送 target；**不执行 ElBot 的风险分级、权限策略或交互确认**。插件必须自行承担工具授权和风险控制。

前台调用：

```json
{"type":"request","id":"plugin:tool-1","method":"tool.call","params":{"name":"web_search","arguments":{"query":"上海天气"},"tool_context":"ctx:..."}}
```

`arguments` 为 JSON 值，无参数时可为 `{}`。单次 `event.handle` 的 `tool_context` 最多可调用 32 次，且在 `event_timeout_seconds` 到期、取消或 worker 重载后失效。

后台调用必须位于 `background_allow`：有 Host 签发上下文时把它放入 `origin`；没有有效 `origin` 时必须给显式 `target`。后台无 origin 时调用主体为 `hook:<id>`，插件应特别谨慎处理授权。

成功 response 的 `result`：

```json
{"content":"...","segments":[],"warnings":[],"receipts":[{"PlatformMessageIDs":["123"]}]}
```

`content` 是工具文本结果，`segments` 为 `{type,text,url,mime_type,name}` 数组，`warnings` 为字符串数组，`receipts` 是工具 outputs 经 Output Manager 发送后的平台消息 ID。失败统一使用外层 `ok=false,error`。Hook 工具调用不会写入 Agent Session。

除 `_shared/` 文件目录外，所有持久 Hook 还共享一个进程内 JSON KV：

| method | `params` | 成功 `result` |
| --- | --- | --- |
| `shared.get` | `{"key":"weather/cache"}` | `{"found":true,"value":<任意 JSON>}`；不存在时 `found=false`。 |
| `shared.set` | `{"key":"weather/cache","value":<任意 JSON>}` | `{"ok":true}` |
| `shared.delete` | `{"key":"weather/cache"}` | `{"deleted":true/false}` |
| `shared.list` | `{"prefix":"weather/"}` | `{"keys":["weather/cache",...]}`，按字典序；空 prefix 列出全部。 |
| `shared.compare_and_swap` | `{"key":"weather/cache","expected":<旧 JSON>,"value":<新 JSON>}` | `{"swapped":true/false}` |

写入 shared KV 的 key 必须为 `<namespace>/<key>`。value 必须是合法 JSON，压缩后单值最大 1 MiB，共享区最大 32 MiB。`compare_and_swap` 是原子操作，按压缩后的 JSON 内容比较；省略 `expected` 表示仅当 key 不存在时写入，显式 `expected: null` 表示当前值必须是 JSON `null`。共享内存跨 Hook 重启和 `/hooks reload` 保留，在 ElBot 重启后清空；需要持久化时由 Hook 写入自己的目录或 `_shared/`。

### 插件自身重载

持久 Hook 修改自身 `hook.toml` 后可请求重载自己：

```json
{"type":"request","id":"plugin:reload-1","method":"hooks.reload","params":{}}
```

不要在唯一读取 stdin 的协议循环中同步等待该 response；否则 Host 回写无人读取而死锁。应让持续读循环按 request ID 分发 response，或从事件工作线程发 request。

Host 会先完整读取并校验候选配置。失败时返回 `ok=false`，旧规则和进程不变；成功时先返回：

```json
{"type":"response","id":"plugin:reload-1","ok":true,"result":{"scheduled":true}}
```

实际替换等当前 `event.handle` 结束后发生：仅替换调用插件的规则和 worker，清理该插件 waiting 路由与工具上下文，不重启其他插件。调用者身份由进程确定，不能重载别的插件；`starting`、`stopping`、`stopped` 时不能请求重载。根 `plugins/hooks.toml` 的插件引用、`enabled`、`path` 以及插件增删仍需管理员执行全局 `/hooks reload`。

## 管理命令

`/hooks` 是超级管理员管理入口：

```text
/hooks
/hooks <rule-name-or-stateful-id>
/hooks start <id>
/hooks stop <id>
/hooks restart <id>
/hooks reload
```

`/hooks` 列出规则和持久 Hook；`/hooks <名称>` 查看规则详情或持久 worker 状态。`start`、`stop`、`restart` 只接受持久 Hook ID。全局 `reload` 重读规则与持久运行配置，并停止、替换或启动相应 worker；配置不做动态 schema patch，受影响 worker 会重启。

## Event 与模板字段

`event` 顶层包含 `id`、`point`、`time`、`metadata`、`control`、`platform`、`actor`、`session`、`request`、`message`、`llm`、`tool`；可能还有 `outputs`、`error`。与当前 Hook 点无关的字段会是零值、空对象或被省略，插件必须按可空值处理。

| 对象 | 字段 |
| --- | --- |
| `control` | `consume`、`stop_propagation`。 |
| `platform` | `name`、`scope_id`、`user_id`、`conversation_id`、`message_id`、`reply_to_message_id`。 |
| `actor` | `id`（`<platform>:<id>`）、`user_id`、`role`、`group_role`、`display_name`。 |
| `session` | `id`、`mode`、`title`、`status`；部分 Hook 点只提供 `id`。 |
| `request` | `id`、`kind`、`session_id`、`phase`；当前可能都为空。 |
| `error` | `message`；仅错误事件可靠。 |

`message`：

| 字段 | 说明 |
| --- | --- |
| `id`、`role` | 消息 ID 和 `user` / `assistant` / `system` / `tool` 角色。 |
| `platform_text` | 平台原始文本，可能为空。 |
| `intent_text` | 去除唤醒前缀后的用户意图；消息输入 Hook 通常应优先使用它。 |
| `segments` | `{type,text,url,mime_type,name}` 数组，type 为 `text` / `image` / `file`。 |
| `reply` | 被引用消息：`message_id`、`sender_id`、`text`、`display_text`、`segments`。 |
| `messages` | LLM 消息数组；非 LLM 上下文通常为空。 |

`llm` 包含 `provider`、`model`、`messages`、`tools`、`usage`、`source_text`、`text`、`tool_calls`、`elapsed_ms`。其中嵌套 `messages`、`tool_calls`、`usage` 沿用 Go 导出的 JSON 名：`Role/Segments/Name/ToolCallID/ToolCalls`、`ID/Name/Arguments`、`PromptTokens/CompletionTokens/TotalTokens/CacheHitTokens`。

`tool` 包含 `id`、`name`、`arguments`（JSON 字符串）、`risk`、`result`、`error`。prepared 阶段主要提供 `id/name/arguments`，completed 阶段才有 `risk/result/error`。

可用于 `if`、`match.field` 和模板的文本字段：

```text
platform.name / platform.scope_id / platform.user_id / platform.conversation_id / platform.message_id / platform.reply_to_message_id
actor.id / actor.user_id / actor.role / actor.group_role / actor.display_name
session.id / session.mode / session.title / session.status
request.id / request.kind / request.session_id / request.phase
message.id / message.text / message.display_text / message.platform_text / message.intent_text / message.role
message.reply.message_id / message.reply.sender_id / message.reply.text / message.reply.display_text
llm.text / llm.source_text / llm.latest_user_text / llm.latest_user_display_text / llm.provider / llm.model
tool.name / tool.arguments / tool.result / tool.risk
error.message
```

其中 `message.text`、`message.display_text`、`llm.latest_user_text`、`llm.latest_user_display_text` 是匹配/模板时计算的虚拟文本字段，不直接存在于 event JSON；插件进程应从 `segments` 或 `messages` 读取原始结构。

模板写法为 `{{...}}`，例如 `{{platform.name}}`、`{{message.text}}`、`{{llm.text}}`。regex 捕获可使用：

```text
{{match.regex.<regex条件序号>.group.<分组序号>}}
{{match.regex.<regex条件序号>.<命名分组>}}
```

前序 tool / exec action 的结果可使用 `{{actions.<name>.result}}`、`{{actions.<name>.error}}`。已知字段在当前事件没有值时渲染为空字符串；未知模板、未知 action 名或不存在的捕获路径会原样保留，便于把它们作为普通文本使用。
