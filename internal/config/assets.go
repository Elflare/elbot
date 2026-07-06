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
?if(windows) {
  $hook_config:str = %AppData%/ElBot/plugins/hooks.toml
}
?else {
  $hook_config:str = $XDG_CONFIG_HOME/elbot/plugins/hooks.toml
  ** 若 XDG_CONFIG_HOME 未设置，按 XDG 规范使用 $HOME/.config
}

step goal {
  ** 根据 $requirement 直接修改 $hook_config
  ** 输出可直接使用的 TOML
  ** 仅当必须使用 exec 时才输出完整 $script_content
  ** 配置示例可看自动生成的 hooks.toml 注释
  ** 完整说明和例子可看 https://github.com/Elflare/elbot/blob/main/docs/hooks.md
  ** 完成后提醒用户执行 /hooks reload
}

step files {
  ** 主 hooks.toml 只允许顶层 [[plugins]] 和 [[rules]]
  ** [[plugins]] 只允许 name,enabled,path
  ** [[plugins]].path 必须相对 plugins/
  ** 未设置 path 时默认读取 plugins/<name>/hook.toml
  ** 插件 hook.toml 只允许顶层 [plugin] 和 [[rules]]
  ** [plugin] 只允许 description
  ** 插件 hook.toml 不能再写 [[plugins]]
  ** 严格模式不允许未知字段、旧字段 stdout/stdin
  ** 同一 rule 内 actions=[...] 和 [[rules.actions]] 只能二选一
}

step rule_shape {
  ** [[rules]] 字段白名单：name,on,priority,enabled,require_wakeup,if,op,value,always,match,roles,actor_roles,group_roles,action,actions,field,text,pattern,replace,kind,path,timing,tool,arguments,command,cwd,timeout_seconds,all,target,outputs,consume,stop_propagation
  ** Hook 点白名单：platform.connected,platform.message.received,agent.input.prepared,llm.turn.prepared,llm.request.prepared,llm.response.received,tool.call.prepared,tool.call.completed,agent.output.prepared,agent.turn.output.prepared,platform.message.sent,error.occurred
  ** 匹配写法三选一：always=true；if/op/value；match=[{field,op,value},...]
  ** op 白名单：exists,contains,fullmatch,startswith,endswith,regex
  ** roles 同时匹配内部角色和群身份
  ** actor_roles 只匹配 superadmin/user
  ** group_roles 只匹配 owner/admin/member
}

step fields {
  ** 匹配字段白名单：platform.name,scope_id,user_id,conversation_id,message_id,reply_to_message_id,actor.id,actor.user_id,actor.role,actor.group_role,actor.display_name,session.id,session.mode,session.status,request.id,request.kind,request.phase,message.text,message.content_text,message.raw_text,message.role,message.reply.message_id,message.reply.sender_id,message.reply.text,message.reply.content_text,llm.text,llm.raw_text,llm.latest_user_text,llm.latest_user_content_text,llm.provider,llm.model,tool.name,tool.arguments,tool.result,tool.risk,error.message
  ** 可编辑 field 映射：on=platform.message.received/agent.input.prepared 时 field="message.text"；on=llm.turn.prepared/llm.request.prepared 时 field="llm.latest_user_text"；on=llm.response.received 时 field="llm.text"；on=tool.call.prepared 时 field="tool.arguments"；on=tool.call.completed 时 field="tool.result"；on=agent.output.prepared/agent.turn.output.prepared/platform.message.sent 时 field="message.text"；llm.raw_text 只可匹配不可作为 field
}

step actions {
  ** action 类型白名单：prepend,append,replace,delete,send,tool,exec
  ** 单 action 可用 action="send" 加平铺字段
  ** 多 action 用 actions=[{type="..."},...] 或 [[rules.actions]]
  ** replace/delete 使用 field,pattern,replace,all
  ** tool 使用 tool 和 arguments
  ** tool 结果模板是 {{actions.<name>.result}}
  ** send 产生输出意图，由 Output Manager 发送
  ** send 字段：kind,text,timing,target,outputs
  ** timing 默认 immediate，可用 after_assistant
  ** target 字段：target.platform,target.scope_id,target.private_user_id,target.group_id,target.superadmins
  ** 不写 target 时发送到当前上下文
  ** output segment 字段：kind,text,url,path,base64,name,mime_type,user_id,message_id
  ** kind 白名单：text,image,file,emoticon,at,reply
  ** outputs 必须是 segment 数组
}

