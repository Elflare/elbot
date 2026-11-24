---
name: agent_skill_creator
description: 编写 AgentSkill 说明，按需用 ELBOT_SKILL.toml 注册命令。
---

在配置目录的 `skills/<name>/` 写 `SKILL.elyph` 或 `SKILL.md`；两者都存在时，优先加载 `SKILL.elyph`。

纯文档不需要命令。要把脚本或可执行文件注册成工具，在同一目录写 `ELBOT_SKILL.toml`，用 `command` 指定程序和固定参数，例如 `["uv", "run", "main.py"]`、`["python3", "main.py"]` 或 `["./mytool"]`。创建或编辑完文件后，用 `/tools reload` 检查并加载；已加载的 Skill 也可以用 `agent_skill` 读写其 TOML。

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

positional = ["input"]

parameters = '''
{
  "type": "object",
  "required": ["input"],
  "properties": {
    "input": {"type": "string", "description": "输入文本"},
    "mode": {"type": "string", "description": "处理模式"},
    "items": {"type": "array", "items": {"type": "string"}, "description": "输入项列表"},
    "options": {"type": "object", "description": "附加选项"}
  }
}
'''

[args]
mode = "--mode"
items = "--items"
options = "--options"

含义：
positional 中的参数按声明顺序直接传入；[args] 参数以 flag/value 对传入。
工具调用 {"input":"abc","mode":"fast","items":["a","b"],"options":{"dry_run":true}} 会按参数名顺序执行：
python foo.py abc --items ["a","b"] --mode fast --options {"dry_run":true}
数组和对象会压缩为 JSON，并各自作为 flag 后的一个 argv 参数传入，不经过 shell。

command 必须是字符串数组。
parameters 必须是 JSON object schema。
parameters.properties 定义工具有哪些入参；positional 和 [args] 的 key 必须对应 parameters.properties，不能重复映射。positional 按数组顺序生成位置参数；[args] 生成按参数名排序的 flag/value 对。必填参数必须映射。
risk 可选；注册成普通工具时必填。纯文档型未写 risk 时按 safe 处理。
superadmin_only 可选，true 时只有超级管理员可发现或使用。
tags 可选，相当于为该工具分类。

需要媒体的 AgentSkill 才在其说明中加入以下约定：
- 普通 AgentSkill 使用稳定的 media:<64位小写SHA-256>，将它传给支持媒体的工具。
- bash 脚本通过 shell 的 media_inputs 显式声明输入，例如 {"cmd":"python generate.py --input \"$ELBOT_MEDIA_1\"","media_inputs":[{"media":"media:<sha256>"}]}。PowerShell 使用 $env:ELBOT_MEDIA_1。不要硬编码路径。
- TOML 工具的单个媒体参数声明为 {"type":"media"}；多个媒体声明为 {"type":"array","items":{"type":"media"}}，可通过 positional 生成位置参数或由 [args] 映射 flag。LLM 传媒体 ID，进程收到相对 Skill 根目录的临时路径；媒体数组作为保留顺序和重复项的紧凑 JSON 数组放在一个 argv 中。普通 string 或 string 数组不自动转换。调用结束会清理输入的临时副本和临时引用。
- 工具 stdout 可以返回 {"content":"完成","segments":[...]}。segments 支持 text、image、file；text 使用 text 字段，image/file 必须且只能使用 media 或 path，另可提供 name、mime_type。media 必须是稳定媒体 ID；path 必须是 Skill 根目录内的相对路径，禁止绝对路径、.. 和链接逃逸。
- 宿主会把 stdout path 指向的文件导入 Media Center，并在工具结果中替换为稳定媒体 ID。源文件不会自动删除；需要保留或清理由 Skill 自己决定。stdout 输出前不得删除该文件，因为宿主会在进程退出后读取并导入。
