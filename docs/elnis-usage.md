# Elnis 配置与使用

本文承接 [Elnis 监听枢纽](elnis.md)，讲如何启用 Elnis、配置 Elwisp 策略，并用 Elvena 投递事件。

## 快速开始

Elnis 默认关闭。启用时先在 `app.toml` 中声明配置文件入口：

```toml
[config_files]
elnis = "elnis.toml"
```

然后在 `elnis.toml` 中配置：

```toml
enabled = true # Elnis 总开关；false 时不启动 HTTP runtime。
allowed_tools = ["web_search", "web_extract"] # 默认允许 Elwisp 预加载的 ElBot 内部工具。

[http]
addr = "127.0.0.1:32170" # 建议先绑定本地地址，需要暴露时再用反代或内网转发。
max_body_bytes = 1048576 # 单个 Elvena 请求 body 上限。
queue_size = 128 # llm 模式后台队列长度。
workers = 2 # llm 模式后台 worker 数。
read_header_timeout_seconds = 5 # 请求头读取期限。
read_timeout_seconds = 30 # 整个请求（含 body）读取期限。
write_timeout_seconds = 300 # handler 执行及响应写出期限；direct 模式可能包含外部 I/O，因此默认较宽松。
idle_timeout_seconds = 60 # keep-alive 空闲连接期限。

[tokens.home]
token_env = ["ELNIS_HOME_TOKEN"] # 从系统环境变量或配置目录 .env 读取。

[delivery_disabled]
targets = [
  # { platform = "telegram" }, # 禁用 telegram 整个平台所有投递。
  # { platform = "telegram", type = "private", id = "123456789" },
  # { platform = "qqonebot", type = "group", id = "987654321" },
]

# Elwisp 默认启用；只有需要限制 token、覆盖投递策略或禁用时才配置。
[elwisps.server-watchdog]
allowed_tokens = ["home"]
allowed_tools = ["shell", "web_search"] # 存在时覆盖顶层 allowed_tools。
disabled_external_tools = ["danger_tool"] # 外部工具默认允许，此处只禁用指定工具。
disabled_targets = [
  # { platform = "qqonebot", type = "group", id = "987654321" },
]

[elwisps.spike-checker]
enabled = false # 只有显式 enabled=false 才禁用该 Elwisp。
```

`.env` 示例：

```dotenv
ELNIS_HOME_TOKEN=change-me
```

配置说明：

- `enabled=false` 时不启动 Elnis HTTP runtime。
- `elbot cli` 是 CLI-only 模式，即使 `enabled=true` 也不会启动 Elnis HTTP runtime；需要接收 Elvena 事件时请使用 `elbot run` 或 `elbot service run`。
- 当前 `targets=[{"platform":"cli"}]` 只适用于 Elnis 与 CLI 同处一个 `elbot run` 前台进程，或 service 模式启用 CLI 远程服务端的场景；独立 `elbot cli` 进程需连接服务端后才能收到通知。
- token 原文不写入配置，推荐放在系统环境变量或配置目录 `.env`。
- `token_env` 可以写多个环境变量名，按顺序尝试。
- Elwisp 默认启用；没写 `[elwisps.<name>]`、写了但没写 `enabled`、或写了 `enabled=true` 都会接收。
- 只有显式 `enabled=false` 才会禁用对应 Elwisp。
- `allowed_tokens` 限制哪些 token 可以代表该 Elwisp 投递事件；不写则允许任意已认证 token。
- 顶层 `allowed_tools` 是 ElBot 内部工具默认白名单，单个 Elwisp 的 `allowed_tools` 存在时覆盖它。
- 外部工具默认允许；单个 Elwisp 可以用 `disabled_external_tools` 禁用指定外部工具。
- Elnis 默认允许投递；只有 `[delivery_disabled].targets` 或单 Elwisp `disabled_targets` 显式列出的目标会被禁止。
- HTTP 超时配置小于等于 0 时使用上述安全默认值，不能用 0 关闭超时。
- 非 loopback 地址应放在反向代理后，由代理负责 TLS、连接上限和请求速率限制；应用层超时不能代替这些边界。

如果想让 ElBot 帮你生成 Elwisp 监听器，可以在 work 模式下向超级管理员会话提出需求，例如：

```text
@tool:elwisp_creator 帮我创建一个监听 RSS 更新并通过 Elnis 投递摘要的 Elwisp
```

`elwisp_creator` 会提供协议说明、配置片段、事件模板、代码脚手架和测试命令；真正写文件或运行命令仍会继续使用对应工具。

启动 ElBot 后，可以用 curl 测试一个 `direct` 事件：

```bash
curl -sS http://127.0.0.1:32170/elvena/v3/events \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer change-me' \
  -d '{
    "version":"elvena.v3",
    "elwisp":{"name":"server-watchdog"},
    "source":"minecraft-main",
    "id":"cpu-alert-001",
    "mode":"direct",
    "title":"服务器 CPU 异常",
    "content":"minecraft-main CPU 使用率超过阈值。",
    "targets":[{"platform":"cli"}]
  }'
```