step templates: ** 模板变量白名单：{{platform.name}},{{platform.scope_id}},{{platform.user_id}},{{platform.message_id}},{{platform.reply_to_message_id}},{{actor.id}},{{actor.user_id}},{{actor.role}},{{message.text}},{{message.content_text}},{{message.raw_text}},{{message.reply.message_id}},{{message.reply.sender_id}},{{message.reply.text}},{{message.reply.content_text}},{{llm.text}},{{llm.raw_text}},{{llm.latest_user_text}},{{tool.arguments}},{{tool.result}},{{error.message}},{{actions.<name>.result}},{{actions.<name>.error}},{{match.regex.0.group.1}},{{match.regex.0.<name>}}

step exec_protocol {
  ** exec 字段：command,cwd,timeout_seconds,field
  ** command 按空白拆分后直接 exec，不自动套 shell
  ** 需要管道、重定向、&& 时显式使用平台 shell
  ** 工作目录默认是 plugins/ 或插件目录
  ** 插件规则的相对 cwd 不能逃出插件目录
  ** hook.v1 是行协议
  ** ElBot 向 stdin 写一行 init JSON
  ** 脚本必须只读取 stdin 第一行作为 init frame
  ** 脚本不能 read_all、read_to_end、fread 到 EOF、循环读到 EOF
  ** 脚本向 stdout 每行写一个 JSON frame
  ** stdout 只能写 JSON frame
  ** 日志和 debug 写 stderr 或文件
  ** stderr 成功时只进日志
  ** exec 失败/崩溃/超时/协议错误时 stderr 尾部会进入 Hook 失败通知
  ** 最后必须写 done 或 error frame
  ** 写出合法 done/error frame 后进程应以 0 退出
  ** 非 0 exit code 会被视为 exec 进程失败
  ** output frame 必须是 {"type":"output","outputs":[...]}
  ** output frame 字段：type,id,outputs
  ** 禁止使用 output={...} 或 segments=[...]
  ** request frame 字段：type,id,method,params
  ** done.message.text 写回 action.field
  ** done.result 存入 {{actions.<name>.result}}
  ** done.error 存入 {{actions.<name>.error}}
  ** done.consume 设置事件 consume
  ** done.stop_propagation 设置事件 stop_propagation
  ** matched=false 会回滚本规则并跳过后续 action
  ** error frame 字段：type,error 或 type,message
  ** request frame 可调用 platform.call、output.send、message.get_reply、message.get、hook.log
  ** 脚本发 request frame 后再逐行读取 stdin 的 response frame
  ** response frame 字段：type,id,ok,result,error
  ** request 失败会收到 ok=false/error，且当前 exec action 失败
}

step exec_init {
  ** init 顶层字段：type,version,event,match,runtime
  ** init.event 字段：id,point,time,metadata,control,platform,actor,session,request,message,llm,tool,outputs,error
  ** init.event.control 字段：consume,stop_propagation
  ** init.event.platform 字段：name,scope_id,user_id,conversation_id,message_id,reply_to_message_id
  ** init.event.actor 字段：id,user_id,role,group_role,display_name
  ** init.event.session 字段：id,mode,title,status
  ** init.event.request 字段：id,kind,session_id,phase
  ** init.event.message 字段：id,role,raw_text,reply,segments,messages
  ** init.event.message.reply 字段：message_id,sender_id,text,content_text,segments
  ** init.event.message 没有 message.text/message.content_text；读取当前原始文本用 raw_text，读取引用用 reply
  ** 读用户文本时拼接 init.event.message.segments 中 type=text 的片段
  ** init.event.llm 字段：provider,model,messages,tools,usage,raw_text,text,tool_calls,elapsed_ms
  ** init.event.tool 字段：id,name,arguments,risk,result,error
  ** init.event.outputs 是已累计输出意图数组
  ** init.event.error.message 是错误文本
  ** regex 匹配结果在 init.match.regex[0].groups
  ** groups[0] 是完整匹配
  ** groups[1+] 是捕获组
  ** 命名捕获组在 init.match.regex[0].named
}

step exec_python_template {
  ** Python 读取 init：init=json.loads(sys.stdin.readline())
  ** Python 读取 regex groups：groups=init.get("match",{}).get("regex",[{}])[0].get("groups",[])
  ** Python 输出文本：print(json.dumps({"type":"output","outputs":[{"kind":"text","text":text}]},ensure_ascii=False),flush=True)
  ** Python 正常结束：print(json.dumps({"type":"done","result":"ok"},ensure_ascii=False),flush=True)
  ** Python 业务失败：print(json.dumps({"type":"error","error":"原因"},ensure_ascii=False),flush=True) 后正常 return
}

