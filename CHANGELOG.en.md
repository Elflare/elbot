<!-- This file is auto-translated from CHANGELOG.md. Do not edit manually. -->

# Changelog

## Unreleased

### Added

- CLI remote service now supports static frontend hosting: automatically detects `frontend/index.html` next to the main configuration.
- Added a large number of CLI remote protocols.
- Added a configuration file lock to prevent multiple instances from using the same configuration file.
- Added `markdownnew` to web search.

### Changed

- `/sessions`, `/resume` lists and `/archives` now allow superadmins to use `-a/--all` to view all platforms.
- File pre-check now uses `go-udiff`; provides a full diff when both the before and after files are under 8 MiB, and no longer limits the line product or diff output length.
- `/messages` changed to bounded pagination of original conversations: Page 1 is the newest, with items ordered from oldest to newest within the page. Structured data retains the full body and attachment names, includes branch history, and is unaffected by compaction summaries.
- Additional input confirmation now only supports `/stop` when canceling.
- `/status` changed to database statistics; no longer reads full history for message counts.
- Manually guided the AI to refactor the App to the business layer packet-by-packet, file-by-file, function-by-function, and field-by-field, further tightening boundaries, clarifying responsibilities, and streamlining code.
- Breaking Change: resident memory changed to TOML tables layered by platform and user ID. Old files are automatically backed up and migrated upon first read; migration compatibility code will be removed two minor versions after the introduction version.
- Breaking Change: Changed hook protocol.
- Breaking Change: No longer performs specific fallback handling for models that do not support vision.
- Streamlined user prompt information and tool schema information.
- Optimized log and audit log display; log filenames changed to `log*`.
- Modified ID formats in multiple places.
- Hook native emoji output now uniformly uses the required `name`; `text` is no longer used for emoji construction or fallback. Plain text display is unified as `[Emoji: Name]`.
- Hook event output snapshot delivery information changed from `Meta` to `Delivery` object, containing `Timing`, `HookPoint`, `HookName`, and `HookMode`; removed derived field `AltText`.
- `/status` active requests no longer repeatedly display Session titles; this information is retained when using `/requests` directly.
- Modified Elwisp protocol to uniformly use `platform`, `api`, and `params` to describe platform native API calls.
- `send_file` tool is now available for ordinary users, limited to media IDs and URLs.
- Optimized retry logic.
- `/resume` no longer uses numbering.
- `/sessions` and `/archives` keywords now require `-k/--keyword`.
- `#` file selection now uses files in the agent workspace.
- Optimized tool warnings for agents.
- Reading files is no longer limited to 200 lines (default remains 200).
- Optimized compaction logic.

### Fixed

- Fixed issues where workspace cleanup of invalid directories or concurrent switching still returned the old directory state, and where cancellation errors were swallowed when Git diff output exceeded the limit.
- File reference format errors, out-of-bounds errors, and target type rejections now display the business reason and terminate input processing, rather than propagating as program failures.
- Fixed an issue where some commands reported `not found` due to the absence of a current Session upon first entry or after `/new`.
- Fixed an issue where `/resume` ignored pinned items.
- Fixed several potential bugs during refactoring.
- Fixed a bug in the chat history search tool. Searching now works in both the background and foreground without interfering with each other.
- Fixed a bug where duplicate content was compressed multiple times.

## See releases for historical change logs
