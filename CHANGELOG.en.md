<!-- This file is auto-translated from CHANGELOG.md. Do not edit manually. -->

# Changelog

All notable changes to ElBot will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unrelease]

### Changed

- `[tool]` previews for multiple tool calls in the same round will be merged into a single message to reduce platform spam.
- Elvena LLM events and LLM Cron now support `session_mode=chat|work` selecting background Session mode, with the default remaining `work`.
- `/detail` high-risk tool call details now support custom plain text display for tools; When not customized, JSON parameters will still be formatted into a more readable multi-line display, and `\n` within strings will be displayed as actual line breaks.
- High-risk confirmation details for `edit_file` now display operations such as replace, add, delete, and match by file, mode, and editing step.
- `edit_file` no longer exposes the `dry_run` parameter to the LLM; the system will automatically pre-check and generate a diff before user confirmation, and if the pre-check fails, it will not proceed to confirmation or write to the file.
- `modify_el_skill` now reuses the `edits` editing instructions and execution capabilities of `edit_file`, and pre-checks edits, ELyph syntax, and no-op modifications before confirmation, displaying the pre-check diff in the high-risk confirmation details.
- Update `ELyph` version to v3
- qq heartbeat ack and qqofficial gateway resumed are no longer logged
- read_el_skill now depends on modify_el_skill to facilitate possible modifications

### Fixed

- `long_memory_write`'s `update` now supports updating meta via filled fields, and `content_edits` has been added to reuse the edit operation of `edit_file` to modify the body; A pre-check will be performed automatically and the diff will be displayed before confirmation.
- Fixed the issue where `edit_file` fails during the write confirmation stage when creating a new file using `create=true` if the target parent directory does not exist.
- `response_timeout_seconds` now controls the total duration of a full round of user requests; by default, `0` indicates no time limit; A single LLM streaming request is controlled only by the first packet and idle timeout.

## [v0.2.0-alpha - 2026-06-27]

### Added

- Elvena v3 action channel: Elnis supports `calls`, initially supporting raw platform APIs as well as `message.recall`, `member.mute`, and `chat.leave` capabilities; unsupported ones can directly call the messaging platform API. Hook rules can execute scripts via the `exec` action and use `stdout=elvena` to trigger Elnis direct/LLM/calls via the internal Elvena Bus. Direct calls-only requests will not send additional messages.
- Added `match_mode` and `index` parameters to the `*_match` operation of `edit_file`: when `match_mode=line`, it matches the entire line by single-line prefix (tolerating leading indentation to avoid newline character matching errors); `content` (default) maintains exact substring semantics. When there are multiple matches, the specific match can be selected via `index`; if `index` is not provided, an error will be reported and all matching positions will be listed.
- Hook rules added role partitioning and tiling control fields: `roles`, `actor_roles`, `group_roles`, `consume`, `stop_propagation`. Platform message Hook output will now be sent; `consume=true` can block subsequent command/LLM processing.
- Hook rules `send` action added `segments` list, supporting multi-type multi-segment output (text/image/file/emoticon, including url/path/base64), with a format unified with Elvena segments.
- Hook rules `exec` action added `outputs` stdout mode; script stdout is parsed as JSON to extract the `outputs` array and optional `text`; When `field` is set, `text` overwrites the corresponding fields; otherwise, the original text remains unchanged.
- Platform inbound context added a unified group identity `owner/admin/member/unknown`; QQ OneBot and Telegram will map group owner/administrator/ordinary member.
- Hook platform context now populates the current platform message ID `platform.message_id` and the referenced/reply target message ID `platform.reply_to_message_id`, facilitating the processing of referenced messages by rule Hooks, such as recalling a referenced message.
- `/hooks` command: list all registered Hooks, view detailed configuration of a specific Hook, and hot-reload all Hooks (takes effect after modifying `hooks.toml` without requiring a restart).

### Changed

