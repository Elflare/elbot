# 快速开始

本文档用于把 ElBot 跑起来，并完成第一次 CLI 对话。

## 环境要求

- Go 1.26 或更新版本。
- 一个 OpenAI-compatible LLM 服务，例如 DeepSeek、OpenAI 或其他兼容 `/chat/completions` 的服务。
- 至少一个可用 API Key。

## 获取代码

```bash
git clone https://github.com/Elflare/elbot/
cd elbot
```

如果已经在本仓库内，可以直接继续下一步。

## 配置 Provider

ElBot 默认按配置查找顺序读取主配置。直接运行源码时会使用 `config/app.toml`；编译后的二进制如果找不到平台配置和源码示例配置，会在平台配置目录自动生成 `app.toml`、`providers.toml`、`state.toml`、`SOUL.md`、`elnis.toml` 和 `.env.example`。

默认配置已经包含 DeepSeek 和 OpenAI Provider：

```toml
[providers.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"

[providers.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
models = ["gpt-4o-mini"]
```

推荐把密钥放在系统环境变量，或把自动生成的 `.env.example` 复制为配置目录下的 `.env` 后填写，不要直接写进 `providers.toml`。

示例 `.env`：

```dotenv
DEEPSEEK_API_KEY=your-api-key
OPENAI_API_KEY=your-api-key
```

## 选择默认模型

默认运行态模型写在 `config/state.toml` ：

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

如果使用其他 Provider，需要同步修改 `provider` 和 `model`。
default_mode 手动设置，其他设置可以在cli中使用命令设置。

## 启动 ElBot

开发期可以直接运行：

```bash
go run ./cmd/elbot
```

指定配置文件：

```bash
go run ./cmd/elbot --config config/app.toml
```

常用运行模式：

```bash
elbot              # 自动模式
elbot run          # 完整前台：CLI + 已启用平台 + Cron
elbot cli          # 本地 CLI-only：只启动 CLI，不启动平台和 Cron
elbot service run  # Linux/headless 服务模式：不启动 CLI，启动已启用平台和 Cron
```

自动模式下，Linux 如果检测到当前用户已有 `elbot service run` 在运行，会进入本地 CLI-only，避免重复连接平台或重复运行 Cron；否则进入完整前台模式。Windows 不做 service 检测，默认完整前台启动。

`elbot cli` 是独立本地进程，会使用同一套配置和 SQLite 数据，但不会接管 service 进程中的当前请求、确认状态或内存里的当前 Session。需要继续历史会话时，可用 `/list` 和 `/resume`。

构建二进制：

```bash
go build -o elbot ./cmd/elbot
```

Windows 下：

```bash
go build -o elbot.exe ./cmd/elbot
```

## Shell 补全

ElBot 可以生成常见 shell 的补全脚本：

```bash
elbot completion bash
elbot completion zsh
elbot completion fish
elbot completion nushell
elbot completion powershell
```

也可以用 `auto` 按当前环境变量猜测 shell：

```bash
elbot completion auto
```

示例：fish

```bash
elbot completion fish > ~/.config/fish/completions/elbot.fish
```

示例：nushell

```bash
elbot completion nushell > ~/.config/nushell/completions/elbot.nu
```

然后在 Nushell 配置中 source 该文件。

## 第一次对话

启动后直接输入消息即可。默认示例中 `config/state.toml` 的 `default_mode` 是 `work`，会启用工具发现能力。

常用起步命令：

```text
/help
/status
/models
/model 1
/chat
/work
```

- `/help` 查看可用命令。
- `/status` 查看当前 Session、模型、上下文和请求状态。
- `/models` 查看可用模型列表。
- `/model <编号或名称>` 切换模型。
- `/chat` 切到低成本聊天模式。
- `/work` 切到可使用工具的工作模式。

更多命令见 [命令速查](commands.md)。

## 配置目录和数据目录

默认配置查找顺序：

1. `--config` 指定的路径。
2. `ELBOT_CONFIG_FILE` 环境变量。
3. 平台配置目录，例如 Windows `%APPDATA%/ElBot/app.toml`，Linux XDG 配置目录。
4. 源码目录 `config/app.toml`。
5. 如果平台配置和源码示例配置都不存在，则自动生成平台默认配置。

运行数据默认进入平台数据目录，例如 SQLite、日志、sandbox 和 artifact。具体规则见 [配置说明](configuration.md)。

## 常见问题

### 启动时报 API Key 缺失

检查：

- `providers.toml` 中的 `api_key_env` 是否写对。
- 系统环境变量或配置目录 `.env` 是否包含对应 Key。
- 当前 shell 是否能读取这些环境变量。

### 模型不可用或名称不对

检查：

- `state.toml` 中的 `provider` 是否存在于 `providers.toml`。
- `model` 名称是否被 Provider 支持。
- 可以启动后用 `/models --fresh` 或 `/models --refresh` 刷新模型列表。

### 不想默认启用工具

把 `config/state.toml` 中的默认模式改为 chat：

```toml
[session]
default_mode = "chat"
```

需要工具时再输入 `/work`。
