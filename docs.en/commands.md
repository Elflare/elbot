<!-- This file is auto-translated from docs/commands.md. Do not edit manually. -->

# Command Quick Reference

ElBot's slash commands are handled uniformly by Agent Core; CLI, QQ, and subsequent platforms share the same semantics. The default command prefix is `/`, which can be configured in `[commands]` of `app.toml`.

## Help

| Command | Function |
| --- | --- |
| `/help` | View the list of available commands. |
| `/help <command>` | View detailed help for a specific command. |

Example:

```text
/help
/help model
/help log
```

## Model

| Command | Function |
| --- | --- |
| `/models` | View the model list. |
| `/models --fresh` or `/models --refresh` | Force refresh the model list cache. |
| `/model <编号或名称>` | Switch the model used in the current Session mode. |
| `/model --chat <模型>` | Switch the chat mode model. |
| `/model --work <模型>` | Switch the work mode model. |
| `/model --elwisp1 <模型>` | Switch to Elnis elwisp1 model slot. |
| `/model --elwisp2 <模型>` | Switch to Elnis elwisp2 model slot. |
| `/model --elwisp3 <模型>` | Switch to Elnis elwisp3 model slot. |
| `/model --compact <模型>` | Switch the context compaction model. |
| `/model --naming <模型>` | Switch the Session auto-naming model. |
| `/checkmodel [关键词]` | View or search for models. |

Model parameters can be a list number, model name, or `provider/model`.

Example:

```text
/models
/models --fresh
/models --refresh
/model 2
/model --work deepseek/deepseek-chat
/model --chat openai/gpt-4o-mini
/model --elwisp2 openai/gpt-4.1
/checkmodel deepseek
```

## Mode

| Command | Function |
| --- | --- |
| `/chat [消息]` | Switch to chat mode, or create a new chat Session; if a message is provided, it will be sent immediately after switching. |
| `/work [消息]` | Switch to work mode, or create a new work Session; if a message is provided, it will be sent immediately after switching. |

Note:

- `chat` mode does not inject tools, making it suitable for casual chat, companionship, and low-cost Q&A.
- `work` mode enables tool discovery and tool calling.
- A work Session with existing history cannot be switched directly to chat; you need to `/new` first, and then `/chat`.
- For example, `/chat 随便聊聊` will first switch to chat mode, then send "Just chatting" as the same user message to the model; if the mode switch is rejected, the message will not be sent.

## Session

| Command | Function |
| --- | --- |
| `/new` | Create and switch to a new Session. |
| `/status` | View the current Session status. |
| `/sessions [关键词]` | List or search visible Sessions. |
| `/resume [最近编号或session_id]` | Restore a historical Session; index `1` represents the most recently updated Session other than the current one. |
| `/archives [页码] [关键词]` | View archived Sessions. |
| `/archive [编号或session_id] --confirm` | Archive Session, defaults to the current Session. |
| `/unarchive [编号或session_id]` | Unarchive Session, defaults to the current Session. |
| `/pin [编号或session_id]` | Pin Session, defaults to the current Session. |
| `/unpin [编号或session_id]` | Unpin Session, defaults to the current Session. |
| `/rename [number or session_id|Current Title] <New Title>` | Rename the current or a specified Session. |
| `/delete <编号或session_id> --confirm` | Permanently delete the Session. |
| `/clean --confirm` | Delete expired Sessions that are not archived or pinned. |

Example:

```text
/new
/status
/sessions
/sessions project
/resume 1
/rename 我的新会话标题
/archive --confirm
/delete 2 --confirm
```

Note:

- `/resume 1` can directly restore the most recently updated Session other than the current one without first executing a bare `/resume`; indices are sorted by update time, are not affected by pinning, and remain continuous when paging.
- The indices displayed by `/sessions` can be reused by Session operation commands such as `/archive`, `/pin`, and `/delete`.
- CLI serves as a local high-privilege entry point and can view Sessions across platforms; non-CLI platforms view Sessions under the current platform and scope by default.
- Deletion is a permanent operation and requires explicit `--confirm`.

## Fork

| Command | Function |
| --- | --- |
| `/messages [页码]` | List assistant message IDs available for forking in the current Session. |
| `/fork <message_id>` | Create a branched Session from a specified assistant message. |

Example:

```text
/messages
/fork msg_xxx
```

Fork preserves the original Session and creates a new context branch from the specified assistant message position.

## Request Management

| Command | Function |
| --- | --- |
| `/requests` | View active requests in the current process, including turn, LLM, tool, and hook requests; turns will display the execution stage and the time spent on each stage. |
| `/stop [request_id]` | Stop a specified request; you can also use the ID displayed by `/requests`; if no parameters are passed, stop the requests of the current Session. |
| `/stopall` | Stop all active requests in the current process. |

Example:

```text
/requests
/stop
/stop 1.1
/stop req_xxx
/stopall
```

## Context Compaction

| Command | Function |
| --- | --- |
| `/compact` | Manually compact the current Session context. |

Note:

- Automatic compaction is controlled by `[context] compact_enabled` and `compact_trigger_ratio`.
- Compaction preserves the original user history and ignores tool return values; upon success, it creates and switches to an independent `原标题 compacted-N` Session, while the old Session remains unmodified.

## Tools and Skills

| Command | Function |
| --- | --- |
| `/tools` | List registered tools and external Skills. |
| `/tools reload` | Rescan and load Skills. |
| `/tools remove <name> --confirm` | Delete external Skills and their directories. |
| `/tools uninstall <name> --confirm` | Equivalent to remove. |

Example:

