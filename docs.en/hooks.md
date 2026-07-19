<!-- This file is auto-translated from docs/hooks.md. Do not edit manually. -->

# Hook

Hooks match or modify events, append output, or hand over events to external programs at key boundaries of ElBot. Output is sent uniformly by the Output Manager.

## Terminology and Selection

| Name | Configuration and Lifecycle | Applicable Scenarios |
| --- | --- | --- |
| **Rule Hook** | TOML `[[rules]]`; execute actions after matching. | Simple logic such as modifying text or sending content. |
| **One-time exec Hook** | `type = "exec"` in the rule; starts an external program each time it is triggered, and exits after processing the current event. | Independent scripts or logic that does not require saving process state. |
| **Transient Worker** | `[plugin.runtime] mode = "transient"`; starts after the trigger rule is hit, and exits after the waiting Session ends. | Short-term stateful, multi-turn interaction. |
| **Persistent Worker** | `[plugin.runtime] mode = "persistent"`; starts with ElBot and remains resident. | Long-term state, background tasks, frequent events. |

Transient Worker and Persistent Worker are collectively referred to as **Worker Hook**; One-time exec Hooks and Worker Hooks are collectively known as **process Hooks**. `mode = "once"` does not create a Worker and still runs according to the rule Hook; A one-time exec Hook will only be started if the rule is configured with `type = "exec"`. All entry points that produce outputs use the same set of segments.

