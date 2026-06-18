# 配置说明

ElBot 使用一个主配置入口加载应用配置、Provider 配置和运行态状态。开发期不考虑旧配置兼容，建议直接按当前 `config/` 示例维护。

## 配置文件职责

默认源码配置目录包含：

| 文件或目录 | 职责 |
| --- | --- |
| `config/app.toml` | 主配置入口，保存 storage、runtime、context、commands、tools、security、platform 和配置文件路径。 |
| `config/providers.toml` | LLM Provider、模型列表、默认请求参数和模型 metadata。 |
| `config/state.toml` | 运行态状态，例如默认 Session 模式、chat/work/compact/naming 模型选择。 |
| `config/elnis.toml` | Elnis 监听枢纽配置，保存 HTTP、token、delivery、allowed_tools 和 Elwisp 策略。 |
| `config/SOUL.md` | Agent 的 System Prompt 来源文件。 |
| `config/.env` | 可选，本地密钥文件，不建议提交。 |
| `config/plugins/` | Hook 和插件配置目录。 |
| `config/skills/` | 用户侧 Skill 目录，默认位于配置目录下。 |
| `config/memories.toml` | 常驻记忆文件，默认位于配置目录下。 |
| `config/long_memory/` | 长期记忆 Markdown 源数据目录，默认位于配置目录下。 |

## 主配置查找顺序

启动时主配置按以下顺序查找：

1. 命令行 `--config`。
2. 环境变量 `ELBOT_CONFIG_FILE`。
3. 平台配置目录：Windows `%APPDATA%/ElBot/app.toml`；Linux 使用 XDG 配置目录。
4. 源码目录 `config/app.toml`。

示例：

```bash
go run ./cmd/elbot --config config/app.toml
```

## 相对路径规则

相对路径默认基于主配置文件所在目录解析。

例如使用 `config/app.toml` 时：

```toml
[config_files]
providers = "providers.toml"
state = "state.toml"
elnis = "elnis.toml"

[soul]
path = "SOUL.md"
```

这些路径都会解析到 `config/` 目录下。

## Provider 配置

Provider 写在 `providers.toml`：

```toml
[global_default]
stream = true
temperature = 1.0
max_tokens = 4096

[providers.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"

[providers.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
models = ["gpt-4o-mini"]
```

说明：

- `base_url` 使用 Provider 的 OpenAI-compatible API 地址。
- `api_key_env` 指向环境变量名，推荐用这种方式保存密钥。
- `models` 可手动补充模型列表；也可以通过 Provider 的模型列表接口获取。
- `[global_default]` 提供默认请求参数。

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
```

- `default_mode` 决定新 Session 默认进入 `chat` 还是 `work`。
- `work` 模式启用工具发现和工具调用。
- `chat` 模式不注入工具，适合闲聊和低成本对话。
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

运行日志、SQLite、sandbox、artifact 等运行数据也会按配置或默认数据目录存放。

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

Cron 表达式由内部 Cron Runtime 调度。默认维护任务包含日志、artifact 和聊天历史清理。

## Context 与压缩

```toml
[context]
compact_enabled = true
compact_trigger_ratio = 0.8
```

- 开启后，Session 上下文接近窗口上限时会触发压缩。
- 也可以通过 `/compact` 手动压缩当前 Session。
- 压缩只影响发给 LLM 的上下文视图，不删除原始历史。

模型窗口可以在 `providers.toml` 中补充：

```toml
[model_metadata]
default_context_window = 256000

[model_metadata.context_windows]
# "deepseek-chat" = 64000
```

## 命令前缀

```toml
[commands]
prefixes = ["/"]
```

默认使用 `/`。如果要支持其他命令前缀，可以在这里添加。

## 工具与安全

```toml
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

[delivery]
default_platforms = ["cli"]
allow_superadmins = true

[elwisps.server-watchdog]
allowed_tokens = ["home"]
allowed_tools = ["shell"]
disabled_external_tools = ["danger_tool"]
```

说明：

- `allowed_tools` 是 Elnis 内部工具白名单；未单独配置的 Elwisp 继承全局默认。
- 单个 Elwisp 若配置 `allowed_tools`，会覆盖全局默认。
- 外部工具默认允许；只有单个 Elwisp 配置 `disabled_external_tools` 时才禁用指定外部工具。
- token 从系统环境变量或配置目录 `.env` 读取，日志只记录 token name，不记录 token 原文。
- `token_env` 支持写成列表，按顺序尝试多个环境变量名；适合临时切换 token 或做多环境兼容。
- Elwisp 默认启用；只有显式配置 `enabled=false` 才会禁用对应 Elwisp。
- 当前支持 `record`、`direct` 和 `llm` 模式；`llm` 模式使用后台 Session runner 执行。
- `direct` 和 `llm` 报告只支持按 Elnis 裁决后的平台发送给 superadmins，不支持任意 user/group 目标。
- Elvena 请求中的 `tools` 声明是开发中能力，但已进入校验、持久化和执行链路；外部工具名仍需由单个 Elwisp 的禁用列表控制。

更多说明见 [Elnis 配置与使用](elnis-usage.md)。

## 平台配置

CLI 默认启用：

```toml
[platform.cli]
enabled = true
```

QQ 官方机器人和 QQ OneBot 配置在示例中默认注释。启用时需要补齐平台自己的认证信息、连接地址或触发关键词。

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
