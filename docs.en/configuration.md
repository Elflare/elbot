<!-- This file is auto-translated from docs/configuration.md. Do not edit manually. -->

# Configuration Guide

ElBot uses a main configuration entry to load application configuration, Provider configuration, and runtime state. Default configurations are generated from the program's built-in assets into the platform configuration directory; existing configuration files will not be overwritten.

## Configuration File Responsibilities

All configuration files are automatically generated from the program's built-in assets to the platform configuration directory upon the first run; existing files will not be overwritten, and the `config/` directory is no longer retained in the source code.

| File or Directory | Responsibility |
| --- | --- |
| `elnis.toml` | Elnis listening hub configuration, saving HTTP, token, delivery, allowed_tools, and Elwisp policies. |
| `state.toml` | Runtime state, e.g., default Session mode, chat/work/compact/naming model selection. |
| `tool_tags.toml` | Configuration file for adding tags and prompts to tools. |
| `SOUL.md` | The System Prompt source file for the Agent. |
| `.env` | Optional, local key file, not recommended for submission; the one automatically generated the first time is `.env.example`, and `.env` will not be generated directly. |
| `plugins/` | Hook and plugin configuration directory. |
| `skills/` | User-side Skill directory, located in the configuration directory by default; The current subdirectories are `skills/agent/` and `skills/go/`. AgentSkill can place `ELBOT_SKILL.toml` in the root directory to configure visibility or register as a regular tool. |
| `memories.toml` | Resident memory file, located in the configuration directory by default. |
| `long_memory/` | Long-term memory Markdown source data directory, located in the configuration directory by default. |

## Main configuration lookup order

The main configuration is searched in the following order upon startup:

1. Command line `--config`.
2. Environment variable `ELBOT_CONFIG_FILE`.
3. Platform configuration directory: Windows `%APPDATA%/ElBot/app.toml`; Linux uses the XDG configuration directory.
4. If the platform configuration does not exist, a default configuration file will be automatically generated in the platform configuration directory.

The content of the automatically generated default configuration comes from the program's built-in assets, and existing files will not be overwritten. Automatic generation is only triggered when there are no explicit `--config` and `ELBOT_CONFIG_FILE`. If the explicitly specified configuration path does not exist, ElBot will report an error instead of silently generating it, to avoid masking path spelling errors.

Files automatically generated for the first time include: `app.toml`, `providers.toml`, `state.toml`, `SOUL.md`, `memories.toml`, `elnis.toml`, `skills/agent/agent_skill_creator/SKILL.md`, `skills/agent/agent_skill_creator/ELBOT_SKILL.toml`, `skills/agent/write_elbot_hook/SKILL.md`, `skills/agent/write_elbot_hook/ELBOT_SKILL.toml`, and `.env.example`; At the same time, the directories `skills/`, `skills/agent/`, `skills/go/`, `plugins/`, and `long_memory/` will be created. Existing files will not be overwritten. `elnis.toml` defaults to `enabled=false`, and HTTP listening will not be started on the first run.


During the development phase, you can run it directly to use the platform configuration directory; default configurations will be automatically generated upon the first run:

```bash
go run ./cmd/elbot
```

If you need to use a temporary configuration file, you can also explicitly specify `--config`.

## Relative Path Rules

Relative paths are resolved based on the directory of the main configuration file by default.

For example, writing `app.toml` under the platform configuration directory:

```toml
[config_files]
providers = "providers.toml"
state = "state.toml"
elnis = "elnis.toml"

[soul]
path = "SOUL.md"
```

These paths will all be resolved to the directory where the main configuration file is located; by default, this is the platform configuration directory.

## Workspace Tools

In work mode, the superadmin can allow the LLM to call the `workspace` tool to switch the shared working directory of the current Session. After switching, path-related tools such as `read_file`, `edit_file`, `send_file`, and the foreground `shell` will resolve relative paths based on this directory, avoiding the need to pass the full path every time.

When switching to a directory for the first time, if `AGENTS.md` or `AGENT.md` exists at the root of the directory, `workspace` will automatically attach the file content to the tool result for the LLM to read the working conventions of the current directory. The main part of the filename must be uppercase `AGENTS` or `AGENT`, while the case of the `.md` suffix is unrestricted; `AGENTS.md` takes precedence over `AGENT.md`.

The maximum size for the automatically attached instruction file is 64 KiB. If the limit is exceeded, the content will not be read, nor will it be marked as attached; the tool result will indicate that the file needs to be shortened or split before switching the workspace.

