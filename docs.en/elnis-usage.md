<!-- This file is auto-translated from docs/elnis-usage.md. Do not edit manually. -->

# Elnis Configuration and Usage

This article follows [Elnis listening hub](elnis.md) and explains how to enable Elnis, configure Elwisp policies, and use Elvena to deliver events.

## Quick Start

Elnis is disabled by default. To enable it, first declare the configuration file entry in `app.toml`:

```toml
[config_files]
elnis = "elnis.toml"
```

Then configure it in `elnis.toml`:

```toml
enabled = true # Main switch for Elnis; the HTTP runtime will not start if set to false.
allowed_tools = ["web_search", "web_extract"] # Default allowed internal ElBot tools preloaded by Elwisp.

[http]
addr = "127.0.0.1:32170" # It is recommended to bind to a local address first; use a reverse proxy or intranet forwarding if exposure is needed.
max_body_bytes = 1048576 # Maximum body size for a single Elvena request.
queue_size = 128 # Background queue length in llm mode.
workers = 2 # Number of background workers in llm mode.
read_header_timeout_seconds = 5 # Timeout for reading request headers.
read_timeout_seconds = 30 # Timeout for reading the entire request (including body).
write_timeout_seconds = 300 # Timeout for handler execution and response writing; direct mode may involve external I/O, so the default is more lenient.
idle_timeout_seconds = 60 # Idle timeout for keep-alive connections.

[tokens.home]
token_env = ["ELNIS_HOME_TOKEN"] # Read from system environment variables or the .env file in the configuration directory.

[delivery_disabled]
targets = [
  # { platform = "telegram" }, # Disable all deliveries for the entire telegram platform.
  # { platform = "telegram", type = "private", id = "123456789" },
  # { platform = "qqonebot", type = "group", id = "987654321" },
]

# Elwisp is enabled by default; configure it only when you need to restrict tokens, override delivery strategies, or disable it.
[elwisps.server-watchdog]
allowed_tokens = ["home"]
allowed_tools = ["shell", "web_search"] # Overrides the top-level allowed_tools if present.
disabled_external_tools = ["danger_tool"] # External tools are allowed by default; only specified tools are disabled here.
disabled_targets = [
  # { platform = "qqonebot", type = "group", id = "987654321" },
]

[elwisps.spike-checker]
enabled = false # This Elwisp is disabled only if explicitly set to enabled=false.
```

`.env` Example:

```dotenv
ELNIS_HOME_TOKEN=change-me
```

Configuration Notes:

- **Startup Condition**: The Elnis HTTP runtime will not start when `enabled=false`.
- `elbot client` is a CLI-only mode; the Elnis HTTP runtime will not start even if `enabled=true`; use `elbot run` or `elbot service run` to receive events.
- `targets=[{"platform":"cli"}]` only takes effect in two cases: Elnis and the CLI are in the same `elbot run` foreground process, or the service mode has enabled the CLI remote server. An independent `elbot client` process must connect to the server to receive notifications.
- **token**: The token is not written to the configuration file; place it in system environment variables or the `.env` in the configuration directory; `token_env` supports multiple variable names to be tried in order; `allowed_tokens` restricts which tokens can deliver on behalf of the Elwisp; if omitted, any authenticated token is allowed.
- **Elwisp Activation**: Enabled by default. Events will be received if `[elwisps.<name>]` is missing, if it's present but `enabled` is missing, or if `enabled=true` is set; it is disabled only if explicitly set to `enabled=false`.
- **Tool Whitelist**: The top-level `allowed_tools` is the default whitelist for internal ElBot tools; it is overridden if a specific Elwisp has `allowed_tools`; external tools are allowed by default, and a specific Elwisp can use `disabled_external_tools` to disable specified items.
- **Delivery**: Allowed by default; only targets explicitly listed in `[delivery_disabled].targets` or a single Elwisp's `disabled_targets` will be prohibited.
- **Timeout**: If less than or equal to 0, the safe default values mentioned above are used; 0 cannot be used to disable the timeout.
- **Exposure**: Non-loopback addresses should be placed behind a reverse proxy, which handles TLS, connection limits, and request rate limiting; application-layer timeouts cannot replace these boundaries.

When you need ElBot to generate an Elwisp, simply state your requirement in a superadmin Session in work mode, for example, `@tool:elwisp_creator 帮我创建一个监听 RSS 更新并通过 Elnis 投递摘要的 Elwisp`.

After starting, you can use curl to test a `direct` event:

