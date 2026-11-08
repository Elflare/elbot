<!-- This file is auto-translated from docs/hooks.md. Do not edit manually. -->

# Hook

## Terminology and Selection

| Name | Configuration & Lifecycle | Use Case |
| --- | --- | --- |
| **Rule Hook** | TOML `[[rules]]`; executes actions after matching. | Simple logic such as modifying text or sending content. |
| **One-time exec Hook** | `type = "exec"` in rules; starts an external program upon each hit and exits after processing the current event. | Independent scripts or logic that does not require saving process state. |
| **Transient Worker** | `[plugin.runtime] mode = "transient"`; starts after hitting a trigger rule and exits after the waiting Session ends. | Short-term stateful, multi-turn interactions. |
| **Persistent Worker** | `[plugin.runtime] mode = "persistent"`; starts and remains resident with ElBot. | Long-term state, background tasks, frequent events. |

- Transient + Persistent are collectively called **Worker Hooks**; one-time exec + Worker are collectively called **Process Hooks**.
- `mode = "once"` does not create a Worker and still runs as a Rule Hook; a one-time exec Hook is only started if the rule is configured with `type = "exec"`.
- All entry points that produce outputs use the same set of segments.

See [Hook Showcase](https://github.com/Elfreese/elbot-showcase/blob/main/hooks/README.md) for examples.

## Configuration Location

The `plugins/hooks.toml` in the configuration directory is the entry point for rules. It can directly contain `[[rules]]` or reference plugin directories:

```toml
[[plugins]]
name = "weather"
# Reads plugins/weather/hook.toml by default
```

| Field | Required | Description |
| --- | --- | --- |
| `name` | Yes | Plugin identifier and default directory name; Worker Hooks also use it as the worker ID. Worker IDs can only contain lowercase letters, numbers, `-`, and `_`; it is recommended that rule plugins also follow this. |
| `enabled` | No | Whether to load, defaults to `true`. After modifying the root configuration, an administrator must execute the global `/hooks reload` command. |
| `path` | No | Configuration path relative to `plugins/`, defaults to `<name>/hook.toml`; cannot be an absolute path or escape the `plugins/` directory. |

- Plugin source code, `hook.toml`, and private state files are placed in `plugins/<id>/`; `plugins/_shared/` is where shared files for all Hooks are located; it is not a plugin directory and will not be scanned.
- If the root `hooks.toml` fails to parse, the current rule configuration loading will fail; if a single referenced plugin configuration is incorrect, only that plugin will be skipped, and others will load as usual. TOML uses strict field validation; unknown fields will cause an error.
- Root rules with an empty `name` will be automatically named `rule.<index>`, and duplicates will have an index appended; do not rely on this compatibility behavior.
- Blocking scope: Rules written directly in the root `hooks.toml` are configured individually; for independent plugins, configure once in the `[plugin]` section of `hook.toml`, which applies to all rules and Workers within that plugin. If these three fields are incorrectly written in plugin rules, a warning will be issued, but the plugin will still load normally and the fields will be ignored.

## Rule Hook

Ordinary rules use `[[rules]]`. After rules are filtered by `on`, matching conditions, roles, and priority, the actions are executed in sequence.

### Minimum Example

```toml
[[rules]]
name = "hello_reply"
on = "platform.message.received"
blocked_platform = ["telegram"]
blocked_group.qqonebot = ["123456"]
blocked_id.qqonebot = ["10001"]
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
| `name` | No | Rule display name; explicit filling is recommended. |
| `description` | No | Description used in `/hooks` list and details. |
| `on` | Yes | Hook point, see table below. |
| `priority` | No | The smaller the number, the earlier it executes; default `1000`, `0` is also treated as the default value. Rules with the same priority are executed stably according to the loading order: root rules, `[[plugins]]` declaration order, and rule order within each plugin. |
| `enabled` | No | Whether to load, default `true`. |
| `wakeup` | No | Wake-up strategy: `required` only processes awakened messages (default), `any` processes regardless of wake-up status, `forbidden` only processes non-awakened messages. Mainly used for `platform.message.received`. |
| `blocked_platform` | No | Used only for direct rules in the root `hooks.toml`; skips specified platforms. |
| `blocked_group` | No | Used only for direct rules in the root `hooks.toml`; skips specified groups, grouped by platform, e.g., `blocked_group.qqonebot = ["123456"]`. |
| `blocked_id` | No | Used only for direct rules in the root `hooks.toml`; skips specified users, grouped by platform, e.g., `blocked_id.qqonebot = ["10001"]`. |
| `consume` | No | In `platform.message.received`, if `true`, the process will not enter commands or the main LLM after sending current outputs. |
| `stop_propagation` | No | If `true`, stops rules following the current Hook point; does not stop the Agent's main process. |

- `wakeup = "any"` / `"forbidden"` allows Hooks to observe non-awakened messages, but will not cause the main LLM to process them automatically; `forbidden` skips awakened messages directly, without affecting subsequent plugins, commands, or the main LLM.
- Blocking checks for root rules occur before ordinary matching and action execution; once hit, the exec process will not start, and other rules are not affected.
- All three blocking fields use exact matching and do not support wildcards; `blocked_group` matches `chat_type = "group"` (Telegram supergroups are also categorized as groups), and IDs are matched as raw strings.

### Hook Points

| Hook Point | Timing | Main Payload | Editable Fields | Outputs will be sent |
| --- | --- | --- | --- | --- |
| `platform.connected` | Platform connection completed | `platform` | None | Yes |
| `platform.message.received` | User message received, before command and LLM | `platform`, `actor`, `message` | `message.text/segments` | Yes |
| `agent.input.prepared` | Before input is written to Session | `session`, `message` | `message.text/segments` | No |
| `llm.turn.prepared` | Before the entire LLM turn call | `session`, current user `message.segments`, read-only `llm.messages`, tools/provider/model | `message.segments` or `llm.latest_user_text` | No |
| `llm.request.prepared` | Before each actual model request | Same as above; only pending `message` newly arrived in tool flow | pending `message.segments` or `llm.latest_user_text` | No |
| `llm.response.received` | Model response completed | `llm.text/source_text/tool_calls/usage` | `llm.text` | Yes |
| `tool.call.prepared` | Before tool call | `session`, `tool` | `tool.arguments` | No |
| `tool.call.completed` | After actual tool execution | `session`, `message.segments`, `tool.name/result/error/risk` | `tool.result` or process response `message.segments` | No |
| `agent.output.prepared` | Before each segment of assistant output is sent | `message` | assistant `message.text` | No |
| `agent.turn.output.prepared` | Before the final assistant output of the entire turn is sent | `message` | assistant `message.text` | No |
| `platform.message.sent` | Notification after assistant chat or preview is successfully sent | `message` | None | No |
| `error.occurred` | Notification when an error occurs during Hook/Agent stages | Original event context, `error.message` | None | No |

- `send` action can create an output intent at all Hook points, but only points where "Outputs will be sent" is "Yes" in the table will consume it.
- `timing = "after_assistant"`: currently only the output of `llm.response.received` will actually be delayed; other points still send immediately.
- `llm.tools` is a read-only business tool definition array, each item containing `name`, `description`, `parameters`, and does not include the tool wrapper of a specific API; protocol request encoding is handled by the client.
- Final assistant text usually goes through `agent.turn.output.prepared` first, then `agent.output.prepared`. Do not configure the same append/prepend at both points, otherwise it will be processed twice.
- Tool hints are first prefixed with `[tool]` individually for each tool and then merged into a single notification, which overall goes through `agent.output.prepared` once. The Hook receives the full text with the prefix and can modify the prefix and body; the text returned by the Hook is used directly when sending.

### Matching Conditions and Roles

Conditions can be written as a single condition shorthand or as an AND array; the two cannot be mixed:

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

- `always = true` indicates unconditional matching and cannot be mixed with `if/op/value` or `match`.

| `op` | Field Requirements | Behavior |
| --- | --- | --- |
| `always` | Do not write `field/value` | Always matches. |
| `exists` | Write only `field` | Matches only if the text value of the field is not empty; empty strings and fields not provided by the current event do not match. |
| `contains` | `field/value` | Substring match. |
| `fullmatch` | `field/value` | The entire field is strictly equal to the value. |
| `startswith` / `endswith` | `field/value` | Prefix or suffix match. |
| `regex` | `field/value` | First substring hit of Go RE2 regex. |

- `regex` is not a full-field match; use `^` and `$` in the regex when full-field constraints are needed.
- In captures, `group.0` is the full hit, and subsequent groups start from `group.1`; the index for multiple regex conditions starts from `0`, counting only the regex conditions themselves.

Role fields:

```toml
roles = ["user", "admin"]
actor_roles = ["superadmin"]
group_roles = ["owner", "admin", "member", "unknown"]
```

Items within the same array are ORed; `roles`, `actor_roles`, and `group_roles` are ANDed. `roles` can mix Actor roles (`user`, `superadmin`) and group roles.

### action Syntax and Execution Order

Choose one form for each rule: `action = "..."` single inline, `actions = [...]` array, or `[[rules.actions]]` array of tables.

```toml
# Single inline action
action = "append"
field = "llm.text"
text = "\n处理完成。"

# actions array, executed in array order
actions = [
  { type = "tool", action_name = "search", tool = "web_search", arguments = '{"query":"{{message.text}}"}' },
  { type = "send", text = "{{actions.search.result}}" },
]

# [[rules.actions]] array of tables, executed in declaration order
# [[rules.actions]]
# action_name = "search"
# type = "tool"
# tool = "web_search"
# arguments = '{"query":"{{message.text}}"}'
```

- `action = "..."` cannot be mixed with `actions`.
- Both `actions = [...]` and `[[rules.actions]]` use `type` and `arguments`; `action_name` is available in both action forms.
- The parameter field for inline `action = "tool"` is also `arguments`, not `args`.

| Type | Main Fields | Effect |
| --- | --- | --- |
| `prepend` / `append` | `field`, `text` | Add content before or after editable text. |
| `replace` | `field`, `pattern`, `replace`, `all` | Regex replacement; when `all = false`, only the first match is replaced. |
| `delete` | `field`, `pattern` or `text` | Delete all regex matches. |
| `send` | Single output field or `outputs` | Append output intent. |
| `tool` | `tool`, `arguments`, optional `action_name` | Call a registered tool. |
| `exec` | `command`, `cwd`, `timeout_seconds`, `field`, `timing`, optional `action_name` | Start a one-time `hook.v2` process. |

Named `tool` / `exec` actions can be referenced by subsequent actions: `{{actions.<name>.result}}`, `{{actions.<name>.error}}`.

Unnamed `tool` actions use the tool name as the result name; unnamed `exec` actions use `exec`. Multiple unnamed execs within the same rule will overwrite the same result; they should be explicitly named when template referencing is required.

### Text action

Editable fields strictly depend on the Hook point (see the table above); other fields cannot be edited even if they are matchable or usable as templates.

For `message.text` and `llm.latest_user_text`:

- `prepend` / `append` modifies the first or last text segment; if no text segment exists, one is added; images and files are preserved.
- `replace` / `delete` only process text segments; images and files are preserved.
- The `message.text` overwrite of the exec response only replaces the text segment; original image and file segments are preserved.
- Process Hooks can directly return `message.segments` to replace the full content; an explicit empty array indicates clearing the content; When the same response returns both `message.text` and `message.segments`, `message.segments` takes precedence.

### Message Content Modification

`message.segments` always points to the **new message** explicitly bound to the current Hook point, and does not represent the full history:

- `platform.message.received`, `agent.input.prepared`, `llm.turn.prepared`: bound to the initial user message of this turn.
- `llm.request.prepared`: Binds to the pending only when a new pending exists before the next request in the tool flow; No editable message is provided when there is no pending; the initial input of the current turn cannot be modified again.
- `tool.call.completed`: Binds to the tool result generated after actually entering tool execution; not triggered by pre-check failure, confirmation rejection, or failure before startup.

`llm.messages` is the business work context (system, history, current message, and tool messages), which is read-only for ordinary Hooks—Chat uses it to organize requests, and Responses continue the chain using native records, sending only new inputs and tool results. Hooks cannot modify system or history messages.

`llm.latest_user_text` is a compatibility view of the text part of the currently bound user message, and no longer represents the last `role=user` in the history; it is empty when no message is bound.

- Modifications by user or pending Hooks are written to the Session history before the model request; modifications by tool completion Hooks are also written to the transcript.
- Plain text only saves `content`; when non-text content such as images is included, complete segments are saved additionally.
- `message.platform_text` always retains the original platform text and is not modified by segments.

Example: The original platform message is "This is a dog". The process Hook uses the same response to change the content seen and persisted by the model to "This is a cat" with an attached image, while `message.platform_text` remains "This is a dog":

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

- Images can be provided via HTTP(S) `url`, `path` relative to the plugin directory, or `base64` up to 10 MiB; path/base64 will be normalized into a data URL readable by the model.
- `message.segments` modifies the LLM input and Session history; `outputs` creates an output intent to be sent to the platform; the two are not interchangeable.
- In tool completion events, `message.role` is `tool`, `message.segments` is the original multimodal result of the tool, and `tool.name` and `tool.id` identify the tool and the current call; `tool.id` and `tool.name` in tool preparation events are read-only.
- The `tool.arguments` of Chat can be rewritten; the actual parameters will be written back to the call header before the tool is executed and used for execution and subsequent requests.
- The call set, ID, name, and parameters returned by the Responses model are all read-only (the same applies to Go Handlers, rules, and process Hooks); Rewriting will cause an error and stop the current Hook; if tool preparation fails, the error result will be returned using the original `call_id`. Input, display text, and tool results can still be rewritten. This restriction ensures consistency between local execution and the function calls saved on the server.

## Unified Output

Rules `send`, `output.send`, one-time exec Hooks, and Worker Hook responses use the same set of outputs segments. `target` and `timing` belong to the **entire group** of outputs, not to a single segment.

### TOML Syntax

A single segment can be written directly in the `send` action (equivalent to a single-element `outputs` array; both cannot appear simultaneously):

```toml
[[rules.actions]]
type = "send"
kind = "reply"
message_id = "{{platform.reply_to_message_id}}"
text = "已收到。"
timing = "immediate"
target.chat_type = "group"
target.id = "123456"
```

For multiple segments, use the `outputs` array:

```toml
[[rules.actions]]
type = "send"
timing = "after_assistant"
outputs = [
  { kind = "text", text = "检测到关键词" },
  { kind = "image", path = "assets/alert.png", name = "alert.png" },
  { kind = "record", path = "assets/alert.mp3", name = "alert.mp3" },
  { kind = "emoticon", emoticon_id = "14", name = "微笑" },
  { kind = "at", user_id = "10001" },
  { kind = "reply", message_id = "456", text = "引用回复" },
]
```

### JSON Syntax

Process Hook responses and `output.send` use the same structure:

```json
{
  "outputs": [{"kind":"reply","message_id":"456","text":"Quoted reply"}],
  "target": {"platform":"qqonebot","chat_type":"group","id":"123"},
  "timing": "immediate"
}
```

| Field | Description |
| --- | --- |
| `kind` | `text`, `image`, `file`, `record`, `emoticon`, `at`, `reply`; defaults to `text`. |
| `text` | Text, media fallback, or reply body. |
| `url` / `path` / `base64` | `image`, `file`, and `record` must and can only choose one source. The full value of any field can be `media:<sha256>`, which is resolved by the Host at the sending boundary; otherwise, `url` accepts HTTP(S), `path` is resolved relative to the plugin directory, and `base64` is up to 10 MiB after decoding. |
| `name` | Display name for images, files, or voice recordings; required non-empty emoticon name for `emoticon`. |
| `mime_type` | MIME type for images, files, or voice recordings. |
| `user_id` | Non-empty platform user ID for `kind = "at"`. |
| `message_id` | Non-empty platform message ID for `kind = "reply"`. |
| `emoticon_id` | Non-empty platform native emoticon or sticker ID for `kind = "emoticon"`; native emoticons do not accept media sources. |

- Native emoticons are sent using `emoticon_id`, and readable content only takes the required `name`; when displayed as plain text, they are uniformly shown as `[Emoticon: Name]`.
- `outputs` is a group of fragments for **a single message**; sending multiple messages requires multiple `send` actions or multiple `output.send` calls.

### target, timing, and paths

The target in both TOML and JSON uses snake_case; if omitted, it inherits from the current event:

```toml
target.platform = "qqonebot"
target.chat_type = "group"
target.id = "123456"
```

- `chat_type` can be `private`, `group`, or `channel`; `id` should be the platform's native ID. CLI uses `private`. The specific available types are determined by the target platform. To send to the superadmin, use `target.platform` and `target.superadmins = true`, and omit the session fields.
- `timing`: `immediate` (default) or `after_assistant`; see the Hook point table for the actual scope of effect.
- Relative paths for media are resolved based on the plugin directory where the rule is declared; rules in the root `plugins/hooks.toml` are relative to `plugins/`.
- Large media should be written to the plugin directory or `_shared/` before returning the `path` or `url`; a single `hook.v2` JSON Lines frame has a maximum size of 16 MiB; do not inline large data.

## Process Hook Programming Interface

Process Hook uses `hook.v2` JSON Lines. one JSON frame per line for stdin and stdout; stdout can only be used to write the protocol; logs should be written to stderr. Host request ID uses `host:*`, Hook request ID uses `plugin:*`, and the response must reuse the request ID.

The Host first sends `system.init`, and then sends `event.handle` upon success. A one-time exec Hook exits after processing a single event; When the request is `/stop`, timed out, or the upstream context is cancelled, the Host will terminate the complete process tree of that exec. Worker Hooks can handle multiple events and receive `system.shutdown`. When waiting for a response after a protocol frame has been fully sent, a normal cancellation will asynchronously attempt to send `event.cancel`, and the Persistent Worker will remain running; If a cancellation, timeout, or write failure occurs during the actual writing process, the Host will close the stdin of that process and terminate its entire process tree to avoid continuing to use an incomplete protocol stream. Waiting for write permissions can also be cancelled, which does not affect calls that already hold write permissions.

### Complete Interaction Example

A complete round-trip for a one-time exec Hook; each line in the code blocks is a JSON Lines frame.

Host writes to Hook stdin:

```jsonl
{"type":"request","id":"host:init","method":"system.init","params":{"version":"hook.v2","runtime":{"plugin_name":"demo","plugin_dir":"C:/elbot/plugins/demo","config_path":"C:/elbot/plugins/demo/hook.toml","rule_name":"weather","cwd":"C:/elbot/plugins/demo"}}}
```

Hook returns a successful response from stdout:

```jsonl
{"type":"response","id":"host:init","ok":true,"result":{}}
```

Subsequently, the Host sends the current event (see [Event and Template Fields](#event-与模板字段) at the end of the document for internal fields of `event`):

```jsonl
{"type":"request","id":"host:event","method":"event.handle","params":{"event":{"id":"evt-123","point":"platform.message.received","platform":{...},"actor":{...},"session":{...},"message":{...}},"match":{},"runtime":{...}}}
```

Hook returns the processing result:

```jsonl
{"type":"response","id":"host:event","ok":true,"result":{"status":"completed","matched":true,"result":"weather accepted","message":{"text":"Query Shanghai weather"},"outputs":[{"kind":"text","text":"Querying..."}],"target":{"platform":"qqonebot","chat_type":"group","id":"123456"},"timing":"immediate","pass_through":false}}
```

A tool completion Hook can directly return text and images:

```jsonl
{"type":"response","id":"host:event","ok":true,"result":{"status":"completed","message":{"segments":[{"type":"text","text":"Screenshot completed"},{"type":"image","path":"result.png","mime_type":"image/png"}]}}}
```

- The image segment must provide exactly one of `url`, `path`, or `base64`. Any source field can be filled with a complete `media:<sha256>`, for example `{"type":"image","url":"media:<sha256>"}`; the Host will normalize it into an internal media reference and will not write the central path or presigned URL into that reference. No independent segment `media` field will be added, nor will `hook.v2` be upgraded.
- The media ID must be a 64-bit lowercase hexadecimal SHA-256; IDs in plain text are not scanned, and mixed sources will be rejected.
- Other sources: `url` accepts absolute HTTP(S) URLs or `data:image/...;base64,...`, and relative `path` is resolved based on the plugin directory; `path`, `base64`, and data URLs have a maximum size of 10 MiB after decoding and are materialized by the host as needed when entering the Agent canonical message.
- Currently, this replacement protocol supports `text` and `image` segments.
- When processing fails, omit `result` and return `{"type":"response","id":"host:event","ok":false,"error":"error message"}`; the `id` of the response must be the same as the request.

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

When a plugin initiates a request, the `id` must start with `plugin:`; it is not part of the method. For example, a Hook requests `platform.call` via stdout:

```jsonl
{"type":"request","id":"plugin:get-message-1","method":"platform.call","params":{"platform":"qqonebot","api":"get_msg","params":{"message_id":"789"}}}
```

The Host returns the same `id` via stdin; the specific structure of the `result` is determined by the platform API:

```jsonl
{"type":"response","id":"plugin:get-message-1","ok":true,"result":{"message_id":789,"raw_message":"Weather Shanghai"}}
```

| method | Available To | `params` and `result` |
| --- | --- | --- |
| `shared.*` | All | Read and write global shared state, see the table below. |
| `platform.call` | One-off | params contains `platform` (optional, defaults to current platform), required `api`, and optional `params` object; result is the platform return value. |
| `output.send` | One-off | params uses the unified output structure; result contains the number of `sent` items and `receipts`. |
| `message.get_reply` | One-off | No parameters; result contains `message_id` and `available`. |
| `message.get` | One-off | Reserved interface; currently always returns `{"available":false}`. |
| `hook.log` | One-off | params serves as the log content; result is `{"ok":true}`. |
| `tool.call` | Worker | Call ElBot tools in the allowlist; see "Tool Calling" for fields. |
| `hooks.reload` | Worker | params is an empty object; reloads the current plugin; see "Plugin Self-Reload" for results. |
| `media.import` | One-off/Worker | Choose one of `url`, `path` (relative path to plugin directory), or `base64`; optional `name` and `mime_type`; returns `media` and metadata. |
| `media.read` | One-off/Worker | `media`; returns `base64` if size is ≤ 1 MiB, otherwise returns a controlled temporary `path`. |
| `media.export` | One-off/Worker | `media`; returns a controlled temporary file `path`. |
| `media.metadata` | One-off/Worker | `media`; returns only safe metadata such as name, MIME, and size. |

- Temporary media references are managed within the Hook runtime lifecycle and are released upon expiration or shutdown; `media.delete` is not provided in the first version.
- Hooks cannot access SQLite, the media root directory, or S3 credentials.
- The `media` parameter in `media.*` is the identifier for the media API and remains unchanged; when using the returned ID for message replacement or output, fill it into one of the existing `url`, `path`, or `base64` fields.

### Shared State

One-time exec Hooks and all Worker Hooks share an in-process JSON KV:

| method | `params` | Success `result` |
| --- | --- | --- |
| `shared.get` | `{"key":"weather/cache"}` | `{"found":true,"value":<any JSON>}`; `found=false` when not existing. |
| `shared.set` | `{"key":"weather/cache","value":<any JSON>,"ttl_seconds":600}` | `{"ok":true}` |
| `shared.delete` | `{"key":"weather/cache"}` | `{"deleted":true/false}` |
| `shared.list` | `{"prefix":"weather/"}` | `{"keys":["weather/cache",...]}`; sorted lexicographically; an empty prefix lists all. |
| `shared.compare_and_swap` | `{"key":"weather/cache","expected":<old JSON>,"value":<new JSON>,"ttl_seconds":600}` | `{"swapped":true/false}` |

- `ttl_seconds` is the idle timeout: defaults to 600 seconds if omitted; positive numbers will reset the timer upon successful `get`, `set`, or CAS; `0` does not expire by time; negative numbers are invalid. `list` and failed CAS do not refresh the timer; expired keys are treated as non-existent.
- Keys must be non-empty after trimming leading and trailing whitespace, with a maximum length of 256 bytes. It is recommended to use prefixes such as `users/<platform>/<id>` or `cache/<name>` to avoid conflicts, but prefixes are not security boundaries.
- Values must be valid JSON, with a maximum single value of 1 MiB after compression; the shared area supports up to 10,000 entries, with a total maximum of 32 MiB for keys and values combined.
- When the limit is reached, expired items are deleted first, then the coldest data is evicted based on the least recently used time; items with `ttl_seconds = 0` may also be evicted.
- CAS performs atomic comparison based on compressed JSON: omitting `expected` means writing only if the key does not exist; explicit `expected: null` only matches JSON `null`.
- Shared state is preserved across Hook restarts and `/hooks reload`, and is cleared after ElBot restarts; write to the plugin directory or `_shared/` for persistence.

### Process Environment

One-time exec, Persistent Worker, and Transient Worker all inherit the ElBot process environment, and are layeredly overridden by the Hook public `plugins/.env` and plugin directory `.env`; The configuration root `.env` will not inject Hooks. For complete priority, `PATH` rules, and effective timing, see [Environment Variables and Process Environment Inheritance](configuration.md#环境变量与进程环境继承).

Hooks do not load interactive Shell initializations such as `.bashrc`, `.profile`, mise/asdf, etc. After modifying the Hook environment file, execute `/hooks reload` to apply the changes, and Workers will be rebuilt based on the new environment.

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

Worker Hooks are declared by the plugin's own `hook.toml`. Persistent Workers start after ElBot boots or `/hooks reload` is executed; Transient Workers start when a trigger rule is matched. Omitting `[plugin.runtime]` or `mode` is equivalent to `mode = "once"`, and no Worker will be created.

```toml
[plugin]
name = "weather"
description = "有状态天气助手"
blocked_platform = []
blocked_group.qqonebot = ["123456"]
blocked_id.qqonebot = ["10001"]

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

# The entire [plugin.runtime.tools] can be omitted when calling Host tools is not required.
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
# Rules for Worker Hooks do not set action/actions; control fields serve as the default values for event.handle.
consume = true
stop_propagation = true
```

### Plugin and Runtime Configuration

`[plugin]`:

| Field | Description |
| --- | --- |
| `name` | Self-descriptive name; recommended to be consistent with the root `[[plugins]].name`. If inconsistent, the root ID prevails and a warning is logged. |
| `description` | Brief introduction used by `/hooks`, `system.init`, and the status list. |
| `blocked_platform` | Completely stops distributing events from specified platforms, e.g., `telegram`. |
| `blocked_group` | Completely stops distributing events from specified groups, grouped by platform, e.g., `blocked_group.qqonebot = ["123456"]`. |
| `blocked_id` | Completely stops distributing events from specified users, grouped by platform, e.g., `blocked_id.qqonebot = ["10001"]`. |

- If any of the three blocking criteria are met, the plugin rule or `event.handle` will not be called; other plugins remain unaffected. Matching is exact and does not support wildcards; `blocked_group` matches `chat_type = "group"` (Telegram supergroups are also categorized as groups), and IDs are matched as raw strings.
- All rules within the plugin (including exec actions), Worker triggers, and waiting routes share the blocking configuration of `[plugin]`. If these three fields are mistakenly written in the plugin's `[[rules]]`, loading and reloading will return a warning and ignore the rule-level fields, but will not prevent the plugin from running.

`[plugin.runtime]` requires the following fields when `mode = "persistent"` or `mode = "transient"`; `mode` can only be `once`, `persistent`, or `transient`, defaulting to `once`. `once` does not create a worker and does not require other runtime fields.

| Field | Description |
| --- | --- |
| `mode` | `once` (default), `persistent`, or `transient`. `persistent` stays resident after startup; `transient` starts only after a rule hit and exits after the Session ends. |
| `command` | Non-empty argv array; the first element is the program, and the remaining elements are passed as arguments as-is, without template rendering or shell execution. |
| `cwd` | Relative to the plugin directory; cannot be an absolute path or escape the plugin directory. Usually `.`. |
| `startup_timeout_seconds` | Maximum seconds to wait for a successful `system.init` response; must be greater than `0`. |
| `shutdown_timeout_seconds` | Maximum seconds to wait for `system.shutdown` and exit; must be greater than `0`. |
| `event_timeout_seconds` | Maximum seconds for a single `event.handle`, which also limits the tool context for this call; must be greater than `0`. |
| `max_wait_seconds` | Maximum remaining time that a waiting Session can declare; must be greater than `0`. |

`[plugin.runtime.restart]` only applies to `mode = "persistent"`:

| Field | Description |
| --- | --- |
| `strategy` | `never`, `on_failure`, `always`. Currently, `on_failure` and `always` will automatically restart after a non-manual exit. |
| `initial_delay_seconds` | Waiting seconds before the first automatic restart; must be greater than `0`. |
| `max_delay_seconds` | Upper limit for exponential backoff; must be no less than the initial delay. |

`[plugin.runtime.tools]` is optional; omitting it is equivalent to an empty allowlist:

| Field | Description |
| --- | --- |
| `allow` | Tool names usable by foreground `tool.call`; the Host distributes the corresponding schema under `system.init.params.tools`. |
| `background_allow` | Tool names usable in the background; must also appear in `allow`. |

- Worker trigger rules reuse the matching, role, priority, `wakeup`, `consume`, and `stop_propagation` of rule Hooks; configuring `action` or `actions` will result in a validation failure. If `pass_through` is omitted, the two control fields in the rule are used.
- Worker states: `starting`, `ready`, `running`, `degraded`, `stopping`, `stopped`, `failed`.
- During shutdown, the Host first requests `system.shutdown`; sending the request, waiting for the response, and graceful exit share a single `shutdown_timeout_seconds` budget, after which it is immediately forcefully killed. There is a separate bounded wait for the actual exit after a forceful kill; a timeout return from the close call does not mean the process has already exited.
- When the Host closes a Hook runtime: it stops accepting new plugin requests and cancellation notifications, revokes waiting timers, cancels pending tool calls, and waits for the process, plugin requests, registered notifications and timed callbacks, reloading, and resource cleanup to actually finish.
- Protocol sending for cancellation notifications and plugin responses is constrained by runtime closure cancellation and `event_timeout_seconds`. Background tool calls are also constrained by runtime closure cancellation; even if a call ignores cancellation, a closure wait timeout will not treat it as having exited.

### waiting, Concurrency, and Cancellation

- When further input collection is required, return `status = "waiting"`, `conversation_id`, and `expires_at`: `expires_at` must be later than the current time, and the remaining time must not exceed `max_wait_seconds`.
- Return `completed` or omit the status upon completion.
- The same platform, Session type, native Session ID, and participant identity are serialized within the same worker; different routes can run in parallel. The waiting lease uses the same structured fields for matching; participant identity is jointly determined by `actor.platform` and `actor.id`, while `actor.name` is not involved in route matching.
- During normal serialized distribution, the first worker to return `waiting` holds the route, and subsequent worker trigger rules will no longer distribute that route. This ensures only one lease is stored per route; if concurrent events cause multiple workers to compete, the last writer overrides the previous lease. Therefore, when multiple worker Hooks may match the same route, `priority`, stricter conditions, or `stop_propagation` should be used to clarify ownership.
- When a waiting state exists, continuation is processed with priority. After the continuation is released, normal `platform.message.received` rules are executed; the corresponding worker trigger rule will not be triggered again.
- The Host first routes subsequent messages to the Hook holding the lease with `continuation = true`; if the plugin returns `pass_through = true`, propagation continues from subsequent rules, eventually potentially entering a command or the main LLM.
- After a continuation is released, the plugin that just ended its waiting state will not be re-triggered by the same message.
- Log results for the continuation and subsequent normal rules are aggregated in execution order; if subsequent rules are not matched or succeed, they will not override the failure or cancellation results of the previous continuation.
- After the continuation returns `completed` or omits the `status`, the Host immediately releases the lease; for transient workers, the process is closed simultaneously. Returning `waiting` again will renew the lease with a new ID and expiration time.
- Waiting expires actively via a one-time timer, without requiring a new message or route query trigger. Upon expiration, the lease is released and the associated Transient Worker is closed, while Persistent Workers retain their processes. Continuations already in progress are constrained by event timeouts; the expiration callback waits for the route execution to finish before re-verifying the lease, and old callbacks will not delete renewed or replaced leases.
- Blocking strategy hits, worker exits, stops, or reloads will clear the plugin's lease.
- When a user sends an exact `/cancel`, the Host cancels the current execution or waiting Session for that route and closes the Transient Worker; Persistent Workers retain their process and memory provided there are no protocol write failures.
- For continuations, `event.cancel` uses the plugin lease's `conversation_id`; for normal events, the platform Session ID is used, without rewriting the original event context.
- Cancellation notifications are sent asynchronously and do not block the return of the original call; if the notification write itself blocks until timeout, the process is terminated as a protocol write failure.

```json
{"type":"event","method":"event.cancel","params":{"conversation_id":"weather-42"}}
```

### Tool Calling

Worker Hooks maintain their own LLM loop, business state, and tool usage decisions. The Host only validates the allowlist, foreground/background capabilities, context ownership, count, timeout, cancellation, and delivery target for `tool.call`; **it does not execute ElBot's risk grading, permission policies, or interactive confirmations**. Plugins must handle tool authorization and risk control themselves.

Foreground calling:

```json
{"type":"request","id":"plugin:tool-1","method":"tool.call","params":{"name":"web_search","arguments":{"query":"Shanghai weather"},"tool_context":"ctx:..."}}
```

`arguments` is a JSON value and can be `{}` when there are no parameters. `tool_context` for a single `event.handle` can be called up to 32 times, and expires after `event_timeout_seconds` is reached, upon cancellation, or after a worker reload.

Use `background = true` for independent calls; the tool must be located in `background_allow`. To inherit an event, place the context issued by the Host into `origin`; if omitted, Actor, chat, Session, or Agent execution information will not be reconstructed. Tools run directly, and results are returned to the plugin via RPC response without triggering the Agent. A delivery target is only required when the tool produces outputs to be sent; pure queries do not require a `target`. Calls with event context use the actual Actor of the event, including role, nickname, and group card.

`result` of a successful response:

```json
{"content":"...","segments":[],"warnings":[],"receipts":[{"PlatformMessageIDs":["123"]}]}
```

`content` is the tool's text result, `segments` is an array of `{type,text,url,mime_type,name}`, `warnings` is a string array, and `receipts` are the platform message IDs after tool outputs are sent via the Output Manager. Failures use the outer `ok=false,error`. Hook tool calls are not written to the Agent Session.

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

- `event` top-level contains `id`, `point`, `time`, `metadata`, `control`, `chat`, `actor`, `session`, `request`, `message`, `llm`, `tool`; it may also contain `outputs` and `error`.
- Fields unrelated to the current Hook point will be zero values, empty objects, or omitted; plugins must handle them as nullable values.
- `segments` preserves the original content after platform normalization; when processing text commands, prioritize reading `intent_text` (which has the wake-up prefix removed); if empty, concatenate text segments.
- For the first call matched by a trigger rule, repeated validation of the same command is usually unnecessary; waiting continuation only captures the same platform, session type, session ID, and participant identity. Business parameters, authorization, and plugin permissions are still validated by the plugin.
- `llm` contains `provider`, `model`, `messages`, `tools`, `usage`, `source_text`, `text`, `tool_calls`, `elapsed_ms`. Among these, the nested `messages`, `tool_calls`, and `usage` follow the JSON names exported by Go: `Role/Segments/Name/ToolCallID/ToolCalls`, `ID/Name/Arguments`, `PromptTokens/CompletionTokens/TotalTokens/CacheHitTokens`.
- `tool` contains `id`, `name`, `arguments` (JSON string), `risk`, `result`, `error`. The prepared phase primarily provides `id/name/arguments`; the completed phase also provides `risk/result/error`, and the full content of the tool message is located in `message.segments`.
- `event.outputs` is a snapshot of outputs accumulated by the event, with array items using JSON field names exported by Go, such as `Kind`, `Text`, `Name`, `EmoticonID`, `Delivery`. `Delivery` contains `Timing`, `HookPoint`, `HookName`, `HookMode`; an empty `Timing` indicates immediate sending, and the source field for non-Hook outputs is empty. When plugins add new outputs, they should still use the lowercase segment fields and group-level `target` and `timing` described in the "Unified Output" section.

| Object | Fields |
| --- | --- |
| `control` | `consume`, `stop_propagation`. |
| `chat` | `platform`, `chat_type`, `id`, `name`, `message_id`, `reply_to_message_id`. `id` is the platform's native chat ID, `name` is the chat display name. |
| `actor` | `platform`, `id`, `name`, `role`, `group_role`, `nickname`, `group_card`. `id` is the native user ID, `name` is the pure display name; for group chats, the group card is usually prioritized, otherwise the nickname is used. |
| `session` | `id`, `mode`, `title`, `status`; some Hook points only provide `id`. |
| `request` | `id`, `kind`, `session_id`, `phase`; currently these may all be empty. |
| `error` | `message`; only reliable for error events. |

Participant identity uses `actor.platform` and `actor.id`, with colons in the ID preserved as is; `name` is not used for identity comparison. Actors called by Elnis use the Elwisp name as the `id` and `name`, and `platform` is empty; authentication tokens and event IDs do not act as participants. Chat and background tasks are separated: when there is no actual chat, `chat` is empty.

`message`:

| Field | Description |
| --- | --- |
| `id`, `role` | Message ID and `user` / `assistant` / `system` / `tool` roles. |
| `platform_text` | Original platform text, may be empty. |
| `platform_message` | Native platform message JSON, structure determined by the platform; currently QQ OneBot provides the original `message` value, other platforms may omit it. |
| `intent_text` | User intent after removing the wake-up prefix; message input Hooks should generally prioritize using this. |
| `segments` | Array of `{type,text,url,mime_type,name}`, where type is `text` / `image` / `file`. |
| `reply` | Quoted message: `message_id`, `sender_id`, `text`, `display_text`, `segments`. |
| `messages` | LLM message array; usually empty for non-LLM contexts. |

### Reading User Input

`segments` preserves the original content after platform normalization; when processing text commands, prioritize reading `intent_text` (which has the wake-up prefix removed), and concatenate text segments only if it is empty:

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

Text fields available for `if`, `match.field`, and templates:

```text
chat.platform / chat.chat_type / chat.id / chat.name / chat.message_id / chat.reply_to_message_id
actor.platform / actor.id / actor.name / actor.role / actor.group_role / actor.nickname / actor.group_card
session.id / session.mode / session.title / session.status
request.id / request.kind / request.session_id / request.phase
message.id / message.text / message.display_text / message.platform_text / message.intent_text / message.role
message.reply.message_id / message.reply.sender_id / message.reply.text / message.reply.display_text
llm.text / llm.source_text / llm.latest_user_text / llm.latest_user_display_text / llm.provider / llm.model
tool.name / tool.arguments / tool.result / tool.risk
error.message
```

Among these, `message.text`, `message.display_text`, `llm.latest_user_text`, and `llm.latest_user_display_text` are virtual text fields calculated during matching/templating and do not exist directly in the event JSON; current message content is read from `message.segments`, while `llm.messages` is used only for observing the full request context.

Templates are written as `{{...}}`, for example `{{chat.platform}}`, `{{message.text}}`, `{{llm.text}}`. Regex captures can be used as:

```text
{{match.regex.<regex_condition_index>.group.<group_index>}}
{{match.regex.<regex_condition_index>.<named_group>}}
```

Results from preceding tool / exec actions can be used via `{{actions.<name>.result}}` and `{{actions.<name>.error}}`. Known fields are rendered as empty strings if they have no value in the current event; unknown templates, unknown action names, or non-existent capture paths are preserved as-is, allowing them to be used as plain text.
