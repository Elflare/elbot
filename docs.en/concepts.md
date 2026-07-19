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

Inline preloading is used to prompt ElBot in normal input that the current task may require a certain type of tool or a specific Skill.

Its purpose is to reduce the steps for the LLM to first discover tools and then call them, allowing clear tasks to enter the working state faster. After successful preloading, the relevant tools or Skills will enter the tool context of the current Session.

Tools can be preloaded using `@tool:<name-or-tag>` or the shorthand `@t:<name-or-tag>`, and Skills can be preloaded using `@skill:<name>` or the shorthand `@s:<name>`; The colon can also be written as a full-width Chinese colon `：`.

For specific syntax and examples, see [Command Quick Reference: Tools and Skills](commands.md#工具与-skill).

## Session

Session is ElBot's persistent session unit, used to save the context, mode, message history, and tool call records of a continuous conversation.

Different platforms and different chat scopes usually use their own Sessions to avoid cross-contamination of context. The CLI is a local high-privilege entry point and can view and manage Sessions across platforms.

For operations such as creating, restoring, archiving, pinning, and deleting Sessions, see [Command Quick Reference: Session](commands.md#session).

## Fork

Fork is used to branch out a new conversation branch from a specific assistant response in the conversation history.

The new branch inherits the context before the fork point but does not modify the original Session. It is suitable for trying a different approach or reworking a solution for the same problem, or continuing exploration while preserving the original conversation.

## Context compaction

Long conversations will gradually approach the model's context window. Context compaction uses a compression model to organize the current conversation, then assembles the compression result with all historical original user messages as a new context starting point.

After successful compression, it will switch to a completely independent new Session with the title `原标题 compacted-N`. The old Session will not be modified, and there is no Fork relationship between the old and new Sessions. The user's next message will be merged with the assembled compressed content to become the first user message of the new Session. It can be triggered automatically or manually by the user.

## Prompt and Soul

Soul is the Agent's basic System Prompt, used to define personality, behavioral boundaries, and a long-term stable expression style.

The Prompt actually sent to the LLM contains not only Soul but also dynamically combines platform information, user identity, memory, tool hints, compaction summaries, and Session history based on the current Session.

Therefore, Soul is only suitable for stable rules; dynamic information such as tool discovery, time, platform context, and temporary states should not be hard-coded into it.

## Memory

ElBot divides memory into two categories:

| Type | Usage |
| --- | --- |
| Resident memory | Short, stable, and frequently useful information will be automatically injected into the conversation. |
| Long-term memory | Longer and more complex information is searched and used via tools on demand. |

Resident memory is suitable for saving information related to identity, preferences, and long-term stability. Long-term memory is suitable for saving larger volumes of data that require retrieval.

## Tool Runtime

Tool Runtime manages the registration, discovery, permissions, risk assessment, and execution of tools.

Tools can come from built-in capabilities, Skills, external extensions, or listening events. ElBot does not stuff all tool details into the context by default, but exposes them on demand through tool discovery and preloading mechanisms.

New messages received during tool execution will not interrupt the tool currently running. When the next model request has not yet started, multiple messages will be merged in order and injected into that request; If the final model request has already started and directly ends the current turn, these messages will automatically start the next turn as a new user message after the response is sent.

Common tool capabilities include:

- Search and webpage extraction.
- File read/write and file sending.
- Shell commands.
- Chat history query.
- Memory management.
- Cron management.
- Skill creation, modification, and execution.


## Security Policy

ElBot's tool system includes risk levels and permission control.

Core rules:

- Risk levels are used for internal permissions and confirmation, and are not directly exposed to the LLM.
- Regular users can only discover and call tools within the allowed risk range.
- Even superadmins require confirmation when calling high-risk tools.
- Permission denials, danger confirmations, and tool calls are recorded in the audit log.

The default local CLI user `local` is a superadmin.

## Hook

The Hook Layer is used to insert extension logic before and after the Agent's key processes, such as modifying input, supplementing context, appending output intent, or triggering external actions.

Hooks are an extension mechanism, not a permission mechanism. Tool calls, dangerous operations, and role restrictions are still determined by the Security Layer.

See [Hook: Rule Hook Configuration](hooks.md#规则-hook-配置) for full configuration and examples.

## Output Layer

Output Layer defines platform-independent output intents.

Agent, Hook, and Tool do not directly depend on specific platforms to send messages; instead, they return a unified output intent, which is then passed to the corresponding platform adapter by the Output Manager for sending.

This allows the same Agent logic to be reused across CLI, chat platforms, and future new platforms.

## Cron

ElBot includes two layers of Cron capabilities:

| Type | Description |
| --- | --- |
| Direct Cron | Send fixed content directly according to a schedule. |
| LLM Cron | Drive model execution based on task descriptions, with the ability to use tools. |

Each schedule trigger of LLM Cron creates an independent background Session, executes the task as a new input, and sends the result of the current round upon completion. Sessions can be viewed on the platform where the Cron was created via `/sessions` and `/resume`; Broadcast tasks will duplicate the Session for other target platforms; the CLI can be used to view Sessions across all platforms.



## Elnis / Elwisp / Elvena

Elnis is ElBot's listening hub, used for receiving external events. Elwisp is an external sub-listener responsible for observing the external world, such as servers, Webhooks, RSS, logs, or script outputs. Elvena is the protocol used by Elwisp to deliver events to Elnis, and it is also the action protocol reused by internal trigger sources such as Hook exec.

Their division of labor is: Elwisp observes everything, Elnis manages everything, and ElBot controls the final execution and delivery.

Elnis does not serve as a chat platform, nor does it replace Cron. Cron handles "time-triggered" tasks, while Elnis handles "external event-triggered" tasks. For a full introduction, see [Elnis Listening Hub](elnis.md); for configuration and request examples, see [Elnis Configuration and Usage](elnis-usage.md).

## Skill and ELyph

Skill is a reusable task extension of ElBot. It can be a set of task instructions for the Agent to read, or it can be wrapped as a structured tool for LLM invocation.

ElBot supports two main types of Skills:

- AgentSkill: Describes tasks in document form, allowing the LLM to complete work using general tools according to the instructions.
- Tool-based Skill: Registers the Skill as a regular tool, allowing the LLM to invoke it via structured parameters.

ELyph Task Notation is a structured representation used by ElBot to describe reusable tasks. It expresses inputs, outputs, steps, conditions, and constraints in a more stable format, reducing the ambiguity of natural language task descriptions.

For an example of AgentSkill toolization configuration, see [Configuration Guide: AgentSkill Toolization Configuration](configuration.md#agentskill-工具化配置). For the relationship between ELyph and Skill, see [ELyph Task Notation: Relationship with Skill](elyph.md#与-skill-的关系); for the complete syntax, see [Syntax Quick Reference](elyph.md#语法速查).

## Platform Adapter

The Platform Adapter is responsible for integrating specific platforms.

Its responsibility is to convert platform messages into a unified input that Agent Core can understand, and to convert the output intent of the Output Layer back into platform messages.

Therefore, Agent Core does not need to care whether messages come from the CLI, group chats, private chats, or other platforms.

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
