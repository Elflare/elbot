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

All messages are in JSON and share the same structure:

| Field | Type | Description |
|------|------|------|
| type | string | Message type, see the table below |
| id | string | Message ID, used for matching completion requests/responses |
| client_id | string | Client identifier |
| token | string | Authentication token (hello only) |
| text | string | Text content |
| cursor | int | Cursor position (completion requests only) |
| items | array | Completion candidates list (completion responses only) |
| snapshot | object | Runtime status snapshot (status only) |

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

### chat — Formal Response

```json
{"type": "chat", "text": "你好！有什么可以帮你的？"}
```

The complete reply message sent after the Agent finishes processing.

### notice — Notification

```json
{"type": "notice", "text": "Cron 任务已完成"}
```

Notification messages for non-dialogue scenarios, such as Cron results, Hook output, Elnis event reports, etc.

### reasoning — Reasoning Process

```json
{"type": "reasoning", "text": "用户在问天气，我需要调用搜索工具..."}
```

Reasoning text pushed by models that support reasoning output during the generation process. The frontend can choose to display or hide it.

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

A snapshot of the runtime status, which the frontend can use for status bar display. Possible values for `phase`: 

| Phase | Description |
|-------|------|
| idle | Idle |
| preparing | Preparing |
| llm | LLM generating |
| tool | Tool executing |
| done | Completed |
| error | Error |

### stream_append — Stream Append

```json
{"type": "stream_append", "text": "你好"}
```

An incremental piece of text streamed from the LLM. The frontend should append it to the end of the message currently being generated.

### stream_replace — Streaming Replace

```json
{"type": "stream_replace", "text": "你好！有什么可以帮你的？"}
```

After streaming ends, replace the previously accumulated streaming content with the final complete text. This may differ from the result of streaming concatenation (Hooks may have modified the output).

### stream_finish — Streaming Finish

```json
{"type": "stream_finish"}
```

Marks that the current streaming message has ended; the frontend can now pin this message.

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
客户端                              服务端
  │                                   │
  │──── hello ──────────────────────▶│
  │◀─── hello_ok ────────────────────│
  │                                   │
  │──── input("/model gpt-4o") ─────▶│
  │◀─── status(preparing) ───────────│
  │◀─── status(llm) ─────────────────│
  │◀─── stream_append("已切换") ──────│
  │◀─── stream_replace("已切换模型") ─│
  │◀─── stream_finish ────────────────│
  │◀─── status(done) ────────────────│
  │                                   │
  │──── complete("/mod", cursor=4) ──▶│
  │◀─── complete_result ──────────────│
  │                                   │
```

## Minimal Client Example

```javascript
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
      // 鉴权成功，发送一条消息
      ws.send(JSON.stringify({ type: "input", text: "你好" }));
      break;
    case "chat":
    case "stream_append":
      console.log(msg.text);
      break;
    case "stream_replace":
      console.log("\n[最终] " + msg.text);
      break;
    case "stream_finish":
      console.log("\n--- 完成 ---");
      break;
    case "error":
      console.error("error: " + msg.text);
      break;
  }
};
```

For more frontend examples, see [elbot-showcase/frontend](https://github.com/Elfreese/elbot-showcase/tree/main/frontend).
