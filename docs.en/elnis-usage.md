<!-- This file is auto-translated from docs/elnis-usage.md. Do not edit manually. -->

# Elnis Configuration and Usage

This article follows [Elnis listening hub](elnis.md) and explains how to enable Elnis, configure Elwisp policies, and use Elvena to deliver events.

## Quick Start

Elnis is disabled by default. To enable it, configure it in `app.toml`:

```toml
[elnis]
enabled = true # Elnis master switch; if false, the HTTP runtime will not start.

[elnis.http]
addr = "127.0.0.1:32170" # It is recommended to bind to a local address first, and use a reverse proxy or intranet forwarding when exposure is needed.
max_body_bytes = 1048576 # Request body limit for a single Elvena request.
queue_size = 128 # Background queue length for LLM mode.
workers = 2 # Number of background workers in LLM mode.

[elnis.tokens.home]
token_env = ["ELNIS_HOME_TOKEN"] # Read from system environment variables or the .env file in the configuration directory.

[elnis.delivery]
default_platforms = ["cli"] # Default delivery platforms allowed by Elnis policies.
allow_superadmins = true # Whether delivery to the target platform's superadmin is allowed.

# Elwisp is enabled by default; configure it only when you need to limit tokens, override delivery policies, or disable it.
[elnis.elwisps.server-watchdog]
allowed_tokens = ["home"]

[elnis.elwisps.server-watchdog.delivery]
default_platforms = ["cli"]
allow_superadmins = true

[elnis.elwisps.spike-checker]
enabled = false # This Elwisp is disabled only when enabled=false is explicitly set.
```

`.env` example:

```dotenv
ELNIS_HOME_TOKEN=change-me
```

Configuration description:

- `[elnis].enabled=false` will not start the Elnis HTTP runtime.
- Do not write the token plaintext into the configuration; it is recommended to place it in system environment variables or the configuration directory `.env`.
- `token_env` can contain multiple environment variable names, which will be tried in order.
- Elwisp is enabled by default; it will receive events if `[elnis.elwisps.<name>]` is not specified, if it is specified but `enabled` is not, or if `enabled=true` is specified.
- Only explicit `enabled=false` will disable the corresponding Elwisp.
- `allowed_tokens` restricts which tokens can represent the Elwisp to deliver events; if not specified, any authenticated token is allowed.
- `default_platforms` is the default delivery platform allowed by the Elnis policy.
- `allow_superadmins=true` indicates that delivery to the target platform's superadmin is allowed.

After starting ElBot, you can use curl to test a `direct` event:

```bash
curl -sS http://127.0.0.1:32170/elvena/v1/events \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer change-me' \
  -d '{
    "version":"elvena.v1",
    "elwisp":{"name":"server-watchdog"},
    "source":"minecraft-main",
    "id":"cpu-alert-001",
    "mode":"direct",
    "title":"服务器 CPU 异常",
    "content":"minecraft-main CPU 使用率超过阈值。",
    "targets":{"platforms":["cli"],"superadmins":true}
  }'
```

If the same `elwisp.name + source + id` is sent again, Elnis will return duplicate and will not redistribute it.

## Elvena Request Example

```json
{
  "version": "elvena.v1",
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
  "tool_list_names": ["shell"],
  "targets": {
    "platforms": ["cli"],
    "superadmins": true
  },
  "meta": {
    "severity": "warning",
    "host": "mc-main-01"
  }
}
```

Common fields:

| Field | Required | Description |
| --- | ---: | --- |
| `version` | Yes | Protocol version, currently `elvena.v1`. |
| `elwisp.name` | Yes | Elwisp name, which is also one of the source identities. |
| `elwisp.tags` | No | Elwisp tag, used for logs and statistics. |
| `source` | Yes | Specific event source, such as service name, script name, or RSS name. |
| `id` | Yes | Unique event ID within the source. |
| `created_at` | No | Occurrence time of the external event; the reception time is used if missing. |
| `mode` | Yes | `record`, `direct`, or `llm`. |
| `title` | No | Event title, used for notifications and background Session titles. |
| `format` | No | `text` or `elyph`, default `text`. |
| `content` | Yes | Event body. For LLM mode, using ELyph Task Notation `#task` is recommended. |
| `model_slot` | No | Model slot, used subsequently for `elwisp1`, `elwisp2`, and `elwisp3`. |
| `tool_list_names` | No | ElBot tool name preloaded for background tasks; `discover_tool` will be ignored. |
| `tools` | No | Tools declared by Elwisp along with the event. This is currently a capability under development and is not recommended for reliance. |
| `targets` | No | The delivery target expected by Elwisp; the final decision is still made by Elnis. |
| `meta` | No | Original supplementary data, used only for recording and prompt attachment. |

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

## Delivery Target and Security Boundary

Elwisp can declare the expected target in `targets`, but the final target is decided by Elnis.

Currently, it is recommended to only use:

```json
{
  "targets": {
    "platforms": ["cli"],
    "superadmins": true
  }
}
```

`platforms` can contain `"all"`, indicating that the request is delivered to all platforms allowed by the Elnis policy. Elnis will still calculate the final target based on global configuration, Elwisp configuration, and platform availability.

Security Conventions:

- The token name is used only for logs and audit logs, and is not equivalent to the Elwisp identity.
- The original token text is not written to logs.
- Elwisp cannot send platform messages directly.
- Elwisp cannot bypass the Tool Runtime and Security Policy to call ElBot tools.
- The `tools` declared by Elwisp is currently still under development and should not be relied upon as a stable interface.
