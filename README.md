# ElBot

[中文](README.zh-CN.md) | English

ElBot is a lightweight Agent/Chatbot framework written in Go. It aims to keep extensibility while reducing runtime cost, context cost, and maintenance complexity. It supports normal chat, tool calling, hooks, scheduled tasks, persistent sessions, and context compaction, making it suitable for personal assistants, platform bots, and programmable automation agents.

## Highlights

### 0. Lightweight and efficient Go implementation

ElBot is implemented in Go, with fast startup, simple deployment, and low resource usage. In the current local environment, startup takes about 2ms and resident memory is about 20MB. It is suitable for long-running use on personal computers, small servers, or lightweight containers. Compared with large Agent frameworks that depend on complex runtimes, ElBot focuses more on controllability, stability, and maintainability.

### 1. Token-efficient tool discovery

Studies suggest that many everyday users still use ChatGPT-like systems mainly as advanced search engines, writing assistants, and conversational listeners; frequent tool calling is not the default pattern for every conversation. ElBot therefore does not inject full schemas for all tools into every turn. Instead, it exposes only `discover_tool` and available tool names. When the model needs a tool, it discovers the details on demand, and the Agent then injects the corresponding schema. This design avoids unnecessary context overhead in ordinary conversations.

References: Chatterji et al., *How People Use ChatGPT*, NBER, 2025; Yan et al., *ShareChat: A Dataset of Chatbot Conversations in the Wild*, arXiv:2512.17843, 2025.

### 2. Separate Chat and Work modes

ElBot separates chat mode from work mode. Chat mode removes tools entirely, making it better suited for casual conversation, companionship, lightweight Q&A, and low-cost interaction. Work mode enables tool discovery and tool calling for search, command execution, task creation, and skill operations. The two modes can use different models, so inexpensive models can handle chat while stronger models focus on complex tasks.

### 3. Extensible hook system

ElBot includes a Hook Layer that can extend key event points such as Agent input, LLM request preparation, LLM responses, platform sending, and platform connection. Hooks can modify messages, append output intents, call low-risk tools, or inject resident memory. Built-in hooks include rule hooks, emoticon hooks, and resident memory hooks, while additional plugins can evolve independently from the core logic.

### 4. Logging and audit system

ElBot separates runtime logs from audit logs. It supports structured fields, log queries, audit queries, request status inspection, and runtime debugging. Runtime logs help diagnose operational issues, while audit logs track command denial, tool calls, Cron delivery, persistence errors, and other important events. For long-running bots, this reduces troubleshooting cost and makes high-risk operations easier to trace.

### 5. Resident memory and persistent memory

ElBot separates memory into lightweight resident memory and queryable persistent memory. Resident memory is kept short and stable, containing only information that is truly useful in every turn, which reduces token usage. Longer and more complex memory is not automatically injected; instead, the LLM can query it when needed. Compared with automatic RAG or graph-based long-term memory, this approach is more controllable, avoids irrelevant memory pollution, and reduces retrieval and injection cost.

### 6. Direct Cron and LLM Cron

ElBot includes both a central Cron runtime and an LLM-orchestrated Cron service. Direct Cron sends fixed content on schedule, while LLM Cron lets a model execute one-time or recurring tasks from task descriptions and requires a parseable JSON result. It supports once/recurring jobs, missed once delivery, multi-platform notification, execution logs, and failure alerts, making it useful for reminders, daily reports, monitoring summaries, and background automation.

### 7. ELyph: task notation for LLM collaboration

ElBot introduces ELyph Task Notation for LLM Cron and native skills. ELyph is designed to reduce ambiguity in natural-language task descriptions and express inputs, outputs, steps, conditions, and constraints in a shorter and more stable structure. Compared with free-form Markdown, ELyph is better suited for task reuse and communication between LLMs, and it is easier to lint, audit, and process with tools.

### 8. LLM-created El Skills

ElBot provides the `create_el_skill` meta tool, allowing the LLM to turn reusable experience into El Skills. An El Skill can describe its purpose, inputs, outputs, and execution rules in ELyph, and can optionally include Go source code for concrete implementation. ELyph validation runs before creation, and successful skills are reloaded automatically. This allows the Agent not only to solve one-off tasks, but also to solidify recurring workflows into reusable capabilities.

### 9. Compatible with Python skills from the web

Besides native El Skills, ElBot is also compatible with common external Python skill structures. It can scan `SKILL.md` or `SKILL.elyph`, read the skill name, description, use cases, and risk level, then execute the skill through hidden wrapper tools. This makes it possible to reuse existing skill ecosystems while gradually moving toward the more structured ELyph format.

### 10. Multi-platform and rich output abstraction

ElBot abstracts platform and output layers. It currently supports CLI and QQ OneBot, with room for more platforms. Hooks, tools, and plugins do not send platform messages directly; instead, they return platform-independent output intents such as text, images, files, mentions, or emoticons. Platforms send rich output when supported and automatically fall back to text when necessary, reducing coupling between plugins and platform adapters.

### 11. Sessions, forks, and context compaction

ElBot includes a persistent Session service with session resume, archive, pin, delete, pagination, and platform isolation. When replying to historical assistant messages, it can automatically fork a branch session to avoid polluting the original timeline. The context layer supports model window resolution, compaction checkpoints, and summary injection, helping long conversations control context size while keeping history traceable.

### 12. Security policy and risk confirmation

ElBot's tool system includes risk levels, permission checks, and high-risk confirmation. Normal users can only discover and call tools within their allowed risk level, and even superadmins must confirm high-risk tool calls. Shell commands are classified by risk and pass through confirmation, while background Cron uses a lightweight sandbox to limit path escape. This gives the Agent practical execution power while reducing the chance of accidental damage.

## Usage

TODO

## Development status

ElBot is still under active development. APIs, configuration, and internal implementation may continue to change. It is currently best suited for experimentation as a personal Agent or bot framework. Installation instructions, configuration examples, plugin development docs, and deployment guides will be improved later.

