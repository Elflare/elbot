package config

import (
	"fmt"
	"os"
	"path/filepath"
)

type defaultAsset struct {
	Path    string
	Content string
}

var defaultConfigAssets = []defaultAsset{
	{Path: "app.toml", Content: defaultAppTOML},
	{Path: "providers.toml", Content: defaultProvidersTOML},
	{Path: "state.toml", Content: defaultStateTOML},
	{Path: "SOUL.md", Content: defaultSoulMD},
	{Path: "memories.toml", Content: defaultMemoriesTOML},
	{Path: "elnis.toml", Content: defaultElnisTOML},
	{Path: "tool_tags.toml", Content: defaultToolTagsTOML},
	{Path: "plugins/hooks.toml", Content: defaultHooksTOML},
	{Path: filepath.Join("skills", "agent", "agent_skill_creator", "SKILL.md"), Content: defaultAgentSkillCreatorSkillMD},
	{Path: filepath.Join("skills", "go", "write_elbot_hook", "SKILL.elyph"), Content: defaultWriteElbotHookSkillElyph},
	{Path: ".env.example", Content: defaultEnvExample},
}

var defaultConfigDirs = []string{
	"skills",
	filepath.Join("skills", "agent"),
	filepath.Join("skills", "go"),
	"plugins",
	"long_memory",
}

func EnsurePlatformDefaults() (string, error) {
	configPath, ok := platformDefaultConfigPath()
	if !ok {
		return "", fmt.Errorf("platform config dir is unavailable")
	}
	configDir := filepath.Dir(configPath)
	for _, dir := range defaultConfigDirs {
		path := filepath.Join(configDir, dir)
		if err := os.MkdirAll(path, 0o755); err != nil {
			return "", fmt.Errorf("create default config dir %q: %w", path, err)
		}
	}
	for _, asset := range defaultConfigAssets {
		path := filepath.Join(configDir, asset.Path)
		if err := writeFileIfMissing(path, asset.Content); err != nil {
			return "", err
		}
	}
	return configPath, nil
}

func writeFileIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat default config asset %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create default config asset dir %q: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write default config asset %q: %w", path, err)
	}
	return nil
}

const defaultAppTOML = `# Main application config. Relative paths are resolved from this file.

[config_files]
providers = "providers.toml"
state = "state.toml"
elnis = "elnis.toml"
tool_tags = "tool_tags.toml"

[storage]
# Leave empty to use the platform default data directory.
sessions_sqlite_path = ""
chat_history_sqlite_path = ""

[runtime]
log_level = "info"
log_retention_days = 30

[maintenance.log_cleanup]
enabled = true
schedule = "0 3 * * *"

[maintenance.session_cleanup]
enabled = false
schedule = "15 3 * * *"
retention_days = 30

[maintenance.sandbox_cleanup]
enabled = true
schedule = "0 4 * * *"
retention_days = 7

[maintenance.chat_history_cleanup]
enabled = true
schedule = "35 4 * * *"
retention_days = 180

[sandbox]
root = ""

[file_delivery]
# base64 will increase the file size by about 33%.
max_direct_base64_bytes = 8388608
backend = "base64"
s3_endpoint = ""
s3_region = "auto"
s3_bucket = ""
s3_access_key_env = "ELBOT_S3_ACCESS_KEY_ID"
s3_secret_key_env = "ELBOT_S3_SECRET_ACCESS_KEY"
s3_public_base_url = ""

[platform_files]
max_receive_file_bytes = 104857600
download_timeout_secs = 60

[llm_request]
first_chunk_timeout_seconds = 180
stream_idle_timeout_seconds = 60
response_timeout_seconds = 0
max_retries = 3
retry_initial_delay_seconds = 2

[context]
compact_enabled = true
compact_trigger_ratio = 0.8

[soul]
path = "SOUL.md"

[view]
session_list_page_size = 10

[commands]
prefixes = ["/"]

[tools]
max_rounds_per_turn = 10

[resident_memory]
# Memory length units: CJK characters count as one each; English/digits count by word.
core_max_units = 200
normal_max_units = 300

[security]
user_max_tool_risk = "low"
superadmin_confirm_risk = "high"

[security.superadmins]
cli = ["local"]

[session]

[session.idle_expiration]
group_user_ttl_minutes = 10
group_superadmin_ttl_minutes = 10
private_user_ttl_minutes = 10
private_superadmin_ttl_minutes = 0

[session.naming]
trigger_step = 3

[platform.cli]
enabled = true
# Default CLI client profile. Used by elbot/elbot cli when -c is omitted.
default_client = "local"
# Default WebSocket URL for clients without their own clients.<name>.url.
# To connect to another machine, set url under the client profile.
default_url = "ws://127.0.0.1:32172/cli/v1/ws"

# Used only when this ElBot runs as a CLI server. It listens here; clients connect via their url.
[platform.cli.server]
enabled = false
listen = "127.0.0.1:32172"

# Client ids allowed to log in to this CLI server and their token environment variables.
[platform.cli.server.tokens]
local = ["ELBOT_CLI_LOCAL_TOKEN"]

# Client profile used by this command. For remote servers, add url = "ws://SERVER_IP:32172/cli/v1/ws".
[platform.cli.clients.local]
token_env = ["ELBOT_CLI_LOCAL_TOKEN"]

# [platform.telegram]
# enabled = false
# bot_token_env = "TELEGRAM_BOT_TOKEN"
# proxy_url_env = "TELEGRAM_PROXY_URL" # optional; read OS env first, then config .env
# trigger_keywords = ["bot"]
# format = "html" # html/plain/rich
# stream_edit_interval_milliseconds = 250
`

