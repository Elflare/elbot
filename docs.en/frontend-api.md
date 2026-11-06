<!-- This file is auto-translated from docs/frontend-api.md. Do not edit manually. -->

# Frontend API

ElBot uses a decoupled frontend and backend; the CLI remote protocol is a JSON messaging protocol based on WebSocket. Any client capable of connecting to a WebSocket can serve as an ElBot frontend—TUI, Web, mobile, or even a script on another machine.

## Connection

```
ws://<host>:<port>/cli/v1/ws
```

Default address `ws://127.0.0.1:32172/cli/v1/ws`. The address and port can be modified in the `[platform.cli]` configuration.

## Authentication

After the connection is established, the client must immediately send a `hello` message:

```json
{
  "type": "hello",
  "client_id": "my-client",
  "token": "your-token"
}
```

The server will respond after validating the token:

- `hello_ok` — Authentication successful, interaction can begin
- `error` — Authentication failed, connection closed

```json
{"type": "hello_ok", "client_id": "my-client"}
```

Tokens are mapped to environment variable names by client_id in `[platform.cli.server.tokens]`, and the server reads the actual values from the corresponding environment variables.

## Message Format

All messages are JSON and share the same structure:

| Field | Type | Description |
|------|------|------|
| type | string | Message type, see table below |
| id | string | Message ID, used for pairing completion requests/responses |
| client_id | string | Client identifier |
| token | string | Authentication token (hello only) |
| text | string | Text content |
| level | string | Notification level (notice only) |
| cursor | int | Cursor position (completion requests only) |
| items | array | Completion candidates list (completion responses only) |
| snapshot | object | Runtime state snapshot (status only) |
| stream_id | uint64 | Logical message stream identifier (stream only), used to associate previews, resets, and submissions of the same message |
| event | object | Message stream event (stream only), fields see below |

## Client → Server

### input — Send Message

```json
{"type": "input", "text": "你好"}
```

Pass text as user input to the Agent for processing. The server does not send a confirmation response; processing results are returned asynchronously via messages such as chat/stream.

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

### notice — Notification

```json
{"type": "notice", "level": "WARN", "text": "Model call failed, retrying..."}
```

Plain text notifications, including command return results, as well as runtime prompts such as retries, Hook failures, and configuration warnings. Command results use `INFO` and can contain multi-line long text. `level` can be `DEBUG`, `INFO`, `WARN`, or `ERROR`; if omitted, it is treated as `INFO`. Notifications do not carry files, media, or background reports.

Notifications are queued independently by platform and sent sequentially to the same target. When the queue is full, a warning is logged and it is not automatically resent; notifications wait for a maximum of 30 seconds by default. Reply notifications from the original remote connection will not migrate to a new connection for the same user. Queued tool previews and retry prompts are cancelled along with the request; failure and timeout prompts that have already occurred are retained based on their source.

### status — Runtime Status

```json
{
  "type": "status",
  "snapshot": {
    "session_id": "abc123",
    "phase": "llm",
    "provider": "openai",
    "model": "gpt-4o",
    "mode": "work",
    "request_id": "req-1",
    "kind": "turn",
    "label": "",
    "tool_name": "",
    "turn_started_at": "2026-07-01T12:00:00Z",
    "stage_started_at": "2026-07-01T12:00:01Z",
    "finished_at": "0001-01-01T00:00:00Z",
    "usage": {"prompt_tokens": 500, "completion_tokens": 100, "total_tokens": 600, "cache_hit_tokens": 0},
    "error": ""
  }
}
```

A runtime status snapshot, which the frontend can use for status bar display. The submitted final state is still published after the request is cancelled; Background tasks have no frontend display target; only new states are displayed after takeover. Status updates will be merged; if the application is closed entirely, there is no guarantee that the final state will be re-sent to the closed frontend. `phase` values:

| Phase | Description |
|-------|------|
| idle | Idle |
| preparing | Preparing |
| llm | LLM generating |
| tool | Tool executing |
| sending | Reply sending |
| done | Completed |
| error | Error |

### stream — Message Stream Events

