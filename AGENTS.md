# ElBot Agent 操作手册

本文件只写 Agent 在本仓库里“怎么做”（How）。架构细节、代码职责和功能说明不要塞回这里：

- 需要理解整体架构：读 `devdocs/architecture.md`。
- 需要定位代码入口：读 `devdocs/code-map.md`。
- 需要用户侧功能说明：从 `docs/README.md` 开始。
- 需要维护任务：读 `devdocs/tasks.md`。

## 基本原则

- 默认使用简体中文。
- 当前项目是 Go 项目。
- 先看用户给出的上下文和相关文档，再查代码；不要凭空猜。
- 只读取和任务相关的文件；不要为了一个小改全项目乱翻。
- 优先复用已有接口和模式，不为一次性需求做抽象。
- 不考虑历史兼容到让代码变复杂,除非用户明确要求。
- 暂不实现但未来可能需要的能力，可以留接口或 TODO，但不要写投机性代码。

<!-- locator:locate -->
## 快速定位流程

1. 先判断任务类型。
2. 用下面的 locator 搜索文档位置，拿到行号后只读附近章节。
3. 如果只是找文件入口，优先查 `devdocs/code-map.md`。
4. 如果要理解调用链路，再查 `devdocs/architecture.md`。
5. 定位到 Go 文件后，优先用 LSP/语义工具理解符号；Markdown 和纯文本用 `rg -n`。

示例：

```bash
rg -n "locator:tool" AGENTS.md devdocs/*.md docs/*.md
rg -n "locator:agent-chat" AGENTS.md devdocs/*.md
```

常见任务入口：

| 任务 | 搜索 locator | 优先阅读 |
|---|---|---|
| 启动、装配、配置 | `locator:startup`, `locator:config` | `devdocs/code-map.md`, `devdocs/architecture.md` |
| Agent 对话流程 | `locator:agent-chat` | `devdocs/architecture.md`, `devdocs/code-map.md` |
| Slash 命令 | `locator:commands` | `devdocs/code-map.md` |
| Request/Turn 状态 | `locator:request-turn` | `devdocs/architecture.md`, `devdocs/code-map.md` |
| Tool/工具调用 | `locator:tool`, `locator:tool-flow` | `devdocs/architecture.md`, `devdocs/code-map.md` |
| Skill | `locator:skill` | `devdocs/code-map.md` |
| Hook/插件 | `locator:hook` | `docs/hooks.md`, `devdocs/code-map.md` |
| Output/发送 | `locator:output` | `devdocs/architecture.md`, `devdocs/code-map.md` |
| 平台适配 | `locator:platform` | `devdocs/code-map.md` |
| Session/上下文 | `locator:session`, `locator:context` | `devdocs/architecture.md`, `devdocs/code-map.md` |
| SQLite/存储 | `locator:storage` | `devdocs/code-map.md` |
| Elnis/Elvena/Elwisp | `locator:elnis` | `docs/elnis.md`, `docs/elnis-usage.md`, `devdocs/elnis-elwisp.md` |
| Cron/维护任务 | `locator:cron` | `devdocs/code-map.md` |
| 文档维护 | `locator:docs` | 本文件、`docs/README.md` |
| 测试验证 | `locator:testing` | 本文件 |


<!-- locator:docs -->
## 文档维护规则

- 用户侧功能、命令、配置、行为变化：更新 `docs/*.md` 和 `CHANGELOG.md`。
- 内部架构、代码地图、维护说明变化：更新 `devdocs/*.md`。
- 新增、删除或明显调整重要 Go 文件职责：更新 `devdocs/code-map.md`；必要时在本文件的 locator 表里补入口。
- `docs.en/`、`README.md`、`CHANGELOG.en.md` 是英文镜像或自动翻译产物，不要手动修改。
- `devdocs/` 只给维护者和 Agent 看，不参与自动翻译。
- `AGENTS.md` 只保留操作规则；不要把架构详解、逐文件说明重新写回这里。
- 在已知代码的情况下，发现 code-map 或者 architecture 和代码有冲突或者内容太多，以代码为准，同时修改或者精简这两个文档。
- 只有changelog.md执行补丁式写作：描述从旧状态怎样变化到新状态；其他文档只描述现在正确的状态。

<!-- locator:testing -->
## 验证规则

- 修改 Go 文件后至少运行 `gofmt`。
- 优先运行相关包测试；影响范围不明确或公共接口变化时，再运行更大范围测试。
- 修复 bug 时，优先用测试或最小复现证明问题，再验证修复。
- 修改文档时，用 `rg -n` 检查关键链接、locator 和禁改说明是否还在。
- 如果本次只改 Markdown，不需要运行 Go 测试。
- 看diff时发现有和自己改动无关或者和记忆中不对的地方，可能是用户在同时修改，先向用户确认。

## 注意：

- 所有非精确rg使用（如搜索内容中有多个|、内容简短等），必须加上限制，如-m，或者使用管道符过滤

