<!-- This file is auto-translated from docs/hooks.md. Do not edit manually. -->

# Hook

Hooks run at the critical boundaries of ElBot: they can observe or modify events, append output intents, or hand over events to external processes. Hooks do not send platform messages directly; All outputs are delivered to the platform by the Output Manager.

This version uniformly uses `hook.v2`. The `init/output/done` frames of the old `hook.v1` have been removed, and existing external exec scripts must be migrated.

## Configuration Location

The `plugins/hooks.toml` in the configuration directory is the rule entry point. It can directly contain `[[rules]]` or reference a plugin directory:

```toml
[[plugins]]
name = "weather"
# Read plugins/weather/hook.toml by default
```

Plugin source code, `hook.toml`, and its self-maintained state files are all placed in `plugins/<id>/`. ElBot additionally creates `plugins/_shared/` for all Hooks to share files; It is not a plugin directory and will not be scanned.

## Rule Hook

Ordinary rules use `[[rules]]`. Rules are first matched by `on`, conditions, and roles, and then actions are executed sequentially.

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

Common actions:

| action | Effect |
| --- | --- |
| `prepend` / `append` / `replace` / `delete` | Modify the text fields of the current event that are allowed to be edited. |
| `send` | Append text/image/file/emoticon/at/reply output intents. |
| `tool` | Call low-risk tools with the normal Hook permissions of the current Actor. |
| `exec` | Start a one-time external Hook and process it according to hook.v2. |

The `require_wakeup = false` of `platform.message.received` can observe uninvoked group messages; It will not let the main LLM process these messages automatically. When the rule is set to `consume = true`, it will no longer enter the command or LLM after sending outputs.

## hook.v2 one-time exec

The `command` of `exec` is executed directly after being split by whitespace, without implicitly passing through a shell. When shell syntax is required, explicitly use `sh -c`, `bash -lc`, or the corresponding platform interpreter.

```toml
actions = [
  { name = "extract", type = "exec", command = "uv run extract.py", field = "llm.text", timeout_seconds = 30 },
]
```

The Host first writes the `system.init` request, and after receiving a successful response, it writes the `event.handle` request. Each frame occupies one line of JSON in stdout/stdin. The complete request received by a one-time exec looks like:

```json
{"type":"request","id":"host:init","method":"system.init","params":{"version":"hook.v2","runtime":{"plugin_name":"demo","plugin_dir":"C:/elbot/plugins/demo","config_path":"C:/elbot/plugins/demo/hook.toml","rule_name":"demo_rule","cwd":"C:/elbot/plugins/demo"}}}
{"type":"request","id":"host:event","method":"event.handle","params":{"event":{"point":"platform.message.received","platform":{"name":"qqonebot","scope_id":"group:123","message_id":"456","reply_to_message_id":"789"},"actor":{"id":"qqonebot:10001"},"message":{"role":"user","segments":[{"type":"text","text":"撤回"}]}},"match":{},"runtime":{"plugin_name":"demo","plugin_dir":"C:/elbot/plugins/demo","config_path":"C:/elbot/plugins/demo/hook.toml","rule_name":"demo_rule","cwd":"C:/elbot/plugins/demo"}}}
```

Event data is located in `params.event`; rule regex captures are located in `params.match`. The example only expands commonly used fields; for actual `event` fields, see "Templates and Fields" below.

The Hook must use the same `host:*` ID to write back the response. `system.init` usually returns an empty object:

```json
{"type":"response","id":"host:init","ok":true,"result":{}}
```

The complete successful response for `event.handle` is:

```json
{
  "type": "response",
  "id": "host:event",
  "ok": true,
  "result": {
    "status": "completed",
    "result": "optional template result",
    "message": {"text": "optional replacement text"},
    "outputs": [{"kind": "text", "text": "hello"}],
    "consume": false,
    "stop_propagation": false
  }
}
```

When processing fails, return `ok=false` and `error`, and do not write `result`:

```json
{"type":"response","id":"host:event","ok":false,"error":"missing platform.reply_to_message_id"}
```

When the exec action is configured with `field`, the presence of `message.text` in the response will overwrite that field; An empty string is a valid value, indicating that the field should be cleared. Omitting `message` or `message.text` indicates that the field will not be modified.

`outputs` is an output intent array; Parsed relative to the plugin directory relative to `path`. For large media, please write to the plugin directory or return the path or URL after `_shared/`; do not put them in the JSON Pipe. stderr is used only for logs and failure diagnosis, and stdout can only output protocol frames.

When a Hook actively requests the Host to call the current platform API, use `platform.call`. `platform` can be omitted, and it defaults to `params.event.platform.name`; If explicitly filled, it must equal the current event platform. The parameters of the platform API itself are placed in the inner `params`, not the Hook output target.

QQ OneBot recall example:

```json
{
  "type": "request",
  "id": "plugin:recall-1",
  "method": "platform.call",
  "params": {
    "platform": "qqonebot",
    "api": "delete_msg",
    "params": {
      "message_id": "789"
    }
  }
}
```

