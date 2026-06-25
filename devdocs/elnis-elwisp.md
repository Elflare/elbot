# Elnis / Elwisp 监听架构规划

## 目标

Elnis（艾露妮斯）是 ElBot 内部的通用监听枢纽，负责接收外部监听消息、执行 Elvena 协议、做鉴权、规范化、去重、审计和分发。Elwisp（艾露维丝）是外部子监听器集群，负责观察具体世界，例如服务器状态、系统事件、RSS、Webhook、日志、脚本输出等。

核心目标是：ElBot 掌控最终执行与投递；Elwisp 只按 Elvena（艾露维娜）协议投递事件；Elnis 不预设事件类型，也不把复杂业务规则写死在核心里。

## 非目标

- Elnis 不管理 Elwisp 生命周期，不负责启动、停止、健康检查外部监听器。
- Elnis 不作为普通聊天平台，不实现 PlatformAdapter 语义。
- Elnis 不复用 CronJob 作为事件存储；Cron 是调度任务，Elnis 是事件入口。
- 首期不做 Elnis 与 Elwisp 的多轮通信。
- 首期不做 CLI C/S 拆分；CLI 远程连接可作为后续本地服务 transport 规划。

## 总体架构

```text
Elwisp
  -> Elvena(JSON over HTTP)
  -> Elnis ingress runtime
       -> token 鉴权
       -> 协议校验与规范化
       -> 持久化去重
       -> 日志与审计
       -> mode 分发
            record -> 仅记录
            direct -> Output 通知
            llm    -> background LLM Session
```

Elnis 是 app 层 runtime，和平台 runtime、cron runtime 同级装配。它可以复用 Agent、Output、Security、Tool、Storage、Logging 等能力，但不把自身伪装成聊天平台。

## 与 Cron 的关系

现有 cron 中有多段逻辑并不应该独属于 cron，应抽象为通用 background 能力：

- 后台 Session 构造与运行。
- 后台 MessageContext 与 discard sender。
- 后台 sandbox 设置。
- 工具/Skill 预加载 `tool_list_names`。

- 后台 LLM 最终 JSON 结果格式要求、解析与重试。
- 后台任务结果报告与错误报告格式。

建议新增公共包或 Agent 内部公共层，例如 `internal/background`。Cron 只保留调度、错过 once 补投递、cron metadata 等调度语义；Elnis 只保留 ingress、事件去重、事件状态等监听语义。二者都调用 background runner。

推荐公共请求模型：

```go
type RunRequest struct {
    Kind          string // cron / elwisp
    Name          string
    Title         string
    Platform      string
    Actor         security.Actor
    ScopeID       string
    SessionID     string
    ModelProvider string
    Model         string
    Prompt        string
    ToolListNames []string
    SandboxSubdir string
    Metadata      map[string]string
}
```

Cron 可包装为 `Kind=cron`，Elnis 可包装为 `Kind=elnis`。Session metadata 应记录各自来源，避免以后 `/sessions`、审计和排错混淆。

background runner 的输入应同时携带：

- 当前会话可见的 ElBot 内置工具。
- Elwisp 注入的额外工具声明。
- 由 ToolRun 过滤后的最终工具视图。

这样 LLM 看到的是“当前上下文可用工具”，而不是“某个平台提供的工具”。

当前实现中已新增 `internal/background` 作为公共后台执行类型与 JSON 结果解析层；Agent 提供通用 `RunBackground`，cron 通过薄适配继续保持原行为，Elnis HTTP runtime 通过队列 worker 调用同一后台 runner。

## Elvena v3 协议

协议使用 JSON 外壳；`content` 支持 ELyph 或自然语言文本，`segments` 支持多模态 direct 投递，`calls` 支持调用平台 API。direct/record 请求中 `content`、`segments`、`calls` 至少提供一个；llm 模式仍必须提供 `content`。

