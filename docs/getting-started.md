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

ElBot 默认读取 `config/app.toml`，并通过其中的 `[config_files]` 加载 `config/providers.toml` 和 `config/state.toml`。

默认示例已经包含 DeepSeek 和 OpenAI Provider：

```toml
[providers.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"

[providers.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
models = ["gpt-4o-mini"]
```

推荐把密钥放在系统环境变量，或放在配置目录下的 `.env` 文件中，不要直接写进 `providers.toml`。

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

## 启动 CLI

开发期可以直接运行：

```bash
go run ./cmd/elbot
```

指定配置文件：

```bash
go run ./cmd/elbot --config config/app.toml
```

构建二进制：

```bash
go build -o elbot ./cmd/elbot
```

Windows 下：

```bash
go build -o elbot.exe ./cmd/elbot
```

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
- 可以启动后用 `/models --fresh` 刷新模型列表。

### 不想默认启用工具

把 `config/state.toml` 中的默认模式改为 chat：

```toml
[session]
default_mode = "chat"
```

需要工具时再输入 `/work`。
