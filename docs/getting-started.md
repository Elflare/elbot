# 快速开始

目标：配好 API Key，启动 CLI，完成第一次对话。

## 环境要求

- Go 1.26 或更新版本。
- 一个 OpenAI-compatible LLM 服务（兼容 `/chat/completions`，例如 DeepSeek、OpenAI）。
- 至少一个可用 API Key。

## 获取代码并启动

```bash
git clone https://github.com/Elflare/elbot/
cd elbot
go run ./cmd/elbot                            # 使用平台配置目录
go run ./cmd/elbot --config path/to/app.toml  # 使用指定配置
```

首次运行且平台配置目录没有 `app.toml` 时，会自动生成 `app.toml`、`providers.toml`、`state.toml`、`SOUL.md`、`elnis.toml` 和 `.env.example`；已有文件不会被覆盖。

## 配置 Provider

默认配置已包含 DeepSeek 和 OpenAI：

```toml
[providers.deepseek]
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"

[providers.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
models = ["gpt-4o-mini"]
```

密钥放在系统环境变量，或把自动生成的 `.env.example` 复制为配置目录下的 `.env` 后填写；不要直接写进 `providers.toml`。

```dotenv
DEEPSEEK_API_KEY=your-api-key
OPENAI_API_KEY=your-api-key
```

## 选择默认模型

运行态模型写在配置目录的 `state.toml`：

```toml
[session]
default_mode = "work"     # 新 Session 默认模式

[mode_models.work]        # work 模式模型；启用工具
provider = "deepseek"
model = "deepseek-chat"

[mode_models.chat]        # chat 模式模型；不注入工具
provider = "deepseek"
model = "deepseek-chat"
```

换 Provider 时同步修改 `provider` 和 `model`。`default_mode` 只能手动改，其他设置可以在 CLI 里用命令设置。

## 运行模式

| 命令 | 行为 |
| --- | --- |
| `elbot` | 自动：优先连接 CLI 服务端，符合条件时回退本地启动 |
| `elbot run` | 完整前台：本地 CLI 界面 + 已启用平台 + 后台任务 |
| `elbot client [-c name]` | CLI 客户端，连接服务端 |
| `elbot service run` | 无界面服务：已启用平台 + 后台任务，按配置提供 CLI 服务端 |

- 自动模式未显式指定 `-c` 时，只有 CLI 被禁用、没有任何连接配置、或默认本机服务拨号失败才回退本地启动；配置、认证和握手错误直接报告。
- service 模式不启动本地交互界面；没有配置消息平台、或全部平台均已退出时仍保持运行，直到外部停止。
- 直接启动的交互 CLI 退出时会结束所属应用进程。
- 同一份配置只能运行一个本地实例。锁文件位于实际配置旁（如 `app.toml.lock`），正常关闭或异常退出后自动释放，无需手动清理；运行期间不要删除，保存或替换配置文件不影响已取得的锁。
- 正常退出是秒级；卡住时最多等 30 秒，到时强制关闭。

## CLI TUI 按键

| 按键 | 作用 |
| --- | --- |
| `PgUp` / `PgDn` / `Home` / `End` | 滚动聊天输出 |
| `Ctrl+K` / `Ctrl+J` | 小幅滚动聊天输出 |
| `Ctrl+U` / `Ctrl+D` | 滚动通知区 |
| `Alt+h` / `Alt+l` | 进入聊天区 / 通知区 copy mode |
| copy mode 中的 `h/j/k/l`、`w/e/b` | 移动 |
| copy mode 中的 `v` / `V`、`y`、`/` | 选择、复制、搜索 |
| copy mode 中的 `i` 或 `Esc` | 返回输入 |
| `#<文件名>` | 补全并引用本地文件；文件不存在时保留原文，按普通 `#` 文本发送 |
| 拖动聊天区与通知区之间的竖线 | 宽屏模式下调整两侧宽度，本次运行期间保留 |

## 构建与 Shell 补全

```bash
go build -o elbot ./cmd/elbot            # Windows: go build -o elbot.exe ./cmd/elbot
elbot completion auto|bash|zsh|fish|nushell|powershell
elbot completion fish > ~/.config/fish/completions/elbot.fish   # 示例
```

`auto` 按当前环境变量猜测 shell。nushell 写入 `~/.config/nushell/completions/elbot.nu` 后，在 Nushell 配置中 source 该文件。

## 第一次对话

启动后直接输入消息即可。常用起步命令：

| 命令 | 作用 |
| --- | --- |
| `/help` | 查看可用命令 |
| `/status` | 当前 Session、模型、上下文和请求状态 |
| `/models`（别名 `/checkmodel`） | 查看可用模型列表 |
| `/model <编号或名称>` | 切换模型 |
| `/chat` / `/work` | 切换低成本聊天模式 / 可使用工具的工作模式 |

更多命令见 [命令速查](commands.md)。

## 配置目录和数据目录

主配置查找顺序：`--config` → `ELBOT_CONFIG_FILE` → 平台配置目录（Windows `%APPDATA%/ElBot/app.toml`，Linux XDG 配置目录）。只有在没有显式 `--config` 和 `ELBOT_CONFIG_FILE` 时才自动生成平台默认配置；显式指定的路径不存在会直接报错。

运行数据（SQLite、日志、sandbox）默认进入平台数据目录。完整规则见 [配置说明](configuration.md)。

## 常见问题

**启动时报 API Key 缺失**：检查 `providers.toml` 中的 `api_key_env` 是否写对、系统环境变量或配置目录 `.env` 是否包含对应 Key、当前 shell 能否读取这些变量。

**模型不可用或名称不对**：检查 `state.toml` 中的 `provider` 是否存在于 `providers.toml`、`model` 是否被该 Provider 支持；也可以启动后用 `/models --fresh` 或 `/models --refresh` 重载厂商配置并刷新模型列表。

**不想默认启用工具**：把 `state.toml` 的 `default_mode` 改为 `"chat"`，需要工具时再 `/work`。