- LLM request timeout configuration changed to `first_chunk_timeout_seconds`, `stream_idle_timeout_seconds`, and `response_timeout_seconds`; the old `timeout_seconds` has been removed; By default, the wait time for the first streaming event is 180 seconds, streaming idle is 60 seconds, and there is no total duration limit for the entire response.
- Provider configuration refactoring: removed unused `[global_default]`, removed `[model_metadata.context_windows]` global model window table; Model-level `context_window` and `extra_payload` are now unified under `[providers.<name>.model_configs."<model>"]` and looked up by `provider/model`, avoiding conflicts between models with the same name across providers.
- Added `proxy` field to Provider, supporting HTTP/SOCKS5 proxies.
- Emoticon Hook changed from an embedded plugin to a rule Hook example; the emoticon plugin and `emoticon.toml` assets are no longer built-in.
- Display the current retry count via Notice when LLM connection/HTTP retriable requests fail.
- The risk level of the `finalize_el_skill` tool has been downgraded from high to medium.

### Fixed

- Fixed an issue where long tool chains would be silently stopped by the Agent's internal 5-minute default request timeout; users will now be notified upon a full round timeout.
- Fixed an issue where API timeouts when sending images/emojis/files via QQ OneBot might cancel WebSocket writes and trigger disconnection and reconnection; a text notification will now be attempted to the same target when media sending fails.
- Fixed an issue where OpenAI-compatible streaming responses that disconnected midway but were missing `[DONE]` were treated as normal terminations; now it will explicitly notify that the LLM response was interrupted.
- Fixed an issue where OpenAI-compatible streaming requests used a single HTTP timeout, causing them to be incorrectly interrupted when the model's first token was slow or long outputs exceeded 60 seconds.


## [v0.1.0-alpha] - 2026-06-24

The first pre-release version of ElBot. A lightweight Agent/Chatbot framework aimed at personal assistants, platform bots, and orchestratable automation assistants.

### Added

- **Lightweight Core**: Implemented in Go, local startup <10ms, resident memory approximately 30MB.
- **Chat/Work Dual Mode**: chat mode disables tools, suitable for daily chatting and low-cost conversations; work mode enables tool discovery and invocation; models can be configured independently for both modes.
- **Tool Discovery Mechanism**: By default, only `discover_tool` and the tool name are exposed, with the full schema injected on demand to reduce unnecessary context overhead.
- **Session Service**: Persistent sessions supporting recovery, archiving, pinning, Forking, deletion, pagination, and platform isolation; automatic context compaction for long conversations.
- **Hook Layer**: Inserts extension logic at key points such as Agent input, LLM request/response, and platform sending; includes built-in rule Hooks and resident memory Hooks.
- **Standard Cron and LLM Cron**: Standard Cron sends fixed content directly according to a schedule; LLM Cron uses ELyph task descriptions to drive model execution, supporting one-time and periodic tasks, missed run recovery, and broadcasting.
- **ELyph Task Notation**: A structured task description language used for LLM Cron and native Skills to reduce natural language ambiguity.
- **Native and External Skills**: The `create_el_skill` meta-tool supports LLMs in creating native EL Skills (pure ELyph or accompanied by Go source code and compiled); Compatible with agentskills.io style external AgentSkills (accompanied by Python scripts).
- **Elnis listening hub**: Receives external events delivered by Elwisp via the Elvena HTTP protocol, supporting three modes (record/direct/llm) and multi-target delivery.
- **Multi-platform adapter**: CLI (including client/server separation and remote connection), QQ OneBot v11, QQ Official Bot, Telegram Bot API.
- **Security Policy**: Tool risk grading, role permission verification, high-risk confirmation workflows, and a lightweight background shell sandbox.
- **Memory System**: Resident memory (core/normal layers) injected by platform and actor; long-term memory based on Markdown source data and SQLite FTS retrieval.
- **Logs and Audit**: Runtime logs, audit logs, and Elnis logs are separated, supporting structured fields and date-based rotation.
- **SQLite Persistence**: Unified storage for Sessions, messages, context summaries, tool call records, Cron jobs, and Elnis events.

### Known Limitations

- MCP tools, sub-Agents, and full multimodality (actual model input for voice, video, and files) are not yet implemented.
- Interfaces, configurations, and internal implementations may still be adjusted; it is more suitable for exploratory use as a personal Agent/bot framework.
