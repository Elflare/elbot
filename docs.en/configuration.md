<!-- This file is auto-translated from docs/configuration.md. Do not edit manually. -->

# Configuration Guide

ElBot uses a main configuration entry to load application configurations, Provider configurations, and runtime states. Except for `.env`, all configuration files are automatically generated from built-in assets to the platform configuration directory upon the first run; existing files will not be overwritten.

## Configuration File Responsibilities

| File or Directory | Responsibility |
| --- | --- |
| `app.toml` | Main configuration: `config_files` references, storage, logs, maintenance tasks, platform, security policies, etc. |
| `providers.toml` | Provider and model configurations. |
| `state.toml` | Runtime state: default Session mode, chat/work/compact/naming model selection. |
| `elnis.toml` | Elnis listening hub: HTTP, token, delivery, allowed_tools, and Elwisp policies. |
| `tool_tags.toml` | Tool tags and prompts appended after tag activation. |
| `SOUL.md` | Source of the Agent's System Prompt. |
| `memories.toml` | Resident memory. |
| `long_memory/` | Long-term memory Markdown source data. |
| `plugins/` | Hook and plugin configuration directory (`hooks.toml`, `<plugin>/hook.toml`, `.env`, `_shared/`). |
| `skills/` | User Skill directory, containing `skills/agent/` and `skills/go/`; AgentSkill can place `ELBOT_SKILL.toml` in the root directory to configure visibility or be registered as a normal tool. |
| `.env` | Optional local key file, not recommended for submission; only `.env.example` is generated the first time, and `.env` will not be generated directly. |

The above can all be placed in the platform configuration directory, or `--config` can be used to point to other locations.

## Main Configuration Lookup Order

1. Command line `--config`.
2. Environment variable `ELBOT_CONFIG_FILE`.
3. Platform configuration directory: Windows `%APPDATA%/ElBot/app.toml`; Linux uses the XDG configuration directory.

If the platform configuration does not exist, a default configuration will be automatically generated (existing files will not be overwritten). However, automatic generation is only triggered when there is no explicit `--config` or `ELBOT_CONFIG_FILE`; if an explicitly specified path does not exist, an error will be reported to avoid masking path spelling mistakes.

## Relative Path Rules

Relative paths are resolved based on the directory where the main configuration file is located, which is the platform configuration directory by default. For example, writing into `app.toml`:

```toml
[config_files]
providers = "providers.toml"
state = "state.toml"
elnis = "elnis.toml"

[soul]
path = "SOUL.md"
```

## Environment Variables and Process Environment Inheritance

ElBot's environment layers:

| Environment Layer | Provider |
| --- | --- |
| ElBot process environment | Provided by the terminal, service manager, or container at startup |
| Configuration root `.env` | Local keys and general variables |
| `plugins/.env` | Environment layer shared by all Hooks |
| `plugins/<plugin-id>/.env` | Single plugin exec and Worker |
| `<skill>/.env` | Variables exclusive to a single skill |

### Configuration Variables and Keys

Fields such as `api_key_env`, `token_env`, `client_secret_env`, `access_token_env`, `bot_token_env`, and `proxy_url_env` store environment variable names.

Read order: ElBot process environment → configuration root `.env`.

Place actual keys in the system environment or the configuration root `.env`; do not write them into TOML or commit them to the repository:

```dotenv
DEEPSEEK_API_KEY=your-api-key
TELEGRAM_BOT_TOKEN=your-bot-token
ELNIS_HOME_TOKEN=your-elnis-token
```

Note: All variables in the configuration root `.env` will be passed to the LLM Shell. Enable related tools only when you trust the current model and the Shell permission policy.

### Shell, Skill, and Hook Environments

| Process Entry Point | Environment Source and Priority |
| --- | --- |
| Built-in tools (including LLM Shell) | The process environment serves as the base, and the config root `.env` only supplements ordinary variables that do not yet exist. |
| AgentSkill TOML commands and Go Skills | Skill `.env` > process environment > config root `.env`; null values in Skill `.env` also override variables with the same name. |
| Root Hook Rules | The process environment serves as the base, and `plugins/.env` overrides ordinary variables with the same name. |
| Plugin exec and Worker | The root Hook environment serves as the base, and the plugin directory `.env` further overrides ordinary variables with the same name. |