```text
/tools
/tools reload
/tools remove my_skill --confirm
```

In work mode, the LLM can discover tool details on demand via `discover_tool`. In a chat, you can also use `@tool:<name-or-tag>` (shorthand `@t:<name-or-tag>`) to preload tools, or use `@skill:<name>` (shorthand `@s:<name>`) to add Skill documentation to the current round of messages and preload the corresponding runtime wrapper. The colon can also be written as a full-width Chinese colon `：`.

## Hook


| Command | Function |
| --- | --- |
| `/hooks` | List all registered Hooks. |
| `/hooks <name>` | View the detailed configuration of a specific Hook. |
| `/hooks start <id>` | Start a persistent Hook. |
| `/hooks stop <id>` | Gracefully stop a Hook. |
| `/hooks restart <id>` | Stop and restart a persistent Hook. |
| `/hooks reload` | Reread rules and persistent Hook configurations, and re-coordinate the process lifecycle. |

Example:

```text
/hooks
/hooks greet
/hooks restart weather
/hooks reload
```

Note:

- Rule Hooks directly use the `name` in the configuration; Built-in Hooks use `builtin.*` names, such as `builtin.resident_memory` and `builtin.cron.missed_once`; Persistent Hooks use plugin IDs.
- `Description` will be displayed in the list and details; rule details are only displayed in the details.
- `reload` will reread `hooks.toml` and the `hook.toml` of each plugin, rebuild Hook registrations, and replace affected persistent processes.
- `/hooks` is a superadmin command.

## Logs and Audit

| Command | Function |
| --- | --- |
| `/log [options]` | Query runtime logs. |
| `/audit [options]` | Query audit logs. |
| `/elwisp [name] [options]` | Query Elnis/Elwisp event logs. |

Common options:

| Option | Function |
| --- | --- |
| `-n, --limit <n>` | Number of entries to return, default is 5. |
| `--days <n>` | Read logs from the last n days, default is 1. |
| `--level <level>` | Minimum level: `debug`, `info`, `warn`, `error`. |
| `-d, -i, -w, -e` | Level shortcuts. |
| `--since <time>` | Only view entries after a certain time, e.g., `2h`, `30m`, `2026-06-03`. |
| `--until <time>` | Only view entries before a certain time. |
| `--msg <text>` | Filter by the msg field. |
| `--contains <text>` | Filter by text, parameters, results, or raw content. |

`/log` additionally supports:

| Option | Function |
| --- | --- |
| `-u`、`-a`、`-t` | Filter user, assistant, and tool events respectively. |
| `-s, --system` | Filter and display `system prompt` logs. |
| `--hook` | Filter Hook events. |

`/audit` additionally supports:

| Option | Function |
| --- | --- |
| `--event <name>` | Filter by audit event, e.g., `tool_call`, `llm_usage`, `permission_denied`. |
| `--risk <level>` | Filter by risk level. |
| `--actor <id>` | Filter by actor ID. |
| `--session <id>` | Filter by Session ID. |
| `--tool <name>` | Filter by tool name. |

`/elwisp` queries `elnis-YYYY-MM-DD.log`, with additional support for:

| Option | Function |
| --- | --- |
| `[name]` | Filter by Elwisp name, equivalent to `--name`. |
| `--name <name>` or `--elwisp <name>` | Filter by Elwisp name. |
| `--source <source>` | Filter by event source. |
| `--id <id>` or `--source-id <id>` | Filter by external event ID. |
| `--mode <record|direct|llm>` | Filter by event mode. |
| `--event-key <key>` | Filter by Elnis event key. |
| `--event-id <id>` | Filter by internal Elnis event ID. |
| `--token <name>` | Filter by token name, excluding the original token text. |

Example:

```text
/log
/log -w -n 10
/log --system
/log --msg startup --days 3
/audit --event tool_call --risk high -n 10
/audit --actor cli:local --since 24h
/elwisp
/elwisp server-watchdog -n 20
/elwisp --source minecraft-main --mode llm --since 2h
```

## Token Consumption Statistics

| Command | Function |
| --- | --- |
| `/usage [options]` | Summarize token consumption data in the audit log. |

Options:

| Option | Function |
| --- | --- |
| `-d, --days <n>` | View the last n days, default is 1. |
| `-m, --model <name>` | Filter by model name. |
| `-s, --session <id>` | Filter by Session ID. |
| `--by <key>` | Summarize by dimension: `model` (default), `day`, `session`. |
| `--since <time>` | Only view entries after a certain time, e.g., `2h`, `30m`, `2026-06-03`. |
| `--until <time>` | Only view entries before a certain time. |

Example:

```text
/usage
/usage -d 7
/usage -m gpt-4o
/usage -s sess-xxx
/usage --by day -d 30
/usage --since 2h
```

Note:

- `/usage` aggregates token usage from `llm_usage` events in the audit log, calculating prompt, completion, total, cache, and duration grouped by model/day/Session.
- Available to superadmins only.

## High-risk Tool Confirmation

When a tool call triggers a high-risk confirmation, the Agent will prompt the available confirmation commands, for example:

| Command | Function |
| --- | --- |
| `/detail` | View details of tool calls awaiting confirmation; supports custom plain-text details for tools, and displays formatted parameters if not customized. |
| `/confirm` | Confirm the current tool call awaiting confirmation. |
| `/confirmtool` | Confirm the current tool. |
| `/confirmall` | Confirm all pending tools in the current batch. |
| `/reject` | Reject the current pending tool call. |
| `/stop` | Stop the current request. |

Specific prompts are subject to the runtime output.