```json
{
  "version": "elvena.v3",
  "elwisp": {
    "name": "server-watchdog",
    "tags": ["server", "prod"]
  },
  "source": "minecraft-main",
  "id": "2026-06-17T12:00:00Z:cpu-alert-001",
  "created_at": "2026-06-17T12:00:00Z",
  "mode": "llm",
  "title": "服务器 CPU 异常",
  "format": "elyph",
  "content": "#task investigate_cpu_alert - 检查服务器 CPU 异常并判断是否需要通知",
  "model_slot": "elwisp2",
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
      "risk": "low",
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

`calls` 只在 direct 模式执行。calls-only 请求可以不写 `content`/`segments`，Elnis 只调用平台 API，不额外发送消息：

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

字段说明：


| 字段 | 必填 | 说明 |
|---|---:|---|
| `version` | 是 | 协议版本，当前固定 `elvena.v3`。 |
| `elwisp.name` | 是 | Elwisp 唯一名称，是主要来源身份。 |
| `elwisp.tags` | 否 | 分类标签，用于日志、统计和目标策略。 |
| `source` | 是 | 具体事件源，例如服务名、RSS 名、脚本名。 |
| `id` | 是 | source 内唯一事件 ID。 |
| `created_at` | 否 | 外部事件发生时间，缺失时使用接收时间。 |
| `mode` | 是 | `record`、`direct` 或 `llm`。 |
| `title` | 否 | 事件标题，用于通知和 Session 标题。 |
| `format` | 否 | `elyph` 或 `text`，默认 `text`。 |
| `content` | 否 | 事件主体。LLM 模式必填，推荐使用 ELyph `#task`；direct/record 模式可为空，但 `content`、`segments`、`calls` 至少提供一个。 |
| `model_slot` | 否 | 模型槽位，例如 `elwisp1`、`elwisp2`、`elwisp3`。 |
| `tool_list_names` | 否 | 请求预加载的工具名或 Skill 名。普通工具注入 schema，Skill 注入后台任务 prompt 并自动注入对应 runner；实际可用性仍由 Elnis/ToolRun/Security 裁决；`discover_tool` 会被静默忽略，后台任务不注入发现入口。 |

| `tools` | 否 | Elwisp 额外声明的工具信息，包含名称、描述、Schema、调用端点或执行方式、风险与超时等。 |
| `targets` | 是 | Elwisp 期望投递目标数组。只写 `platform` 表示投递到该平台超级管理员；`type=private/group` 且带 `id` 表示指定私聊/群聊；`platform=all` 表示所有已启用平台超级管理员。最终投递目标由 Elnis 配置裁决。 |
| `calls` | 否 | Elvena v3 动作调用数组。`kind=raw` 透传平台原始 API，`kind=capability` 使用统一能力名；direct 请求只有 `calls` 且没有 `content`/`segments` 时只执行 API，不发送消息。 |
| `meta` | 否 | 原始补充数据，只做记录与 prompt 附加，不让核心理解事件类型。 |

HTTP 响应只表示接收状态，不等待 LLM 完成：

```json
{
  "accepted": true,
  "duplicate": false,
  "event_key": "server-watchdog/minecraft-main/2026-06-17T12:00:00Z:cpu-alert-001",
  "mode": "llm",
  "status": "queued"
}
```

## 鉴权与来源识别

Elnis 配置多个 token，每个 token 有名称。token name 只用于审计和日志，不作为 Elwisp 身份。

来源与去重使用：

```text
elwisp.name + source + id
```

鉴权建议：

- 支持 `Authorization: Bearer <token>`。
- 可兼容 `X-Elnis-Token: <token>`。
- token 原文不写日志，只记录 token name。
- `token_env` 支持列表，按顺序尝试多个环境变量名；先读系统环境变量，再读配置目录 `.env`。

## 模式语义

### record

仅记录事件，不调用 LLM，不发送平台通知。

处理步骤：

1. 鉴权。
2. 协议校验。
3. 去重。
4. 事件落库。
5. 写 Elnis 日志与全局审计。

### direct

直接通知，不调用 LLM。Elwisp 可以声明期望目标，但 Elnis 必须做最终裁决。

示例裁决规则：

