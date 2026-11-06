# 前端 API

ElBot 前后端分离，CLI 远程协议通过 WebSocket 交换 JSON 消息，通过同一端口的 HTTP 接口收发附件。TUI、Web、移动端和脚本共用这套协议，客户端自行处理展示和下载交互。

## 连接

```
ws://<host>:<port>/cli/v1/ws
```

默认地址 `ws://127.0.0.1:32172/cli/v1/ws`。地址和端口在 `[platform.cli]` 配置中修改。

## 静态前端托管

启用 CLI 远程服务后，可在主配置文件所在目录放置 `frontend/index.html`，访问 `http://<host>:<port>/` 即可打开。页面和 WebSocket 共用 `server.listen`，无需另启服务器，也没有内置前端模板：

```text
配置目录/
├── app.toml
├── .env
└── frontend/
    ├── index.html
    ├── settings.html
    ├── app.js
    └── assets/
        └── app.css
```

- 初始化时自动识别入口；目录或入口不存在时不启用托管，其他读取错误或入口不是普通文件时报错。首次放入入口后需重启服务。
- `/` 返回 `index.html`；`/settings.html`、`/app.js` 和 `/assets/app.css` 返回对应文件。目录路径以 `/` 结尾时读取其中的 `index.html`，没有入口则返回 404，不列出目录内容。
- 支持多个实际 HTML 页面和 hash 路由；不提供 SPA 路径回退，缺失的 `/settings` 等路径返回 404。
- 文件按请求从磁盘读取，修改已有文件后可刷新页面加载，缓存遵循 HTTP 行为。只开放 `frontend/` 内的文件，禁止路径越界和符号链接逃逸。
- 静态文件无需登录即可加载；前端自行提供登录界面，通过下述 `hello` 认证后才能操作 ElBot。不要把 token 写进前端文件或 URL，默认只在页面内存保留。

