<!-- This file is auto-translated from README.zh-CN.md. Do not edit manually. -->

# ElBot

[中文](README.zh-CN.md) | English

ElBot is a lightweight Agent/Chatbot framework written in Go, aiming to minimize operating costs, context costs, and maintenance complexity while preserving extensibility.
It supports general chat, tool calling, Hook extensions, long-term task scheduling, persistent Sessions, and context compaction, making it suitable for scenarios such as personal assistants, platform bots, and orchestratable automation assistants.

## Features

### 1. Lightweight and Efficient

**Ultra-lightweight Go implementation**: ElBot's current local startup time is <10ms (N5105, SATA SSD), with resident memory of about 30MB.

**Token-efficient tool discovery**: Research shows that many ordinary users still primarily use LLM-like products as advanced search engines, writing assistants, and listening objects; frequent tool calls are not the norm for all conversations.
Reference: Chatterji et al., *How People Use ChatGPT*, NBER, 2025;Yan et al., *ShareChat: A Dataset of Chatbot Conversations in the Wild*, arXiv:2512.17843, 2025。

ElBot does not inject the full schema of all tools by default in every round of conversation, but only exposes `discover_tool` and the names of currently available tools. When the model needs to use a tool, it first discovers the tool details on demand, and then the Agent injects the corresponding schema. Greatly reduces invalid context overhead.

**Chat / Work dual mode**: ElBot distinguishes between chat mode and work mode. chat mode completely removes tools, making it more suitable for daily chatting, companionship, lightweight Q&A, and low-cost conversations; Work mode enables tool discovery and tool calling capabilities for complex tasks. The two modes can be configured with models independently, allowing low-cost models to handle casual chat and powerful models to focus on complex tasks.

**Resident memory and long-term memory layering**: Resident memory only saves short, stable information that truly needs to be injected into every round, reducing token consumption; Longer and more complex memories are not forcibly auto-injected; instead, the LLM actively queries them via the `long_memory` tool when needed. Long-term memory uses human-readable Markdown files as source data, while using SQLite FTS as a reconstructible search index, balancing transparency and retrieval efficiency.

For personal daily use, token consumption per request:

- work mode: <1000
- chat mode: <500
- Cache hit: >90%

### II. Powerful and Extensible

**Extensible Hook system**: ElBot has a built-in Hook Layer, allowing extension logic to be inserted at key event points such as Agent input, LLM request, LLM response, platform sending, and platform connection. Hooks can modify messages, append output intents, call low-risk tools, or inject resident memory. Built-in rule Hooks, emoji Hooks, and resident memory Hooks are provided, with support for extending independent plugins in the future.

**Standard Cron and LLM Cron**: ElBot features a built-in Cron Runtime and an LLM-orchestratable Cron service. Standard Cron sends fixed content directly according to a schedule; LLM Cron drives model execution using task descriptions, making it suitable for scheduled tasks that require analysis, summarization, or the use of tools.

**ELyph Task Notation**: ELyph is used to describe LLM Cron and native skills. The goal is to reduce ambiguity in natural language task descriptions and use a shorter, more stable structure to express inputs, outputs, steps, conditions, and constraints. Compared to arbitrary Markdown, ELyph is better suited for reusing and passing tasks between LLMs, and is also easier to lint, audit, and process with tools.

**EL Skills creatable by LLM**: ElBot has a built-in `create_el_skill` meta-tool, allowing the LLM to crystallize reusable experience into EL Skills. Automatically validate ELyph syntax upon creation, with optional Go source code attachment and compilation; Pure ELyph text or Go source code created is maintained by a unified `read_el_skill` / `modify_el_skill`.

**Compatible with external AgentSkills**: In addition to native Go Skills, ElBot is also compatible with external AgentSkills that follow the agentskills.io style. Automatically scan `skills/agent/<skill>/SKILL.md` or `SKILL.elyph` to read the name, description, applicable scenarios, and risk level; Currently, bundled Python scripts can be executed via hidden wrapper tools.


### III. Elnis Event Perception System

Traditional Agents usually only wait for user input; Cron can only respond to time. Elnis provides ElBot with another trigger method: external events.

Elnis is the listening hub of ElBot, Elwisp consists of external listeners distributed in various locations, and Elvena is a unified JSON over HTTP event protocol. Working together, these three allow any signal from the external world—such as server alerts, RSS updates, Webhooks, game events, or even external computer information—to be sent to Elnis, then processed and returned by ElBot.

For detailed information, see [Elnis Listening Hub](docs.en/elnis.md).

### IV. Flexible Deployment and Enhanced Sessions

**Multi-platform and Rich Output Abstraction**: ElBot abstracts the platform and output layers, currently supporting CLI, QQ OneBot, QQ Official, and Telegram, while reserving space for extending to other platforms.

**Session, Fork, and Context Compaction**: Built-in persistent Session service, supporting Session recovery, archiving, pinning, Forking, deletion, paginated viewing, and platform isolation. Long conversations automatically trigger context compaction to keep the window controllable; normal conversation can continue after compaction.

### V. Secure and Reliable

**Security Policies and Risk Confirmation**: The tool system has built-in risk levels, role permission checks, and high-risk confirmation processes. Regular users can only discover and invoke low-risk tools; even superadmins must confirm each item when invoking high-risk tools.

**Lightweight Sandbox Isolation**: Background Shell execution is subject to AST-level sandbox constraints. Tool execution comes with its own sandbox context, and artifact files are temporarily stored in an isolated directory to avoid affecting system files.

**Comprehensive Logging and Auditing**: Distinguishes between runtime logs, Elwisp logs, and audit logs, supporting structured fields, log queries, audit queries, and runtime debugging.

## Usage

During the development phase, it can be started directly from the source code; the first run will automatically generate a default configuration in the platform configuration directory, and existing configurations will not be overwritten:

```bash
go run ./cmd/elbot
```

Common startup methods:

```bash
elbot              # Automatic mode: enters local CLI-only when Linux detects a service, otherwise starts in full foreground
elbot run          # Full foreground: CLI + enabled platforms + Cron
elbot cli          # Local CLI-only: only starts CLI, does not start platforms and Cron
elbot service run  # Linux/headless service mode: does not start CLI, starts enabled platforms and Cron
```

Shell completion can be generated via `elbot completion <shell>`, supporting `bash`, `zsh`, `fish`, `nushell`, `powershell`, and `auto`.

Minimum usage flow:

1. Configure the OpenAI-compatible Provider in `config/providers.toml`.
2. Set the API Key corresponding to `api_key_env` via system environment variables or the configuration directory `.env`.
3. Select the default `chat` / `work` mode and model in `config/state.toml`.
4. After starting, enter `/help` to view commands, or start a conversation directly.

For detailed instructions, see:

- [Quick Start](docs.en/getting-started.md)
- [Configuration Guide](docs.en/configuration.md)
- [Command Cheat Sheet](docs.en/commands.md)
- [Core Concepts](docs.en/concepts.md)
- [Elnis Listening Hub](docs.en/elnis.md)
- [Elnis Configuration and Usage](docs.en/elnis-usage.md)

Development plan and task decomposition: [devdocs](devdocs/).

## Development Status

ElBot is still under rapid development; interfaces, configurations, and internal implementations may continue to be adjusted. It is currently more suitable for exploration as a personal Agent/bot framework.

