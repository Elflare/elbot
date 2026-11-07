<!-- This file is auto-translated from docs/frontend-api.md. Do not edit manually. -->

# Frontend API

ElBot features frontend-backend separation. The CLI remote protocol exchanges JSON messages via WebSocket and sends/receives attachments through an HTTP interface on the same port. TUI, Web, mobile clients, and scripts share this protocol, with clients handling display and download interactions independently.

## Connection

```
ws://<host>:<port>/cli/v1/ws
```

Default address `ws://127.0.0.1:32172/cli/v1/ws`. The address and port can be modified in the `[platform.cli]` configuration.

## Static Frontend Hosting

After enabling the CLI remote service, you can place `frontend/index.html` in the directory where the main configuration file is located. Accessing `http://<host>:<port>/` will open it. The page and WebSocket share `server.listen`, so no separate server is needed, and there are no built-in frontend templates:

```text
config directory/
├── app.toml
├── .env
└── frontend/
    ├── index.html
    ├── settings.html
    ├── app.js
    └── assets/
        └── app.css
```

- The entry point is automatically recognized during initialization; hosting is not enabled if the directory or entry point does not exist, and an error is reported for other read errors or if the entry point is not a regular file. The service must be restarted after the entry point is placed for the first time.
- `/` returns `index.html`; `/settings.html`, `/app.js`, and `/assets/app.css` return their corresponding files. When a directory path ends with `/`, the `index.html` within it is read; if no entry point exists, a 404 is returned, and directory contents are not listed.
- Supports multiple actual HTML pages and hash routing; no SPA path fallback is provided, so missing paths like `/settings` return 404.
- Files are read from disk upon request; existing files can be reloaded by refreshing the page, and caching follows HTTP behavior. Only files within `frontend/` are exposed; path traversal and symbolic link escape are prohibited.
- Static files can be loaded without logging in; the frontend should provide its own login interface, and ElBot can only be operated after authentication via the `hello` command described below. Do not write tokens into frontend files or URLs; by default, they are only kept in page memory.