const defaultMemoriesTOML = `# Resident memory data. Generated by ElBot.
# Stores per-platform, per-actor resident memories.
`

const defaultProvidersTOML = `# Provider/model config. Do not commit real API keys.
# Prefer api_key_env and set secrets in the OS environment or .env.

[providers.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"

[providers.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
models = ["gpt-4o-mini"]

# [providers.openai.model_configs."gpt-4o-mini"]
# context_window = 128000
# extra_payload = { }

[model_metadata]
default_context_window = 256000
`

const defaultStateTOML = `[session]
default_mode = "work"

[mode_models.work]
provider = "deepseek"
model = "deepseek-v4-pro"

[mode_models.chat]
provider = "deepseek"
model = "deepseek-v4-flash"

# Optional Elnis LLM model slots. If omitted, each slot falls back to work.
[mode_models.elwisp1]
provider = "deepseek"
model = "deepseek-chat"

[mode_models.elwisp2]
provider = "deepseek"
model = "deepseek-chat"

[mode_models.elwisp3]
provider = "deepseek"
model = "deepseek-chat"
`

const defaultSoulMD = `You are ElBot, a helpful assistant.

Keep responses concise, accurate, and friendly. Follow the user's language unless they ask otherwise.
`

const defaultElnisTOML = `# Elnis listening hub config. Loaded from app.toml [config_files].elnis.

enabled = false
# Default ElBot internal tools that Elwisp events may preload.
allowed_tools = ["web_search", "web_extract"]

[http]
addr = "127.0.0.1:32170"
max_body_bytes = 1048576
queue_size = 128
workers = 2

[tokens.home]
# Read token values from OS environment variables or the config directory .env file.
token_env = ["ELNIS_HOME_TOKEN", "ELNIS_HOME_TOKEN_ALT"]

# Delivery is allowed by default. Targets listed here are explicitly disabled.
# In disabled config, platform-only disables all delivery to that platform.
[delivery_disabled]
targets = [
  # { platform = "telegram" },
  # { platform = "telegram", type = "private", id = "123456789" },
  # { platform = "qqonebot", type = "group", id = "987654321" },
]

[segment]
max_file_bytes = 104857600  # 100MB, max per image/file segment
download_timeout_secs = 60

# Elwisp is enabled by default. Configure a named Elwisp only when you need
# token restrictions, tool overrides, delivery disables, or explicit disable.
# [elwisps.server-watchdog]
# allowed_tokens = ["home"]
# allowed_tools = ["shell", "web_search"]
# disabled_external_tools = ["danger_tool"]
# disabled_targets = [
#   { platform = "qqonebot", type = "group", id = "987654321" },
# ]

#
# [elwisps.spike-checker]
# enabled = false
`