- The Skill working directory is the Skill root directory; the merged result is passed to the child process by ElBot, so the script does not need to load `.env` on its own.
- The base environment is used during the Go Skill compilation phase, and Skill `.env` is not loaded.
- The config root `.env` is not injected into Hooks. Place shared variables in `plugins/.env` and single-plugin variables in the plugin directory `.env`; One-time exec, Persistent / Transient Worker follow the same rules.
- When a proxy is required, set `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY` in the corresponding environment layer; whether child processes use them depends on the specific program.

### PATH Rules and systemd

`PATH` does not participate in ordinary overriding, but is instead appended by layer and deduplicated:

- The base value is the process PATH obtained when ElBot starts.
- Only write the absolute directories to be added in `.env`.
- Skill execution order: process PATH → configuration root `.env` → Skill `.env`; the AgentSkill command also uses it to find executable files.

```dotenv
PATH=/home/elbot/.local/bin:/usr/local/go/bin
```

Do not write `$PATH`, `%PATH%`, or `~`—dotenv does not perform variable substitution or path expansion. Omit `PATH` when there are no additional directories.

Two systemd pitfalls:

- Use the PATH provided by the service manager when PATH is not explicitly set. `EnvironmentFile` itself does not affect PATH, but if the file contains `PATH=`, systemd will completely overwrite the original value before startup. ElBot can only append to this and cannot recover lost system directories; `EnvironmentFile` also does not expand `$PATH`.
- When ElBot reads the configuration root `.env` itself, the unit does not need to load the file repeatedly (PATH follows the append rule). If the unit uses `EnvironmentFile` to load the same file, PATH must be written as a full value. All variables within it will become part of the process environment and be inherited by Shell and Hook, losing the isolation of "configuration root `.env` is not injected into Hook".

### Effective Timing

- Configuration root `.env`, systemd environment: takes effect after restart.
- `plugins/.env`, plugin `.env`: reread during startup and `/hooks reload`; reload rebuilds Workers based on the new environment.
- If the file does not exist, it is treated as an empty configuration.

## Workspace Tools

In work mode, the superadmin can allow the LLM to call the `workspace` tool to switch the shared working directory of the current Session. After switching, path-related tools such as `read_file`, `edit_file`, `rollback_file`, `send_file`, and the foreground `shell` will resolve relative paths based on this directory.

