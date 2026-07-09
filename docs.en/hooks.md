<!-- This file is auto-translated from docs/hooks.md. Do not edit manually. -->

# Hook

The Hook Layer is used to extend behavior before and after key processes, for example:

- Agent input processing.
- LLM request preparation.
- LLM response processing.
- Before and after platform transmission.
- Platform connection events.

Hooks can modify messages, append output intents, call low-risk tools, or inject resident memory. Rule Hooks can also execute local scripts.

Important convention: Hooks do not replace the Security Layer; security determinations are still based on the Security Layer. Hooks and plugins do not send platform messages directly; they should return an output intent, which is then handed over by the Agent to the Output Manager for sending.

## Hook Sources

ElBot will register these Hook sources:

| Source | Name | Description |
| --- | --- |
| Rule Hook | `name` in the configuration | Loads declarative rules from `plugins/hooks.toml`, supporting conditional matching, text operations, output sending, tool calls, and script execution. |
| Resident Memory Hook | `builtin.resident_memory` | Inject the resident memory and temporary username of the current platform + actor every round. |
| Cron Hook | `builtin.cron.missed_once` | Re-deliver missed once cron upon platform connection. |

The Emoji Hook has been changed from an embedded plugin to a Rule Hook example; see the [Emoji Extraction Example](#表情提取示例) in this document.

## Rule Hook Configuration

The main configuration for rule Hooks is fixed at `plugins/hooks.toml` in the configuration directory. Plugin rules are referenced via `[[plugins]]` in the main configuration, and `plugins/<plugin-name>/hook.toml` is read by default.

```toml
[[plugins]]
name = "demo"
enabled = true
# path = "demo/hook.toml" # Optional; must be a path relative to plugins/
```

Plugin configuration files can contain `[plugin]` metadata and their own `[[rules]]`. Local relative paths and paths relative to `cwd` in plugin rules are resolved based on the directory where the plugin configuration is located.

```toml
[plugin]
name = "demo"                 # Optional; the actual reference name is based on [[plugins]].name in the main hooks.toml
description = "demo plugin"   # Optional; serves as a fallback description when the rule does not have a description

[[rules]]
name = "demo_rule"
on = "platform.message.received"
always = true
action = "send"
text = "ok"
```

`/hooks` lists the successfully registered rule names, such as `demo_rule`, not the plugin names of `[[plugins]]`. `/hooks reload` will reread the main configuration and plugin configurations; If a plugin is skipped, a warning will be displayed in the command result.

### Rule Structure

```toml
[[rules]]
name = "stable_debug_name"          # Required, used for logs and audit
description = "简短说明"            # Optional, recommended
on = "hook.point"                   # Required, Hook point
enabled = true                      # Optional, defaults to true
priority = 1000                     # Optional, smaller values are executed first
require_wakeup = true               # Optional, defaults to true; false indicates that non-triggered messages can also trigger it
```

### Trigger Requirements

`platform.message.received` rules process only triggered messages by default, for compatibility with legacy behavior. Triggered messages typically include private chats, slash commands, matching trigger words, @mentioning the bot, or replying to the bot's messages.

If you want the Hook to passively listen to ordinary group messages, set:

```toml
require_wakeup = false
```

Example:

```toml
[[rules]]
name = "passive_cat_ping"
on = "platform.message.received"
require_wakeup = false
match = [{ field = "message.text", op = "contains", value = "猫" }]
action = "send"
text = "检测到猫。"
```

The processing order is: first run the `platform.message.received` Hook; send Hook outputs; If the rule sets `consume = true`, the current message ends here and will not enter the command or LLM; If not consumed, then only triggered messages will continue to enter the command or LLM. In other words, `require_wakeup = false` allows the Hook to see untriggered messages, but it will not automatically let the LLM process all messages in the group.

### Condition Matching

Single condition:

```toml
if = "message.text"
op = "contains"
value = "hello"
```

No condition:

```toml
always = true
```

Multiple conditions (AND):

```toml
match = [
  { field = "platform.name", op = "fullmatch", value = "qqonebot" },
  { field = "message.text", op = "contains", value = "猫" },
]
```

`always` cannot be used in combination with `if/op/value` or `match`; `if/op/value` cannot be used in combination with `match`.

### Matching Operations

| op | Description |
| --- | --- |
| `always` | Unconditional match; field or value cannot be set |
| `exists` | Field is not empty |
| `contains` | Field contains value |
| `fullmatch` | Field equals value exactly |
| `startswith` | Field starts with value |
| `endswith` | Field ends with value |
| `regex` | Regular expression match; capture groups can be referenced via template variables |

### Matchable Fields

```
platform.name / scope_id / user_id / conversation_id / message_id / reply_to_message_id
actor.id / user_id / role / group_role / display_name
session.id / mode / title / status
request.id / kind / session_id / phase
message.id / text / display_text / platform_text / intent_text / role
message.reply.message_id / sender_id / text / display_text
llm.text / source_text / latest_user_text / latest_user_display_text / provider / model
tool.name / arguments / result / risk
error.message
```

Field selection quick reference:

| Requirement | Recommended Field | Description |
| --- | --- | --- |
| Matches what the user actually said, ignoring group chat wake-up words or bot mentions | `message.intent_text` | For example, when the user sends `elbot 咩`, `message.intent_text` is `咩` |
| Match the plain text of the current message, preserving the wake-up word | `message.text` | Only concatenate text fragments, excluding image/file placeholders |
| Match the readable content of the current message, preserving the wake-up word | `message.display_text` | text + image/file placeholders, e.g., `[图片: ...]` |
| Match the original platform text | `message.platform_text` | Original text provided by the platform, excluding quote fallback expansion |
| Match the content of the quote/reply | `message.reply.*` | Has a value only when the current message is a reply to someone else |

When automatically replying and blocking subsequent commands/LLM, prioritize using `platform.message.received` + `consume=true`. If a wake-up word is required in the group, use `message.intent_text` for matching:

```toml
[[rules]]
name = "reply_mee"
on = "platform.message.received"
if = "message.intent_text"
op = "fullmatch"
value = "咩"
consume = true

[[rules.actions]]
type = "send"
kind = "text"
text = "咩"
```

### Hook Point

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

### Editable Fields

Different Hook Points allow different editable fields:

| Hook Point | Editable Fields |
| --- | --- |
| `platform.message.received` / `agent.input.prepared` | `message.text` |
| `llm.turn.prepared` / `llm.request.prepared` | `llm.latest_user_text` |
| `llm.response.received` | `llm.text` |
| `tool.call.prepared` | `tool.arguments` |
| `tool.call.completed` | `tool.result` |
| `agent.output.prepared` / `agent.turn.output.prepared` / `platform.message.sent` | assistant `message.text` |

`llm.source_text` can be used for conditional matching, but cannot be edited.

### Action Type

Each rule can contain a single `action` or multiple `actions` (executed in order).

#### Text Operations

```toml
# Single action
action = "replace"
field = "message.text"
pattern = "猫"
replace = "狗"
all = true                 # Optional, replaces only the first occurrence by default

# Multiple actions
actions = [
  { type = "replace", field = "message.text", pattern = "猫", replace = "狗", all = true },
  { type = "append", field = "message.text", text = "!" },
]
```

Text operation types: `prepend`, `append`, `replace`, `delete`. `delete` is equivalent to `replace` being an empty string.

Text operations preserve the positions of multimodal segments such as images and files in the message, modifying only the text segment.

#### send

`send` action generates an output intent, which is sent to the platform by the Output Manager.

Single output (backward compatible):

```toml
action = "send"
kind = "text"              # text/image/file/emoticon/at, default text
text = "检测到关键词"
timing = "after_assistant" # Optional, default immediate
```

Multi-segment output (outputs):

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

The output segment format is unified with the [Elvena Protocol](elnis-usage.md#segments多模态消息段), with additional support for local `path`, `base64`, and `emoticon` types:

| Field | Description |
| --- | --- |
| `kind` | `text` / `image` / `file` / `emoticon` / `at` / `reply`, default `text` |
| `text` | Text content (required for text type, optional as additional text for other types; serves as user ID for at type when `user_id` is not set) |
| `url` | HTTP/HTTPS URL（image/file） |
| `path` | Local file path (image/file/emoticon); Serves as the replied message ID for reply type when `message_id` is not set; Ordinary paths are handled according to the platform's default method; prefixes `base64://`, `file://`, `http://`, and `https://` indicate that the caller has specified the media source, which will be handled directly according to the platform's capabilities. |
| `base64` | base64 encoded data (image/file) |
| `name` | File name or emoticon name |
| `mime_type` | MIME type hint |
| `user_id` | Target user ID for at |
| `message_id` | reply target message ID |

`target` and `timing` are inherited from the action to all segments.

#### tool

Call a registered tool; the result is stored in `{{actions.<name>.result}}` for use by subsequent actions.

```toml
actions = [
  { name = "search", type = "tool", tool = "web_search", arguments = '{"query":"ElBot"}' },
  { type = "append", field = "llm.latest_user_text", text = "\n\nHook 工具结果：{{actions.search.result}}" },
]
```

Tool calls are constrained by the Security Policy: the risk level must be within the range allowed for the current Actor, and high-risk tools requiring interactive confirmation will be rejected.

#### exec

Execute a local script. Scripts in the main configuration use `plugins/` as the default working directory; scripts in plugin configurations use the directory where the plugin configuration is located as the default working directory and cannot escape the plugin directory relative to `cwd`.

`command` will be split by whitespace into an executable program and arguments and then executed directly, without implicitly wrapping it in `sh -c`. For example, `uv run script.py` will directly execute `uv`, and `bash ./script.sh` will directly execute `bash`; When pipes, redirection, `&&`, or other shell syntax are required, please explicitly write `bash -lc "..."`, `sh -c "..."`, or the corresponding interpreter for the platform.

exec uses the `hook.v1` line protocol: after ElBot starts the script, it first writes a line of init JSON to stdin; The script writes one JSON frame per line to stdout, and must finally write a `done` or `error` frame. stderr is not used as protocol data; When the script succeeds, it only goes to the logs; when the script fails, times out, crashes, or has a protocol error, the end of stderr will be merged into the Hook failure notification.

The script should only read the first line of stdin as the init frame; do not read-all, read-to-end, `fread` until EOF, or read in a loop until EOF; stdin is subsequently used for `request`/`response` frames. After the script writes a valid `done` or `error` frame, it should exit with 0; A non-zero exit code will be regarded as a failure of the exec process.

```toml
actions = [
  { name = "script", type = "exec", command = "uv run script.py", timeout_seconds = 30 },
]
```

### exec hook.v1 protocol

The init frame is written to the script's stdin by ElBot:

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

init frame fields:

| Field | Description |
| --- | --- |
| `type` | Fixed as `init` |
| `version` | Fixed as `hook.v1` |
| `event` | Current Hook event context; fields are populated according to the Hook point, and irrelevant fields are empty, zero-valued, or omitted |
| `match` | Current rule matching context; contains `regex` array when regex hits |
| `runtime` | Execution context for this exec run |

`event` field:

| Field | Description |
| --- | --- |
| `id` | Hook event ID |
| `point` | Hook point, e.g., `platform.message.received`, `llm.response.received` |
| `time` | Event time, RFC3339 format |
| `metadata` | Reserved/extension metadata object |
| `control.consume` | Whether to block subsequent slash commands and LLM processing |
| `control.stop_propagation` | Whether to block the execution of subsequent rules at the same Hook point |
| `platform.name` | Platform name, e.g., `qqonebot`, `telegram`, `cli` |
| `platform.scope_id` | Platform Session scope ID; generated by the platform adapter, usually with a scope prefix, e.g., `group:<id>`, `private:<id>` |
| `platform.user_id` | Current platform sender user ID |
| `platform.conversation_id` | Platform Session ID; empty if not provided by the platform |
| `platform.message_id` | Current inbound platform message ID |
| `platform.reply_to_message_id` | Target platform message ID being quoted/replied to by the current message. |
| `actor.id` | ElBot internal Actor ID, usually composed of the platform name and user ID |
| `actor.user_id` | Platform user ID corresponding to the Actor |
| `actor.role` | ElBot internal role: `superadmin` or `user` |
| `actor.group_role` | Group identity: `owner`, `admin`, `member`, `unknown` |
| `actor.display_name` | Display name |
| `session.id` | Current Session ID |
| `session.mode` | Current Session mode, e.g., `chat`, `work`; empty before entering a Session |
| `session.title` | Current Session title |
| `session.status` | Current Session status; empty when there is no Session context |
| `request.id` | Current Request ID |
| `request.kind` | Request type: `turn`, `llm`, `tool`, `hook`, `compress`, `sub_agent`; empty when there are no running Requests |
| `request.session_id` | Session ID associated with the Request |
| `request.phase` | Turn stage: `idle`, `llm`, `tool`, `awaiting_risk_confirm`, `awaiting_append_confirm`, `compact`; Empty when there is no Turn context |
| `message.id` | Current message ID; empty when not set |
| `message.role` | Message role, e.g., `user`, `assistant` |
| `message.text` | Plain text of the current message, only concatenating text fragments, without image/file placeholders |
| `message.display_text` | Readable content text of the current message, which may include image/file placeholders |
| `message.platform_text` | Original current message text from the platform, excluding expanded content from fallback references |
| `message.intent_text` | User input intent text; Configured wake-up keywords and bot mentions are removed from user messages; suitable for matching `咩` in messages like `elbot 咩` within `platform.message.received`. |
| `message.segments` | Current message fragments array; Scripts reading user text should prioritize aggregating fragments of `type=text` from here. In `platform.message.received`, this represents the current inbound message, excluding the text of quoted messages. |
| `message.messages` | Related LLM messages array; populated only at certain Hook points. |
| `message.reply.message_id` | Target platform message ID being quoted/replied to by the current message. |
| `message.reply.sender_id` | Sender ID of the quoted message; empty if not provided by the platform. |
| `message.reply.text` | Plain text content of the quoted message; empty if there is no text |
| `message.reply.display_text` | Readable content text of the quoted message, which may contain image/file placeholders |
| `llm.provider` | LLM provider name |
| `llm.model` | LLM model name |
| `llm.messages` | The LLM message array for this request |
| `llm.tools` | The array of available tool schemas for this request |
| `llm.usage` | LLM usage statistics; empty if not provided |
| `llm.source_text` | Raw LLM response text; can be matched, but cannot be edited |
| `llm.text` | Currently visible/editable LLM text |
| `llm.tool_calls` | Tool call array returned by the LLM |
| `llm.elapsed_ms` | LLM call duration in milliseconds |
| `tool.id` | Tool call ID |
| `tool.name` | Tool name |
| `tool.arguments` | Tool parameters JSON string |
| `tool.risk` | Tool risk level |
| `tool.result` | Tool result text |
| `tool.error` | Tool error; usually used only in error contexts |
| `outputs` | Array of accumulated output intents for the current event |
| `error.message` | Current error text; only related to error Hooks |

Common fragment fields used by `message.segments`, `llm.messages[].segments`, the `outputs` field of stdout `output` frame, `params.outputs` of `request output.send`, and `outputs` of TOML send action:

| Field | Description |
| --- | --- |
| `type` / `kind` | Fragment type; common for inbound messages are `text`, `image`, `file`, and output also supports `emoticon`, `at`, `reply` |
| `text` | Text content or additional text |
| `url` | HTTP/HTTPS resource URL |
| `path` | Local resource path; resolved relative to `plugins/` or the plugin directory during output |
| `base64` | base64 encoded data; used only for output fragments, maximum 10 MiB after decoding |
| `name` | File name or emoticon name |
| `mime_type` | MIME type hint |
| `user_id` | `at` Target user ID for output |
| `message_id` | `reply` Target platform message ID for output |

Note: `message.text`, `message.display_text`, `llm.latest_user_text`, etc., are derived fields in rule matching and template variables, not fields with the same name in the init JSON. The exec script needs to read raw data from `event.message.segments`, `event.message.platform_text`, `event.message.reply`, or `event.llm.messages[].segments`.

`match.regex[]` field:

| Field | Description |
| --- | --- |
| `field` | Field name for regex matching |
| `value` | Regular expression |
| `text` | Matched text |
| `groups` | Capture group array; `groups[0]` is the full match |
| `named` | Named capture group object |
| `start` / `end` | Hit range |

`runtime` field:

| Field | Description |
| --- | --- |
| `plugin_name` | Plugin name; the rule in the main `plugins/hooks.toml` is empty |
| `plugin_dir` | Plugin directory; the main rule is empty |
| `config_path` | Current rule configuration file path |
| `rule_name` | Final name of the current rule |
| `cwd` | Working directory of the exec process |

stdout frames that the script can write:

| type | Description |
| --- | --- |
| `output` | Queue output intent for this Hook; the frame must contain the `outputs` field, whose value is an array of output segment objects |
| `request` | Call ElBot capabilities; when `id` is provided, ElBot will write a `response` frame to stdin |
| `done` | Normal termination; can include `matched=false` to indicate that this rule is not effective and to roll back previous action effects |
| `error` | Failure termination, with the `error` or `message` field serving as the error text |

Example of stdout frame structure:

```json
{"type":"output","outputs":[{"kind":"text","text":"内容"}]}
{"type":"output","outputs":[{"kind":"image","path":"images/a.png","text":"附加说明"}]}
{"type":"request","id":"send-1","method":"output.send","params":{"outputs":[{"kind":"text","text":"立即发送"}]}}
{"type":"done","result":"ok","message":{"text":"改写后的文本"}}
{"type":"error","error":"失败原因"}
```

`output` frame only uses the `outputs` field; Do not write `{"type":"output","output":{...}}` or `{"type":"output","segments":[...]}`. When multiple output segments are needed, place multiple output segments in the same `outputs` array; Alternatively, multiple lines of `output` frames can be written. TOML send action also uses `outputs = [...]`.

A single stdout frame is maximum 16 MiB. Large media such as images should not be put directly into `base64`; it is recommended to first write to the plugin directory or a temporary file, and then return using `path`; Alternatively, `url` can be returned.

`output` frame field:

| Field | Description |
| --- | --- |
| `type` | Fixed as `output` |
| `id` | Optional; once set, ElBot will return `response`, and `result.queued=true` upon success |
| `outputs` | Required, an array of output segment objects |

`request` frame field:

| Field | Description |
| --- | --- |
| `type` | Fixed as `request` |
| `id` | Optional but strongly recommended; once set, ElBot will write the `response` frame to stdin |
| `method` | Request method, see the method table below |
| `params` | Request parameters object; structure varies by method |

`request` without `id` will not receive `response`; however, if the request fails, the current exec action will still fail and trigger a Hook failure notification.

`done` optional fields:

| Field | Description |
| --- | --- |
| `matched` | Default is `true`; when it is `false`, the entire rule is considered a miss, and subsequent actions will no longer be executed |
| `result` | Save to `{{actions.<name>.result}}` |
| `error` | Save to `{{actions.<name>.error}}` |
| `message.text` | Overwrite the `field` of the action; overwrite `message.text` when `field` is not set |
| `consume` | Set the consume control bit of this event |
| `stop_propagation` | Set the stop_propagation control bit of this event |

`error` frame field:

| Field | Description |
| --- | --- |
| `type` | Fixed as `error` |
| `error` / `message` | Failure text; `error` takes precedence |

Supported request methods:

| method | params | Description |
| --- | --- | --- |
| `platform.call` | `platform`、`api`、`params` | Call the current platform's original API; cannot be called across platforms |
| `output.send` | `outputs` | Immediately send the output segment array and return a receipt; requires the app-layer sender to be available |
| `message.get_reply` | None | Return the target message ID that the current message references/replies to |
| `message.get` | Reserved | Currently returns `available=false` |
| `hook.log` | Any JSON | Write to Hook plugin log |

The `response` frame fields written back to stdin by ElBot:

| Field | Description |
| --- | --- |
| `type` | Fixed as `response` |
| `id` | Corresponding `id` of the request/output frame |
| `ok` | `true` indicates success, `false` indicates failure |
| `result` | Success result; exists only when `ok=true` |
| `error` | Failure text; exists only when `ok=false` |

When requests such as `platform.call` and `output.send` fail, the script will first receive a response from `ok=false`; Subsequently, the current exec action will also fail and trigger a Hook failure notification.

Minimal Python example:

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

### Template Variables

Text fields and exec commands support `{{...}}` template rendering:

```
{{platform.name}}          {{platform.scope_id}}      {{platform.user_id}}
{{platform.message_id}}    {{platform.reply_to_message_id}}
{{actor.id}}               {{actor.user_id}}          {{actor.role}}
{{message.text}}           {{message.display_text}}   {{message.platform_text}}
{{message.reply.message_id}} {{message.reply.text}}    {{message.reply.display_text}}
{{llm.text}}               {{llm.source_text}}        {{llm.latest_user_text}}
{{tool.arguments}}         {{tool.result}}
{{actions.<name>.result}}  {{actions.<name>.error}}
```

Capture groups from regex matches can be referenced via `{{match.regex.0.group.1}}` or named groups `{{match.regex.0.<name>}}`.

### Role Partitioning

Rules can use `roles`, `actor_roles`, and `group_roles` for permission partitioning:

- `superadmin` / `user`: ElBot internal security roles.
- `owner` / `admin` / `member`: platform group identities, mapped by the platform adapter.

`roles` matches both internal roles and group identities; `actor_roles` only matches internal roles; `group_roles` only matches group identities.

```toml
[[rules]]
name = "admin_only_rule"
on = "platform.message.received"
roles = ["admin"]
always = true
action = "send"
text = "仅管理员可见"
```

### Control Fields

```toml
consume = true              # Block subsequent slash commands and LLM processing
stop_propagation = true      # Block subsequent rules at the same Hook point from continuing execution
```

`consume = true` is typically used for `platform.message.received` Hooks: it blocks subsequent commands and LLM processing after sending output, allowing the Hook to take full control of the message.

## Hook exec platform call

The `exec` action of a rule Hook can call the current platform's original API via the `request` frame. Platform calls will be entered into the audit log, and only the platform to which the current event belongs can be called; it is not possible to cross from one platform Hook to another platform.

```json
{"type":"request","id":"call-1","method":"platform.call","params":{"api":"delete_msg","params":{"message_id":"123"}}}
```

ElBot will write the response frame to the script's stdin:

```json
{"type":"response","id":"call-1","ok":true,"result":{}}
```

## Example of Recalling a Quoted Message

The following example is used when a superadmin replies to a platform message and sends "recall", calling the platform API via Elvena v3 `calls` to recall the quoted message.

Prerequisite: The platform adapter must support raw API calls, and the current message must have a quote/reply relationship. In the Hook, the platform message ID of the quoted message can be read via `message.reply.message_id` or `platform.reply_to_message_id`, and the message ID that triggered the current Hook can be read via `platform.message_id`. The `message.text` in `platform.message.received` only represents the text sent by the current user; The quoted content is in `message.reply.*` and will not pollute `message.text`.

Telegram uses Bot API `deleteMessage`; the bot must have permission to delete the target message, and the target message must not exceed the deletion time limit allowed by the platform.

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

The script reads the init frame from stdin, writes the `platform.call` request frame to stdout, then reads the response frame, and finally writes the done frame.

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

## Emoticon Extraction Example

The following example uses a rule Hook + exec script to implement the emoticon extraction function, replacing the old embedded emoticon Hook.

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

The script reads the init frame from stdin, extracts `[[token]]`, and checks if there are images in the `emoticons/<token>/` directory; if so, it outputs an emoticon frame and removes the token from the text; otherwise, it keeps it as is.

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

### Configuration Description

- `root_dir` (the configuration item for the old embedded plugin) is no longer needed; the directory path relative to `plugins/` is written directly in the script.
- `timing` controls the timing of emoticon delivery: `immediate` (default) sends before the LLM text output, and `after_assistant` sends after the assistant's reply.
- `field = "llm.text"` allows the `text` returned by the script to overwrite the LLM response text, removing the extracted tokens.
- When `field` is not set, the script only produces outputs without modifying the original text (suitable for scenarios such as "send a notification when content is detected but do not modify the original text").

More exec script examples (C / C++ / TypeScript) can be found at [elbot-showcase/hooks](https://github.com/Elfreese/elbot-showcase/tree/main/hooks).

