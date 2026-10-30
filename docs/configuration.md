# 配置说明

ElBot 使用一个主配置入口加载应用配置、Provider 配置和运行态状态。除 `.env` 外，所有配置文件均由内置 assets 首次运行时自动生成到平台配置目录，已存在的文件不会被覆盖。

## 配置文件职责

| 文件或目录 | 职责 |
| --- | --- |
| `app.toml` | 主配置：`config_files` 引用、存储、日志、维护任务、平台、安全策略等。 |
| `providers.toml` | Provider 与模型配置。 |
| `state.toml` | 运行态状态：默认 Session 模式、chat/work/compact/naming 模型选择。 |
| `elnis.toml` | Elnis 监听枢纽：HTTP、token、delivery、allowed_tools 和 Elwisp 策略。 |
| `tool_tags.toml` | 工具 tag 与 tag 激活后追加的 prompt。 |
| `SOUL.md` | Agent 的 System Prompt 来源。 |
| `memories.toml` | 常驻记忆。 |
| `long_memory/` | 长期记忆 Markdown 源数据。 |
| `plugins/` | Hook 与插件配置目录（`hooks.toml`、`<plugin>/hook.toml`、`.env`、`_shared/`）。 |
| `skills/` | 用户 Skill 目录，含 `skills/agent/` 与 `skills/go/`；AgentSkill 可在根目录放 `ELBOT_SKILL.toml` 配置可见性或注册为普通工具。 |
| `.env` | 可选本地密钥文件，不建议提交；首次只生成 `.env.example`，不会直接生成 `.env`。 |

以上均可放在平台配置目录，也可用 `--config` 指向其他位置。

## 主配置查找顺序

1. 命令行 `--config`。
2. 环境变量 `ELBOT_CONFIG_FILE`。
3. 平台配置目录：Windows `%APPDATA%/ElBot/app.toml`；Linux 使用 XDG 配置目录。

平台配置不存在时会自动生成默认配置（已存在的文件不覆盖），但自动生成只在没有显式 `--config` 和 `ELBOT_CONFIG_FILE` 时触发；显式指定的路径不存在则报错，避免掩盖路径拼写错误。

## 相对路径规则

相对路径基于主配置文件所在目录解析，默认就是平台配置目录。例如 `app.toml` 写入：

```toml
[config_files]
providers = "providers.toml"
state = "state.toml"
elnis = "elnis.toml"

[soul]
path = "SOUL.md"
```

## 环境变量与进程环境继承

ElBot 的环境层：

| 环境层 | 提供方 |
| --- | --- |
| ElBot 进程环境 | 终端、服务管理器或容器启动时提供 |
| 配置根 `.env` | 本地密钥与普通变量 |
| `plugins/.env` | 所有 Hook 共用的环境层 |
| `plugins/<plugin-id>/.env` | 单个插件 exec 与 Worker |
| `<skill>/.env` | 单个技能专属变量 |

### 配置变量与密钥

`api_key_env`、`token_env`、`client_secret_env`、`access_token_env`、`bot_token_env`、`proxy_url_env` 等字段保存的是环境变量名。

读取顺序：ElBot 进程环境 → 配置根 `.env`。

真实密钥放在系统环境或配置根 `.env`，不要写进 TOML 或提交到仓库：

```dotenv
DEEPSEEK_API_KEY=your-api-key
TELEGRAM_BOT_TOKEN=your-bot-token
ELNIS_HOME_TOKEN=your-elnis-token
```

注意：配置根 `.env` 的全部变量会补充给 LLM Shell。只在信任当前模型及 Shell 权限策略时才启用相关工具。

### Shell、Skill 与 Hook 环境

| 进程入口 | 环境来源与优先级 |
| --- | --- |
| 内置工具（含 LLM Shell） | 进程环境为基础，配置根 `.env` 只补充尚不存在的普通变量。 |
| AgentSkill TOML 命令与 Go Skill | 技能 `.env` > 进程环境 > 配置根 `.env`；技能 `.env` 中的空值也覆盖同名变量。 |
| 根 Hook 规则 | 进程环境为基础，`plugins/.env` 覆盖同名普通变量。 |
| 插件 exec 与 Worker | 根 Hook 环境为基础，插件目录 `.env` 再覆盖同名普通变量。 |