- When no workspace is set, the process working directory is used.
- When `AGENTS.md` or `AGENT.md` is present at the root of the target directory, the content will be automatically attached to the LLM upon first discovery, injection, or first switch to that directory. The main part of the filename must be uppercase `AGENTS` / `AGENT`, the case of the `.md` suffix is unrestricted, and `AGENTS.md` takes priority.
- The attachment limit is 64 KiB. If the limit is exceeded, the content will not be read or marked as attached, and the tool result will prompt to shorten or split the content before switching again.
- Edit undo backups are saved by the target absolute path: switching the workspace does not clear backups, but the same relative path will be resolved according to the new workspace; To undo files in the original directory, you can switch back or provide the absolute path. See [Undo File Edit](commands.md#文件编辑撤销).

`read_file` tool description:

- `ast` / `ast_function` modes support single file and directory queries.
- Dialect determination: `.go` follows Go, `.mksh` follows mksh; Others are determined by the first-line Shell shebang (including `env`, `env -S`, and `env --split-string` syntax).
- `.sh`, `.bash`, and `.bats` default to Bash when no recognizable shebang is found.

`edit_file` generates the edit result and a diff preview before confirmation, and submits the same result after confirmation; if the file content or target changes during the waiting period, the submission will be rejected and must be prepared again.

## Resident Memory Configuration

Resident memory is stored in `memories.toml` within the configuration directory (generated upon the first run). The length limit is specified in the main configuration:

```toml
[resident_memory]
core_max_units = 200
normal_max_units = 300
```

| Type | Meaning | Modification Confirmation |
| --- | --- | --- |
| `core` | high-risk core memory | Regular users also need personal confirmation when modifying their own core memory. |
| `normal` | low-risk ordinary memory | No confirmation required. |

- `units` is similar to "Chinese by character count, English by word count": CJK characters are counted as 1 each, and continuous segments of English/numbers are counted as 1.
- When injecting the Prompt, the two segments are merged into a single piece of natural text.

## Provider Configuration

Provider is written in `providers.toml`:

```toml
[providers.deepseek]
api_mode = "chat"                  # Optional: chat / response, defaults to chat
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"
proxy = ""                          # Optional, HTTP/SOCKS5 proxy address
extra_payload = { provider_field = "xxx" }  # Optional, Provider-level native parameters

[providers.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
models = ["gpt-4o-mini"]             # Manually supplement the model list (used when the API cannot retrieve it)

# [providers.openai.model_configs."gpt-4o-mini"]
# context_window = 128000
# extra_payload = { }

[model_metadata]
default_context_window = 256000
```

| Field | Description |
| --- | --- |
| `api_mode` | `chat` (default) or `response`, corresponding to Chat Completions and Responses API, respectively. |
| `base_url` | Provider API base address. |
| `api_key_env` | Points to an environment variable name (recommended for saving keys). |
| `proxy` | Supports `http://` and `socks5://`; Connects directly when omitted or left blank, and **does not inherit** the process's `HTTP_PROXY` / `HTTPS_PROXY`. Applies to both the model list and chat requests. |
| `models` | A manually supplemented list of model names, used when the model list interface cannot retrieve them. |
| `model_configs."<model>"` | Configure `context_window` and `extra_payload` for a single model; both are optional. |
| `extra_payload` | Shared between Provider level and model level; fill in the native parameters supported by the API / model according to `api_mode`, which will eventually be merged into the LLM request JSON. |
| `[model_metadata].default_context_window` | Global fallback value, defaults to `256000`; used when `model_configs` has no `context_window` configured. |

### Supplementary Notes for Responses

```toml
[providers.native_text]
api_mode = "response"
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
extra_payload = { store = false, reasoning = { effort = "medium" } }
```

- `store`: Provider or model-level `extra_payload` can use `store = false` to disable server-side storage, after which the full native history will be sent. By default, server-side continuation is used; Automatically switch and log a WARN in the background when the upstream returns `store:false`.
- Tool definitions follow the `additional_tools` of native input, current calling permissions are limited by `tool_choice.allowed_tools`, and `none` is sent when no tools are available. The upstream must support these two items; there is no top-level `tools` fallback. `extra_payload.tool_choice` can only further narrow permissions; setting `required` when forcing the call of unavailable tools or when no tools exist will result in an error.
- Sessions use a versioned native input format. Old Sessions lacking format markers cannot continue conversations, be Forked, or be compressed, and must be newly created. Tool definitions within the same native history are fixed; if a schema with the same name changes, a new Session must also be created.
- Compression: Responses Sessions use the native compression of the current conversation model, without using the `/model --compact` option. Naming and text summaries for Chat Sessions are available on both clients.

### Session Protocol Locking

The protocol and provider of an existing Session are fixed during the first conversation. The provider refers to the node name of `[providers.<name>]`.

| Session Type | Switchable Range |
| --- | --- |
| Chat | Any Chat provider |
| Responses | Responses models under the same node |

A new Session must be created when crossing protocols or Responses providers. When the same node `base_url` changes, Responses rebuilds the chain using complete local native materials.

## Built-in Web Tool Configuration

| tool | Environment Variable | Behavior |
| --- | --- | --- |
| `web_search` | `TAVILY_API_KEY` | Required. |
| `web_extract` | `JINA_API_KEY` | Uses Jina Reader when configured; otherwise, falls back to direct scraping. |
| `web_extract` | `WEB_EXTRACT_PROXY` | Fixed proxy for all default `web_extract` calls, such as `http://127.0.0.1:7890`. |

The `proxy` parameter of `web_extract` is used to control the proxy for a single webpage extraction request.

## LLM Request and Round Timeout

The `[llm_request]` of `app.toml` controls streaming requests, full-round processing, and retries:

```toml
[llm_request]
first_chunk_timeout_seconds = 180
stream_idle_timeout_seconds = 60
response_timeout_seconds = 0
max_retries = 3
retry_initial_delay_seconds = 2
```

| Field | Description |
| --- | --- |
| `first_chunk_timeout_seconds` | The maximum wait time from the start of the response to the first streaming event, default is 180; suitable for models with slower first-token generation. |
| `stream_idle_timeout_seconds` | The maximum silence duration between two events during streaming, default is 60; the timer resets upon receiving each new event. |
| `response_timeout_seconds` | The total duration of a full request round, from receiving input to the end of the final response, default is 0 (unlimited); if set to a positive number, the round will stop and notify the user upon timeout. A single LLM streaming request is not limited by this. |
| `max_retries` / `retry_initial_delay_seconds` | The number of retries and the initial delay for connection or retryable HTTP failures; the delay increases according to exponential backoff. |

## CLI Remote Configuration

`[platform.cli]` saves both server-side configuration and client connection configuration: `server` is the configuration read when the current ElBot acts as a server, and `clients` is a set of connection configurations used by the client to connect to the server.

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

- When `server.enabled=true`, `elbot service run` will start the CLI WebSocket server and listen on `server.listen`.
- `server.tokens` is a list of environment variables for CLI client IDs and tokens allowed to log in.
- `clients.<name>` is the connection configuration: `id` can be omitted (defaults to `<name>`), `url` can be omitted (defaults to `default_url`); Enter the remote WebSocket address when connecting to other machines.
- `elbot client -c <name>` uses the specified connection configuration; if not specified, `default_client` is used.

## AgentSkill Tooling Configuration

AgentSkill is used as documentation by default; the risk of executing scripts according to the documentation is borne by the tools actually called, such as `shell`.

To limit the visibility of documentation-type Skills, or to register `skills/agent/<skill>/` as a regular tool, add `ELBOT_SKILL.toml` to the root directory of the Skill:

- If `command` / `parameters` / `[args]` are not specified: it only serves as a visibility configuration and is not registered as a tool.
- If any tooling field is specified: the complete tooling configuration must be provided.

Limit visibility only:

```toml
risk = "high"
superadmin_only = true
```

Register as a regular tool:

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

| Field | Required | Description |
| --- | --- | --- |
| `risk` | Required when registering as a tool | `safe`, `low`, `medium`, `high`, `critical`; if not specified for document types, it is handled as `safe`. |
| `superadmin_only` | No | When `true`, only the superadmin can discover, preload, or call it. |
| `tags` | No | Tool category, used for `@tool:<tag>` preloading. |
| `command` | Required for toolization | Command array, do not use shell strings. |
| `parameters` | Required for toolization | JSON object schema, determines the parameters seen by the LLM. |
| `[args]` | Required for toolization | Flat parameter mapping: `input = "--input"` translates parameter `input` to `--input <value>`. |
| `timeout_seconds` | No | Command timeout. |
| `expose_root` | No | Default is `false`; when `true`, the discovered Skill will expose its root path. |

- ElBot only reads `ELBOT_SKILL.toml` under the Skill root directory and does not scan recursively.
- The working directory during execution is fixed to the Skill root directory, and stdout serves as the tool result; If stdout is `{"content":"..."}` JSON, the `content` field is used.
- Media parameters are declared as `{"type":"media"}` in `parameters.properties`, and flags are still mapped via `[args]`; LLM passes `media:<sha256>`, which is exported as a temporary file path relative to the Skill root directory during host execution. Strings not declared as media are not converted. stdout also supports `segments`; see [Skill Media Usage](concepts.md#skill-使用媒体) for format and lifecycle.
- When writing via the `agent_skill` meta-tool, it is only confirmed after a full reload succeeds; if reload fails, the original TOML will be restored, or the newly created file will be deleted if it did not exist previously.

## Go Skill Compiler Path

After modifying the `code_source` of a Go skill, ElBot automatically executes `gofmt`, `go build`, and reloads. Executable search order:

1. System environment variable `ELBOT_GO_BINARY`
2. `ELBOT_GO_BINARY` in the configuration directory `.env`
3. `GOROOT/bin/go` (when the service environment is configured with `GOROOT`)
4. `go` in process `PATH`

It is recommended to specify it directly in the configuration directory `.env`:

```dotenv
ELBOT_GO_BINARY=/usr/local/go/bin/go
```

When using asdf, mise, Nix, Linuxbrew, Snap, or a custom installation path, write the actual `go` path into `ELBOT_GO_BINARY`, and do not rely on the initialization scripts of an interactive shell. It can also be specified in the systemd service:

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

| Field | Purpose |
| --- | --- |
| `group_user_ttl_minutes` | Group chat ordinary user |
| `group_superadmin_ttl_minutes` | Group chat superadmin |
| `private_user_ttl_minutes` | Private chat with ordinary users |
| `private_superadmin_ttl_minutes` | Private chat with superadmin |

- Unit: minutes; set to `0` to disable idle expiration for the corresponding scenario.
- By default, both roles in group chats and private chats with ordinary users will start a new Session after 10 minutes of idleness; private chats with superadmins do not expire.
- The "superadmin" here refers to the ElBot superadmin configured in `[security.superadmins]`; platform group owners or group administrators who are not in this list will still be treated as ordinary users.

## Model Status Configuration

`state.toml` saves the runtime model selection (the three Elwisp slots have the same structure):

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

- `default_mode`: Whether a new Session enters `chat` or `work` by default. For the difference between the two modes, see [Chat / Work Dual Mode](concepts.md#chat--work-双模式).
- `elwisp1/2/3`: Optional model slot for Elnis LLM events; Elvena requests are specified via `model_slot`, falling back to `work` when not configured.
- Runtime `/model` switches will be synchronously saved to `state.toml`.
- `state.toml` is only read at startup; manual modifications during runtime require a restart to take effect.

## Storage and Runtime Data

Storage-related configurations in `app.toml`:

```toml
[storage]
sessions_sqlite_path = ""
chat_history_sqlite_path = ""
```

When left blank, the platform's default data directory is used (Windows `%APPDATA%/ElBot/data`, Linux `$XDG_DATA_HOME/elbot` or `~/.local/share/elbot`); Runtime logs, SQLite, and sandbox are also stored according to the configuration or default directory.

Platform inbound attachment download limits are controlled by `[platform_files]`:

```toml
[platform_files]
max_receive_file_bytes = 104857600
download_timeout_secs = 60
```

- `max_receive_file_bytes`: Maximum media reception limit, default 100MB; a "media unavailable" prompt is shown if the limit is exceeded or the media cannot be retrieved.
- `download_timeout_secs`: Media download timeout, default 60 seconds; Telegram uses the platform API timeout.
- Platform media is downloaded only during actual processing and will not be stored directly in the sandbox. Pure file messages are also handled according to the unified wake-up rules.

### Media Delivery

`[file_delivery]` controls media delivery in **model requests**, calculating the total raw bytes of media for the current complete request (the same media is accumulated if it appears multiple times, but reading, uploading, and signing are reused based on the media ID).

`send_file` does not use this restriction: it only checks local paths, common file types, filenames, and MIME. The method of sending files to the chat platform is controlled by the corresponding platform configuration.

| `backend` | Behavior |
| --- | --- |
| `base64` | Stop the request when the total exceeds `max_direct_base64_bytes`, reporting the actual total and the limit. |
| `hybrid` | Use base64 for all if the limit is not exceeded; use S3 for all if it is. |
| `s3` | Use presigned download links for all; local media is uploaded on demand and reuses recorded object keys. Configuration, upload, or signing failures will report explicit errors and will not fall back to hybrid delivery. |

- S3 uses variables specified by `s3_endpoint`, `s3_region`, `s3_bucket` and `s3_access_key_env`, `s3_secret_key_env`.
- Model services are downloaded via pre-signed URLs valid for 1 hour, without the need for additional keys.
- Cloudflare R2 allows buckets to remain private without enabling public access; the downloader or relay service must be able to access the link.
- The Media Center cleans up objects according to `[maintenance.sandbox_cleanup].schedule` (default 04:00 daily) after all references have been released for 1 hour; this is enabled independently and is not affected by the sandbox cleanup switch; Records of failed deletions are kept for retry; objects still referenced by a Session or chat history will not be deleted. The expiration of a pre-signed URL only affects the link and does not delete the object.

`[media]` controls image compression upon storage:

- `llm_image_compression_threshold_bytes`: The original image byte size threshold that triggers compression, default 4 MiB.
- `llm_image_max_length`: The side length limit after compression, default 4096.
- Proportional compression is applied when the original image exceeds the threshold or any side length reaches the limit.


## Logs and Maintenance Tasks

```toml
[runtime]
log_level = "info"
log_retention_days = 30
```

- `runtime.log_level` only controls the runtime log, supporting `debug`, `info`, `warn`, and `error`.
- Audit and Elnis logs are retained independently; usage facts required by `/usage` will continue to be recorded even if the runtime level is increased.
- Log files are rotated daily with prefixes `elbot-`, `audit-`, and `elnis-`; the number of days to retain is controlled by `log_retention_days`.
- Query level and recording level are independent: `/log -d` only displays existing records, without changing configurations or recovering unrecorded content. For filtering methods, see [Log and Audit Commands](commands.md#日志和审计).
- Facility failures such as signal dispatch, background signal tasks, and log writing are reported directly to stderr (not filtered by log level); the system continues to run after reporting during runtime, but returns to the main program for handling if initialization fails.

Maintenance tasks use a Linux crontab-style 5-field Cron: `分钟 小时 日 月 星期`, scheduled by the internal Cron Runtime.

| Task | Enabled by default | Scheduled by default | Default retention_days |
| --- | --- | --- | --- |
| `[maintenance.log_cleanup]` | `true` | `0 3 * * *` | — |
| `[maintenance.session_cleanup]` | `false` | `15 3 * * *` | 30 |
| `[maintenance.sandbox_cleanup]` | `true` | `0 4 * * *` | 7 |
| `[maintenance.chat_history_cleanup]` | `true` | `35 4 * * *` | 180 |

```toml
[maintenance.sandbox_cleanup]
enabled = true
schedule = "0 4 * * *"
retention_days = 7
```

## Context and Compaction

```toml
[context]
compact_enabled = true
compact_trigger_ratio = 0.8
```

- When enabled, context is automatically compacted when it approaches the window limit; it can also be manually triggered using `/compact`.
- After successful compaction, it switches to a new independent Session without modifying the history of the original Session.
- Both protocols share the aforementioned switches, thresholds, and window configurations of the current model. Responses call the `/responses/compact` of the current conversation model, using the entire native return window as the starting point for the new Session.

The model window is configured in `model_configs` of `providers.toml`:

```toml
[providers.deepseek.model_configs."deepseek-chat"]
context_window = 64000

[model_metadata]
default_context_window = 256000
```

- `[model_metadata].default_context_window` is the global fallback value, defaulting to `256000`, and is used when `context_window` is not configured in the model block.

## Command Prefix

```toml
[commands]
prefixes = ["/"]
```

Default is `/`; add other prefixes here if needed.

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

- Regular users can only discover and call tools within the allowed risk range; after permission verification, risk calls via `high` / `critical` still require confirmation from the current user.
- `OwnerScoped` tools can only access the caller's own data, so they can be called by ordinary users; however, `high` / `critical` operations still require confirmation from the user themselves.
- The superadmin's confirmation threshold is configured by `superadmin_confirm_risk`.
- The default local CLI user `local` is a superadmin.

### `tool_tags.toml`

Independent configuration file, the path is specified by `[config_files].tool_tags`, relative to the directory where `app.toml` is located:

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

| Field | Description |
| --- | --- |
| `[tags.<tag-name>]` | Define a tag, for example, `[tags.agent]` corresponds to `@tool:agent` / `@t:agent`. |
| `tools` | The list of preloaded tool names for this tag; they must be registered tools that the current user has permission to access. |
| `prompt` | The tool usage strategy appended to the system prompt after the tag is activated; it is shown directly to the model without automatically adding a tag title. |

Usage: `@tool:agent 帮我检查这个项目的问题` (or `@t:agent`) will preload the tools under `agent` into the current Session; When `prompt` is not empty, it will be appended to the system prompt starting from this round.

- Configured tags are appended to the built-in tags, not overwritten.
- The prompt for the tag is activated in this Session only after `@tool:<tag>` matches at least one tool.
- Using `@tool:<tool-name>` directly only preloads the specified tools and does not activate the tag prompt.
- Activated tags are written to the Session metadata and remain effective after `/resume`.
- `prompt` is dynamically read from the file each time; changes will affect subsequent requests (similar to `SOUL.md`).
- Preloading existing tools repeatedly will not add them again, and the platform will prompt `已存在工具：<name>`.
- `prompt` should describe specific tool usage strategies; do not write configuration mechanisms that the model does not need to know, such as "the current tag is xxx".

## Elnis listening hub

Elnis is disabled by default; Once enabled, ElBot starts a local HTTP ingress to receive events delivered by Elwisp according to the Elvena protocol. It is recommended to split the configuration into a separate `elnis.toml`, and only keep the entry point in `app.toml`:

```toml
[config_files]
elnis = "elnis.toml"
```

- Internal tool whitelists, external tool switches, delivery disabling, tokens, and Elwisp policies are all configured in `elnis.toml`.
- Logs only record the token name and do not write the original token text.
- Supports three modes: `record`, `direct`, and `llm`; `llm` can use `model_slot` to select `elwisp1/2/3`, and falls back to `work` when not configured.

For complete fields, examples, and security boundaries, see [Elnis Configuration and Usage](elnis-usage.md).

## Platform Configuration

CLI enabled by default:

```toml
[platform.cli]
enabled = true
```

QQ Official Bot, QQ OneBot, and Telegram are commented out in the default examples; when enabling them, you must provide their respective authentication information and trigger keywords. Media downloads are restricted according to `[platform_files]`.

### QQ Official Bot

```toml
[platform.qqofficial]
enabled = true
app_id = "your-app-id"
client_secret_env = "QQOFFICIAL_CLIENT_SECRET"
trigger_keywords = ["bot"]
```

```dotenv
QQOFFICIAL_CLIENT_SECRET=your-client-secret
```

- `client_secret_env` points to the environment variable name where the Client Secret is stored; You can also write it directly using `client_secret`, but it is not recommended to commit the actual Secret.
- Handles private chats, @ mentions in groups, and ordinary group messages actually delivered by the platform.
- Group chats follow the unified wake-up rules: slash commands, `trigger_keywords`, @ mentioning the bot, or quoting the bot's historical replies will trigger a response; ordinary group messages that are not triggered are still written to the chat history.
- Whether ordinary group messages are delivered depends on the group message capabilities enabled for the bot by the QQ Open Platform.

### QQ OneBot

```toml
[platform.qqonebot]
enabled = true
ws_url = "ws://127.0.0.1:6700/"
access_token_env = "QQONEBOT_ACCESS_TOKEN"
api_timeout_seconds = 15 # Base timeout for writing and response waiting
trigger_keywords = ["bot"]
send_file_mode = "base64" # Local images/files/voice are base64 by default; file_uri can be used for shared file systems
```

- `access_token_env` points to the environment variable name where the Access Token is stored; The original `access_token` remains compatible and takes priority. Both items can be omitted when authentication is not required.
- `send_file_mode` simultaneously controls the sending of local images, files, and `record` voice messages: `base64` is suitable for deployments that do not share a file system, while `file_uri` is only applicable when both parties can access the same path.
- `api_timeout_seconds` applies to frame writing and API response waiting, respectively. For large frames, 1 second is added for every full 1 MiB of encoded size, up to the base timeout itself; ElBot disconnects and reconnects after a write failure or timeout to prevent a single slow send from blocking subsequent messages. Sending still synchronously waits for the platform receipt.

### Telegram

Uses Bot API long polling.

```toml
[security.superadmins]
cli = ["local"]
telegram = ["123456789"]

[platform.telegram]
enabled = true
bot_token_env = "TELEGRAM_BOT_TOKEN"
proxy_url_env = "TELEGRAM_PROXY_URL" # Optional
trigger_keywords = ["bot"]
format = "html" # html/plain/rich
stream_edit_interval_milliseconds = 250
```

| `format` | Behavior |
| --- | --- |
| `html` (default) | Standard `sendMessage` + `parse_mode="HTML"`, lightweight conversion of common Markdown to HTML; Supports readable rendering of headings, quotes, horizontal rules, code blocks, and tables. |
| `plain` | Disable formatting and send plain text only. |
| `rich` | Experimental: Use `sendRichMessage` / Private Chat `sendRichMessageDraft`; some clients may not be able to see them. |

- `bot_token_env` / `proxy_url_env` point to environment variable names; `bot_token` / `proxy_url` can also be used to write values directly (not recommended to commit actual values). Proxy examples: `http://127.0.0.1:7890`, `socks5://127.0.0.1:1080`.
- `stream_edit_interval_milliseconds`: Throttling interval for streaming updates, defaults to 250ms, to avoid triggering platform rate limits.
- After a successful connection, built-in slash commands will be synchronized to the bot command menu; only the main command names are synchronized, not the aliases.
- In group chats/supergroups, command prefixes, trigger keywords, `@bot_username`, or replying to bot messages will trigger processing; private chats are processed by default.
- Confirmation messages for high-risk tools include an inline keyboard; clicking them will convert to existing confirmation commands such as `/confirm` and `/reject`.
- Fill in the Telegram user ID or a private chat ID that can be sent to directly in `security.superadmins.telegram`, used for superadmin permissions and notification delivery.

## Plugin and Hook Configuration

Plugin configurations are fixed under `plugins/` in the configuration directory:

| Path | Purpose |
| --- | --- |
| `plugins/hooks.toml` | Rule Hook configuration. |
| `plugins/<plugin-id>/hook.toml` | Plugin Hooks referenced by `hooks.toml`, which may include `[plugin.runtime]` persistent runtime configuration. |
| `plugins/_shared/` | A cross-Hook file collaboration directory created by ElBot, not used for plugin scanning. |

Hooks should not send platform messages directly; they should return an output intent, which the Agent will then pass to the Output Manager for sending. For full configuration, see [Hook](hooks.md).

## Configuration Diagnosis

The superadmin can execute `/doctor` in the chat window to perform a read-only check of the main configuration currently used by the service and the separated configuration pointed to by `config_files` (supporting custom main configuration file names and relative/absolute paths; `SOUL.md` is located by `soul.path`).

Check scope:

- Whether files in the default asset manifest are missing.
- The format, duplicate definitions, and field types of the configuration TOML; `app.toml` additionally checks for unknown fields.
- The TOML format of `ELBOT_SKILL.toml` for built-in Skills.
- Content differences between the built-in `SKILL.md` and its accompanying `ELBOT_SKILL.toml`; Skills added by the user are not included in the comparison.
- SOUL, memory, and environment files are only checked for existence.

`/doctor` is only available in chat services that have been started. If a configuration error prevents startup, you must first correct it according to the startup output.