- Elwisp 请求 `targets=[{"platform":"cli"},{"platform":"qqofficial"},{"platform":"telegram","type":"private","id":"123456789"}]`。
- Elnis 全局或单 Elwisp 配置显式禁用了 `qqofficial` 和该 Telegram 私聊。
- 最终只发送到 `cli`。

direct 内容默认使用 `title + content` 组合为通知文本。未来可扩展 typed output，但首期只做文本，避免让外部事件绕开 Output 与安全边界。

### llm

转为后台 LLM Session 处理。HTTP 请求快速返回 `queued`，实际处理由后台 worker 执行。

处理步骤：

1. 事件状态置为 `queued`。
2. worker 将状态置为 `running`。
3. 根据 `model_slot` 选择模型，未配置则 fallback 到 `work`。
4. 生成不暴露来源身份的 background prompt。
5. 将 `tool_list_names` 和 `tools` 交给后台 runner/ToolRun 做工具聚合、Skill prompt 注入、命名空间解析、可见性过滤和 schema 注入。

6. 使用 Elnis sandbox 子目录运行 ElBot 内置工具；Elwisp 工具由 ToolRun 路由到对应 Elwisp 调用端点或后续多轮通道。
7. 要求 LLM 最终输出严格 JSON。
8. 解析结果并更新事件状态。
9. 若需要报告，按 Elnis 裁决后的目标发送通知。

LLM 最终 JSON：

```json
{
  "completed": true,
  "need_report": true,
  "report": "处理结果"
}
```

语义：

- `completed` 表示后台任务是否完成。
- `need_report` 表示是否需要向目标平台汇报；成功、失败或阻塞都可以请求汇报。
- `report` 是需要发给目标平台的自然语言汇报，可填写处理结果、失败原因或阻塞原因。

## 投递目标控制

Elwisp 可以声明它希望发给谁，但 Elnis 必须拥有最终控制权。

建议 Elvena 请求中使用：

```json
{
  "targets": [
    {"platform": "cli"},
    {"platform": "telegram", "type": "private", "id": "123456789"},
    {"platform": "qqonebot", "type": "group", "id": "987654321"},
    {"platform": "all"}
  ]
}
```

首期支持扁平 target 数组：

- `{ "platform": "telegram" }`：投递到指定平台超级管理员。
- `{ "platform": "telegram", "type": "private", "id": "123456789" }`：投递到指定平台私聊。
- `{ "platform": "qqonebot", "type": "group", "id": "987654321" }`：投递到指定平台群聊。
- `{ "platform": "all" }`：投递到所有已启用平台超级管理员，不能同时写 `type` 或 `id`。

Elnis 默认允许投递；只有命中全局或单 Elwisp 的 disabled target 时才阻止。配置中的 platform-only disabled target 表示禁用整个平台所有投递。外部 Elwisp 不能绕过 Elnis 配置与平台 runtime 可用性裁决。

Elnis 配置独立放在 `elnis.toml`，支持按 Elwisp 设置额外策略，且 `elwisps` 是可选项：

```toml
allowed_tools = ["web_search", "web_extract"]

[delivery_disabled]
targets = [
  # { platform = "telegram" },
  # { platform = "telegram", type = "private", id = "123456789" },
  # { platform = "qqonebot", type = "group", id = "987654321" },
]

[elwisps.server-watchdog]
allowed_tokens = ["server"]
allowed_tools = ["shell", "web_search"]
disabled_external_tools = ["danger_tool"]
disabled_targets = [
  # { platform = "qqonebot", type = "group", id = "987654321" },
]

[elwisps.spike-checker]
enabled = false
```

说明：

- Elwisp 默认启用；未配置单个 Elwisp 时，也会接收通过 Elnis token 认证的事件。
- 单个 Elwisp 配置只用于限制 token、覆盖投递策略或显式禁用。
- 只有显式 `enabled=false` 才会禁用某个 Elwisp。

最终目标计算：

```text
requested targets
  -> token policy
  -> elwisp policy
  -> global delivery policy
  -> enabled platform/runtime availability
  -> output.Target
```

## 模型槽位

Elnis 复用 `state.toml` 的 `mode_models` 扩展 key：

