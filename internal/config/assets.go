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
	{Path: filepath.Join("skills", "agent", "agent_skill_creator", "ELBOT_SKILL.toml"), Content: defaultAgentSkillCreatorSkillTOML},
	{Path: filepath.Join("skills", "agent", "write_elbot_hook", "SKILL.md"), Content: defaultWriteElbotHookSkillMD},
	{Path: filepath.Join("skills", "agent", "write_elbot_hook", "ELBOT_SKILL.toml"), Content: defaultWriteElbotHookSkillTOML},
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
model = "deepseek-v4-flash"

[mode_models.elwisp2]
provider = "deepseek"
model = "deepseek-v4-flash"

[mode_models.elwisp3]
provider = "deepseek"
model = "deepseek-v4-flash"
`

const defaultSoulMD = `You are ElBot, a helpful assistant. ElBot's repo is https://github.com/Elflare/elbot.
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
risk, superadmin_only, tags, command, timeout_seconds, expose_root, parameters, [args]

纯文档型 Skill 只限制可见性时，可以只写：
risk = "high"
superadmin_only = true

示例：
risk = "medium"
superadmin_only = false
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
risk 可选；注册成普通工具时必填。纯文档型未写 risk 时按 safe 处理。
superadmin_only 可选，true 时只有超级管理员可发现或使用。
tags 可选，相当于为该工具分类。

创建 AgentSkill：
在配置目录的 skills/agent/<name>/SKILL.md 编写 AgentSkill 说明。Windows 默认位置是 %APPDATA%/ElBot/skills/agent/<name>/SKILL.md；Linux 遵循 XDG 配置目录，通常是 $XDG_CONFIG_HOME/elbot/skills/agent/<name>/SKILL.md。
AgentSkill 适合文档型任务、外部脚本包装、临时或低频流程。
如果要把该 AgentSkill 注册成普通工具，再为它创建 ELBOT_SKILL.toml。

AgentSkill 和 EL Skill 分开选择：
高性能、强结构化、需要校验/编译/长期维护的任务，优先使用 EL Skill。
`

const defaultAgentSkillCreatorSkillTOML = `risk = "low"
superadmin_only = true
`

const defaultWriteElbotHookSkillMD = `---
name: write_elbot_hook
description: 编写或修改 ElBot 规则 Hook 配置。
---

hook路径：
windows：%AppData%/ElBot/plugins/hooks.toml
Linux：= $XDG_CONFIG_HOME/elbot/plugins/hooks.toml
若 XDG_CONFIG_HOME 未设置，按 XDG 规范使用 $HOME/.config

简单hook直接参考hooks.toml中的注释写，复杂hook看https://raw.githubusercontent.com/Elflare/elbot/main/docs/hooks.md
修改完hooks.md后提醒用户使用 /hooks reload 重新加载
`

const defaultWriteElbotHookSkillTOML = `risk = "low"
superadmin_only = true
`
const defaultEnvExample = `# Copy this file to .env or set these variables in your OS environment.

# Provider API keys
DEEPSEEK_API_KEY=
OPENAI_API_KEY=

# Platform secrets
TELEGRAM_BOT_TOKEN=
TELEGRAM_PROXY_URL=