## Resident Memory Configuration

Resident memory data is saved by default in `memories.toml` of the configuration directory, and the file is generated when the program runs for the first time. The length limits for the two resident memory segments, core and normal, can be set in the main configuration:

```toml
[resident_memory]
core_max_units = 200
normal_max_units = 300
```

The length unit `units` can be roughly understood as "Chinese by character count, English by word count": CJK characters are counted individually, and continuous segments of English/numbers are counted as one word. core is used for core memories that require high-risk confirmation, and normal is used for ordinary memories that can be organized; When injecting the Prompt, the two segments will be merged into a single piece of natural text.

## Provider Configuration

Provider is written in `providers.toml`:

```toml
[providers.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"
proxy = ""                          # Optional, HTTP/SOCKS5 proxy address
extra_payload = { provider_field = "xxx" }  # Optional, Provider-level extra payload

[providers.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
models = ["gpt-4o-mini"]             # Manually supplemented model list (used when the API cannot retrieve them)

# Optional: configure context_window or extra_payload for specific models
# [providers.openai.model_configs."gpt-4o-mini"]
# context_window = 128000
# extra_payload = { }

[model_metadata]
default_context_window = 256000
```

Note:

- `base_url` uses the Provider's OpenAI-compatible API address.
- `api_key_env` points to an environment variable name; this method is recommended for saving keys.
- `proxy` is optional and supports `http://` and `socks5://` proxy addresses.
- `models` is a manually supplemented list of model names, used when the Provider's model list interface cannot retrieve certain models.
- `[providers.<name>.model_configs."<model>"]` configures `context_window` and `extra_payload` for specific models; both are optional.
- `extra_payload` will be merged into the LLM request JSON, with model-level settings overriding provider-level settings.
- The `default_context_window` of `[model_metadata]` is a global fallback value, used when `context_window` is not configured in `model_configs`.

## Built-in Web Tool Configuration

The `proxy` parameter of `web_extract` is used to control the proxy for webpage extraction requests:
If you want all default `web_extract` calls to go through a fixed proxy, you can set it in the configuration directory `.env` or in the system environment:

```env
WEB_EXTRACT_PROXY=http://127.0.0.1:7890
```

## LLM Request and Round Timeout

`app.toml`'s `[llm_request]` controls OpenAI-compatible streaming requests, round processing, and retries:

```toml
[llm_request]
first_chunk_timeout_seconds = 180
stream_idle_timeout_seconds = 60
response_timeout_seconds = 0
max_retries = 3
retry_initial_delay_seconds = 2
```

- `first_chunk_timeout_seconds`: The maximum wait time from the start of the response to the first streaming event, defaulting to 180 seconds, suitable for models with slower first-token generation.
- `stream_idle_timeout_seconds`: The maximum silence duration between two events during streaming, defaulting to 60 seconds; the timer resets upon receiving each new event.
- `response_timeout_seconds`: The maximum total duration for a round of user requests, from receiving user input to the end of the final response; a default of 0 indicates no time limit; When set to a positive number, processing for this round will stop when the time is reached, and the user will be notified. A single LLM streaming request is not limited by this field.
- `max_retries` and `retry_initial_delay_seconds` are used for connection failures or retryable HTTP failures, with retry delays increasing via exponential backoff.

The legacy `timeout_seconds` has been removed; existing configurations should be updated to the three new fields mentioned above.

## API Key and `.env`

Key reading priority:

1. System environment variables.
2. `.env` in the configuration directory.

Recommended method:

```dotenv
DEEPSEEK_API_KEY=your-api-key
OPENAI_API_KEY=your-api-key
```

Do not commit actual keys to the repository.

## CLI Remote Configuration

`[platform.cli]` stores both CLI server and client configurations. `server` is the configuration read when the current ElBot runs as a server, and `clients` is the configuration read when the current command connects to the server as a CLI client.

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

- When `server.enabled=true`, `elbot service run` will start the CLI WebSocket server.
- `server.listen` is the server listening address.
- `default_url` is the default client connection address; when connecting to other machines, enter the remote WebSocket address in `clients.<name>.url`.
- `server.tokens` is the list of CLI client ID and token environment variables allowed to log in to the server.
- `clients.<name>` is the client profile; `id` can be omitted, defaulting to `<name>`; `url` can be omitted, defaulting to `default_url`.
- `elbot cli -c <name>` uses the specified client profile; if not specified, `default_client` is used.
- Similar to the Provider API Key, the CLI token prioritizes system environment variables, then reads the configuration directory `.env`.