See Hook examples in [Hook Showcase](https://github.com/Elfreese/elbot-showcase/blob/main/hooks/README.md).

## Configuration Location

The `plugins/hooks.toml` in the configuration directory is the rule entry point. It can directly contain `[[rules]]`, or reference plugin directories:

```toml
[[plugins]]
name = "weather"
# Read plugins/weather/hook.toml by default
```

`[[plugins]]` field:

| Field | Required | Description |
| --- | --- | --- |
| `name` | Yes | Plugin identifier and default directory name; Worker Hook also uses it as the worker ID. Worker ID can only use lowercase letters, numbers, `-`, and `_`; Rule plugins are also recommended to follow this format. |
| `enabled` | No | Whether to load, default `true`. After modifying the root configuration, an administrator needs to execute a global `/hooks reload`. |
| `path` | No | Configuration path relative to `plugins/`, default `<name>/hook.toml`; it cannot be an absolute path or escape `plugins/`. |

Plugin source code, `hook.toml`, and plugin private state files are placed in `plugins/<id>/`. ElBot additionally creates `plugins/_shared/` for all Hooks to share files; It is not a plugin directory and will not be scanned.

Parsing failure of the root `hooks.toml` will cause the current rule configuration loading to fail; When a single referenced plugin configuration is incorrect, that plugin will be skipped, and other plugins will be loaded as usual. TOML uses strict field validation; unknown fields will cause an error. If the rule `name` is empty, it will currently be automatically named `rule.<序号>`, and duplicate names will have sequence numbers appended automatically; Do not rely on this compatibility behavior.

Rules written directly in the root `plugins/hooks.toml` can each configure their own blocking scope. Independent plugins are not configured repeatedly in each rule, but are configured once in the `[plugin]` of the plugin `hook.toml`, which takes effect for all rules and Workers of that plugin. A warning will be generated if these three fields are incorrectly written in plugin rules, but the plugin will still load normally, and the incorrectly written fields will be ignored.

## Rule Hook

Ordinary rules use `[[rules]]`. After rules are filtered by `on`, matching conditions, roles, and priority, the actions are executed in sequence.

### Minimal Example

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

### Common Fields, Order and Control

| Field | Required | Description |
| --- | --- | --- |
| `name` | No | Rule display name; it is recommended to fill this in explicitly. |
| `description` | No | Instructions for using `/hooks` lists and details. |
| `on` | Yes | Hook point, see the table below. |
| `priority` | No | The smaller the number, the earlier it is executed; Default is `1000`, and `0` is also handled according to the default value. The same priority is executed stably according to the loading order: root rules, `[[plugins]]` declaration order, and the order of rules within each plugin. |
| `enabled` | No | Whether to load, defaults to `true`. |
| `wakeup` | No | Wake-up strategy: `required` only processes awakened messages (default), `any` processes regardless of whether it is awakened, `forbidden` only processes non-awakened messages. Mainly used for `platform.message.received`. |
| `blocked_platform` | No | Used only by direct rules in the root `hooks.toml`; skips specified platforms. |
| `blocked_group` | No | Used only by direct rules in the root `hooks.toml`; skips specified groups, format `<platform>:<平台原始群 ID>`. |
| `blocked_id` | No | Used only by direct rules in the root `hooks.toml`; skips specified users, format `<platform>:<平台原始用户 ID>`. |
| `consume` | No | When set to `true` in `platform.message.received`, it will no longer enter commands or the main LLM after sending the current outputs. |
| `stop_propagation` | No | When set to `true`, it stops rules following the current Hook point; it does not stop the Agent's main process. |

`wakeup = "any"` and `wakeup = "forbidden"` allow Hooks to observe non-awakened messages, but will not let the main LLM process them automatically. `forbidden` rules will skip directly when encountering awakened messages, without affecting subsequent plugins, commands, or the main LLM.

The blocking check of root rules is executed prior to normal matching and action execution; therefore, the exec process will not be started after a match; Other rules are unaffected. All three items are exact matches and do not support wildcards; `blocked_group` recognizes both `group` and `supergroup` scopes.

### Hook Point

| Hook Point | Timing | Main payload | Editable Fields | outputs will be sent |
| --- | --- | --- | --- | --- |
| `platform.connected` | Platform connection completed | `platform` | None | Yes |
| `platform.message.received` | Upon receiving a user message, before commands and the LLM | `platform`、`actor`、`message` | `message.text/segments` | Yes |
| `agent.input.prepared` | Before input is written to the Session | `session`、`message` | `message.text/segments` | No |
| `llm.turn.prepared` | Before the full round of LLM calls | `session`, current user `message.segments`, read-only `llm.messages`, tools/provider/model | `message.segments` or `llm.latest_user_text` | No |
| `llm.request.prepared` | Before each actual model request | Same as above; only newly arrived pending in the tool flow provide `message` | pending `message.segments` or `llm.latest_user_text` | No |
| `llm.response.received` | Model response completed | `llm.text/source_text/tool_calls/usage` | `llm.text` | Yes |
| `tool.call.prepared` | Before tool call | `session`、`tool` | `tool.arguments` | No |
| `tool.call.completed` | After the tool is actually executed | `session`、`message.segments`、`tool.name/result/error/risk` | `tool.result` or process response `message.segments` | No |
| `agent.output.prepared` | Before sending each segment of assistant output | `message` | assistant `message.text` | No |
| `agent.turn.output.prepared` | Before sending the final assistant output of the round | `message` | assistant `message.text` | No |
| `platform.message.sent` | Notification after assistant chat or preview is successfully sent | `message` | None | No |
| `error.occurred` | Notification when an error occurs during the Hook/Agent stage | Original event context, `error.message` | None | No |

Although `send` action can create an output intent at all Hook points, only the points in the table above where 'outputs will be sent' will consume it. Currently, only the output of `llm.response.received` in `timing = "after_assistant"` will actually be deferred; Outputs from other points are still sent immediately according to the current flow.

The final assistant text usually passes through `agent.turn.output.prepared` first, and then through `agent.output.prepared`. Do not configure the same append/prepend rule at two different points; otherwise, the text will be processed twice.

### Matching Conditions and Roles

Conditions can be written as a single condition shorthand or as an AND array; the two cannot be used interchangeably:

```toml
if = "message.intent_text"
op = "fullmatch"
value = "天气"
```

```toml
# Every item in the array must match.
match = [
  { field = "message.intent_text", op = "startswith", value = "查询天气" },
  { field = "actor.group_role", op = "fullmatch", value = "admin" },
]
```

`always = true` indicates unconditional matching and cannot be used interchangeably with `if/op/value` or `match`.

| `op` | Field Requirements | Behavior |
| --- | --- | --- |
| `always` | Do not specify `field/value` | Always matches. |
| `exists` | Specify only `field` | Matches only if the text value of the field is not empty; empty strings and fields not provided in the current event do not match. |
| `contains` | `field/value` | Substring match. |
| `fullmatch` | `field/value` | The entire field is strictly equal to the value. |
| `startswith` / `endswith` | `field/value` | Prefix or suffix match. |
| `regex` | `field/value` | The first substring match of the Go RE2 regular expression. |

`regex` is not a full-field match; Use `^` and `$` in the regular expression when full-field constraints are required. In captures, `group.0` is the full match, and subsequent groups start from `group.1`. The index for multiple regex conditions starts from `0`, counting only the regex conditions themselves.

Role field:

```toml
roles = ["user", "admin"]
actor_roles = ["superadmin"]
group_roles = ["owner", "admin", "member", "unknown"]
```

Within the same array, it is OR; Between `roles`, `actor_roles`, and `group_roles`, it is AND. `roles` can be a mix of Actor roles (`user`, `superadmin`) and group roles.

### action syntax and execution order

Choose one format for each rule:

```toml
# A single inline action.
action = "append"
field = "llm.text"
text = "\n处理完成。"
```

```toml
# An inline actions array, executed in array order.
actions = [
  { type = "tool", action_name = "search", tool = "web_search", arguments = "{\"query\":\"{{message.text}}\"}" },
  { type = "send", text = "{{actions.search.result}}" },
]
```

```toml
# TOML array table, executed in declaration order.
[[rules.actions]]
action_name = "search"
type = "tool"
tool = "web_search"
arguments = "{\"query\":\"{{message.text}}\"}"

[[rules.actions]]
type = "send"
text = "{{actions.search.result}}"
```

`action = "..."` cannot be mixed with `actions`. Both `actions = [...]` and `[[rules.actions]]` use `type` and `arguments`; `action_name` is available in both action forms. The parameter field for inline `action = "tool"` is also `arguments`, not `args`.

| Type | Main fields | Effect |
| --- | --- | --- |
| `prepend` / `append` | `field`、`text` | Add content before and after the editable text. |
| `replace` | `field`、`pattern`、`replace`、`all` | Regex replacement; only the first match is replaced when `all = false`. |
| `delete` | `field`, `pattern`, or `text` | Delete all regex matches. |
| `send` | Single output field or `outputs` | Append output intent. |
| `tool` | `tool`, `arguments`, optional `action_name` | Call a registered tool. |
| `exec` | `command`, `cwd`, `timeout_seconds`, `field`, `timing`, optional `action_name` | Start a one-time `hook.v2` process. |

Named `tool` / `exec` actions are available for subsequent actions:

```text
{{actions.<name>.result}}
{{actions.<name>.error}}
```

Unnamed `tool` uses the tool name as the result name; unnamed `exec` uses `exec`. Multiple unnamed execs within the same rule will overwrite the same result; they should be explicitly named when template referencing is required.

### Text action

Editable fields strictly depend on the Hook point; see the Hook point table above. Other fields cannot be edited even if they can be matched or used as templates.

For `message.text` and `llm.latest_user_text`:

- `prepend` / `append` modify the first or last text segment; Add a text segment when no text segment exists; Images and files are preserved.
- `replace` / `delete` only process text segments; images and files are preserved.
- The `message.text` overwrite of the exec response only replaces the text segment; original image and file segments will be preserved.

Process Hooks can also directly return `message.segments` to replace the full content of the current message; An explicit empty array indicates clearing. If the same response returns both `message.text` and `message.segments`, `message.segments` shall prevail.

### Message Content Modification

`message.segments` always points to the new message explicitly bound to the current Hook point, and does not represent the full history:

- `platform.message.received`, `agent.input.prepared`, and `llm.turn.prepared` are bound to the initial user message of this turn.
- `llm.request.prepared` is bound to the pending item only when a new pending exists before the next request in the tool flow; When there is no pending item, no editable message is provided, and the initial input of this turn cannot be modified again.
- `tool.call.completed` is bound to the tool result produced after actually entering the tool execution phase; pre-check failure, confirmation rejection, or failure before startup will not trigger this point.

`llm.messages` is the complete working context to be sent to the model, including system, historical messages, current messages, and tool messages, but it is read-only for ordinary Hooks. Hooks cannot modify the system or historical messages. `llm.latest_user_text` is a compatibility view of the text part of the currently bound user message, and no longer represents the last `role=user` in the history; It is empty when no message is bound.

Modifications by the user or pending Hooks will be written to the Session history before the model request; Modifications from tool completion Hooks will also be written to the transcript. For plain text continuations, only `content` is saved; when non-text content such as images is included, complete segments are additionally saved. `message.platform_text` always retains the original text from the platform and does not change with segments.

For example, if the original platform message is "This is a dog", a process Hook can use the same response to change the content that the model actually sees and persists to "This is a cat" with an attached image, while `message.platform_text` remains "This is a dog". Where the `result` field is:

```json
{
  "message": {
    "segments": [
      {"type": "text", "text": "这是猫"},
      {"type": "image", "path": "assets/cat.png", "name": "cat.png"}
    ]
  }
}
```

Images can use HTTP(S) `url`, `path` relative to the plugin directory, or `base64` up to 10 MiB; path/base64 will be normalized into a data URL readable by the model. `message.segments` modifies the LLM input and Session history; `outputs` creates an output intent sent to the platform; the two will not be converted into each other.

In the tool completion event, `message.role` is `tool`, `message.segments` is the original multimodal result of the tool, and `tool.name` and `tool.id` identify the tool and this specific call. In the tool preparation event, `tool.id` and `tool.name` are read-only; only `tool.arguments` can be rewritten; The rewritten parameters are used for actual execution, subsequent LLM requests, and the transcript.

## Unified Output

Rules `send`, `output.send`, one-time exec Hooks, and Worker Hook responses use the same set of outputs segments. `target` and `timing` belong to the entire group of outputs, not to a single segment.

### TOML Syntax

A single segment can be written directly in the `send` action:

```toml
type = "send"
kind = "reply"
message_id = "{{platform.reply_to_message_id}}"
text = "已收到。"
timing = "immediate"
target.group_id = "123456"
```

This syntax is equivalent to a single-element `outputs`, and both cannot appear simultaneously. Multi-segment syntax:

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

### JSON Syntax

Process Hook responses and `output.send` use the same structure:

```json
{
  "outputs": [{"kind":"reply","message_id":"456","text":"引用回复"}],
  "target": {"platform":"qqonebot","scope_id":"group:123"},
  "timing": "immediate"
}
```

| Field | Description |
| --- | --- |
| `kind` | `text`, `image`, `file`, `emoticon`, `at`, `reply`; default `text`. |
| `text` | Text, media fallback, reply body, or native emoji fallback. |
| `url` / `path` / `base64` | `image` and `file` must and can only choose one source. `url` only accepts HTTP(S), `path` only accepts filesystem paths resolved relative to the plugin directory, and `base64` has a maximum size of 10 MiB after decoding. |
| `name` / `mime_type` | Display name and MIME type of the image or file; `name` can also serve as the readable name of a native emoji. |
| `user_id` | Non-empty platform user ID of `kind = "at"`. |
| `message_id` | Non-empty platform message ID of `kind = "reply"`. |
| `emoticon_id` | Non-empty platform native emoji or sticker ID of `kind = "emoticon"`; native emojis do not accept media sources. |

`outputs` is a group of segments for a single message. Sending multiple messages requires multiple `send` actions or multiple `output.send`.

### target, timing and paths

Rule TOML uses snake_case target:

```toml
target.platform = "qqonebot"
target.scope_id = "group:123456"
target.private_user_id = "10001"
target.group_id = "123456"
target.superadmins = true
```

Both JSON and TOML targets use the snake_case fields mentioned above; If omitted, the current event is used. `timing` is `immediate` (default) or `after_assistant`; for the actual scope of effect, refer to the Hook point table.

Relative media paths are resolved based on the plugin directory of the declared rule; Rules for the root `plugins/hooks.toml` are relative to `plugins/`. Large media files should be written to the plugin directory or `_shared/`, then return `path` or `url`. A single `hook.v2` JSON Lines frame has a maximum size of 16 MiB; do not inline large data.

## Process Hook Programming Interface

Process Hook uses `hook.v2` JSON Lines. one JSON frame per line for stdin and stdout; stdout can only be used to write the protocol; logs should be written to stderr. Host request ID uses `host:*`, Hook request ID uses `plugin:*`, and the response must reuse the request ID.

The Host first sends `system.init`, and then sends `event.handle` upon success. A one-time exec Hook exits after processing a single event; A Worker Hook can process multiple events and receives `system.shutdown` and potentially `event.cancel`.

### Complete Interaction Example

The following is a complete round-trip of a one-time exec Hook; each line in the code block is a JSON Lines frame. Host writes to Hook stdin:

```jsonl
{"type":"request","id":"host:init","method":"system.init","params":{"version":"hook.v2","runtime":{"plugin_name":"demo","plugin_dir":"C:/elbot/plugins/demo","config_path":"C:/elbot/plugins/demo/hook.toml","rule_name":"weather","cwd":"C:/elbot/plugins/demo"}}}
```

Hook returns a successful response from stdout:

```jsonl
{"type":"response","id":"host:init","ok":true,"result":{}}
```

Subsequently, the Host sends the current event:

```jsonl
{"type":"request","id":"host:event","method":"event.handle","params":{"event":{"id":"evt-123","point":"platform.message.received","time":"2026-07-14T12:00:00+08:00","metadata":{"match":{}},"control":{"consume":false,"stop_propagation":false},"platform":{"name":"qqonebot","scope_id":"group:123456","user_id":"10001","conversation_id":"group:123456","message_id":"789","reply_to_message_id":""},"actor":{"id":"qqonebot:10001","role":"user","group_role":"member","user_id":"10001","nickname":"Alice","group_card":"","display_name":"Alice"},"session":{"id":"session-123","mode":"group","title":"","status":""},"request":{"id":"","kind":"","session_id":"","phase":""},"message":{"id":"789","role":"user","platform_text":"天气 上海","intent_text":"天气 上海","segments":[{"type":"text","text":"天气 上海"}]},"llm":{"provider":"","model":""},"tool":{"id":"","name":""}},"match":{},"runtime":{"plugin_name":"demo","plugin_dir":"C:/elbot/plugins/demo","config_path":"C:/elbot/plugins/demo/hook.toml","rule_name":"weather","cwd":"C:/elbot/plugins/demo"}}}
```

Hook returns the processing result:

```jsonl
{"type":"response","id":"host:event","ok":true,"result":{"status":"completed","matched":true,"result":"weather accepted","message":{"text":"查询上海天气"},"outputs":[{"kind":"text","text":"正在查询……"}],"target":{"platform":"qqonebot","scope_id":"group:123456"},"timing":"immediate","pass_through":false}}
```

Tool completion Hooks can directly return text and images:

```jsonl
{"type":"response","id":"host:event","ok":true,"result":{"status":"completed","message":{"segments":[{"type":"text","text":"截图完成"},{"type":"image","path":"result.png","mime_type":"image/png"}]}}}
```

Image segments must provide one and only one of `url`, `path`, or `base64`. `url` accepts absolute HTTP(S) URLs or `data:image/...;base64,...`; relative `path` are resolved based on the plugin directory; path, base64, and data URLs have a maximum size of 10 MiB after decoding and are normalized to data URLs before being sent to the LLM. Currently, this replacement protocol supports `text` and `image` segments.

When processing fails, omit `result` and return `{"type":"response","id":"host:event","ok":false,"error":"error message"}`. The `id` of the response must be the same as the request.

### system.init.params

| Field | User | Description |
| --- | --- | --- |
| `version` | All | Fixed as `hook.v2`. |
| `runtime.plugin_name` | One-time | Plugin name; the root rule may be empty. |
| `runtime.plugin_dir` | One-time | Plugin directory. |
| `runtime.config_path` | One-time | Configuration file for declaring rules. |
| `runtime.rule_name` | One-time | Current rule name; use the action name if the rule has no name. |
| `runtime.cwd` | One-time | Current working directory of the process. |
| `hook.id` / `hook.description` | Worker | Worker ID and description. |
| `hook.plugin_dir` / `hook.cwd` / `hook.shared_dir` | Worker | Plugin directory, working directory, and shared directory. |
| `tools` | Worker | The schema of the tool in `[plugin.runtime.tools].allow`; empty if not configured. |

### event.handle.params

| Field | User | Description |
| --- | --- | --- |
| `event` | All | Current event; see the Event section at the end for fields. |
| `match` | All | Regex captures from the trigger rule; an empty object if there are no captures. |
| `runtime` | One-time | Same as `system.init.params.runtime`. |
| `continuation` | Worker | `true` represents subsequent messages captured by the waiting route. |
| `tool_context` | Worker | Current foreground `tool.call` token. |

### event.handle result

| Field | User | Description |
| --- | --- | --- |
| `status` | All | Omitted or `completed` indicates the end; the Worker can also return `waiting`. |
| `outputs` / `target` / `timing` | All | Unified output format, see "Unified Output". |
| `pass_through` | All | `false` indicates takeover, `true` indicates pass-through; overrides the rule defaults `consume` and `stop_propagation`. |
| `matched` | One-time | `false` indicates that this rule was not hit: roll back the modifications and outputs of this rule, and skip the remaining actions. |
| `result` / `error` | One-time | Text to be written to `{{actions.<name>.result/error}}`. |
| `message.text` | All | One-time Hooks overwrite the `field` of the action, while Worker Hooks overwrite the current message text; an empty string indicates clearing, omission indicates no change, and existing media is preserved. |
| `message.segments` | All | Replaces the complete segments of the current message; supports text and images, and an explicit empty array indicates clearing. It takes precedence when appearing simultaneously with `message.text`. |
| `consume` / `stop_propagation` | One-time | Set the corresponding control field when it is `true`. |
| `conversation_id` | Worker | A required non-empty opaque ID when `waiting`. |
| `expires_at` | Worker | RFC 3339 time required when `waiting`, must not exceed `max_wait_seconds`. |

### Host Methods

When the plugin initiates a request, `id` must start with `plugin:`; It is not part of the method. For example, the Hook requests `platform.call` from stdout:

```jsonl
{"type":"request","id":"plugin:get-message-1","method":"platform.call","params":{"platform":"qqonebot","api":"get_msg","params":{"message_id":"789"}}}
```

The Host returns the same `id` from stdin; the specific structure of `result` is determined by the platform API:

```jsonl
{"type":"response","id":"plugin:get-message-1","ok":true,"result":{"message_id":789,"raw_message":"天气 上海"}}
```

| method | Available party | `params` and `result` |
| --- | --- | --- |
| `shared.*` | All | Read and write global shared state, see the table below. |
| `platform.call` | One-time | params are `platform` (optional, default current platform), required `api`, and optional object `params`; result is the platform return value. |
| `output.send` | One-time | params use a unified output structure; result contains the count of `sent` and `receipts`. |
| `message.get_reply` | One-time | No parameters; result contains `message_id` and `available`. |
| `message.get` | One-time | Reserved interface; currently always returns `{"available":false}`. |
| `hook.log` | One-time | params are used as log content; result is `{"ok":true}`. |
| `tool.call` | Worker | Call ElBot tools in the allowlist; see "Tool Call" for fields. |
| `hooks.reload` | Worker | params is an empty object; reload the current plugin; see "Plugin Self-Reload" for results. |

### Shared State

One-time exec Hooks share an in-process JSON KV with all Worker Hooks:

| method | `params` | Successful `result` |
| --- | --- | --- |
| `shared.get` | `{"key":"weather/cache"}` | `{"found":true,"value":<任意 JSON>}`; `found=false` when it does not exist. |
| `shared.set` | `{"key":"weather/cache","value":<任意 JSON>,"ttl_seconds":600}` | `{"ok":true}` |
| `shared.delete` | `{"key":"weather/cache"}` | `{"deleted":true/false}` |
| `shared.list` | `{"prefix":"weather/"}` | `{"keys":["weather/cache",...]}`; sorted lexicographically; an empty prefix lists all. |
| `shared.compare_and_swap` | `{"key":"weather/cache","expected":<旧 JSON>,"value":<新 JSON>,"ttl_seconds":600}` | `{"swapped":true/false}` |

`ttl_seconds` is the idle timeout: defaults to 600 seconds if omitted; A positive number will reset the timer upon successful `get`, `set`, or CAS; `0` does not expire by time; Negative numbers are invalid. `list` and failed CAS do not refresh the time; expired keys are treated as non-existent.

The key must be non-empty after trimming leading and trailing whitespace, with a maximum length of 256 bytes. It is recommended to use prefixes such as `users/<platform>/<id>` and `cache/<name>` to avoid conflicts, but prefixes are not permission boundaries. The value must be valid JSON, with a maximum size of 1 MiB per value after compression; The shared area supports up to 10,000 entries, with a combined maximum size of 32 MiB for keys and values.

When the limit is reached, expired items are deleted first, and then the coldest data is evicted based on the most recent usage time; `ttl_seconds = 0` may also be evicted. CAS performs atomic comparisons based on the compressed JSON; Omitting `expected` indicates writing only when the key does not exist; explicitly providing `expected: null` only matches JSON `null`. Shared state is preserved across Hook restarts and `/hooks reload`, and is cleared after ElBot restarts; Write to the plugin directory or `_shared/` when persistence is required.

### One-time exec Hook

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

`command` is a non-empty argv array: the first element is the program, and the remaining elements are passed as arguments as-is; Each element renders the template individually and does not go through a shell. When shell syntax is required, explicitly configure it as `["bash", "-lc", "..."]`, `["sh", "-c", "..."]`, or the corresponding platform interpreter. No additional timeout when `timeout_seconds` is `0` or omitted; it cannot be a negative number.

The `cwd` of rules within a plugin defaults to the plugin directory and can only use relative paths within the plugin; Rules for the root `plugins/hooks.toml` are executed in `plugins/` by default, and relative or absolute cwd can be used.

In the `platform.call` of a one-time exec Hook, `platform` can be omitted and will default to the current platform; When explicitly filled, it must equal the current platform. When the adapter returns JSON, the Host places it as-is into response `result`; otherwise, it is treated as a string.

## Worker Hook

Worker Hooks are declared by the plugin's own `hook.toml`. Persistent Workers start after ElBot starts or `/hooks reload`; Transient Workers start when a trigger rule is matched. Omitting `[plugin.runtime]` or `mode` is equivalent to `mode = "once"`, and no Worker will be created.

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

# When there is no need to call Host tools, the entire [plugin.runtime.tools] can be omitted.
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
# The rules of a Worker Hook do not set action/actions; control fields serve as the default value for event.handle.
consume = true
stop_propagation = true
```

### Plugin and Runtime Configuration

`[plugin]`：

| Field | Description |
| --- | --- |
| `name` | Declared name; it is recommended to be consistent with the root `[[plugins]].name`. If inconsistent, the root ID prevails and a warning is logged. |
| `description` | A brief description of `/hooks`, `system.init`, and the status list. |
| `blocked_platform` | Completely stop distributing events from the specified platform, such as `telegram`. |
| `blocked_group` | Completely stop distributing events from the specified group, format `<platform>:<平台原始群 ID>`. |
| `blocked_id` | Completely stop distributing events from the specified user, format `<platform>:<平台原始用户 ID>`. |

If any of the three items are hit, the plugin rule or `event.handle` will not be called; Other plugins are not affected. Exact match, wildcards are not supported; `blocked_group` recognizes both `group` and `supergroup` scopes.

All rules within the plugin (including exec actions), Worker triggers, and waiting routes uniformly use the blocking configuration of `[plugin]`. If `blocked_platform`, `blocked_group`, or `blocked_id` are mistakenly written in `[[rules]]` within the plugin, loading and reloading will return a warning and ignore these rule-level fields, which will not prevent the plugin from running.

`[plugin.runtime]` requires the following fields when `mode = "persistent"` or `mode = "transient"`; `mode` can only be `once`, `persistent`, or `transient`; it defaults to `once` if omitted. `once` does not create a worker and does not require other runtime fields.

| Field | Description |
| --- | --- |
| `mode` | `once` (default), `persistent`, or `transient`. `persistent` remains resident after starting; `transient` only starts after a rule is hit and exits after the Session ends. |
| `command` | A non-empty argv array; the first element is the program, and the remaining elements are passed as arguments as-is, without template rendering or shell processing. |
| `cwd` | Relative to the plugin directory; cannot be an absolute path or escape the plugin directory. Usually `.`. |
| `startup_timeout_seconds` | Maximum seconds to wait for a successful response from `system.init`, must be greater than `0`. |
| `shutdown_timeout_seconds` | Maximum seconds to wait for `system.shutdown` and exit, must be greater than `0`. |
| `event_timeout_seconds` | Maximum seconds for a single `event.handle`, which also limits the tool context for this instance, must be greater than `0`. |
| `max_wait_seconds` | Maximum remaining time that can be declared for a waiting Session, must be greater than `0`. |

`[plugin.runtime.restart]` only applies to `mode = "persistent"`:

| Field | Description |
| --- | --- |
| `strategy` | `never`, `on_failure`, `always`. Currently, `on_failure` and `always` will automatically restart after a non-manual exit. |
| `initial_delay_seconds` | The number of seconds to wait before the first automatic restart, which must be greater than `0`. |
| `max_delay_seconds` | The upper limit for exponential backoff, which must not be less than the initial delay. |

`[plugin.runtime.tools]` can be omitted; omitting it is equivalent to an empty allowlist:

| Field | Description |
| --- | --- |
| `allow` | Tool names available to the foreground `tool.call`; the Host delivers the corresponding schema in `system.init.params.tools`. |
| `background_allow` | Tool names available to the background, which must also appear in `allow`. |

Worker trigger rule reuses the matching, role, priority, `wakeup`, `consume`, and `stop_propagation` of the rule Hook; Configuring `action` or `actions` will result in a validation failure. When `pass_through` is omitted, the two control fields in the rule are used.

Worker status is `starting`, `ready`, `running`, `degraded`, `stopping`, `stopped`, or `failed`. When stopping, the Host first requests `system.shutdown`, and the process is forcibly terminated only after the shutdown timeout is exceeded.

### Waiting, Concurrency, and Cancellation

Return `status = "waiting"`, `conversation_id`, and `expires_at` when continuing to collect input is required; `expires_at` must be later than the current time, and the remaining time must not exceed `max_wait_seconds`. Return `completed` or omit status upon completion.

The same `platform + scope_id + actor.id` is executed serially within the same worker, while different routes can be executed in parallel. The waiting lease also uses this triplet as the key:

- In normal serial distribution, the first worker to return `waiting` holds the route, and subsequent worker trigger rules will no longer distribute this route. Ensuring that only one lease is stored per route; If concurrent events cause multiple workers to compete simultaneously, the last writer overrides the previous lease. Therefore, when multiple worker Hooks may match the same route, `priority`, stricter conditions, or `stop_propagation` should be used to clarify ownership.
- When waiting exists, ordinary `platform.message.received` rules will still run first; worker trigger rules will not be triggered again.
- The Host first routes subsequent messages to the Hook holding the lease via `continuation = true`; When the plugin returns `pass_through = true`, propagation continues from subsequent rules, and can finally enter a command or the main LLM.
- After the continuation is released, the plugin that just finished waiting will not be re-triggered by the same message.
- After the continuation returns `completed` or omits `status`, the Host immediately releases the lease; For transient workers, the process is closed simultaneously. Returning `waiting` again will renew it with a new ID and expiration time.
- Blocking strategy hits, worker exits, stops, or reloads will clear the plugin lease.

When the user sends the exact `/cancel`, the Host cancels the current execution or waiting Session of the route and closes the Transient Worker; Persistent Worker only receives cancellation notifications and retains the process and memory.

```json
{"type":"event","method":"event.cancel","params":{"conversation_id":"weather-42"}}
```

### Tool Call

Worker Hook maintains the LLM loop, business state, and tool usage decisions on its own. The Host only validates the allowlist, foreground/background capabilities, context ownership, count, timeout, cancellation, and send target for `tool.call`; **Does not execute ElBot's risk grading, permission policies, or interaction confirmations**. Plugins must handle tool authorization and risk control on their own.

Foreground call:

```json
{"type":"request","id":"plugin:tool-1","method":"tool.call","params":{"name":"web_search","arguments":{"query":"上海天气"},"tool_context":"ctx:..."}}
```

`arguments` is a JSON value, which can be `{}` when there are no parameters. The `tool_context` of a single `event.handle` can be called up to 32 times, and becomes invalid after `event_timeout_seconds` expires, is cancelled, or the worker is reloaded.

Background calls must be located in `background_allow`: when there is a context issued by the Host, place it in `origin`; When there is no valid `origin`, an explicit `target` must be provided. When there is no origin in the background, the calling entity is `hook:<id>`, and plugins should handle authorization with particular caution.

`result` of a successful response:

```json
{"content":"...","segments":[],"warnings":[],"receipts":[{"PlatformMessageIDs":["123"]}]}
```

`content` is the tool text result, `segments` is a `{type,text,url,mime_type,name}` array, `warnings` is a string array, and `receipts` is the platform message ID after the tool outputs are sent via the Output Manager. Use the outer `ok=false,error` upon failure. Hook tool calls will not be written to the Agent Session.

### Plugin Self-Reload

After a worker modifies its own `hook.toml`, it can request to reload itself:

```json
{"type":"request","id":"plugin:reload-1","method":"hooks.reload","params":{}}
```

Do not synchronously wait for the response within the protocol loop that is the sole reader of stdin; otherwise, the Host's write-back will have no reader, leading to a deadlock. The continuous read loop should distribute responses by request ID, or requests should be sent from event worker threads.

The Host will first fully read and validate the candidate configuration. If it fails, it returns `ok=false`, and the old rules and processes remain unchanged; if it succeeds, it first returns:

```json
{"type":"response","id":"plugin:reload-1","ok":true,"result":{"scheduled":true}}
```

The actual replacement occurs after the current `event.handle` ends: only the rules and workers that call the plugin are replaced, the waiting routes and tool contexts of that plugin are cleared, and other plugins are not restarted. The caller's identity is determined by the process, and it cannot reload other plugins; Reload requests cannot be made during `starting`, `stopping`, or `stopped`. Plugin references in the root `plugins/hooks.toml`, `enabled`, `path`, as well as the addition or removal of plugins, still require an administrator to perform a global `/hooks reload`.

## Event and Template Fields

`event` top-level contains `id`, `point`, `time`, `metadata`, `control`, `platform`, `actor`, `session`, `request`, `message`, `llm`, and `tool`; There may also be `outputs` and `error`. Fields unrelated to the current Hook point will be zero values, empty objects, or omitted; plugins must handle them as nullable values.

| Object | Field |
| --- | --- |
| `control` | `consume`、`stop_propagation`。 |
| `platform` | `name`、`scope_id`、`user_id`、`conversation_id`、`message_id`、`reply_to_message_id`。 |
| `actor` | `id`（`<platform>:<id>`）、`user_id`、`role`、`group_role`、`nickname`、`group_card`、`display_name`。 `display_name` is a pure display name; Group chats usually prioritize group profiles, otherwise nicknames are used. |
| `session` | `id`, `mode`, `title`, `status`; some Hook points only provide `id`. |
| `request` | `id`, `kind`, `session_id`, `phase`; currently all may be empty. |
| `error` | `message`; only reliable for error events. |

`message`：

| Field | Description |
| --- | --- |
| `id`、`role` | Message ID and `user` / `assistant` / `system` / `tool` roles. |
| `platform_text` | Original platform text, may be empty. |
| `platform_message` | Platform-native message JSON, the structure of which is determined by the platform; currently, QQ OneBot provides the raw `message` value, while other platforms may omit it. |
| `intent_text` | User intent after removing the wake-up prefix; message input Hooks should generally prioritize using it. |
| `segments` | `{type,text,url,mime_type,name}` array, with type as `text` / `image` / `file`. |
| `reply` | Quoted message: `message_id`, `sender_id`, `text`, `display_text`, `segments`. |
| `messages` | LLM message array; non-LLM context is usually empty. |

### Reading User Input

`segments` retains the original content after platform normalization; when processing text commands, priority is given to reading `intent_text` with the wake-up prefix removed, and text segments are concatenated if it is empty:

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

The first call where the trigger rule has already matched usually does not require repeated validation of the same command; Waiting continuation only captures the same platform, scope, and actor. Business parameters, authorization, and plugin permissions are still validated by the plugin.

`llm` contains `provider`, `model`, `messages`, `tools`, `usage`, `source_text`, `text`, `tool_calls`, `elapsed_ms`. Among them, nested `messages`, `tool_calls`, `usage` follow the JSON names exported by Go: `Role/Segments/Name/ToolCallID/ToolCalls`, `ID/Name/Arguments`, `PromptTokens/CompletionTokens/TotalTokens/CacheHitTokens`.

`tool` contains `id`, `name`, `arguments` (JSON string), `risk`, `result`, `error`. The prepared stage primarily provides `id/name/arguments`; The completed stage also provides `risk/result/error`, and the full content of the corresponding tool message is located in `message.segments`.

Text fields that can be used in `if`, `match.field`, and templates:

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

Among these, `message.text`, `message.display_text`, `llm.latest_user_text`, and `llm.latest_user_display_text` are virtual text fields calculated during matching/templating and do not exist directly in the event JSON; The current message content is read from `message.segments`, while `llm.messages` is only used to observe the full request context.

The template syntax is `{{...}}`, for example `{{platform.name}}`, `{{message.text}}`, and `{{llm.text}}`. Regex captures can be used as:

```text
{{match.regex.<regex条件序号>.group.<分组序号>}}
{{match.regex.<regex条件序号>.<命名分组>}}
```

Results from preceding tools / exec actions can be used via `{{actions.<name>.result}}` and `{{actions.<name>.error}}`. Known fields are rendered as empty strings when they have no value in the current event; Unknown templates, unknown action names, or non-existent capture paths will be kept as-is, making it easy to use them as plain text.
