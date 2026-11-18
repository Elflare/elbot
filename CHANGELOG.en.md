<!-- This file is auto-translated from CHANGELOG.md. Do not edit manually. -->

# Changelog

## Unreleased

### Added

- Added `tool_weave`, which can execute Python code and call elbot tools. Plain-text AgentSkills can be added to orchestration using `weavable = true`.
- AgentSkill TOML now supports `depends_on`; Skill tags now participate in `@t:` tool group completion and preloading.
- Added `use_workspace` to AgentSkill TOML; when enabled, command and media paths are based on the current workspace.
- AgentSkill TOML tools now support passing positional arguments in a specified order via the `positional` array, which can be used in combination with `[args]` flag parameters.
- Added static frontend hosting to the CLI remote service: automatically recognizes `frontend/index.html` next to the main configuration.
- Added a large number of CLI remote protocols.
- Added a configuration file lock to prevent multiple instances from using the same configuration file.
- Added `markdownnew` to web search.

### Changed

- `/sessions`, `/resume` lists, and `/archives` now allow superadmins to view all platforms using `-a/--all`.
- File pre-check now uses `go-udiff`. A full diff is provided when both the original and modified files are under 8 MiB, removing restrictions on the product of line counts or diff output length.
- `/messages` now uses bounded pagination for original conversations: page 1 is the newest, with messages ordered from oldest to newest within the page. Structured data retains the full body and attachment names, includes branch history, and is unaffected by compaction summaries.
- Appending input confirmation now only supports `/stop` for cancellation.
- `/status` now uses database statistics and no longer reads the full history for message counts.
- Manually guided the AI to refactor the App into the business layer package-by-package, file-by-file, function-by-function, and field-by-field, further tightening boundaries, clarifying responsibilities, and streamlining code.
- Breaking Change: Resident memory has been changed to TOML tables layered by platform and user ID. Old files are automatically backed up and migrated upon first read. Compatibility code for migration will be removed after two minor version increments following this release.
- Breaking Change: Updated the hook protocol.
- Breaking Change: Removed specialized fallback handling for models that do not support vision.
- Streamlined user prompt information and tool schema information.
- Optimized runtime log and audit log display; log filenames have been changed to `log*`.
- Modified ID formats in multiple locations.
- Hook native emoji output now uniformly uses the required `name` field; `text` is no longer used for emoji construction or fallback. Plain text display is now unified as `[Emoji: Name]`.
- Delivery information for Hook event output snapshots has been changed from the `Meta` object to the `Delivery` object, containing `Timing`, `HookPoint`, `HookName`, and `HookMode`. The derived field `AltText` has been removed.
- Active requests in `/status` no longer repeatedly display Session titles; this information is still retained when using `/requests` directly.
- Modified the Elwisp protocol to uniformly use `platform`, `api`, and `params` to describe platform native API calls.
- The `send_file` tool is now available to ordinary users, limited to media IDs and URLs.
- Optimized retry logic.
- `/resume` no longer uses numbering.
- Keywords for `/sessions` and `/archives` now require `-k/--keyword`.
- File selection via `#` now uses files within the agent workspace.
- Optimized tool warnings for the agent.
- Reading files is no longer limited to 200 lines (though the default remains 200).
- Optimized compaction logic.
- The `ast` mode of `read_file` has been changed to a Go single-file structure index, returning declaration skeletons and line ranges. It now supports limiting the number of entries and expanding type members by index. Removed `ast_function`, identifier search, and Go/Shell directory AST queries.

### Fixed

- Fixed issues where workspace cleanup of invalid directories or concurrent switching still returned the old directory state, and where cancellation errors were swallowed when Git diff output exceeded the limit.
- File reference format errors, out-of-bounds errors, and target type rejections now display the business reason and terminate input processing, rather than propagating as program failures.
- Fixed an issue where some commands reported `not found` due to the absence of a current Session after the first entry or using `/new`.
- Fixed an issue where `/resume` ignored pinned items.
- Fixed several potential bugs during refactoring.
- Fixed a bug in the chat history search tool. Both background and foreground searches are now functional and do not interfere with each other.
- Fixed a bug where duplicate content occurred during multiple compressions.

## See releases for historical change logs