## AgentSkill Tooling Configuration

By default, AgentSkill is used only as documentation; If the script is executed according to the documentation, the risk is borne by the tools actually called, such as `shell`. To restrict the visibility of a documentation-type Skill, or to register `skills/agent/<skill>/` as a regular tool, add `ELBOT_SKILL.toml` to the root directory of that Skill. When `command`/`parameters`/`[args]` are not specified, it only serves as a visibility configuration and will not be registered as a regular tool; Once any toolization field is specified, the complete toolization configuration must be provided. The default generated `agent_skill_creator` Skill can be used to view instructions and assist in creating the file:

```toml
risk = "high"
superadmin_only = true
```

```toml
risk = "medium"
superadmin_only = false
tags = ["doc"]
command = ["python", "foo.py"]
timeout_seconds = 30
expose_root = false

parameters = '''
{
  "type": "object",
  "required": ["input"],
  "properties": {
    "input": {"type": "string", "description": "输入文本"}
  }
}
'''

[args]
input = "--input"
```

Field descriptions:

- `risk`: Optional, allows `safe`, `low`, `medium`, `high`, `critical`; Required when registering as a regular tool. When not specified for a documentation-type Skill, it is handled according to `safe`.
- `superadmin_only`: Optional. `true` indicates that only the ElBot superadmin can discover, preload, or call this Skill.
- `tags`: Optional, equivalent to categorizing the tool, which can be used for `@tool:<tag>` preloading.
- `command`: Required for toolization; a command array, do not use shell strings.
- `parameters`: Required for toolization; a JSON object schema that determines the tool parameters seen by the LLM.
- `[args]`: Required for toolization; a flat parameter mapping. `input = "--input"` will translate tool parameter `input` into `--input <value>`.
- `timeout_seconds`: Optional, command timeout.
- `expose_root`: Optional, defaults to `false`; when set to `true`, the Skill root path will be exposed upon discovering the Skill.

ElBot only reads `ELBOT_SKILL.toml` in the Skill root directory and does not scan recursively. The working directory during execution is fixed to the Skill root directory, and stdout will serve as the tool result; If stdout is `{"content":"..."}` JSON, the `content` field will be used.

## Go Skill Compiler Path

After modifying the `code_source` of the Go Skill, ElBot will automatically execute `gofmt`, `go build`, and reload. If ElBot is running as a Linux service, the service environment may not have loaded the `PATH` of the interactive shell, causing the `go` available in the terminal to be invisible to ElBot.

It is recommended to specify the Go executable in the configuration directory `.env`:

```dotenv
ELBOT_GO_BINARY=/usr/local/go/bin/go
```

Lookup order:

1. System environment variable `ELBOT_GO_BINARY`.
2. `ELBOT_GO_BINARY` in the configuration directory `.env`.
3. `GOROOT/bin/go`, if `GOROOT` is configured in the service environment.
4. `go` in the ElBot process `PATH`.

If using asdf, mise, Nix, Linuxbrew, Snap, or a custom installation path, it is recommended to write the actual `go` path directly into `ELBOT_GO_BINARY`, rather than relying on the initialization scripts of an interactive shell.

After modifying `.env` or the systemd environment, the ElBot service needs to be restarted.

For advanced deployments, this can also be specified in the systemd service:

```ini
[Service]
Environment=ELBOT_GO_BINARY=/usr/local/go/bin/go
```

## Session Configuration

`[session.idle_expiration]` in `app.toml` controls the idle expiration time of the current Session, in minutes:

```toml
[session.idle_expiration]
group_user_ttl_minutes = 10
group_superadmin_ttl_minutes = 10
private_user_ttl_minutes = 10
private_superadmin_ttl_minutes = 0
```

Field descriptions:

- `group_user_ttl_minutes`: Idle expiration time of the current Session for ordinary users in group chats.
- `group_superadmin_ttl_minutes`: Idle expiration time of the current Session for superadmins in group chats.
- `private_user_ttl_minutes`: Idle expiration time of the current Session for ordinary users in private chats.
- `private_superadmin_ttl_minutes`: Idle expiration time of the current Session for superadmins in private chats.
- Setting any field to `0` disables idle expiration for the corresponding scenario.

