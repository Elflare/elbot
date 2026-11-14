<!-- This file is auto-translated from docs/frontend-api.md. Do not edit manually. -->

# Frontend API

ElBot uses a decoupled frontend and backend architecture. The CLI remote protocol exchanges JSON messages via WebSocket, and uses HTTP interfaces on the same port to send and receive attachments and invoke structured management operations. TUI, Web, mobile clients, and scripts share the same protocol, with clients handling their own display and download interactions.

## Connection

```
ws://<host>:<port>/cli/v1/ws
```

Default address `ws://127.0.0.1:32172/cli/v1/ws`. The address and port can be modified in the `[platform.cli]` configuration.

## Static Frontend Hosting

After enabling the CLI remote service, place `frontend/index.html` in the main configuration directory to access it via `http://<host>:<port>/`. Static pages share `server.listen` with WebSocket, and no built-in templates are provided.

- `frontend/index.html` is recognized when the service starts; a restart is required after adding the entry point for the first time. `/` returns this entry point, and directory URLs can read the `index.html` within them, but directory listings or SPA path fallbacks are not provided.
- Files are served only from within `frontend/`; path traversal and symbolic link escapes are prohibited. Static files do not require authentication, but `hello` authentication is still required before operating ElBot.

The frontend connects to `/cli/v1/ws` based on the page protocol: `ws:` for HTTP and `wss:` for HTTPS; TLS is provided by the deployment environment.

## Authentication

After the connection is established, the client must immediately send a `hello` message:

```json
{
  "type": "hello",
  "client_id": "my-client",
  "token": "your-token"
}
```

After the server validates the token, it responds with:

- `hello_ok` — Authentication successful, interaction can begin
- `error` — Authentication failed, connection closed

```json
{"type": "hello_ok", "client_id": "my-client", "media_token": "connection-media-token", "api_token": "connection-api-token"}
```

