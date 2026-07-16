# Hook

Hook 在 ElBot 的关键边界匹配或修改事件、追加输出，或把事件交给外部程序。输出统一由 Output Manager 发送。

## 名词与选型

| 名称 | 配置与生命周期 | 适用场景 |
| --- | --- | --- |
| **规则 Hook** | TOML `[[rules]]`；匹配后执行 actions。 | 改文本、发送内容等简单逻辑。 |
| **一次性 exec Hook** | 规则中的 `type = "exec"`；每次命中启动一个外部程序，处理当前事件后退出。 | 独立脚本或无需保存进程状态的逻辑。 |
| **Transient Worker** | `[plugin.runtime] mode = "transient"`；命中 trigger rule 后启动，waiting 会话结束后退出。 | 短期有状态、多轮交互。 |
| **Persistent Worker** | `[plugin.runtime] mode = "persistent"`；随 ElBot 启动并常驻。 | 长期状态、后台任务、频繁事件。 |

Transient Worker 与 Persistent Worker 统称 **Worker Hook**；一次性 exec Hook 与 Worker Hook 统称 **进程 Hook**。`mode = "once"` 不创建 Worker，仍按规则 Hook 运行；只有规则配置了 `type = "exec"` 才会启动一次性 exec Hook。所有产生 outputs 的入口使用同一套 segment。

Hook 示例见 [Hook Showcase](https://github.com/Elfreese/elbot-showcase/blob/main/hooks/README.md)。

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
| `name` | 是 | 插件标识和默认目录名；Worker Hook 同时把它作为 worker ID。Worker ID 只能使用小写字母、数字、`-`、`_`；规则插件也建议遵守此格式。 |
| `enabled` | 否 | 是否加载，默认 `true`。修改根配置后需要管理员执行全局 `/hooks reload`。 |
| `path` | 否 | 相对 `plugins/` 的配置路径，默认 `<name>/hook.toml`；不能是绝对路径或逃出 `plugins/`。 |

插件源码、`hook.toml` 和插件私有状态文件放在 `plugins/<id>/`。ElBot 额外创建 `plugins/_shared/` 供所有 Hook 共享文件；它不是插件目录，也不会被扫描。

根 `hooks.toml` 的解析失败会使本次规则配置加载失败；单个被引用插件配置有误时，该插件会被跳过，其他插件照常加载。TOML 使用严格字段校验，未知字段会报错。规则空 `name` 当前会自动命名为 `rule.<序号>`，重复名称会自动追加序号；不要依赖这种兼容行为。

直接写在根 `plugins/hooks.toml` 中的规则可以各自配置阻断范围。独立插件不在各条规则重复配置，而是在插件 `hook.toml` 的 `[plugin]` 中配置一次，对该插件全部规则和 Worker 生效。插件规则误写这三个字段时会产生 warning，但插件仍正常加载，误写字段会被忽略。

## 规则 Hook

普通规则使用 `[[rules]]`。规则按 `on`、匹配条件、角色和优先级筛选后，依次执行 actions。

### 最小示例

```toml
[[rules]]
name = "hello_reply"
on = "platform.message.received"
blocked_platform = ["telegram"]
blocked_group = ["qqonebot:123456"]
blocked_id = ["qqonebot:10001"]
if = "message.intent_text"
op = "fullmatch"
value = "你好"
action = "send"
kind = "text"
text = "你好呀"
```

### 公共字段、顺序与控制

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `name` | 否 | 规则显示名；建议显式填写。 |
| `description` | 否 | `/hooks` 列表和详情使用的说明。 |
| `on` | 是 | Hook 点，见下表。 |
| `priority` | 否 | 数字越小越先执行；默认 `1000`，`0` 也按默认值处理。相同 priority 按加载顺序稳定执行：根规则、`[[plugins]]` 声明顺序、各插件内规则顺序。 |
| `enabled` | 否 | 是否加载，默认 `true`。 |
| `wakeup` | 否 | 唤起策略：`required` 仅处理已唤起消息（默认），`any` 无论是否唤起都处理，`forbidden` 仅处理未唤起消息。主要用于 `platform.message.received`。 |
| `blocked_platform` | 否 | 仅根 `hooks.toml` 的直接规则使用；跳过指定平台。 |
| `blocked_group` | 否 | 仅根 `hooks.toml` 的直接规则使用；跳过指定群，格式 `<platform>:<平台原始群 ID>`。 |
| `blocked_id` | 否 | 仅根 `hooks.toml` 的直接规则使用；跳过指定用户，格式 `<platform>:<平台原始用户 ID>`。 |
| `consume` | 否 | 在 `platform.message.received` 中为 `true` 时，发送当前 outputs 后不再进入命令或主 LLM。 |
| `stop_propagation` | 否 | 为 `true` 时停止当前 Hook 点之后的规则；不停止 Agent 主流程。 |

`wakeup = "any"` 和 `wakeup = "forbidden"` 允许 Hook 观察未唤起消息，但不会让主 LLM 自动处理它们。`forbidden` 规则遇到已唤起消息时会直接跳过，不影响后续插件、命令或主 LLM。

根规则的阻断检查先于普通匹配和 action 执行，因此命中后不会启动 exec 进程；其他规则不受影响。三项均为精确匹配且不支持通配符，`blocked_group` 同时识别 `group` 和 `supergroup` scope。

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
  { type = "tool", action_name = "search", tool = "web_search", arguments = "{\"query\":\"{{message.text}}\"}" },
  { type = "send", text = "{{actions.search.result}}" },
]
```

```toml
# TOML array table，按声明顺序执行。
[[rules.actions]]
action_name = "search"
type = "tool"
tool = "web_search"
arguments = "{\"query\":\"{{message.text}}\"}"

