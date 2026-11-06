# 前端 API

ElBot 前后端分离，CLI 远程协议是基于 WebSocket 的 JSON 消息协议。任何能连 WebSocket 的客户端都可以作为 ElBot 前端——TUI、Web、移动端、甚至另一台机器上的脚本。

## 连接

```
ws://<host>:<port>/cli/v1/ws
```

默认地址 `ws://127.0.0.1:32172/cli/v1/ws`。地址和端口在 `[platform.cli]` 配置中修改。

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
{"type": "hello_ok", "client_id": "my-client"}
```

Token 在 `[platform.cli.server.tokens]` 中按 client_id 映射到环境变量名，服务端从对应环境变量读取实际值。

## 消息格式

所有消息均为 JSON，共享同一个结构：

| 字段 | 类型 | 说明 |
|------|------|------|
| type | string | 消息类型，见下表 |
| id | string | 消息 ID，用于补全请求/响应配对 |
| client_id | string | 客户端标识 |
| token | string | 鉴权 token（仅 hello） |
| text | string | 文本内容 |
| level | string | 通知等级（仅 notice） |
| cursor | int | 光标位置（仅补全请求） |
| items | array | 补全候选列表（仅补全响应） |
| snapshot | object | 运行状态快照（仅 status） |
| stream_id | uint64 | 逻辑消息流标识（仅 stream），用于关联同一条消息的预览、重置和提交 |
| event | object | 消息流事件（仅 stream），字段见下文 |

## 客户端 → 服务端

### input — 发送消息

```json
{"type": "input", "text": "你好"}
```

将文本作为用户输入交给 Agent 处理。服务端不回复确认，处理结果通过 chat/stream 等消息异步返回。

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

以下代码可放在浏览器页面中运行；用 `textContent` 展示内容，不把模型输出作为 HTML。示例处理正文和推理、重试重置及最终提交；按钮和状态栏可按需扩展。

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
