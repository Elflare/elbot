<!-- This file is auto-translated from docs/concepts.md. Do not edit manually. -->

# Core Concepts

This article explains the main concepts of ElBot: how it organizes conversations, tools, context, and extensibility.

## Agent Core

Agent Core is the conversation and orchestration center: receive platform input → parse slash commands → manage Session context → build Prompt → call LLM → handle tool call loops → unify and output the final response.

The platform adapter layer is only responsible for converting platform messages into a unified input and sending the output intent back to the platform. Agent Core does not depend on any specific platform.

## Chat / Work Dual Mode

| Mode | Suitable Scenarios | Tool |
| --- | --- | --- |
| `chat` | Casual chat, companionship, lightweight Q&A, low-cost conversation | No tool injection |
| `work` | Tasks such as search, files, command, Cron, Skill, etc. | Enable tool discovery and tool calling |

- Regular chat does not pay the context cost for tool schemas, nor is it interfered with by irrelevant tools.
- Different models can be configured for the two modes; a more powerful model can be selected for work tasks.

Switch at runtime using `/chat` and `/work`. The Session mode is independent of the Provider's `api_mode`. See [Session Protocol Locking](configuration.md#会话协议锁定) for the switchable scope.

## Tool Discovery

ElBot does not inject the full schema of all tools into every round of work conversation. The default process is:

1. The Agent only provides `discover_tool` and the names of currently available tools.
2. The LLM determines which tool is needed and calls `discover_tool` to get the details.
3. The Agent injects the discovered tool schema into subsequent requests, and the LLM then calls the specific tool.

The discovery process is independent of the model protocol: Chat Completions send the current tools in the top-level `tools` of the request; Responses append the first appearance of a definition as `additional_tools` to the native input, without repeating it later, while preserving the original position during full playback. Text returned by tool discovery is preserved independently; queries for the current time of cron, Skill descriptions, etc., will not be lost due to schema deduplication.

Responses limit the currently callable tools via `tool_choice` in each round; retaining a definition in the history does not mean it is currently authorized for calling. If the definition of a tool with the same name changes, a new Session must be created.

Tools can restrict the applicable API types: discovery, completion, preloading, and execution are all filtered by the current conversation model, while background tasks are determined by the model they have selected.

## View Images

`view_image` is only applicable to Responses and can be preloaded using `@tool:view_image`. Images are passed to the model as tool results and are not automatically sent to the chat.

| Parameters | Description |
| --- | --- |
| `source` | Media ID, HTTP(S) URL, or local path (including `file://`); local paths are only available to superadmins and comply with workspace, background sandbox, and sensitive file reading rules. |
| `message_id` | An array of message IDs from the current chat (can include `#`), mutually exclusive with `source`. |
| `media_index` | An array of media indices corresponding to each message, starting from 1; files also occupy an index, but only images can be selected. Example: if a message contains a file, Image A, and Image B in order, `[[2,3]]` selects the two images. |

- When `media_index` is omitted, the first image of each message is taken.
- Parameters and indices are checked collectively before retrieving images; if retrieval fails, an explanation is returned per item, while successfully retrieved images can still be viewed.
- When `view_image` and `get_media` retrieve historical media, a maximum of 5 uncached locations are attempted each time (failures are also counted); cached locations do not count towards this limit, and repeated locations in the same request reuse the result.
- `get_media` returns the Media ID, defaulting to the first media item of each message.

## Shell Output Limits

The stdout and stderr of `shell` each retain a maximum of the first 256 KiB (counted by bytes); if exceeded, a truncation notice is appended to the end of the corresponding output. The command will continue to run and drain the output; When complete large-scale output is required, write it to a file and then read it by range.

## Inline Preloading

Inline preloading is used to directly tell ElBot which type of tool or which Skill may be needed for the current task, thereby skipping the tool discovery step; after preloading, it enters the tool context of the current Session.