如果同一个 `elwisp.name + source + id` 再次发送，Elnis 会返回 duplicate，不重复分发。

## Elvena 请求示例

```json
{
  "version": "elvena.v3",
  "elwisp": {
    "name": "server-watchdog",
    "tags": ["server", "prod"]
  },
  "source": "minecraft-main",
  "id": "cpu-alert-001",
  "mode": "llm",
  "title": "服务器 CPU 异常",
  "format": "elyph",
  "content": "#task investigate_cpu_alert - 检查服务器 CPU 异常并判断是否需要通知",
  "model_slot": "elwisp2",
  "session_mode": "work",
  "tool_list_names": ["shell"],
  "tools": [
    {
      "name": "server_status",
      "description": "查询 minecraft-main 当前服务状态和最近错误摘要",
      "schema": {
        "type": "object",
        "properties": {
          "detail": {"type": "boolean"}
        }
      },
      "timeout_seconds": 10,
      "endpoint": "http://127.0.0.1:32171/tools/server_status"
    }
  ],
  "targets": [
    {"platform": "cli"},
    {"platform": "telegram", "type": "private", "id": "123456789"}
  ],
  "meta": {
    "severity": "warning",
    "host": "mc-main-01"
  }
}
```

### direct calls-only 示例

```json
{
  "version": "elvena.v3",
  "elwisp": {"name": "hook-recall"},
  "source": "rules-hook",
  "id": "recall-qqonebot-1024",
  "mode": "direct",
  "targets": [{"platform": "qqonebot", "type": "group", "id": "987654321"}],
  "calls": [
    {
      "kind": "capability",
      "name": "message.recall",
      "platform": "qqonebot",
      "target": {"platform": "qqonebot", "type": "group", "id": "987654321"},
      "params": {"message_id": 1024}
    }
  ]
}
```


## Segments（多模态消息段）

Elvena v3 支持通过 `segments` 字段发送图片和文件。`content` 保留为纯文本 fallback；direct/record 请求中 `content`、`segments`、`calls` 至少提供一个，LLM 模式仍必须提供 `content`。

`segments` 为空时行为不变，非空时优先 segments 渲染，content 作为附加文本。

### Segment 字段

| 字段 | 类型 | 必填 | 说明 |
| --- | ---: | :---: | --- |
| `kind` | string | 是 | `text`、`image`、`file`。 |
| `text` | string | text 必填 | 纯文本内容，不落盘。 |
| `url` | string | image/file 必填 | `http://`、`https://` 或 `data:` base64 URI。 |
| `name` | string | 否 | 文件名，用于下载保存和展示。 |
| `mime_type` | string | 否 | MIME 类型提示。 |

### 下载与存储

- Elnis 接收后自动下载到 `sandbox/elnis/<elwisp名>/<事件id>/`。
- 发送到 LLM 时使用原始 URL（多模态模型可直接看图），沙盒保留副本。
- direct 模式同样支持 image/file 输出，平台不支持时自动降级为文字描述。
- 文件大小受 `elnis.toml` 的 `[segment].max_file_bytes` 限制（默认 100MB）。
- `data:` URI 仅支持 base64 编码，解码后同样受限。
- `file://` 等本地协议禁止。

### 示例

```json
{
  "version": "elvena.v3",
  "elwisp": {"name": "monitor"},
  "source": "prod-server",
  "id": "cpu-chart-002",
  "mode": "direct",
  "title": "CPU 异常",
  "content": "CPU 飙到 90%",
  "segments": [
    {"kind": "text",  "text": "服务器 CPU 飙到 90%，详见附图。"},
    {"kind": "image", "url": "https://monitor.example.com/chart.png", "name": "cpu_chart.png"},
    {"kind": "file",  "url": "https://logs.example.com/dump.txt", "name": "cpu_dump.txt"}
  ],
  "targets": [{"platform": "cli"}]
}
```

### LLM 结果中的 report_segments

后台 LLM 处理事件后，`JSONResult` 的 `report_segments` 可附带图片/文件路径，Elnis 会在报告发送时一并投递。`url` 必须是当前任务工作目录内的相对路径，不能使用绝对路径、`~` 或 `..`。


```json
{
  "completed": true,
  "need_report": true,
  "report": "分析完成，见截图。",
  "report_segments": [
    {"type": "image", "url": "chart.png"}
  ]

}
```


常用字段：