```json
{
  "type": "stream",
  "stream_id": 1,
  "event": {"type": "text_delta", "text": "Hello"}
}
```

`stream_id` identifies a logical message, not a Session ID or request ID. The client maintains the body and reasoning preview based on this identifier; do not append all increments to a single global buffer. The identifier is only used for message association within the current connection, not as a cursor for recovering history after reconnection.

`event` field:

| Field | Type | Description |
|------|------|------|
| type | string | Event type, see table below |
| text | string | Body/reasoning increment, or the final complete body for `send`; empty text may be omitted and should be treated as an empty string |
| actions | array | Optional message actions; element fields are `ID`, `Label`, and `Command`. The frontend can display these as buttons; clicking them submits the `Command` via `input`, which is still subject to server-side permission validation |

| event.type | Client Handling |
|------------|------------|
| text_delta | Append `text` to the body preview of the stream |
| reasoning_delta | Append `text` to the reasoning preview of the stream; the frontend may choose to show or hide this |
| reasoning_done | End the current reasoning generation and retain the reasoning content received |
| reset | Delete the current body and reasoning previews for the stream; retries can continue using the same `stream_id` to generate new previews |
| finish | Generation ends; retain the preview and its stream association, waiting for the final `send` or a `reset` from a subsequent retry |
| send | **Entirely replace** the body preview with `text` and submit the message, while retaining reasoning content; remove the active stream association. If no preview exists, create a complete message directly |

For example, the final body may be rewritten by a Hook, so the full text in `send` must be used instead of directly fixing the result of incremental concatenation:

```json
{"type": "stream", "stream_id": 1, "event": {"type": "finish"}}
{"type": "stream", "stream_id": 1, "event": {"type": "send", "text": "Hello! How can I help you?"}}
```

An empty body in `send` must also overwrite the original preview and cannot be ignored; if both body and reasoning are empty, the message can be removed. `finish` is not equivalent to a formal submission; the stream association should not be removed upon receiving it, nor should it be required that increments or `finish` precede every `send`.

Reasoning content consists of raw increments and does not include `[thinking]` / `[/thinking]` text markers; the display style is determined by the frontend. Message streams are delivered exclusively to the original connection and do not migrate to a new connection for the same user; once a background task takes over, only newly generated content is displayed, and reasoning increments from before the takeover are not replayed.

Additional content is delivered separately by the Agent output layer and is not transmitted as `stream` events for the remote CLI.

### complete_result — Completion Result

```json
{
  "type": "complete_result",
  "id": "c-1",
  "items": [
    {
      "text": "/model",
      "label": "/model",
      "description": "切换或查看模型",
      "kind": "command",
      "replace_start": 0,
      "replace_end": 4
    }
  ]
}
```

Completion candidate list, where `id` corresponds to `id` at the time of the request. `replace_start`/`replace_end` indicates the byte range of the original input text to be replaced.

### error — Error

```json
{"type": "error", "text": "context deadline exceeded"}
```

An error that occurred during processing.

## Completion Item Fields

| Field | Type | Description |
|------|------|------|
| text | string | Inserted text |
| label | string | Display label |
| description | string | Additional description |
| kind | string | Candidate type, such as `command`, `tool`, `fork` |
| replace_start | int | Replacement start byte offset |
| replace_end | int | Replacement end byte offset |

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

The `stream(1, ...)` in the diagram represents `stream_id=1` and the corresponding `event.type`, not actual JSON. Reasoning events are optional, and status updates may be merged; after a generation failure, a `reset` may be received followed by regeneration; do not rely on the complete sequence above occurring every time. Results of commands such as model switching are returned via `notice` and do not require LLM streaming generation.

## Minimal Client Example

The following code can be run in a browser page; it uses `textContent` to display content to avoid treating model output as HTML. The example handles body text and reasoning, retry resets, and final submissions; buttons and status bars can be extended as needed.

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
      // Only end generation; preserve preview and stream association, waiting for send or reset.
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

const ws = new WebSocket("ws://127.0.0.1:32172/cli/v1/ws");

ws.onopen = () => {
  ws.send(JSON.stringify({
    type: "hello",
    client_id: "web",
    token: "your-token",
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
