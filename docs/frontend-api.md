# 前端 API

ElBot 前后端分离，CLI 远程协议通过 WebSocket 交换 JSON 消息，通过同一端口的 HTTP 接口收发附件和调用结构化管理操作。TUI、Web、移动端和脚本共用协议，客户端自行处理展示和下载交互。

## 连接

```
ws://<host>:<port>/cli/v1/ws
```

默认地址 `ws://127.0.0.1:32172/cli/v1/ws`。地址和端口在 `[platform.cli]` 配置中修改。

## 静态前端托管

启用 CLI 远程服务后，在主配置文件目录放置 `frontend/index.html`，即可通过 `http://<host>:<port>/` 访问。静态页面与 WebSocket 共用 `server.listen`，不提供内置模板。

- 服务启动时识别 `frontend/index.html`；首次添加入口后需重启。`/` 返回该入口，目录 URL 可读取其中的 `index.html`，但不提供目录列表或 SPA 路径回退。
- 文件仅从 `frontend/` 内提供，禁止路径越界和符号链接逃逸；静态文件无需认证，操作 ElBot 前仍须通过 `hello` 鉴权。

前端根据页面协议连接 `/cli/v1/ws`：HTTP 使用 `ws:`，HTTPS 使用 `wss:`；TLS 由部署环境提供。

## 鉴权

连接建立后，客户端必须立即发送一条 `hello` 消息：

```json
{
  "type": "hello",
  "client_id": "my-client",
  "token": "your-token"
}
```

服务端校验 token 后回复：

- `hello_ok` — 鉴权通过，可以开始交互
- `error` — 鉴权失败，连接关闭

```json
{"type": "hello_ok", "client_id": "my-client", "media_token": "connection-media-token", "api_token": "connection-api-token"}
```

