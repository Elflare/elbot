# 配置说明

ElBot 使用一个主配置入口加载应用配置、Provider 配置和运行态状态。默认配置由程序内置 assets 生成到平台配置目录；已有配置文件不会被覆盖。

## 配置文件职责

源码中的 `config/` 目录只保留可独立维护的示例/辅助配置文件；主配置 `app.toml` 首次运行时由程序内置 assets 生成到平台配置目录。

| 文件或目录 | 职责 |
| --- | --- |
| `config/elnis.toml` | Elnis 监听枢纽配置，保存 HTTP、token、delivery、allowed_tools 和 Elwisp 策略。 |
| `config/state.toml` | 运行态状态，例如默认 Session 模式、chat/work/compact/naming 模型选择。 |
| `config/tool_tags.toml` | 给工具添加 tag 和 prompt 的配置文件。 |
| `config/SOUL.md` | Agent 的 System Prompt 来源文件。 |
| `config/.env` | 可选，本地密钥文件，不建议提交；首次自动生成的是 `.env.example`，不会直接生成 `.env`。 |
| `config/plugins/` | Hook 和插件配置目录。 |
| `config/skills/` | 用户侧 Skill 目录，默认位于配置目录下；当前子目录为 `skills/agent/` 和 `skills/go/`。 |
| `config/memories.toml` | 常驻记忆文件，默认位于配置目录下。 |
| `config/long_memory/` | 长期记忆 Markdown 源数据目录，默认位于配置目录下。 |

## 主配置查找顺序

启动时主配置按以下顺序查找：

1. 命令行 `--config`。
2. 环境变量 `ELBOT_CONFIG_FILE`。
3. 平台配置目录：Windows `%APPDATA%/ElBot/app.toml`；Linux 使用 XDG 配置目录。
4. 若平台配置不存在，则自动在平台配置目录生成默认配置文件。

自动生成的默认配置内容来自程序内置 assets，且已有文件不会被覆盖。自动生成只在没有显式 `--config` 和 `ELBOT_CONFIG_FILE` 时触发。若显式指定的配置路径不存在，ElBot 会报错而不是偷偷生成，避免掩盖路径拼写错误。

首次自动生成的文件包括：`app.toml`、`providers.toml`、`state.toml`、`SOUL.md`、`memories.toml`、`elnis.toml` 和 `.env.example`；同时会创建 `skills/`、`skills/agent/`、`skills/go/`、`plugins/` 和 `long_memory/` 目录。已有文件不会被覆盖。`elnis.toml` 默认 `enabled=false`，不会在首次运行时启动 HTTP 监听。


开发期直接运行即可使用平台配置目录；首次运行会自动生成默认配置：

```bash
go run ./cmd/elbot
```

如需使用临时配置文件，也可以显式指定 `--config`。

## 相对路径规则

相对路径默认基于主配置文件所在目录解析。

例如平台配置目录下的 `app.toml` 写入：

```toml
[config_files]
providers = "providers.toml"
state = "state.toml"
elnis = "elnis.toml"

[soul]
path = "SOUL.md"
```

这些路径都会解析到主配置文件所在目录下；默认情况下就是平台配置目录。

## 常驻记忆配置

常驻记忆数据默认保存在配置目录的 `memories.toml`，文件由程序首次运行时生成。主配置中可以设置 core/normal 两段常驻记忆的长度上限：

```toml
[resident_memory]
core_max_units = 200
normal_max_units = 300
```

长度单位 `units` 可以近似理解为“中文按字数、英文按单词”：中日韩字符按单字计数，英文/数字连续片段按一个词计数。core 用于需要高风险确认的核心记忆，normal 用于可整理的普通记忆；注入 Prompt 时两段会合并成一段自然文本。

## Provider 配置

Provider 写在 `providers.toml`：

```toml
[providers.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"
proxy = ""                          # 可选，HTTP/SOCKS5 代理地址
extra_payload = { provider_field = "xxx" }  # 可选，Provider 级 extra payload

[providers.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
models = ["gpt-4o-mini"]             # 手动补充模型列表（API 获取不到时使用）

# 可选：为特定模型配置 context_window 或 extra_payload
# [providers.openai.model_configs."gpt-4o-mini"]
# context_window = 128000
# extra_payload = { }

[model_metadata]
default_context_window = 256000
```

说明：

- `base_url` 使用 Provider 的 OpenAI-compatible API 地址。
- `api_key_env` 指向环境变量名，推荐用这种方式保存密钥。
- `proxy` 可选，支持 `http://` 和 `socks5://` 代理地址。
- `models` 是手动补充的模型名列表，当 Provider 的模型列表接口获取不到某些模型时使用。
- `[providers.<name>.model_configs."<model>"]` 为特定模型配置 `context_window` 和 `extra_payload`，两者都是可选的。
- `extra_payload` 会合并到 LLM 请求 JSON 中，模型级覆盖 Provider 级。
- `[model_metadata]` 的 `default_context_window` 是全局回退值，没有在 `model_configs` 里配 `context_window` 时使用。



## API Key 与 `.env`