Under the default configuration, both regular users and superadmins in group chats will start a new Session after being idle for 10 minutes; In private chats, regular users will start a new Session after being idle for 10 minutes; In private chats, superadmins do not expire.

Here, "superadmin" refers to the ElBot superadmin configured in `[security.superadmins]`. Platform group owners or group administrators who are not in the superadmin list will still be handled according to the regular user rules.

## Model Status Configuration

`state.toml` saves the runtime model selection:


```toml
[session]
default_mode = "work"

[mode_models.work]
provider = "deepseek"
model = "deepseek-chat"

[mode_models.chat]
provider = "deepseek"
model = "deepseek-chat"

[mode_models.elwisp1]
provider = "deepseek"
model = "deepseek-chat"

[mode_models.elwisp2]
provider = "deepseek"
model = "deepseek-chat"

[mode_models.elwisp3]
provider = "deepseek"
model = "deepseek-chat"
```

- `default_mode` determines whether a new Session defaults to `chat` or `work`.
- `work` mode enables tool discovery and tool calling.
- `chat` mode does not inject tools, making it suitable for casual chatting and low-cost conversations.
- `elwisp1`, `elwisp2`, and `elwisp3` are optional model slots for Elnis LLM events; Elvena requests can be specified via `model_slot`, falling back to `work` when not configured.
- After switching models using `/model` at runtime, the state will be written back to `state.toml`.

## Storage and Runtime Data

Storage-related configurations in `app.toml`:

```toml
[storage]
sessions_sqlite_path = ""
chat_history_sqlite_path = ""
```

When left blank, the platform's default data directory is used:

- Windows：`%APPDATA%/ElBot/data`
- Linux: `$XDG_DATA_HOME/elbot` or `~/.local/share/elbot`

Runtime logs, SQLite, sandbox, and other runtime data will also be stored according to the configuration or the default data directory.

Platform inbound attachment download limits use `[platform_files]`:

```toml
[platform_files]
max_receive_file_bytes = 104857600
download_timeout_secs = 60
```

- `max_receive_file_bytes`: Maximum save size for platform inbound files, default 100MB; a prompt will be sent to the user when the limit is exceeded, and the file will not be saved to the server.
- `download_timeout_secs`: Platform inbound file download timeout, default 60 seconds.
- Inbound files are saved to the `platform/<platform name>` directory under sandbox by default, and will not trigger the LLM.


## Logs and Maintenance Tasks

Runtime log configuration:

```toml
[runtime]
log_level = "info"
log_retention_days = 30
```

Maintenance task examples:

```toml
[maintenance.log_cleanup]
enabled = true
schedule = "0 3 * * *"
```

Cron expressions are scheduled by the internal Cron Runtime, using the Linux crontab-style 5-field format: `分钟 小时 日 月 星期`. Default maintenance tasks include the cleanup of logs, Sessions, sandboxes, and chat history. For example, Session cleanup defaults to a 30-day retention period, sandbox content older than 7 days is cleaned up daily at 04:00 by default, and chat history cleanup is executed daily at 04:35 by default:

```toml
[maintenance.session_cleanup]
enabled = false
schedule = "15 3 * * *"
retention_days = 30
```

```toml
[maintenance.sandbox_cleanup]
enabled = true
schedule = "0 4 * * *"
retention_days = 7
```

```toml
[maintenance.chat_history_cleanup]
enabled = true
schedule = "35 4 * * *"
retention_days = 180
```

## Context and Compaction

```toml
[context]
compact_enabled = true
compact_trigger_ratio = 0.8
```

- When enabled, compaction will be triggered when the Session context approaches the window limit.
- You can also manually compress the current Session via `/compact`.
- Compression only affects the context view sent to the LLM and does not delete the original history.

The model window is configured in `model_configs` of `providers.toml`:

```toml
[providers.deepseek.model_configs."deepseek-chat"]
context_window = 64000

[model_metadata]
default_context_window = 256000
```

- `[model_metadata].default_context_window` is a global fallback value, used when `context_window` is not configured in the model block.

## Command Prefix

```toml
[commands]
prefixes = ["/"]
```

`/` is used by default. If you want to support other command prefixes, you can add them here.

## Tools and Security

```toml
[config_files]
tool_tags = "tool_tags.toml"

[tools]
max_rounds_per_turn = 10

[security]
user_max_tool_risk = "low"
superadmin_confirm_risk = "high"

[security.superadmins]
cli = ["local"]
```

