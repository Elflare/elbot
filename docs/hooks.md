# Hook

Hook 在 ElBot 的关键边界运行：它可以观察或修改事件、追加输出意图，或把事件交给外部进程。Hook 不直接发送平台消息；所有 outputs 都由 Output Manager 落到平台。

本版本统一使用 `hook.v2`。旧 `hook.v1` 的 `init/output/done` 帧已移除，现有外部 exec 脚本必须迁移。

## 配置位置

配置目录的 `plugins/hooks.toml` 是规则入口。它可直接包含 `[[rules]]`，也可引用插件目录：

```toml
[[plugins]]
name = "weather"
# 默认读取 plugins/weather/hook.toml
```

插件源码、`hook.toml` 和它自行维护的状态文件都放在 `plugins/<id>/`。ElBot 额外创建 `plugins/_shared/`，供所有 Hook 共享文件；它不是插件目录，也不会被扫描。

## 规则 Hook

普通规则使用 `[[rules]]`。规则先按 `on`、条件和角色匹配，再顺序执行 actions。

```toml
[[rules]]
name = "notify_qqonebot_connected"
on = "platform.connected"
if = "platform.name"
op = "fullmatch"
value = "qqonebot"
action = "send"
kind = "text"
text = "ElBot 已连接 QQ OneBot。"
target.superadmins = true
```

常用 action：

| action | 作用 |
| --- | --- |
| `prepend` / `append` / `replace` / `delete` | 修改当前事件允许编辑的文本字段。 |
| `send` | 追加 text/image/file/emoticon/at/reply 输出意图。 |
| `tool` | 以当前 Actor 的普通 Hook 权限调用低风险工具。 |
| `exec` | 启动一次性外部 Hook，并按 hook.v2 处理。 |

`platform.message.received` 的 `require_wakeup = false` 可观察未唤起的群消息；它不会让主 LLM 自动处理这些消息。规则设置 `consume = true` 时，发送 outputs 后不再进入命令或 LLM。

## hook.v2 一次性 exec

`exec` 的 `command` 按空白拆分后直接执行，不隐式经过 shell。需要 shell 语法时显式使用 `sh -c`、`bash -lc` 或平台对应解释器。

```toml
actions = [
  { name = "extract", type = "exec", command = "uv run extract.py", field = "llm.text", timeout_seconds = 30 },
]
```

Host 依次写入两个 request：`system.init` 和 `event.handle`。脚本以相同 `host:*` ID 写 response。`event.handle` 的成功 result 使用：

```json
{
  "status": "completed",
  "result": "optional template result",
  "message": {"text": "optional replacement text"},
  "outputs": [{"kind": "text", "text": "hello"}],
  "consume": false,
  "stop_propagation": false
}
```

`outputs` 是输出意图数组；相对 `path` 相对插件目录解析。大媒体请写入插件目录或 `_shared/` 后返回路径或 URL，不要放进 JSON Pipe。stderr 只用于日志和失败诊断，stdout 只能输出协议帧。

Hook 主动请求 Host 能力时使用：

```json
{"type":"request","id":"plugin:send-1","method":"platform.call","params":{}}
```

Host 回写 `response`。Host 发起的 ID 必须是 `host:*`，Hook 发起的 ID 必须是 `plugin:*`；response 复用原 request ID。

## 持久 Hook

持久 Hook 仍是 Hook，不存在独立的插件系统或 `/plugins` 命令。它由插件自己的 `hook.toml` 声明：

```toml
[plugin]
name = "weather"
description = "有状态天气助手"

[plugin.runtime]
stateful = true
command = "uv run weather_hook.py"
cwd = "."
startup_timeout_seconds = 10
shutdown_timeout_seconds = 5
event_timeout_seconds = 30
max_wait_seconds = 900

[plugin.runtime.restart]
strategy = "on_failure" # never / on_failure / always
initial_delay_seconds = 1
max_delay_seconds = 30

[plugin.runtime.tools]
allow = ["web_search"]
background_allow = []

[[rules]]
name = "weather_entry"
on = "platform.message.received"
require_wakeup = false
if = "message.intent_text"
op = "contains"
value = "天气"
# 持久 Hook 的 rules 仅是 event.handle 触发器，不设置 action/actions。
```

所有持久运行字段都必须显式填写。ElBot 启动和 `/hooks reload` 后会自动启动已启用的持久 Hook；其状态为 `starting`、`ready`、`running`、`degraded`、`stopping`、`stopped` 或 `failed`。`system.shutdown` 会先请求优雅退出，超过关闭超时才终止进程。

持久 Hook 在 `system.init` 后可接收多个 `event.handle`。同一 `platform + scope + actor` 串行，不同路由可以并行。Hook 的 response 可以返回：

```json
{
  "status": "waiting",
  "conversation_id": "weather-42",
  "expires_at": "2026-07-10T12:00:00Z"
}
```

等待路由只捕获发起者在同一 scope 的后续消息；群聊不会捕获其他成员，也不要求发起者重新 at。它在常规 `platform.message.received` Hook 完成且未 consume 后、命令和主 LLM 前执行。`/cancel` 只取消当前路由的这次执行或等待会话，进程和内存状态继续保留；使用 `/hooks stop <id>` 才停止进程。


## 工具与共享状态

持久 Hook 管理自己的 LLM loop 和业务状态。ElBot 只在 `system.init` 下发 `[plugin.runtime.tools].allow` 中的 schema，并处理 Hook 的 `tool.call` request。

普通 `tool.call` 必须携带 Host 下发的 `tool_context`。Host 校验 allowlist、前后台可用性、调用次数、超时、取消和上下文归属；它不把 Hook 调用写入 ElBot Agent Session，也不执行用户风险确认。工具结果返回 `content`、`segments`、`warnings` 和 Output Manager 的发送回执。

后台调用仅能使用 `background_allow`，主体是 `hook:<id>`；没有有效 origin 时必须显式提供输出 target。若携带 Host 签发且未过期的 origin，平台、scope 和用户信息仅用于正确的上下文工具、发送路由和最小边界审计。

除 `_shared/` 文件目录外，所有持久 Hook 共享一个进程内 JSON KV：

| request method | 说明 |
| --- | --- |
| `shared.get` | 按 key 读取值。 |
| `shared.set` | 写入 JSON 值。 |
| `shared.delete` | 删除 key。 |
| `shared.list` | 按 prefix 列出 key。 |
| `shared.compare_and_swap` | 原子条件写入。 |

key 必须为 `<namespace>/<key>`。共享内存跨 Hook 重启和 `/hooks reload` 保留，在 ElBot 重启后清空；需要持久化时由 Hook 写入自己的目录或 `_shared/`。

## 管理命令

`/hooks` 是唯一管理入口（超级管理员命令）：

```text
/hooks
/hooks <name-or-id>
/hooks start <id>
/hooks stop <id>
/hooks restart <id>
/hooks reload
```

`reload` 会重新读取规则与持久运行配置，并停止、替换或启动对应的持久 Hook。配置变更不做动态 schema patch，重载会重启受影响进程。

## 模板与字段

普通规则的文本和 `exec.command` 支持 `{{...}}` 模板，例如 `{{platform.name}}`、`{{actor.id}}`、`{{message.text}}`、`{{llm.text}}`、`{{tool.result}}` 和 `{{match.regex.0.group.1}}`。前序 tool/exec action 的结果可用 `{{actions.<name>.result}}` 与 `{{actions.<name>.error}}`。

可匹配字段、Hook 点和可编辑字段沿用现有规则语义；配置错误会在 `/hooks reload` 报告，并跳过问题插件，不影响其他已注册 Hook。