[[rules.actions]]
type = "send"
text = "{{actions.search.result}}"
```

`action = "..."` 不能和 `actions` 混用。`actions = [...]` 与 `[[rules.actions]]` 都使用 `type` 和 `arguments`；`action_name` 在两种 actions 形式中都可用。内联 `action = "tool"` 的参数字段同样是 `arguments`，不是 `args`。

| 类型 | 主要字段 | 作用 |
| --- | --- | --- |
| `prepend` / `append` | `field`、`text` | 在可编辑文本前后添加内容。 |
| `replace` | `field`、`pattern`、`replace`、`all` | 正则替换；`all = false` 时只替换第一个命中。 |
| `delete` | `field`、`pattern` 或 `text` | 删除全部正则命中。 |
| `send` | 单输出字段或 `outputs` | 追加输出意图。 |
| `tool` | `tool`、`arguments`、可选 `action_name` | 调用已注册工具。 |
| `exec` | `command`、`cwd`、`timeout_seconds`、`field`、`timing`、可选 `action_name` | 启动一次性 `hook.v2` 进程。 |

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

## 统一输出

规则 `send`、`output.send`、一次性 exec Hook 和 Worker Hook 的 response 使用同一套 outputs segment。`target` 和 `timing` 属于整组 outputs，不属于单个 segment。

### TOML 写法

单 segment 可直接写在 `send` action：

```toml
type = "send"
kind = "reply"
message_id = "{{platform.reply_to_message_id}}"
text = "已收到。"
timing = "immediate"
target.group_id = "123456"
```

该写法等价于单元素 `outputs`，两者不能同时出现。多 segment 写法：

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

### JSON 写法

进程 Hook response 和 `output.send` 使用相同结构：

```json
{
  "outputs": [{"kind":"reply","message_id":"456","text":"引用回复"}],
  "target": {"platform":"qqonebot","scope_id":"group:123"},
  "timing": "immediate"
}
```

| 字段 | 说明 |
| --- | --- |
| `kind` | `text`、`image`、`file`、`emoticon`、`at`、`reply`；默认 `text`。 |
| `text` | 文本、媒体 fallback、回复正文或原生表情 fallback。 |
| `url` / `path` / `base64` | `image`、`file` 必须且只能选择一种来源。`url` 只接受 HTTP(S)，`path` 只接受文件系统路径且相对插件目录解析，`base64` 解码后最大 10 MiB。 |
| `name` / `mime_type` | 图片或文件的显示名和 MIME 类型；`name` 也可作为原生表情的可读名称。 |
| `user_id` | `kind = "at"` 的非空平台用户 ID。 |
| `message_id` | `kind = "reply"` 的非空平台消息 ID。 |
| `emoticon_id` | `kind = "emoticon"` 的非空平台原生表情或贴纸 ID；原生表情不接受媒体来源。 |

`outputs` 是一条消息的片段组。发送多条消息需要多个 `send` action 或多次 `output.send`。

### target、timing 与路径

规则 TOML 使用 snake_case target：

```toml
target.platform = "qqonebot"
target.scope_id = "group:123456"
target.private_user_id = "10001"
target.group_id = "123456"
target.superadmins = true
```

JSON 与 TOML target 都使用上述 snake_case 字段；省略时沿用当前事件。`timing` 为 `immediate`（默认）或 `after_assistant`，实际生效范围见 Hook 点表。

媒体相对路径按声明规则的插件目录解析；根 `plugins/hooks.toml` 的规则相对 `plugins/`。大媒体应写入插件目录或 `_shared/` 后返回 `path` 或 `url`。单个 `hook.v2` JSON Lines 帧最大 16 MiB，不要内联大数据。

## 进程 Hook 编程接口

进程 Hook 使用 `hook.v2` JSON Lines。stdin 和 stdout 每行一个 JSON 帧；stdout 只能写协议，日志写 stderr。Host request ID 使用 `host:*`，Hook request ID 使用 `plugin:*`，response 必须复用 request ID。

Host 先发送 `system.init`，成功后发送 `event.handle`。一次性 exec Hook 处理一次事件后退出；Worker Hook 可处理多次事件，并接收 `system.shutdown` 和可能的 `event.cancel`。

### 完整交互示例

以下是一次性 exec Hook 的完整往返；代码块中每行都是一个 JSON Lines 帧。Host 写入 Hook stdin：

```jsonl
{"type":"request","id":"host:init","method":"system.init","params":{"version":"hook.v2","runtime":{"plugin_name":"demo","plugin_dir":"C:/elbot/plugins/demo","config_path":"C:/elbot/plugins/demo/hook.toml","rule_name":"weather","cwd":"C:/elbot/plugins/demo"}}}
```

Hook 从 stdout 返回成功 response：

```jsonl
{"type":"response","id":"host:init","ok":true,"result":{}}
```

随后 Host 发送当前事件：

```jsonl
{"type":"request","id":"host:event","method":"event.handle","params":{"event":{"id":"evt-123","point":"platform.message.received","time":"2026-07-14T12:00:00+08:00","metadata":{"match":{}},"control":{"consume":false,"stop_propagation":false},"platform":{"name":"qqonebot","scope_id":"group:123456","user_id":"10001","conversation_id":"group:123456","message_id":"789","reply_to_message_id":""},"actor":{"id":"qqonebot:10001","role":"user","group_role":"member","user_id":"10001","nickname":"Alice","group_card":"","display_name":"Alice"},"session":{"id":"session-123","mode":"group","title":"","status":""},"request":{"id":"","kind":"","session_id":"","phase":""},"message":{"id":"789","role":"user","platform_text":"天气 上海","intent_text":"天气 上海","segments":[{"type":"text","text":"天气 上海"}]},"llm":{"provider":"","model":""},"tool":{"id":"","name":""}},"match":{},"runtime":{"plugin_name":"demo","plugin_dir":"C:/elbot/plugins/demo","config_path":"C:/elbot/plugins/demo/hook.toml","rule_name":"weather","cwd":"C:/elbot/plugins/demo"}}}
```

Hook 返回处理结果：

```jsonl
{"type":"response","id":"host:event","ok":true,"result":{"status":"completed","matched":true,"result":"weather accepted","message":{"text":"查询上海天气"},"outputs":[{"kind":"text","text":"正在查询……"}],"target":{"platform":"qqonebot","scope_id":"group:123456"},"timing":"immediate","pass_through":false}}
```

处理失败时省略 `result`，返回 `{"type":"response","id":"host:event","ok":false,"error":"error message"}`。response 的 `id` 必须与 request 相同。

### system.init.params

| 字段 | 使用者 | 说明 |
| --- | --- | --- |
| `version` | 全部 | 固定为 `hook.v2`。 |
| `runtime.plugin_name` | 一次性 | 插件名；根规则可能为空。 |
| `runtime.plugin_dir` | 一次性 | 插件目录。 |
| `runtime.config_path` | 一次性 | 声明规则的配置文件。 |
| `runtime.rule_name` | 一次性 | 当前规则名；规则无名称时使用 action 名。 |
| `runtime.cwd` | 一次性 | 当前进程工作目录。 |
| `hook.id` / `hook.description` | Worker | Worker ID 和简介。 |
| `hook.plugin_dir` / `hook.cwd` / `hook.shared_dir` | Worker | 插件目录、工作目录和共享目录。 |
| `tools` | Worker | `[plugin.runtime.tools].allow` 中工具的 schema；未配置时为空。 |

### event.handle.params

| 字段 | 使用者 | 说明 |
| --- | --- | --- |
| `event` | 全部 | 当前事件，字段见文末 Event 章节。 |
| `match` | 全部 | trigger rule 的 regex 捕获；无捕获时为空对象。 |
| `runtime` | 一次性 | 与 `system.init.params.runtime` 相同。 |
| `continuation` | Worker | `true` 表示 waiting 路由捕获的后续消息。 |
| `tool_context` | Worker | 当前前台 `tool.call` 令牌。 |

### event.handle result

| 字段 | 使用者 | 说明 |
| --- | --- | --- |
| `status` | 全部 | 省略或 `completed` 表示结束；Worker 还可返回 `waiting`。 |
| `outputs` / `target` / `timing` | 全部 | 统一输出格式，见“统一输出”。 |
| `pass_through` | 全部 | `false` 表示接管，`true` 表示放行；覆盖规则默认的 `consume` 和 `stop_propagation`。 |
| `matched` | 一次性 | `false` 表示本规则未命中：回滚本规则修改和 outputs，并跳过剩余 actions。 |
| `result` / `error` | 一次性 | 写入 `{{actions.<name>.result/error}}` 的文本。 |
| `message.text` | 一次性 | 覆写 action 的 `field`；未配置 field 时默认 `message.text`。空字符串表示清空，省略表示不改。 |
| `consume` / `stop_propagation` | 一次性 | 为 `true` 时设置对应控制字段。 |
| `conversation_id` | Worker | `waiting` 时必填的非空不透明 ID。 |
| `expires_at` | Worker | `waiting` 时必填的 RFC 3339 时间，不得超过 `max_wait_seconds`。 |

### Host 方法

插件发起 request 时，`id` 必须以 `plugin:` 开头；它不是 method 的一部分。例如，Hook 从 stdout 请求 `platform.call`：

```jsonl
{"type":"request","id":"plugin:get-message-1","method":"platform.call","params":{"platform":"qqonebot","api":"get_msg","params":{"message_id":"789"}}}
```

Host 从 stdin 返回相同 `id`；`result` 的具体结构由平台 API 决定：

```jsonl
{"type":"response","id":"plugin:get-message-1","ok":true,"result":{"message_id":789,"raw_message":"天气 上海"}}
```

| method | 可用方 | `params` 与 `result` |
| --- | --- | --- |
| `shared.*` | 全部 | 读写全局共享状态，见下表。 |
| `platform.call` | 一次性 | params 为 `platform`（可选，默认当前平台）、必填 `api`、可选对象 `params`；result 为平台返回值。 |
| `output.send` | 一次性 | params 使用统一输出结构；result 含 `sent` 数量和 `receipts`。 |
| `message.get_reply` | 一次性 | 无参数；result 含 `message_id` 和 `available`。 |
| `message.get` | 一次性 | 保留接口；当前总是返回 `{"available":false}`。 |
| `hook.log` | 一次性 | params 作为日志内容；result 为 `{"ok":true}`。 |
| `tool.call` | Worker | 调用 allowlist 中的 ElBot 工具，字段见“工具调用”。 |
| `hooks.reload` | Worker | params 为空对象；重载当前插件，结果见“插件自身重载”。 |

### 共享状态

一次性 exec Hook 与所有 Worker Hook 共享一个进程内 JSON KV：

| method | `params` | 成功 `result` |
| --- | --- | --- |
| `shared.get` | `{"key":"weather/cache"}` | `{"found":true,"value":<任意 JSON>}`；不存在时 `found=false`。 |
| `shared.set` | `{"key":"weather/cache","value":<任意 JSON>,"ttl_seconds":600}` | `{"ok":true}` |
| `shared.delete` | `{"key":"weather/cache"}` | `{"deleted":true/false}` |
| `shared.list` | `{"prefix":"weather/"}` | `{"keys":["weather/cache",...]}`；按字典序排列，空 prefix 列出全部。 |
| `shared.compare_and_swap` | `{"key":"weather/cache","expected":<旧 JSON>,"value":<新 JSON>,"ttl_seconds":600}` | `{"swapped":true/false}` |

`ttl_seconds` 是空闲超时：省略时默认 600 秒；正数会在成功 `get`、`set` 或 CAS 时重新计时；`0` 不按时间过期；负数非法。`list` 和失败的 CAS 不刷新时间，过期 key 视为不存在。

key 去除首尾空白后必须非空，最长 256 字节。建议使用 `users/<platform>/<id>`、`cache/<name>` 等前缀避免冲突，但前缀不是权限边界。value 必须是合法 JSON，压缩后单值最大 1 MiB；共享区最多 10,000 条，key 与 value 合计最大 32 MiB。

达到上限时先删除过期项，再按最近使用时间淘汰最冷数据；`ttl_seconds = 0` 也可能被淘汰。CAS 按压缩后的 JSON 原子比较；省略 `expected` 表示仅当 key 不存在时写入，显式 `expected: null` 只匹配 JSON `null`。共享状态跨 Hook 重启和 `/hooks reload` 保留，ElBot 重启后清空；需要持久化时写入插件目录或 `_shared/`。

### 一次性 exec Hook

```toml
[[rules]]
name = "extract_answer"
on = "llm.response.received"
always = true