密钥读取优先级：

1. 系统环境变量。
2. 配置目录下的 `.env`。

推荐方式：

```dotenv
DEEPSEEK_API_KEY=your-api-key
OPENAI_API_KEY=your-api-key
```

不要把真实 Key 提交到仓库。

## CLI 远程配置

`[platform.cli]` 同时保存 CLI 服务端和客户端配置。`server` 是当前 ElBot 作为服务端运行时读取的配置，`clients` 是当前命令作为 CLI 客户端连接服务端时读取的配置。

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

- `server.enabled=true` 时，`elbot service run` 会启动 CLI WebSocket 服务端。
- `server.listen` 是服务端监听地址。
- `default_url` 是客户端默认连接地址；连接其他机器时在 `clients.<name>.url` 写远程 WebSocket 地址。
- `server.tokens` 是服务端允许登录的 CLI client id 与 token 环境变量列表。
- `clients.<name>` 是客户端 profile；`id` 可省略，默认等于 `<name>`；`url` 可省略，默认使用 `default_url`。
- `elbot cli -c <name>` 使用指定客户端 profile；未指定时使用 `default_client`。
- CLI token 与 Provider API Key 一样，优先读系统环境变量，再读配置目录 `.env`。

## Go Skill 编译器路径

修改 Go skill 的 `code_source` 后，ElBot 会自动执行 `gofmt`、`go build` 并 reload。若 ElBot 以 Linux service 运行，service 环境可能没有加载交互 shell 的 `PATH`，导致终端里可用的 `go` 在 ElBot 中不可见。

推荐在配置目录 `.env` 中指定 Go 可执行文件：

```dotenv
ELBOT_GO_BINARY=/usr/local/go/bin/go
```

查找顺序：

1. 系统环境变量 `ELBOT_GO_BINARY`。
2. 配置目录 `.env` 中的 `ELBOT_GO_BINARY`。
3. `GOROOT/bin/go`，如果 service 环境配置了 `GOROOT`。
4. ElBot 进程 `PATH` 中的 `go`。

如果使用 asdf、mise、Nix、Linuxbrew、Snap 或自定义安装路径，推荐直接把实际 `go` 路径写入 `ELBOT_GO_BINARY`，不要依赖交互 shell 的初始化脚本。

修改 `.env` 或 systemd 环境后，需要重启 ElBot service。

高级部署也可以在 systemd service 中指定：

```ini
[Service]
Environment=ELBOT_GO_BINARY=/usr/local/go/bin/go
```

## 模型状态配置

`state.toml` 保存运行态模型选择：


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

- `default_mode` 决定新 Session 默认进入 `chat` 还是 `work`。
- `work` 模式启用工具发现和工具调用。
- `chat` 模式不注入工具，适合闲聊和低成本对话。
- `elwisp1`、`elwisp2`、`elwisp3` 是 Elnis LLM 事件可选模型槽位；Elvena 请求可通过 `model_slot` 指定，未配置时回退到 `work`。
- 运行时使用 `/model` 切换模型后，状态会写回 `state.toml`。

## 存储与运行数据

`app.toml` 中 storage 相关配置：

```toml
[storage]
sessions_sqlite_path = ""
chat_history_sqlite_path = ""
```

留空时使用平台默认数据目录：

- Windows：`%APPDATA%/ElBot/data`
- Linux：`$XDG_DATA_HOME/elbot` 或 `~/.local/share/elbot`

运行日志、SQLite、sandbox 等运行数据也会按配置或默认数据目录存放。


## 日志与维护任务

运行日志配置：

```toml
[runtime]
log_level = "info"
log_retention_days = 30
```

维护任务示例：

```toml
[maintenance.log_cleanup]
enabled = true
schedule = "0 3 * * *"
```

Cron 表达式由内部 Cron Runtime 调度，使用 Linux crontab 风格的 5 字段格式：`分钟 小时 日 月 星期`。默认维护任务包含日志、Session、sandbox 和聊天历史清理，例如 Session 清理默认保留 30 天，sandbox 默认每天 04:00 清理 7 天前内容，聊天历史默认每天 04:35 执行：

```toml
[maintenance.session_cleanup]
enabled = false
schedule = "15 3 * * *"
retention_days = 30
```

```toml
[maintenance.sandbox_cleanup]
enabled = true
schedule = "0 4 * * *"
retention_days = 7
```

```toml
[maintenance.chat_history_cleanup]
enabled = true
schedule = "35 4 * * *"
retention_days = 180
```

## Context 与压缩

```toml
[context]
compact_enabled = true
compact_trigger_ratio = 0.8
```

- 开启后，Session 上下文接近窗口上限时会触发压缩。
- 也可以通过 `/compact` 手动压缩当前 Session。
- 压缩只影响发给 LLM 的上下文视图，不删除原始历史。

模型窗口在 `providers.toml` 的 `model_configs` 中配置：

```toml
[providers.deepseek.model_configs."deepseek-chat"]
context_window = 64000

[model_metadata]
default_context_window = 256000
```

