<!-- This file is auto-translated from docs/configuration.md. Do not edit manually. -->

# Configuration Guide

ElBot uses a main configuration entry to load application configuration, Provider configuration, and runtime state. Default configurations are generated from the program's built-in assets into the platform configuration directory; existing configuration files will not be overwritten.

## Configuration File Responsibilities

The `config/` directory in the source code only retains example/auxiliary configuration files that can be maintained independently; the main configuration `app.toml` is generated from the program's built-in assets into the platform configuration directory upon the first run.

| File or Directory | Responsibility |
| --- | --- |
| `config/elnis.toml` | Elnis listening hub configuration, saving HTTP, token, delivery, allowed_tools, and Elwisp policies. |
| `config/state.toml` | Runtime state, e.g., default Session mode, chat/work/compact/naming model selection. |
| `config/tool_tags.toml` | Configuration file for adding tags and prompts to tools. |
| `config/SOUL.md` | The System Prompt source file for the Agent. |
| `config/.env` | Optional, local key file, not recommended for submission; the one automatically generated the first time is `.env.example`, and `.env` will not be generated directly. |
| `config/plugins/` | Hook and plugin configuration directory. |
| `config/skills/` | User-side Skill directory, located in the configuration directory by default; current subdirectories are `skills/agent/` and `skills/go/`. |
| `config/memories.toml` | Resident memory file, located in the configuration directory by default. |
| `config/long_memory/` | Long-term memory Markdown source data directory, located in the configuration directory by default. |

## Main configuration lookup order

The main configuration is searched in the following order upon startup:

1. Command line `--config`.
2. Environment variable `ELBOT_CONFIG_FILE`.
3. Platform configuration directory: Windows `%APPDATA%/ElBot/app.toml`; Linux uses the XDG configuration directory.
4. If the platform configuration does not exist, a default configuration file will be automatically generated in the platform configuration directory.

The content of the automatically generated default configuration comes from the program's built-in assets, and existing files will not be overwritten. Automatic generation is only triggered when there are no explicit `--config` and `ELBOT_CONFIG_FILE`. If the explicitly specified configuration path does not exist, ElBot will report an error instead of silently generating it, to avoid masking path spelling errors.

Files automatically generated for the first time include: `app.toml`, `providers.toml`, `state.toml`, `SOUL.md`, `memories.toml`, `elnis.toml`, and `.env.example`; At the same time, the directories `skills/`, `skills/agent/`, `skills/go/`, `plugins/`, and `long_memory/` will be created. Existing files will not be overwritten. `elnis.toml` defaults to `enabled=false`, and HTTP listening will not be started on the first run.


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

- `[tags.<tag-name>]`: Defines a tag that can be used in chat, for example, `[tags.agent]` corresponds to `@tool:agent`.
- `tools`: A list of tool names that this tag will preload. Tool names must be registered tools that the current user has permission to access.
- `prompt`: The tool usage strategy appended to the system prompt after this tag is successfully activated. The content will be presented directly to the model without automatically adding a tag name header.

Usage:

```text
@tool:agent 帮我检查这个项目的问题
```

This will preload the tools configured under `agent` into the current Session. If `prompt` is not empty, it will also be appended to the system prompt starting from this round.

Notes:

- Configured tags will be appended to built-in tags, not overwrite them.
- Only after `@tool:<tag>` successfully hits at least one tool will the current Session activate the prompt for that tag.
- Directly using `@tool:<tool-name>` only preloads the specified tools and does not activate the tag prompt.
- Activated tags are written to the Session metadata and remain effective after `/resume`.
- Prompt text is dynamically read from `tool_tags.toml`; changes to the file affect subsequent requests, behaving similarly to `SOUL.md`.
- When preloading tools that already exist, they will not be added again, and the platform will prompt `已存在工具：<name>`.
- It is recommended to write `prompt` as a specific tool usage strategy, rather than configuration mechanisms that the model does not need to know, such as "the current tag is xxx".

## Elnis listening hub

Elnis is disabled by default. Once enabled, ElBot will start a local HTTP ingress to receive events delivered by Elwisp according to the Elvena protocol. It is recommended to split the Elnis configuration into a separate `config/elnis.toml`, while `app.toml` only retains the entry path.

```toml
[config_files]
elnis = "elnis.toml"

# config/elnis.toml
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

QQ Official Bot, QQ OneBot, and Telegram configurations are commented out by default in the examples. When enabling them, you need to provide the platform's own authentication information and trigger keywords.

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
- `plugins/<plugin-name>.toml`: Plugin-specific configuration.

Hooks and plugins should not send platform messages directly; they should return an output intent, which the Agent then passes to the Output Manager for sending.

## Recommended Maintenance Method

- User-editable configurations should be centralized in the platform configuration directory to avoid directly modifying source code examples.
- Place actual keys in system environment variables or `.env`.
- Update this document synchronously when adding new configuration items.
- Update [Quick Start](getting-started.md) and README synchronously when changing default paths or startup behavior.
