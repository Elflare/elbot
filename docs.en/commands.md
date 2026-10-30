<!-- This file is auto-translated from docs/commands.md. Do not edit manually. -->

# Command Quick Reference

Slash commands are handled uniformly by Agent Core, and platforms such as CLI and QQ share the same semantics. The default prefix is `/`, which can be configured in `[commands]` of `app.toml`.

## Help and Diagnostics

| Command | Function |
| --- | --- |
| `/help` | List of available commands. |
| `/help <command>` | Detailed help for a specific command, for example `/help model`. |
| `/doctor` | Read-only check of the current configuration and built-in Skills (superadmin only). |

`/doctor` uses the main configuration path selected during service startup; separated configurations are located according to `config_files`. When issues occur, it lists the actual file paths, specific issues, descriptions, and GitHub raw addresses of default templates, and generates a processing request that can be copied to ElBot; Copy and provide it to an ElBot that has access to these files for processing. The check itself does not modify files or perform automatic repairs; if there are no issues, it only returns `Everything is OK`. See [Configuration Diagnosis](configuration.md#配置诊断) for the scope.

## Model

| Command | Function |
| --- | --- |
| `/checkmodel [关键词]` (alias `/models`) | View or search for models. |
| `/models --fresh` / `--refresh` | Reload vendor configurations and force refresh the model list. |
| `/model <编号或名称>` | Switch the model used in the current Session mode. |
| `/model --chat <模型>` / `--work <模型>` | Switch models for chat / work mode. |
| `/model --elwisp<1\|2\|3> `<model>` | Switch Elnis elwisp1/2/3 model slots. |
| `/model --compact <模型>` | Switch the Chat text summary model; Responses native compression uses the current conversation model. |
| `/model --naming <模型>` | Switch the Session auto-naming model. |

Model parameters can be a list number, a model name, or `provider/model`, for example `/model --work deepseek/deepseek-chat`.

Model selection takes effect globally by mode/slot and is saved to `state.toml`; When `/model` does not specify a target, it uses the mode of the current Session; if there is no current Session, the default mode is used.

## Mode

| Command | Function |
| --- | --- |
| `/chat [消息]` | Switch to chat mode or create a new chat Session, without injecting tools. |
| `/work [消息]` | Switch to work mode or create a new work Session, enabling tool discovery and invocation. |

- When a message is provided, it switches first and then sends it as the same user message; if the mode switch is rejected, the message is not sent (e.g., `/chat 随便聊聊`).
- A work Session with existing history cannot be switched directly to chat; `/new` is required first.

## Session

| Command | Function |
| --- | --- |
| `/new` | Create and switch to a new Session. |
| `/status` | Current Session status. |
| `/sessions [关键词]` | List or search visible Sessions. |
| `/resume [编号或 session_id]` | Restore historical Session. |
| `/archives [页码] [关键词]` | List of archived Sessions. |
| `/archive [编号或 session_id] --confirm` | Archive. |
| `/unarchive [编号或 session_id]` | Unarchive. |
| `/pin` / `/unpin [编号或 session_id]` | Pin / Unpin. |
| `/rename [Number or session_id\"|Current Title] <New Title>` | Rename. |
| `/delete <编号或 session_id> --confirm` | Permanently delete. |
| `/clean --confirm` | Delete expired Sessions that are not archived or pinned. |

- Except for `/rename` and `/delete`, commands with `[编号或 session_id]` act on the current Session when parameters are omitted; IDs are displayed by `/sessions` and `/archives`, and can be reused by these commands.
- `/resume 1` directly restores the most recently updated non-current Session without needing to first execute a bare `/resume`; IDs are sorted by update time, and pagination is continuous.
- CLI is a local high-privilege entry point, allowing viewing and managing Sessions across platforms; Non-CLI platforms only see the current platform and scope. Cron/Elnis background Sessions can only be restored via private chat on the same platform by the owning user, or via the CLI management entry point; they are not displayed in group chats or channels, and cannot be bypassed via direct ID, message quoting, or unarchiving; The first recovery will permanently transfer ownership to the foreground; switching away will not restore the background identity.
- While the current Session is processing, switching commands such as `/new`, `/resume`, `/fork`, `/chat`, and `/work` cannot be executed; `/stop` is required first.
- Deletion is a permanent operation and requires `--confirm`; Sessions currently executing cannot be deleted, and automatic cleanup will also skip them.

## Fork

| Command | Function |
| --- | --- |
| `/messages [页码]` | List assistant message IDs available for forking in the current Session. |
| `/fork <message_id>` | Create a branched Session from this assistant message; the original Session remains unchanged. |

## Request Management

| Command | Function |
| --- | --- |
| `/requests` | Active requests of the current process, including turn, LLM, tool, and hook; turn displays the execution stage and the duration of that stage. |
| `/stop [request_id\|ID]` | Stop a specified request; the ID is taken from `/requests`; Stops the request of the current Session when no parameters are passed. A one-time exec Hook will terminate the entire process tree, while a persistent Worker only cancels the current call. |
| `/stopall` | Stop all active requests of the current process. |

## Context Compaction

| Command | Function |
| --- | --- |
| `/compact` | Manually compact the current Session context. |

- Automatic compaction is controlled by `[context] compact_enabled` and `compact_trigger_ratio`.
- Compaction retains the original user utterances from history and ignores tool return values; upon success, it creates and switches to an independent `原标题 compacted-N` Session, leaving the old Session unmodified; Can be canceled using `/stop`.

## Tools and Skills

| Command | Function |
| --- | --- |
| `/tools` | List registered tools and external Skills. |
| `/tools reload` | Rescan and load Skills. |
| `/tools remove <name> --confirm` (alias `/tools uninstall`) | Delete external Skills and their directories. |

`reload` first fully scans and validates candidate Skills, then replaces the current set all at once; it returns an error and retains the original tool set if there are duplicate Skill names, names that conflict with built-in tools, or if reading fails.

In work mode, the LLM can use `discover_tool` for on-demand tool discovery; In chat, `@tool:<name-or-tag>` (shorthand `@t:`) can also be used to preload tools, or `@skill:<name>` (shorthand `@s:`) can be used to add Skill documentation to the current round and preload the execution wrapper. See [Inline Preloading](concepts.md#内联预载) for syntax and examples.

## Undo File Edits

Available to superadmins only.

| Command | Function |
| --- | --- |
| `/rollback` | List undoable files: ID, absolute path, edit time, undo action. |
| `/rollback <编号>` | Directly undo the file edit corresponding to the index. |

- Each file retains the most recent successful `edit_file` modification: existing files are restored to their original content and permissions, and new files are deleted while parent directories are kept; backups are consumed after undoing, and consecutive undoes or redos are not supported.
- IDs are bound to specific modifications: editing again will generate a new ID and invalidate the old one; IDs are not reused within the process; Undo will be rejected after the file has been modified again. Undo requires that the current Session has no running tasks or compaction (you can wait for completion or `/stop`).
- Backups are stored in memory only and are isolated by user, platform Scope, and current Session: they become invalid immediately after switching or clearing the Session, cannot be recovered upon switching back, and are also lost upon restart. The entire process supports a maximum of 256 MiB of original content and 1024 records; once the limit is exceeded, the oldest records are evicted, and a notification will be provided in the edit results; Failures and previews do not replace existing backups.

## Hook

| Command | Function |
| --- | --- |
| `/hooks` | List all registered Hooks. |
| `/hooks <name>` | Detailed configuration of a specific Hook. |
| `/hooks start <id>` / `stop <id>` | Start / gracefully stop a persistent Hook. |
| `/hooks restart <id>` | Stop and restart a persistent Hook. |
| `/hooks reload` | Reread rules and persistent Hook configurations, and re-coordinate process lifecycles. |

- Rule Hooks use `name` from the configuration; built-in Hooks use `builtin.*`, such as `builtin.cron.missed_once`.
- `Description` is displayed in the list and details; rule details are only displayed in the details.
- `reload` rereads `hooks.toml` and the `hook.toml` of each plugin, rebuilds Hook registrations, and replaces affected persistent processes.
- `/hooks` is a superadmin command.

## Logs and Audit

| Command | Function |
| --- | --- |
| `/log [options]` | Runtime log. |
| `/audit [options]` | Audit log. |
| `/elwisp [name] [options]` | Elnis/Elwisp event logs. |
| `/usage [options]` | Summarize token consumption of audit logs (superadmin only). |

Options (if the "Applicable" column is omitted, it applies to all three):

| Option | Applicable | Function |
| --- | --- | --- |
| `-n, --limit <n>` | log/audit/elwisp | Number of entries to return, default is 5. |
| `--days <n>` | log/audit/elwisp | Read the last n days, default is 1. |
| `--level <level>` | log/audit/elwisp | Minimum level: `debug`, `info`, `warn`, `error`. |
| `-d, -i, -w, -e` | log/audit/elwisp | Level shortcuts (debug/info/warn/error), does not change the display format. |
| `--raw` | log/audit/elwisp | Displays the complete raw records in the file, independent of level filtering. |
| `--since <time>` / `--until <time>` | log/audit/elwisp | Time range, for example `2h`, `30m`, `2026-06-03`. |
| `--msg <text>` | log/audit/elwisp | Filter by message body. |
| `--contains <text>` | log/audit/elwisp | Filter by decoded record content or raw records. |
| `-u` / `-a` | log | Filter user / assistant events. |
| `-s, --system` | log | Filter and display `system prompt` logs. |
| `--hook` | log/audit | Filter records of `module=hook`. |
| `-t` | audit | Filter tool audits for `event=tool_call`. |
| `--run_source <来源>` | audit | `foreground`, `cron`, `elnis`; can be combined with `-t` and other conditions. |
| `--event <name>` | audit | For example, `tool_call`, `llm_usage`, `permission_denied`. |
| `--risk <level>` | audit | Risk level. |
| `--actor <id>` / `--session <id>` / `--tool <name>` | audit | Filter by actor, Session, or tool name. |
| `[name]`、`--name`、`--elwisp <name>` | elwisp | Filter by Elwisp name; three writing styles are equivalent. |
| `--source` / `--id`（= `--source-id`） | elwisp | By event source / external event ID. |
| `--mode <record\|direct\|llm>` | elwisp | event mode. |
| `--event-key <key>` / `--event-id <id>` | elwisp | Elnis event key / internal event ID. |
| `--token <name>` | elwisp | token name (excluding original text). |
| `--tag <tag>` | elwisp | Contains specified tags, can be repeated, all must match. |
| `-d, --days <n>` | usage | Summarize the last n days, default is 1. |
| `-n <x[-y]>` | usage | Summarize the x-th to y-th most recent model calls (inclusive), index 1–100000; if y is omitted, only the x-th call is taken. |
| `-m, --model <name>` | usage | Model name. |
| `-s, --session <id>` | usage | Session ID。 |
| `--by <key>` | usage | `model` (default), `day`, `session`. |
| `--since <time>` / `--until <time>` | usage | Time range. |

Example: `/log -w -n 10` · `/log --system` · `/audit --event tool_call --risk high` · `/audit -t --run_source cron` · `/elwisp server-watchdog --mode llm --since 2h` · `/usage --by day -d 30` · `/usage -n 2-4 -m gpt-4o`.

- `/usage` aggregates from `llm_usage` events in the audit log, calculating statistics for prompt, completion, total, cache, and duration, grouped by model/day/Session; Unknown, illegal, or overflow values are displayed as `null` and passed to the affected groups and totals; explicit zeros are counted normally; calls with unknown usage are still counted in the number of calls; if corrupted records are encountered during scanning, a prompt will indicate that statistics may be incomplete.
- Tool parameter logs are limited to 512 runes, and log summaries returned by tools are limited to 1024 runes; truncation ellipses are counted towards the limit (Chinese characters count as 1 rune); Actual call parameters and results returned to the model are not affected by the log length limit.

## High-risk Tool Confirmation

Available commands when triggering high-risk confirmation:

| Command | Function |
| --- | --- |
| `/detail` | Details of the tool call awaiting confirmation; supports custom plain-text details defined by the tool; if not customized, formatted parameters are displayed. |
| `/confirm` | Confirm the current tool call awaiting confirmation. |
| `/confirmtool` | Confirm the current tool. |
| `/confirmall` | Confirm all pending tools in the current batch. |
| `/reject` | Reject the current pending tool call. |

- Confirmation wait for ordinary users expires by default after 10 minutes of no valid operation; A shorter time limit is used if the Session TTL of the current group chat or private chat is shorter. Superadmins are not subject to the additional 10-minute limit, but still adhere to a Session TTL greater than 0. `/detail` will reset the timer; After expiration, the tool will not be executed, and current processing will stop.
- `/stop` can also be used to stop the current request (see [Request Management](#请求管理)). Specific prompts are subject to the runtime output.
