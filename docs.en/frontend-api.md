<!-- This file is auto-translated from docs/frontend-api.md. Do not edit manually. -->

# Frontend API

The ElBot client sends and receives conversations via WebSocket and manages Sessions and transfers attachments via the HTTP interface on the same port. The endpoints, requests, responses, and errors of the `/api/v1` management interface follow [OpenAPI](../docs/frontend-api.openapi.yaml), which can also be [viewed online](https://elfreese.github.io/elbot-landing/docs/api/). This document explains how to integrate them into a single conversation.

## Static Frontend Hosting

Place `frontend/index.html` next to the main configuration. After adding it for the first time, restart the CLI remote service to access it from `/`. Static files are public, and directory listing or SPA path fallback is not provided; operating ElBot still requires authentication. See [CLI Remote Configuration](configuration.md#cli-远程配置) for the listening address and token.

## Connection and Authentication

Connect to `/cli/v1/ws` (default `ws://127.0.0.1:32172/cli/v1/ws`; use HTTPS/WSS for external deployment) and immediately send:

```json
{"type":"hello","client_id":"my-client","token":"your-token"}
```

If login fails, `error` is returned and the connection is closed. On success, `hello_ok` is returned, where `api_token` is used for management interfaces and `media_token` is used for attachments. Both are sent via HTTP `Authorization: Bearer <token>` and should not be placed in the URL. Long-term tokens are used for login only; connection credentials are stored only in memory, and different connections under the same account cannot be mixed.

## Sessions and First Message

1. **Entering a new conversation**: Clear subscriptions for this connection and display a blank page. Do not create a Session, nor stop tasks from old Sessions.
2. **First transmission**: First create a Session, subscribe to the returned ID, and then send a WebSocket `input`. When opening an existing Session, subscribe and read history directly without needing to create one.
3. **Subsequent inputs**: Use a new `id` for each transmission; the `session_id` must match the subscription of this connection. Standard CLIs without a subscription may omit `session_id` and use the platform's current Session. Subscription is not equivalent to `/resume`.
4. **Reconnection**: Re-login, subscribe, and read history. The server does not replay increments during the disconnection period, and the client should not automatically replay write requests. After switching Sessions, ignore in-flight output from old Sessions based on the `session_id`.

```json
{"type":"input","id":"m-1","session_id":"cl93","text":"Hello"}
```

## HTTP Attachment Interface

`/cli/v1/files` is not part of the management API. Use a `media_token` to send `POST /cli/v1/files`, uploading one `multipart/form-data` field named `file` each time. Place the returned `id`s in `input.file_ids` in order; for inputs consisting only of attachments, `text` may be omitted. If any attachment fails to prepare, the entire input will not be submitted.

The output `attachment` frame contains attachment descriptions specific to the connection (`id`, `name`, `mime_type`, `size`, `kind`). Download via `GET /cli/v1/files/{id}`, use `HEAD` to retrieve only metadata, and `DELETE` to revoke authorization. Sending input does not release attachments; old IDs become invalid after disconnection, and files waiting to be sent must be re-uploaded.

## WebSocket Messages

| Message | Client Handling |
|---|---|
| `user_input` | Subscribers of the same account receive input echoes; this does not mean the model has processed it or the history has been persisted. Deduplicate based on `id` and history `input_ids`, not by content. |
| `chat` / `attachment` | Display complete messages and attachments in order of arrival. `chat.blocks` takes priority over `text`; `question` blocks are filled as drafts and confirmed via the management API, but model output after answering still goes through WebSocket. |
| `stream` | Maintain a preview of a single message by `stream_id` (see below). Content already submitted via `send` will not be resent as `chat`. |
| `command_result` | Sent only to the original connection. `request_id` corresponds to `input.id`; prioritize displaying `error`, otherwise display `result.blocks`, and read `result.data` only on `success`. Commands in `actions` are submitted as new inputs. |
| `complete` → `complete_result` | Sends `id`, `text`, and optional `cursor`; match responses by `id`. `cursor` (defaulting to the end) and candidate `ReplaceStart`, `ReplaceEnd` are UTF-8 byte offsets, not character indices. |
| `status` / `notice` | `status.snapshot` is a potentially merged runtime status snapshot; `notice` is an account-level text notification, not filtered by Session, with a default level of `INFO`. |
| `session_binding_changed` | The current account's binding changes from `session_id` to `next_session_id`; only notifies connections still subscribed to the old ID. The server does not change subscriptions on behalf of the client; if the page is still viewing the old Session, use `PUT /view/session` to subscribe to the new ID. Not sent for initial binding, unbinding, or simple view switching; not resent after disconnection. |
| `error` | Display `text`; associate with the original request when `id` is present. Disconnection or client timeout does not mean the background task has been cancelled. |

In stream events, `text_delta` and `reasoning_delta` append content and reasoning respectively, and `reasoning_done` ends the reasoning. `reset` clears the preview for retries; `finish` only indicates the generation has ended, and the preview must still be retained. Only upon receiving `send` should the preview be replaced by the final content or `blocks` and submitted, while retaining existing reasoning.

Tool calls and results may span across streams: pair `tool_call` and `tool_result` by `tool_call_id` rather than `stream_id`, preserving the call name, parameters, and result segments. `stream_id` is only valid for the current connection and is not used for reconnection recovery.

See [elbot-showcase/frontend](https://github.com/Elfreese/elbot-showcase/tree/main/frontend) for a complete client example.
