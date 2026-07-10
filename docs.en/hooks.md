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

Host writes two requests sequentially: `system.init` and `event.handle`. The script writes the response using the same `host:*` ID. Usage of the successful result of `event.handle`:

```json
{
  "status": "completed",
  "result": "optional template result",
  "message": {"text": "optional replacement text"},
  "outputs": [{"kind": "text", "text": "hello"}],
  "consume": false,
  "stop_propagation": false
}
```

`outputs` is an output intent array; Parsed relative to the plugin directory relative to `path`. For large media, please write to the plugin directory or return the path or URL after `_shared/`; do not put them in the JSON Pipe. stderr is used only for logs and failure diagnosis, and stdout can only output protocol frames.

Used when the Hook actively requests Host capabilities:

```json
{"type":"request","id":"plugin:send-1","method":"platform.call","params":{}}
```

Host writes back `response`. The ID initiated by the Host must be `host:*`, and the ID initiated by the Hook must be `plugin:*`; The response reuses the original request ID.

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
