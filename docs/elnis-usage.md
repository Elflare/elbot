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

[tokens.home]
token_env = ["ELNIS_HOME_TOKEN"] # 从系统环境变量或配置目录 .env 读取。

[delivery]
default_platforms = ["cli"] # Elnis 策略允许的默认投递平台。
allow_superadmins = true # 是否允许投递给目标平台超级管理员。

# Elwisp 默认启用；只有需要限制 token、覆盖投递策略或禁用时才配置。
[elwisps.server-watchdog]
allowed_tokens = ["home"]
allowed_tools = ["shell", "web_search"] # 存在时覆盖顶层 allowed_tools。
disabled_external_tools = ["danger_tool"] # 外部工具默认允许，此处只禁用指定工具。

[elwisps.server-watchdog.delivery]
default_platforms = ["cli"]
allow_superadmins = true

[elwisps.spike-checker]
enabled = false # 只有显式 enabled=false 才禁用该 Elwisp。
```

`.env` 示例：

```dotenv
ELNIS_HOME_TOKEN=change-me
```

配置说明：

- `enabled=false` 时不启动 Elnis HTTP runtime。
- token 原文不写入配置，推荐放在系统环境变量或配置目录 `.env`。
- `token_env` 可以写多个环境变量名，按顺序尝试。
- Elwisp 默认启用；没写 `[elwisps.<name>]`、写了但没写 `enabled`、或写了 `enabled=true` 都会接收。
- 只有显式 `enabled=false` 才会禁用对应 Elwisp。
- `allowed_tokens` 限制哪些 token 可以代表该 Elwisp 投递事件；不写则允许任意已认证 token。
- 顶层 `allowed_tools` 是 ElBot 内部工具默认白名单，单个 Elwisp 的 `allowed_tools` 存在时覆盖它。
- 外部工具默认允许；单个 Elwisp 可以用 `disabled_external_tools` 禁用指定外部工具。
- `default_platforms` 是 Elnis 策略允许的默认投递平台。
- `allow_superadmins=true` 表示允许投递给目标平台超级管理员。

启动 ElBot 后，可以用 curl 测试一个 `direct` 事件：

```bash
curl -sS http://127.0.0.1:32170/elvena/v1/events \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer change-me' \
  -d '{
    "version":"elvena.v1",
    "elwisp":{"name":"server-watchdog"},
    "source":"minecraft-main",
    "id":"cpu-alert-001",
    "mode":"direct",
    "title":"服务器 CPU 异常",
    "content":"minecraft-main CPU 使用率超过阈值。",
    "targets":{"platforms":["cli"],"superadmins":true}
  }'
```

如果同一个 `elwisp.name + source + id` 再次发送，Elnis 会返回 duplicate，不重复分发。

## Elvena 请求示例

```json
{
  "version": "elvena.v1",
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
  "targets": {
    "platforms": ["cli"],
    "superadmins": true
  },
  "meta": {
    "severity": "warning",
    "host": "mc-main-01"
  }
}
```

常用字段：

| 字段 | 必填 | 说明 |
| --- | ---: | --- |
| `version` | 是 | 协议版本，当前为 `elvena.v1`。 |
| `elwisp.name` | 是 | Elwisp 名称，也是来源身份之一。 |
| `elwisp.tags` | 否 | Elwisp 标签，用于日志和统计。 |
| `source` | 是 | 具体事件源，例如服务名、脚本名、RSS 名。 |
| `id` | 是 | source 内唯一事件 ID。 |
| `created_at` | 否 | 外部事件发生时间；缺失时使用接收时间。 |
| `mode` | 是 | `record`、`direct` 或 `llm`。 |
| `title` | 否 | 事件标题，用于通知和后台 Session 标题。 |
| `format` | 否 | `text` 或 `elyph`，默认 `text`。 |
| `content` | 是 | 事件主体。LLM 模式推荐使用 ELyph Task Notation（任务表示法）`#task`。 |
| `model_slot` | 否 | 模型槽位，后续用于 `elwisp1`、`elwisp2`、`elwisp3`。 |
| `tool_list_names` | 否 | 后台任务预加载的 ElBot 内部工具名；必须在 Elnis `allowed_tools` 裁决范围内，`discover_tool` 会被忽略。 |
| `tools` | 否 | Elwisp 随事件声明的外部工具；默认允许，命中该 Elwisp 的 `disabled_external_tools` 时拒绝。 |
| `targets` | 否 | Elwisp 期望投递目标，最终仍由 Elnis 裁决。 |
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

## 投递目标与安全边界

Elwisp 可以在 `targets` 中声明期望目标，但最终目标由 Elnis 裁决。

当前建议只使用：

```json
{
  "targets": {
    "platforms": ["cli"],
    "superadmins": true
  }
}
```

`platforms` 可以包含 `"all"`，表示请求投递到 Elnis 策略允许的全部平台。Elnis 仍会结合全局配置、Elwisp 配置和平台可用性计算最终目标。

安全约定：

- token 名只用于日志和审计，不等同于 Elwisp 身份。
- token 原文不写日志。
- Elwisp 不能直接发送平台消息。
- Elwisp 不能绕过 Tool Runtime 和 Security Policy 调用 ElBot 内部工具；`tool_list_names` 会经过 Elnis `allowed_tools` 裁决。
- Elwisp 声明的外部 `tools` 默认允许，并通过 ToolRun 以 `elwisp_<elwisp>_<tool>` 形式注入为模型可调用函数名；单个 Elwisp 可用 `disabled_external_tools` 禁用指定工具。
- 外部工具调用由 Elnis 对声明的 endpoint 发起 HTTP JSON POST；外部工具自身负责实际风险边界，Elnis 侧按 low 风险处理。
