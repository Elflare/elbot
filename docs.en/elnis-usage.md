<!-- This file is auto-translated from docs/elnis-usage.md. Do not edit manually. -->

# Elnis Configuration and Usage

This article follows [Elnis listening hub](elnis.md) and explains how to enable Elnis, configure Elwisp policies, and use Elvena to deliver events.

## Quick Start

Elnis is disabled by default. When enabling it, first declare the configuration file entry in `app.toml`:

```toml
[config_files]
elnis = "elnis.toml"
```

Then configure it in `elnis.toml`:

```toml
enabled = true # Elnis master switch; if false, the HTTP runtime will not start.
allowed_tools = ["web_search", "web_extract"] # By default, ElBot internal tools preloaded by Elwisp are allowed.

[http]
addr = "127.0.0.1:32170" # It is recommended to bind to a local address first, and use a reverse proxy or intranet forwarding when exposure is needed.
max_body_bytes = 1048576 # Request body limit for a single Elvena request.
queue_size = 128 # Background queue length for LLM mode.
workers = 2 # Number of background workers in LLM mode.

[tokens.home]
token_env = ["ELNIS_HOME_TOKEN"] # Read from system environment variables or the .env file in the configuration directory.

[delivery_disabled]
targets = [
  # { platform = "telegram" }, # Disable all deliveries for the entire telegram platform.
  # { platform = "telegram", type = "private", id = "123456789" },
  # { platform = "qqonebot", type = "group", id = "987654321" },
]

# Elwisp is enabled by default; configure it only when you need to limit tokens, override delivery policies, or disable it.
[elwisps.server-watchdog]
allowed_tokens = ["home"]
allowed_tools = ["shell", "web_search"] # Overrides the top-level allowed_tools when present.
disabled_external_tools = ["danger_tool"] # External tools are allowed by default; only specified tools are disabled here.
disabled_targets = [
  # { platform = "qqonebot", type = "group", id = "987654321" },
]

[elwisps.spike-checker]
enabled = false # This Elwisp is disabled only when enabled=false is explicitly set.
```

`.env` example:

```dotenv
ELNIS_HOME_TOKEN=change-me
```

Configuration description:

- `enabled=false` will not start the Elnis HTTP runtime.
- `elbot cli` is CLI-only mode; even with `enabled=true`, the Elnis HTTP runtime will not start; Please use `elbot run` or `elbot service run` when you need to receive Elvena events.
- Currently, `targets=[{"platform":"cli"}]` only applies to scenarios where Elnis and the CLI are in the same `elbot run` foreground process, or when the CLI remote server is enabled in service mode; Independent `elbot cli` processes must connect to the server before they can receive notifications.
- Do not write the token plaintext into the configuration; it is recommended to place it in system environment variables or the configuration directory `.env`.
- `token_env` can contain multiple environment variable names, which will be tried in order.
- Elwisp is enabled by default; it will receive events if `[elwisps.<name>]` is not specified, if it is specified but `enabled` is not, or if `enabled=true` is specified.
- Only explicit `enabled=false` will disable the corresponding Elwisp.
- `allowed_tokens` restricts which tokens can represent the Elwisp to deliver events; if not specified, any authenticated token is allowed.
- The top-level `allowed_tools` is the default whitelist for ElBot internal tools; it is overridden when `allowed_tools` of an individual Elwisp exists.
- External tools are allowed by default; an individual Elwisp can use `disabled_external_tools` to disable specific external tools.
- Elnis allows delivery by default; Only targets explicitly listed in `[delivery_disabled].targets` or a single Elwisp `disabled_targets` will be prohibited.

If you want ElBot to help you generate an Elwisp listener, you can submit a request to the superadmin Session in work mode, for example:

```text
@tool:elwisp_creator 帮我创建一个监听 RSS 更新并通过 Elnis 投递摘要的 Elwisp
```

`elwisp_creator` will provide protocol descriptions, configuration snippets, event templates, code scaffolding, and test commands; actually writing files or running commands will still continue to use the corresponding tools.

After starting ElBot, you can use curl to test a `direct` event:

```bash
curl -sS http://127.0.0.1:32170/elvena/v2/events \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer change-me' \
  -d '{
    "version":"elvena.v2",
    "elwisp":{"name":"server-watchdog"},
    "source":"minecraft-main",
    "id":"cpu-alert-001",
    "mode":"direct",
    "title":"服务器 CPU 异常",
    "content":"minecraft-main CPU 使用率超过阈值。",
    "targets":[{"platform":"cli"}]
  }'
```

If the same `elwisp.name + source + id` is sent again, Elnis will return duplicate and will not redistribute it.