- 技能工作目录是技能根目录；合并结果由 ElBot 传给子进程，脚本无需自行加载 `.env`。
- Go Skill 编译阶段用基础环境，不加载技能 `.env`。
- 配置根 `.env` 不注入 Hook。共用变量放 `plugins/.env`，单插件变量放插件目录 `.env`；一次性 exec、Persistent / Transient Worker 规则相同。
- 需要代理时在对应环境层设 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`；子进程是否使用取决于具体程序。

### PATH 规则与 systemd

`PATH` 不参与普通覆盖，而是按层级追加并去重：

- 基础值是 ElBot 启动时已获得的进程 PATH。
- `.env` 中只写要增加的绝对目录。
- 技能执行顺序：进程 PATH → 配置根 `.env` → 技能 `.env`；AgentSkill 命令也用它查找可执行文件。

```dotenv
PATH=/home/elbot/.local/bin:/usr/local/go/bin
```

不要写 `$PATH`、`%PATH%` 或 `~`——dotenv 不做变量替换或路径展开。没有额外目录时省略 `PATH`。

systemd 两个坑：

- 未显式设 PATH 时用服务管理器提供的 PATH。`EnvironmentFile` 本身不影响 PATH，但文件里一旦含 `PATH=`，systemd 会在启动前完整覆盖原值，ElBot 只能在此基础上追加，无法恢复已丢失的系统目录；`EnvironmentFile` 也不展开 `$PATH`。
- 由 ElBot 自行读取配置根 `.env` 时，unit 不必重复加载该文件（PATH 按追加规则）。若 unit 用 `EnvironmentFile` 加载同一文件，PATH 必须写成完整值，且其中全部变量都成为进程环境、被 Shell 和 Hook 继承，失去“配置根 `.env` 不注入 Hook”的隔离。

### 生效时机

- 配置根 `.env`、systemd 环境：重启后生效。
- `plugins/.env`、插件 `.env`：启动及 `/hooks reload` 时重读，reload 按新环境重建 Worker。
- 文件不存在视为空配置。

## Workspace 工具

work 模式下，超级管理员可让 LLM 调用 `workspace` 工具切换当前 Session 的共享工作目录。切换后 `read_file`、`edit_file`、`rollback_file`、`send_file` 和前台 `shell` 等路径类工具按该目录解析相对路径。

- 未设置 workspace 时沿用进程工作目录。
- 目标目录根部有 `AGENTS.md` 或 `AGENT.md` 时，首次发现、注入或首次切换到该目录会自动把内容附带给 LLM。文件名主体必须大写 `AGENTS` / `AGENT`，`.md` 后缀大小写不限，`AGENTS.md` 优先。
- 附带上限 64 KiB。超限不读取、不标记已附带，工具结果提示缩短或拆分后重新切换。
- 编辑撤销备份按目标绝对路径保存：切换 workspace 不清空备份，但相同相对路径按新 workspace 解析；撤销原目录文件可切回或传绝对路径。见[文件编辑撤销](commands.md#文件编辑撤销)。

`read_file` 工具说明：

- `ast` / `ast_function` 模式支持单文件和目录查询。
- 方言判定：`.go` 按 Go，`.mksh` 按 mksh；其余看首行 Shell shebang（含 `env`、`env -S`、`env --split-string` 写法）。
- `.sh`、`.bash`、`.bats` 无可识别 shebang 时按 Bash。

`edit_file` 确认前生成编辑结果和差异预览，确认后提交同一份结果；等待期间文件内容或目标变化会拒绝提交，需重新准备。

## 常驻记忆配置

常驻记忆存放在配置目录的 `memories.toml`（首次运行时生成）。长度上限写在主配置：

```toml
[resident_memory]
core_max_units = 200
normal_max_units = 300
```

| 类型 | 含义 | 修改确认 |
| --- | --- | --- |
| `core` | 高风险核心记忆 | 普通用户改自己的 core 也需本人确认。 |
| `normal` | 低风险普通记忆 | 无需确认。 |

- `units` 近似“中文按字数、英文按单词”：中日韩字符单字计 1，英文/数字连续片段计 1。
- 注入 Prompt 时两段合并为一段自然文本。

## Provider 配置

Provider 写在 `providers.toml`：

```toml
[providers.deepseek]
api_mode = "chat"                  # 可选：chat / response，省略为 chat
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"
proxy = ""                          # 可选，HTTP/SOCKS5 代理地址
extra_payload = { provider_field = "xxx" }  # 可选，Provider 级原生参数

