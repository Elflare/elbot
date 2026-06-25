<!-- This file is auto-translated from docs/hooks.md. Do not edit manually. -->

# Hook

The Hook Layer is used to extend behavior before and after key processes, for example:

- Agent input processing.
- LLM request preparation.
- LLM response processing.
- Before and after platform transmission.
- Platform connection events.

Hooks can modify messages, append output intents, call low-risk tools, or inject resident memory. Rule Hooks can also execute local scripts.

Important convention: Hooks do not replace the Security Layer; security determinations are still based on the Security Layer. Hooks and plugins do not send platform messages directly; they should return an output intent, which is then handed over by the Agent to the Output Manager for sending.

## Built-in Hooks

ElBot registers two types of built-in hooks with the program:

| Type | Description |
| --- | --- |
| Rule Hook | Loads declarative rules from `plugins/hooks.toml`, supporting conditional matching, text operations, output sending, tool calls, and script execution. |
| Resident Memory Hook | Inject the resident memory and temporary username of the current platform + actor every round. |

The Emoji Hook has been changed from an embedded plugin to a Rule Hook example; see the [Emoji Extraction Example](#表情提取示例) in this document.

## Rule Hook Configuration

Rule Hook configurations are fixed in `plugins/hooks.toml` of the configuration directory. Plugin-specific configurations are placed in `plugins/<plugin-name>.toml`.

### Rule Structure

```toml
[[rules]]
name = "stable_debug_name"          # Required, used for logs and audit
on = "hook.point"                   # Required, Hook point
enabled = true                      # Optional, defaults to true
priority = 1000                     # Optional, smaller values are executed first
```

### Condition Matching

Single condition:

```toml
if = "message.text"
op = "contains"
value = "hello"
```

No condition:

```toml
always = true
```

Multiple conditions (AND):

```toml
match = [
  { field = "platform.name", op = "fullmatch", value = "qqonebot" },
  { field = "message.text", op = "contains", value = "猫" },
]
```

`always` cannot be used in combination with `if/op/value` or `match`; `if/op/value` cannot be used in combination with `match`.

### Matching Operations

| op | Description |
| --- | --- |
| `always` | Unconditional match; field or value cannot be set |
| `exists` | Field is not empty |
| `contains` | Field contains value |
| `fullmatch` | Field equals value exactly |
| `startswith` | Field starts with value |
| `endswith` | Field ends with value |
| `regex` | Regular expression match; capture groups can be referenced via template variables |

### Matchable Fields

```
platform.name / scope_id / user_id / conversation_id / message_id / reply_to_message_id
actor.id / user_id / role / group_role / display_name
session.id / mode / status
request.id / kind / phase
message.text / content_text / role
llm.text / raw_text / latest_user_text / latest_user_content_text / provider / model
tool.name / arguments / result / risk
```

### Hook Point

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

### Editable Fields

Different Hook Points allow different editable fields:

| Hook Point | Editable Fields |
| --- | --- |
| `platform.message.received` / `agent.input.prepared` | `message.text` |
| `llm.turn.prepared` / `llm.request.prepared` | `llm.latest_user_text` |
| `llm.response.received` | `llm.text` |
| `tool.call.prepared` | `tool.arguments` |
| `tool.call.completed` | `tool.result` |
| `agent.output.prepared` / `agent.turn.output.prepared` / `platform.message.sent` | assistant `message.text` |

`llm.raw_text` can be used for conditional matching, but cannot be edited.

### Action Type

Each rule can contain a single `action` or multiple `actions` (executed in order).

#### Text Operations

```toml
# Single action
action = "replace"
field = "message.text"
pattern = "猫"
replace = "狗"
all = true                 # Optional, replaces only the first occurrence by default

# Multiple actions
actions = [
  { type = "replace", field = "message.text", pattern = "猫", replace = "狗", all = true },
  { type = "append", field = "message.text", text = "!" },
]
```

Text operation types: `prepend`, `append`, `replace`, `delete`. `delete` is equivalent to `replace` being an empty string.

Text operations preserve the positions of multimodal segments such as images and files in the message, modifying only the text segment.

#### send

`send` action generates an output intent, which is sent to the platform by the Output Manager.

Single output (backward compatible):

```toml
action = "send"
kind = "text"              # text/image/file/emoticon/at, default text
text = "检测到关键词"
timing = "after_assistant" # Optional, default immediate
```

Multi-segment output (segments):

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

The segment format is unified with the [Elvena Protocol](elnis-usage.md#segments多模态消息段), with additional support for local `path`, `base64`, and `emoticon` types:

| Field | Description |
| --- | --- |
| `kind` | `text` / `image` / `file` / `emoticon`, default `text` |
| `text` | Text content (required for text type, optional as additional text for other types) |
| `url` | HTTP/HTTPS URL（image/file） |
| `path` | Local file path (image/file/emoticon) |
| `base64` | base64 encoded data (image/file) |
| `name` | File name or emoticon name |
| `mime_type` | MIME type hint |

`target` and `timing` are inherited from the action to all segments.

#### tool

Call a registered tool; the result is stored in `{{actions.<name>.result}}` for use by subsequent actions.

```toml
actions = [
  { name = "search", type = "tool", tool = "web_search", arguments = '{"query":"ElBot"}' },
  { type = "append", field = "llm.latest_user_text", text = "\n\nHook 工具结果：{{actions.search.result}}" },
]
```

Tool calls are constrained by the Security Policy: the risk level must be within the range allowed for the current Actor, and high-risk tools requiring interactive confirmation will be rejected.

#### exec

Execute a local script. The script uses `plugins/` as the working directory by default, which can be overridden by `cwd` (absolute paths are used directly, while relative paths are still based on `plugins/`).

By default, stdin is a JSON containing the full event and match context. You can also use the `stdin` field to customize the stdin content (template rendering is supported).

`stdout` mode:

| Mode | Description |
| --- | --- |
| `capture` | Default value; saves stdout to `{{actions.<name>.result}}` for use by subsequent actions |
| `send` | Sends stdout as text output |
| `outputs` | Parses stdout as JSON, extracting the `outputs` array and optional `text` |
| `elvena` | Parses stdout as an Elvena JSON request and delivers it to Elnis via the internal Elvena Bus |
| `ignore` | Ignore stdout |

```toml
actions = [
  { type = "exec", command = "uv run script.py", stdout = "capture", timeout_seconds = 30 },
]
```

### exec outputs mode

When `stdout = "outputs"`, the script stdout must be JSON:

```json
{
  "outputs": [
    {"kind": "emoticon", "name": "微笑", "path": "emoticons/微笑/01.png"},
    {"kind": "text", "text": "已处理"}
  ],
  "text": "清理后的文本"
}
```

- `outputs`: Each item's format is consistent with send segments and is converted into an output intent to be sent to the platform.
- `text`: Optional. When action is set to `field`, `text` will completely overwrite the field (subject to editable field validation); The original text will not be modified if `field` is not set or if `text` is empty.

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

### Template Variables

Text fields and exec command/stdin support `{{...}}` template rendering:

```
{{platform.name}}          {{platform.scope_id}}      {{platform.user_id}}
{{actor.id}}               {{actor.user_id}}          {{actor.role}}
{{message.text}}           {{message.content_text}}
{{llm.text}}               {{llm.raw_text}}           {{llm.latest_user_text}}
{{tool.arguments}}         {{tool.result}}
{{actions.<name>.result}}  {{actions.<name>.error}}
```

Capture groups from regex matches can be referenced via `{{match.regex.0.group.1}}` or named groups `{{match.regex.0.<name>}}`.

### Role Partitioning

Rules can use `roles`, `actor_roles`, and `group_roles` for permission partitioning:

- `superadmin` / `user`: ElBot internal security roles.
- `owner` / `admin` / `member`: platform group identities, mapped by the platform adapter.

`roles` matches both internal roles and group identities; `actor_roles` only matches internal roles; `group_roles` only matches group identities.

```toml
[[rules]]
name = "admin_only_rule"
on = "platform.message.received"
roles = ["admin"]
always = true
action = "send"
text = "仅管理员可见"
```

### Control Fields

```toml
[control]
consume = true              # Block subsequent slash commands and LLM processing
stop_propagation = true      # Block subsequent rules at the same Hook point from continuing execution
```

`consume = true` is typically used for `platform.message.received` Hooks: it blocks subsequent commands and LLM processing after sending output, allowing the Hook to take full control of the message.

## Hook exec Delivery to Elvena

The `exec` action of a Rule Hook can be set to `stdout = "elvena"`. The script stdout must be a complete Elvena JSON request; ElBot will pass it to Elnis via the internal Elvena Bus, rather than going through HTTP token authentication again.

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

The script can output `mode = "direct"` notifications or `mode = "llm"` background tasks; Subsequent processing is still handled by Elnis's target arbitration, logging, deduplication, and background runner.

Elvena is based on JSON, and the content must be UTF-8 encoded. For the complete Elvena request fields, see [Elnis Configuration and Usage](elnis-usage.md#elvena-请求示例).

## Emoticon Extraction Example

The following example uses a rule Hook + exec script to implement the emoticon extraction function, replacing the old embedded emoticon Hook.

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

The script reads the event JSON from stdin, extracts `[[token]]`, and checks if there are images in the `emoticons/<token>/` directory. If images exist, it generates an emoticon output and removes the token from the text; otherwise, it keeps the text as is.

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

### Configuration Description

- `root_dir` (the configuration item for the old embedded plugin) is no longer needed; the directory path relative to `plugins/` is written directly in the script.
- `timing` controls the timing of emoticon delivery: `immediate` (default) sends before the LLM text output, and `after_assistant` sends after the assistant's reply.
- `field = "llm.text"` allows the `text` returned by the script to overwrite the LLM response text, removing the extracted tokens.
- When `field` is not set, the script only produces outputs without modifying the original text (suitable for scenarios such as "send a notification when content is detected but do not modify the original text").

### write_elbot_hook Skill Example

You can save the following content as `skills/go/write_elbot_hook/SKILL.elyph`, which allows the LLM to generate rule Hook configurations and optional exec scripts as needed.

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
** 控制字段：control.consume=true 阻止后续 slash 命令和 LLM 处理；control.stop_propagation=true 阻止同 Hook 点后续规则执行
** 模板变量：platform.name/scope_id/user_id、actor.id/user_id/role、message.text/content_text、llm.text/raw_text/latest_user_text、tool.arguments/result、actions.<name>.result/error；regex 捕获组用 match.regex.0.group.1 或命名组 match.regex.0.<name>
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
  ** 在 platform.message.received Hook 上使用 control.consume = true
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
```