const defaultAgentSkillCreatorSkillMD = `---
name: agent_skill_creator
description: 创建或修改 AgentSkill 的 ELBOT_SKILL.toml。
---

if a skill 带脚本:
    if 你没有该脚本的Schema只能用shell来运行 and 你觉得使用shell很麻烦:
        使用该工具创建 ELBOT_SKILL.toml，之后会自动注入你的Schema，之后可以不使用shell运行
elif:
    检查你已知Schema工具，是否有该技能的脚本，就可以直接调用，而不用shell
ELBOT_SKILL.toml写法：
只允许这些字段：
risk, tags, command, timeout_seconds, expose_root, parameters, [args]

示例：
risk = "medium"
tags = ["doc"]
command = ["python", "foo.py"]
timeout_seconds = 30
expose_root = false

parameters = '''
{
  "type": "object",
  "required": ["input"],
  "properties": {
    "input": {"type": "string", "description": "输入文本"},
    "mode": {"type": "string", "description": "处理模式"}
  }
}
'''

[args]
input = "--input"
mode = "--mode"

含义：
工具调用 {"input":"abc","mode":"fast"} 会执行：
python foo.py --input abc --mode fast

command 必须是字符串数组。
parameters 必须是 JSON object schema。
parameters.properties 定义工具有哪些入参；[args] 的 key 必须对应 parameters.properties。
risk 必填。
tags 可选，相当于为该工具分类。

创建 AgentSkill：
在配置目录的 skills/agent/<name>/SKILL.md 编写 AgentSkill 说明。Windows 默认位置是 %APPDATA%/ElBot/skills/agent/<name>/SKILL.md；Linux 遵循 XDG 配置目录，通常是 $XDG_CONFIG_HOME/elbot/skills/agent/<name>/SKILL.md。
AgentSkill 适合文档型任务、外部脚本包装、临时或低频流程。
如果要把该 AgentSkill 注册成普通工具，再为它创建 ELBOT_SKILL.toml。

AgentSkill 和 EL Skill 分开选择：
高性能、强结构化、需要校验/编译/长期维护的任务，优先使用 EL Skill。
`

const defaultWriteElbotHookSkillElyph = `#skill write_elbot_hook - 根据需求编写 ElBot 规则 Hook
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
** 唤起字段：require_wakeup 默认 true；在 platform.message.received 上设为 false 可监听未 at、未命中唤起词、未回复机器人的普通群消息，但不会自动让 LLM 处理该消息
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

> 完成后通知用户用/hooks reload重载
`

const defaultEnvExample = `# Copy this file to .env or set these variables in your OS environment.

# Provider API keys
DEEPSEEK_API_KEY=
OPENAI_API_KEY=

# Platform secrets
TELEGRAM_BOT_TOKEN=
TELEGRAM_PROXY_URL=

# Web tools
WEB_EXTRACT_PROXY=

# CLI remote client/server tokens
ELBOT_CLI_LOCAL_TOKEN=
ELBOT_CLI_WINDOWS_TOKEN=

# Elnis tokens
ELNIS_HOME_TOKEN=
ELNIS_HOME_TOKEN_ALT=
`

const defaultToolTagsTOML = `# Tool tag config. Prompts are appended to system prompt only after the tag is activated by @tool:<tag>.
[tags.agent]
tools = ["read_file", "edit_file", "shell", "long_memory", "long_memory_search", "long_memory_write"]
prompt = """
ROLE: Complete the user's task safely and accurately.
MUST:
- Inspect context first; do not make things up.
- Use tools when possible; follow required syntax.
- Plan before coding; ask if unclear or risky.
- Touch only what must be changed; keep it simple.
- Validate success criteria before and after implementation.
"""
`

