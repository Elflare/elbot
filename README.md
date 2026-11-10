<!-- Body auto-translated from README.zh-CN.md. Only the manual-header region may be edited manually. -->

<!-- manual-header:start -->
<div align="center">

<img src="assets/elbot-logo.svg" alt="ElBot Logo" width="94" height="94" align="middle" />&emsp;<img src="assets/elbot-wordmark.svg" alt="ElBot" width="102" height="38" align="middle" />

Let ElBot Feel Everything.

[![Token Efficient](https://img.shields.io/badge/Token_Efficient-365F8A?style=flat)](docs.en/concepts.md) [![Event Driven](https://img.shields.io/badge/Event_Driven-487DB5?style=flat)](docs.en/elnis.md) [![Extensible](https://img.shields.io/badge/Extensible-5B7190?style=flat)](docs.en/hooks.md)

[中文](README.zh-CN.md) | English

</div>

## Introduction

ElBot is a lightweight Agent/Chatbot framework written in Go, aiming to minimize operating costs, context costs, and maintenance complexity while preserving extensibility.
It supports general chat, tool calling, Hook extensions, long-term task scheduling, persistent Sessions, and context compaction, making it suitable for scenarios such as personal assistants, platform bots, and orchestratable automation assistants.

<!-- manual-header:end -->

## Features

### I. Lightweight and Efficient

**Extremely lightweight Go implementation**:

| Metric           | Value                            |
| -------------- | ------------------------------- |
| Local startup time   | ~10ms (tested on N5105, SATA SSD) |
| Resident memory       | ~50MB                           |
| Binary size | <30MB                           |

**On-demand tool discovery to save tokens**: Most users treat LLMs as search engines, writing assistants, or listeners; frequent tool calls are not the norm for conversations[^1][^2].

ElBot does not inject the full schema of all tools into every conversation round, exposing only `discover_tool` and the names of currently available tools; the model discovers details when needed, and the Agent injects the corresponding schema.

**Chat / Work dual mode**: The two modes can be configured with independent models: low-cost models for chatting and powerful models for complex tasks.

| Mode   | Tools               | Applicable Scenarios                                 | First Request Token Consumption      |
| ------ | ------------------ | ---------------------------------------- | -------------------------- |
| `chat` | No injection             | Chatting, companionship, lightweight Q&A, low-cost conversations         | <500 (subsequent cache hit 98%+)  |
| `work` | Enable tool discovery and invocation | Complex tasks such as search, files, commands, Cron, Skills, etc. | <1000 (subsequent cache hit 95%+) |

> Note: After using the `Responses` protocol, tool discovery via `additional_tools` no longer breaks the cache, leading to higher subsequent cache hit rates.

**Layered resident memory and long-term memory**: Resident memory only stores short and stable information injected into each round, internally distinguishing between 'core' (requiring confirmation for modification) and 'normal' (organizable); longer memories are queried by the LLM as needed via `long_memory`, using Markdown source data and SQLite FTS at the bottom to balance transparency and retrieval efficiency.

### II. Powerful and Extensible

**Extensible Hook System**: The Hook Layer covers event points such as Agent input, LLM request, LLM response, platform sending, and platform connection, allowing for message modification, appending output intents, and calling scripts. Plugins can be written in **any language**.

**Standard Cron and LLM Cron**: Standard Cron sends fixed content according to a schedule; LLM Cron uses task descriptions to drive model execution, making it suitable for scheduled tasks that require analysis, summarization, or the use of tools.

**ELyph Task Notation**: Uses structured syntax to describe the inputs, outputs, steps, conditions, and constraints of LLM Cron and native skills, which is shorter and less ambiguous than natural language Markdown, facilitating reuse and transmission by the LLM, as well as linting, auditing, and tooling.

**LLM-Creatable EL Skills**: The meta-tool `create_el_skill` allows the LLM to crystallize reusable experience into EL Skills, validating ELyph syntax during creation, with the option to include and compile Go source code.

**Compatible with External AgentSkills**: Once configured, external AgentSkill scripts following the agentskills.io style can be used as tools.

### III. Elnis Event Perception System

**The third trigger method**: Traditional Agents only wait for user input, and Cron can only respond to time; Elnis allows ElBot to be driven by external events.

**Listening Hub**: Elnis is the listening hub, and Elwisp consists of external listeners distributed everywhere. External information such as system alerts, RSS updates, Webhooks, and game events are sent to Elnis via Elwisp, then processed and returned by ElBot. See [Elnis Listening Hub](docs.en/elnis.md) for details.

### IV. Flexible Deployment and Comprehensive Sessions

**Multi-platform and Rich Output Abstraction**: ElBot abstracts the platform and output layers, currently supporting CLI, QQ OneBot, QQ Official, and Telegram. The platform layer is extensible, and other platforms can be integrated via platform adapters.

**CLI Client/Server Separation**: ElBot can function as either a client or a server. **Customizable Frontend**: You can create interfaces in any form; the images below are for illustration purposes only (except for the TUI, these are conceptual mockups and do not represent the final UI).

<p align="center">
  <img src="https://raw.githubusercontent.com/Elfreese/elbot-showcase/refs/heads/main/assets/frontend/web-light.png" width="500" />
  <img src="https://raw.githubusercontent.com/Elfreese/elbot-showcase/refs/heads/main/assets/frontend/web-dark.png" width="500" />
  <br>
  <img src="https://raw.githubusercontent.com/Elfreese/elbot-showcase/refs/heads/main/assets/frontend/web-status.png" width="500" />
</p>

For the complete frontend code and more screenshots, see [elbot-showcase/frontend](https://github.com/Elfreese/elbot-showcase/tree/main/frontend).

**Sessions, Forking, and Context Compaction**: Built-in persistent Session service supporting features such as session recovery, archiving, pinning, forking, deletion, paginated viewing, platform isolation, and compaction.

### V. Secure and Reliable

**Security Policies and Risk Confirmation**: Tools are categorized by risk level and judged in combination with role permissions. Ordinary users can only discover and call tools within the permitted range; superadmins also require confirmation when calling high-risk tools.

**Lightweight Sandbox Isolation**: Background Shell execution is constrained by an AST-level sandbox, and background tasks have independent sandbox working directories.

**Logs and Auditing**: Runtime logs, Elwisp logs, and audit logs are separated, supporting structured fields and filtered queries; permission denials, tool calls, risk confirmations, and Cron deliveries are all recorded in the audit log.

---

First-time use: Simply configure the Provider and API Key to start chatting. For detailed instructions, see:

- [Quick Start](docs.en/getting-started.md)
- [Configuration Guide](docs.en/configuration.md)
- [Command Reference](docs.en/commands.md)
- [Core Concepts](docs.en/concepts.md)
- [Elnis Listening Hub](docs.en/elnis.md)
- [Elnis Configuration and Usage](docs.en/elnis-usage.md)
- [Frontend API](docs.en/frontend-api.md)

## Development Status

ElBot is still under rapid development; interfaces, configurations, and internal implementations may continue to be adjusted. It is currently more suitable for exploration as a personal Agent/bot framework.

[^1]: Chatterji et al., [_How People Use ChatGPT_](https://www.nber.org/papers/w34255), NBER Working Paper 34255, 2025.
[^2]: Yan et al., [_ShareChat: A Dataset of Chatbot Conversations in the Wild_](https://arxiv.org/abs/2512.17843), arXiv:2512.17843, 2025.