## Elvena Request Example

```json
{
  "version": "elvena.v2",
  "elwisp": {
    "name": "server-watchdog",
    "tags": ["server", "prod"]
  },
  "source": "minecraft-main",
  "id": "cpu-alert-001",
  "mode": "llm",
  "title": "服务器 CPU 异常",
  "format": "elyph",
  "content": "#task investigate_cpu_alert - 检查服务器 CPU 异常并判断是否需要通知",
  "model_slot": "elwisp2",
  "tool_list_names": ["shell"],
  "tools": [
    {
      "name": "server_status",
      "description": "查询 minecraft-main 当前服务状态和最近错误摘要",
      "schema": {
        "type": "object",
        "properties": {
          "detail": {"type": "boolean"}
        }
      },
      "timeout_seconds": 10,
      "endpoint": "http://127.0.0.1:32171/tools/server_status"
    }
  ],
  "targets": [
    {"platform": "cli"},
    {"platform": "telegram", "type": "private", "id": "123456789"}
  ],
  "meta": {
    "severity": "warning",
    "host": "mc-main-01"
  }
}
```

## Segments (Multimodal Message Segments)

Elvena v2 supports sending images and files via the `segments` field. `content` is reserved as a plain text fallback and is fully compatible with the old Elwisp.

Behavior remains unchanged when `segments` is empty; when not empty, segments are rendered preferentially, and content serves as additional text.

### Segment Fields

| Field | Type | Required | Description |
| --- | ---: | :---: | --- |
| `kind` | string | Yes | `text`、`image`、`file`。 |
| `text` | string | text (Required) | Plain text content, not persisted to disk. |
| `url` | string | image/file (Required) | `http://`, `https://`, or `data:` base64 URI. |
| `name` | string | No | File name, used for downloading, saving, and displaying. |
| `mime_type` | string | No | MIME type hint. |

### Download and Storage

- Elnis automatically downloads it to `sandbox/elnis/<elwisp名>/<事件id>/` upon receipt.
- The original URL is used when sending to the LLM (multimodal models can view images directly), and a copy is kept in the sandbox.
- Direct mode also supports image/file output; it automatically degrades to a text description if the platform does not support it.
- File size is limited by `[segment].max_file_bytes` of `elnis.toml` (default 100MB).
- `data:` URIs only support base64 encoding and are similarly restricted after decoding.
- Local protocols such as `file://` are prohibited.

### Example

```json
{
  "version": "elvena.v2",
  "elwisp": {"name": "monitor"},
  "source": "prod-server",
  "id": "cpu-chart-002",
  "mode": "direct",
  "title": "CPU 异常",
  "content": "CPU 飙到 90%",
  "segments": [
    {"kind": "text",  "text": "服务器 CPU 飙到 90%，详见附图。"},
    {"kind": "image", "url": "https://monitor.example.com/chart.png", "name": "cpu_chart.png"},
    {"kind": "file",  "url": "https://logs.example.com/dump.txt", "name": "cpu_dump.txt"}
  ],
  "targets": [{"platform": "cli"}]
}
```

### report_segments in LLM results

After the background LLM processes the event, the `report_segments` of `JSONResult` can include image/file paths, which Elnis will deliver together when the report is sent. `url` must be a relative path within the current task working directory; absolute paths, `~`, or `..` cannot be used.


```json
{
  "completed": true,
  "need_report": true,
  "report": "分析完成，见截图。",
  "report_segments": [
    {"type": "image", "url": "chart.png"}
  ]

}
```


Common fields:

| Field | Required | Description |
| --- | ---: | --- |
| `version` | Yes | Protocol version, currently `elvena.v2`. |
| `elwisp.name` | Yes | Elwisp name, which is also one of the source identities; only English letters, numbers, `_`, and `-` are allowed; dots are not allowed. |