step decisions {
  ** 能用 replace、append、prepend、delete、send、tool 完成时不使用 exec
  ** 需要复杂解析、随机、文件、外部程序或平台 API 时使用 exec
  ** 拦截输入并阻止后续 LLM 时使用 on="platform.message.received" 且 consume=true
  ** 监听未唤起群消息时使用 on="platform.message.received" 且 require_wakeup=false
  ** 普通改写用户输入优先使用 agent.input.prepared
  ** 改写 LLM 回复优先使用 llm.response.received
  ** 只改最终发出的 assistant 文本优先使用 agent.turn.output.prepared
}

~ 使用未列出的 Hook 点、字段、action、segment 字段、request method 或模板变量
~ 修改当前 Hook 点不可编辑的字段
~ 让 Hook 或脚本绕过 Output Manager 直接发送平台消息
~ 把 exec 日志写到 stdout
~ 让 exec 脚本读取 stdin 到 EOF
~ 让 exec 脚本输出 done/error 后以非 0 退出
~ 用 output={...} 或 segments=[...] 代替 outputs=[...]
~ 编造配置字段或旧版 stdout/stdin 字段
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
# Full docs and examples: https://github.com/Elflare/elbot/blob/main/docs/hooks.md
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
# send action with outputs (kind/text/url/path/base64/name/mime_type/user_id/message_id):
# actions = [
#   { type = "send", timing = "after_assistant", outputs = [
#     { kind = "text", text = "检测到关键词" },
#     { kind = "image", path = "alert.png" },
#     { kind = "emoticon", name = "微笑", path = "emoticons/微笑/01.png" },
#     { kind = "at", user_id = "123456" },
#   ] },
# ]
#
# exec action uses hook.v1 line protocol:
# ElBot writes one init JSON frame line to stdin.
# Scripts must read only the first stdin line as init; do not read until EOF.
# Scripts write one JSON frame per stdout line: output/request/done/error.
# stderr is logged on success; on exec failure/crash/timeout/protocol error, the
# stderr tail is included in the Hook failure notice.
# After writing done/error, exit with code 0; non-zero exit means process failure.
# Output frame shape: {"type":"output","outputs":[{"kind":"text","text":"hello"}]}.
# request frame shape: {"type":"request","id":"x","method":"platform.call","params":{...}}.
# response frame shape: {"type":"response","id":"x","ok":true,"result":{...}} or
# {"type":"response","id":"x","ok":false,"error":"..."}.
# done.result is available as {{actions.<name>.result}}.
# done.error is available as {{actions.<name>.error}}.
# done.message.text is written back to the field specified by the action's field setting.
# done.consume and done.stop_propagation set event control flags.
# actions = [
#   { name = "extract", type = "exec", command = "uv run extract.py", field = "llm.text", timing = "after_assistant" },
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
# actor.id/user_id/role/display_name
# session.id/mode/status
# request.id/kind/phase
# message.text/content_text/raw_text/role
# message.reply.message_id/sender_id/text/content_text
# llm.text/raw_text/latest_user_text/latest_user_content_text/provider/model
# tool.name/arguments/result/risk
# error.message
#
# require_wakeup=false on platform.message.received lets a rule observe ordinary
# group messages that did not mention or wake the bot. Hook outputs may still be
# sent, but command/LLM processing only continues for woken messages unless the
# rule consumes the message first.
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
# {{platform.name}}, {{platform.scope_id}}, {{platform.user_id}}
# {{actor.id}}, {{actor.user_id}}
# {{message.text}}, {{message.content_text}}, {{message.raw_text}}
# {{message.reply.message_id}}, {{message.reply.text}}, {{message.reply.content_text}}
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
# Example: emoticon extraction via exec + hook.v1.
# The script reads the init frame from stdin, extracts [[token]] tokens, picks a random
# image from emoticons/<token>/, writes output frames and ends with done.message.text.
# [[rules]]
# name = "emoticon_extract"
# on = "llm.response.received"
# priority = 1000
# if = "llm.text"
# op = "regex"
# value = "\\[\\[[^\\[\\]]+\\]\\]"
# actions = [
#   { name = "extract", type = "exec", command = "uv run emoticon_extract.py", field = "llm.text", timing = "after_assistant" },
# ]
`
