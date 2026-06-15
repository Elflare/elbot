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
| `/models --fresh` | Force refresh the model list cache. |
| `/model <编号或名称>` | Switch the model used in the current Session mode. |
| `/model --chat <模型>` | Switch the chat mode model. |
| `/model --work <模型>` | Switch the work mode model. |
| `/model --compact <模型>` | Switch the context compaction model. |
| `/model --naming <模型>` | Switch the Session auto-naming model. |
| `/checkmodel [关键词]` | View or search for models. |

Model parameters can be a list number, model name, or `provider/model`.

Example:

```text
/models
/models --fresh
/model 2
/model --work deepseek/deepseek-chat
/model --chat openai/gpt-4o-mini
/checkmodel deepseek
```

## Mode

| Command | Function |
| --- | --- |
| `/chat` | Switch to chat mode, or create a new chat Session. |
| `/work` | Switch to work mode, or create a new work Session. |

Note:

- `chat` mode does not inject tools, making it suitable for casual chat, companionship, and low-cost Q&A.
- `work` mode enables tool discovery and tool calling.
- A work Session with existing history cannot be switched directly to chat; you need to `/new` first, and then `/chat`.

## Session

| Command | Function |
| --- | --- |
| `/new` | Create and switch to a new Session. |
| `/status` | View the current Session status. |
| `/sessions [关键词]` | List or search visible Sessions. |
| `/resume [编号或session_id]` | Restore a historical Session. |
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

- The IDs displayed by `/sessions` can be reused by commands such as `/resume`, `/archive`, `/pin`, and `/delete`.
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
| `/requests` | View active requests in the current process. |
| `/stop [request_id]` | Stop a specified request; if no parameter is provided, stop the request of the current Session. |
| `/stopall` | Stop all active requests in the current process. |

Example:

```text
/requests
/stop
/stop req_xxx
/stopall
```

## Context Compaction

| Command | Function |
| --- | --- |
| `/compact` | Manually compact the current Session context. |

Note:

- Automatic compaction is controlled by `[context] compact_enabled` and `compact_trigger_ratio`.
- Compaction only affects the context view sent to the LLM and does not delete the original message history.

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

In work mode, the LLM can discover tool details on demand via `discover_tool`. In chat, `@tool:<name-or-tag>` can also be used to preload tools.

## Logs and Audit

| Command | Function |
| --- | --- |
| `/log [options]` | Query runtime logs. |
| `/audit [options]` | Query audit logs. |

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

`/audit` additionally supports:

| Option | Function |
| --- | --- |
| `--event <name>` | Filter by audit event, e.g., `tool_call`, `llm_usage`, `permission_denied`. |
| `--risk <level>` | Filter by risk level. |
| `--actor <id>` | Filter by actor ID. |
| `--session <id>` | Filter by Session ID. |
| `--tool <name>` | Filter by tool name. |

Example:

```text
/log
/log -w -n 10
/log --msg startup --days 3
/audit --event tool_call --risk high -n 10
/audit --actor cli:local --since 24h
```

## High-risk Tool Confirmation

When a tool call triggers a high-risk confirmation, the Agent will prompt the available confirmation commands, for example:

| Command | Function |
| --- | --- |
| `/detail` | View details of the tool call awaiting confirmation. |
| `/confirm` | Confirm the current tool call awaiting confirmation. |
| `/confirmtool` | Confirm the current tool. |
| `/confirmall` | Confirm all pending tools in the current batch. |
| `/reject` | Reject the current pending tool call. |
| `/stop` | Stop the current request. |

Specific prompts are subject to the runtime output.
