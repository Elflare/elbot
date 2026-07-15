<!-- This file is auto-translated from docs/getting-started.md. Do not edit manually. -->

# Quick Start

This document is used to get ElBot up and running and complete your first CLI conversation.

## Environment Requirements

- Go 1.26 or a newer version.
- An OpenAI-compatible LLM service, such as DeepSeek, OpenAI, or other services compatible with `/chat/completions`.
- At least one available API Key.

## Get the Code

```bash
git clone https://github.com/Elflare/elbot/
cd elbot
```

If you are already in this repository, you can proceed directly to the next step.

## Configure Provider

ElBot reads the main configuration by default according to the configuration lookup order. During the first run, if `app.toml` does not exist in the platform configuration directory, `app.toml`, `providers.toml`, `state.toml`, `SOUL.md`, `elnis.toml`, and `.env.example` will be automatically generated in the platform configuration directory; Existing configuration files will not be overwritten.

The default configuration already includes DeepSeek and OpenAI Providers:

```toml
[providers.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"

[providers.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
models = ["gpt-4o-mini"]
```

It is recommended to place the keys in system environment variables, or copy the automatically generated `.env.example` to `.env` in the configuration directory before filling it in; do not write them directly into `providers.toml`.

Example `.env`:

```dotenv
DEEPSEEK_API_KEY=your-api-key
OPENAI_API_KEY=your-api-key
```

## Select Default Model

The default runtime model is specified in `state.toml` of the configuration directory:

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

If using other Providers, you need to synchronously modify `provider` and `model`.
`default_mode` is set manually; other settings can be configured using commands in the CLI.

## Start ElBot

During development, you can run it directly:

```bash
go run ./cmd/elbot
```

To use a temporary configuration file, you can specify it explicitly:

```bash
go run ./cmd/elbot --config path/to/app.toml
```

Common running modes:

```bash
elbot              # Automatic mode
elbot run          # Full foreground: CLI + enabled platforms + Cron
elbot cli          # Local CLI-only: start CLI only, without platforms and Cron
elbot service run  # Linux/headless service mode: do not start CLI, start enabled platforms and Cron
```

In automatic mode, if Linux detects that the current user already has `elbot service run` running, it will enter local CLI-only mode to avoid duplicate platform connections or duplicate Cron executions; Otherwise, it will enter full foreground mode. Windows does not perform service detection and starts in full foreground mode by default.

`elbot cli` is an independent local process that uses the same set of configurations and SQLite data, but it will not take over current requests, confirmation states, or the current Session in memory from the service process. When you need to continue a historical Session, you can use `/list` and `/resume`.

Common CLI TUI keys:

- `PgUp` / `PgDn` / `Home` / `End`: Scroll chat output.
- `Ctrl+K` / `Ctrl+J`: Scroll chat output slightly; `Ctrl+U` / `Ctrl+D`: Scroll notification area.
- `Alt+h` / `Alt+l`: Enter copy mode for chat area / notification area.
- In copy mode, use `h/j/k/l` and `w/e/b` to move, `v` / `V` to select, `y` to copy, `/` to search, and `i` or `Esc` to return to input.
- `#<文件名>`: Complete and reference local files.
- In wide-screen mode, you can click and drag the vertical line between the chat area and the notification area to adjust the width of both sides; the adjustment will be preserved during the current run.

Build binary:

```bash
go build -o elbot ./cmd/elbot
```

On Windows:

```bash
go build -o elbot.exe ./cmd/elbot
```

## Shell Completion

ElBot can generate completion scripts for common shells:

```bash
elbot completion bash
elbot completion zsh
elbot completion fish
elbot completion nushell
elbot completion powershell
```

You can also use `auto` to guess the shell based on current environment variables:

```bash
elbot completion auto
```

Example: fish

```bash
elbot completion fish > ~/.config/fish/completions/elbot.fish
```

Example: nushell

```bash
elbot completion nushell > ~/.config/nushell/completions/elbot.nu
```

Then source this file in your Nushell configuration.

## First Conversation

After starting, simply enter your message. In the default example, the `default_mode` of `state.toml` is `work`, which enables tool discovery capabilities.

Common getting started commands:

```text
/help
/status
/models
/model 1
/chat
/work
```

- `/help` View available commands.
- `/status` View current Session, model, context, and request status.
- `/models` View the list of available models.
- `/model <编号或名称>` Switch models.
- `/chat` Switch to low-cost chat mode.
- `/work` Switch to work mode where tools can be used.

For more commands, see [Command Quick Reference](commands.md).

## Configuration Directory and Data Directory

Default configuration lookup order:

1. The path specified by `--config`.
2. `ELBOT_CONFIG_FILE` environment variable.
3. Platform configuration directory, such as Windows `%APPDATA%/ElBot/app.toml` or Linux XDG configuration directory.
4. If the platform configuration does not exist, the platform default configuration will be automatically generated.

Runtime data is stored in the platform data directory by default, such as SQLite, logs, and sandbox. For specific rules, see [Configuration Instructions](configuration.md).


## FAQ

### API Key missing error during startup

Check:

- Whether `api_key_env` in `providers.toml` is written correctly.
- Whether the system environment variables or the configuration directory `.env` contain the corresponding Key.
- Whether the current shell can read these environment variables.

### Model unavailable or incorrect name

Check:

- Whether `provider` in `state.toml` exists in `providers.toml`.
- Whether the name `model` is supported by the Provider.
- After starting, you can use `/models --fresh` or `/models --refresh` to refresh the model list.

### Do not want to enable tools by default

Change the default mode in `state.toml` to chat:

```toml
[session]
default_mode = "chat"
```

Enter `/work` when tools are needed.
