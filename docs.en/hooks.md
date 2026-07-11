<!-- This file is auto-translated from docs/hooks.md. Do not edit manually. -->

# Hook

Hooks run at critical boundaries of ElBot: they can match or modify events, append output intents, or hand over events to external processes. Hooks do not directly call the platform's sending interface; Output is uniformly delivered to the platform by ElBot's Output Manager.


## Select Type First

| Requirement | Selection |
| --- | --- |
| Modify text, append output, or call registered tools after matching events | Rule Hook |
| Each event is independently handed over to an external program, which exits after processing | The `exec` action of Rule Hooks |
| Process residency, maintaining state, waiting for subsequent input, actively calling Host tools | Persistent Hook |

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
| `name` | Yes | Plugin identifier and default directory name; Persistent Hooks also use it as the worker ID. Persistent Hook IDs can only use lowercase letters, numbers, `-`, and `_`; it is also recommended that rule plugins follow this format. |
| `enabled` | No | Whether to load, default `true`. After modifying the root configuration, an administrator needs to execute a global `/hooks reload`. |
| `path` | No | Configuration path relative to `plugins/`, default `<name>/hook.toml`; it cannot be an absolute path or escape `plugins/`. |

Plugin source code, `hook.toml`, and plugin private state files are placed in `plugins/<id>/`. ElBot additionally creates `plugins/_shared/` for all Hooks to share files; It is not a plugin directory and will not be scanned.

Parsing failure of the root `hooks.toml` will cause the current rule configuration loading to fail; When a single referenced plugin configuration is incorrect, that plugin will be skipped, and other plugins will be loaded as usual. TOML uses strict field validation; unknown fields will cause an error. If the rule `name` is empty, it will currently be automatically named `rule.<序号>`, and duplicate names will have sequence numbers appended automatically; Do not rely on this compatibility behavior.

## Rule Hook

Ordinary rules use `[[rules]]`. After rules are filtered by `on`, matching conditions, roles, and priority, the actions are executed in sequence.

### Minimal Example

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

### Common Fields, Order and Control

| Field | Required | Description |
| --- | --- | --- |
| `name` | No | Rule display name; it is recommended to fill this in explicitly. |
| `description` | No | Instructions for using `/hooks` lists and details. |
| `on` | Yes | Hook point, see the table below. |
| `priority` | No | The smaller the number, the earlier it is executed; Default is `1000`, and `0` is also handled according to the default value. The same priority is executed stably according to the loading order: root rules, `[[plugins]]` declaration order, and the order of rules within each plugin. |
| `enabled` | No | Whether to load, defaults to `true`. |
| `require_wakeup` | No | Defaults to `true`; primarily used for `platform.message.received`. Setting it to `false` allows observing group messages that were not triggered. |
| `consume` | No | When set to `true` in `platform.message.received`, it will no longer enter commands or the main LLM after sending the current outputs. |
| `stop_propagation` | No | When set to `true`, it stops rules following the current Hook point; it does not stop the Agent's main process. |

`require_wakeup = false` only allows Hooks to observe un-invoked messages and will not let the main LLM process them automatically.

### Hook Point

| Hook Point | Timing | Main payload | Editable Fields | outputs will be sent |
| --- | --- | --- | --- | --- |
| `platform.connected` | Platform connection completed | `platform` | None | Yes |
| `platform.message.received` | Upon receiving a user message, before commands and the LLM | `platform`、`actor`、`message` | `message.text` | Yes |
| `agent.input.prepared` | Before input is written to the Session | `session`、`message` | `message.text` | No |
| `llm.turn.prepared` | Before the full round of LLM calls | `session`、`llm.messages/tools/provider/model` | `llm.latest_user_text` | No |
| `llm.request.prepared` | Before each actual model request | Same as above | `llm.latest_user_text` | No |
| `llm.response.received` | Model response completed | `llm.text/source_text/tool_calls/usage` | `llm.text` | Yes |
| `tool.call.prepared` | Before tool call | `session`、`tool` | `tool.arguments` | No |
| `tool.call.completed` | After tool call | `session`、`tool.result/error/risk` | `tool.result` | No |
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
  { type = "tool", name = "search", tool = "web_search", arguments = "{\"query\":\"{{message.text}}\"}" },
  { type = "send", text = "{{actions.search.result}}" },
]
```

```toml
# TOML array table, executed in declaration order.
[[rules.actions]]
name = "search"
type = "tool"
tool = "web_search"
arguments = "{\"query\":\"{{message.text}}\"}"