- Regular users can only discover and call tools within the allowed risk range.
- Superadmins also need confirmation when calling high-risk tools.
- The default local CLI user `local` is a superadmin.
- `tool_tags.toml` is used to configure the tool groups that can be injected into `@tool:<tag>`, as well as the tool usage strategies appended to the system prompt after a tag is activated.

### `tool_tags.toml`

`tool_tags.toml` is a standalone configuration file, with its path specified by `[config_files].tool_tags`. Relative paths are based on the directory where `app.toml` is located:

```toml
[config_files]
tool_tags = "tool_tags.toml"
```

The file format uses tags as entry points:

```toml
[tags.agent]
tools = ["read_file", "edit_file", "shell"]
prompt = """
ROLE:
- The goal is to complete the user's task safely and accurately.

MUST:
- Inspect relevant files before editing.
- Prefer minimal, verifiable changes.
- Evaluate command safety before running shell commands.
"""
```

Field descriptions:

- `[tags.<tag-name>]`: Define a tag that can be used in chat, for example, `[tags.agent]` corresponds to `@tool:agent` or the shorthand `@t:agent`.
- `tools`: A list of tool names that this tag will preload. Tool names must be registered tools that the current user has permission to access.
- `prompt`: The tool usage strategy appended to the system prompt after this tag is successfully activated. The content will be presented directly to the model without automatically adding a tag name header.

Usage:

```text
@tool:agent 帮我检查这个项目的问题
@t:agent 帮我检查这个项目的问题
```

This will preload the tools configured under `agent` into the current Session. If `prompt` is not empty, it will also be appended to the system prompt starting from this round.

Notes:

- Configured tags will be appended to built-in tags, not overwrite them.
- Only after `@tool:<tag>` or `@t:<tag>` successfully hits at least one tool will the current Session activate the prompt for that tag.
- Directly using `@tool:<tool-name>` or `@t:<tool-name>` only preloads the specified tools and does not activate the tag prompt.
- Activated tags are written to the Session metadata and remain effective after `/resume`.
- Prompt text is dynamically read from `tool_tags.toml`; changes to the file affect subsequent requests, behaving similarly to `SOUL.md`.
- When preloading tools that already exist, they will not be added again, and the platform will prompt `已存在工具：<name>`.
- It is recommended to write `prompt` as a specific tool usage strategy, rather than configuration mechanisms that the model does not need to know, such as "the current tag is xxx".

## Elnis listening hub

Elnis is disabled by default. Once enabled, ElBot will start a local HTTP ingress to receive events delivered by Elwisp according to the Elvena protocol. It is recommended to split the Elnis configuration into a separate `elnis.toml`, while `app.toml` only retains the entry path.

```toml
[config_files]
elnis = "elnis.toml"

# elnis.toml
enabled = true
allowed_tools = ["shell", "web_search"]

[http]
addr = "127.0.0.1:32170"
max_body_bytes = 1048576
queue_size = 128
workers = 2

[tokens.home]
token_env = ["ELNIS_HOME_TOKEN", "ELNIS_HOME_TOKEN_ALT"]

[delivery_disabled]
targets = [
  # { platform = "telegram" },
  # { platform = "telegram", type = "private", id = "123456789" },
  # { platform = "qqonebot", type = "group", id = "987654321" },
]

[elwisps.server-watchdog]
allowed_tokens = ["home"]
allowed_tools = ["shell"]
disabled_external_tools = ["danger_tool"]
disabled_targets = [
  # { platform = "qqonebot", type = "group", id = "987654321" },
]
```

Note:

- `allowed_tools` is the Elnis internal tool whitelist; Elwisps without separate configurations inherit the global default.
- If a single Elwisp configures `allowed_tools`, it will override the global default.
- External tools are allowed by default; specified external tools are only disabled when a single Elwisp configures `disabled_external_tools`.
- Elnis delivery is allowed by default; `[delivery_disabled].targets` and single Elwisp `disabled_targets` are used to explicitly prohibit platforms, private chats, or group chats; `platform-only` in the configuration indicates that all deliveries for the entire platform are disabled.
- The token is read from system environment variables or the configuration directory `.env`. Logs only record the token name, not the raw token.
- `token_env` can be written as a list to try multiple environment variable names in order; this is suitable for temporarily switching tokens or achieving multi-environment compatibility.
- Elwisp is enabled by default; the corresponding Elwisp will only be disabled if `enabled=false` is explicitly configured.
- Currently, `record`, `direct`, and `llm` modes are supported; `llm` mode is executed using a background Session runner.
- In `llm` mode, `model_slot` can be specified as `elwisp1`, `elwisp2`, or `elwisp3` in Elvena requests; If not specified or if the corresponding slot is not configured, it will fall back to the `work` model.
- `direct` and `llm` reports only support sending to superadmins via the platform decided by Elnis, and do not support arbitrary user/group targets.
- `tools` in Elvena requests enters the validation, persistence, and execution chain; external tool names are still controlled by the denylist of individual Elwisps.