# Web tools
JINA_API_KEY=
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
# Full docs and examples: https://github.com/Elflare/elbot/blob/main/docs/hooks.md
#
# Optional plugin configs:
# [[plugins]]
# name = "demo"
# enabled = true
# path = "demo/hook.toml" # optional; default is plugins/<name>/hook.toml
#
# Plugin hook.toml may include:
# [plugin]
# name = "demo" # optional metadata; [[plugins]].name is the reference name
# description = "demo plugin"
#
# Rule shape:
# [[rules]]
# name = "stable_debug_name"
# description = "short summary" # optional, recommended
# on = "hook.point"
# enabled = true          # optional, default true
# priority = 1000        # optional, smaller runs earlier
# wakeup = "required"   # optional: required (default), any, or forbidden.
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
# action = "send"        # send/prepend/append/replace/delete/tool/exec
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
# send action with outputs (kind/text/url/path/base64/name/mime_type/user_id/message_id/emoticon_id):
# target.platform/target.scope_id/target.private_user_id/target.group_id/target.superadmins
# can redirect send outputs; omit target to send to the current context.
# actions = [
#   { type = "send", timing = "after_assistant", outputs = [
#     { kind = "text", text = "检测到关键词" },
#     { kind = "image", path = "alert.png" },
#     { kind = "image", name = "微笑", path = "emoticons/微笑/01.png" },
#     { kind = "at", user_id = "123456" },
#   ] },
# ]
#
# exec action uses hook.v2 line protocol:
# ElBot sends request system.init, then request event.handle on stdin.
# Scripts reply with response frames using the Host request ID (host:*).
# Hook-originated requests use plugin:* IDs; stdout only contains request/response/event frames.
# stderr is logged on success; on exec failure/crash/timeout/protocol error, the
# stderr tail is included in the Hook failure notice.
# A one-shot script responds to event.handle with {"status":"completed",...}
# and exits with code 0; non-zero exit means process failure.
# request frame shape: {"type":"request","id":"plugin:x","method":"platform.call","params":{...}}.
# response frame shape: {"type":"response","id":"x","ok":true,"result":{...}} or
# {"type":"response","id":"x","ok":false,"error":"..."}.
# event.handle result.result is available as {{actions.<name>.result}}.
# event.handle result.error is available as {{actions.<name>.error}}.
# event.handle result.message.text is written back to the action field.
# event.handle result.outputs, consume and stop_propagation apply Hook output/control.
# actions = [
#   { action_name = "extract", type = "exec", command = "uv run extract.py", field = "llm.text", timing = "after_assistant" },
# ]
#
# Supported hook points:
# platform.connected, platform.message.received, agent.input.prepared,
# llm.turn.prepared, llm.request.prepared, llm.response.received,
# tool.call.prepared, tool.call.completed, agent.output.prepared,
# agent.turn.output.prepared, platform.message.sent, error.occurred
#
# Match ops: always, exists, contains, fullmatch, startswith, endswith, regex.
# Common fields:
# platform.name/scope_id/user_id/conversation_id/message_id/reply_to_message_id
# actor.id/user_id/role/group_role/display_name
# session.id/mode/title/status
# request.id/kind/session_id/phase (kind: turn,llm,tool,compress,sub_agent; phase: idle,llm,tool,awaiting_risk_confirm,awaiting_append_confirm,compact)
# message.id/text/display_text/platform_text/intent_text/role
# message.intent_text strips wakeup keywords and bot mentions; use it for user intent matching.
# message.reply.message_id/sender_id/text/display_text
# llm.text/source_text/latest_user_text/latest_user_display_text/provider/model
# tool.name/arguments/result/risk
# error.message
#
# wakeup="any" on platform.message.received also observes ordinary group messages
# that did not mention or wake the bot. wakeup="forbidden" observes only those
# messages and skips the rule when the user explicitly wakes the bot. Hook outputs
# may still be sent, but command/LLM processing only continues for woken messages.
#
# Editable fields:
# platform.message.received / agent.input.prepared: message.text
# llm.turn.prepared / llm.request.prepared: llm.latest_user_text
# llm.response.received: llm.text
# tool.call.prepared: tool.arguments
# tool.call.completed: tool.result
# agent.output.prepared / agent.turn.output.prepared / platform.message.sent: message.text
#
# Template variables include:
# {{platform.name}}, {{platform.scope_id}}, {{platform.user_id}}, {{platform.message_id}}, {{platform.reply_to_message_id}}
# {{actor.id}}, {{actor.user_id}}, {{actor.role}}, {{actor.group_role}}
# {{message.text}}, {{message.display_text}}, {{message.platform_text}}, {{message.intent_text}}
# {{message.reply.message_id}}, {{message.reply.sender_id}}, {{message.reply.text}}, {{message.reply.display_text}}
# {{llm.text}}, {{llm.source_text}}, {{llm.latest_user_text}}, {{llm.latest_user_display_text}}
# {{tool.arguments}}, {{tool.result}}
# {{error.message}}
# {{match.regex.0.group.1}}, {{match.regex.0.<name>}}
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
#   { action_name = "search", type = "tool", tool = "web_search", arguments = '{"query":"ElBot"}' },
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
# Example: emoticon extraction via exec + hook.v2.
# The script answers system.init, handles event.handle, and returns outputs plus message.text.
# [[rules]]
# name = "emoticon_extract"
# on = "llm.response.received"
# priority = 1000
# if = "llm.text"
# op = "regex"
# value = "\\[\\[[^\\[\\]]+\\]\\]"
# actions = [
#   { action_name = "extract", type = "exec", command = "uv run emoticon_extract.py", field = "llm.text", timing = "after_assistant" },
# ]
`