Token 在 `[platform.cli.server.tokens]` 中按 `client_id` 映射到环境变量名；配置细节见 [CLI 远程配置](configuration.md#cli-远程配置)。

`media_token` 仅用于下述 HTTP 附件接口，通过 `Authorization: Bearer <media_token>` 发送，不放入 URL。它绑定当前连接，断连失效；相同 `client_id` 的连接相互隔离。

`api_token` 仅用于把追加输入确认后的动作路由回当前连接，通过 `X-CLI-Connection` 发送，不单独授予权限。管理 API 请求还须携带配置的 client token 和 `X-Client-ID: <client_id>`。

## 消息格式

消息为 JSON。各类型字段见对应章节；`id` 用于关联输入与补全响应，命令结果的 `request_id` 回传输入 ID。

## HTTP 附件接口

所有接口都需要当前 WebSocket 连接的 `media_token`，与静态页面是否存在无关：

| 接口 | 请求与响应 |
|------|------------|
| `POST /cli/v1/files` | `multipart/form-data`，单个文件字段 `file`；成功返回 201 和附件描述 |
| `GET /cli/v1/files/{id}` | 返回文件内容；HEAD 返回相同元信息而不返回正文 |
| `DELETE /cli/v1/files/{id}` | 撤销当前连接的附件授权，成功返回 204 |

上传响应与 `attachment` 描述相同；一次 POST 只上传一个 `file`，多文件须分别上传。浏览器可用带认证头的 `fetch` 获取 Blob 并自行预览或下载。GET 响应使用 `Content-Disposition: attachment`、`X-Content-Type-Options: nosniff` 和 `Cache-Control: no-store`。

删除或断连会撤销当前连接的附件授权；断连后需重新上传。认证失败返回 401；附件不存在或不属于当前连接返回 404；请求格式错误返回 400；服务停止接入返回 503；媒体处理失败返回 500 和统一文案“附件处理失败”。

## HTTP 管理 API

管理 API 的端点、请求和响应字段见 [OpenAPI 文档](frontend-api.openapi.yaml)；在线版见[官网 API 文档](https://elfreese.github.io/elbot-landing/docs/api/)。

## 客户端 → 服务端

### input — 发送消息

```json
{"type": "input", "text": "你好"}
```

将文本作为用户输入交给 Agent 处理。服务端不回复确认，处理结果通过 chat/stream 等消息异步返回。

上传附件后，将返回的 ID 放入 `file_ids`，也支持省略正文的纯附件消息：

```json
{"type":"input","id":"m-1","text":"看看这个文件","file_ids":["attachment-id"]}
```

服务端先处理正文，再按 `file_ids` 顺序处理附件。图片转为图片输入；其他文件在单文件 256 KiB、累计展开 1 MiB 内，有效 UTF-8 且不含 NUL 时按文件名和代码围栏展开为文本，其余作为文件输入。该额度只控制文本展开，上传大小限制复用 Media 配置。

附件必须属于当前连接；准备任一附件失败时，不提交部分输入，而是返回 `error` 和原输入的 `id`，正文为“附件处理失败”。附件不会因发送输入自动释放，客户端可在不再需要时调用 DELETE。

### complete — 请求补全

```json
{"type": "complete", "id": "c-1", "text": "/mod", "cursor": 4}
```

请求当前输入文本的补全候选。`id` 用于匹配响应。`cursor` 为光标在 `text` 中的字节偏移，省略时取末尾。

服务端回复 `complete_result`。

## 服务端 → 客户端

### chat — 正式回复

```json
{"type": "chat", "text": "你好！有什么可以帮你的？"}
```

独立的完整对话输出，包括非流式投递的正文、Hook 输出以及 Cron／Elnis 后台报告。后台报告也进入聊天区。可选 `blocks` 与流事件使用相同结构，存在时优先于 `text`。通过 `stream` 的 `send` 事件提交的正文不会再以 `chat` 重复发送，客户端需同时处理两种消息。

### attachment — 文件或图片输出

```json
{"type":"attachment","attachment":{"id":"attachment-id","name":"report.pdf","mime_type":"application/pdf","size":2048,"kind":"file"}}
```

`kind` 为 `image` 或 `file`；描述中的 `id` 是当前连接的附件 ID，不是 MediaID 或磁盘路径。混合输出按原顺序发送 `chat` 和 `attachment`。客户端通过 HTTP 获取附件内容；`send_file` 使用同一套协议，不区分网页或 TUI。

### notice — 通知

```json
{"type": "notice", "level": "WARN", "text": "模型调用失败，正在重试…"}
```

纯文本运行提示，包括重试、Hook 失败和配置告警。`level` 可取 `DEBUG`、`INFO`、`WARN`、`ERROR`；省略时按 `INFO` 处理。通知不承载文件、媒体或后台报告。

通知按平台分别排队，并按目标顺序发送；队列满时不自动重发。通知不会迁移到同一用户的新连接。

### command_result — 命令结果

```json
{"type":"command_result","request_id":"m-1","command":"help","result":{"kind":"text","data":{"content":"命令列表"},"confirmation":null},"error":null}
```

`request_id` 原样关联 `input.id`，结果只发给原连接；命令结果不通过 `notice` 返回。追加确认回执使用 `command="append"`。确认动作以新的 `input.text` 提交，权限和目标状态仍由服务端校验，动作 ID 不是授权凭据。

成功时 `error` 为 `null`；失败时包含 `kind`、`code`、`message`，完整失败的 `result` 为 `null`。部分结果可同时带 `result` 和 `error`。客户端超时或断线不代表后端执行已取消。

### status — 运行状态

```json
{"type":"status","snapshot":{"Phase":"llm","Provider":"openai","Model":"gpt-4o"}}
```

快照用于展示当前运行状态。状态更新可能合并；取消后仍可能收到已提交的终态。`Phase` 为 `idle`、`preparing`、`llm`、`tool`、`sending`、`done` 或 `error`。

### stream — 消息流事件

```json
{
  "type": "stream",
  "stream_id": 1,
  "event": {"type": "text_delta", "text": "你好"}
}
```

`stream_id` 标识当前连接中的单条逻辑消息，用于分别维护正文和推理预览；它不是请求 ID，也不能用于重连恢复。

事件可包含 `text`、`actions`、`blocks` 或工具结果字段。`blocks` 是有序的 `{kind, content}` 列表；`actions` 含 `id`、`label`、`command`，客户端可将命令作为新的 `input.text` 提交。

| event.type | 客户端处理 |
|------------|------------|
| text_delta / reasoning_delta | 将增量追加到该流的正文／推理预览 |
| reasoning_done | 结束推理生成，保留已有内容 |
| reset | 清除该流的正文和推理预览；重试可复用 `stream_id` |
| finish | 当前生成结束，但保留预览和流关联，等待 `send` 或重试 |
| send | 用最终 `text` 或 `blocks` 整体替换正文预览并提交；保留推理内容并结束流关联 |
| tool_result | 工具结果持久化后发送，可通过 `tool_call_id` 关联；详情见 `GET /api/v1/tools/{name}/detail` |

### complete_result — 补全结果

```json
{
  "type": "complete_result",
  "id": "c-1",
  "items": [
    {
      "Text": "/model",
      "Label": "/model",
      "Description": "切换或查看模型",
      "Kind": "command",
      "ReplaceStart": 0,
      "ReplaceEnd": 4
    }
  ]
}
```

补全候选的 `id` 对应请求 ID；`ReplaceStart` 和 `ReplaceEnd` 是原输入文本中的字节偏移。

### error — 错误

```json
{"type": "error", "text": "context deadline exceeded"}
```

处理过程中发生的错误。

## 配置

CLI 监听、认证和环境变量配置见 [CLI 远程配置](configuration.md#cli-远程配置)。

更多前端示例见 [elbot-showcase/frontend](https://github.com/Elfreese/elbot-showcase/tree/main/frontend)。
