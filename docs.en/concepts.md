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

## Inline Preloading

Users can specify the tools or Skills to be used in the current round in advance within a normal input.

- `@tool:<name-or-tag>`: Pre-inject the schema of a tool or tool tag.
- `@skill:<name>`: Add the content of the specified Skill document to the current user message and inject the runtime wrapper required by that Skill. The Skill entity itself is not a top-level tool schema.

Example:

```text
@tool:web 帮我查一下今天的新闻摘要
@tool:files 读取这个配置并解释
按这个技能处理文件 @skill:docx
```

Valid commands will be stripped; Tool schemas and Skill wrappers will be persisted to the tool cache of the current Session. Invalid values will be kept as plain text and a prompt will be shown. When multiple ELyph Skills are injected simultaneously, the ELyph rule description will only be added once.

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

Resident memory is internally saved as two segments, core and normal: core is used for core information, and modifications require high-risk confirmation; normal is used for general information and can be appended, overwritten, or cleared. When injecting the Prompt, segment headings will not be exposed; instead, they will be merged into a single piece of natural text.

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

Tool results can be fed back to the LLM or return a platform-agnostic output intent, which is sent uniformly by the Agent. The `Warnings` in tool results will be fed back to the LLM to suggest prioritizing more appropriate tools in the future, such as using `read_file` instead of shell `cat`. EL Skill, resident memory, and long-term memory source files are protected by FileGuard: reading will prompt the use of corresponding dedicated tools, and direct writing via general file tools or shell will be rejected.


## Security Policy

ElBot's tool system includes risk levels and permission control.

Core rules:

- Risk levels are used for internal permissions and confirmation, and are not directly exposed to the LLM.
- Regular users can only discover and call tools within the allowed risk range.
- Even superadmins require confirmation when calling high-risk tools.
- Permission denials, danger confirmations, and tool calls are recorded in the audit log.

The default local CLI user `local` is a superadmin.

## Hook

The Hook Layer is used to extend behavior before and after critical processes, such as modifying messages, appending output intents, calling low-risk tools, or injecting resident memory. Rule Hooks support conditional matching, multi-segment output, exec scripts, and role partitioning.

Important Convention: Hooks do not replace the Security Layer; security determinations are still based on the Security Layer. For full configuration and examples, see [Hook](hooks.md).


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

Background Cron has an independent Session and sandbox constraints. LLM Cron can select the background Session mode via `session_mode`: the default is `work`, and when passing `chat`, the tool schema is not injected, which is suitable for low-cost tasks that do not require tools. LLM Cron can pre-inject tool names or Skill names via `tool_list_names`: ordinary tools will inject the schema, while Skills will inject the description into the background task prompt and automatically inject the corresponding runner. All path parameters in background tasks should use relative paths within the current task working directory; The final JSON of LLM Cron can attach relative paths of images or files within the current task working directory via `report_segments`. When a superadmin quotes and replies to an LLM Cron notification message in the platform, it will automatically resume to the corresponding background Session to continue the conversation; When a regular user quotes it, it will only be processed as ordinary quoted text.

## Elnis / Elwisp / Elvena

Elnis is ElBot's listening hub, used for receiving external events. Elwisp is an external sub-listener responsible for observing the external world, such as servers, Webhooks, RSS, logs, or script outputs. Elvena is the protocol used by Elwisp to deliver events to Elnis, and it is also the action protocol reused by internal trigger sources such as Hook exec.


Their division of labor is: Elwisp observes everything, Elnis manages everything, and ElBot controls the final execution and delivery.

Elnis does not serve as a chat platform, nor does it replace Cron. Cron handles "time-triggered" tasks, while Elnis handles "external event-triggered" tasks. For a full introduction, see [Elnis Listening Hub](elnis.md); for configuration and request examples, see [Elnis Configuration and Usage](elnis-usage.md).

## Skill and ELyph

ElBot supports Skill extensions and introduces ELyph Task Notation to describe reusable tasks.

Skill types:

- AgentSkill: placed in `skills/agent/<skill>/`, following or compatible with the agentskills.io style `SKILL.md`, and `SKILL.elyph` can also be used to override the Agent-readable description; Currently, attached Python scripts can be executed via `python_skill_run`.
- Go Skill: placed in `skills/go/<skill>/`, using `SKILL.elyph` to describe the task; When a binary exists, it can be executed via `go_skill_run` and receive a JSON payload from stdin.


The goal of ELyph is to express inputs, outputs, steps, conditions, and constraints using a shorter and more stable structure, reducing the ambiguity of natural language task descriptions. Reading or modifying the `SKILL.elyph` / `main.go` of Go Skill should use `read_el_skill` / `modify_el_skill`; Direct modification of these files via general file tools or shell will be rejected. For the complete syntax, see [ELyph Task Notation](elyph.md).


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