[[rules.actions]]
action_name = "extract"
type = "exec"
command = ["uv", "run", "extract.py"]
field = "llm.text"
timeout_seconds = 30
```

`command` 是非空 argv 数组：第一个元素是程序，其余元素原样作为参数；每个元素分别渲染模板，不经过 shell。需要 shell 语法时显式配置为 `["bash", "-lc", "..."]`、`["sh", "-c", "..."]` 或对应平台解释器。`timeout_seconds` 为 `0` 或省略时不额外超时，不能为负数。

插件内规则的 `cwd` 默认插件目录，只能使用插件内相对路径；根 `plugins/hooks.toml` 的规则默认在 `plugins/` 执行，可使用相对或绝对 cwd。

一次性 exec Hook 的 `platform.call` 中，`platform` 可省略并默认当前平台；显式填写时必须等于当前平台。adapter 返回 JSON 时 Host 原样放入 response `result`，否则作为字符串。

## Worker Hook

Worker Hook 由插件自己的 `hook.toml` 声明。Persistent Worker 在 ElBot 启动或 `/hooks reload` 后启动；Transient Worker 在 trigger rule 命中时启动。省略 `[plugin.runtime]` 或 `mode` 等价于 `mode = "once"`，不创建 Worker。

```toml
[plugin]
name = "weather"
description = "有状态天气助手"
blocked_platform = []
blocked_group = ["qqonebot:123456"]
blocked_id = ["qqonebot:10001"]

