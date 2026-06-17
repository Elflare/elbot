<!-- This file is auto-translated from docs/configuration.md. Do not edit manually. -->

# Configuration Guide

ElBot uses a main configuration entry to load application configurations, Provider configurations, and runtime states. Backward compatibility for old configurations is not considered during the development phase; it is recommended to maintain them directly according to the current `config/` example.

## Configuration File Responsibilities

The default source configuration directory contains:

| File or Directory | Responsibility |
| --- | --- |
| `config/app.toml` | Main configuration entry, saving application-level configurations such as storage, runtime, context, commands, tools, security, and platform. |
| `config/providers.toml` | LLM Provider, model list, default request parameters, and model metadata. |
| `config/state.toml` | Runtime state, e.g., default Session mode, chat/work/compact/naming model selection. |
| `config/SOUL.md` | The System Prompt source file for the Agent. |
| `config/.env` | Optional, local key file; not recommended to be committed. |
| `config/plugins/` | Hook and plugin configuration directory. |
| `config/skills/` | User-side Skill directory, located in the configuration directory by default. |
| `config/memories.toml` | Resident memory file, located in the configuration directory by default. |
| `config/long_memory/` | Long-term memory Markdown source data directory, located in the configuration directory by default. |

## Main configuration lookup order

The main configuration is searched in the following order upon startup:

1. Command line `--config`.
2. Environment variable `ELBOT_CONFIG_FILE`.
3. Platform configuration directory: Windows `%APPDATA%/ElBot/app.toml`; Linux uses the XDG configuration directory.
4. Source code directory `config/app.toml`.

Example:

```bash
go run ./cmd/elbot --config config/app.toml
```

## Relative Path Rules

Relative paths are resolved based on the directory of the main configuration file by default.

For example, when using `config/app.toml`:

```toml
[config_files]
providers = "providers.toml"
state = "state.toml"

[soul]
path = "SOUL.md"
```

These paths will all resolve to the `config/` directory.

## Provider Configuration

Provider is written in `providers.toml`:

```toml
[global_default]
stream = true
temperature = 1.0
max_tokens = 4096

[providers.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"

[providers.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
models = ["gpt-4o-mini"]
```

Note:

- `base_url` uses the Provider's OpenAI-compatible API address.
- `api_key_env` points to an environment variable name; this method is recommended for saving keys.
- `models` can be used to manually supplement the model list, or it can be obtained via the Provider's model list interface.
- `[global_default]` provides default request parameters.

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
```

- `default_mode` determines whether a new Session defaults to `chat` or `work`.
- `work` mode enables tool discovery and tool calling.
- `chat` mode does not inject tools, making it suitable for casual chatting and low-cost conversations.
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

Runtime logs, SQLite, sandbox, artifacts, and other runtime data will also be stored according to the configuration or the default data directory.

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

Cron expressions are scheduled by the internal Cron Runtime. Default maintenance tasks include the cleanup of logs, artifacts, and chat history.

## Context and Compaction

```toml
[context]
compact_enabled = true
compact_trigger_ratio = 0.8
```

- When enabled, compaction will be triggered when the Session context approaches the window limit.
- You can also manually compress the current Session via `/compact`.
- Compression only affects the context view sent to the LLM and does not delete the original history.

The model window can be supplemented in `providers.toml`:

```toml
[model_metadata]
default_context_window = 256000

[model_metadata.context_windows]
# "deepseek-chat" = 64000
```

## Command Prefix

```toml
[commands]
prefixes = ["/"]
```

`/` is used by default. If you want to support other command prefixes, you can add them here.

## Tools and Security

```toml
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

## Elnis Middle Platform Listener

Elnis is disabled by default. Once enabled, ElBot will start a local HTTP ingress to receive events delivered by Elwisp according to the Elvena protocol.

```toml
[elnis]
enabled = true

[elnis.http]
addr = "127.0.0.1:32170"
max_body_bytes = 1048576
queue_size = 128
workers = 2

[elnis.tokens.home]
token_env = ["ELNIS_HOME_TOKEN", "ELNIS_HOME_TOKEN_ALT"]

[elnis.delivery]
default_platforms = ["cli"]
allow_superadmins = true

[elnis.elwisps.server-watchdog]
enabled = true
allowed_tokens = ["home"]

[elnis.elwisps.server-watchdog.delivery]
default_platforms = ["cli"]
allow_superadmins = true
```

Note:

- `elnis.elwisps` is optional; leaving it empty indicates that no Elwisp is currently enabled.
- The token is read from system environment variables or the configuration directory `.env`. Logs only record the token name, not the raw token.
- `token_env` can be written as a list to try multiple environment variable names in order; this is suitable for temporarily switching tokens or achieving multi-environment compatibility.
- The initial phase supports `record` and `direct` modes; the `llm` mode is reserved for the subsequent background runner.
- `direct` mode only supports sending to superadmins via the platform after Elnis adjudication; it does not support arbitrary user/group targets.

## Platform Configuration

CLI enabled by default:

```toml
[platform.cli]
enabled = true
```

Configurations for the official QQ bot and QQ OneBot are commented out by default in the examples. When enabling them, you need to provide the platform's own authentication information, connection address, or trigger keywords.

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
