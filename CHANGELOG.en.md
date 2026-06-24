<!-- This file is auto-translated from CHANGELOG.md. Do not edit manually. -->

# Changelog

All notable changes to ElBot will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed
- Provider configuration refactoring: removed unused `[global_default]`, removed `[model_metadata.context_windows]` global model window table; Model-level `context_window` and `extra_payload` are now unified under `[providers.<name>.model_configs."<model>"]` and looked up by `provider/model`, avoiding conflicts between models with the same name across providers.
- Added `proxy` field to Provider, supporting HTTP/SOCKS5 proxies.

### Fixed
- Fixed an issue where OpenAI-compatible streaming responses that disconnected midway but were missing `[DONE]` were treated as normal terminations; now it will explicitly notify that the LLM response was interrupted.

### Changed
- Display the current retry count via Notice when LLM connection/HTTP retriable requests fail.
- The risk level of the `finalize_el_skill` tool has been downgraded from high to medium.
- Added `match_mode` and `index` parameters to the `*_match` operation of `edit_file`: when `match_mode=line`, it matches the entire line by single-line prefix (tolerating leading indentation to avoid newline character matching errors); `content` (default) maintains exact substring semantics. When there are multiple matches, the specific match can be selected via `index`; if `index` is not provided, an error will be reported and all matching positions will be listed.

## [v0.1.0-alpha] - 2026-06-24

The first pre-release version of ElBot. A lightweight Agent/Chatbot framework aimed at personal assistants, platform bots, and orchestratable automation assistants.

### Added

- **Lightweight Core**: Implemented in Go, local startup <10ms, resident memory approximately 30MB.
- **Chat/Work Dual Mode**: chat mode disables tools, suitable for daily chatting and low-cost conversations; work mode enables tool discovery and invocation; models can be configured independently for both modes.
- **Tool Discovery Mechanism**: By default, only `discover_tool` and the tool name are exposed, with the full schema injected on demand to reduce unnecessary context overhead.
- **Session Service**: Persistent sessions supporting recovery, archiving, pinning, Forking, deletion, pagination, and platform isolation; automatic context compaction for long conversations.
- **Hook Layer**: Insert extension logic at key points such as Agent input, LLM request/response, and platform sending; built-in rule Hooks, emoji Hooks, and resident memory Hooks.
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
