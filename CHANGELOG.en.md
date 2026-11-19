<!-- This file is auto-translated from CHANGELOG.md. Do not edit manually. -->

# Changelog

## Unreleased

### Added

- Added foreground tool `ask_user`.
- Added `tool_weave`, which can execute Python code and call elbot tools. Pure `AgentSkill` can be added to orchestration using `weavable = true`.
- `AgentSkill` TOML now supports `depends_on`; Skill tags now participate in `@t:` tool group completion and preloading.
- Added `use_workspace` to `AgentSkill` TOML; when enabled, commands and media paths are based on the current workspace.
- `AgentSkill` TOML tools now support passing positional arguments in a specified order via the `positional` array, which can be used in combination with `[args]` flag parameters.
- Added static frontend hosting to the CLI remote service: automatically recognizes `frontend/index.html` next to the main configuration.
- Added a large number of CLI remote protocols.
- Added a configuration file lock to prevent multiple instances from using the same configuration file.
- Added `markdownnew` to web search.

### Changed

- `/usage` now only summarizes existing calls within the interval; empty results display `nil`, and "insufficient records" prompts are removed. The usage API removes `available_calls` and `range_truncated`.
- Superadmins can now use `-a/--all` with `/sessions`, `/resume` lists, and `/archives` to view all platforms.
- File pre-check now uses `go-udiff`. A full diff is provided when both the before and after versions of the file do not exceed 8 MiB; limits on the product of line counts or diff output length are no longer applied.
- `/messages` now uses bounded pagination for original conversations: page 1 is the newest, and items within a page are ordered from oldest to newest. Structured data retains the full body and attachment names, includes branch history, and is not affected by context compaction summaries.
- Additional input confirmation now only supports `/stop` for cancellation.
- `/status` now uses database statistics and no longer reads the full history for message counts.
- Manually guided the AI to refactor the App to the business layer package-by-package, file-by-file, function-by-function, and field-by-field, further tightening boundaries, clarifying responsibilities, and streamlining code.
- Breaking Change: Resident memory has been changed to TOML tables layered by platform and user ID. Old files are automatically backed up and migrated upon first read. Migration compatibility code will be removed after two minor version increments following the introduction version.
- Breaking Change: Changed the hook protocol.
- Breaking Change: No longer performs specific fallback handling for models that do not support vision.
- Streamlined user prompt information and tool schema information.
- Optimized runtime log and audit log display; log filenames have been changed to `log*`.
- Modified ID formats in multiple locations.
- Hook native emoji output now uniformly uses the required `name` field; `text` is no longer used for emoji construction or fallback. Plain text display is unified as `[Emoji: Name]`.
- Delivery information for Hook event output snapshots has been changed from the `Meta` object to the `Delivery` object, containing `Timing`, `HookPoint`, `HookName`, and `HookMode`; the derived field `AltText` has been removed.
- Active requests in `/status` no longer repeatedly display the Session title; this information is still retained when using `/requests` directly.
- Modified the Elwisp protocol to uniformly use `platform`, `api`, and `params` to describe platform native API calls.
- The `send_file` tool is now available to ordinary users, limited to media IDs and URLs.
- Optimized retry logic.
- `/resume` no longer uses numbering.
- Keywords for `/sessions` and `/archives` now require `-k/--keyword`.
- Selecting files with `#` now uses files within the agent workspace.
- Optimized tool warnings for the agent.
- Reading files is no longer limited to 200 lines (default remains 200).
- Optimized compaction logic.
- The `ast` mode of `read_file` has been changed to a Go single-file structure index, returning declaration skeletons and line ranges. It now supports limiting the number of entries and expanding type members by index; `ast_function`, identifier search, and Go/Shell directory AST queries have been removed.

### Fixed

- Fixed issues related to official QQ quoted replies and @ mentions.
- Fixed issues where workspace cleanup of invalid directories or concurrent switching still returned the old directory state, and where cancellation errors were swallowed when Git diff output exceeded the limit.
- File reference format errors, out-of-bounds errors, and target type rejections now display the business reason and terminate input processing, rather than propagating as program failures.
- Fixed an issue where some commands reported `not found` due to the absence of a current Session after the first entry or using `/new`.
- Fixed an issue where `/resume` ignored pinned items.
- Fixed several potential bugs during refactoring.
- Fixed a bug in the chat history search tool. Now both background and foreground searches are available and do not interfere with each other.
- Fixed a bug where repeated content was compressed multiple times.

## See releases for historical change logs
