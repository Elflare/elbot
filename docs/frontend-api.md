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
| cursor | int | 光标位置（仅补全请求） |
| items | array | 补全候选列表（仅补全响应） |
| snapshot | object | 运行状态快照（仅 status） |

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

Agent 完成处理后发送的完整回复消息。

### notice — 通知

```json
{"type": "notice", "text": "Cron 任务已完成"}
```

非对话场景的通知消息，如 Cron 结果、Hook 输出、Elnis 事件报告等。

### reasoning — 推理过程

```json
{"type": "reasoning", "text": "用户在问天气，我需要调用搜索工具..."}
```

支持推理输出的模型在生成过程中推送的推理文本。前端可选择展示或隐藏。

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

运行状态快照，前端可用于状态栏展示。`phase` 取值：

| Phase | 说明 |
|-------|------|
| idle | 空闲 |
| preparing | 准备中 |
| llm | LLM 生成中 |
| tool | 工具执行中 |
| done | 完成 |
| error | 错误 |

### stream_append — 流式追加

```json
{"type": "stream_append", "text": "你好"}
```

LLM 流式输出的一段增量文本。前端应追加到当前正在生成的消息末尾。

### stream_replace — 流式替换

```json
{"type": "stream_replace", "text": "你好！有什么可以帮你的？"}
```

流式结束后，用最终完整文本替换之前累积的流式内容。可能和流式拼接的结果不同（Hook 可能修改了输出）。

### stream_finish — 流式结束

```json
{"type": "stream_finish"}
```

标记当前流式消息已结束，前端可以固定该消息。

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

## 最小客户端示例

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

更多前端示例见 [elbot-showcase/frontend](https://github.com/Elfreese/elbot-showcase/tree/main/frontend)。