```toml
[mode_models.elwisp1]
provider = "openai"
model = "gpt-4o-mini"

[mode_models.elwisp2]
provider = "openai"
model = "gpt-4.1"

[mode_models.elwisp3]
provider = "openai"
model = "gpt-4.1"
```

`/model` 已支持：

```text
/model --elwisp1 gpt-4o-mini
/model --elwisp2 openai/gpt-4.1
/model --elwisp3 gpt-4.1
```

Elnis LLM payload 的 `model_slot` 可指定 `elwisp1`、`elwisp2` 或 `elwisp3`；未指定或对应槽位未配置时 fallback 到 `work`。

更通用的 `/model --mode elwisp2 <model>` 可作为后续扩展。首期先做固定槽位，避免命令解析过度复杂。

## ToolRun 中间层

Elnis 不应直接把 Elwisp 工具和 ElBot 内置工具混在同一个扁平列表里，也不应让 LLM 感知工具来自哪里。LLM 只接触被注入到当前上下文中的工具描述和少量运行态信息；它不知道这些工具来自 CLI、消息平台、cron、Elwisp 还是其他 transport。

已新增 `internal/toolrun` 作为 ToolRun 中间层地基，位于 LLM/Agent 与具体工具执行器之间。ToolRun 不替代 Tool Runtime；它负责当前 session 的工具视图、缓存、解析、权限风险、确认、执行编排、记录和失效提示。职责包括：

- 聚合当前上下文可见的工具。
- 区分 ElBot 内置工具命名空间与 Elwisp 工具命名空间。
- 处理工具 schema 注入、去重、冲突和可见性过滤。
- 根据当前 session、actor、source、background kind 和 tool metadata 路由到对应执行器。
- 统一做权限、风险、超时、审计和结果回灌。

推荐的命名空间策略：

- 无前缀：ElBot native 工具，包括 builtin、skill、隐藏包装工具和未来 MCP 等所有非 Elwisp 工具。
- `elwisp.<elwisp-name>.<tool-name>`：Elwisp 通过 Elvena 协议显式注入的工具。
- Elwisp 工具不能通过 `discover_tool` 发现，必须由协议 payload 注入并缓存到对应 session。
- 后续若有平台私有工具，可再独立 namespace，但不要和业务工具共享一个无前缀平面。

工具路由原则：

- LLM 只做“要调用哪个工具”的决策，不关心工具来源。
- ToolRun 根据当前对话来源、会话模式、Actor、security policy 和工具声明选择实际执行器。
- 若工具名冲突，优先使用当前上下文显式注入的命名空间解析结果，不允许静默串台。
- Elwisp 工具和 ElBot 工具可以同时注入，但必须经过可见性和风险过滤后再进入 prompt。

实现上，ToolRun 可以把多来源工具汇总成一份统一的 LLM tool view，但这份 view 只是当前会话上下文的投影，不是全局工具真相表。

Elwisp 工具声明首期建议字段：

| 字段 | 必填 | 说明 |
|---|---:|---|
| `name` | 是 | Elwisp 内工具名，进入 ToolRun 后会绑定到 `elwisp.<elwisp-name>.<name>`。 |
| `description` | 是 | 给 LLM 看的工具用途说明，不包含 Elwisp 身份叙述。 |
| `schema` | 是 | JSON Schema 参数结构，必须通过 Elnis 校验后才能注入。 |
| `risk` | 否 | Elwisp 声明的风险等级，只能作为下限；Elnis/ToolRun 可提升风险。 |
| `timeout_seconds` | 否 | 单次调用超时。 |
| `endpoint` | 否 | HTTP 调用端点；后续也可支持 stdio/pipe/多轮通道。 |

安全边界：

- Elwisp提供的工具，应是Elwisp所在平台的工具（如Elwisp所在计算机的终端）
- Elwisp 提供的 schema 不等于可信工具；ToolRun 必须按 token、Elwisp policy、risk policy 和当前 background context 决定是否注入。
- Elwisp 工具执行结果必须走统一 tool result 回灌，不允许直接写 assistant 消息。
- Elwisp 工具不能绕过 Output Manager 直接发平台消息；若需要通知，返回结构化结果，由 Elnis/Agent 决定是否发送。
- Elwisp工具的多轮调用可能需要配合多轮通信
- ToolRun 审计必须记录逻辑工具名、实际执行命名空间、Elwisp 名、source、event id、风险、耗时和错误。