| `elwisp.tags` | No | Elwisp tag, used for logs and statistics. |
| `source` | Yes | Specific event source, such as service name, script name, or RSS name. |
| `id` | Yes | Unique event ID within the source. |
| `created_at` | No | Occurrence time of the external event; the reception time is used if missing. |
| `mode` | Yes | `record`, `direct`, or `llm`. |
| `title` | No | Event title, used for notifications and background Session titles. |
| `format` | No | `text` or `elyph`, default `text`. |
| `content` | Yes | Event body. For LLM mode, using ELyph Task Notation `#task` is recommended. |
| `model_slot` | No | Elnis LLM model slot, only supporting `elwisp1`, `elwisp2`, and `elwisp3`; if left blank or the corresponding slot is not configured, it will fall back to `work`. |
| `tool_list_names` | No | The ElBot internal tool name or Skill name preloaded for background tasks; Ordinary tools inject the schema, while Skills inject task descriptions and automatically inject the corresponding runner; Must be within the Elnis `allowed_tools` adjudication range; `discover_tool` will be ignored. |
| `tools` | No | External tools declared by Elwisp with events; allowed by default, rejected when hitting the `disabled_external_tools` of that Elwisp. |
| `targets` | Yes | Elwisp expects a delivery target array: `{"platform":"telegram"}` indicates sending to the platform superadmin, `type=private/group` with `id` indicates sending to a specified private chat/group chat, and `{"platform":"all"}` indicates all enabled platform superadmins. The final decision is still made by Elnis. |
| `meta` | No | Original supplementary data, used only for recording and prompt attachment. |


> **Note:** Elvena is based on JSON, and the content must be UTF-8 encoded.

The HTTP response only indicates that Elnis has received or rejected the request, and does not wait for the LLM to complete.


```json
{
  "accepted": true,
  "duplicate": false,
  "event_key": "server-watchdog/minecraft-main/cpu-alert-001",
  "mode": "llm",
  "status": "queued"
}
```

## CLI Remote Connection

`platform.cli` saves both CLI server and client configurations. `server` is the configuration read when ElBot acts as the server, and `clients` is the configuration read when the current command connects to the server as a CLI client.

```toml
[platform.cli]
enabled = true
default_client = "local"
default_url = "ws://127.0.0.1:32172/cli/v1/ws"

[platform.cli.server]
enabled = false
listen = "127.0.0.1:32172"

[platform.cli.server.tokens]
local = ["ELBOT_CLI_LOCAL_TOKEN"]
windows = ["ELBOT_CLI_WINDOWS_TOKEN"]

[platform.cli.clients.local]
token_env = ["ELBOT_CLI_LOCAL_TOKEN"]

[platform.cli.clients.windows]
url = "ws://192.168.1.10:32172/cli/v1/ws"
token_env = ["ELBOT_CLI_WINDOWS_TOKEN"]
```

- When `server.enabled=true`, `elbot service run` will start a local CLI WebSocket server.
- `server.listen` is the server listening address.
- `default_url` is the default connection address for the client; when connecting to other machines, specify the remote WebSocket address in `clients.<name>.url`.
- `server.tokens` is the list of CLI client ID and token environment variables allowed for server login.
- `clients.<name>` is the client profile; `id` can be omitted, defaulting to `<name>`; `url` can be omitted, defaulting to `default_url`.
- Similar to Provider/Elnis, the token is read from system environment variables first, and then from the configuration directory `.env`.
- `elbot cli -c <name>` uses a specified client profile to connect to the server; `-c` can be omitted when there is only one default client.

## Delivery Target and Security Boundary


Elwisp can declare the expected target in `targets`, but the final target is decided by Elnis.

`targets` must be an array:

```json
{
  "targets": [
    {"platform": "telegram"},
    {"platform": "telegram", "type": "private", "id": "123456789"},
    {"platform": "qqonebot", "type": "group", "id": "987654321"},
    {"platform": "all"}
  ]
}
```

Semantics:

- Only write `platform`: deliver to the superadmin of that platform.
- `type=private`: deliver to a specified platform private chat.
- `type=group`: deliver to a specified platform group chat.
- `platform=all`: deliver to the superadmins of all enabled platforms; `type` or `id` cannot be written at the same time.

Elnis allows delivery by default; It is only prohibited when `[delivery_disabled].targets` or a single Elwisp `disabled_targets` is hit. Setting `{ platform = "telegram" }` in the disable configuration means disabling all deliveries for this platform.

Security Conventions:

- The token name is used only for logs and audit logs, and is not equivalent to the Elwisp identity.
- The original token text is not written to logs.
- `model_slot` can only choose `elwisp1`, `elwisp2`, or `elwisp3`, and cannot specify an arbitrary internal mode name.
- Elwisp cannot bypass Tool Runtime and Security Policy to call ElBot internal tools; Tool names or Skill names in `tool_list_names` will all undergo Elnis `allowed_tools` adjudication.

- External `tools` declared by Elwisp are allowed by default and are injected as model-callable function names in the form of `elwisp_<elwisp>_<tool>` via ToolRun; A single Elwisp can use `disabled_external_tools` to disable specific tools.
- External tool calls are initiated by Elnis as HTTP JSON POST requests to the declared endpoint; the external tool itself is responsible for the actual risk boundary, and Elnis handles it as low-risk.
