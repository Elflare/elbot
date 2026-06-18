<!-- This file is auto-translated from docs/configuration.md. Do not edit manually. -->

# Configuration Guide

ElBot uses a main configuration entry to load application configurations, Provider configurations, and runtime states. Backward compatibility for old configurations is not considered during the development phase; it is recommended to maintain them directly according to the current `config/` example.

## Configuration File Responsibilities

The default source configuration directory contains:

| File or Directory | Responsibility |
| --- | --- |
| `config/app.toml` | Main configuration entry, saving storage, runtime, context, commands, tools, security, platform, and configuration file paths. |
| `config/providers.toml` | LLM Provider, model list, default request parameters, and model metadata. |
| `config/state.toml` | Runtime state, e.g., default Session mode, chat/work/compact/naming model selection. |
| `config/tool_tags.md` | Configuration file for adding tags and prompts to tools. |
| `config/elnis.toml` | Elnis listening hub configuration, saving HTTP, token, delivery, allowed_tools, and Elwisp policies. |
| `config/SOUL.md` | The System Prompt source file for the Agent. |
| `config/.env` | Optional, local key file, not recommended for submission; the one automatically generated the first time is `.env.example`, and `.env` will not be generated directly. |
| `config/plugins/` | Hook and plugin configuration directory. |
| `config/skills/` | User-side Skill directory, located in the configuration directory by default; current subdirectories are `skills/py/` and `skills/go/`. |
| `config/memories.toml` | Resident memory file, located in the configuration directory by default. |
| `config/long_memory/` | Long-term memory Markdown source data directory, located in the configuration directory by default. |

## Main configuration lookup order

The main configuration is searched in the following order upon startup:

1. Command line `--config`.
2. Environment variable `ELBOT_CONFIG_FILE`.
3. Platform configuration directory: Windows `%APPDATA%/ElBot/app.toml`; Linux uses the XDG configuration directory.
4. Source code directory `config/app.toml`.
5. If neither the platform configuration nor the source code example configuration exists, a default configuration file will be automatically generated in the platform configuration directory.

Automatic generation is only triggered when there are no explicit `--config` and `ELBOT_CONFIG_FILE`. If the explicitly specified configuration path does not exist, ElBot will report an error instead of silently generating it, to avoid masking path spelling errors.

Files automatically generated for the first time include: `app.toml`, `providers.toml`, `state.toml`, `SOUL.md`, `elnis.toml`, and `.env.example`; At the same time, the directories `skills/`, `skills/py/`, `skills/go/`, `plugins/`, and `long_memory/` will be created. Existing files will not be overwritten. `elnis.toml` defaults to `enabled=false`, and HTTP listening will not be started on the first run.

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
elnis = "elnis.toml"

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

[delivery]
default_platforms = ["cli"]
allow_superadmins = true

[elwisps.server-watchdog]
allowed_tokens = ["home"]
allowed_tools = ["shell"]
disabled_external_tools = ["danger_tool"]
```

Note:

- `allowed_tools` is the Elnis internal tool whitelist; Elwisps without separate configurations inherit the global default.
- If a single Elwisp configures `allowed_tools`, it will override the global default.
- External tools are allowed by default; specified external tools are only disabled when a single Elwisp configures `disabled_external_tools`.
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