前端应根据当前页面地址连接 `/cli/v1/ws`：HTTP 页面用 `ws:`，HTTPS 页面用 `wss:`，见下方[最小客户端示例](#最小客户端示例)。使用 HTTPS 时由部署环境提供 TLS 入口；ElBot 的监听与认证配置见[CLI 远程配置](configuration.md#cli-远程配置)。

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
{"type": "hello_ok", "client_id": "my-client", "media_token": "connection-media-token"}
```

Token 在 `[platform.cli.server.tokens]` 中按 client_id 映射到环境变量名，服务端从对应环境变量读取实际值。

`media_token` 仅用于下述 HTTP 附件接口，绑定当前已认证的 WebSocket 连接；断连后失效，重连获取新凭据。相同 client_id 的多个连接各自隔离。凭据保留在客户端内存，通过 `Authorization: Bearer <media_token>` 发送，不放进 URL。

## 消息格式

所有消息均为 JSON，共享同一个结构：

| 字段 | 类型 | 说明 |
|------|------|------|
| type | string | 消息类型，见下表 |
| id | string | 消息 ID，用于补全请求/响应配对，以及关联附件输入错误 |
| client_id | string | 客户端标识 |
| token | string | 鉴权 token（仅 hello） |
| media_token | string | 当前连接的 HTTP 附件凭据（仅 hello_ok） |
| file_ids | array of string | 当前连接已上传或收到的附件 ID（仅 input） |
| attachment | object | 附件描述（仅 attachment），字段见下文 |
| text | string | 文本内容 |
| level | string | 通知等级（仅 notice） |
| cursor | int | 光标位置（仅补全请求） |
| items | array | 补全候选列表（仅补全响应） |
| snapshot | object | 运行状态快照（仅 status） |
| stream_id | uint64 | 逻辑消息流标识（仅 stream），用于关联同一条消息的预览、重置和提交 |
| event | object | 消息流事件（仅 stream），字段见下文 |

## HTTP 附件接口

所有接口都需要当前 WebSocket 连接的 `media_token`，与静态页面是否存在无关：

| 接口 | 请求与响应 |
|------|------------|
| `POST /cli/v1/files` | `multipart/form-data`，单个文件字段 `file`；成功返回 201 和附件描述 |
| `GET /cli/v1/files/{id}` | 返回文件内容；HEAD 返回相同元信息而不返回正文 |
| `DELETE /cli/v1/files/{id}` | 撤销当前连接的附件授权，成功返回 204 |

上传响应与 `attachment` 消息中的描述相同。文件名、类型、大小以归档结果为准，包括已有的图片压缩行为。多文件分别上传，不在一个 POST 中夹带其他字段或文件。

GET 使用 `Content-Disposition: attachment` 和 `X-Content-Type-Options: nosniff`，附件响应使用 `Cache-Control: no-store`。浏览器可先通过带认证头的 `fetch` 获取 Blob，再自行处理预览或下载。

删除或断连只撤销附件授权并结束连接持有，不直接删除 Media 对象；已取得独立持有的在途操作可以完成，最终回收仍由 Media 管理。刷新或重连后，原附件 ID 不可复用，需要重新上传。

认证无效返回 401；附件不存在或不属于当前连接返回 404；请求格式错误返回 400；服务停止接入时返回 503；媒体处理失败（包括超过上传限制）返回 500 和统一文案“附件处理失败”，具体原因记录在服务端日志。

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

请求当前输入文本的命令补全候选。`id` 用于匹配响应。`cursor` 为光标在 `text` 中的字节偏移，省略时取末尾。

服务端回复 `complete_result`。

## 服务端 → 客户端

### chat — 正式回复

```json
{"type": "chat", "text": "你好！有什么可以帮你的？"}
```

独立的完整对话输出，包括非流式投递的正文、Hook 输出以及 Cron／Elnis 后台报告。后台报告也进入聊天区。通过 `stream` 的 `send` 事件提交的正文不会再以 `chat` 重复发送，客户端需同时处理两种消息。

### attachment — 文件或图片输出

```json
{"type":"attachment","attachment":{"id":"attachment-id","name":"report.pdf","mime_type":"application/pdf","size":2048,"kind":"file"}}
```

`kind` 为 `image` 或 `file`；描述中的 `id` 是当前连接的附件 ID，不是 MediaID 或磁盘路径。混合输出按原顺序发送 `chat` 和 `attachment`。客户端通过 HTTP 获取附件内容；`send_file` 使用同一套协议，不区分网页或 TUI。

### notice — 通知

```json
{"type": "notice", "level": "WARN", "text": "模型调用失败，正在重试…"}
```

纯文本通知，包括命令返回结果，以及重试、Hook 失败和配置告警等运行提示。命令结果使用 `INFO`，可以包含多行长文本。`level` 可取 `DEBUG`、`INFO`、`WARN`、`ERROR`；省略时按 `INFO` 处理。通知不承载文件、媒体或后台报告。

通知按平台独立排队，同一目标按顺序发送。队列满时记录告警，不自动重发；通知默认最多等待 30 秒。原远程连接的回复通知不会迁移到同一用户的新连接。排队中的工具预览和重试提示随请求取消；已经发生的失败和超时提示由其来源决定保留。

### status — 运行状态

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

运行状态快照，前端可用于状态栏展示。请求取消后仍发布已提交的终态；后台任务没有前台展示目标，接管后只展示新状态。状态更新会合并，应用整体关闭不保证向已关闭的前端补发终态。`phase` 取值：

| Phase | 说明 |
|-------|------|
| idle | 空闲 |
| preparing | 准备中 |
| llm | LLM 生成中 |
| tool | 工具执行中 |
| sending | 回复发送中 |
| done | 完成 |
| error | 错误 |

### stream — 消息流事件

```json
{
  "type": "stream",
  "stream_id": 1,
  "event": {"type": "text_delta", "text": "你好"}
}
```

`stream_id` 标识一条逻辑消息，不是 Session ID 或请求 ID。客户端按该标识维护正文和推理预览，不能把所有增量拼到同一个全局缓冲区。标识只用于当前连接中的消息关联，不是重连后恢复历史的游标。

`event` 字段：

| 字段 | 类型 | 说明 |
|------|------|------|
| type | string | 事件类型，见下表 |
| text | string | 正文／推理增量，或 `send` 的最终完整正文；空文本可能省略，按空字符串处理 |
| actions | array | 可选的消息操作；元素字段为 `ID`、`Label`、`Command`。前端可展示为按钮，点击后用 `input` 提交 `Command`，仍由服务端校验权限 |

| event.type | 客户端处理 |
|------------|------------|
| text_delta | 将 `text` 追加到该流的正文预览 |
| reasoning_delta | 将 `text` 追加到该流的推理预览，前端可选择展示或隐藏 |
| reasoning_done | 结束本次推理生成，保留已收到的推理内容 |
| reset | 删除该流当前的正文和推理预览；重试可继续使用相同 `stream_id` 生成新预览 |
| finish | 本次生成结束，保留预览及其流关联，等待最终 `send` 或后续重试的 `reset` |
| send | 用 `text` **整体替换**正文预览并提交消息，保留推理内容；移除活动流关联。没有预览时也直接创建完整消息 |

例如，最终正文可能被 Hook 改写，因此必须使用 `send` 中的全文，而不能直接固定增量拼接结果：

```json
{"type": "stream", "stream_id": 1, "event": {"type": "finish"}}
{"type": "stream", "stream_id": 1, "event": {"type": "send", "text": "你好！有什么可以帮你的？"}}
```

`send` 的空正文也必须覆盖原预览，不能忽略；正文和推理都为空时可移除该消息。`finish` 不等于正式提交，不应在收到它时移除流关联，也不要要求每个 `send` 前必须有增量或 `finish`。

推理内容是原始增量，不附加 `[thinking]`／`[/thinking]` 文本标记，展示样式由前端决定。消息流固定投递到原连接，不迁移到同一用户的新连接；后台任务接管后只展示新产生的内容，不重放接管前的推理增量。

附加内容由 Agent 输出层单独投递，不作为远程 CLI 的 `stream` 事件传送。

### complete_result — 补全结果

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

补全候选列表，`id` 对应请求时的 `id`。`replace_start`/`replace_end` 指示应替换原输入文本的字节区间。

### error — 错误

```json
{"type": "error", "text": "context deadline exceeded"}
```

处理过程中发生的错误。

## 补全 Item 字段

| 字段 | 类型 | 说明 |
|------|------|------|
| text | string | 插入的文本 |
| label | string | 展示标签 |
| description | string | 补充描述 |
| kind | string | 候选类型，如 `command`、`tool`、`fork` |
| replace_start | int | 替换起始字节偏移 |
| replace_end | int | 替换结束字节偏移 |

## 配置

在 `app.toml` 的 `[platform.cli]` 中配置：

```toml
[platform.cli]
enabled = true

[platform.cli.server]
enabled = true
listen = "127.0.0.1:32172"

[platform.cli.server.tokens]
my-client = ["ELBOT_CLI_TOKEN"]
```

`tokens` 是 client_id 到环境变量名列表的映射。服务端按顺序尝试从这些环境变量读取 token 值，与客户端发送的 token 比对。环境变量可从系统环境或配置目录 `.env` 读取。

## 典型交互流程

```
客户端                              服务端
  │                                   │
  │──── hello ──────────────────────▶│
  │◀─── hello_ok ────────────────────│
  │                                   │
  │──── input("你好") ───────────────▶│
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

图中的 `stream(1, ...)` 表示 `stream_id=1` 及对应的 `event.type`，不是实际 JSON。推理事件可缺省，状态更新可能合并；生成失败后可能收到 `reset` 并重新生成，不能依赖每次都有完整的上述序列。模型切换等命令结果通过 `notice` 返回，不需要经过 LLM 流式生成。

## 最小客户端示例

以下示例用于 ElBot 托管的浏览器页面，输入的凭据仅保留在页面内存；用 `textContent` 展示内容，不把模型输出作为 HTML。示例处理正文和推理、重试重置及最终提交；登录界面、按钮和状态栏可按需扩展。

```javascript
const output = document.createElement("pre");
document.body.append(output);
const messages = [];
const streams = new Map();
const render = () => {
  output.textContent = messages.map((m) =>
    (m.reasoning ? "[思考] " + m.reasoning + "\n" : "") + m.text
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
      // 只结束生成；保留预览和流关联，等待 send 或 reset。
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
    message.text = text; // 即使为空，也替换全部正文；不另追加一条消息。
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
      // 鉴权成功，发送一条消息
      ws.send(JSON.stringify({ type: "input", text: "你好" }));
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

更多前端示例见 [elbot-showcase/frontend](https://github.com/Elfreese/elbot-showcase/tree/main/frontend)。
