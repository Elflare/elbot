# Hook

Hook Layer 用于在关键流程前后扩展行为，例如：

- Agent 输入处理。
- LLM 请求准备。
- LLM 响应处理。
- 平台发送前后。
- 平台连接事件。

Hook 可以修改消息、追加输出意图、调用低风险工具或注入常驻记忆。规则 Hook 还可以执行本地脚本。

重要约定：Hook 不替代 Security Layer，安全判定仍以 Security Layer 为准。Hook 和插件不直接发平台消息，应返回输出意图，由 Agent 统一交给 Output Manager 发送。

## 内置 Hook

ElBot 随程序注册两类内置 Hook：

| 类型 | 说明 |
| --- | --- |
| 规则 Hook | 从 `plugins/hooks.toml` 加载声明式规则，支持条件匹配、文本操作、输出发送、工具调用和脚本执行。 |
| 常驻记忆 Hook | 每轮注入当前 platform + actor 的常驻记忆和临时用户名。 |

表情 Hook 已从内嵌插件改为规则 Hook 示例，见本文档[表情提取示例](#表情提取示例)。

## 规则 Hook 配置

规则 Hook 配置固定在配置目录的 `plugins/hooks.toml`。插件专属配置放在 `plugins/<plugin-name>.toml`。

### 规则结构

```toml
[[rules]]
name = "stable_debug_name"          # 必填，用于日志和审计
on = "hook.point"                   # 必填，Hook 点
enabled = true                      # 可选，默认 true
priority = 1000                     # 可选，值越小越先执行
```

### 条件匹配

单条件：

```toml
if = "message.text"
op = "contains"
value = "hello"
```

无条件：

```toml
always = true
```

多条件（AND）：

```toml
match = [
  { field = "platform.name", op = "fullmatch", value = "qqonebot" },
  { field = "message.text", op = "contains", value = "猫" },
]
```

`always` 不能与 `if/op/value` 或 `match` 组合使用；`if/op/value` 不能与 `match` 组合使用。

### 匹配操作

| op | 说明 |
| --- | --- |
| `always` | 无条件匹配，不能设 field 或 value |
| `exists` | 字段非空 |
| `contains` | 字段包含 value |
| `fullmatch` | 字段完全等于 value |
| `startswith` | 字段以 value 开头 |
| `endswith` | 字段以 value 结尾 |
| `regex` | 正则匹配，捕获组可通过模板变量引用 |

### 可匹配字段

```
platform.name / scope_id / user_id / conversation_id / message_id / reply_to_message_id
actor.id / user_id / role / group_role / display_name
session.id / mode / status
request.id / kind / phase
message.text / content_text / role
llm.text / raw_text / latest_user_text / latest_user_content_text / provider / model
tool.name / arguments / result / risk
```

### Hook 点

```
platform.connected
platform.message.received
agent.input.prepared
llm.turn.prepared
llm.request.prepared
llm.response.received
tool.call.prepared
tool.call.completed
agent.output.prepared
agent.turn.output.prepared
platform.message.sent
error.occurred
```

### 可编辑字段

不同 Hook 点允许编辑的字段不同：

| Hook 点 | 可编辑字段 |
| --- | --- |
| `platform.message.received` / `agent.input.prepared` | `message.text` |
| `llm.turn.prepared` / `llm.request.prepared` | `llm.latest_user_text` |
| `llm.response.received` | `llm.text` |
| `tool.call.prepared` | `tool.arguments` |
| `tool.call.completed` | `tool.result` |
| `agent.output.prepared` / `agent.turn.output.prepared` / `platform.message.sent` | assistant `message.text` |

`llm.raw_text` 可以用于条件匹配，但不能被编辑。

### Action 类型

每条规则可以写单个 `action` 或多个 `actions`（按顺序执行）。

#### 文本操作

```toml
# 单 action
action = "replace"
field = "message.text"
pattern = "猫"
replace = "狗"
all = true                 # 可选，默认只替换第一处

# 多 actions
actions = [
  { type = "replace", field = "message.text", pattern = "猫", replace = "狗", all = true },
  { type = "append", field = "message.text", text = "!" },
]
```

文本操作类型：`prepend`、`append`、`replace`、`delete`。`delete` 等同于 `replace` 为空字符串。

文本操作会保留消息中的图片、文件等多模态 segment 位置，只修改 text segment。

#### send

`send` action 产生输出意图，由 Output Manager 统一发送到平台。

单输出（向后兼容）：

```toml
action = "send"
kind = "text"              # text/image/file/emoticon/at，默认 text
text = "检测到关键词"
timing = "after_assistant" # 可选，默认 immediate
```

多段输出（segments）：

```toml
actions = [
  { type = "send", timing = "after_assistant", segments = [
    { kind = "text", text = "检测到关键词" },
    { kind = "image", path = "alert.png" },
    { kind = "image", url = "https://example.com/chart.png", name = "chart.png" },
    { kind = "image", base64 = "iVBORw0KGgo..." },
    { kind = "emoticon", name = "微笑", path = "emoticons/微笑/01.png" },
    { kind = "file", path = "report.pdf", name = "报告.pdf" },
  ] },
]
```

segment 格式与 [Elvena 协议](elnis-usage.md#segments多模态消息段)统一，额外支持本地 `path`、`base64` 和 `emoticon` 类型：

| 字段 | 说明 |
| --- | --- |
| `kind` | `text` / `image` / `file` / `emoticon`，默认 `text` |
| `text` | 文本内容（text 类型必填，其他类型可选作为附加文本） |
| `url` | HTTP/HTTPS URL（image/file） |
| `path` | 本地文件路径（image/file/emoticon）；普通路径按平台默认方式处理，`base64://`、`file://`、`http://`、`https://` 前缀表示调用方已指定媒体源，会按平台能力直接处理 |
| `base64` | base64 编码数据（image/file） |
| `name` | 文件名或表情名 |
| `mime_type` | MIME 类型提示 |

`target` 和 `timing` 从 action 继承到所有 segment。

#### tool

调用已注册工具，结果存到 `{{actions.<name>.result}}` 供后续 action 使用。

```toml
actions = [
  { name = "search", type = "tool", tool = "web_search", arguments = '{"query":"ElBot"}' },
  { type = "append", field = "llm.latest_user_text", text = "\n\nHook 工具结果：{{actions.search.result}}" },
]
```

工具调用受 Security Policy 约束：风险等级必须在当前 Actor 允许范围内，需要交互确认的高风险工具会被拒绝。

#### exec

执行本地脚本。脚本默认以 `plugins/` 为工作目录，`cwd` 可覆盖（绝对路径直接使用，相对路径仍基于 `plugins/`）。

`command` 会按空白拆分为可执行程序和参数后直接执行，不会隐式套 `sh -c`。例如 `uv run script.py` 会直接执行 `uv`，`bash ./script.sh` 会直接执行 `bash`；需要管道、重定向、`&&` 等 shell 语法时，请显式写 `bash -lc "..."`、`sh -c "..."` 或平台对应解释器。

默认 stdin 是包含完整 event 和 match 上下文的 JSON。也可以用 `stdin` 字段自定义 stdin 内容（支持模板渲染）。

`stdout` 模式：

| 模式 | 说明 |
| --- | --- |
| `capture` | 默认值，把 stdout 保存到 `{{actions.<name>.result}}`，供后续 action 使用 |
| `send` | 把 stdout 作为文本输出发送 |
| `outputs` | 把 stdout 解析为 JSON，提取 `outputs` 数组和可选 `text` |
| `elvena` | 把 stdout 解析为 Elvena JSON 请求，经内部 Elvena Bus 交给 Elnis |
| `ignore` | 忽略 stdout |

```toml
actions = [
  { type = "exec", command = "uv run script.py", stdout = "capture", timeout_seconds = 30 },
]
```

### exec outputs 模式

`stdout = "outputs"` 时，脚本 stdout 必须是 JSON：

```json
{
  "outputs": [
    {"kind": "emoticon", "name": "微笑", "path": "emoticons/微笑/01.png"},
    {"kind": "text", "text": "已处理"}
  ],
  "text": "清理后的文本"
}
```

- `outputs`：每项格式与 send segments 一致，转换为输出意图发送到平台。
- `text`：可选。当 action 设了 `field` 时，`text` 会整体覆写该字段（走可编辑字段校验）；不设 `field` 或 `text` 为空时不修改原文。

```toml
[[rules]]
name = "emoticon_extract"
on = "llm.response.received"
if = "llm.text"
op = "regex"
value = "\\[\\[[^\\[\\]]+\\]\\]"
actions = [
  { type = "exec", command = "uv run emoticon_extract.py", stdout = "outputs", field = "llm.text", timing = "after_assistant" },
]
```

### 模板变量

文本字段和 exec command/stdin 支持 `{{...}}` 模板渲染：

```
{{platform.name}}          {{platform.scope_id}}      {{platform.user_id}}
{{platform.message_id}}    {{platform.reply_to_message_id}}
{{actor.id}}               {{actor.user_id}}          {{actor.role}}
{{message.text}}           {{message.content_text}}
{{llm.text}}               {{llm.raw_text}}           {{llm.latest_user_text}}
{{tool.arguments}}         {{tool.result}}
{{actions.<name>.result}}  {{actions.<name>.error}}
```

regex 匹配的捕获组可通过 `{{match.regex.0.group.1}}` 或命名组 `{{match.regex.0.<name>}}` 引用。

### 角色分区

规则可用 `roles`、`actor_roles`、`group_roles` 做权限分区：

- `superadmin` / `user`：ElBot 内部安全角色。
- `owner` / `admin` / `member`：平台群身份，由平台适配器映射。

`roles` 同时匹配内部角色和群身份；`actor_roles` 只匹配内部角色；`group_roles` 只匹配群身份。

```toml
[[rules]]
name = "admin_only_rule"
on = "platform.message.received"
roles = ["admin"]
always = true
action = "send"
text = "仅管理员可见"
```

### 控制字段

```toml
consume = true              # 阻止后续 slash 命令和 LLM 处理
stop_propagation = true      # 阻止同一 Hook 点后续规则继续执行
```

`consume = true` 通常用于 `platform.message.received` Hook：发送输出后阻止后续命令和 LLM 处理，让 Hook 完全接管消息。

## Hook exec 投递 Elvena

规则 Hook 的 `exec` action 可以设置 `stdout = "elvena"`。脚本 stdout 必须是完整 Elvena JSON 请求，ElBot 会通过内部 Elvena Bus 交给 Elnis，而不是重新走 HTTP token 鉴权。

```toml
[[rules]]
name = "server-script"
on = "platform.message.received"
roles = ["superadmin"]
always = true

[[rules.actions]]
type = "exec"
name = "notify"
command = "uv run scripts/build_elvena.py"
stdout = "elvena"
```

脚本可以输出 `mode = "direct"` 的通知，也可以输出 `mode = "llm"` 的后台任务；后续处理仍由 Elnis 的目标裁决、日志、去重和后台 runner 负责。

Elvena 基于 JSON，内容必须使用 UTF-8 编码。完整 Elvena 请求字段见 [Elnis 配置与使用](elnis-usage.md#elvena-请求示例)。

## 撤回引用消息示例

以下示例用于超级管理员回复某条平台消息并发送“撤回这条”时，通过 Elvena v3 `calls` 调用平台 API 撤回被引用的消息。

前提：平台适配器需要支持原始 API 调用，且本次消息必须带引用/回复关系。Hook 里可通过 `platform.reply_to_message_id` 读取被引用消息的平台消息 ID，通过 `platform.message_id` 读取当前触发 Hook 的消息 ID。

Telegram 使用 Bot API `deleteMessage`，机器人必须有删除目标消息的权限，且目标消息不能超过平台允许的删除时限。

### hooks.toml

```toml
[[rules]]
name = "recall_quoted_message"
on = "platform.message.received"
roles = ["superadmin"]
match = [
  { field = "message.text", op = "fullmatch", value = "撤回这条" },
  { field = "platform.reply_to_message_id", op = "exists" },
]

consume = true

[[rules.actions]]
type = "exec"
name = "recall"
command = "uv run recall_quoted_message.py"
stdout = "elvena"
timeout_seconds = 10
```

### recall_quoted_message.py

脚本从 stdin 读取 Hook event JSON，按当前平台生成 Elvena v3 direct calls-only 请求。Elnis 只执行 `calls`，请求没有 `content` 或 `segments` 时不会额外发送确认消息。

```python
#!/usr/bin/env python3
import json
import sys
import time


def numeric_scope_id(scope_id):
    if ":" not in scope_id:
        return scope_id
    return scope_id.split(":", 1)[1]


def main():
    data = json.load(sys.stdin)
    event = data.get("event", {})
    platform = event.get("platform", {})
    platform_name = platform.get("name", "")
    scope_id = platform.get("scope_id", "")
    reply_id = platform.get("reply_to_message_id", "")

    if not reply_id:
        raise SystemExit("missing platform.reply_to_message_id")

    target = {"platform": platform_name}
    scope_target_id = numeric_scope_id(scope_id)
    if scope_id.startswith("group:") or scope_id.startswith("supergroup:"):
        target.update({"type": "group", "id": scope_target_id})
    elif scope_id.startswith("private:"):
        target.update({"type": "private", "id": scope_target_id})

    call = {
        "kind": "capability",
        "name": "message.recall",
        "platform": platform_name,
        "target": target,
        "params": {"message_id": reply_id},
    }

    # Telegram deleteMessage 还需要 chat_id；message.recall 会从 target.id 映射。
    if platform_name == "telegram":
        call["params"]["chat_id"] = scope_target_id

    req = {
        "version": "elvena.v3",
        "elwisp": {"name": "hook_recall"},
        "source": "rules-hook",
        "id": f"recall-{platform_name}-{reply_id}-{int(time.time() * 1000)}",
        "mode": "direct",
        "targets": [target],
        "calls": [call],
    }
    json.dump(req, sys.stdout, ensure_ascii=False)


if __name__ == "__main__":
    main()
```

OneBot 也可以不用 capability，直接 raw 调用：

```json
{
  "kind": "raw",
  "platform": "qqonebot",
  "api": "delete_msg",
  "params": {"message_id": "{{platform.reply_to_message_id}}"}
}
```

## 表情提取示例

以下示例用规则 Hook + exec 脚本实现表情提取功能，替代旧的内嵌表情 Hook。

### hooks.toml

```toml
[[rules]]
name = "emoticon_extract"
on = "llm.response.received"
priority = 1000
if = "llm.text"
op = "regex"
value = "\\[\\[[^\\[\\]]+\\]\\]"
actions = [
  { type = "exec", command = "uv run emoticon_extract.py", stdout = "outputs", field = "llm.text", timing = "after_assistant" },
]
```

### emoticon_extract.py

脚本从 stdin 读取 event JSON，提取 `[[token]]`，检查 `emoticons/<token>/` 目录是否有图片，有则生成 emoticon output 并从文本中移除 token，无则保留原样。

```python
#!/usr/bin/env python3
import json
import os
import random
import re
import sys

TOKEN_RE = re.compile(r"\[\[([^\[\]]+)\]\]")
EMOTICON_DIR = "emoticons"
IMAGE_EXTS = {".jpg", ".jpeg", ".png", ".gif", ".webp"}

def main():
    data = json.load(sys.stdin)
    text = data.get("event", {}).get("llm", {}).get("text", "")
    outputs = []

    def replace(match):
        name = match.group(1).strip()
        d = os.path.join(EMOTICON_DIR, name)
        if not os.path.isdir(d):
            return match.group(0)
        images = [f for f in os.listdir(d)
                  if os.path.splitext(f)[1].lower() in IMAGE_EXTS]
        if not images:
            return match.group(0)
        path = os.path.join(d, random.choice(images))
        outputs.append({"kind": "emoticon", "name": name, "path": path})
        return ""

    cleaned = TOKEN_RE.sub(replace, text).strip()
    result = {"outputs": outputs, "text": cleaned}
    json.dump(result, sys.stdout, ensure_ascii=False)

if __name__ == "__main__":
    main()
```

### 配置说明

- `root_dir`（旧内嵌插件的配置项）不再需要；脚本里直接写相对 `plugins/` 的目录路径。
- `timing` 控制表情发送时机：`immediate`（默认）在 LLM 文本输出前发送，`after_assistant` 在 assistant 回复后发送。
- `field = "llm.text"` 让脚本返回的 `text` 覆写 LLM 响应文本，移除已被提取的 token。
- 不设 `field` 时脚本只产出 outputs，不修改原文（适合"检测到内容就发通知但不改原文"的场景）。

### write_elbot_hook Skill 示例

下面内容在ElBot第一次运行时自动生成在 `skills/go/write_elbot_hook/SKILL.elyph`，用于让 LLM 按需求生成规则 Hook 配置和可选 exec 脚本。

```text
#skill write_elbot_hook - 根据需求编写 ElBot 规则 Hook
<- $requirement:str!
<- $script_name:str?
-> $script_content:str
?if(windows){
$hook_config:str=%AppData/Roaming/ElBot/hooks.toml
}
?else {
$hook_config:str=~/.config/elbot/plugins/hooks.toml
}
** $requirement 是用户想实现的 Hook 行为，直接修改$hook_config；$script_name 是可选脚本文件名，仅在需要 exec 时使用
** $script_content 仅在需要 exec 时输出完整脚本，否则说明不需要
** Hook 点：platform.connected=平台连接完成；platform.message.received=平台消息刚收到（适合关键词拦截、预处理和 consume）
** Hook 点：agent.input.prepared=Agent 输入准备后（改写用户输入文本）；llm.turn.prepared=LLM turn 准备阶段（改写本轮 latest user 文本）；llm.request.prepared=LLM 请求发出前（改写 latest user 文本）
** Hook 点：llm.response.received=LLM 响应收到后（改写 assistant 文本或提取标记）；tool.call.prepared=工具调用执行前（改写 tool.arguments）；tool.call.completed=工具调用完成后（改写 tool.result）
** Hook 点：agent.output.prepared=Agent 输出准备后（改写 assistant message.text）；agent.turn.output.prepared=本轮最终输出准备后（改写 assistant message.text）；platform.message.sent=平台消息发送后（记录或后处理）；error.occurred=发生错误时（记录或通知）
** 匹配字段——平台/消息：platform.name、scope_id、user_id、conversation_id、message_id、reply_to_message_id
** 引用字段说明：platform.message_id 是当前入站平台消息 ID；platform.reply_to_message_id 是当前消息引用/回复的目标平台消息 ID，适合撤回引用消息、引用上下文判断和传给 Elvena calls
** 匹配字段——Actor：actor.id、actor.user_id、actor.role（superadmin/user）、actor.group_role（owner/admin/member）、actor.display_name
** 匹配字段——Session/Request：session.id/mode/status、request.id/kind/phase
** 匹配字段——Message：message.text（部分 Hook 点可编辑）、message.content_text（纯文本聚合，用于匹配）、message.role
** 匹配字段——LLM：llm.text（可编辑）、llm.raw_text（只匹配不可编辑）、llm.latest_user_text（可编辑）、llm.latest_user_content_text（用于匹配）、llm.provider、llm.model
** 匹配字段——Tool：tool.name、tool.arguments（可编辑）、tool.result（可编辑）、tool.risk
** 匹配写法：always=true 无条件匹配（不能与 if/op/value 或 match 混用）；单条件用 if/op/value；多条件 AND 用 match 数组（每项含 field/op/value）
** 匹配操作：exists=非空、contains=包含 value、fullmatch=完全等于、startswith=以 value 开头、endswith=以 value 结尾、regex=正则匹配（捕获组可用模板引用）
** 可编辑字段按 Hook 点：platform.message.received 和 agent.input.prepared→message.text；llm.turn.prepared 和 llm.request.prepared→llm.latest_user_text；llm.response.received→llm.text；tool.call.prepared→tool.arguments；tool.call.completed→tool.result；agent.output.prepared、agent.turn.output.prepared、platform.message.sent→assistant message.text；llm.raw_text 只能匹配不能编辑
** Action 类型：prepend=开头追加、append=末尾追加、replace=替换（可用 pattern/replace/all）、delete=删除（等同 replace 空串）、send=生成输出意图由 Output Manager 发送、tool=调用已注册工具（结果存 actions.<name>.result）、exec=执行本地脚本（默认 cwd 是 plugins/）
** send 字段：kind（text/image/file/emoticon/at，默认 text）、text（文本内容）、timing（immediate/after_assistant，默认 immediate）、target（输出目标，未指定时用当前上下文）
** segment 字段：kind（text/image/file/emoticon）、text（内容或附加文本）、url（HTTP/HTTPS）、path（相对 plugins/ 的本地路径）、base64（编码数据）、name（文件名或表情名）、mime_type（MIME 提示）
** exec 字段：command（命令）、cwd（可覆盖工作目录，相对路径仍基于 plugins/）、stdin（自定义标准输入，支持模板；未设置时为 event+match 的 JSON）、timeout_seconds（超时）、field（仅 stdout=outputs 且需用 text 覆写字段时使用）
** exec stdout 模式：capture=默认（存 actions.<name>.result）、send=作为文本输出发送、outputs=解析 JSON 读取 outputs 数组和可选 text、elvena=解析为 Elvena JSON 请求交内部 Elnis、ignore=忽略
** outputs JSON：outputs 数组每项格式同 send segments；text 可选，action 设 field 时用 text 覆写该字段
** elvena JSON：必须是完整 Elvena 请求，UTF-8 编码，经内部 Elvena Bus 投递
** 角色字段：roles 同时匹配内部角色和群身份；actor_roles 只匹配 superadmin/user；group_roles 只匹配 owner/admin/member
** 控制字段：consume=true 阻止后续 slash 命令和 LLM 处理；stop_propagation=true 阻止同 Hook 点后续规则执行，二者都与 on/name/match/actions 等字段平级
** 模板变量：platform.name/scope_id/user_id/message_id/reply_to_message_id、actor.id/user_id/role、message.text/content_text、llm.text/raw_text/latest_user_text、tool.arguments/result、actions.<name>.result/error；regex 捕获组用 match.regex.0.group.1 或命名组 match.regex.0.<name>
** 先判断需求适合的 Hook 点，只使用本 Skill 列出的 Hook 点；选择 always、单条件 if/op/value 或 match 多条件，不混用互斥写法
** 只编辑当前 Hook 点允许修改的字段；需要发送消息时优先用 send action 产出 output 意图，不直接调用平台发送
** 需要多模态输出时使用 segments，字段格式必须使用本 Skill 列出的 segment 字段；需要本地脚本时使用 exec action 并明确 stdout 模式
** exec 脚本默认以 plugins/ 为工作目录，脚本和资源路径用相对 plugins/ 的路径；工具调用必须遵守 Security Policy，只调用当前 Actor 可用且不需要交互确认的工具
** 输出必须包含可直接复制的 TOML；如果需要脚本，也输出完整脚本内容
~ 使用本 Skill 未列出的 Hook 点
~ 修改不可编辑字段
~ 让 Hook 或脚本直接绕过 Output Manager 发送平台消息
~ 让 exec stdout 输出非 JSON 却声明 outputs 或 elvena 模式
~ 编造不存在的 action 类型、segment 字段、stdout 模式或模板变量
?if(需求需要拦截输入并阻止后续 LLM 处理) {
  ** 在 platform.message.received Hook 上使用 consume = true
}
?else {
  ** 不主动设置 consume，避免误拦截正常对话
}
?if(需求包含脚本处理、外部程序或复杂文本解析) {
  ** 使用 exec action
  ** 如果脚本要同时发送 outputs 并改写文本，stdout 使用 outputs，并在 action 上设置 field
  ** 脚本从 stdin 读取 event JSON，向 stdout 写规定格式结果
}
?else {
  ** 优先用 replace、append、prepend、delete、send 或 tool action 完成
}
> 选择的 Hook 点、匹配条件和 action 原因。
> plugins/hooks.toml 中可复制的 [[rules]] 配置。
?if(用到exec){
> 脚本文件路径和完整脚本内容
}
> 把脚本和资源放在 plugins/ 下，并按需测试。
> 完成后通知用户用/hooks reload重载
```