const defaultHooksTOML = `# Declarative Hook rules. Loaded at ElBot startup.
# Complex logic should be implemented as a code plugin instead.
#
# Rule shape:
# [[rules]]
# name = "stable_debug_name"
# on = "hook.point"
# enabled = true          # optional, default true
# priority = 1000        # optional, smaller runs earlier
# require_wakeup = true  # optional, default true; false allows passive group messages.
#
# Single condition:
# if = "message.text"
# op = "contains"
# value = "hello"
#
# No condition:
# always = true
#
# Multiple conditions are AND:
# match = [
#   { field = "platform.name", op = "fullmatch", value = "qqonebot" },
#   { field = "message.text", op = "contains", value = "猫" },
# ]
#
# Single action:
# action = "send"        # send/prepend/append/replace/delete/tool
# text = "..."
# timing = "after_assistant" # optional for send outputs; default immediate.
#
# Multiple actions run in order:
# actions = [
#   { type = "replace", field = "message.text", pattern = "猫", replace = "狗", all = true },
#   { type = "send", kind = "text", text = "检测到关键词", timing = "after_assistant" },
#   { type = "append", field = "message.text", text = "!" },
# ]
#
# send action with segments (kind/text/url/path/base64/name/mime_type):
# actions = [
#   { type = "send", timing = "after_assistant", segments = [
#     { kind = "text", text = "检测到关键词" },
#     { kind = "image", path = "alert.png" },
#     { kind = "emoticon", name = "微笑", path = "emoticons/微笑/01.png" },
#   ] },
# ]
#
# exec action with stdout = "outputs":
# Script stdout must be JSON: {"outputs":[...],"text":"..."}
# outputs items use the same segment spec as send segments.
# text is written back to the field specified by the action's field setting.
# actions = [
#   { type = "exec", command = "uv run extract.py", stdout = "outputs", field = "llm.text", timing = "after_assistant" },
# ]
#
# Supported hook points:
# platform.connected, platform.message.received, agent.input.prepared,
# llm.request.prepared, llm.response.received, tool.call.prepared,
# tool.call.completed, agent.output.prepared, agent.turn.output.prepared,
# platform.message.sent, error.occurred
#
# Match ops: always, exists, contains, fullmatch, startswith, endswith, regex.
# Common fields:
# platform.name/scope_id/user_id/conversation_id/message_id/reply_to_message_id
# actor.id/user_id/role/display_name
# session.id/mode/status
# request.id/kind/phase
# message.text/content_text/role
# llm.text/raw_text/latest_user_text/latest_user_content_text/provider/model
# tool.name/arguments/result/risk
#
# require_wakeup=false on platform.message.received lets a rule observe ordinary
# group messages that did not mention or wake the bot. Hook outputs may still be
# sent, but command/LLM processing only continues for woken messages unless the
# rule consumes the message first.
#
# Editable fields:
# platform.message.received / agent.input.prepared: message.text
# llm.request.prepared: llm.latest_user_text
# llm.response.received: llm.text, llm.raw_text
# tool.call.prepared: tool.arguments
# tool.call.completed: tool.result
# agent.output.prepared / platform.message.sent: assistant message.text
#
# Template variables include:
# {{platform.name}}, {{platform.scope_id}}, {{platform.user_id}}
# {{actor.id}}, {{actor.user_id}}
# {{message.text}}, {{message.content_text}}
# {{llm.text}}, {{llm.raw_text}}, {{llm.latest_user_text}}, {{llm.latest_user_content_text}}
# {{tool.arguments}}, {{tool.result}}
# {{actions.<name>.result}}, {{actions.<name>.error}} from earlier tool actions.

# Notify qqonebot superadmins after OneBot connects.
[[rules]]
name = "notify_qqonebot_connected"
on = "platform.connected"
priority = 1000
if = "platform.name"
op = "fullmatch"
value = "qqonebot"
action = "send"
kind = "text"
text = "ElBot 已连接 QQ OneBot。"
target.superadmins = true

# Example: append a low-risk tool result to the same user message before the LLM request.
# [[rules]]
# name = "inject_web_search"
# on = "llm.request.prepared"
# priority = 1000
# always = true
# actions = [
#   { name = "search", type = "tool", tool = "web_search", arguments = '{"query":"ElBot"}' },
#   { type = "append", field = "llm.latest_user_text", text = "\n\nHook 工具结果：{{actions.search.result}}" },
# ]
#
# Example: modify the final assistant output shown for one turn without changing LLM history.
# [[rules]]
# name = "cat_to_dog_final_output"
# on = "agent.turn.output.prepared"
# always = true
# action = "replace"
# field = "message.text"
# pattern = "猫"
# replace = "狗"
# all = true
#
# Example: multiple conditions with one action.
# [[rules]]
# name = "cat_to_dog"
# on = "agent.input.prepared"
# match = [
#   { field = "platform.name", op = "fullmatch", value = "qqonebot" },
#   { field = "message.text", op = "contains", value = "猫" },
# ]
# action = "replace"
# field = "message.text"
# pattern = "猫"
# replace = "狗"
# all = true
#
# Example: emoticon extraction via exec + outputs.
# The script reads event JSON from stdin, extracts [[token]] tokens, picks a random
# image from emoticons/<token>/, outputs JSON with emoticon outputs and cleaned text.
# [[rules]]
# name = "emoticon_extract"
# on = "llm.response.received"
# priority = 1000
# if = "llm.text"
# op = "regex"
# value = "\\[\\[[^\\[\\]]+\\]\\]"
# actions = [
#   { type = "exec", command = "uv run emoticon_extract.py", stdout = "outputs", field = "llm.text", timing = "after_assistant" },
# ]
`