[plugin.runtime]
mode = "persistent"
command = ["uv", "run", "weather_hook.py"]
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
wakeup = "any"
if = "message.intent_text"
op = "contains"
value = "天气"
# Worker Hook 的 rules 不设置 action/actions；控制字段作为 event.handle 的默认值。
consume = true
stop_propagation = true
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

插件内所有规则（包括 exec action）、Worker trigger 和 waiting 路由统一使用 `[plugin]` 的阻断配置。若在插件内的 `[[rules]]` 误写 `blocked_platform`、`blocked_group` 或 `blocked_id`，加载和 reload 会返回 warning 并忽略这些规则级字段，不会阻止插件运行。

`[plugin.runtime]` 在 `mode = "persistent"` 或 `mode = "transient"` 时需要以下字段；`mode` 只能是 `once`、`persistent` 或 `transient`，省略时为 `once`。`once` 不创建 worker，也不要求其余 runtime 字段。

| 字段 | 说明 |
| --- | --- |
| `mode` | `once`（默认）、`persistent` 或 `transient`。`persistent` 启动后常驻；`transient` 只在规则命中后启动，并在会话结束后退出。 |
| `command` | 非空 argv 数组；第一个元素是程序，其余元素原样作为参数，不渲染模板，也不经过 shell。 |
| `cwd` | 相对插件目录，不能为绝对路径或逃出插件目录。通常为 `.`。 |
| `startup_timeout_seconds` | 等待 `system.init` 成功 response 的最长秒数，必须大于 `0`。 |
| `shutdown_timeout_seconds` | 等待 `system.shutdown` 和退出的最长秒数，必须大于 `0`。 |
| `event_timeout_seconds` | 单次 `event.handle` 的最长秒数，也限制此次工具上下文，必须大于 `0`。 |
| `max_wait_seconds` | waiting 会话可声明的最长剩余时间，必须大于 `0`。 |

