<!-- This file is auto-translated from docs/getting-started.md. Do not edit manually. -->

# Quick Start

Goal: Configure the API Key, start the CLI, and complete your first conversation.

## Environment Requirements

- Go 1.26 or a newer version.
- An LLM service: compatible with either `/chat/completions` or the Responses API (e.g., DeepSeek, OpenAI).
- At least one available API Key.

## Get the Code and Start

```bash
git clone https://github.com/Elflare/elbot/
cd elbot
go run ./cmd/elbot                            # Use the platform configuration directory
go run ./cmd/elbot --config path/to/app.toml  # Use a specified configuration
```

When running for the first time and `app.toml` is missing from the platform configuration directory, `app.toml`, `providers.toml`, `state.toml`, `SOUL.md`, `elnis.toml`, and `.env.example` will be automatically generated; Existing files will not be overwritten.

## Configure Provider

The default configuration already includes DeepSeek and OpenAI:

```toml
[providers.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"

[providers.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
models = ["gpt-4o-mini"]
```

Place the keys in system environment variables, or copy the automatically generated `.env.example` to `.env` in the configuration directory and then fill it in; do not write them directly into `providers.toml`.

```dotenv
DEEPSEEK_API_KEY=your-api-key
OPENAI_API_KEY=your-api-key
```

## Select Default Model

The runtime model is specified in `state.toml` in the configuration directory:

```toml
[session]
default_mode = "work"     # Default mode for new Sessions

[mode_models.work]        # Model for work mode; enable tools
provider = "deepseek"
model = "deepseek-chat"

[mode_models.chat]        # Model for chat mode; do not inject tools
provider = "deepseek"
model = "deepseek-chat"
```

When changing the Provider, update `provider` and `model` simultaneously. `default_mode` can only be modified manually; other settings can be configured using commands in the CLI.

## Runtime Mode

| Command | Behavior |
| --- | --- |
| `elbot` | Auto: Prioritize connecting to the CLI server, fallback to local startup when conditions are met |
| `elbot run` | Full Foreground: Local CLI interface + enabled platforms + background tasks |
| `elbot client [-c name]` | CLI client, connects to the server |
| `elbot service run` | Headless Service: Enabled platforms + background tasks, provides CLI server based on configuration |

- In auto mode, when `-c` is not explicitly specified, it will only fall back to local startup if the CLI is disabled, there are no connection configurations, or the default local service dialing fails; configuration, authentication, and handshake errors are reported directly.
- service mode does not start the local interactive interface; it remains running even if no messaging platforms are configured or all platforms have exited, until stopped externally.
- When the directly started interactive CLI exits, the associated application process will terminate.
- Only one local instance can run with the same configuration. The lock file is located next to the actual configuration (e.g., `app.toml.lock`) and is automatically released after a normal shutdown or abnormal exit, requiring no manual cleanup; Do not delete it during operation; saving or replacing the configuration file does not affect the lock already acquired.
- Normal exit takes seconds; if stuck, wait up to 30 seconds before forcing a shutdown.

## CLI TUI Keys

| Key | Action |
| --- | --- |
| `PgUp` / `PgDn` / `Home` / `End` | Scroll chat output |
| `Ctrl+K` / `Ctrl+J` | Scroll chat output slightly |
| `Ctrl+U` / `Ctrl+D` | Scroll notification area |
| `Alt+h` / `Alt+l` | Enter copy mode for chat area / notification area |
| `h/j/k/l`, `w/e/b` in copy mode | Move |
| `v` / `V`, `y`, `/` in copy mode | Select, copy, search |
| `i` or `Esc` in copy mode | Return to input |
| `#<文件名>` | Complete and reference local files; if the file does not exist, keep the original text and send it as plain `#` text |
| Drag the vertical line between the chat area and the notification area | Adjust the width of both sides in widescreen mode; settings are preserved for the current run |

## Build and Shell Completion

```bash
go build -o elbot ./cmd/elbot            # Windows: go build -o elbot.exe ./cmd/elbot
elbot completion auto|bash|zsh|fish|nushell|powershell
elbot completion fish > ~/.config/fish/completions/elbot.fish   # Example
```

`auto` guesses the shell based on current environment variables. After writing nushell to `~/.config/nushell/completions/elbot.nu`, source this file in the Nushell configuration.

## First Conversation

Simply enter a message after starting. Common getting-started commands:

| Command | Function |
| --- | --- |
| `/help` | View available commands |
| `/status` | Current Session, model, context, and request status |
| `/models` (alias `/checkmodel`) | View the list of available models |
| `/model <编号或名称>` | Switch model |
| `/chat` / `/work` | Switch between low-cost chat mode / work mode with tool access |

For more commands, see [Command Quick Reference](commands.md).

## Configuration Directory and Data Directory

Main configuration lookup order: `--config` → `ELBOT_CONFIG_FILE` → platform configuration directory (Windows `%APPDATA%/ElBot/app.toml`, Linux XDG configuration directory). Platform default configurations are automatically generated only when there are no explicit `--config` and `ELBOT_CONFIG_FILE`; An error will be thrown directly if an explicitly specified path does not exist.

Runtime data (SQLite, logs, sandbox) goes into the platform data directory by default. See [Configuration Instructions](configuration.md) for the full rules.

## FAQ

**API Key missing error at startup**: Check if `api_key_env` in `providers.toml` is correct, whether the system environment variables or configuration directory `.env` contain the corresponding Key, and whether the current shell can read these variables.

**Model unavailable or incorrect name**: Check if `provider` in `state.toml` exists in `providers.toml`, and if `model` is supported by the Provider; You can also use `/models --fresh` or `/models --refresh` after startup to reload the provider configuration and refresh the model list.

**Do not want to enable tools by default**: Change `default_mode` of `state.toml` to `"chat"`, and then `/work` when tools are needed.