| 字段 | 必填 | 说明 |
| --- | ---: | --- |
| `version` | 是 | 协议版本，当前为 `elvena.v3`。 |
| `elwisp.name` | 是 | Elwisp 名称，也是来源身份之一；仅允许英文字母、数字、`_`、`-`，不允许点号。 |
| `elwisp.tags` | 否 | Elwisp 标签，用于日志和统计。 |
| `source` | 是 | 具体事件源，例如服务名、脚本名、RSS 名。 |
| `id` | 是 | source 内唯一事件 ID。 |
| `created_at` | 否 | 外部事件发生时间；缺失时使用接收时间。 |
| `mode` | 是 | `record`、`direct` 或 `llm`。 |
| `title` | 否 | 事件标题，用于通知和后台 Session 标题。 |
| `format` | 否 | `text` 或 `elyph`，默认 `text`。 |
| `content` | 否 | 事件主体。LLM 模式必填，推荐使用 ELyph Task Notation（任务表示法）`#task`；direct/record 模式可为空，但 `content`、`segments`、`calls` 至少提供一个。 |
| `model_slot` | 否 | Elnis LLM 模型槽位，仅支持 `elwisp1`、`elwisp2`、`elwisp3`；未填写或对应槽位未配置时回退到 `work`。 |
| `session_mode` | 否 | LLM 后台 Session 模式：`work` 或 `chat`，默认 `work`；`chat` 不注入工具 schema，适合不需要工具的低成本后台处理。 |
| `tool_list_names` | 否 | 后台任务预加载的 ElBot 内部工具名或 Skill 名；普通工具注入 schema，Skill 注入任务说明并自动注入对应 runner；必须在 Elnis `allowed_tools` 裁决范围内，`discover_tool` 会被忽略。 |
| `tools` | 否 | Elwisp 随事件声明的外部工具；默认允许，命中该 Elwisp 的 `disabled_external_tools` 时拒绝。 |
| `targets` | 是 | Elwisp 期望投递目标数组，`{"platform":"telegram"}` 表示发给平台超级管理员，`type=private/group` 且带 `id` 时发指定私聊/群聊，`{"platform":"all"}` 表示所有已启用平台超级管理员。最终仍由 Elnis 裁决。 |
| `calls` | 否 | Elvena v3 动作调用数组。`kind="raw"` 透传平台原始 API，`kind="capability"` 使用统一能力名；首批 capability 包含 `message.recall`、`member.mute`、`chat.leave`。direct 请求只有 `calls` 且没有 `content`/`segments` 时只执行 API，不发送消息。 |
| `meta` | 否 | 原始补充数据，只做记录和 prompt 附加。 |

HTTP 响应只表示 Elnis 已接收或拒绝请求，不等待 LLM 完成。


```json
{
  "accepted": true,
  "duplicate": false,
  "event_key": "server-watchdog/minecraft-main/cpu-alert-001",
  "mode": "llm",
  "status": "queued"
}
```

每个 HTTP 请求体必须且只能包含一个 JSON 值，尾随空白允许，尾随第二个 JSON 值或非法内容会返回 `400`。未知字段会被忽略，不参与协议语义、规范化、事件 `content_hash` 或审计；需要正式语义的字段应加入对应 Elvena 协议版本。


## 投递目标与安全边界

Elwisp 可以在 `targets` 中声明期望目标，但最终目标由 Elnis 裁决。

`targets` 必须是数组：

```json
{
  "targets": [
    {"platform": "telegram"},
    {"platform": "telegram", "type": "private", "id": "123456789"},
    {"platform": "qqonebot", "type": "group", "id": "987654321"},
    {"platform": "all"}
  ]
}
```

语义：

- 只写 `platform`：投递到该平台超级管理员。
- `type=private`：投递到指定平台私聊。
- `type=group`：投递到指定平台群聊。
- `platform=all`：投递到所有已启用平台超级管理员，不能同时写 `type` 或 `id`。

Elnis 默认允许投递；命中 `[delivery_disabled].targets` 或单 Elwisp `disabled_targets` 时才禁止。禁用配置中的 `{ platform = "telegram" }` 表示禁用该平台所有投递。

安全约定：

- token 名只用于日志和审计，不等同于 Elwisp 身份。
- token 原文不写日志。
- `model_slot` 只能选择 `elwisp1`、`elwisp2`、`elwisp3`，不能指定任意内部模式名；`session_mode` 只允许 `work` 或 `chat`，不影响模型槽位。
- Elwisp 不能绕过 Tool Runtime 和 Security Policy 调用 ElBot 内部工具；`tool_list_names` 中的工具名或 Skill 名都会经过 Elnis `allowed_tools` 裁决。
- Elwisp 声明的外部 `tools` 默认允许，并通过 ToolRun 以 `elwisp_<elwisp>_<tool>` 形式注入为模型可调用函数名；单个 Elwisp 可用 `disabled_external_tools` 禁用指定工具。
- 外部工具调用由 Elnis 对声明的 endpoint 发起 HTTP JSON POST；外部工具自身负责实际风险边界，Elnis 侧按 low 风险处理。