- `[model_metadata].default_context_window` 是全局回退值，模型块里没配 `context_window` 时使用。

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

- 普通用户只能发现和调用允许风险范围内的工具。
- 超级管理员调用高风险工具时也需要确认。
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

- `[tags.<tag-name>]`：定义一个可在聊天里使用的 tag，例如 `[tags.agent]` 对应 `@tool:agent`。
- `tools`：这个 tag 会预载的工具名列表。工具名必须是已注册且当前用户有权限访问的工具。
- `prompt`：这个 tag 成功激活后追加到 system prompt 的工具使用策略。内容会直接给模型看，不会自动添加 tag 名标题。

使用方式：

```text
@tool:agent 帮我检查这个项目的问题
```

这会把 `agent` 下配置的工具预载到当前 Session。如果 `prompt` 非空，也会从本轮开始追加到 system prompt。

注意事项：

- 配置 tag 会追加到内置 tag，不覆盖内置 tag。
- 只有 `@tool:<tag>` 成功命中至少一个工具后，当前 Session 才会激活该 tag 的 prompt。
- 直接 `@tool:<tool-name>` 只预载指定工具，不激活 tag prompt。
- 激活的 tag 会写入 Session metadata，`/resume` 后仍生效。
- prompt 文本从 `tool_tags.toml` 动态读取；文件变更后影响后续请求，行为类似 `SOUL.md`。
- 重复预载已经存在的工具时不会重复添加，平台会提示 `已存在工具：<name>`。
- 建议把 `prompt` 写成具体工具使用策略，不要写“当前 tag 是 xxx”这类模型不需要知道的配置机制。

## Elnis 监听枢纽

Elnis 默认关闭。启用后，ElBot 会启动本地 HTTP ingress，接收 Elwisp 按 Elvena 协议投递的事件。Elnis 配置建议拆到独立 `config/elnis.toml`，`app.toml` 只保留入口路径。

```toml
[config_files]
elnis = "elnis.toml"

# config/elnis.toml
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
- token 从系统环境变量或配置目录 `.env` 读取，日志只记录 token name，不记录 token 原文。
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

QQ 官方机器人、QQ OneBot 和 Telegram 配置在示例中默认注释。启用时需要补齐平台自己的认证信息和触发关键词。

Telegram 使用 Bot API long polling。最小配置示例：

```toml
[security.superadmins]
cli = ["local"]
telegram = ["123456789"]

[platform.telegram]
enabled = true
bot_token_env = "TELEGRAM_BOT_TOKEN"
proxy_url_env = "TELEGRAM_PROXY_URL" # 可选；先读系统环境变量，再读配置目录 .env
trigger_keywords = ["bot"]
format = "html" # html/plain/rich
stream_edit_interval_milliseconds = 250
```

说明：

- `bot_token_env` 指向 Bot Token 的变量名，读取顺序为系统环境变量、配置目录 `.env`；也可以用 `bot_token` 直接写入配置，但不建议提交真实 token。
- `proxy_url_env` 指向代理地址的变量名，读取顺序同样为系统环境变量、配置目录 `.env`；也可以用 `proxy_url` 直接写入配置。代理地址示例：`http://127.0.0.1:7890`、`socks5://127.0.0.1:1080`。
- `format="html"` 是默认值：使用普通 `sendMessage` + `parse_mode="HTML"`，并把常见 Markdown 轻量转换成 Telegram HTML；支持标题、引用、分割线、代码块和表格的可读渲染，失败时自动纯文本重试。
- `format="plain"` 关闭格式化，只发送纯文本。
- `format="rich"` 是实验模式：使用 `sendRichMessage` / 私聊 `sendRichMessageDraft`，Rich Message 失败时会自动退回 HTML；部分客户端可能无法查看 Rich Message。
- `stream_edit_interval_milliseconds` 控制流式刷新节流间隔，默认 250ms，避免触发平台限频。
- 启动连接成功后，ElBot 会把内置 slash 命令同步到 Telegram bot 命令菜单；只同步主命令名，不同步 alias。
- 群聊/超级群组中，命令前缀、触发关键词、`@bot_username` 或回复 bot 消息都会触发处理。私聊默认处理。
- 高风险工具确认消息会附带 Telegram inline keyboard，点击按钮会转换为 `/confirm`、`/reject` 等现有确认命令。
- `security.superadmins.telegram` 填 Telegram 用户 ID 或可直接发送的私聊 chat ID，用于超级管理员权限与通知投递。


## 插件和 Hook 配置

插件配置固定放在配置目录的 `plugins/` 下：

- `plugins/hooks.toml`：规则 Hook 配置。
- `plugins/<plugin-name>.toml`：插件专属配置。

Hook 和插件不要直接发平台消息，应返回输出意图，由 Agent 统一交给 Output Manager 发送。

## 建议的维护方式

- 用户可编辑配置集中放在平台配置目录，避免直接改源码示例。
- 真实密钥放系统环境变量或 `.env`。
- 新增配置项时同步更新本文档。
- 改变默认路径或启动行为时同步更新 [快速开始](getting-started.md) 和 README。