ToolRun 不是 Tool Runtime 的重命名，也不是 Prompt Builder 的一部分：

- Tool Runtime 负责工具注册、权限、风险评估和执行器封装。
- ToolRun 负责把当前对话可见工具整理成 LLM 可用视图，并把 tool call 路由到正确的执行器。
- Prompt Builder 只消费 ToolRun 给出的最终工具视图，不自己拼来源规则。

Elnis LLM 模式必须复用现有 Tool Runtime、Security Policy 和工具风险评估，不允许绕过安全层。

建议将当前 cron 专用 sandbox 标记泛化：

```go
type SandboxContext struct {
    Root           string
    Dir            string
    Background     bool
    BackgroundKind string // cron / elwisp
}

```

Elnis 目录建议：

```text
data/sandbox/elnis/<elwisp-name>/
```

风险策略建议：

- native 工具继续使用 ElBot 的 Security Policy、风险评估和高风险确认；cron/Elnis 后台 sandbox 中的 shell 非 critical 自动确认，critical 会提示改用相对路径并限制在 sandbox 内。
- Elwisp 工具在 Elnis 侧按无人值守外部工具处理，默认视为 low，不触发 ElBot 高风险确认；执行影响由 Elwisp 所在环境自行负责。
- 恢复历史 Elwisp session 时，即使原 Elwisp 工具已不可用，ToolRun 仍保留缓存 schema；若 LLM 再调用，应返回工具已失效或当前不可用提示。
- 工具结果、拒绝原因、风险等级、source 和 canonical name 继续进入工具调用记录与审计。

## 存储设计

Elnis 事件需要持久化，避免重启后重复处理。

建议新增表 `elnis_events`：

| 字段 | 说明 |
|---|---|
| `id` | 内部 UUID。 |
| `event_key` | 规范化事件 key。 |
| `token_name` | 鉴权 token 名。 |
| `elwisp_name` | Elwisp 名。 |
| `source` | 来源。 |
| `source_id` | 外部事件 ID。 |
| `tags` | JSON 数组。 |
| `mode` | record/direct/llm。 |
| `model_slot` | 模型槽位。 |
| `content_hash` | 事件内容 hash。 |
| `requested_targets` | JSON。 |
| `resolved_targets` | JSON。 |
| `tool_declarations` | Elwisp 随事件声明的工具 JSON，供重放、审计和失败排查。 |
| `tool_declarations_hash` | 工具声明 hash，用于重复事件和 schema 变化排查。 |
| `status` | accepted/queued/running/completed/failed/duplicate。 |
| `session_id` | LLM Session ID。 |
| `result` | LLM JSON result。 |
| `error` | 错误文本。 |
| `received_at` | 接收时间。 |
| `created_at` | 外部事件时间。 |
| `updated_at` | 更新时间。 |

唯一索引：

```sql
unique(elwisp_name, source, source_id)
```

重复处理：

- key 相同且 hash 相同：返回 duplicate，不重复分发。
- key 相同但 hash 不同：返回 duplicate，并记录 warning，避免外部复用 ID 覆盖历史。

## 日志与审计

建议给 Elnis 单独日志文件：

```text
elnis-YYYY-MM-DD.log
```

固定日志字段：

- `token_name`
- `elwisp_name`
- `source`
- `source_id`
- `tags`
- `mode`
- `event_key`
- `status`
- `session_id`
- `resolved_targets`
- `error`

全局审计事件前缀：

```text
elnis.accepted
elnis.duplicate
elnis.recorded
elnis.direct_started
elnis.direct_completed
elnis.direct_failed
elnis.llm_queued
elnis.llm_started
elnis.llm_completed
elnis.llm_failed
```

## 配置