`[plugin.runtime.restart]` 只适用于 `mode = "persistent"`：

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

Worker trigger rule 复用规则 Hook 的匹配、角色、priority、`wakeup`、`consume` 和 `stop_propagation`；配置 `action` 或 `actions` 会校验失败。`pass_through` 省略时使用规则中的两个控制字段。

Worker 状态为 `starting`、`ready`、`running`、`degraded`、`stopping`、`stopped` 或 `failed`。停止时 Host 先请求 `system.shutdown`，超过关闭超时才强制结束进程。

### waiting、并发与取消

需要继续收集输入时返回 `status = "waiting"`、`conversation_id` 和 `expires_at`；`expires_at` 必须晚于当前时间，且剩余时间不超过 `max_wait_seconds`。完成时返回 `completed` 或省略 status。

同一 `platform + scope_id + actor.id` 在同一个 worker 内串行，不同路由可并行。waiting 租约也以该三元组为 key：

- 正常串行分发中，首个返回 `waiting` 的 worker 持有该路由，后续 worker trigger rule 不会再分发该路由。实现每条路由只存一份租约；若并发事件使多个 worker 同时竞争，最后写入者覆盖此前租约。因此多个 worker Hook 可能匹配同一路由时，应使用 `priority`、更严格条件或 `stop_propagation` 明确归属。
- waiting 存在时，普通 `platform.message.received` 规则仍会先运行；worker trigger rule 不会再次触发。
- Host 先将后续消息以 `continuation = true` 路由给持有租约的 Hook；插件返回 `pass_through = true` 时，再从后续规则继续传播，最后可进入命令或主 LLM。
- continuation 放行后不会用同一条消息重新触发刚结束 waiting 的插件。
- continuation 返回 `completed` 或省略 `status` 后，Host 立即释放租约；对于 transient worker，同时关闭该进程。再次返回 `waiting` 则以新 ID、过期时间续期。
- 阻断策略命中、worker 退出、停止或重载会清理该插件租约。