```bash
curl -sS http://127.0.0.1:32170/elvena/v3/events \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer change-me' \
  -d '{
    "version":"elvena.v3",
    "elwisp":{"name":"server-watchdog"},
    "source":"minecraft-main",
    "id":"cpu-alert-001",
    "mode":"direct",
    "title":"服务器 CPU 异常",
    "content":"minecraft-main CPU 使用率超过阈值。",
    "targets":[{"platform":"cli"}]
  }'
```

Sending the same `elwisp.name + source + id` again will return a duplicate and will not be redistributed.

## Elvena Request Example

```json
{
  "version": "elvena.v3",
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

### direct calls-only example

Withdraw a message via the QQ OneBot native interface `delete_msg`, where `params.message_id` is the platform message ID. This event only executes the API and does not send additional messages.

```json
{
  "version": "elvena.v3",
  "elwisp": {"name": "hook-recall"},
  "source": "rules-hook",
  "id": "recall-qqonebot-1024",
  "mode": "direct",
  "targets": [{"platform": "qqonebot", "type": "group", "id": "987654321"}],
  "calls": [
    {
      "platform": "qqonebot",
      "api": "delete_msg",
      "params": {"message_id": 1024}
    }
  ]
}
```

## Segments (Multimodal Message Segments)

`segments` is used to send images and files, while `content` is reserved as a plain text fallback. In a direct/record request, at least one of `content`, `segments`, or `calls` must be provided; LLM mode still requires `content` to be provided.

Behavior remains unchanged when `segments` is empty; when not empty, it is rendered according to segments by priority, with `content` serving as additional text.

| Field | Type | Required | Description |
| --- | ---: | :---: | --- |
| `kind` | string | Yes | `text`、`image`、`file`。 |
| `text` | string | text (Required) | Plain text content, not persisted to disk. |
| `url` | string | image/file (Required) | `http://`, `https://`, `data:` base64 URI, or a complete `media:<64 位小写 SHA-256>` of existing media. Media content will not be returned to external Elwisp. |
| `name` | string | No | File name, used for downloading, saving, and displaying. |
| `mime_type` | string | No | MIME type hint. |

Download and Storage:

- **Download on demand**: Events that are recorded, rejected, duplicated, or only queued will not download source media; references will be established for media IDs already existing in queued tasks.
- **direct**: Import or reuse Media Center media when there is an actual delivery target, without keeping a downloaded copy in the Elnis sandbox; Similarly supports image/file output, automatically falling back to text descriptions when the platform does not support them.
- **llm**: Input is materialized into a stable media ID during actual execution; the Session saves the ID, which is then resolved before the request is sent.
- **workspace**: Ordinary work files continue to be managed by the sandbox; report attachments are imported into the center when persistent delivery is determined to be necessary.
- **File name**: The original name is preserved if an extension already exists (including `.PNG`); it is only completed when no extension is present.
- **Limit**: File size is restricted by the `[segment].max_file_bytes` of `elnis.toml` (default 100MB); `data:` URIs only support base64 encoding and are similarly restricted after decoding; Local protocols such as `file://` are prohibited.

### Example