Use `@tool:<name-or-tag>` (shorthand `@t:`) for tools, and `@skill:<name>` (shorthand `@s:`) for Skills; the colon can also be written as a full-width `：`. See [Command Quick Reference: Tools and Skills](commands.md#工具与-skill) for syntax and examples.

## Session

A Session is a persistent session unit: the context, mode, message history, and tool call records of a single continuous conversation.

Different platforms and different scopes use their own Sessions to avoid cross-contamination of context; the CLI is a high-privilege entry point that allows cross-platform viewing and management.

Rules for creating, restoring, archiving, pinning, and deleting, as well as restoring a Session via quoted replies, can be found in [Command Quick Reference: Session](commands.md#session).

## Fork

Fork creates a new branch from a specific assistant reply: it inherits the context before the fork point without modifying the original Session. This is suitable for trying a different approach to the same problem, reworking a plan, or continuing exploration while preserving the original conversation.

## Context compaction

Triggered when a long conversation approaches the model's context window. Chat Sessions use a compression model to generate text summaries while preserving the user's original words; Responses use the native compression capabilities of the current conversation model.

Key design: after successful compression, the system switches to a **completely independent new Session** (title `原标题 compacted-N`), rather than modifying the original Session in place—the original conversation becomes immutable, traceable, and comparable.

For configuration and protocol differences, see [Context and Compression](configuration.md#context-与压缩).

## Prompt and Soul

Soul is the Agent's basic System Prompt, defining personality, behavioral boundaries, and a long-term stable expression style.

The actual Prompt sent contains not only the Soul but also dynamically combines platform information, user identity, memory, tool hints, compression summaries, and Session history based on the current Session. Therefore, Soul is only suitable for stable rules—dynamic information such as time, platform context, and tool discovery results should not be hard-coded into it.

Messages containing images will send both the images and the text labels adjacent to the images to the vision model; Tags are numbered according to the order within the message and contain stable media IDs for reference by media tools. Sessions only save media references; sent copies are converted to base64 or temporary URLs according to the configuration; The same original materials are still used after restoring the Session.

## Memory

| Type | Content | Usage |
| --- | --- | --- |
| Resident memory | Short, stable, and frequently useful information (long-term stable items such as identity, preferences, etc.). | Automatically injected into the conversation. |
| Long-term memory | Longer, more complex materials that require retrieval. | Searched and used via tools on demand. |

The reason for splitting them into two categories is cost: resident memory is included in the Prompt every round and must be very short; Long materials can only be retrieved on demand. See [Resident Memory Configuration](configuration.md#常驻记忆配置) for the length limit.

## Tool Runtime

Tool Runtime manages the registration, discovery, permissions, risk assessment, and execution of tools.

Tools can come from built-in capabilities, Skills, external extensions, or listening events. ElBot does not stuff all tool details into the context by default, but exposes them on demand through tool discovery and preloading mechanisms.

New messages received during tool execution will not interrupt the tool currently running. When the next model request has not yet started, multiple messages will be merged in order and injected into that request; If the final model request has already started and directly ends the current turn, these messages will automatically start the next turn as a new user message after the response is sent.

## System Prompt

The system prompt is assembled in real-time into a system message for each request, with the parts arranged in the following order:

1. Soul。
2. Currently available tools and Skills.
3. Activated Tool Tag prompts.
4. The current user's resident memory.
5. Meta information of the current Session.

Meta information uses a single line of plain text, for example:

```text
meta: platform=qqonebot, conversation=private, display_name="昵称"(id:1001).
meta: platform=qqonebot, conversation=group(id:9), display_name="群名片"(id:1001), session_created_at=2026-08-27T12:34:56.
```

`display_name` prioritizes the group nickname in group chats, and uses the user's nickname when no group nickname is available; Use nicknames in private chats and channels. Do not output the corresponding brackets for missing IDs; Output `display_name=""(id:xxx)` when there is no display name but there is a user ID; omit the field when both are missing.
`session_created_at` is the local time when the Session was created, precise to the second; This value remains unchanged throughout the Session lifecycle to prevent time changes from reducing the Prompt cache hit rate.

## Security Policy

Risk levels are used for internal permissions and confirmation, and are not directly exposed to the LLM; Ordinary users can only discover and call tools within the permitted risk range; Superadmins also require confirmation when calling high-risk tools; Permission denials, danger confirmations, and tool calls are all recorded in the audit log.

See [Tools and Security](configuration.md#工具与安全) for thresholds and the superadmin list.

## Hook

The Hook Layer is used to insert extension logic before and after the Agent's key processes, such as modifying input, supplementing context, appending output intent, or triggering external actions.

Hooks are an extension mechanism, not a permission mechanism. Tool calls, dangerous operations, and role restrictions are still determined by the Security Layer.

See [Hook](hooks.md) for full configuration and examples.

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

Each schedule trigger of LLM Cron creates an independent background Session, executes the task as a new input, and sends the result of the current round upon completion. The associated user can view this via `/sessions` and `/resume` in a private chat on the same platform; the CLI can manage Sessions across all platforms; Group chats and channels cannot restore background Sessions. The first restoration will permanently convert it into an ordinary foreground Session; the original task currently running will continue to wait for this execution to complete. After takeover, it will no longer report or perform supplementary delivery; The next independent trigger of the scheduled task will still use a new background Session.

## Elnis / Elwisp / Elvena

Elnis is ElBot's listening hub, used for receiving external events. Elwisp is an external sub-listener responsible for observing the external world, such as servers, Webhooks, RSS, logs, or script outputs. Elvena is the protocol used by Elwisp to deliver events to Elnis, and it is also the action protocol reused by internal trigger sources such as Hook exec.

Their division of labor is: Elwisp observes everything, Elnis manages everything, and ElBot controls the final execution and delivery.

Elnis does not serve as a chat platform, nor does it replace Cron. Cron handles "time-triggered" tasks, while Elnis handles "external event-triggered" tasks. For a full introduction, see [Elnis Listening Hub](elnis.md); for configuration and request examples, see [Elnis Configuration and Usage](elnis-usage.md).

## Skill and ELyph

A Skill is a reusable task extension: it can be a set of task instructions for an Agent to read, or it can be wrapped as a structured tool for LLM invocation.

| Type | Form |
| --- | --- |
| AgentSkill | The document describes the task, and the LLM completes it using general tools according to the instructions. |
| Tool-based Skill | Registered as a regular tool, invoked by the LLM via structured parameters. |

ELyph Task Notation is a structured representation for describing reusable tasks, using a more stable format to express inputs, outputs, steps, conditions, and constraints, thereby reducing the ambiguity of natural language descriptions.

For tooling configuration, see [AgentSkill Tooling Configuration](configuration.md#agentskill-工具化配置); for the relationship between ELyph and Skill and the complete syntax, see [ELyph Task Notation](elyph.md).

### Skill Media Usage

Media uses the stable identifier `media:<64 位小写 SHA-256>`. Standard document-based Skills can guide the Agent to pass the identifier to tools that support media.

bash scripts explicitly declare inputs via `shell`, for example:

```json
{
  "cmd": "python generate.py --input \"$ELBOT_MEDIA_1\" --output result.png",
  "media_inputs": [{"media": "media:<sha256>"}]
}
```

Each item in `media_inputs` only accepts `media`. The host exports or reuses files in the sandbox's `media-inputs/` directory and injects environment variables such as `ELBOT_MEDIA_1` and `ELBOT_MEDIA_2` for the current process. PowerShell uses `$env:ELBOT_MEDIA_1`. Scripts treat inputs as read-only and copy them before modification; Cache hits also refresh the file access time, and idle copies are cleaned up by the existing sandbox retention period. Original commands and call parameters remain unchanged.

Tool-based AgentSkill uses `{"type":"media"}` to declare media parameters in `parameters.properties` of `ELBOT_SKILL.toml`, and maps command-line flags in `[args]`. The LLM passes the media ID, and the actual process receives the file path relative to the Skill root directory during the call; Ordinary string parameters will not be automatically converted.

When calling a tool, if a JSON field uses a media ID, it will also be regarded as a reference to that Session. Plain text calls do not count.

Go Skill uses the same input list in `payload.media_inputs` of `go_skill_run`. The host supplements `path`, `name`, `mime_type`, and `size` for each item in the execution copy of stdin; files not exceeding 1 MiB also provide `base64`. `payload.media_workspace` is a call-exclusive directory relative to the Skill root directory; An empty input list can be passed to request a directory used only for output. Temporary paths and base64 are not written back to the original call parameters.

The stdout of Go/TOML Skills can return media results:

```json
{"content":"处理完成","segments":[{"type":"image","path":"result.png"}]}
```

`segments` supports `text`, `image`, and `file`; Media segments must and can only provide `media` or `path`. Paths are resolved relative to the Skill root directory; absolute paths, `..`, and symlink/junction escapes are rejected. The host imports files before returning; the Tool Transcript saves a stable media ID and establishes a reference. Call-specific directories and call-period references are released after success, failure, cancellation, or timeout; Other working files in the Skill root directory are still managed by the Skill. Go Skills are recommended to write temporary output to `media_workspace`, and TOML scripts can write to the directory where the received media input is located.

## Platform Adapter

Platform Adapter connects to specific platforms: it converts platform messages into a unified input for Agent Core and converts the output intent from the Output Layer back into platform messages. Therefore, Agent Core does not need to care whether messages come from the CLI, group chats, or private chats.

## Logs and Auditing

| Type | Purpose |
| --- | --- |
| runtime log | Troubleshoot runtime issues such as startup, model requests, platform connections, and persistence. |
| audit log | Track critical behaviors such as permission denials, tool calls, danger confirmations, and Cron deliveries. |

For query and filter options, see [Logs and Auditing](commands.md#日志和审计).

The runtime and audit logs of Agent events uniformly carry available `session_id`, `run_id`, `attempt`, `request_id`, and `root_request_id`, used to associate the same Session, logical execution, execution attempt, and parent-child requests; Fields without a corresponding identity are omitted.