```toml
enabled = true
allowed_tools = ["web_search", "web_extract"]

[http]
addr = "127.0.0.1:32170"
max_body_bytes = 1048576
queue_size = 128
workers = 2

[tokens.home]
token_env = ["ELNIS_HOME_TOKEN", "ELNIS_HOME_TOKEN_ALT"]

[tokens.server]
token_env = ["ELNIS_SERVER_TOKEN"]

[delivery_disabled]
targets = [
  # { platform = "telegram" },
  # { platform = "telegram", type = "private", id = "123456789" },
  # { platform = "qqonebot", type = "group", id = "987654321" },
]

[elwisps.server-watchdog]
allowed_tokens = ["server"]
allowed_tools = ["shell", "web_search"]
disabled_external_tools = ["danger_tool"]
disabled_targets = [
  # { platform = "qqonebot", type = "group", id = "987654321" },
]

[elwisps.spike-checker]
enabled = false
```

配置原则：

- `enabled=false` 时不启动 Elnis runtime。
- `tokens` 只保存 token 名和读取方式，不保存 token 明文。
- 顶层 `allowed_tools` 是 ElBot 内部工具默认白名单；单 Elwisp `allowed_tools` 存在时覆盖默认值。
- Elwisp 外部工具默认允许；单 Elwisp `disabled_external_tools` 可禁用指定外部工具。
- Elnis 投递默认允许；全局 `[delivery_disabled].targets` 和单 Elwisp `disabled_targets` 用于显式禁止平台、私聊或群聊，配置中的 platform-only 表示禁用整个平台所有投递。
- `elwisps.<name>` 可按 Elwisp 名限制 token、增加投递禁用项或显式禁用。
- Elwisp 默认启用；只有显式 `enabled=false` 才会禁用对应 Elwisp。

## HTTP Runtime

首期 endpoint：

```text
POST /elvena/v3/events
GET  /healthz
```

实现要求：

- 限制 body 大小。
- 严格校验 content-type 或至少只接受 JSON object。
- 请求处理只完成接收与入队，不等待 LLM 完成。
- worker 数量可配置。
- app 退出时停止接收新请求，等待正在处理的 worker 结束或被 context 取消。

## 多轮通信预留

未来可支持 Elnis 与 Elwisp 的多轮通信，让 Elwisp 充当 user，Elnis/LLM 作为 assistant。

暂定方向：

- Elwisp 事件可携带 `conversation_id`。
- Elnis 为同一 conversation 维护后台 Session。
- Elnis response 或回调中返回 assistant 指令。
- Elwisp 根据指令继续采集数据或执行外部动作。

首期只在 `devdocs/tasks.md` 记录 TODO，不设计完整协议，避免过早锁死。

## 分阶段实施

### Phase 1：Ingress 与 direct/record

- 增加 Elnis config。
- 增加 Elvena v3 类型与校验。
- 增加 HTTP server。
- 增加 token 鉴权。
- 增加 Elnis 事件表与 repository。
- 增加持久化去重。
- 增加 Elnis 独立日志。
- 实现 `record`。
- 实现 `direct` 文本通知和目标裁决。

### Phase 2：Background 抽象与 LLM 模式

- 抽象 cron/Elnis 共用 background runner。
- 将 cron LLM 执行迁移到 background runner，保持行为不变。
- 实现 Elnis LLM prompt。
- 实现 Elnis LLM JSON result 解析与重试。
- 实现 Elnis worker 状态流转。
- 复用工具预加载、Security 和 sandbox。

### Phase 3：模型槽位与命令

- 已支持 `elwisp1`、`elwisp2`、`elwisp3` 模型槽位。
- `/model` 支持 `--elwisp1`、`--elwisp2`、`--elwisp3`。
- `/models` 输出标记 Elnis 槽位当前模型。
- Elnis payload `model_slot` 使用对应槽位；未指定或对应槽位未配置时 fallback 到 `work`。

### Phase 4：文档与运维能力

- 更新用户配置文档。
- 更新命令文档。
- 增加 Elnis 运行状态查看命令或日志查询能力。
- 增加事件列表/重试/禁用能力。
- 记录多轮通信、stdio/pipe transport 和 CLI C/S 的后续 TODO。
