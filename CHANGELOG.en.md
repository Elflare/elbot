<!-- This file is auto-translated from CHANGELOG.md. Do not edit manually. -->

# Changelog

## Unreleased

### Added

- QQ official adapter saves sender avatars.
- Added `tool_weave` and disabled `py` in `shell`; it can execute Python code and call elbot tools.
- Foreground `shell` and `tool_weave` now support asynchronous execution. Skills can also be configured as asynchronous.
- Added foreground tool `ask_user`, asynchronous by default.
- Added draft tool `draft`, allowing agents to read, write, and execute at any time.
- Added `depends_on`, `use_workspace`, `tags`, `positional`, and `weavable` to AgentSkill TOML.
- Added static frontend hosting to CLI remote service: automatically recognizes `frontend/index.html` next to the main configuration.
- Added a large number of CLI remote protocols.
- Added configuration file locking to prevent multiple instances from using the same configuration file.
- Added `markdownnew` to web search.

### Changed

- Breaking Change: Resident memory changed to TOML tables layered by platform and user ID; old files are automatically backed up and migrated upon first read. Compatibility code for migration will be removed after two minor version increments following the introduction version.
- Breaking Change: Changed the hook protocol.
- Breaking Change: No longer providing specific fallback handling for models that do not support vision.
- Breaking Change: Skill directory adjusted to `skills/<name>/`; removed Go-exclusive skills.
- Responses now send `store:false` and the full native window by default; incremental chaining is enabled only when `store:true` is explicitly set.
- Optimized Markdown sending logic for the official QQ platform adapter.
- `/usage` now only summarizes existing calls within the interval, displaying `nil` for empty results, and removes the "insufficient records" prompt. The usage API removes `available_calls` and `range_truncated`.
- Superadmins can now use `-a/--all` with `/sessions`, `/resume` lists, and `/archives` to view all platforms.
- File pre-check now uses `go-udiff`; a full diff is provided when both the before and after files are under 8 MiB, removing restrictions on the product of line counts or diff output length.
- `/messages` changed to bounded pagination for raw conversations: page 1 is the newest, with entries ordered from oldest to newest within the page. Structured data retains the full body and attachment names, includes branch history, and is unaffected by context compaction summaries.
- Additional input confirmation now only supports `/stop` for cancellation.
- `/status` now uses database statistics and no longer reads the full history for message counting.
- Manually guided the AI to refactor the App to the business layer packet-by-packet, file-by-file, function-by-function, and field-by-field, further tightening boundaries, clarifying responsibilities, and streamlining code.
- Streamlined user prompt information and tool schema information.
- Optimized runtime log and audit log display; log filenames changed to `log*`.
- Modified ID formats in multiple locations.
- Hook native emoji output now uniformly uses the required `name`; `text` is no longer used for emoji construction or fallback. Plain text display is unified as `[Emoji: Name]`.
- Delivery information for Hook event output snapshots changed from the `Meta` object to the `Delivery` object, containing `Timing`, `HookPoint`, `HookName`, and `HookMode`; removed the derived field `AltText`.
- Active requests in `/status` no longer repeatedly display Session titles; this information is retained when using `/requests` directly.
- Modified the Elwisp protocol to uniformly use `platform`, `api`, and `params` to describe native platform API calls.
- The `send_file` tool is now available to ordinary users, limited to media IDs and URLs.
- Optimized retry logic.
- `/resume` no longer uses numbering.
- Keywords for `/sessions` and `/archives` now require `-k/--keyword`.
- Using `#` to select files now uses files within the agent workspace.
- Optimized tool warnings for the agent.
- Reading files is no longer limited to 200 lines (default remains 200).
- Optimized compaction logic.
- The `ast` mode of `read_file` has been changed to a Go single-file structure index, returning declaration skeletons and line ranges, with support for limiting the number of entries and expanding type members by index; removed `ast_function`, identifier search, and Go/Shell directory AST queries.
- Optimized chat history; the assistant can now see its own sent content.

### Fixed

- Corrected `/doctor` help and command documentation.
- Fixed issues related to official QQ quoted replies and @ mentions.
- Fixed issues where workspace cleanup of invalid directories or concurrent switching still returned the old directory state, and where cancellation errors were swallowed when Git diff output exceeded the limit.
- File reference format errors, out-of-bounds errors, and target type rejections now display the business reason and terminate input processing, rather than propagating as program failures.
- Fixed an issue where some commands reported `not found` due to the absence of a current Session after first entry or using `/new`.
- Fixed an issue where `/resume` ignored pinned items.
- Fixed several potential bugs during refactoring.
- Fixed a bug in the chat history search tool. Now both background and foreground searches are functional and do not interfere with each other.
- Fixed a bug where repeated content was compressed multiple times.

## See releases for historical change logs
