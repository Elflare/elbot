<!-- This file is auto-translated from README.zh-CN.md. Do not edit manually. -->

# ElBot

Chinese | [English](README.md)

ElBot is a lightweight Agent/Chatbot framework written in Go, aiming to minimize operating costs, context costs, and maintenance complexity while preserving extensibility.
 It supports general chat, tool calling, Hook extensions, long-term task scheduling, persistent Sessions, and context compaction, making it suitable for scenarios such as personal assistants, platform bots, and orchestratable automation assistants.

## Features

### 0. Lightweight and Efficient Go Implementation

ElBot currently takes about 2ms for local startup and uses approximately 20MB of resident memory.

### 1. Token-Efficient Tool Discovery Mechanism

Research shows that many average users still primarily use ChatGPT-like products as advanced search engines, writing assistants, and listening partners; frequent tool calling is not the norm for all conversations. ElBot does not inject the full schema of all tools by default in every round of conversation, but only exposes `discover_tool` and the names of currently available tools. When the model needs to use a tool, it first discovers the tool details on demand, and then the Agent injects the corresponding schema. This design reduces the invalid context overhead in the vast majority of ordinary chats.

Reference: Chatterji et al., *How People Use ChatGPT*, NBER, 2025;Yan et al., *ShareChat: A Dataset of Chatbot Conversations in the Wild*, arXiv:2512.17843, 2025。

### 2. Chat / Work Dual Mode

ElBot distinguishes between chat mode and work mode. chat mode completely removes tools, making it more suitable for daily chatting, companionship, lightweight Q&A, and low-cost conversations; work mode enables tool discovery and tool calling capabilities for complex tasks.
 The two modes can be configured with models independently, allowing low-cost models to handle casual chat and powerful models to focus on complex tasks.

### 3. Extensible Hook System

ElBot has a built-in Hook Layer, allowing extension logic to be inserted at key event points such as Agent input, LLM request, LLM response, platform sending, and platform connection.
 Hooks can modify messages, append output intents, call low-risk tools, or inject resident memory. Built-in rule Hooks, emoji Hooks, and resident memory Hooks are provided, with support for extending independent plugins in the future.

### 4. Comprehensive Log and Audit System

ElBot distinguishes between runtime logs and audit logs, supporting structured field recording, log queries, audit queries, request status viewing, and runtime debugging. Runtime logs are used to locate operational issues, while audit logs are used to track key behaviors such as tool calls.

### 5. Resident Memory and Long-term Memory Layering

ElBot is divided into resident memory and long-term memory. Resident memory only saves short, stable information that truly needs to be injected into every round, reducing token consumption; Longer and more complex memories are not forcibly auto-injected; instead, the LLM actively discovers and queries them via the `long_memory` tool when needed.

Long-term memory uses human-readable Markdown files as source data, while using SQLite FTS as a reconstructible search index, balancing transparency and retrieval efficiency.

Compared to fully automated RAG or graph-based long-term memory retrieval, ElBot's memory design is more explicit and controllable.

### 6. Standard Cron and LLM Cron

ElBot has a built-in Cron Runtime and an LLM-orchestratable Cron service. Standard Cron can send fixed content directly according to a schedule; LLM Cron allows the model to be driven by task descriptions.

### 7. ELyph: Task Representation for LLM Collaboration

ElBot introduces ELyph Task Notation to describe LLM Cron and native Skills. The goal of ELyph is to reduce ambiguity in natural language task descriptions, using shorter and more stable structures to express inputs, outputs, steps, conditions, and constraints. Compared to arbitrary Markdown, ELyph is better suited for reusing and passing tasks between LLMs, and is also easier for linting, auditing, and subsequent tooling.

### 8. El Skills that can be created by LLMs

ElBot has a built-in `create_el_skill` meta-tool, allowing LLMs to crystallize reusable experience into El Skills.

### 9. Compatible with Internet Python Skills

In addition to native El Skills, ElBot is also compatible with common external Python skill structures. Automatically scan the `SKILL.md` or `SKILL.elyph` of Python skills, read the name, description, applicable scenarios, and risk level, and execute them through hidden wrapper tools.

### 10. Multi-platform and Rich Output Abstraction

ElBot abstracts the platform and output layers, supporting CLI and QQ OneBot, with space reserved for extending to other platforms.

### 11. Session, Fork, and Context Compaction

ElBot has a built-in persistent Session service, supporting Session recovery, archiving, pinning, forking, deletion, paginated viewing, and platform isolation.

### 12. Security Policies and Risk Confirmation

ElBot's tool system has built-in risk levels, permission judgments, and high-risk confirmation processes. Regular users can only discover and call tools within the low-risk range, and superadmins also need confirmation when calling high-risk tools.

## Usage

During development, you can start the CLI directly from the source code:

```bash
go run ./cmd/elbot --config config/app.toml
```

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

Development plans and task breakdowns have been moved to [devdocs](devdocs/).

## Development Status

ElBot is still under rapid development; interfaces, configurations, and internal implementations may continue to be adjusted. It is currently more suitable for exploration as a personal Agent/bot framework.

Documentation maintenance strategy: The README only retains the project entry point and the minimum startup path; User documentation is located in `docs/`, and development plans and internal materials are located in `devdocs/`. When adding user-visible features, prioritize updating the corresponding topic documentation.