The frontend should connect to `/cli/v1/ws` based on the current page address: use `ws:` for HTTP pages and `wss:` for HTTPS pages, see the [Minimal Client Example](#最小客户端示例) below. When using HTTPS, the TLS entry is provided by the deployment environment; for ElBot's listening and authentication configuration, see [CLI Remote Configuration](configuration.md#cli-远程配置).

## Authentication

After the connection is established, the client must immediately send a `hello` message:

```json
{
  "type": "hello",
  "client_id": "my-client",
  "token": "your-token"
}
```

After validating the token, the server responds with:

- `hello_ok` — Authentication successful, interaction can begin
- `error` — Authentication failed, connection closed

```json
{"type": "hello_ok", "client_id": "my-client", "media_token": "connection-media-token"}
```

Tokens are mapped to environment variable names by client_id in `[platform.cli.server.tokens]`; the server reads the actual values from the corresponding environment variables.

The `media_token` is used only for the HTTP attachment interfaces described below and is bound to the currently authenticated WebSocket connection; it becomes invalid after disconnection, and new credentials must be obtained upon reconnection. Multiple connections with the same client_id are isolated from each other. Credentials are kept in client memory and sent via `Authorization: Bearer <media_token>`, not included in the URL.

## Message Format

All messages are JSON and share the same structure:

| Field | Type | Description |
|------|------|------|
| type | string | Message type, see table below |
| id | string | Input/completion correlation ID; the request_id of a command result returns the input id, and attachment input errors return the id |
| client_id | string | Client identifier |
| token | string | Authentication token (hello only) |
| media_token | string | HTTP attachment credentials for the current connection (hello_ok only) |
| file_ids | array of string | Attachment IDs uploaded or received in the current connection (input only) |
| attachment | object | Attachment description (attachment only), fields see below |
| text | string | Text content |
| level | string | Notification level (notice only) |
| cursor | int | Cursor position (completion requests only) |
| items | array | Completion candidates list (completion responses only) |
| snapshot | object | Runtime state snapshot (status only) |
| stream_id | uint64 | Logical message stream identifier (stream only), used to correlate the preview, reset, and submission of the same message |
| event | object | Message stream event (stream only), fields see below |

## HTTP Attachment Interface

All interfaces require the `media_token` of the current WebSocket connection, regardless of whether a static page exists:

| Interface | Request & Response |
|------|------------|
| `POST /cli/v1/files` | `multipart/form-data`, single file field `file`; returns 201 and attachment description on success |
| `GET /cli/v1/files/{id}` | Returns file content; HEAD returns the same metadata without the body |
| `DELETE /cli/v1/files/{id}` | Revokes attachment authorization for the current connection; returns 204 on success |

The upload response is identical to the description in the `attachment` message. Filenames, types, and sizes are based on the archiving result, including existing image compression behavior. Multiple files must be uploaded separately; do not include other fields or files in a single POST request.

GET uses `Content-Disposition: attachment` and `X-Content-Type-Options: nosniff`; attachment responses use `Cache-Control: no-store`. Browsers can first obtain a Blob via `fetch` with authentication headers, then handle preview or download independently.

Deletion or disconnection only revokes attachment authorization and ends the connection hold; it does not directly delete the Media object. In-flight operations that have already acquired independent hold can be completed; final reclamation is still managed by Media. After refreshing or reconnecting, the original attachment ID cannot be reused and must be re-uploaded.

Invalid authentication returns 401; if the attachment does not exist or does not belong to the current connection, 404 is returned; incorrect request format returns 400; 503 is returned when the service stops accepting connections; media processing failure (including exceeding upload limits) returns 500 and the unified message "Attachment processing failed", with specific reasons recorded in the server-side logs.

## Client → Server

### input — Send Message

```json
{"type": "input", "text": "Hello"}
```

Passes text as user input to the Agent for processing. The server does not send a confirmation; processing results are returned asynchronously via chat/stream and other messages.

After uploading attachments, place the returned IDs in `file_ids`. Pure attachment messages without a body are also supported:

```json
{"type":"input","id":"m-1","text":"Take a look at this file","file_ids":["attachment-id"]}
```

The server processes the body first, then processes attachments in the order of `file_ids`. Images are converted to image inputs; other files are expanded as text with filenames and code fences if they are valid UTF-8, contain no NUL characters, and are within 256 KiB per file and 1 MiB total expansion. Otherwise, they are treated as file inputs. This quota only controls text expansion; upload size limits reuse the Media configuration.

Attachments must belong to the current connection. If preparing any attachment fails, the partial input will not be submitted; instead, an `error` and the `id` of the original input will be returned, with the body "Attachment processing failed". Attachments are not automatically released upon sending input; the client may call DELETE when they are no longer needed.

### complete — Request Completion

```json
{"type": "complete", "id": "c-1", "text": "/mod", "cursor": 4}
```

Request command completion candidates for the current input text. `id` is used to match the response. `cursor` is the byte offset of the cursor in `text`, defaulting to the end if omitted.

The server responds with `complete_result`.

## Server → Client

### chat — Formal Reply

```json
{"type": "chat", "text": "Hello! How can I help you?"}
```

An independent, complete dialogue output, including non-streaming delivered body text, Hook outputs, and Cron/Elnis background reports. Background reports also enter the chat area. Body text submitted via the `send` event of `stream` will not be resent as `chat`; the client must handle both types of messages simultaneously.

### attachment — File or Image Output

```json
{"type":"attachment","attachment":{"id":"attachment-id","name":"report.pdf","mime_type":"application/pdf","size":2048,"kind":"file"}}
```

`kind` is `image` or `file`; the `id` in the description is the attachment ID of the current connection, not a MediaID or disk path. Mixed outputs send `chat` and `attachment` in their original order. The client retrieves attachment content via HTTP; `send_file` uses the same set of protocols, regardless of whether it is a web page or TUI.

### notice — Notification

```json
{"type": "notice", "level": "WARN", "text": "Model call failed, retrying..."}
```

Plain text runtime prompts, including retries, Hook failures, and configuration alerts. `level` can be `DEBUG`, `INFO`, `WARN`, or `ERROR`; if omitted, it is treated as `INFO`. Notifications do not carry files, media, or background reports.

Notifications are queued independently by platform and sent sequentially to the same target. When the queue is full, an alert is logged and the notification is not automatically resent; notifications wait for a maximum of 30 seconds by default. Reply notifications from the original remote connection will not migrate to a new connection for the same user. Queued tool previews and retry prompts are removed upon request cancellation; failure and timeout prompts that have already occurred are retained based on their source.

### command_result — Command Result

```json
{
  "type": "command_result",
  "request_id": "m-1",
  "command": "messages",
  "result": {
    "kind": "messages",
    "data": {
      "session_id": "s-1",
      "page": 1,
      "page_size": 20,
      "has_next": false,
      "total_pages": 1,
      "items": [
        {"id":"u-1","role":"user","content":"Hello","attachments":[],"created_at":"2026-11-07T12:00:00Z"},
        {"id":"a-1","role":"assistant","content":"Hello","attachments":[],"created_at":"2026-11-07T12:00:01Z"}
      ]
    },
    "confirmation": null
  },
  "error": null
}
```

`request_id` is associated with `input.id` as-is, not an internal Agent request ID. Results are sent only to the original connection; other connections with the same Client ID will not receive them. Commands are not returned via `notice`. Append confirmation receipts are returned via `command="append"`; the confirmation button is used to accept the append, send `y` or `/confirm` to resume, or use `/stop` to cancel.

| Field | Description |
| --- | --- |
| result.kind | Data structure category, explicitly specified by the server; different branches of the same command may vary |
| result.data | Named business data corresponding to the category; when `kind=text`, it is `{content: string}` |
| result.confirmation | If not null, indicates that confirmation is still pending and the target operation has not yet been executed; contains actions |
| error | Independent categorized error; null upon success |

When a complete failure occurs, `result=null`; when partial results exist, both `result` and `error` can be returned simultaneously. Itemized failures within a list are preserved in the result data without erasing successful items. Internal server raw error chains and subsequent inputs are not included in the JSON.

`error` contains `kind`, `code`, and `message`: `kind` can be `input` (unknown command/parameter error), `rejected` (permission/busy status/target does not exist/confirmation expired), `internal` (internal failure), or `interrupted` (actual execution canceled/timeout). `code` provides the specific reason, such as `unknown_command`, `invalid_arguments`, `permission_denied`, `session_busy`, `confirmation_expired`, `canceled`, `timeout`. Client-side timeout or disconnection does not mean the backend execution has been canceled. Accepting a stop request via `/stop` is a normal result.

The structure of a confirmation operation is:

```json
{"actions":[{"id":"confirm","label":"Confirm Delete","command":"/delete s-1 --confirm"}]}
```

The client submits the `command` as a new `input.text`; permissions and target states are still validated by the server, and the `id` is not an authorization credential.

Main data categories:

| kind | data |
| --- | --- |
| sessions | items, current binding, search terms, and pagination; each item contains Session fields, message count, the most recent message, and summaries of user/assistant content |
| session | operation target, the Session after changes, current binding, actual number of changes, and the most recent messages read by resume/fork |
| messages | session_id, items, and pagination; message content is complete, while historical attachments only provide names |
| status | nullable current Session, mode, model, Turn phase, requests, tool statistics, recent user/assistant content, and context |
| models / model | model directory and errors per provider / the actually submitted model and target |
| requests / stop | request snapshot / stop target and the actual number of accepted cancellations |
| tools, rollbacks, rollback | tool directory and management target / rollback list and actual results |
| logs, usage, doctor, hooks, compact, help | raw reports, statistics, or operation results for the corresponding domains |
| text | text feedback without business data |

`/messages` reads page 1 by default, with 20 items per page. Page 1 is the most recent, and page numbers increase as they read further back into history; items within each page are ordered by conversation sequence. When loading upwards, earlier pages are inserted at the top and deduplicated by message ID. Page boundaries may shift if new messages arrive during page queries; cross-request snapshots are not provided. Empty history returns `items=[]` and `total_pages=0`.

The `context` of `/status` contains `usage`, `context_window`, `compact_enabled`, and `compact_trigger_ratio`. Usage fields are `prompt_tokens`, `completion_tokens`, `total_tokens`, and `cache_hit_tokens`; null indicates unknown, and 0 is a known zero value. The context ratio uses known total/window, and the cache hit rate uses known cache/prompt; missing counts are not extrapolated. The phase here comes from Turn; real-time `status` frames still use Runtime snapshots.

For complete business field and category definitions, see [Command Common Data Types](../internal/command/data.go) and [Result Contract](../internal/command/result.go).

### status — Runtime Status

```json
{
  "type": "status",
  "snapshot": {
    "SessionID": "abc123",
    "Phase": "llm",
    "Provider": "openai",
    "Model": "gpt-4o",
    "Mode": "work",
    "RequestID": "req-1",
    "Kind": "turn",
    "Label": "",
    "ToolName": "",
    "TurnStartedAt": "2026-07-01T12:00:00Z",
    "StageStartedAt": "2026-07-01T12:00:01Z",
    "FinishedAt": "0001-01-01T00:00:00Z",
    "Usage": {"PromptTokens": 500, "CompletionTokens": 100, "TotalTokens": 600, "CacheHitTokens": 0},
    "Error": ""
  }
}
```

Runtime status snapshot, which the frontend can use for status bar display. The final state will still be published after a request is cancelled; background tasks have no foreground display target, so only new states are displayed after takeover. Status updates will be merged; if the application is closed entirely, there is no guarantee that the final state will be resent to the closed frontend. `Phase` values:

| Phase | Description |
|-------|-------------|
| idle | Idle |
| preparing | Preparing |
| llm | LLM generating |
| tool | Tool executing |
| sending | Sending response |
| done | Done |
| error | Error |

### stream — Message Stream Events

```json
{
  "type": "stream",
  "stream_id": 1,
  "event": {"type": "text_delta", "text": "Hello"}
}
```

`stream_id` identifies a logical message, not a Session ID or Request ID. The client maintains the body and reasoning preview based on this identifier; do not append all deltas to a single global buffer. This identifier is only used for message association within the current connection and is not a cursor for recovering history after reconnection.

`event` field:

| Field | Type | Description |
|------|------|------|
| type | string | Event type, see table below |
| text | string | Body/reasoning delta, or the final complete body of a `send` event; empty text may be omitted and should be treated as an empty string |
| actions | array | Optional message actions; element fields are `id`, `label`, and `command`. The frontend can display these as buttons; clicking them submits the `command` via `input`, which is still subject to server-side permission validation |

| event.type | Client Handling |
|------------|------------|
| text_delta | Append `text` to the body preview of this stream |
| reasoning_delta | Append `text` to the reasoning preview of this stream; the frontend may choose to show or hide it |
| reasoning_done | End the current reasoning generation and retain the reasoning content received so far |
| reset | Delete the current body and reasoning preview of this stream; retries can continue to use the same `stream_id` to generate a new preview |
| finish | Generation ended; retain the preview and its stream association, awaiting the final `send` or a `reset` from a subsequent retry |
| send | **Completely replace** the body preview with `text` and submit the message, retaining reasoning content; remove the active stream association. If no preview exists, create a complete message directly |

For example, the final body may be rewritten by a Hook, so the full text in `send` must be used rather than directly relying on the concatenated result of deltas:

```json
{"type": "stream", "stream_id": 1, "event": {"type": "finish"}}
{"type": "stream", "stream_id": 1, "event": {"type": "send", "text": "Hello! How can I help you?"}}
```

An empty body in `send` must also overwrite the original preview and cannot be ignored; the message may be removed if both the body and reasoning are empty. `finish` is not equivalent to a formal submission; the stream association should not be removed upon receiving it, nor should it be required that every `send` be preceded by deltas or a `finish`.

Reasoning content consists of raw deltas without `[thinking]` or `[/thinking]` text markers; the display style is determined by the frontend. Message streams are delivered exclusively to the original connection and do not migrate to a new connection for the same user; once a background task takes over, only newly generated content is displayed, and reasoning deltas from before the takeover are not replayed.

Additional content is delivered separately by the Agent output layer and is not transmitted as `stream` events for the remote CLI.

### complete_result — Completion Result

```json
{
  "type": "complete_result",
  "id": "c-1",
  "items": [
    {
      "Text": "/model",
      "Label": "/model",
      "Description": "Switch or view model",
      "Kind": "command",
      "ReplaceStart": 0,
      "ReplaceEnd": 4
    }
  ]
}
```

Completion candidate list; the `id` corresponds to the `id` used during the request. `ReplaceStart`/`ReplaceEnd` indicates the byte range of the original input text to be replaced.

### error — Error

```json
{"type": "error", "text": "context deadline exceeded"}
```

An error that occurred during processing.

## Completion Item Fields

| Field | Type | Description |
|------|------|------|
| Text | string | Inserted text |
| Label | string | Display label |
| Description | string | Supplementary description |
| Kind | string | Candidate type, such as `command`, `tool`, `fork` |
| ReplaceStart | int | Starting byte offset for replacement |
| ReplaceEnd | int | Ending byte offset for replacement |

## Configuration

Configure in `[platform.cli]` of `app.toml`:

```toml
[platform.cli]
enabled = true

[platform.cli.server]
enabled = true
listen = "127.0.0.1:32172"

[platform.cli.server.tokens]
my-client = ["ELBOT_CLI_TOKEN"]
```

`tokens` is a mapping from client_id to a list of environment variable names. The server attempts to read token values from these environment variables in order and compares them with the token sent by the client. Environment variables can be read from the system environment or the configuration directory `.env`.

## Typical Interaction Flow

```
Client                               Server
  │                                   │
  │──── hello ──────────────────────▶│
  │◀─── hello_ok ────────────────────│
  │                                   │
  │──── input("Hello") ───────────────▶│
  │◀─── status(preparing) ───────────│
  │◀─── status(llm) ─────────────────│
  │◀─── stream(1, reasoning_delta) ──│
  │◀─── stream(1, reasoning_done) ───│
  │◀─── stream(1, text_delta) ───────│
  │◀─── stream(1, finish) ───────────│
  │◀─── status(sending) ─────────────│
  │◀─── stream(1, send) ─────────────│
  │◀─── status(done) ────────────────│
  │                                   │
  │──── complete("/mod", cursor=4) ──▶│
  │◀─── complete_result ──────────────│
  │                                   │
```

The `stream(1, ...)` in the diagram represents `stream_id=1` and the corresponding `event.type`, not actual JSON. Reasoning events may be omitted, and status updates may be merged; after a generation failure, a `reset` may be received followed by regeneration, so you cannot rely on the complete sequence above occurring every time. Results of commands such as model switching are returned via `command_result` and do not require LLM streaming generation.

## Minimal Client Example

The following example is for browser pages hosted by ElBot; the entered credentials are kept only in page memory. Content is displayed using `textContent` to avoid treating model output as HTML. The example handles body text and reasoning, retry resets, and final submissions; the login interface, buttons, and status bar can be expanded as needed.

```javascript
const output = document.createElement("pre");
document.body.append(output);
const messages = [];
const streams = new Map();
const render = () => {
  output.textContent = messages.map((m) =>
    (m.reasoning ? "[Thinking] " + m.reasoning + "\n" : "") + m.text
  ).join("\n\n");
};
const removeMessage = (message) => {
  const index = messages.indexOf(message);
  if (index !== -1) messages.splice(index, 1);
};

function handleStream({ stream_id: id, event }) {
  if (!event) return;
  const text = event.text ?? "";
  switch (event.type) {
    case "reasoning_done":
    case "finish":
      // Only end generation; keep preview and stream association, waiting for send or reset.
      return;
    case "reset": {
      const message = streams.get(id);
      if (message) removeMessage(message);
      streams.delete(id);
      render();
      return;
    }
    case "text_delta":
    case "reasoning_delta":
    case "send":
      break;
    default:
      return;
  }
  let message = streams.get(id);
  if (!message) {
    message = { text: "", reasoning: "" };
    streams.set(id, message);
    messages.push(message);
  }
  if (event.type === "text_delta") message.text += text;
  if (event.type === "reasoning_delta") message.reasoning += text;
  if (event.type === "send") {
    message.text = text; // Replace the entire body even if empty; do not append another message.
    streams.delete(id);
    if (!message.text && !message.reasoning) removeMessage(message);
  }
  render();
}

const clientId = window.prompt("Client ID");
const token = window.prompt("Token");
const wsUrl = new URL("/cli/v1/ws", window.location.href);
wsUrl.protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
const ws = new WebSocket(wsUrl);

ws.onopen = () => {
  ws.send(JSON.stringify({
    type: "hello",
    client_id: clientId,
    token,
  }));
};

ws.onmessage = (e) => {
  const msg = JSON.parse(e.data);
  switch (msg.type) {
    case "hello_ok":
      // Authentication successful, send a message
      ws.send(JSON.stringify({ type: "input", text: "Hello" }));
      break;
    case "chat":
      messages.push({ text: msg.text ?? "", reasoning: "" });
      render();
      break;
    case "stream":
      handleStream(msg);
      break;
    case "command_result":
      if (msg.result) console.log(msg.result.kind, msg.result.data);
      if (msg.error) console.error(msg.error.kind, msg.error.code, msg.error.message);
      break;
    case "notice":
      console.log(`[${msg.level ?? "INFO"}] ${msg.text ?? ""}`);
      break;
    case "status":
      console.log("status:", msg.snapshot);
      break;
    case "error":
      console.error("error: " + msg.text);
      break;
  }
};
```

For more frontend examples, see [elbot-showcase/frontend](https://github.com/Elfreese/elbot-showcase/tree/main/frontend).
