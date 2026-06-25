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

## Built-in Hooks

ElBot registers two types of built-in hooks with the program:

| Type | Description |
| --- | --- |
| Rule Hook | Loads declarative rules from `plugins/hooks.toml`, supporting conditional matching, text operations, output sending, tool calls, and script execution. |
| Resident Memory Hook | Inject the resident memory and temporary username of the current platform + actor every round. |

The Emoji Hook has been changed from an embedded plugin to a Rule Hook example; see the [Emoji Extraction Example](#表情提取示例) in this document.

## Rule Hook Configuration

Rule Hook configurations are fixed in `plugins/hooks.toml` of the configuration directory. Plugin-specific configurations are placed in `plugins/<plugin-name>.toml`.

### Rule Structure

```toml
[[rules]]
name = "stable_debug_name"          # Required, used for logs and audit
on = "hook.point"                   # Required, Hook point
enabled = true                      # Optional, defaults to true
priority = 1000                     # Optional, smaller values are executed first
```

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
session.id / mode / status
request.id / kind / phase
message.text / content_text / role
llm.text / raw_text / latest_user_text / latest_user_content_text / provider / model
tool.name / arguments / result / risk
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

`llm.raw_text` can be used for conditional matching, but cannot be edited.

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

Multi-segment output (segments):

```toml
actions = [
  { type = "send", timing = "after_assistant", segments = [
    { kind = "text", text = "检测到关键词" },
    { kind = "image", path = "alert.png" },
    { kind = "image", url = "https://example.com/chart.png", name = "chart.png" },
    { kind = "image", base64 = "iVBORw0KGgo..." },
    { kind = "emoticon", name = "微笑", path = "emoticons/微笑/01.png" },
    { kind = "file", path = "report.pdf", name = "报告.pdf" },
  ] },
]
```

The segment format is unified with the [Elvena Protocol](elnis-usage.md#segments多模态消息段), with additional support for local `path`, `base64`, and `emoticon` types:

| Field | Description |
| --- | --- |
| `kind` | `text` / `image` / `file` / `emoticon`, default `text` |
| `text` | Text content (required for text type, optional as additional text for other types) |
| `url` | HTTP/HTTPS URL（image/file） |
| `path` | Local file path (image/file/emoticon) |
| `base64` | base64 encoded data (image/file) |
| `name` | File name or emoticon name |
| `mime_type` | MIME type hint |

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

Execute a local script. The script uses `plugins/` as the working directory by default, which can be overridden by `cwd` (absolute paths are used directly, while relative paths are still based on `plugins/`).

By default, stdin is a JSON containing the full event and match context. You can also use the `stdin` field to customize the stdin content (template rendering is supported).

`stdout` mode:

| Mode | Description |
| --- | --- |
| `capture` | Default value; saves stdout to `{{actions.<name>.result}}` for use by subsequent actions |
| `send` | Sends stdout as text output |
| `outputs` | Parses stdout as JSON, extracting the `outputs` array and optional `text` |
| `elvena` | Parses stdout as an Elvena JSON request and delivers it to Elnis via the internal Elvena Bus |
| `ignore` | Ignore stdout |

```toml
actions = [
  { type = "exec", command = "uv run script.py", stdout = "capture", timeout_seconds = 30 },
]
```

### exec outputs mode

When `stdout = "outputs"`, the script stdout must be JSON:

```json
{
  "outputs": [
    {"kind": "emoticon", "name": "微笑", "path": "emoticons/微笑/01.png"},
    {"kind": "text", "text": "已处理"}
  ],
  "text": "清理后的文本"
}
```

- `outputs`: Each item's format is consistent with send segments and is converted into an output intent to be sent to the platform.
- `text`: Optional. When action is set to `field`, `text` will completely overwrite the field (subject to editable field validation); The original text will not be modified if `field` is not set or if `text` is empty.

```toml
[[rules]]
name = "emoticon_extract"
on = "llm.response.received"
if = "llm.text"
op = "regex"
value = "\\[\\[[^\\[\\]]+\\]\\]"
actions = [
  { type = "exec", command = "uv run emoticon_extract.py", stdout = "outputs", field = "llm.text", timing = "after_assistant" },
]
```

### Template Variables

Text fields and exec command/stdin support `{{...}}` template rendering:

```
{{platform.name}}          {{platform.scope_id}}      {{platform.user_id}}
{{actor.id}}               {{actor.user_id}}          {{actor.role}}
{{message.text}}           {{message.content_text}}
{{llm.text}}               {{llm.raw_text}}           {{llm.latest_user_text}}
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
[control]
consume = true              # Block subsequent slash commands and LLM processing
stop_propagation = true      # Block subsequent rules at the same Hook point from continuing execution
```

`consume = true` is typically used for `platform.message.received` Hooks: it blocks subsequent commands and LLM processing after sending output, allowing the Hook to take full control of the message.

## Hook exec Delivery to Elvena

The `exec` action of a Rule Hook can be set to `stdout = "elvena"`. The script stdout must be a complete Elvena JSON request; ElBot will pass it to Elnis via the internal Elvena Bus, rather than going through HTTP token authentication again.

```toml
[[rules]]
name = "server-script"
on = "platform.message.received"
roles = ["superadmin"]
always = true

[[rules.actions]]
type = "exec"
name = "notify"
command = "uv run scripts/build_elvena.py"
stdout = "elvena"
```

The script can output `mode = "direct"` notifications or `mode = "llm"` background tasks; Subsequent processing is still handled by Elnis's target arbitration, logging, deduplication, and background runner.

Elvena is based on JSON, and the content must be UTF-8 encoded. For the complete Elvena request fields, see [Elnis Configuration and Usage](elnis-usage.md#elvena-请求示例).

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
  { type = "exec", command = "uv run emoticon_extract.py", stdout = "outputs", field = "llm.text", timing = "after_assistant" },
]
```

### emoticon_extract.py

The script reads the event JSON from stdin, extracts `[[token]]`, and checks if there are images in the `emoticons/<token>/` directory. If images exist, it generates an emoticon output and removes the token from the text; otherwise, it keeps the text as is.

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
    data = json.load(sys.stdin)
    text = data.get("event", {}).get("llm", {}).get("text", "")
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
    result = {"outputs": outputs, "text": cleaned}
    json.dump(result, sys.stdout, ensure_ascii=False)

if __name__ == "__main__":
    main()
```

### Configuration Description

- `root_dir` (the configuration item for the old embedded plugin) is no longer needed; the directory path relative to `plugins/` is written directly in the script.
- `timing` controls the timing of emoticon delivery: `immediate` (default) sends before the LLM text output, and `after_assistant` sends after the assistant's reply.
- `field = "llm.text"` allows the `text` returned by the script to overwrite the LLM response text, removing the extracted tokens.
- When `field` is not set, the script only produces outputs without modifying the original text (suitable for scenarios such as "send a notification when content is detected but do not modify the original text").