用户发送精确的 `/cancel` 时，Host 取消该路由当前执行或 waiting 会话，并关闭 Transient Worker；Persistent Worker 只接收取消通知并保留进程和内存。

```json
{"type":"event","method":"event.cancel","params":{"conversation_id":"weather-42"}}
```

### 工具调用

Worker Hook 自行维护 LLM loop、业务状态和工具使用决策。Host 对 `tool.call` 只校验 allowlist、前后台能力、context 归属、次数、超时、取消和发送 target；**不执行 ElBot 的风险分级、权限策略或交互确认**。插件必须自行承担工具授权和风险控制。

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

`content` 是工具文本结果，`segments` 为 `{type,text,url,mime_type,name}` 数组，`warnings` 为字符串数组，`receipts` 是工具 outputs 经 Output Manager 发送后的平台消息 ID。失败使用外层 `ok=false,error`。Hook 工具调用不会写入 Agent Session。

### 插件自身重载

worker 修改自身 `hook.toml` 后可请求重载自己：

```json
{"type":"request","id":"plugin:reload-1","method":"hooks.reload","params":{}}
```

不要在唯一读取 stdin 的协议循环中同步等待该 response；否则 Host 回写无人读取而死锁。应让持续读循环按 request ID 分发 response，或从事件工作线程发 request。

