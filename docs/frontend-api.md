# 前端 API

ElBot 客户端通过 WebSocket 收发对话，通过同端口的 HTTP 接口管理会话、传输附件。`/api/v1` 管理接口的端点、请求、响应和错误以 [OpenAPI](frontend-api.openapi.yaml) 为准，也可[在线查阅](https://elfreese.github.io/elbot-landing/docs/api/)。本文说明如何把它们接成一次对话。

## 静态前端托管

在主配置旁放置 `frontend/index.html`，首次添加后重启 CLI 远程服务，即可从 `/` 访问。静态文件公开，不提供目录列表或 SPA 路径回退；操作 ElBot 仍须鉴权。监听地址和 token 见 [CLI 远程配置](configuration.md#cli-远程配置)。

## 连接与鉴权

连接 `/cli/v1/ws`（默认 `ws://127.0.0.1:32172/cli/v1/ws`；对外部署使用 HTTPS/WSS），立即发送：

```json
{"type":"hello","client_id":"my-client","token":"your-token"}
```

登录失败返回 `error` 并关闭连接。成功返回 `hello_ok`，其中 `api_token` 用于管理接口，`media_token` 用于附件。两者均通过 HTTP `Authorization: Bearer <token>` 发送，不放入 URL。长期 token 仅用于登录；连接凭据只保存在内存中，同一账号的不同连接不能混用。

## 会话与首条消息

1. **进入新对话**：清空本连接的订阅，显示空白页。不创建会话，也不停止旧会话的任务。
2. **首次发送**：先创建会话，订阅返回的 ID，再发 WebSocket `input`。打开已有会话则直接订阅并读取历史，不必创建。
3. **后续输入**：每次发送使用新的 `id`，`session_id` 必须与本连接的订阅一致。未订阅的普通 CLI 可省略 `session_id`，使用平台当前会话。订阅不等于 `/resume`。
4. **重连**：重新登录、订阅并读取历史。服务端不重放断线期间的增量，客户端也不要自动重放写请求。切换会话后按 `session_id` 忽略旧会话在途的输出。

```json
{"type":"input","id":"m-1","session_id":"cl93","text":"你好"}
```

## HTTP 附件接口

`/cli/v1/files` 不属于管理 API。用 `media_token` 发 `POST /cli/v1/files`，每次上传一个 `multipart/form-data` 字段 `file`。把返回的 `id` 按顺序放入 `input.file_ids`；纯附件输入可省略 `text`。任一附件准备失败，整条输入不提交。

输出的 `attachment` 帧含连接专属的附件描述（`id`、`name`、`mime_type`、`size`、`kind`）。通过 `GET /cli/v1/files/{id}` 下载，`HEAD` 只取元信息，`DELETE` 撤销授权。发送输入不会释放附件；断连后旧 ID 失效，待发送的文件须重新上传。

## WebSocket 消息

| 消息 | 客户端处理 |
|---|---|
| `user_input` | 同账号订阅者收到输入回显，不代表模型已处理或历史已落库。按 `id` 与历史的 `input_ids` 去重，不按正文去重。 |
| `chat` / `attachment` | 按到达顺序展示完整消息和附件。`chat.blocks` 优先于 `text`；`question` 块通过管理 API 填草稿、确认，回答后的模型输出仍走 WebSocket。 |
| `stream` | 按 `stream_id` 维护单条消息的预览，见下文。已由 `send` 提交的正文不会再以 `chat` 重发。 |
| `command_result` | 仅发给原连接。`request_id` 对应 `input.id`；优先展示 `error`，否则展示 `result.blocks`，仅在 `success` 时读取 `result.data`。`actions` 的命令作为新输入提交。 |
| `complete` → `complete_result` | 发送 `id`、`text` 和可选的 `cursor`，按 `id` 匹配响应。`cursor`（默认末尾）及候选的 `ReplaceStart`、`ReplaceEnd` 都是 UTF-8 字节偏移，不是字符序号。 |
| `status` / `notice` | `status.snapshot` 是可能合并的运行状态快照；`notice` 是账号级文本通知，不按会话过滤，缺省级别为 `INFO`。 |
| `error` | 展示 `text`；有 `id` 时关联原请求。断线或客户端超时不代表后台任务已取消。 |

流事件中，`text_delta`、`reasoning_delta` 分别追加正文和推理，`reasoning_done` 结束推理。`reset` 清空预览以便重试；`finish` 只表示生成结束，仍须保留预览。收到 `send` 才用最终正文或 `blocks` 替换预览并提交，保留已有推理。

工具调用与结果可能跨流：按 `tool_call_id` 而非 `stream_id` 配对 `tool_call` 和 `tool_result`，保留调用名称、参数和结果 segments。`stream_id` 仅在当前连接有效，不用于重连恢复。

完整客户端示例见 [elbot-showcase/frontend](https://github.com/Elfreese/elbot-showcase/tree/main/frontend)。