```json
{
  "version": "elvena.v3",
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

After the background LLM processes the event, the `report_segments` of `JSONResult` can include images / files:

- `url` should be filled with a relative path within the current task working directory; absolute paths, `~`, `..`, or symlinks/junctions that escape the directory are not allowed. Existing HTTP(S) sources remain compatible.
- `url` can also be filled with `media:<sha256>` to reference central resources.
- The host imports attachments when preparing the report, and the outbox saves stable media IDs; therefore, retries are still possible after the original workspace files are deleted.

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

### Output Association and Cleanup

Media references are divided into four levels and are released in the following order:

1. **Sending / outbox pending retry**: Keep valid references.
2. **After successful sending**: Associated with the corresponding platform message for subsequent reference replies (speculative associations are not created when the correspondence cannot be determined).
3. **Sent cache**: The retention period reuses `app.toml`'s `[maintenance.sandbox_cleanup].retention_days`; it is not retained if the value is not positive; Cache expiration only releases associated references; independent references for Sessions, Transcripts, forks, and reports pending retry are still retained.
4. **Orphan Cleanup**: After all references disappear and a 1-hour grace period passes, the media maintenance task cleans up the entity and its record; if deletion fails, the state is preserved for retry.

The start of the grace period is stored in the database; time spent with ElBot shut down is also counted, and it is not reset upon restart; After the grace period expires, cleanup occurs during the **next** execution of the maintenance task, rather than being deleted immediately at the exact one-hour mark. This task reuses the sandbox cleanup schedule, but does not delete Media Center content based on the age of directory files.

QQ OneBot, QQ Official, and Telegram support restoring associated media in quoted replies: replying to the last assistant message of one's own Session will restore that Session, while replying to an earlier message will Fork from that position. Elwisp remote tools only operate on their own files and do not obtain host media export paths, RPC, or storage credentials.

## Fields and Responses

| Field | Required | Description |
| --- | ---: | --- |
| `version` | Yes | Protocol version, currently `elvena.v3`. |
| `elwisp.name` | Yes | Elwisp name, also one of the source identities; only English letters, numbers, `_`, and `-` are allowed; dots are not permitted. |
| `elwisp.tags` | No | Elwisp tags, used for logs and statistics. |
| `source` | Yes | Specific event source, such as service name, script name, or RSS name. |
| `id` | Yes | Unique event ID within the source. |
| `created_at` | No | Timestamp of the external event; if missing, the reception time is used. |
| `mode` | Yes | `record`, `direct`, or `llm`. |
| `title` | No | Event title, used for notifications and background Session titles. |
| `format` | No | `text` or `elyph`, defaults to `text`. |
| `content` | No | Event body. Required in `llm` mode, where ELyph `#task` is recommended; can be empty in `direct`/`record` modes, but at least one of `content`, `segments`, or `calls` must be provided. |
| `model_slot` | No | Only `elwisp1`, `elwisp2`, or `elwisp3`; falls back to `work` if left blank or if the corresponding slot is not configured. |
| `tool_list_names` | No | Internal tool or Skill names preloaded for background tasks; ordinary tools inject the schema, while Skills inject descriptions and automatically inject the runner; must be within the Elnis `allowed_tools` range; `discover_tool` is ignored. |
| `tools` | No | External tools declared by Elwisp along with the event; allowed by default, rejected if they match the `disabled_external_tools` of that Elwisp. |
| `targets` | Yes | Array of desired delivery targets, see [Delivery Targets](#投递目标与安全边界) for semantics; final decision is made by Elnis. |
| `calls` | No | Array of platform-native API calls. Each item contains `platform`, `api`, and `params`; if `platform` is omitted, the platform of the first resolved target is used. For `direct` requests, if only `calls` are present without `content`/`segments`, only the API is executed and no message is sent. |
| `meta` | No | Original supplementary data, used only for records and prompt appending. |

The HTTP response only indicates whether Elnis has accepted or rejected the request, without waiting for the LLM to complete:

```json
{
  "accepted": true,
  "duplicate": false,
  "event_key": "server-watchdog/minecraft-main/cpu-alert-001",
  "mode": "llm",
  "status": "queued"
}
```

Each request body must contain exactly one JSON value; trailing whitespace is allowed. A second JSON value or invalid content will return `400`. Business rejections return `400`, authentication rejections return `401`, and internal or IO failures during reception return `500`. Unknown fields are ignored and do not participate in protocol semantics, normalization, event `content_hash`, or auditing; fields requiring formal semantics should be added to the corresponding protocol version.

If a platform transmission returns an error after partial success, the failure receipt is not used to associate messages or recover delivery; reporting a retry may duplicate fragments that have already been delivered. Other deliveries that have been separately confirmed as successful and persisted will still be skipped.

## Delivery Targets and Security Boundaries

`targets` must be an array; Elwisp can only declare the desired targets, and the final decision is made by Elnis:

- Only specify `platform`: deliver to the superadmin of that platform.
- `type=private` / `type=group` + `id`: deliver to a specified private chat / group chat on the platform.
- `platform=all`: deliver to the superadmins of all enabled platforms; `type` or `id` cannot be specified simultaneously.

Elnis allows delivery by default; it is only prohibited when matching `[delivery_disabled].targets` or a single Elwisp's `disabled_targets`. `{ platform = "telegram" }` in the disable configuration indicates that all deliveries to that platform are disabled.

Security Conventions:

- Token names are used only for logs and audits and are not equivalent to the Elwisp identity; the raw token is not written to logs.
- `model_slot` can only be selected from `elwisp1/2/3` and cannot specify any arbitrary internal mode name.
- Elwisp cannot bypass the Tool Runtime and Security Policy to call internal tools; names in `tool_list_names` will all be adjudicated by `allowed_tools`.
- External `tools` are allowed by default and are injected via ToolRun as model-callable function names in the form of `elwisp_<elwisp>_<tool>`; a single Elwisp can disable them using `disabled_external_tools`.
- External tool calls are initiated by Elnis as HTTP JSON POST requests to the declared endpoint; the external tool itself is responsible for the actual risk boundary, and Elnis treats them as low-risk.