Host 会先完整读取并校验候选配置。失败时返回 `ok=false`，旧规则和进程不变；成功时先返回：

```json
{"type":"response","id":"plugin:reload-1","ok":true,"result":{"scheduled":true}}
```

实际替换等当前 `event.handle` 结束后发生：仅替换调用插件的规则和 worker，清理该插件 waiting 路由与工具上下文，不重启其他插件。调用者身份由进程确定，不能重载别的插件；`starting`、`stopping`、`stopped` 时不能请求重载。根 `plugins/hooks.toml` 的插件引用、`enabled`、`path` 以及插件增删仍需管理员执行全局 `/hooks reload`。

## Event 与模板字段

`event` 顶层包含 `id`、`point`、`time`、`metadata`、`control`、`platform`、`actor`、`session`、`request`、`message`、`llm`、`tool`；可能还有 `outputs`、`error`。与当前 Hook 点无关的字段会是零值、空对象或被省略，插件必须按可空值处理。

| 对象 | 字段 |
| --- | --- |
| `control` | `consume`、`stop_propagation`。 |
| `platform` | `name`、`scope_id`、`user_id`、`conversation_id`、`message_id`、`reply_to_message_id`。 |
| `actor` | `id`（`<platform>:<id>`）、`user_id`、`role`、`group_role`、`nickname`、`group_card`、`display_name`。`display_name` 是纯展示名；群聊通常优先群名片，否则使用昵称。 |
| `session` | `id`、`mode`、`title`、`status`；部分 Hook 点只提供 `id`。 |
| `request` | `id`、`kind`、`session_id`、`phase`；当前可能都为空。 |
| `error` | `message`；仅错误事件可靠。 |

`message`：

| 字段 | 说明 |
| --- | --- |
| `id`、`role` | 消息 ID 和 `user` / `assistant` / `system` / `tool` 角色。 |
| `platform_text` | 平台原始文本，可能为空。 |
| `platform_message` | 平台原生 message JSON，结构由平台决定；当前 QQ OneBot 提供原始 `message` 值，其他平台可能省略。 |
| `intent_text` | 去除唤醒前缀后的用户意图；消息输入 Hook 通常应优先使用它。 |
| `segments` | `{type,text,url,mime_type,name}` 数组，type 为 `text` / `image` / `file`。 |
| `reply` | 被引用消息：`message_id`、`sender_id`、`text`、`display_text`、`segments`。 |
| `messages` | LLM 消息数组；非 LLM 上下文通常为空。 |

### 读取用户输入

`segments` 保留平台归一化后的原始内容；处理文本指令时优先读取已去除唤醒前缀的 `intent_text`，为空再拼接 text segments：

```python
def message_text(event):
    message = event.get("message") or {}
    intent = str(message.get("intent_text") or "").strip()
    if intent:
        return intent
    return "".join(
        str(item.get("text") or "")
        for item in message.get("segments") or []
        if item.get("type") == "text"
    ).strip()
```

trigger rule 已匹配的首次调用通常无需重复校验同一指令；waiting continuation 只捕获同一 platform、scope 和 actor。业务参数、授权和插件权限仍由插件校验。

`llm` 包含 `provider`、`model`、`messages`、`tools`、`usage`、`source_text`、`text`、`tool_calls`、`elapsed_ms`。其中嵌套 `messages`、`tool_calls`、`usage` 沿用 Go 导出的 JSON 名：`Role/Segments/Name/ToolCallID/ToolCalls`、`ID/Name/Arguments`、`PromptTokens/CompletionTokens/TotalTokens/CacheHitTokens`。

`tool` 包含 `id`、`name`、`arguments`（JSON 字符串）、`risk`、`result`、`error`。prepared 阶段主要提供 `id/name/arguments`，completed 阶段才有 `risk/result/error`。

可用于 `if`、`match.field` 和模板的文本字段：

```text
platform.name / platform.scope_id / platform.user_id / platform.conversation_id / platform.message_id / platform.reply_to_message_id
actor.id / actor.user_id / actor.role / actor.group_role / actor.nickname / actor.group_card / actor.display_name
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
