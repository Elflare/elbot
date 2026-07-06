# Hook

Hook Layer 用于在关键流程前后扩展行为，例如：

- Agent 输入处理。
- LLM 请求准备。
- LLM 响应处理。
- 平台发送前后。
- 平台连接事件。

Hook 可以修改消息、追加输出意图、调用低风险工具或注入常驻记忆。规则 Hook 还可以执行本地脚本。

重要约定：Hook 不替代 Security Layer，安全判定仍以 Security Layer 为准。Hook 和插件不直接发平台消息，应返回输出意图，由 Agent 统一交给 Output Manager 发送。

## 内置 Hook

ElBot 随程序注册两类内置 Hook：

| 类型 | 说明 |
| --- | --- |
| 规则 Hook | 从 `plugins/hooks.toml` 加载声明式规则，支持条件匹配、文本操作、输出发送、工具调用和脚本执行。 |
| 常驻记忆 Hook | 每轮注入当前 platform + actor 的常驻记忆和临时用户名。 |

表情 Hook 已从内嵌插件改为规则 Hook 示例，见本文档[表情提取示例](#表情提取示例)。

## 规则 Hook 配置

规则 Hook 主配置固定在配置目录的 `plugins/hooks.toml`。插件规则通过主配置里的 `[[plugins]]` 引用，默认读取 `plugins/<plugin-name>/hook.toml`。

```toml
[[plugins]]
name = "demo"
enabled = true
# path = "demo/hook.toml" # 可选；必须是相对 plugins/ 的路径
```

插件配置文件可以包含 `[plugin]` 元信息和自己的 `[[rules]]`。插件规则里的本地相对路径和相对 `cwd` 都基于该插件配置所在目录解析。

### 规则结构

```toml
[[rules]]
name = "stable_debug_name"          # 必填，用于日志和审计
on = "hook.point"                   # 必填，Hook 点
enabled = true                      # 可选，默认 true
priority = 1000                     # 可选，值越小越先执行
require_wakeup = true               # 可选，默认 true；false 表示未唤起消息也可触发
```

### 唤起要求

`platform.message.received` 规则默认只处理已唤起消息，兼容旧行为。已唤起通常包括私聊、slash 命令、命中唤起词、at 机器人、回复机器人消息。

如果希望 Hook 被动监听普通群消息，设置：

```toml
require_wakeup = false
```

示例：

```toml
[[rules]]
name = "passive_cat_ping"
on = "platform.message.received"
require_wakeup = false
match = [{ field = "message.text", op = "contains", value = "猫" }]
action = "send"
text = "检测到猫。"
```

处理顺序是：先运行 `platform.message.received` Hook；发送 Hook outputs；如果规则设置 `consume = true`，本次消息到此结束，不进入命令或 LLM；如果未 consume，则只有已唤起消息才继续进入命令或 LLM。也就是说，`require_wakeup = false` 可以让 Hook 看见未唤起消息，但不会自动让 LLM 处理群里所有消息。

### 条件匹配

单条件：

```toml
if = "message.text"
op = "contains"
value = "hello"
```

无条件：

```toml
always = true
```

多条件（AND）：

```toml
match = [
  { field = "platform.name", op = "fullmatch", value = "qqonebot" },
  { field = "message.text", op = "contains", value = "猫" },
]
```

`always` 不能与 `if/op/value` 或 `match` 组合使用；`if/op/value` 不能与 `match` 组合使用。

### 匹配操作

| op | 说明 |
| --- | --- |
| `always` | 无条件匹配，不能设 field 或 value |
| `exists` | 字段非空 |
| `contains` | 字段包含 value |
| `fullmatch` | 字段完全等于 value |
| `startswith` | 字段以 value 开头 |
| `endswith` | 字段以 value 结尾 |
| `regex` | 正则匹配，捕获组可通过模板变量引用 |

### 可匹配字段

```
platform.name / scope_id / user_id / conversation_id / message_id / reply_to_message_id
actor.id / user_id / role / group_role / display_name
session.id / mode / status
request.id / kind / phase
message.text / content_text / raw_text / role
message.reply.message_id / sender_id / text / content_text
llm.text / raw_text / latest_user_text / latest_user_content_text / provider / model
tool.name / arguments / result / risk
error.message
```

### Hook 点

```
platform.connected
platform.message.received
agent.input.prepared
llm.turn.prepared
llm.request.prepared
llm.response.received
tool.call.prepared
tool.call.completed
agent.output.prepared
agent.turn.output.prepared
platform.message.sent
error.occurred
```

### 可编辑字段

不同 Hook 点允许编辑的字段不同：

| Hook 点 | 可编辑字段 |
| --- | --- |
| `platform.message.received` / `agent.input.prepared` | `message.text` |
| `llm.turn.prepared` / `llm.request.prepared` | `llm.latest_user_text` |
| `llm.response.received` | `llm.text` |
| `tool.call.prepared` | `tool.arguments` |
| `tool.call.completed` | `tool.result` |
| `agent.output.prepared` / `agent.turn.output.prepared` / `platform.message.sent` | assistant `message.text` |

`llm.raw_text` 可以用于条件匹配，但不能被编辑。

### Action 类型

每条规则可以写单个 `action` 或多个 `actions`（按顺序执行）。

#### 文本操作

```toml
# 单 action
action = "replace"
field = "message.text"
pattern = "猫"
replace = "狗"
all = true                 # 可选，默认只替换第一处

# 多 actions
actions = [
  { type = "replace", field = "message.text", pattern = "猫", replace = "狗", all = true },
  { type = "append", field = "message.text", text = "!" },
]
```

文本操作类型：`prepend`、`append`、`replace`、`delete`。`delete` 等同于 `replace` 为空字符串。

文本操作会保留消息中的图片、文件等多模态 segment 位置，只修改 text segment。

#### send

`send` action 产生输出意图，由 Output Manager 统一发送到平台。

单输出（向后兼容）：

```toml
action = "send"
kind = "text"              # text/image/file/emoticon/at，默认 text
text = "检测到关键词"
timing = "after_assistant" # 可选，默认 immediate
```

多段输出（outputs）：

```toml
actions = [
  { type = "send", timing = "after_assistant", outputs = [
    { kind = "text", text = "检测到关键词" },
    { kind = "image", path = "alert.png" },
    { kind = "image", url = "https://example.com/chart.png", name = "chart.png" },
    { kind = "image", base64 = "iVBORw0KGgo..." },
    { kind = "emoticon", name = "微笑", path = "emoticons/微笑/01.png" },
    { kind = "file", path = "report.pdf", name = "报告.pdf" },
  ] },
]
```

output segment 格式与 [Elvena 协议](elnis-usage.md#segments多模态消息段)统一，额外支持本地 `path`、`base64` 和 `emoticon` 类型：

| 字段 | 说明 |
| --- | --- |
| `kind` | `text` / `image` / `file` / `emoticon` / `at` / `reply`，默认 `text` |
| `text` | 文本内容（text 类型必填，其他类型可选作为附加文本；at 类型未设置 `user_id` 时作为用户 ID） |
| `url` | HTTP/HTTPS URL（image/file） |
| `path` | 本地文件路径（image/file/emoticon）；reply 类型未设置 `message_id` 时作为被回复消息 ID；普通路径按平台默认方式处理，`base64://`、`file://`、`http://`、`https://` 前缀表示调用方已指定媒体源，会按平台能力直接处理 |
| `base64` | base64 编码数据（image/file） |
| `name` | 文件名或表情名 |
| `mime_type` | MIME 类型提示 |
| `user_id` | at 目标用户 ID |
| `message_id` | reply 目标消息 ID |

`target` 和 `timing` 从 action 继承到所有 segment。

#### tool

调用已注册工具，结果存到 `{{actions.<name>.result}}` 供后续 action 使用。

```toml
actions = [
  { name = "search", type = "tool", tool = "web_search", arguments = '{"query":"ElBot"}' },
  { type = "append", field = "llm.latest_user_text", text = "\n\nHook 工具结果：{{actions.search.result}}" },
]
```

工具调用受 Security Policy 约束：风险等级必须在当前 Actor 允许范围内，需要交互确认的高风险工具会被拒绝。

#### exec

执行本地脚本。主配置里的脚本默认以 `plugins/` 为工作目录；插件配置里的脚本默认以插件配置所在目录为工作目录，且相对 `cwd` 不能逃出插件目录。

`command` 会按空白拆分为可执行程序和参数后直接执行，不会隐式套 `sh -c`。例如 `uv run script.py` 会直接执行 `uv`，`bash ./script.sh` 会直接执行 `bash`；需要管道、重定向、`&&` 等 shell 语法时，请显式写 `bash -lc "..."`、`sh -c "..."` 或平台对应解释器。

exec 使用 `hook.v1` 行协议：ElBot 启动脚本后，会先向 stdin 写一行 init JSON；脚本向 stdout 每行写一个 JSON frame，最后必须写 `done` 或 `error` frame。stderr 不作为协议数据；脚本成功时只进入日志，脚本失败、超时、崩溃或协议错误时会把 stderr 尾部并入 Hook 失败通知。

脚本只应读取 stdin 第一行作为 init frame，不要 read-all、read-to-end、`fread` 到 EOF 或循环读到 EOF；stdin 后续还用于 `request`/`response` frame。脚本写出合法 `done` 或 `error` frame 后应以 0 退出；非 0 exit code 会被视为 exec 进程失败。

```toml
actions = [
  { name = "script", type = "exec", command = "uv run script.py", timeout_seconds = 30 },
]
```

### exec hook.v1 协议

init frame 由 ElBot 写入脚本 stdin：

```json
{
  "type": "init",
  "version": "hook.v1",
  "event": {},
  "match": {},
  "runtime": {
    "plugin_name": "demo",
    "plugin_dir": ".../plugins/demo",
    "config_path": ".../plugins/demo/hook.toml",
    "rule_name": "demo_rule",
    "cwd": ".../plugins/demo"
  }
}
```

init frame 字段：

| 字段 | 说明 |
| --- | --- |
| `type` | 固定为 `init` |
| `version` | 固定为 `hook.v1` |
| `event` | 当前 Hook 事件上下文；字段按 Hook 点填充，不相关字段为空、零值或省略 |
| `match` | 当前规则匹配上下文；正则命中时包含 `regex` 数组 |
| `runtime` | 本次 exec 运行上下文 |

`event` 字段：

| 字段 | 说明 |
| --- | --- |
| `id` | Hook 事件 ID |
| `point` | Hook 点，例如 `platform.message.received`、`llm.response.received` |
| `time` | 事件时间，RFC3339 格式 |
| `metadata` | 预留/扩展元数据对象 |
| `control.consume` | 是否阻止后续 slash 命令和 LLM 处理 |
| `control.stop_propagation` | 是否阻止同 Hook 点后续规则执行 |
| `platform.name` | 平台名，例如 `qqonebot`、`telegram`、`cli` |
| `platform.scope_id` | 平台会话范围 ID；由平台适配器生成，通常带范围前缀，例如 `group:<id>`、`private:<id>` |
| `platform.user_id` | 当前平台发送者用户 ID |
| `platform.conversation_id` | 平台会话 ID；平台未提供时为空 |
| `platform.message_id` | 当前入站平台消息 ID |
| `platform.reply_to_message_id` | 当前消息引用/回复的目标平台消息 ID |
| `actor.id` | ElBot 内部 Actor ID，通常由平台名和用户 ID 组合得到 |
| `actor.user_id` | Actor 对应的平台用户 ID |
| `actor.role` | ElBot 内部角色：`superadmin` 或 `user` |
| `actor.group_role` | 群身份：`owner`、`admin`、`member`、`unknown` |
| `actor.display_name` | 显示名 |
| `session.id` | 当前 Session ID |
| `session.mode` | 当前 Session 模式 |
| `session.title` | 当前 Session 标题 |
| `session.status` | 当前 Session 状态 |
| `request.id` | 当前 Request ID |
| `request.kind` | Request 类型 |
| `request.session_id` | Request 关联的 Session ID |
| `request.phase` | Request/Turn 阶段 |
| `message.id` | 当前消息 ID；未设置时为空 |
| `message.role` | 消息角色，例如 `user`、`assistant` |
| `message.raw_text` | 平台原始当前消息文本，不包含引用 fallback 展开内容 |
| `message.segments` | 当前消息片段数组；脚本读取用户文本应优先从这里聚合 `type=text` 的片段。`platform.message.received` 中这里表示当前入站消息，不包含被引用消息文本 |
| `message.messages` | 相关 LLM 消息数组；仅部分 Hook 点填充 |
| `message.reply.message_id` | 当前消息引用/回复的目标平台消息 ID |
| `message.reply.sender_id` | 被引用消息发送者 ID；平台未提供时为空 |
| `message.reply.text` | 被引用消息的纯文本内容；没有文本时为空 |
| `message.reply.content_text` | 被引用消息的可读内容文本，可能包含图片/文件占位 |
| `llm.provider` | LLM provider 名 |
| `llm.model` | LLM model 名 |
| `llm.messages` | 本次 LLM 消息数组 |
| `llm.tools` | 本次请求可用工具 schema 数组 |
| `llm.usage` | LLM usage 统计；未提供时为空 |
| `llm.raw_text` | LLM 原始响应文本；可匹配，不可编辑 |
| `llm.text` | 当前可见/可编辑的 LLM 文本 |
| `llm.tool_calls` | LLM 返回的工具调用数组 |
| `llm.elapsed_ms` | LLM 调用耗时毫秒数 |
| `tool.id` | 工具调用 ID |
| `tool.name` | 工具名 |
| `tool.arguments` | 工具参数 JSON 字符串 |
| `tool.risk` | 工具风险等级 |
| `tool.result` | 工具结果文本 |
| `tool.error` | 工具错误；通常仅用于错误上下文 |
| `outputs` | 当前事件已累计的输出意图数组 |
| `error.message` | 当前错误文本；仅错误 Hook 相关 |

`message.segments`、`llm.messages[].segments`、stdout `output` frame 的 `outputs` 字段、`request output.send` 的 `params.outputs`，以及 TOML send action 的 `outputs` 使用的常见片段字段：

| 字段 | 说明 |
| --- | --- |
| `type` / `kind` | 片段类型；入站消息常见为 `text`、`image`、`file`，输出还支持 `emoticon`、`at`、`reply` |
| `text` | 文本内容或附加文本 |
| `url` | HTTP/HTTPS 资源 URL |
| `path` | 本地资源路径；输出时相对 `plugins/` 或插件目录解析 |
| `base64` | base64 编码数据；仅输出片段使用 |
| `name` | 文件名或表情名 |
| `mime_type` | MIME 类型提示 |
| `user_id` | `at` 输出的目标用户 ID |
| `message_id` | `reply` 输出的目标平台消息 ID |

注意：`message.text`、`message.content_text`、`llm.latest_user_text` 等是规则匹配和模板变量里的派生字段，不是 init JSON 里的同名字段。exec 脚本需要从 `event.message.segments`、`event.message.raw_text`、`event.message.reply` 或 `event.llm.messages[].segments` 读取原始数据。

`match.regex[]` 字段：

| 字段 | 说明 |
| --- | --- |
| `field` | 正则匹配的字段名 |
| `value` | 正则表达式 |
| `text` | 被匹配的文本 |
| `groups` | 捕获组数组；`groups[0]` 为完整匹配 |
| `named` | 命名捕获组对象 |
| `start` / `end` | 命中范围 |

`runtime` 字段：

| 字段 | 说明 |
| --- | --- |
| `plugin_name` | 插件名；主 `plugins/hooks.toml` 中的规则为空 |
| `plugin_dir` | 插件目录；主规则为空 |
| `config_path` | 当前规则配置文件路径 |
| `rule_name` | 当前规则最终名称 |
| `cwd` | exec 进程工作目录 |

脚本可写的 stdout frame：

| type | 说明 |
| --- | --- |
| `output` | 排入本次 Hook 的输出意图；frame 必须包含 `outputs` 字段，值是 output segment 对象数组 |
| `request` | 调用 ElBot 能力；带 `id` 时 ElBot 会向 stdin 写 `response` frame |
| `done` | 正常结束；可带 `matched=false` 表示本规则不生效并回滚此前 action 效果 |
| `error` | 失败结束，`error` 或 `message` 字段作为错误文本 |

stdout frame 结构示例：

```json
{"type":"output","outputs":[{"kind":"text","text":"内容"}]}
{"type":"output","outputs":[{"kind":"image","path":"images/a.png","text":"附加说明"}]}
{"type":"request","id":"send-1","method":"output.send","params":{"outputs":[{"kind":"text","text":"立即发送"}]}}
{"type":"done","result":"ok","message":{"text":"改写后的文本"}}
{"type":"error","error":"失败原因"}
```

`output` frame 只使用 `outputs` 字段；不要写 `{"type":"output","output":{...}}` 或 `{"type":"output","segments":[...]}`。需要多段输出时，把多个 output segment 放在同一个 `outputs` 数组里；也可以写多行 `output` frame。TOML send action 同样使用 `outputs = [...]`。

`output` frame 字段：

| 字段 | 说明 |
| --- | --- |
| `type` | 固定为 `output` |
| `id` | 可选；设置后 ElBot 会回 `response`，成功时 `result.queued=true` |
| `outputs` | 必填，output segment 对象数组 |

`request` frame 字段：

| 字段 | 说明 |
| --- | --- |
| `type` | 固定为 `request` |
| `id` | 可选但强烈建议设置；设置后 ElBot 会向 stdin 写 `response` frame |
| `method` | 请求方法，见下方 method 表 |
| `params` | 请求参数对象；结构随 method 变化 |

不带 `id` 的 `request` 不会收到 `response`；但如果请求失败，当前 exec action 仍会失败并触发 Hook 失败通知。

`done` 可选字段：

| 字段 | 说明 |
| --- | --- |
| `matched` | 默认 `true`；为 `false` 时整条规则视为未命中，后续 action 不再执行 |
| `result` | 保存到 `{{actions.<name>.result}}` |
| `error` | 保存到 `{{actions.<name>.error}}` |
| `message.text` | 覆写 action 的 `field`；未设置 `field` 时覆写 `message.text` |
| `consume` | 设置本事件的 consume 控制位 |
| `stop_propagation` | 设置本事件的 stop_propagation 控制位 |

`error` frame 字段：

| 字段 | 说明 |
| --- | --- |
| `type` | 固定为 `error` |
| `error` / `message` | 失败文本；`error` 优先 |

支持的 request method：

| method | params | 说明 |
| --- | --- | --- |
| `platform.call` | `platform`、`api`、`params` | 调用当前平台原始 API；不能跨平台调用 |
| `output.send` | `outputs` | 立即发送 output segment 数组并返回 receipt；需要 app 层 sender 可用 |
| `message.get_reply` | 无 | 返回当前消息引用/回复的目标消息 ID |
| `message.get` | 预留 | 当前返回 `available=false` |
| `hook.log` | 任意 JSON | 写入 Hook 插件日志 |

ElBot 写回 stdin 的 `response` frame 字段：

| 字段 | 说明 |
| --- | --- |
| `type` | 固定为 `response` |
| `id` | 对应 request/output frame 的 `id` |
| `ok` | `true` 表示成功，`false` 表示失败 |
| `result` | 成功结果；仅 `ok=true` 时存在 |
| `error` | 失败文本；仅 `ok=false` 时存在 |

`platform.call`、`output.send` 等 request 失败时，脚本会先收到 `ok=false` 的 response；随后当前 exec action 也会失败并触发 Hook 失败通知。

最小 Python 示例：

```python
#!/usr/bin/env python3
import json
import sys

init = json.loads(sys.stdin.readline())
event = init["event"]
segments = event.get("message", {}).get("segments", [])
text = "".join(seg.get("text", "") for seg in segments if seg.get("type") == "text")

print(json.dumps({
    "type": "output",
    "outputs": [{"kind": "text", "text": "收到：" + text}],
}, ensure_ascii=False), flush=True)
print(json.dumps({"type": "done", "result": "ok"}, ensure_ascii=False), flush=True)
```

```toml
[[rules]]
name = "emoticon_extract"
on = "llm.response.received"
if = "llm.text"
op = "regex"
value = "\\[\\[[^\\[\\]]+\\]\\]"
actions = [
  { name = "extract", type = "exec", command = "uv run emoticon_extract.py", field = "llm.text", timing = "after_assistant" },
]
```

### 模板变量

文本字段和 exec command 支持 `{{...}}` 模板渲染：

```
{{platform.name}}          {{platform.scope_id}}      {{platform.user_id}}
{{platform.message_id}}    {{platform.reply_to_message_id}}
{{actor.id}}               {{actor.user_id}}          {{actor.role}}
{{message.text}}           {{message.content_text}}   {{message.raw_text}}
{{message.reply.message_id}} {{message.reply.text}}    {{message.reply.content_text}}
{{llm.text}}               {{llm.raw_text}}           {{llm.latest_user_text}}
{{tool.arguments}}         {{tool.result}}
{{actions.<name>.result}}  {{actions.<name>.error}}
```

regex 匹配的捕获组可通过 `{{match.regex.0.group.1}}` 或命名组 `{{match.regex.0.<name>}}` 引用。

### 角色分区

规则可用 `roles`、`actor_roles`、`group_roles` 做权限分区：

- `superadmin` / `user`：ElBot 内部安全角色。
- `owner` / `admin` / `member`：平台群身份，由平台适配器映射。

`roles` 同时匹配内部角色和群身份；`actor_roles` 只匹配内部角色；`group_roles` 只匹配群身份。

```toml
[[rules]]
name = "admin_only_rule"
on = "platform.message.received"
roles = ["admin"]
always = true
action = "send"
text = "仅管理员可见"
```

### 控制字段

```toml
consume = true              # 阻止后续 slash 命令和 LLM 处理
stop_propagation = true      # 阻止同一 Hook 点后续规则继续执行
```

`consume = true` 通常用于 `platform.message.received` Hook：发送输出后阻止后续命令和 LLM 处理，让 Hook 完全接管消息。

## Hook exec 平台调用

规则 Hook 的 `exec` action 可以通过 `request` frame 调用当前平台原始 API。平台调用会进入审计日志，并且只能调用当前事件所属平台，不能从一个平台 Hook 跨到另一个平台。

```json
{"type":"request","id":"call-1","method":"platform.call","params":{"api":"delete_msg","params":{"message_id":"123"}}}
```

ElBot 会向脚本 stdin 写入 response frame：

```json
{"type":"response","id":"call-1","ok":true,"result":{}}
```

## 撤回引用消息示例

以下示例用于超级管理员回复某条平台消息并发送“撤回”时，通过 Elvena v3 `calls` 调用平台 API 撤回被引用的消息。

前提：平台适配器需要支持原始 API 调用，且本次消息必须带引用/回复关系。Hook 里可通过 `message.reply.message_id` 或 `platform.reply_to_message_id` 读取被引用消息的平台消息 ID，通过 `platform.message_id` 读取当前触发 Hook 的消息 ID。`platform.message.received` 中的 `message.text` 只表示当前用户发送的文本；被引用内容在 `message.reply.*` 中，不会污染 `message.text`。

Telegram 使用 Bot API `deleteMessage`，机器人必须有删除目标消息的权限，且目标消息不能超过平台允许的删除时限。

### hooks.toml

```toml
[[rules]]
name = "recall_quoted_message"
on = "platform.message.received"
require_wakeup = false
roles = ["superadmin"]
match = [
  { field = "message.text", op = "fullmatch", value = "撤回" },
  { field = "message.reply.message_id", op = "exists" },
]

consume = true

[[rules.actions]]
type = "exec"
name = "recall"
command = "uv run recall_quoted_message.py"
timeout_seconds = 10
```

### recall_quoted_message.py

脚本从 stdin 读取 init frame，向 stdout 写 `platform.call` request frame，再读取 response frame，最后写 done frame。

```python
#!/usr/bin/env python3
import json
import sys


def numeric_scope_id(scope_id):
    if ":" not in scope_id:
        return scope_id
    return scope_id.split(":", 1)[1]


def main():
    init = json.loads(sys.stdin.readline())
    event = init.get("event", {})
    platform = event.get("platform", {})
    platform_name = platform.get("name", "")
    scope_id = platform.get("scope_id", "")
    reply_id = platform.get("reply_to_message_id", "")

    if not reply_id:
        raise SystemExit("missing platform.reply_to_message_id")

    scope_target_id = numeric_scope_id(scope_id)
    if platform_name == "qqonebot":
        api = "delete_msg"
        params = {"message_id": reply_id}
    elif platform_name == "telegram":
        api = "deleteMessage"
        params = {"chat_id": scope_target_id, "message_id": int(reply_id)}
    else:
        raise SystemExit(f"unsupported platform: {platform_name}")

    req = {
        "type": "request",
        "id": "recall",
        "method": "platform.call",
        "params": {
            "platform": platform_name,
            "api": api,
            "params": params,
        },
    }
    print(json.dumps(req, ensure_ascii=False), flush=True)
    resp = json.loads(sys.stdin.readline())
    if not resp.get("ok"):
        print(json.dumps({
            "type": "error",
            "error": resp.get("error", "platform.call failed"),
        }, ensure_ascii=False), flush=True)
        return

    done = {"type": "done", "result": "recalled", "consume": True}
    if platform_name == "telegram":
        done["message"] = {"text": "已撤回引用消息"}
    print(json.dumps(done, ensure_ascii=False), flush=True)


if __name__ == "__main__":
    main()
```

## 表情提取示例

以下示例用规则 Hook + exec 脚本实现表情提取功能，替代旧的内嵌表情 Hook。

### hooks.toml

```toml
[[rules]]
name = "emoticon_extract"
on = "llm.response.received"
priority = 1000
if = "llm.text"
op = "regex"
value = "\\[\\[[^\\[\\]]+\\]\\]"
actions = [
  { name = "extract", type = "exec", command = "uv run emoticon_extract.py", field = "llm.text", timing = "after_assistant" },
]
```

### emoticon_extract.py

脚本从 stdin 读取 init frame，提取 `[[token]]`，检查 `emoticons/<token>/` 目录是否有图片，有则输出 emoticon frame 并从文本中移除 token，无则保留原样。

```python
#!/usr/bin/env python3
import json
import os
import random
import re
import sys

TOKEN_RE = re.compile(r"\[\[([^\[\]]+)\]\]")
EMOTICON_DIR = "emoticons"
IMAGE_EXTS = {".jpg", ".jpeg", ".png", ".gif", ".webp"}

def main():
    init = json.loads(sys.stdin.readline())
    text = init.get("event", {}).get("llm", {}).get("text", "")
    outputs = []

    def replace(match):
        name = match.group(1).strip()
        d = os.path.join(EMOTICON_DIR, name)
        if not os.path.isdir(d):
            return match.group(0)
        images = [f for f in os.listdir(d)
                  if os.path.splitext(f)[1].lower() in IMAGE_EXTS]
        if not images:
            return match.group(0)
        path = os.path.join(d, random.choice(images))
        outputs.append({"kind": "emoticon", "name": name, "path": path})
        return ""

    cleaned = TOKEN_RE.sub(replace, text).strip()
    if outputs:
        print(json.dumps({
            "type": "output",
            "outputs": outputs,
        }, ensure_ascii=False), flush=True)
    print(json.dumps({
        "type": "done",
        "message": {"text": cleaned},
        "result": str(len(outputs)),
    }, ensure_ascii=False), flush=True)

if __name__ == "__main__":
    main()
```

### 配置说明

- `root_dir`（旧内嵌插件的配置项）不再需要；脚本里直接写相对 `plugins/` 的目录路径。
- `timing` 控制表情发送时机：`immediate`（默认）在 LLM 文本输出前发送，`after_assistant` 在 assistant 回复后发送。
- `field = "llm.text"` 让脚本返回的 `text` 覆写 LLM 响应文本，移除已被提取的 token。
- 不设 `field` 时脚本只产出 outputs，不修改原文（适合"检测到内容就发通知但不改原文"的场景）。

### write_elbot_hook Skill 示例

下面内容在ElBot第一次运行时自动生成在 `skills/go/write_elbot_hook/SKILL.elyph`，用于让 LLM 按需求生成规则 Hook 配置和可选 exec 脚本。

```text
#skill write_elbot_hook - 根据需求编写 ElBot 规则 Hook
<- $requirement:str!
<- $script_name:str?
-> $script_content:str
?if(windows) {
  $hook_config:str = %AppData%/ElBot/plugins/hooks.toml
}
?else {
  $hook_config:str = $XDG_CONFIG_HOME/elbot/plugins/hooks.toml
  ** 若 XDG_CONFIG_HOME 未设置，按 XDG 规范使用 $HOME/.config
}

step goal {
  ** 根据 $requirement 直接修改 $hook_config
  ** 输出可直接使用的 TOML
  ** 仅当必须使用 exec 时才输出完整 $script_content
  ** 配置示例可看自动生成的 hooks.toml 注释
  ** 完整说明和例子可看 https://github.com/Elflare/elbot/blob/main/docs/hooks.md
  ** 完成后提醒用户执行 /hooks reload
}

step files {
  ** 主 hooks.toml 只允许顶层 [[plugins]] 和 [[rules]]
  ** [[plugins]] 只允许 name,enabled,path
  ** [[plugins]].path 必须相对 plugins/
  ** 未设置 path 时默认读取 plugins/<name>/hook.toml
  ** 插件 hook.toml 只允许顶层 [plugin] 和 [[rules]]
  ** [plugin] 只允许 description
  ** 插件 hook.toml 不能再写 [[plugins]]
  ** 严格模式不允许未知字段、旧字段 stdout/stdin
  ** 同一 rule 内 actions=[...] 和 [[rules.actions]] 只能二选一
}

step rule_shape {
  ** [[rules]] 字段白名单：name,on,priority,enabled,require_wakeup,if,op,value,always,match,roles,actor_roles,group_roles,action,actions,field,text,pattern,replace,kind,path,timing,tool,arguments,command,cwd,timeout_seconds,all,target,outputs,consume,stop_propagation
  ** Hook 点白名单：platform.connected,platform.message.received,agent.input.prepared,llm.turn.prepared,llm.request.prepared,llm.response.received,tool.call.prepared,tool.call.completed,agent.output.prepared,agent.turn.output.prepared,platform.message.sent,error.occurred
  ** 匹配写法三选一：always=true；if/op/value；match=[{field,op,value},...]
  ** op 白名单：exists,contains,fullmatch,startswith,endswith,regex
  ** roles 同时匹配内部角色和群身份
  ** actor_roles 只匹配 superadmin/user
  ** group_roles 只匹配 owner/admin/member
}

step fields {
  ** 匹配字段白名单：platform.name,scope_id,user_id,conversation_id,message_id,reply_to_message_id,actor.id,actor.user_id,actor.role,actor.group_role,actor.display_name,session.id,session.mode,session.status,request.id,request.kind,request.phase,message.text,message.content_text,message.raw_text,message.role,message.reply.message_id,message.reply.sender_id,message.reply.text,message.reply.content_text,llm.text,llm.raw_text,llm.latest_user_text,llm.latest_user_content_text,llm.provider,llm.model,tool.name,tool.arguments,tool.result,tool.risk,error.message
  ** 可编辑 field 映射：on=platform.message.received/agent.input.prepared 时 field="message.text"；on=llm.turn.prepared/llm.request.prepared 时 field="llm.latest_user_text"；on=llm.response.received 时 field="llm.text"；on=tool.call.prepared 时 field="tool.arguments"；on=tool.call.completed 时 field="tool.result"；on=agent.output.prepared/agent.turn.output.prepared/platform.message.sent 时 field="message.text"；llm.raw_text 只可匹配不可作为 field
}

step actions {
  ** action 类型白名单：prepend,append,replace,delete,send,tool,exec
  ** 单 action 可用 action="send" 加平铺字段
  ** 多 action 用 actions=[{type="..."},...] 或 [[rules.actions]]
  ** replace/delete 使用 field,pattern,replace,all
  ** tool 使用 tool 和 arguments
  ** tool 结果模板是 {{actions.<name>.result}}
  ** send 产生输出意图，由 Output Manager 发送
  ** send 字段：kind,text,timing,target,outputs
  ** timing 默认 immediate，可用 after_assistant
  ** target 字段：target.platform,target.scope_id,target.private_user_id,target.group_id,target.superadmins
  ** 不写 target 时发送到当前上下文
  ** output segment 字段：kind,text,url,path,base64,name,mime_type,user_id,message_id
  ** kind 白名单：text,image,file,emoticon,at,reply
  ** outputs 必须是 segment 数组
}

step templates: ** 模板变量白名单：{{platform.name}},{{platform.scope_id}},{{platform.user_id}},{{platform.message_id}},{{platform.reply_to_message_id}},{{actor.id}},{{actor.user_id}},{{actor.role}},{{message.text}},{{message.content_text}},{{message.raw_text}},{{message.reply.message_id}},{{message.reply.sender_id}},{{message.reply.text}},{{message.reply.content_text}},{{llm.text}},{{llm.raw_text}},{{llm.latest_user_text}},{{tool.arguments}},{{tool.result}},{{error.message}},{{actions.<name>.result}},{{actions.<name>.error}},{{match.regex.0.group.1}},{{match.regex.0.<name>}}

step exec_protocol {
  ** exec 字段：command,cwd,timeout_seconds,field
  ** command 按空白拆分后直接 exec，不自动套 shell
  ** 需要管道、重定向、&& 时显式使用平台 shell
  ** 工作目录默认是 plugins/ 或插件目录
  ** 插件规则的相对 cwd 不能逃出插件目录
  ** hook.v1 是行协议
  ** ElBot 向 stdin 写一行 init JSON
  ** 脚本必须只读取 stdin 第一行作为 init frame
  ** 脚本不能 read_all、read_to_end、fread 到 EOF、循环读到 EOF
  ** 脚本向 stdout 每行写一个 JSON frame
  ** stdout 只能写 JSON frame
  ** 日志和 debug 写 stderr 或文件
  ** stderr 成功时只进日志
  ** exec 失败/崩溃/超时/协议错误时 stderr 尾部会进入 Hook 失败通知
  ** 最后必须写 done 或 error frame
  ** 写出合法 done/error frame 后进程应以 0 退出
  ** 非 0 exit code 会被视为 exec 进程失败
  ** output frame 必须是 {"type":"output","outputs":[...]}
  ** output frame 字段：type,id,outputs
  ** 禁止使用 output={...} 或 segments=[...]
  ** request frame 字段：type,id,method,params
  ** done.message.text 写回 action.field
  ** done.result 存入 {{actions.<name>.result}}
  ** done.error 存入 {{actions.<name>.error}}
  ** done.consume 设置事件 consume
  ** done.stop_propagation 设置事件 stop_propagation
  ** matched=false 会回滚本规则并跳过后续 action
  ** error frame 字段：type,error 或 type,message
  ** request frame 可调用 platform.call、output.send、message.get_reply、message.get、hook.log
  ** 脚本发 request frame 后再逐行读取 stdin 的 response frame
  ** response frame 字段：type,id,ok,result,error
  ** request 失败会收到 ok=false/error，且当前 exec action 失败
}

step exec_init {
  ** init 顶层字段：type,version,event,match,runtime
  ** init.event 字段：id,point,time,metadata,control,platform,actor,session,request,message,llm,tool,outputs,error
  ** init.event.control 字段：consume,stop_propagation
  ** init.event.platform 字段：name,scope_id,user_id,conversation_id,message_id,reply_to_message_id
  ** init.event.actor 字段：id,user_id,role,group_role,display_name
  ** init.event.session 字段：id,mode,title,status
  ** init.event.request 字段：id,kind,session_id,phase
  ** init.event.message 字段：id,role,raw_text,reply,segments,messages
  ** init.event.message.reply 字段：message_id,sender_id,text,content_text,segments
  ** init.event.message 没有 message.text/message.content_text；读取当前原始文本用 raw_text，读取引用用 reply
  ** 读用户文本时拼接 init.event.message.segments 中 type=text 的片段
  ** init.event.llm 字段：provider,model,messages,tools,usage,raw_text,text,tool_calls,elapsed_ms
  ** init.event.tool 字段：id,name,arguments,risk,result,error
  ** init.event.outputs 是已累计输出意图数组
  ** init.event.error.message 是错误文本
  ** regex 匹配结果在 init.match.regex[0].groups
  ** groups[0] 是完整匹配
  ** groups[1+] 是捕获组
  ** 命名捕获组在 init.match.regex[0].named
}

step exec_python_template {
  ** Python 读取 init：init=json.loads(sys.stdin.readline())
  ** Python 读取 regex groups：groups=init.get("match",{}).get("regex",[{}])[0].get("groups",[])
  ** Python 输出文本：print(json.dumps({"type":"output","outputs":[{"kind":"text","text":text}]},ensure_ascii=False),flush=True)
  ** Python 正常结束：print(json.dumps({"type":"done","result":"ok"},ensure_ascii=False),flush=True)
  ** Python 业务失败：print(json.dumps({"type":"error","error":"原因"},ensure_ascii=False),flush=True) 后正常 return
}

step decisions {
  ** 能用 replace、append、prepend、delete、send、tool 完成时不使用 exec
  ** 需要复杂解析、随机、文件、外部程序或平台 API 时使用 exec
  ** 拦截输入并阻止后续 LLM 时使用 on="platform.message.received" 且 consume=true
  ** 监听未唤起群消息时使用 on="platform.message.received" 且 require_wakeup=false
  ** 普通改写用户输入优先使用 agent.input.prepared
  ** 改写 LLM 回复优先使用 llm.response.received
  ** 只改最终发出的 assistant 文本优先使用 agent.turn.output.prepared
}

~ 使用未列出的 Hook 点、字段、action、segment 字段、request method 或模板变量
~ 修改当前 Hook 点不可编辑的字段
~ 让 Hook 或脚本绕过 Output Manager 直接发送平台消息
~ 把 exec 日志写到 stdout
~ 让 exec 脚本读取 stdin 到 EOF
~ 让 exec 脚本输出 done/error 后以非 0 退出
~ 用 output={...} 或 segments=[...] 代替 outputs=[...]
~ 编造配置字段或旧版 stdout/stdin 字段

```