[providers.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
models = ["gpt-4o-mini"]             # 手动补充模型列表（API 获取不到时用）

# [providers.openai.model_configs."gpt-4o-mini"]
# context_window = 128000
# extra_payload = { }

[model_metadata]
default_context_window = 256000
```

| 字段 | 说明 |
| --- | --- |
| `api_mode` | `chat`（默认）或 `response`，分别对应 Chat Completions 与 Responses API。 |
| `base_url` | Provider API 基础地址。 |
| `api_key_env` | 指向环境变量名（推荐用此方式保存密钥）。 |
| `proxy` | 支持 `http://` 与 `socks5://`；省略或留空时直连，**不继承**进程的 `HTTP_PROXY` / `HTTPS_PROXY`。同时作用于模型列表和聊天请求。 |
| `models` | 手动补充的模型名列表，用于模型列表接口获取不到的情况。 |
| `model_configs."<model>"` | 为单个模型配 `context_window` 和 `extra_payload`，均可选。 |
| `extra_payload` | Provider 级与模型级共用，按 `api_mode` 填该 API / 模型支持的原生参数，最终合并进 LLM 请求 JSON。 |
| `[model_metadata].default_context_window` | 全局回退值，默认 `256000`；`model_configs` 未配 `context_window` 时用它。 |

### Responses 补充说明

```toml
[providers.native_text]
api_mode = "response"
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
extra_payload = { store = false, reasoning = { effort = "medium" } }
```

- `store`：Provider 或模型级 `extra_payload` 可用 `store = false` 关闭服务端存储，之后发送完整原生历史。默认用服务端续链；上游返回 `store:false` 时自动切换并后台记 WARN。
- 工具定义走原生输入的 `additional_tools`，当前调用权限由 `tool_choice.allowed_tools` 限定，无可用工具时发 `none`。上游必须支持这两项，没有顶层 `tools` 回退。`extra_payload.tool_choice` 只能进一步收窄权限，强制调用不可用工具或无工具时设 `required` 会报错。
- 会话使用带版本的原生输入格式。缺少格式标记的旧会话不能继续对话、Fork 或压缩，需新建。同一原生历史中的工具定义固定，同名 schema 变化也需新建。
- 压缩：Responses 会话用当前对话模型原生压缩，不使用 `/model --compact` 的选择。命名与 Chat 会话文字摘要则两个客户端都可用。

### 会话协议锁定

已有会话的协议和厂商在首次对话时固定。厂商即 `[providers.<name>]` 的节点名。

| 会话类型 | 可切换范围 |
| --- | --- |
| Chat | 任意 Chat 厂商 |
| Responses | 同一节点下的 Responses 模型 |

跨协议或跨 Responses 厂商需新建会话。同一节点 `base_url` 变化时，Responses 用完整本地原生材料重建链。

## 内置 Web 工具配置

| 工具 | 环境变量 | 行为 |
| --- | --- | --- |
| `web_search` | `TAVILY_API_KEY` | 必需。 |
| `web_extract` | `JINA_API_KEY` | 配置后走 Jina Reader，未配置回退为直接抓取。 |
| `web_extract` | `WEB_EXTRACT_PROXY` | 所有默认 `web_extract` 调用的固定代理，例如 `http://127.0.0.1:7890`。 |

`web_extract` 的 `proxy` 参数用于控制单次网页提取请求的代理。

## LLM 请求与整轮超时

`app.toml` 的 `[llm_request]` 控制流式请求、整轮处理和重试：

```toml
[llm_request]
first_chunk_timeout_seconds = 180
stream_idle_timeout_seconds = 60
response_timeout_seconds = 0
max_retries = 3
retry_initial_delay_seconds = 2
```

| 字段 | 说明 |
| --- | --- |
| `first_chunk_timeout_seconds` | 从响应开始到首个流式事件的等待上限，默认 180；适合首字较慢的模型。 |
| `stream_idle_timeout_seconds` | 流式过程中两次事件间的静默上限，默认 60；每收到新事件重新计时。 |
| `response_timeout_seconds` | 整轮请求总时长，从收到输入到最终回复结束，默认 0 不限时；设正数后到时停止本轮并提示用户。单次 LLM 流式请求不受它限制。 |
| `max_retries` / `retry_initial_delay_seconds` | 建连或 HTTP 可重试失败的重试次数与首次延迟，延迟按指数退避增长。 |

## CLI 远程配置

`[platform.cli]` 同时保存服务端配置和客户端连接配置：`server` 是当前 ElBot 作为服务端时读取的配置，`clients` 是客户端连接服务端时用的一组连接配置。

```toml
[platform.cli]
enabled = true
default_client = "local"
default_url = "ws://127.0.0.1:32172/cli/v1/ws"

[platform.cli.server]
enabled = false
listen = "127.0.0.1:32172"

[platform.cli.server.tokens]
local = ["ELBOT_CLI_LOCAL_TOKEN"]
windows = ["ELBOT_CLI_WINDOWS_TOKEN"]

[platform.cli.clients.local]
token_env = ["ELBOT_CLI_LOCAL_TOKEN"]

[platform.cli.clients.windows]
url = "ws://192.168.1.10:32172/cli/v1/ws"
token_env = ["ELBOT_CLI_WINDOWS_TOKEN"]
```

- `server.enabled=true` 时，`elbot service run` 会启动 CLI WebSocket 服务端，监听 `server.listen`。
- `server.tokens` 是允许登录的 CLI client id 与 token 环境变量列表。
- `clients.<name>` 是连接配置：`id` 可省略（默认等于 `<name>`），`url` 可省略（默认用 `default_url`）；连其他机器时写远程 WebSocket 地址。
- `elbot client -c <name>` 使用指定连接配置，未指定时用 `default_client`。

## AgentSkill 工具化配置

AgentSkill 默认只作为说明文档使用，按文档执行脚本的风险由实际调用的 `shell` 等工具承担。

要限制文档型 Skill 的可见性，或把 `skills/agent/<skill>/` 注册成普通工具，在该 Skill 根目录加 `ELBOT_SKILL.toml`：

- 未写 `command` / `parameters` / `[args]`：只作可见性配置，不注册成工具。
- 写了任一工具化字段：必须补齐完整工具化配置。

只限制可见性：

```toml
risk = "high"
superadmin_only = true
```

注册成普通工具：

```toml
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
    "input": {"type": "string", "description": "输入文本"}
  }
}
'''

[args]
input = "--input"
```

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `risk` | 注册成工具时必填 | `safe`、`low`、`medium`、`high`、`critical`；文档型未写按 `safe` 处理。 |
| `superadmin_only` | 否 | `true` 时只有超级管理员能发现、预载或调用。 |
| `tags` | 否 | 工具分类，用于 `@tool:<tag>` 预载。 |
| `command` | 工具化必填 | 命令数组，不用 shell 字符串。 |
| `parameters` | 工具化必填 | JSON object schema，决定 LLM 看到的参数。 |
| `[args]` | 工具化必填 | 扁平参数映射：`input = "--input"` 把参数 `input` 译为 `--input <value>`。 |
| `timeout_seconds` | 否 | 命令超时时间。 |
| `expose_root` | 否 | 默认 `false`；`true` 时发现该 Skill 会暴露其根路径。 |

- ElBot 只读 Skill 根目录下的 `ELBOT_SKILL.toml`，不递归扫描。
- 执行时工作目录固定为 Skill 根目录，stdout 作为工具结果；若 stdout 是 `{"content":"..."}` JSON，取 `content` 字段。
- 媒体参数在 `parameters.properties` 声明为 `{"type":"media"}`，仍通过 `[args]` 映射 flag；LLM 传 `media:<sha256>`，宿主执行时导出为相对 Skill 根目录的临时文件路径。未声明为媒体的字符串不转换。stdout 也支持 `segments`，格式与生命周期见 [Skill 使用媒体](concepts.md#skill-使用媒体)。
- 通过 `agent_skill` 元工具写入时，完整 reload 成功后才确认；reload 失败会恢复原 TOML，原先没有该文件则删除本次新建文件。

## Go Skill 编译器路径

修改 Go skill 的 `code_source` 后，ElBot 自动执行 `gofmt`、`go build` 并 reload。可执行文件查找顺序：

1. 系统环境变量 `ELBOT_GO_BINARY`
2. 配置目录 `.env` 中的 `ELBOT_GO_BINARY`
3. `GOROOT/bin/go`（service 环境配了 `GOROOT` 时）
4. 进程 `PATH` 中的 `go`

推荐直接在配置目录 `.env` 指定：

```dotenv
ELBOT_GO_BINARY=/usr/local/go/bin/go
```

用 asdf、mise、Nix、Linuxbrew、Snap 或自定义安装路径时，把实际 `go` 路径写入 `ELBOT_GO_BINARY`，不要依赖交互 shell 的初始化脚本。也可在 systemd service 中指定：

```ini
[Service]
Environment=ELBOT_GO_BINARY=/usr/local/go/bin/go
```

## Session 配置

`app.toml` 中的 `[session.idle_expiration]` 控制当前 Session 的闲置过期时间，单位为分钟：

```toml
[session.idle_expiration]
group_user_ttl_minutes = 10
group_superadmin_ttl_minutes = 10
private_user_ttl_minutes = 10
private_superadmin_ttl_minutes = 0
```

| 字段 | 作用 |
| --- | --- |
| `group_user_ttl_minutes` | 群聊普通用户 |
| `group_superadmin_ttl_minutes` | 群聊超级管理员 |
| `private_user_ttl_minutes` | 私聊普通用户 |
| `private_superadmin_ttl_minutes` | 私聊超级管理员 |

- 单位分钟；设为 `0` 禁用对应场景的闲置过期。
- 默认下群聊两种角色、私聊普通用户都是闲置 10 分钟开启新 Session，私聊超级管理员不过期。
- 这里的“超级管理员”指 `[security.superadmins]` 中配置的 ElBot 超级管理员；平台群主或群管理员不在该列表中时仍按普通用户处理。

## 模型状态配置

`state.toml` 保存运行态模型选择（三个 elwisp 槽位结构相同）：

```toml
[session]
default_mode = "work"

[mode_models.work]
provider = "deepseek"
model = "deepseek-chat"

[mode_models.chat]
provider = "deepseek"
model = "deepseek-chat"

[mode_models.elwisp1]
provider = "deepseek"
model = "deepseek-chat"

[mode_models.elwisp2]
provider = "deepseek"
model = "deepseek-chat"

[mode_models.elwisp3]
provider = "deepseek"
model = "deepseek-chat"
```

- `default_mode`：新 Session 默认进入 `chat` 还是 `work`。两种模式的区别见 [Chat / Work 双模式](concepts.md#chat--work-双模式)。
- `elwisp1/2/3`：Elnis LLM 事件的可选模型槽位，Elvena 请求通过 `model_slot` 指定，未配置时回退到 `work`。
- 运行时 `/model` 切换会同步保存到 `state.toml`。
- `state.toml` 只在启动时读取，运行中手动修改需重启生效。

## 存储与运行数据

`app.toml` 中 storage 相关配置：

```toml
[storage]
sessions_sqlite_path = ""
chat_history_sqlite_path = ""
```

留空时使用平台默认数据目录（Windows `%APPDATA%/ElBot/data`，Linux `$XDG_DATA_HOME/elbot` 或 `~/.local/share/elbot`）；运行日志、SQLite、sandbox 也按配置或默认目录存放。

平台入站附件下载限制用 `[platform_files]`：

```toml
[platform_files]
max_receive_file_bytes = 104857600
download_timeout_secs = 60
```

- `max_receive_file_bytes`：媒体接收上限，默认 100MB；超限或无法取得时提示媒体不可用。
- `download_timeout_secs`：媒体下载超时，默认 60 秒；Telegram 用平台 API 超时。
- 平台媒体在实际处理时才下载，不会收到即存 sandbox。纯文件消息也按统一唤醒规则处理。

### 媒体交付

`[file_delivery]` 控制**模型请求**中的媒体交付，统计本次完整请求的媒体原始字节总量（同一媒体出现多次累加，但读取、上传、签名按媒体 ID 复用）。

`send_file` 不用此限制：它只检查本地路径、普通文件类型、文件名和 MIME。向聊天平台发送文件的方式由相应平台配置控制。

| `backend` | 行为 |
| --- | --- |
| `base64` | 总量超过 `max_direct_base64_bytes` 时停止请求，报告实际总量和上限。 |
| `hybrid` | 未超限全用 base64，超限全用 S3。 |
| `s3` | 全用预签名下载链接；本地媒体按需上传并复用已记录的对象键。配置、上传或签名失败会明确报错，不回退成混合交付。 |

- S3 使用 `s3_endpoint`、`s3_region`、`s3_bucket` 和 `s3_access_key_env`、`s3_secret_key_env` 指定的变量。
- 模型服务通过有效期 1 小时的预签名 URL 下载，无需另给密钥。
- Cloudflare R2 可保持 bucket 私有，无需开公共访问；下载方或中转服务必须能访问该链接。
- 媒体中心在所有引用释放满 1 小时后，按 `[maintenance.sandbox_cleanup].schedule`（默认每天 04:00）清理对象，且独立启用、不受 sandbox 清理开关影响；删除失败保留记录待重试，仍被 Session 或聊天历史引用的不删。预签名 URL 到期只影响链接，不删对象。

`[media]` 控制图片入库压缩：

- `llm_image_compression_threshold_bytes`：触发压缩的原图字节数阈值，默认 4 MiB。
- `llm_image_max_length`：压缩后的边长限制，默认 4096。
- 原图超阈值或任一边长达到限制时等比例压缩。


## 日志与维护任务

```toml
[runtime]
log_level = "info"
log_retention_days = 30
```

- `runtime.log_level` 只控制运行日志，支持 `debug`、`info`、`warn`、`error`。
- 审计和 Elnis 日志独立保留，即使提高运行等级也会继续记录 `/usage` 所需的用量事实。
- 日志文件按日轮转，前缀为 `elbot-`、`audit-`、`elnis-`，保留天数由 `log_retention_days` 控制。
- 查询等级与记录等级独立：`/log -d` 只显示已有记录，不改配置、不补回未记录内容。筛选方式见[日志和审计命令](commands.md#日志和审计)。
- 信号派发、后台信号任务和日志写入等设施故障直接报告到 stderr（不受日志等级过滤）；运行期报告后继续运行，初始化失败则返回主程序处理。

维护任务使用 Linux crontab 风格的 5 字段 Cron：`分钟 小时 日 月 星期`，由内部 Cron Runtime 调度。

| 任务 | 默认 enabled | 默认 schedule | 默认 retention_days |
| --- | --- | --- | --- |
| `[maintenance.log_cleanup]` | `true` | `0 3 * * *` | — |
| `[maintenance.session_cleanup]` | `false` | `15 3 * * *` | 30 |
| `[maintenance.sandbox_cleanup]` | `true` | `0 4 * * *` | 7 |
| `[maintenance.chat_history_cleanup]` | `true` | `35 4 * * *` | 180 |

```toml
[maintenance.sandbox_cleanup]
enabled = true
schedule = "0 4 * * *"
retention_days = 7
```

## Context 与压缩

```toml
[context]
compact_enabled = true
compact_trigger_ratio = 0.8
```

- 开启后，上下文接近窗口上限时自动压缩；也可用 `/compact` 手动触发。
- 压缩成功后切换到独立的新 Session，不修改原 Session 的历史。
- 两种协议共用上述开关、阈值和当前模型的窗口配置。Responses 调用当前对话模型的 `/responses/compact`，以整个原生返回窗口作为新会话起点。

模型窗口在 `providers.toml` 的 `model_configs` 中配置：

```toml
[providers.deepseek.model_configs."deepseek-chat"]
context_window = 64000

[model_metadata]
default_context_window = 256000
```

- `[model_metadata].default_context_window` 是全局回退值，默认 `256000`，模型块里没配 `context_window` 时使用。

## 命令前缀

```toml
[commands]
prefixes = ["/"]
```

默认使用 `/`。如果要支持其他命令前缀，可以在这里添加。

## 工具与安全

```toml
[config_files]
tool_tags = "tool_tags.toml"

[tools]
max_rounds_per_turn = 10

[security]
user_max_tool_risk = "low"
superadmin_confirm_risk = "high"

[security.superadmins]
cli = ["local"]
```

- 普通用户只能发现和调用允许风险范围内的工具；工具通过权限校验后，`high`/`critical` 风险调用还需要当前用户确认。
- `OwnerScoped` 工具只能访问调用者自己的数据，因此普通用户可以调用；其中 `high`/`critical` 风险操作仍需本人确认。
- 超级管理员的确认阈值由 `superadmin_confirm_risk` 配置。
- CLI 默认本地用户 `local` 是超级管理员。
- `tool_tags.toml` 用来配置 `@tool:<tag>` 可注入的工具组，以及 tag 激活后追加到 system prompt 的工具使用策略。

### `tool_tags.toml`

`tool_tags.toml` 是独立配置文件，路径由 `[config_files].tool_tags` 指定。相对路径以 `app.toml` 所在目录为基准：

```toml
[config_files]
tool_tags = "tool_tags.toml"
```

文件格式以 tag 为入口：

```toml
[tags.agent]
tools = ["read_file", "edit_file", "shell"]
prompt = """
ROLE:
- The goal is to complete the user's task safely and accurately.

MUST:
- Inspect relevant files before editing.
- Prefer minimal, verifiable changes.
- Evaluate command safety before running shell commands.
"""
```

字段说明：

- `[tags.<tag-name>]`：定义一个可在聊天里使用的 tag，例如 `[tags.agent]` 对应 `@tool:agent` 或简写 `@t:agent`。
- `tools`：这个 tag 会预载的工具名列表。工具名必须是已注册且当前用户有权限访问的工具。
- `prompt`：这个 tag 成功激活后追加到 system prompt 的工具使用策略。内容会直接给模型看，不会自动添加 tag 名标题。

使用方式：

```text
@tool:agent 帮我检查这个项目的问题
@t:agent 帮我检查这个项目的问题
```

这会把 `agent` 下配置的工具预载到当前 Session。如果 `prompt` 非空，也会从本轮开始追加到 system prompt。

注意事项：

- 配置 tag 会追加到内置 tag，不覆盖内置 tag。
- 只有 `@tool:<tag>` 或 `@t:<tag>` 成功命中至少一个工具后，当前 Session 才会激活该 tag 的 prompt。
- 直接 `@tool:<tool-name>` 或 `@t:<tool-name>` 只预载指定工具，不激活 tag prompt。
- 激活的 tag 会写入 Session metadata，`/resume` 后仍生效。
- prompt 文本从 `tool_tags.toml` 动态读取；文件变更后影响后续请求，行为类似 `SOUL.md`。
- 重复预载已经存在的工具时不会重复添加，平台会提示 `已存在工具：<name>`。
- 建议把 `prompt` 写成具体工具使用策略，不要写“当前 tag 是 xxx”这类模型不需要知道的配置机制。

## Elnis 监听枢纽

Elnis 默认关闭。启用后，ElBot 会启动本地 HTTP ingress，接收 Elwisp 按 Elvena 协议投递的事件。Elnis 配置建议拆到独立 `elnis.toml`，`app.toml` 只保留入口路径。

```toml
[config_files]
elnis = "elnis.toml"

# elnis.toml
enabled = true
allowed_tools = ["shell", "web_search"]

[http]
addr = "127.0.0.1:32170"
max_body_bytes = 1048576
queue_size = 128
workers = 2

[tokens.home]
token_env = ["ELNIS_HOME_TOKEN", "ELNIS_HOME_TOKEN_ALT"]

[delivery_disabled]
targets = [
  # { platform = "telegram" },
  # { platform = "telegram", type = "private", id = "123456789" },
  # { platform = "qqonebot", type = "group", id = "987654321" },
]

[elwisps.server-watchdog]
allowed_tokens = ["home"]
allowed_tools = ["shell"]
disabled_external_tools = ["danger_tool"]
disabled_targets = [
  # { platform = "qqonebot", type = "group", id = "987654321" },
]
```

说明：

- `allowed_tools` 是 Elnis 内部工具白名单；未单独配置的 Elwisp 继承全局默认。
- 单个 Elwisp 若配置 `allowed_tools`，会覆盖全局默认。
- 外部工具默认允许；只有单个 Elwisp 配置 `disabled_external_tools` 时才禁用指定外部工具。
- Elnis 投递默认允许；`[delivery_disabled].targets` 和单 Elwisp `disabled_targets` 用于显式禁止平台、私聊或群聊，配置中的 platform-only 表示禁用整个平台所有投递。
- Elnis 日志只记录 token name，不记录 token 原文。
- `token_env` 支持写成列表，按顺序尝试多个环境变量名；适合临时切换 token 或做多环境兼容。
- Elwisp 默认启用；只有显式配置 `enabled=false` 才会禁用对应 Elwisp。
- 当前支持 `record`、`direct` 和 `llm` 模式；`llm` 模式使用后台 Session runner 执行。
- `llm` 模式可在 Elvena 请求中指定 `model_slot` 为 `elwisp1`、`elwisp2` 或 `elwisp3`；未指定或对应槽位未配置时回退到 `work` 模型。
- `direct` 和 `llm` 报告只支持按 Elnis 裁决后的平台发送给 superadmins，不支持任意 user/group 目标。
- Elvena 请求中的 `tools` 进入校验、持久化和执行链路；外部工具名仍需由单个 Elwisp 的禁用列表控制。

更多说明见 [Elnis 配置与使用](elnis-usage.md)。

## 平台配置

CLI 默认启用：

```toml
[platform.cli]
enabled = true
```

QQ 官方机器人、QQ OneBot 和 Telegram 配置在示例中默认注释。启用时需要补齐平台自己的认证信息和触发关键词。媒体在实际处理时按 `[platform_files]` 限制下载。

QQ 官方机器人最小配置示例：

```toml
[platform.qqofficial]
enabled = true
app_id = "your-app-id"
client_secret_env = "QQOFFICIAL_CLIENT_SECRET"
trigger_keywords = ["bot"]
```

对应的配置目录 `.env`：

```dotenv
QQOFFICIAL_CLIENT_SECRET=your-client-secret
```

`client_secret_env` 指向保存 Client Secret 的环境变量名；也可以用 `client_secret` 直接写入配置，但不建议提交真实 Secret。

QQ 官方机器人会处理私聊、群内 @ 和平台实际下发的普通群消息。群聊沿用统一唤醒规则：slash 命令、`trigger_keywords`、@ 机器人或引用机器人的历史回复会触发响应；未触发的普通群消息仍会写入聊天历史。普通群消息是否下发取决于 QQ 开放平台为机器人启用的群消息能力。

QQ OneBot 最小配置示例：

```toml
[platform.qqonebot]
enabled = true
ws_url = "ws://127.0.0.1:6700/"
access_token_env = "QQONEBOT_ACCESS_TOKEN"
api_timeout_seconds = 15 # OneBot 写入和响应等待的基础超时
trigger_keywords = ["bot"]
send_file_mode = "base64" # 本地图片、文件、语音默认用 base64；共享文件系统可改为 file_uri
```

`access_token_env` 指向保存 Access Token 的环境变量名；原有 `access_token` 仍然兼容且优先于 `access_token_env`。OneBot 不要求鉴权时，两项都可省略。

`send_file_mode` 同时控制 QQ OneBot 本地图片、文件和 `record` 语音的发送方式。`base64` 适用于 ElBot 与 OneBot 不共享文件系统的部署；`file_uri` 仅适用于双方能访问同一本地路径的场景。

`api_timeout_seconds` 分别作为 OneBot 帧写入和 API 响应等待的基础超时。大帧写入会按编码后大小每完整 1 MiB 增加 1 秒，最多增加到基础超时本身；写入失败或超时后 ElBot 会断开并重连 OneBot，避免一个慢发送长期占住后续消息。发送仍会同步等待平台回执。

Telegram 使用 Bot API long polling。最小配置示例：

```toml
[security.superadmins]
cli = ["local"]
telegram = ["123456789"]

[platform.telegram]
enabled = true
bot_token_env = "TELEGRAM_BOT_TOKEN"
proxy_url_env = "TELEGRAM_PROXY_URL" # 可选
trigger_keywords = ["bot"]
format = "html" # html/plain/rich
stream_edit_interval_milliseconds = 250
```

说明：

- `bot_token_env` 指向保存 Bot Token 的环境变量名；也可以用 `bot_token` 直接写入配置，但不建议提交真实 token。
- `proxy_url_env` 指向保存代理地址的环境变量名；也可以用 `proxy_url` 直接写入配置。代理地址示例：`http://127.0.0.1:7890`、`socks5://127.0.0.1:1080`。
- `format="html"` 是默认值：使用普通 `sendMessage` + `parse_mode="HTML"`，并把常见 Markdown 轻量转换成 Telegram HTML；支持标题、引用、分割线、代码块和表格的可读渲染。
- `format="plain"` 关闭格式化，只发送纯文本。
- `format="rich"` 是实验模式：使用 `sendRichMessage` / 私聊 `sendRichMessageDraft`；部分客户端可能无法查看 Rich Message。
- `stream_edit_interval_milliseconds` 控制流式刷新节流间隔，默认 250ms，避免触发平台限频。
- 启动连接成功后，ElBot 会把内置 slash 命令同步到 Telegram bot 命令菜单；只同步主命令名，不同步 alias。
- 群聊/超级群组中，命令前缀、触发关键词、`@bot_username` 或回复 bot 消息都会触发处理。私聊默认处理。
- 高风险工具确认消息会附带 Telegram inline keyboard，点击按钮会转换为 `/confirm`、`/reject` 等现有确认命令。
- `security.superadmins.telegram` 填 Telegram 用户 ID 或可直接发送的私聊 chat ID，用于超级管理员权限与通知投递。


## 插件和 Hook 配置

插件配置固定放在配置目录的 `plugins/` 下：

- `plugins/hooks.toml`：规则 Hook 配置。
- `plugins/<plugin-id>/hook.toml`：被 `hooks.toml` 引用的插件 Hook；可包含 `[plugin.runtime]` 持久运行配置。
- `plugins/_shared/`：ElBot 创建的跨 Hook 文件协作目录，不作为插件扫描。

Hook 不要直接发平台消息，应返回输出意图，由 Agent 统一交给 Output Manager 发送。

规则与持久 Hook 的完整配置说明见 [Hook](hooks.md)。

## 配置诊断

超级管理员可以在聊天窗口执行 `/doctor`。命令只读检查当前服务实际使用的主配置及其 `config_files` 指向的分离配置，支持自定义主配置文件名、相对路径和绝对路径；`SOUL.md` 按 `soul.path` 定位。

检查范围：

- 对默认资产清单中的文件检查是否缺失。
- 对配置 TOML 检查格式、重复定义和字段类型；内置 Skill 的 `ELBOT_SKILL.toml` 检查 TOML 格式。
- `app.toml` 会检查未知字段置
- 内置 `SKILL.md` 及配套 `ELBOT_SKILL.toml` 检查内容差异；用户自己添加的 Skill 不参与比较。
- SOUL、记忆和环境文件只检查存在性。

`/doctor` 只在已启动的聊天服务中可用。如果配置错误导致 ElBot 无法启动，需要先根据启动输出修正错误。

## 建议的维护方式

- 用户可编辑配置集中放在平台配置目录，避免直接改源码示例。
- 新增配置项时同步更新本文档。
- 改变默认路径或启动行为时同步更新 [快速开始](getting-started.md) 和 README。