Tokens are mapped to environment variable names by `client_id` in `[platform.cli.server.tokens]`; for configuration details, see [CLI Remote Configuration](configuration.md#cli-远程配置).

The `media_token` is used only for the HTTP attachment interfaces described below and is sent via `Authorization: Bearer <media_token>`, not in the URL. It is bound to the current connection and becomes invalid upon disconnection; connections with the same `client_id` are isolated from each other.

The `api_token` is used only to route actions back to the current connection after confirming additional input, sent via `X-CLI-Connection`, and does not grant permissions on its own. Management API requests must also carry the configured client token and `X-Client-ID: <client_id>`.

## Message Format

Messages are in JSON format. See the corresponding sections for fields of each type; `id` is used to associate inputs with completion responses, and the `request_id` in command results returns the input ID.

## HTTP Attachment Interfaces

All interfaces require the `media_token` of the current WebSocket connection, regardless of whether a static page exists:

| Interface | Request & Response |
|------|------------|
| `POST /cli/v1/files` | `multipart/form-data`, single file field `file`; returns 201 and attachment description on success |
| `GET /cli/v1/files/{id}` | Returns file content; HEAD returns the same metadata without the body |
| `DELETE /cli/v1/files/{id}` | Revokes attachment authorization for the current connection; returns 204 on success |

The upload response is identical to the `attachment` description; a single POST request uploads only one `file`, and multiple files must be uploaded separately. Browsers can use `fetch` with authentication headers to obtain a Blob for previewing or downloading. GET responses use `Content-Disposition: attachment`, `X-Content-Type-Options: nosniff`, and `Cache-Control: no-store`.

Deletion or disconnection will revoke the attachment authorization for the current connection; files must be re-uploaded after disconnection. Authentication failure returns 401; if the attachment does not exist or does not belong to the current connection, 404 is returned; request format errors, business rejections, or invalid targets return 400; permission denied returns 403; conflicts return 409; service access suspension or unavailable capabilities return 503; media business failures and internal faults return 500; cancellations return 408, and timeouts return 504. Business errors return `Result.Message`, and Go errors return `err.Error()`.

## HTTP Management API

For the endpoints, request, and response fields of the Management API, please refer to the [OpenAPI documentation](frontend-api.openapi.yaml); the online version is available in the [Official API Documentation](https://elfreese.github.io/elbot-landing/docs/api/).

## Client → Server

### input — Send Message

```json
{"type": "input", "text": "Hello"}
```

Passes text as user input to the Agent for processing. The server does not send a confirmation reply; processing results are returned asynchronously via chat/stream and other messages.

`#file_path` or `#"file path with spaces"` in the body are expanded according to the Agent workspace. If a quote is not closed, the path is out of bounds, or the target is not a regular file, the reason for rejection is displayed and the current input processing is terminated. Non-existent references are kept as original text; oversized or binary files retain their paths and their content is not expanded.

After uploading attachments, place the returned IDs into `file_ids`. Pure attachment messages without a body are also supported:

```json
{"type":"input","id":"m-1","text":"Check out this file","file_ids":["attachment-id"]}
```

The server processes the body first, then processes attachments in the order of `file_ids`. Images are converted to image inputs; other files are expanded as text with filenames and code fences if they are valid UTF-8, contain no NUL characters, and are within 256 KiB per file and 1 MiB total expansion. Otherwise, they are treated as file inputs. This quota only controls text expansion; upload size limits reuse the Media configuration.

Attachments must belong to the current connection. If preparing any attachment fails, the partial input is not submitted; instead, an `error` and the original input `id` are returned, with the body containing the raw text of Result.Message or a Go error. Attachments are not automatically released upon sending input; the client can call DELETE when they are no longer needed.

### complete — Request Completion

```json
{"type": "complete", "id": "c-1", "text": "/mod", "cursor": 4}
```

Request completion candidates for the current input text. The `id` is used to match the response. The `cursor` is the byte offset of the cursor within the `text`; if omitted, it defaults to the end.

The server responds with `complete_result`.

## Server → Client

### chat — Official Reply

```json
{"type": "chat", "text": "Hello! How can I help you?"}
```

An independent, complete dialogue output, including non-streaming delivered body text, Hook output, and Cron/Elnis background reports. Background reports also enter the chat area. The optional `blocks` use the same structure as stream events and take precedence over `text` when present. Body text submitted via the `send` event of a `stream` will not be resent as a `chat` message; the client must handle both types of messages.

### attachment — File or Image Output

```json
{"type":"attachment","attachment":{"id":"attachment-id","name":"report.pdf","mime_type":"application/pdf","size":2048,"kind":"file"}}
```

`kind` is `image` or `file`; the `id` in the description is the attachment ID of the current connection, not a MediaID or disk path. Mixed outputs send `chat` and `attachment` in their original order. The client retrieves attachment content via HTTP; `send_file` uses the same set of protocols, regardless of whether it is a web page or TUI.

### notice — Notification

```json
{"type": "notice", "level": "WARN", "text": "Model call failed, retrying..."}
```

Plain text runtime prompts, including retries, Hook failures, and configuration warnings. `level` can be `DEBUG`, `INFO`, `WARN`, or `ERROR`; if omitted, it is treated as `INFO`. Notifications do not carry files, media, or background reports.

Notifications are queued separately by platform and sent in target order; they are not automatically resent when the queue is full. Notifications will not migrate to a new connection for the same user.

### command_result — Command Result

```json
{"type":"command_result","request_id":"m-1","command":{"raw":"/detail","prefix":"/","Name":"detail","args":""},"result":{"status":"success","kind":"text","data":{"content":"Nothing to confirm"}}}
```

`request_id` is associated with `input.id` as-is, and the result is sent only to the original connection; command results are not returned via `notice`. `command` preserves the original call context; the `command.Name` for appending a confirmation receipt is `append`. Confirmation actions are submitted via a new `input.text`; permissions and target states are still validated by the server, and the action ID is not an authorization credential.

`result.status` expresses the business status of the command; read `data` only when it is `success`, and display `result.message` upon rejection or failure. `kind` describes the structure of the successful data, and `confirmation` provides the confirmation action. `status` is an internal business status, not an HTTP status or protocol error code.

Program or IO failures pass original descriptions via the `error` string; when this field exists, it should be displayed with priority, and `result.data` should not be read. Omit `error` when there are no failures. Client timeouts or disconnections do not mean that backend execution has been cancelled.

### status — Runtime Status

```json
{"type":"status","snapshot":{"Phase":"llm","Provider":"openai","Model":"gpt-4o"}}
```

The snapshot is used to display the current runtime status. Status updates may be merged; final states that have already been submitted may still be received after cancellation. `Phase` can be `idle`, `preparing`, `llm`, `tool`, `sending`, `done`, or `error`.

### stream — Message Stream Event

```json
{
  "type": "stream",
  "stream_id": 1,
  "event": {"type": "text_delta", "text": "Hello"}
}
```

`stream_id` identifies a single logical message within the current connection, used to separately maintain the main body and reasoning preview; it is not a request ID and cannot be used for reconnection recovery.

Events may contain `text`, `actions`, `blocks`, or tool result fields. `blocks` is an ordered list of `{kind, content}`; `actions` contains `id`, `label`, and `command`, which the client can submit as a new `input.text`.

| event.type | Client Handling |
|------------|-----------------|
| text_delta / reasoning_delta | Append the delta to the main body/reasoning preview of the stream |
| reasoning_done | End reasoning generation and preserve existing content |
| reset | Clear the main body and reasoning preview of the stream; `stream_id` can be reused for retries |
| finish | Current generation has ended, but preserve the preview and stream association, waiting for `send` or a retry |
| send | Replace the main body preview entirely with the final `text` or `blocks` and submit; preserve reasoning content and end the stream association |
| tool_result | Sent after the tool result is persisted, associated via `tool_call_id`; see `GET /api/v1/tools/{name}/detail` for details |

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

The `id` of the completion candidates corresponds to the request ID; `ReplaceStart` and `ReplaceEnd` are byte offsets in the original input text.

### error — Error

```json
{"type": "error", "text": "context deadline exceeded"}
```

An error that occurred during processing.

## Configuration

For CLI listening, authentication, and environment variable configuration, see [CLI Remote Configuration](configuration.md#cli-远程配置).

For more frontend examples, see [elbot-showcase/frontend](https://github.com/Elfreese/elbot-showcase/tree/main/frontend).