The `message_id` here can be taken from `params.event.platform.reply_to_message_id`. `delete_msg` does not require additional declaration of group chat or private chat targets; Which target fields are required by other platform APIs is defined by the corresponding platform API.

Reuse the `plugin:*` ID for write-back after Host success:

```json
{"type":"response","id":"plugin:recall-1","ok":true,"result":{}}
```

`result` is the data returned by the platform adapter: if the platform returns JSON, the Host places it as a JSON value into `result` as is; otherwise, it is placed as a string. Its structure has no cross-platform convention; the Hook should use the outer `ok` to determine general success, and only parse `result` when there is an explicit dependency on a specific platform API. The failure response is:

```json
{"type":"response","id":"plugin:recall-1","ok":false,"error":"platform api call failed"}
```

IDs initiated by the Host must use `host:*`, and IDs initiated by the Hook must use `plugin:*`; The response must reuse the original request ID. stdout can only be used to write protocol frames; logs should be written to stderr.

## Persistent Hook

A Persistent Hook is still a Hook; there is no independent plugin system or `/plugins` command. It is declared by the plugin's own `hook.toml`:

```toml
[plugin]
name = "weather"
description = "有状态天气助手"

[plugin.runtime]
stateful = true
command = "uv run weather_hook.py"
cwd = "."
startup_timeout_seconds = 10
shutdown_timeout_seconds = 5
event_timeout_seconds = 30
max_wait_seconds = 900

[plugin.runtime.restart]
strategy = "on_failure" # never / on_failure / always
initial_delay_seconds = 1
max_delay_seconds = 30

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
# The rules of a Persistent Hook are only event.handle triggers; action/actions are not set.
```

All persistent runtime fields must be explicitly filled. ElBot will automatically start enabled Persistent Hooks after startup and `/hooks reload`; Its state is `starting`, `ready`, `running`, `degraded`, `stopping`, `stopped`, or `failed`. `system.shutdown` will first request a graceful shutdown and will only terminate the process if the shutdown timeout is exceeded.

Persistent Hooks can receive multiple `event.handle` after `system.init`. The same `platform + scope + actor` is processed serially, while different routes can be processed in parallel. The Hook response can return:

```json
{
  "status": "waiting",
  "conversation_id": "weather-42",
  "expires_at": "2026-07-10T12:00:00Z"
}
```

The wait route only captures subsequent messages from the initiator within the same scope; Group chats will not capture other members, nor do they require the initiator to @ again. It is executed after the regular `platform.message.received` Hook is completed and not consumed, and before the command and the main LLM. `/cancel` only cancels the current execution or waiting Session of the current route, while the process and memory state are retained; Only using `/hooks stop <id>` will stop the process.


## Tools and Shared State

Persistent Hooks manage their own LLM loop and business state. ElBot only delivers the schema in `[plugin.runtime.tools].allow` under `system.init`, and handles the `tool.call` requests of the Hook.

Ordinary `tool.call` must carry the `tool_context` delivered by the Host. The Host validates the allowlist, foreground/background availability, call count, timeout, cancellation, and context ownership; It does not write Hook calls into the ElBot Agent Session, nor does it perform user risk confirmation. Tool results return `content`, `segments`, `warnings`, and the delivery receipt from the Output Manager.

Background calls can only use `background_allow`, and the subject is `hook:<id>`; When there is no valid origin, an output target must be explicitly provided. If a Host-issued and non-expired origin is carried, platform, scope, and user information are used only for correct contextual tools, delivery routing, and minimum boundary auditing.

Except for the `_shared/` file directory, all persistent Hooks share an in-process JSON KV:

| request method | Description |
| --- | --- |
| `shared.get` | Read value by key. |
| `shared.set` | Write JSON value. |
| `shared.delete` | Delete key. |
| `shared.list` | List keys by prefix. |
| `shared.compare_and_swap` | Atomic conditional write. |

The key must be `<namespace>/<key>`. Shared memory is preserved across Hook restarts and `/hooks reload`, and is cleared after ElBot restarts; When persistence is required, the Hook writes to its own directory or `_shared/`.

## Management Commands

`/hooks` is the sole management entry point (superadmin command):

```text
/hooks
/hooks <name-or-id>
/hooks start <id>
/hooks stop <id>
/hooks restart <id>
/hooks reload
```

`reload` will reread the rules and persistent runtime configuration, and stop, replace, or start the corresponding persistent Hooks. Configuration changes do not perform dynamic schema patches; reloading will restart the affected processes.

## Templates and Fields

The text and `exec.command` of ordinary rules support `{{...}}` templates, such as `{{platform.name}}`, `{{actor.id}}`, `{{message.text}}`, `{{llm.text}}`, `{{tool.result}}`, and `{{match.regex.0.group.1}}`. The results of preceding tool/exec actions are available via `{{actions.<name>.result}}` and `{{actions.<name>.error}}`.

Matchable fields, Hook points, and editable fields follow existing rule semantics; configuration errors will be reported in `/hooks reload`, and the problematic plugin will be skipped without affecting other registered Hooks.
