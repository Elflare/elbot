# Elnis / ELwisp 监听架构规划

## 目标

Elnis（艾露妮斯）是 ElBot 内部的通用监听中台，负责接收外部监听消息、执行 ELvena 协议、做鉴权、规范化、去重、审计和分发。ELwisp（艾露维斯）是外部子监听器集群，负责观察具体世界，例如服务器状态、系统事件、RSS、Webhook、日志、脚本输出等。

核心目标是：ElBot 掌控最终执行与投递；ELwisp 只按协议投递事件；Elnis 不预设事件类型，也不把复杂业务规则写死在核心里。

## 非目标

- Elnis 不管理 ELwisp 生命周期，不负责启动、停止、健康检查外部监听器。
- Elnis 不作为普通聊天平台，不实现 PlatformAdapter 语义。
- Elnis 不复用 CronJob 作为事件存储；Cron 是调度任务，Elnis 是事件入口。
- 首期不做 Elnis 与 ELwisp 的多轮通信。
- 首期不做 CLI C/S 拆分；CLI 远程连接可作为后续本地服务 transport 规划。

## 总体架构

```text
ELwisp
  -> ELvena(JSON over HTTP)
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
- 工具预加载 `tool_list_names`。
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

Cron 可包装为 `Kind=cron`，Elnis 可包装为 `Kind=elwisp`。Session metadata 应记录各自来源，避免以后 `/sessions`、审计和排错混淆。

## ELvena v1 协议草案

首期协议使用 JSON 外壳，主体内容支持 ELyph 或自然语言文本。

```json
{
  "version": "elvena.v1",
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

字段说明：

| 字段 | 必填 | 说明 |
|---|---:|---|
| `version` | 是 | 协议版本，首期固定 `elvena.v1`。 |
| `elwisp.name` | 是 | ELwisp 唯一名称，是主要来源身份。 |
| `elwisp.tags` | 否 | 分类标签，用于日志、统计和目标策略。 |
| `source` | 是 | 具体事件源，例如服务名、RSS 名、脚本名。 |
| `id` | 是 | source 内唯一事件 ID。 |
| `created_at` | 否 | 外部事件发生时间，缺失时使用接收时间。 |
| `mode` | 是 | `record`、`direct` 或 `llm`。 |
| `title` | 否 | 事件标题，用于通知和 Session 标题。 |
| `format` | 否 | `elyph` 或 `text`，默认 `text`。 |
| `content` | 是 | 事件主体。LLM 模式推荐使用 ELyph `#task`。 |
| `model_slot` | 否 | 模型槽位，例如 `elwisp1`、`elwisp2`、`elwisp3`。 |
| `tool_list_names` | 否 | 请求预加载的工具名。实际可用性仍由 Elnis/Agent/Security 裁决。 |
| `targets` | 否 | ELwisp 期望投递目标。最终投递目标由 Elnis 配置裁决。 |
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

Elnis 配置多个 token，每个 token 有名称。token name 只用于审计和日志，不作为 ELwisp 身份。

来源与去重使用：

```text
elwisp.name + source + id
```

鉴权建议：

- 支持 `Authorization: Bearer <token>`。
- 可兼容 `X-Elnis-Token: <token>`。
- token 原文不写日志，只记录 token name。
- token 可从系统环境变量或配置目录 `.env` 读取。

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

直接通知，不调用 LLM。ELwisp 可以声明期望目标，但 Elnis 必须做最终裁决。

示例裁决规则：

- ELwisp 请求 `targets.platforms=["cli", "qqofficial"]`。
- Elnis 配置只允许该 token 或该 ELwisp 投递到 `cli`。
- 最终只发送到 `cli`。

direct 内容默认使用 `title + content` 组合为通知文本。未来可扩展 typed output，但首期只做文本，避免让外部事件绕开 Output 与安全边界。

### llm

转为后台 LLM Session 处理。HTTP 请求快速返回 `queued`，实际处理由后台 worker 执行。

处理步骤：

1. 事件状态置为 `queued`。
2. worker 将状态置为 `running`。
3. 根据 `model_slot` 选择模型，未配置则 fallback 到 `work`。
4. 生成 Elnis background prompt。
5. 预加载 `tool_list_names`。
6. 使用 Elnis sandbox 子目录运行工具。
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
- `need_report` 只有 `completed=true` 时有效。
- `report` 是需要发给目标平台的汇报；未完成时填写失败或阻塞原因。

## 投递目标控制

ELwisp 可以声明它希望发给谁，但 Elnis 必须拥有最终控制权。

建议 ELvena 请求中使用：

```json
{
  "targets": {
    "platforms": ["cli"],
    "superadmins": true,
    "scopes": ["group:123456"]
  }
}
```

首期建议只支持：

- `platforms`：期望平台列表。
- `superadmins`：是否发给目标平台超管。

暂不支持任意 user/group scope 投递，除非后续安全模型明确，否则容易让外部监听器变成任意消息发送器。

Elnis 配置应支持 allowlist：

```toml
[elnis.delivery]
default_platforms = ["cli"]
allow_superadmins = true

[elnis.elwisps.server-watchdog.delivery]
allowed_platforms = ["cli", "qqofficial"]
allow_superadmins = true
```

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

`/model` 后续应支持：

```text
/model --elwisp1 gpt-4o-mini
/model --elwisp2 openai/gpt-4.1
/model --elwisp3 gpt-4.1
```

更通用的 `/model --mode elwisp2 <model>` 可作为后续扩展。首期先做固定槽位，避免命令解析过度复杂。

## Sandbox 与工具风险

Elnis LLM 模式必须复用现有 Tool Runtime、Security Policy 和工具风险评估，不允许绕过安全层。

建议将当前 cron 专用 sandbox 标记泛化：

```go
type SandboxContext struct {
    Root           string
    Dir            string
    ArtifactDir    string
    Background     bool
    BackgroundKind string // cron / elwisp
}
```

Elnis 目录建议：

```text
data/sandbox/elnis/<elwisp-name>/
```

风险策略建议：

- critical 风险后台直接拒绝。
- shell 等文件/命令工具只能在 sandbox 约束下自动执行低风险或中风险操作。
- 高风险工具首期不自动确认，除非后续增加明确后台自动确认配置。
- 工具结果、拒绝原因和风险等级继续进入工具调用记录与审计。

## 存储设计

Elnis 事件需要持久化，避免重启后重复处理。

建议新增表 `elnis_events`：

| 字段 | 说明 |
|---|---|
| `id` | 内部 UUID。 |
| `event_key` | 规范化事件 key。 |
| `token_name` | 鉴权 token 名。 |
| `elwisp_name` | ELwisp 名。 |
| `source` | 来源。 |
| `source_id` | 外部事件 ID。 |
| `tags` | JSON 数组。 |
| `mode` | record/direct/llm。 |
| `model_slot` | 模型槽位。 |
| `content_hash` | 事件内容 hash。 |
| `requested_targets` | JSON。 |
| `resolved_targets` | JSON。 |
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

## 配置草案

```toml
[elnis]
enabled = true

[elnis.http]
addr = "127.0.0.1:32170"
max_body_bytes = 1048576
queue_size = 128
workers = 2

[elnis.tokens.home]
token_env = "ELNIS_HOME_TOKEN"

[elnis.tokens.server]
token_env = "ELNIS_SERVER_TOKEN"

[elnis.delivery]
default_platforms = ["cli"]
allow_superadmins = true

[elnis.elwisps.server-watchdog]
enabled = true
allowed_tokens = ["server"]

[elnis.elwisps.server-watchdog.delivery]
allowed_platforms = ["cli"]
allow_superadmins = true
```

配置原则：

- `enabled=false` 时不启动 Elnis runtime。
- `tokens` 只保存 token 名和读取方式，不保存 token 明文。
- `elwisps.<name>` 可按 ELwisp 名做 allowlist。
- 未配置的 ELwisp 默认拒绝还是默认允许，需要首期实现前明确；推荐默认拒绝，减少误接入。

## HTTP Runtime

首期 endpoint：

```text
POST /elvena/v1/events
GET  /healthz
```

实现要求：

- 限制 body 大小。
- 严格校验 content-type 或至少只接受 JSON object。
- 请求处理只完成接收与入队，不等待 LLM 完成。
- worker 数量可配置。
- app 退出时停止接收新请求，等待正在处理的 worker 结束或被 context 取消。

## 多轮通信预留

未来可支持 Elnis 与 ELwisp 的多轮通信，让 ELwisp 充当 user，Elnis/LLM 作为 assistant。

暂定方向：

- ELwisp 事件可携带 `conversation_id`。
- Elnis 为同一 conversation 维护后台 Session。
- Elnis response 或回调中返回 assistant 指令。
- ELwisp 根据指令继续采集数据或执行外部动作。

首期只在 `devdocs/tasks.md` 记录 TODO，不设计完整协议，避免过早锁死。

## 分阶段实施

### Phase 1：Ingress 与 direct/record

- 增加 Elnis config。
- 增加 ELvena v1 类型与校验。
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

- 支持 `elwisp1`、`elwisp2`、`elwisp3` 模型槽位。
- `/model` 支持 `--elwisp1`、`--elwisp2`、`--elwisp3`。
- `/models` 输出标记 Elnis 槽位当前模型。
- Elnis payload `model_slot` 使用对应槽位，未配置 fallback `work`。

### Phase 4：文档与运维能力

- 更新用户配置文档。
- 更新命令文档。
- 增加 Elnis 运行状态查看命令或日志查询能力。
- 增加事件列表/重试/禁用能力。
- 记录多轮通信、stdio/pipe transport 和 CLI C/S 的后续 TODO。
