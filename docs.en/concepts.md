<!-- This file is auto-translated from docs/concepts.md. Do not edit manually. -->

# Core Concepts

This document explains the main concepts of ElBot to help you understand how it organizes conversations, tools, context, and extensibility.

## Agent Core

Agent Core is the conversation and orchestration center of ElBot, responsible for:

- Receiving platform input.
- Parsing slash commands.
- Managing Session context.
- Constructing Prompts.
- Calling the LLM.
- Handle the tool calling loop.
- Unify the output of the final response.

The platform adapter layer is only responsible for converting platform messages into a unified input and sending the output intent back to the platform. Agent Core does not depend on a specific platform.

## Chat / Work Dual Mode

ElBot divides conversations into two modes:

| Mode | Suitable Scenarios | Tool |
| --- | --- | --- |
| `chat` | Casual chat, companionship, lightweight Q&A, low-cost conversation | No tool injection |
| `work` | Tasks such as search, files, command, Cron, Skill, etc. | Enable tool discovery and tool calling |

The purpose of doing this:

- Ordinary chat does not pay the context cost for tool schemas.
- Work tasks can use more powerful models and tool capabilities.
- Different models can be configured for the two modes.

You can use `/chat` and `/work` to switch modes at runtime.

## Tool Discovery

ElBot does not inject the full schema of all tools by default in every round of work conversation.

The default process is:

1. The Agent only provides `discover_tool` and the names of currently available tools.
2. The LLM determines which tool is needed.
3. The LLM calls `discover_tool` to get tool details.
4. The Agent injects the discovered tool schema into subsequent requests.
5. The LLM then calls the specific tool.

This mechanism can reduce invalid context overhead in ordinary tasks and also lower interference from irrelevant tools.

## `@tool:` Preloading

Users can use `@tool:<name-or-tag>` in ordinary input to specify tools or tool tags in advance.

Example:

```text
@tool:web 帮我查一下今天的新闻摘要
@tool:files 读取这个配置并解释
```

Valid commands will be stripped and persisted to the tool cache of the current Session; invalid values will be kept as ordinary text and a prompt will be shown.

## Session

A Session is the persistent session unit of ElBot. It saves:

- Session title and mode.
- User messages and assistant replies.
- Context compaction checkpoint.
- Tool call records.
- Platform isolation information.

Common operations:

- `/new` Create a new Session.
- `/sessions` View historical Sessions.
- `/resume` Restore Session.
- `/archive` archive Session.
- `/pin` pin Session.
- `/delete` delete Session.

CLI is a local high-privilege entry point that allows viewing Sessions across platforms; other platforms, by default, only see Sessions under the current platform and scope.

## Fork

Fork is used to create a new conversation branch from a historical assistant message.

Process:

1. Use `/messages` to find the forkable assistant message ID.
2. Use `/fork <message_id>` to create a branched Session.
3. New Sessions use the context before the fork cutoff point to continue the conversation.

Forking does not delete or modify the original Session.

## Context compaction

Long conversations will gradually approach the model's context window. ElBot supports context compaction:

- Automatic compaction: controlled by `[context] compact_enabled` and `compact_trigger_ratio`.
- Manual compaction: use `/compact`.

The compacted context view typically consists of "the latest summary + new messages after the summary".

Important convention: compaction does not delete original messages; it only changes the context view sent to the LLM subsequently.

## Prompt and Soul

ElBot loads the Agent's basic System Prompt from `SOUL.md`.

The Prompt Builder combines the following content into the final request:

- Soul Prompt。
- Current platform and Actor information.
- Resident memory.
- Tool name hints.
- Compressed summary.
- Session history.

Dynamic information such as tool discovery, resident memory, and time will not be hard-coded into `SOUL.md`.

## Memory

ElBot divides memory into two categories:

| Type | Usage |
| --- | --- |
| Resident memory | Short, stable information that may be useful in every round will be injected into the Prompt. |
| Long-term memory | Longer and more complex information, using Markdown as source data, searched on-demand via tools. |

Long-term memory uses Markdown files as human-readable source data and SQLite FTS as a reconstructible search index.

## Tool Runtime

Tool Runtime manages the registration, discovery, permissions, risk assessment, and execution of tools.

Current built-in capabilities include:

- Web search and webpage extraction.
- File read and write.
- Shell commands.
- Chat history query.
- Resident memory and long-term memory.
- Cron management.
- File sending.
- Skill creation, reading, modification, and execution.
- `elwisp_creator`: Returns protocol specifications, configuration snippets, scaffolding, and test checklists for creating Elwisp for the superadmin.

Tool results can be fed back to the LLM, or return platform-independent output intents, which are sent uniformly by the Agent.

## Security Policy

ElBot's tool system includes risk levels and permission control.

Core rules:

- Risk levels are used for internal permissions and confirmation, and are not directly exposed to the LLM.
- Regular users can only discover and call tools within the allowed risk range.
- Even superadmins require confirmation when calling high-risk tools.
- Permission denials, danger confirmations, and tool calls are recorded in the audit log.

The default local CLI user `local` is a superadmin.

## Hook

The Hook Layer is used to extend behavior before and after critical processes, for example:

- Agent input processing.
- LLM request preparation.
- LLM response processing.
- Before and after platform transmission.
- Platform connection events.

Hooks can modify messages, append output intents, call low-risk tools, or inject resident memory.

Important convention: Hooks do not replace the Security Layer; security determinations are still based on the Security Layer.

## Output Layer

The Output Layer defines platform-agnostic output intents, for example:

- Text.
- Images.
- Files.
- at。
- reply。
- Emojis.

Agents, Hooks, and tools should not directly depend on specific platforms to send messages; instead, they should return an output intent, which is then sent uniformly by the Output Manager.

## Cron

ElBot includes two layers of Cron capabilities:

| Type | Description |
| --- | --- |
| Direct Cron | Send fixed content directly according to a schedule. |
| LLM Cron | Drive model execution based on task descriptions, with the ability to use tools. |

Background Cron has independent Session and sandbox constraints. LLM Cron can pre-inject tool names, allowing the model to stably use specified tools in background tasks.

## Elnis / Elwisp / Elvena

Elnis is ElBot's listening hub, used for receiving external events. Elwisp is an external sub-listener responsible for observing the external world, such as servers, Webhooks, RSS, logs, or script outputs. Elvena is the event protocol used by Elwisp to deliver events to Elnis.

Their division of labor is: Elwisp observes everything, Elnis manages everything, and ElBot controls the final execution and delivery.

Elnis does not serve as a chat platform, nor does it replace Cron. Cron handles "time-triggered" tasks, while Elnis handles "external event-triggered" tasks. For a full introduction, see [Elnis Listening Hub](elnis.md); for configuration and request examples, see [Elnis Configuration and Usage](elnis-usage.md).

## Skill and ELyph

ElBot supports Skill extensions and introduces ELyph Task Notation to describe reusable tasks.

Skill types:

- Native El Skill: uses `SKILL.elyph` to describe tasks, with optional Go source code implementation.
- Python Skill: compatible with common external Python skill directory structures.
- Go Skill: can be executed via binary and receives JSON payloads from stdin.

The goal of ELyph is to express inputs, outputs, steps, conditions, and constraints using a shorter and more stable structure, reducing the ambiguity of natural language task descriptions. For the complete syntax, see [ELyph Task Notation](elyph.md).

## Platform Adapter

Platform Adapter is responsible for integrating specific platforms. Currently, it mainly includes:

- CLI。
- QQ OneBot。
- QQ official bot.

The platform adapter layer is responsible for:

- Receiving inbound messages.
- Parsing platform features such as text, images, files, @mentions, and replies.
- Converting them into a unified input for Agent Core.
- Sending unified output intents from the Output Layer.

## Logs and Auditing

ElBot distinguishes between:

| Type | Purpose |
| --- | --- |
| runtime log | Troubleshoot runtime issues such as startup, model requests, platform connections, and persistence. |
| audit log | Track critical behaviors such as permission denials, tool calls, danger confirmations, and Cron deliveries. |

You can use `/log` and `/audit` to query at runtime.

## Development Period Conventions

ElBot is still under rapid development:

- Internal interfaces may be adjusted.
- Configurations and commands may change.
- User documentation prioritizes stable usage paths.
- Detailed development plans and task breakdowns are located in [`../devdocs/`](../devdocs/).
