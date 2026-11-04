# ElBot Agent 操作手册

本文件只写 Agent 在本仓库里“怎么做”（How）。架构细节、代码职责和功能说明不要塞回这里：

- 需要理解整体架构：读 `devdocs/architecture.md`。architecture-principles必读
- 需要定位代码入口：读 `devdocs/code-map.md`。
- 需要用户侧功能说明：从 `docs/README.md` 开始。
- 需要维护任务：读 `devdocs/tasks.md`。

## 基本原则

- 默认使用简体中文。
- 当前项目是 Go 项目。
- 先看用户给出的上下文和相关文档，再查代码；不要凭空猜。
- 只读取和任务相关的文件；不要为了一个小改全项目乱翻。
- （非重构时）优先复用已有接口和模式，不为一次性需求做抽象。
- 不考虑历史兼容,不让代码变复杂,除非用户明确要求。
- 旧代码与已确认的目标设计不符时，先询问用户是否同时改造或优化；不得擅自扩大改动、改变设计或强行兼容旧代码。已获授权的改造按确认结论执行。
- 暂不实现但未来可能需要的能力，可以留接口或 TODO，但不要写投机性代码。
- 所有包、文件位置，包名、文件名、函数名、变量名不能自作主张，必须和用户讨论，可以提出建议候选让用户选。测试代码除外。
- 对于比较底层的定义型字段，需在代码的注释中说明字段作用。不确定是否应该写时，应询问用户。
- 测试代码不要测不存在的东西，只测需要的东西
- 更新文档时，旧文档、矛盾、中间过程、临时直接删除，只保留最新状态

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
| 公共上下文事实 | `locator:contextinfo` | `devdocs/code-map.md`, `devdocs/architecture.md` |
| 信号与订阅 | `locator:signal` | `devdocs/code-map.md`, `devdocs/architecture.md` |
| Session/上下文 | `locator:session`, `locator:context` | `devdocs/architecture.md`, `devdocs/code-map.md` |
| SQLite/存储 | `locator:storage` | `devdocs/code-map.md` |
| Elnis/Elvena/Elwisp | `locator:elnis` | `docs/elnis.md`, `docs/elnis-usage.md`, `devdocs/architecture.md` |
| Cron/维护任务 | `locator:cron` | `devdocs/code-map.md` |
| 文档维护 | `locator:docs` | 本文件、`docs/README.md` |
| 测试验证 | `locator:testing` | 本文件 |


<!-- locator:docs -->
## 文档维护规则

- 文档只收录其职责范围内、读者完成任务需要的信息；代码变化不等于需要追加文档。更新时改写已有表述，删除失效或重复内容，不累积补丁说明。
- `architecture.md`：模块职责、依赖边界、关键数据流与架构约束。仅在说明取舍、特殊情况或难以理解的设计时展开；不记录字段清单、函数步骤、展示格式或局部实现。
- `code-map.md`：主题 → 主要目录／文件 → 简短定位用途。保留主要文件，不追求穷举；不解释行为、调用步骤或设计约束，普通测试文件不单列。
- 收录前判断：是否属于本文职责？是否已有说明？能否从对应代码直接看懂？超出职责的不收，已有说明的引用，代码足以表达的细节省略；必要的取舍、特殊约束和跨模块关系保留。
- 表达以准确、简短、无歧义为准。可混用中英、逻辑符号、伪代码和 Mermaid；符号含义须明确，图与正文不重复复述，不为缩短篇幅省略条件、主体或因果关系。
- 用户侧功能、命令、配置、行为变化：更新 `docs/*.md` 和 `CHANGELOG.md`。
- `docs.en/`、`CHANGELOG.en.md` 及 `README.md` 正文是自动翻译产物，不要手动修改。中英文 README 的 `manual-header:start` / `manual-header:end` 区域分别手动维护（标题至项目介绍），不参与自动翻译。
- `devdocs/` 只给维护者和 Agent 看，不参与自动翻译。
- `AGENTS.md` 只保留操作规则；不要把架构详解、逐文件说明重新写回这里。
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