[[rules.actions]]
type = "send"
text = "{{actions.search.result}}"
```

`action = "..."` cannot be mixed with `actions`. Both `actions = [...]` and `[[rules.actions]]` use `type` and `arguments`; `name` is available in both action forms. The parameter field for inline `action = "tool"` is also `arguments`, not `args`.

| Type | Main fields | Effect |
| --- | --- | --- |
| `prepend` / `append` | `field`、`text` | Add content before and after the editable text. |
| `replace` | `field`、`pattern`、`replace`、`all` | Regex replacement; only the first match is replaced when `all = false`. |
| `delete` | `field`, `pattern`, or `text` | Delete all regex matches. |
| `send` | Single output field or `outputs` | Append output intent. |
| `tool` | `tool`, `arguments`, optional `name` | Call a registered tool. |
| `exec` | `command`, `cwd`, `timeout_seconds`, `field`, `timing`, optional `name` | Start a one-time `hook.v2` process. |

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
- Overwriting `message.text` in the exec response will reconstruct it as a pure text segment, and the original image and file segments will be lost.

## Output and Path

There are three types of output formats; fields cannot be mixed. Below is a comparison of fields that are easily confused, which will be expanded upon in subsequent sections.

| Concept | Rule TOML single `send` | Rule TOML `outputs` | One-time exec `result.outputs` | Persistent Hook JSON `outputs` |
| --- | --- | --- | --- | --- |
| Output Type | `kind` | `kind` | `kind` | `kind` |
| User ID of `at` | `text` | `user_id`, fallback to `text` when empty | `user_id`, fallback to `text` when empty | `text` |
| Message ID of `reply` | `path` | `message_id`, fallback to `path` when empty | `message_id`, fallback to `path` when empty | `reply_to_message_id` |
| Media source | `path` | `path`、`url`、`base64` | `path`、`url`、`base64` | `path`, `url`; `base64` is not supported |
| target | `target.*` of action, snake_case | `target.*` of action, snake_case | Not supported; use the current event | output's own `target`, PascalCase |
| timing | `timing` of action | `timing` of action | `timing` of exec action | Not supported |

### A single `send` of the rule TOML

```toml
action = "send"
kind = "reply"
# Put the quoted message ID of the reply in path.
path = "{{platform.reply_to_message_id}}"
text = "已收到。"
timing = "immediate"
target.group_id = "123456"
```

Single output supports `kind`, `text`, `path`, `timing`, and `target`. `kind` defaults to `text`:

- `text`: `text` is text;
- `image` / `file`: `path` is the media source, and `text` is the fallback;
- `emoticon`: `text` is the emoji name, and `path` can specify local media;
- `at`: `text` is the platform's original user ID;
- `reply`: `path` is the platform message ID, and `text` is the reply body.

### TOML `outputs` and `outputs` of one-time exec

Multi-segment outputs of rules use `outputs`, and `outputs` of one-time exec responses use the same segment format:

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

| Field | Description |
| --- | --- |
| `kind` | `text`, `image`, `file`, `emoticon`, `at`, `reply`; default `text`. |
| `text` | Fallback field for text, media fallback, or special outputs. |
| `url` / `path` / `base64` | Media source for images, files, or emojis. Relative to `path`, resolved using the plugin directory that declares the rule. `base64` maximum 10 MiB after decoding. |
| `name` / `mime_type` | Media display name and MIME type. |
| `user_id` | Platform raw user ID of `kind = "at"`; falls back to `text` when empty. |
| `message_id` | Platform message ID of `kind = "reply"`; falls back to `path` when empty. |

Large media for one-time exec should be written to the plugin directory or `_shared/`, then return `path` or `url`; Do not stuff large amounts of data into the JSON Pipe. A single stdout protocol frame is at most 16 MiB.

### target and timing

Rule TOML uses snake_case target:

```toml
target.platform = "qqonebot"
target.scope_id = "group:123456"
target.private_user_id = "10001"
target.group_id = "123456"
target.superadmins = true
```

Usually, target is omitted to reuse the current event. `timing` is `immediate` (default) or `after_assistant`; See the Hook point table for its actual effective scope.

## One-off exec and hook.v2

### Common Protocol Principles

Both one-time exec and persistent Hooks use `hook.v2` JSON Lines: each line of stdin and stdout is a JSON frame; stdout can only be used to write protocol frames; logs should be written to stderr. Request IDs initiated by the Host must start with `host:`, and request IDs initiated by the Hook must start with `plugin:`; responses must reuse the corresponding request ID.

Both modes process `system.init` first, then process `event.handle` upon success, and both allow the Hook to send requests to the Host; Available methods vary depending on the execution mode. A one-time exec exits after processing a single `event.handle`; A persistent Hook continuously processes multiple events and additionally receives `system.shutdown` and may receive `event.cancel`.

`exec` starts a child process for each match: the Host sends `system.init`, sends `event.handle` after receiving a successful response, and waits for the process to exit after reading its response.

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

### command, cwd and timeout

`exec.command` does not implicitly go through a shell. It supports single quotes, double quotes, and limited escaping of quotes/backslashes; Shell syntax should explicitly call `bash -lc`, `sh -c`, or a platform interpreter.

- When `timeout_seconds` is `0` or omitted, no additional exec timeout is set; it cannot be a negative number.
- The `cwd` of rules within a plugin defaults to the plugin directory; an explicit cwd must be a relative path within the plugin directory.
- Rules for the root `plugins/hooks.toml` are executed in the `plugins/` directory by default; its cwd can be a relative or absolute path.
- The relative media `path` returned by `exec` is always relative to the plugin directory of the declared rule; root rules are relative to `plugins/`.

### Host Request and Hook Response

Each stdin/stdout frame is a line of JSON. Host ID uses `host:*`, and Hook active request ID uses `plugin:*`; The response must reuse the original request ID. stdout can only be used to write protocol frames; logs should be written to stderr.

Host first sends:

```json
{"type":"request","id":"host:init","method":"system.init","params":{"version":"hook.v2","runtime":{"plugin_name":"demo","plugin_dir":"C:/elbot/plugins/demo","config_path":"C:/elbot/plugins/demo/hook.toml","rule_name":"demo_rule","cwd":"C:/elbot/plugins/demo"}}}
```

Hook successfully responds:

```json
{"type":"response","id":"host:init","ok":true,"result":{}}
```

Then Host sends:

```json
{"type":"request","id":"host:event","method":"event.handle","params":{"event":{"point":"platform.message.received","platform":{"name":"qqonebot","scope_id":"group:123","message_id":"456","reply_to_message_id":"789"},"actor":{"id":"qqonebot:10001"},"message":{"role":"user","segments":[{"type":"text","text":"撤回"}]}},"match":{},"runtime":{"plugin_name":"demo","plugin_dir":"C:/elbot/plugins/demo","config_path":"C:/elbot/plugins/demo/hook.toml","rule_name":"demo_rule","cwd":"C:/elbot/plugins/demo"}}}
```

`event.handle` successful response:

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

Return only the outer error upon failure:

```json
{"type":"response","id":"host:event","ok":false,"error":"missing platform.reply_to_message_id"}
```

`status` can only be omitted or set to `completed`. When action `field` is configured, the existing `message.text` overrides this field; Empty strings are valid; omitting `message` or `message.text` indicates no change.

`matched = false` indicates that the external script's decision rule was not hit: text modifications and outputs generated by this rule will be rolled back, remaining actions will not be executed, and this will not be treated as an error.

### exec Hook actively requests Host

A one-time exec can send an `plugin:*` request to the Host:

| method | Effect |
| --- | --- |
| `platform.call` | Call the native API of the current event platform. |
| `output.send` | Immediately send outputs via the Output Manager. |
| `message.get_reply` | Read the currently referenced platform message ID. |
| `message.get` | Reserved interface; currently always returns `available: false`. |
| `hook.log` | Write to Host logs. |

`platform.call` example:

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

`platform` is optional and defaults to the `platform.name` of the current event; if provided, it must equal the current platform. After the Host succeeds, it writes back with the same ID:

```json
{"type":"response","id":"plugin:recall-1","ok":true,"result":{}}
```

When the platform adapter returns JSON, the Host places it as a JSON value into `result` as is; otherwise, as a string. Hooks should first determine general success based on the outer `ok`.

## Persistent Hook

Persistent Hooks are still Hooks; there is no independent plugin system or `/plugins` command. It is declared by the plugin's own `hook.toml` and starts automatically after ElBot starts or `/hooks reload`.

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

# When there is no need to call Host tools, the entire [plugin.runtime.tools] can be omitted.
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
# The rules of a Persistent Hook are only event.handle triggers; do not set action/actions/consume/stop_propagation.
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

The following fields for `[plugin.runtime]` are required:

| Field | Description |
| --- | --- |
| `stateful` | Must be `true`. |
| `command` | Startup command, executed without a shell. Currently split simply by whitespace, **quotes are not parsed**; if parameters contain spaces, please wrap them in a script or avoid this path. |
| `cwd` | Relative to the plugin directory; cannot be an absolute path or escape the plugin directory. Usually `.`. |
| `startup_timeout_seconds` | Maximum seconds to wait for a successful response from `system.init`, must be greater than `0`. |
| `shutdown_timeout_seconds` | Maximum seconds to wait for `system.shutdown` and exit, must be greater than `0`. |
| `event_timeout_seconds` | Maximum seconds for a single `event.handle`, which also limits the tool context for this instance, must be greater than `0`. |
| `max_wait_seconds` | Maximum remaining time that can be declared for a waiting Session, must be greater than `0`. |

The following fields for `[plugin.runtime.restart]` are required:

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

Persistent trigger rules reuse the matching, role, priority, and `require_wakeup` semantics of rule Hooks. `action` or `actions` will cause configuration validation to fail; `consume` and `stop_propagation` will not be applied by the trigger rule even if written; they should instead be returned by the plugin's `event.handle` response.

When the plugin needs to dynamically decide whether to intercept, keep `consume` and `stop_propagation` in the trigger rule as `false` (or omit them), and then have the plugin return these two fields in the `event.handle` response of stdout based on the processing result. Return `consume: true` and `stop_propagation: true` when processing is successful and exclusive access to the message is required; Omit or return `false` when not processed; the message will continue to be passed to subsequent rules or the main LLM.

The worker state is `starting`, `ready`, `running`, `degraded`, `stopping`, `stopped`, or `failed`. When stopping, the Host first requests `system.shutdown`, and the process is forcibly terminated only after the shutdown timeout is exceeded.

### Protocol and Output

`system.init.params` received by the persistent process:

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

| Field | Description |
| --- | --- |
| `event` | The current Hook event. |
| `match` | Regex captures from the trigger rule; an empty object if there are no captures. |
| `continuation` | `true` represents subsequent messages captured by the waiting route. |
| `tool_context` | The foreground tool call token for this instance must be used as is for the foreground `tool.call`. |

#### Reading User Input

Persistent Hooks should not assume that `event.message.segments` has had the wake-up prefix removed: it retains the original segments after platform normalization; `event.message.intent_text` is the user intent text calculated by the Host after removing the wake-up prefix. When handling text commands, `intent_text` should be read preferentially; if it is empty, concatenate the text segments:

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

For the first call triggered by a `platform.message.received` trigger rule matched with `message.intent_text`, the plugin usually does not need to verify the same wake-up prefix or command text again; The Host has already completed this layer of routing. waiting continuation will also only capture messages from the same platform, scope, and actor. Business parameter validation, external service authorization, and plugin-specific permission checks are still the responsibility of the plugin.

The `result` of a successful `event.handle` response:

| Field | Description |
| --- | --- |
| `status` | Omitted or `completed` indicates the end; `waiting` indicates continuing to capture subsequent messages. |
| `conversation_id` | A required non-empty opaque ID when `waiting`. |
| `expires_at` | A required RFC 3339 timestamp when `waiting`, which must be later than the current time and not exceed `max_wait_seconds`. |
| `outputs` | The output array passed to the Output Manager. |
| `consume` | Blocks commands and the main LLM when the first trigger message is `true`. |
| `stop_propagation` | Stops subsequent rules of the current Hook point when it is `true`. |

Persistent Hook JSON output uses another set of fields:

```json
{
  "kind": "reply",
  "reply_to_message_id": "456",
  "text": "引用回复",
  "target": {"Platform":"qqonebot","ScopeID":"group:123"}
}
```

| Field | Description |
| --- | --- |
| `kind` | `text`、`image`、`file`、`emoticon`、`at`、`reply`。 |
| `text` | text content, media fallback; the platform's original user ID of `at` is also placed here. |
| `name` / `alt_text` | Display name and alternative text when media cannot be sent. |
| `url` / `path` / `mime_type` | Media source; resolved relative to `path` using the plugin directory. Persistent Hook output does not support `base64`. |
| `reply_to_message_id` | The referenced platform message ID of `reply`. |
| `target` | Optional explicit target, using PascalCase: `Platform`, `ScopeID`, `PrivateUserID`, `GroupID`, `Superadmins`. |

When target is omitted, the current event target is used. Rule TOML targets use snake_case; do not mix them.

### Waiting, Concurrency, and Cancellation

The same `platform + scope_id + actor.id` is executed serially within the same worker, while different routes can be executed in parallel. The waiting lease also uses this triplet as the key:

- In normal serial distribution, the first persistent Hook that returns `waiting` holds the route, and subsequent persistent trigger rules will no longer distribute this route. Ensuring that only one lease is stored per route; If concurrent events cause multiple Hooks to compete simultaneously, the last writer overwrites the previous lease. Therefore, when multiple persistent Hooks might match the same route, `priority`, stricter conditions, or `stop_propagation` should be used to clarify ownership.
- When waiting exists, ordinary `platform.message.received` rules will still run first; persistent trigger rules will not be triggered again.
- After regular rules are completed and the message has not been `consume`, the Host will route subsequent messages via `continuation = true` to the Hook holding the lease.
- After the continuation is routed, messages will no longer enter slash commands or the main LLM, even if `consume` in the response is `false`.
- After the continuation returns `completed` or omits `status`, the Host immediately releases the lease; Returning `waiting` again will renew it with a new ID and expiration time.
- Blocking strategy hits, worker exits, stops, or reloads will clear the plugin lease.

When the user sends the exact `/cancel`, the Host cancels the current execution or waiting Session of that route and writes a notification frame without a response to the plugin:

```json
{"type":"event","method":"event.cancel","params":{"conversation_id":"weather-42"}}
```

`conversation_id` is provided only when it exists. `/cancel` does not stop the process and plugin memory; use `/hooks stop <id>` to stop the worker.

### Tools and Shared State

Persistent Hooks maintain their own LLM loop, business state, and tool usage decisions. The Host only validates the allowlist, foreground/background capabilities, context ownership, count, timeout, cancellation, and send target for `tool.call`; **Does not execute ElBot's risk grading, permission policies, or interaction confirmations**. Plugins must handle tool authorization and risk control on their own.

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

`content` is the tool text result, `segments` is a `{type,text,url,mime_type,name}` array, `warnings` is a string array, and `receipts` is the platform message ID after the tool outputs are sent via the Output Manager. Failures uniformly use the outer `ok=false,error`. Hook tool calls will not be written to the Agent Session.

In addition to the `_shared/` file directory, all persistent Hooks also share an in-process JSON KV:

| method | `params` | Successful `result` |
| --- | --- | --- |
| `shared.get` | `{"key":"weather/cache"}` | `{"found":true,"value":<任意 JSON>}`; `found=false` when it does not exist. |
| `shared.set` | `{"key":"weather/cache","value":<任意 JSON>}` | `{"ok":true}` |
| `shared.delete` | `{"key":"weather/cache"}` | `{"deleted":true/false}` |
| `shared.list` | `{"prefix":"weather/"}` | `{"keys":["weather/cache",...]}`, in lexicographical order; an empty prefix lists all. |
| `shared.compare_and_swap` | `{"key":"weather/cache","expected":<旧 JSON>,"value":<新 JSON>}` | `{"swapped":true/false}` |

The key written to the shared KV must be `<namespace>/<key>`. The value must be valid JSON, with a maximum size of 1 MiB per value after compression and a maximum of 32 MiB for the shared area. `compare_and_swap` is an atomic operation, compared based on the compressed JSON content; Omitting `expected` indicates writing only if the key does not exist; explicitly specifying `expected: null` indicates that the current value must be JSON `null`. Shared memory is preserved across Hook restarts and `/hooks reload`, and is cleared after ElBot restarts; When persistence is required, the Hook writes to its own directory or `_shared/`.

### Plugin Self-Reload

After a persistent Hook modifies its own `hook.toml`, it can request to reload itself:

```json
{"type":"request","id":"plugin:reload-1","method":"hooks.reload","params":{}}
```

Do not synchronously wait for the response within the protocol loop that is the sole reader of stdin; otherwise, the Host's write-back will have no reader, leading to a deadlock. The continuous read loop should distribute responses by request ID, or requests should be sent from event worker threads.

The Host will first fully read and validate the candidate configuration. If it fails, it returns `ok=false`, and the old rules and processes remain unchanged; if it succeeds, it first returns:

```json
{"type":"response","id":"plugin:reload-1","ok":true,"result":{"scheduled":true}}
```

The actual replacement occurs after the current `event.handle` ends: only the rules and workers that call the plugin are replaced, the waiting routes and tool contexts of that plugin are cleared, and other plugins are not restarted. The caller's identity is determined by the process, and it cannot reload other plugins; Reload requests cannot be made during `starting`, `stopping`, or `stopped`. Plugin references in the root `plugins/hooks.toml`, `enabled`, `path`, as well as the addition or removal of plugins, still require an administrator to perform a global `/hooks reload`.

## Management Commands

`/hooks` is the superadmin management entry point:

```text
/hooks
/hooks <rule-name-or-stateful-id>
/hooks start <id>
/hooks stop <id>
/hooks restart <id>
/hooks reload
```

`/hooks` lists rules and persistent Hooks; `/hooks <名称>` views rule details or persistent worker status. `start`, `stop`, and `restart` only accept persistent Hook IDs. Global `reload` re-reads rules and persistent runtime configurations, and stops, replaces, or starts the corresponding workers; Configurations are not dynamically schema-patched; affected workers will restart.

## Event and Template Fields

`event` top-level contains `id`, `point`, `time`, `metadata`, `control`, `platform`, `actor`, `session`, `request`, `message`, `llm`, and `tool`; There may also be `outputs` and `error`. Fields unrelated to the current Hook point will be zero values, empty objects, or omitted; plugins must handle them as nullable values.

| Object | Field |
| --- | --- |
| `control` | `consume`、`stop_propagation`。 |
| `platform` | `name`、`scope_id`、`user_id`、`conversation_id`、`message_id`、`reply_to_message_id`。 |
| `actor` | `id`（`<platform>:<id>`）、`user_id`、`role`、`group_role`、`display_name`。 |
| `session` | `id`, `mode`, `title`, `status`; some Hook points only provide `id`. |
| `request` | `id`, `kind`, `session_id`, `phase`; currently all may be empty. |
| `error` | `message`; only reliable for error events. |

`message`：

| Field | Description |
| --- | --- |
| `id`、`role` | Message ID and `user` / `assistant` / `system` / `tool` roles. |
| `platform_text` | Original platform text, may be empty. |
| `intent_text` | User intent after removing the wake-up prefix; message input Hooks should generally prioritize using it. |
| `segments` | `{type,text,url,mime_type,name}` array, with type as `text` / `image` / `file`. |
| `reply` | Quoted message: `message_id`, `sender_id`, `text`, `display_text`, `segments`. |
| `messages` | LLM message array; non-LLM context is usually empty. |

`llm` contains `provider`, `model`, `messages`, `tools`, `usage`, `source_text`, `text`, `tool_calls`, `elapsed_ms`. Among them, nested `messages`, `tool_calls`, `usage` follow the JSON names exported by Go: `Role/Segments/Name/ToolCallID/ToolCalls`, `ID/Name/Arguments`, `PromptTokens/CompletionTokens/TotalTokens/CacheHitTokens`.

`tool` contains `id`, `name`, `arguments` (JSON string), `risk`, `result`, `error`. The prepared stage mainly provides `id/name/arguments`, while `risk/result/error` is only available in the completed stage.

Text fields that can be used in `if`, `match.field`, and templates:

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

Among these, `message.text`, `message.display_text`, `llm.latest_user_text`, and `llm.latest_user_display_text` are virtual text fields calculated during matching/templating and do not exist directly in the event JSON; The plugin process should read the original structure from `segments` or `messages`.

The template syntax is `{{...}}`, for example `{{platform.name}}`, `{{message.text}}`, and `{{llm.text}}`. Regex captures can be used as:

```text
{{match.regex.<regex条件序号>.group.<分组序号>}}
{{match.regex.<regex条件序号>.<命名分组>}}
```

Results from preceding tools / exec actions can be used via `{{actions.<name>.result}}` and `{{actions.<name>.error}}`. Known fields are rendered as empty strings when they have no value in the current event; Unknown templates, unknown action names, or non-existent capture paths will be kept as-is, making it easy to use them as plain text.