For more information, see [Elnis Configuration and Usage](elnis-usage.md).

## Platform Configuration

CLI enabled by default:

```toml
[platform.cli]
enabled = true
```

Configurations for QQ Official Bot, QQ OneBot, and Telegram are commented out by default in the examples. When enabled, the platform's own authentication information and trigger keywords must be provided. When files are received, download and save operations will be restricted according to `[platform_files]`.

Telegram uses Bot API long polling. Minimum configuration example:

```toml
[security.superadmins]
cli = ["local"]
telegram = ["123456789"]

[platform.telegram]
enabled = true
bot_token_env = "TELEGRAM_BOT_TOKEN"
proxy_url_env = "TELEGRAM_PROXY_URL" # Optional; reads system environment variables first, then the .env file in the configuration directory
trigger_keywords = ["bot"]
format = "html" # html/plain/rich
stream_edit_interval_milliseconds = 250
```

Note:

- `bot_token_env` is the variable name pointing to the Bot Token; the reading order is system environment variables, then the configuration directory `.env`; You can also use `bot_token` to write the configuration directly, but it is not recommended to commit actual tokens.
- `proxy_url_env` is the variable name pointing to the proxy address; the reading order is likewise system environment variables, then the configuration directory `.env`; You can also use `proxy_url` to write the configuration directly. Proxy address examples: `http://127.0.0.1:7890`, `socks5://127.0.0.1:1080`.
- `format="html"` is the default value: uses standard `sendMessage` + `parse_mode="HTML"`, and performs a lightweight conversion of common Markdown to Telegram HTML; Supports readable rendering of headings, quotes, horizontal rules, code blocks, and tables, with automatic plain text retry upon failure.
- `format="plain"` disables formatting and sends only plain text.
- `format="rich"` is an experimental mode: uses `sendRichMessage` / private chat `sendRichMessageDraft`, and automatically falls back to HTML if Rich Message fails; Some clients may not be able to view Rich Messages.
- `stream_edit_interval_milliseconds` controls the streaming refresh throttle interval, defaulting to 250ms, to avoid triggering platform rate limits.
- After the connection is successfully established, ElBot will synchronize built-in slash commands to the Telegram bot command menu; only main command names are synchronized, not aliases.
- In group chats/supergroups, command prefixes, trigger keywords, `@bot_username`, or replying to bot messages will all trigger processing. Private chats are processed by default.
- Confirmation messages for high-risk tools will be accompanied by a Telegram inline keyboard; clicking buttons will convert them into existing confirmation commands such as `/confirm` and `/reject`.
- Fill `security.superadmins.telegram` with a Telegram user ID or a private chat ID that can be sent to directly, used for superadmin permissions and notification delivery.


## Plugin and Hook Configuration

Plugin configurations are fixed under `plugins/` in the configuration directory:

- `plugins/hooks.toml`: Rule Hook configuration.
- `plugins/<plugin-id>/hook.toml`: Plugin Hooks referenced by `hooks.toml`; Can contain `[plugin.runtime]` persistent runtime configuration.
- `plugins/_shared/`: A cross-Hook file collaboration directory created by ElBot, not scanned as a plugin.

Hooks should not send platform messages directly; they should return an output intent, which is then handed over to the Output Manager by the Agent for sending.

For complete configuration instructions for rules and persistent Hooks (action, hook.v2, lifecycle, tools, and multi-turn capture), see [Hook](hooks.md).

## Recommended Maintenance Method


- User-editable configurations should be centralized in the platform configuration directory to avoid directly modifying source code examples.
- Place actual keys in system environment variables or `.env`.
- Update this document synchronously when adding new configuration items.
- Update [Quick Start](getting-started.md) and README synchronously when changing default paths or startup behavior.
