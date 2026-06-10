# 接口设计

## 设计目标

接口设计用于约束 ElBot 内部模块边界。Go 实现中应优先通过 interface 解耦 Agent Core、LLM、Tool、Storage、Platform 和 Security。

本文档中的代码为接口草案，具体字段可在实现中调整。

## 基础类型

### MessageRole

```go
type MessageRole string

const (
    RoleSystem    MessageRole = "system"
    RoleUser      MessageRole = "user"
    RoleAssistant MessageRole = "assistant"
    RoleTool      MessageRole = "tool"
)
```

### SessionMode

```go
type SessionMode string

const (
    SessionModeWork SessionMode = "work"
    SessionModeChat SessionMode = "chat"
)
```

### RiskLevel

```go
type RiskLevel string

const (
    RiskSafe     RiskLevel = "safe"
    RiskLow      RiskLevel = "low"
    RiskMedium   RiskLevel = "medium"
    RiskHigh     RiskLevel = "high"
    RiskCritical RiskLevel = "critical"
)
```

## LLM Adapter

### LLM 接口

```go
type LLM interface {
    ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error)
    ListModels(ctx context.Context) ([]string, error)
}
```

`ChatStream` 是 ElBot 的主对话接口，Adapter 应通过流式响应持续产出 `StreamChunk`。调用方负责消费 chunk、拼接最终 assistant 内容、累积工具调用和记录 usage。

### ChatRequest

```go
type ChatRequest struct {
    Model       string
    Messages    []LLMMessage
    Temperature float64
    MaxTokens   int
    ExtraBody   map[string]any
}
```

`ExtraBody` 用于携带供应商扩展请求字段，合并到最终请求体中。

### StreamChunk

```go
type StreamChunk struct {
    DeltaContent   string
    ToolCallDeltas []ToolCallDelta
    FinishReason   string
    Usage          *Usage
}
```

### ToolCallDelta

```go
type ToolCallDelta struct {
    Index int
    ID    string
    Name  string
    Args  string
}
```

`Args` 表示流式工具调用参数的 JSON 片段或已累积结果，具体由 Adapter 负责转换为统一语义。

### ChatResponse

```go
type ChatResponse struct {
    Message   LLMMessage
    ToolCalls []ToolCallRequest
    Usage     *Usage
}
```

`ChatResponse` 可作为调用方聚合流式结果后的完整响应结构。

### LLMMessage

```go
type LLMMessage struct {
    Role       MessageRole
    Content    string
    Name       string
    ToolCallID string
    ToolCalls  []ToolCallRequest
    Metadata   map[string]any
}
```

### Usage

```go
type Usage struct {
    PromptTokens     int
    CompletionTokens int
    TotalTokens      int
}
```

## Tool Runtime

### Tool 接口

```go
type Tool interface {
    Name() string
    Description() string
    Schema() ToolSchema
    Risk(ctx context.Context, args json.RawMessage) RiskLevel
    Call(ctx context.Context, args json.RawMessage) (*ToolResult, error)
}
```

### ToolRegistry

```go
type ToolRegistry interface {
    Register(tool Tool) error
    Unregister(name string) error
    Get(name string) (Tool, bool)
    List() []ToolInfo
    Discover(name string) (*ToolDiscoveryResult, error)
}
```

### ToolSchema

```go
type ToolSchema struct {
    Name        string
    Description string
    Parameters  json.RawMessage
}
```

### ToolInfo

```go
type ToolInfo struct {
    Name        string
    Description string
    Group       string
    RiskLevel   RiskLevel
}
```

### ToolCallRequest

```go
type ToolCallRequest struct {
    ID        string
    Name      string
    Arguments string
}
```

### ToolResult

```go
type ToolResult struct {
    Content  string
    Data     json.RawMessage
    Metadata map[string]any
}
```

### ToolDiscoveryResult

```go
type ToolDiscoveryResult struct {
    Tools []ToolSchema
    Brief []ToolInfo
}
```

约定：

- `discover_tool` 用于查询其他工具的详细信息。
- `discover_tool` 简介为：用于发现其他工具的工具。
- `discover_tool` 参数：
  - `name`: String，可选，工具名称。
- `discover_tool` 返回值：
  - 指定 `name` 时，返回该工具的名称、简介、参数定义和调用约束。
  - 未指定 `name` 时，返回全部工具的名称与简介。
- 模型需要使用工具时，应优先根据工具名称调用 `discover_tool` 查询工具详情。
- 若模型仅凭名称无法判断用途，应先获取全部工具名称与简介，再进一步查询目标工具。
- `ToolDiscoveryResult.Tools` 用于承载完整工具定义。
- `ToolDiscoveryResult.Brief` 用于承载工具名称和简介列表。

## Commands

### CommandRouter

```go
type CommandRouter interface {
    Dispatch(ctx context.Context, input InputMessage) (*OutputMessage, error)
}
```

### CommandHandler

```go
type CommandHandler interface {
    Name() string
    Handle(ctx context.Context, req CommandRequest) (*CommandResult, error)
}
```

### CommandRequest

```go
type CommandRequest struct {
    Actor   Actor
    Session *Session
    Input   InputMessage
    Name    string
    Args    []string
}
```

### CommandResult

```go
type CommandResult struct {
    SessionID string
    Content   string
    Metadata  map[string]any
}
```

统一 Slash 命令由 Agent Core 分发。Platform Adapter 负责输入转换和平台特性映射。

### StatusResult

```go
type StatusResult struct {
    SessionID           string
    Title               string
    Mode                SessionMode
    Status              string
    ArchivedAt          *time.Time
    PinnedAt            *time.Time
    CurrentModel        string
    CompactModel        string
    MessageCount        int
    ConversationTurns   int
    ActiveRequestStatus string
    TokenUsage          Usage
    ContextWindow       ContextWindowStatus
    CreatedAt           time.Time
    LastMessageAt       time.Time
    SubAgents           []SubAgentStatus
    ToolUsage           []ToolUsageSummary
    LastAskPreview      string
    LastAnswerPreview   string
}
```

`TokenUsage` 使用 LLM 厂商返回的 usage 信息。当前里程碑无法获取的字段由命令输出层显示为 `TODO`。后续版本可通过 `SubAgents` 展示多个子 Agent 状态。

### SubAgentStatus

```go
type SubAgentStatus struct {
    ID       string
    Name     string
    Status   string
    Summary  string
    Metadata map[string]any
}
```

### ContextWindowStatus

```go
type ContextWindowStatus struct {
    EstimatedTokens int
    ContextWindow   int
    UsedRatio       float64
    CompactThreshold float64
}
```

### ToolUsageSummary

```go
type ToolUsageSummary struct {
    Name  string
    Count int
}
```

`/stop` 默认停止当前 Session 下全部 active request、工具调用和子 Agent 活动。

## Agent Core

### Agent 接口

```go
type Agent interface {
    HandleMessage(ctx context.Context, input InputMessage) (*OutputMessage, error)
    StopSession(ctx context.Context, sessionID string) error
}
```

### InputMessage

```go
type InputMessage struct {
    ActorID                  string
    SessionID                string
    Platform                 string
    PlatformScopeID          string
    PlatformMessageID        string
    ReplyToPlatformMessageID string
    Content                  string
    Attachments              []Attachment
    Metadata                 map[string]any
}
```

### OutputMessage

```go
type OutputMessage struct {
    SessionID string
    Content   string
    Metadata  map[string]any
}
```

### PromptBuilder

```go
type PromptBuilder interface {
    Build(ctx context.Context, req PromptBuildRequest) ([]LLMMessage, error)
}
```

### PromptBuildRequest

```go
type PromptBuildRequest struct {
    Session      Session
    Actor        Actor
    Messages     []Message
    ToolNames    []string
    ResidentMemo []Memory
    Soul         *Soul
}
```

## Context Management

### ContextWindowResolver

```go
type ContextWindowResolver interface {
    Resolve(ctx context.Context, provider string, model string) (int, error)
}
```

解析模型上下文窗口大小。优先从 Provider `/models` API 或 metadata 获取；API 或 metadata 无法提供时，读取手动配置；最后使用默认窗口。

### TokenEstimator

```go
type TokenEstimator interface {
    EstimateMessages(messages []LLMMessage) int
    EstimateText(text string) int
}
```

MVP 可先使用近似估算，后续按模型接入更精确 tokenizer。

### ContextCompressor

```go
type ContextCompressor interface {
    Compact(ctx context.Context, req CompactRequest) (*CompactResult, error)
}
```

### CompactRequest

```go
type CompactRequest struct {
    SessionID       string
    Provider        string
    Model           string
    Messages        []LLMMessage
    TargetTokens    int
    TriggerReason   string // manual or auto
    FromMessageID   string
    ToMessageID     string
}
```

### CompactResult

```go
type CompactResult struct {
    Summary              string
    SourceTokenEstimate  int
    SummaryTokenEstimate int
}
```

### ContextLoader

```go
type ContextLoader interface {
    Load(ctx context.Context, sessionID string) (*LoadedContext, error)
}
```

### LoadedContext

```go
type LoadedContext struct {
    Summary  *ContextSummary
    Messages []Message
}
```

上下文加载应返回当前 Session 的“最新压缩摘要 + 摘要之后的新消息”。原始消息仍完整保存在 `messages` 表中。

### ContextSummaryRepository

```go
type ContextSummaryRepository interface {
    Create(ctx context.Context, summary *ContextSummary) error
    LatestBySession(ctx context.Context, sessionID string) (*ContextSummary, error)
}
```

### ContextSummary

```go
type ContextSummary struct {
    ID                   string
    SessionID            string
    FromMessageID        string
    ToMessageID          string
    Summary              string
    Provider             string
    Model                string
    SourceTokenEstimate  int
    SummaryTokenEstimate int
    TriggerReason        string
    Metadata             string
    CreatedAt            time.Time
}
```

## Session Service

### SessionService

```go
type SessionService interface {
    GetOrCreateCurrent(ctx context.Context, actor Actor, scope PlatformScope) (*Session, error)
    Create(ctx context.Context, req CreateSessionRequest) (*Session, error)
    Resume(ctx context.Context, actor Actor, sessionID string) (*Session, error)
    List(ctx context.Context, actor Actor, req ListSessionsRequest) ([]SessionSummary, error)
    Fork(ctx context.Context, req ForkSessionRequest) (*Session, error)
    SetMode(ctx context.Context, sessionID string, mode SessionMode) error
    Archive(ctx context.Context, sessionID string) error
    Unarchive(ctx context.Context, sessionID string) error
    Pin(ctx context.Context, sessionID string) error
    Unpin(ctx context.Context, sessionID string) error
    Delete(ctx context.Context, sessionID string) error
    CleanupExpired(ctx context.Context, now time.Time) ([]SessionSummary, error)
}
```

### ListSessionsRequest

```go
type ListSessionsRequest struct {
    Query string
    Limit int
}
```

### ForkSessionRequest

```go
type ForkSessionRequest struct {
    ActorID       string
    SourceSession string
    FromMessageID string
    InitialMode   SessionMode
}
```

## Storage

### Storage 接口

```go
type Storage interface {
    Sessions() SessionRepository
    Messages() MessageRepository
    ToolCalls() ToolCallRepository
    Confirmations() ConfirmationRepository
    Memories() MemoryRepository
    AuditLogs() AuditLogRepository
    Usage() UsageRepository
    Transaction(ctx context.Context, fn func(ctx context.Context) error) error
}
```

### SessionRepository

```go
type SessionRepository interface {
    Create(ctx context.Context, session *Session) error
    Get(ctx context.Context, id string) (*Session, error)
    Update(ctx context.Context, session *Session) error
    ListByActor(ctx context.Context, actorID string, req ListSessionsRequest) ([]SessionSummary, error)
    Delete(ctx context.Context, id string) error
    ListExpired(ctx context.Context, before time.Time, limit int) ([]SessionSummary, error)
}
```

### MessageRepository

```go
type MessageRepository interface {
    Append(ctx context.Context, message *Message) error
    Get(ctx context.Context, id string) (*Message, error)
    ListBySession(ctx context.Context, sessionID string) ([]Message, error)
    LoadForkContext(ctx context.Context, sessionID string) ([]Message, error)
    MapPlatformMessage(ctx context.Context, mapping PlatformMessageMap) error
    FindByPlatformMessage(ctx context.Context, platform, scopeID, platformMessageID string) (*Message, error)
}
```

## Platform Adapter

### PlatformAdapter

```go
type PlatformAdapter interface {
    Name() string
    Run(ctx context.Context, handler PlatformHandler) error
    Send(ctx context.Context, msg PlatformOutputMessage) error
}
```

### PlatformHandler

```go
type PlatformHandler interface {
    HandlePlatformMessage(ctx context.Context, msg PlatformInputMessage) error
}
```

### PlatformInputMessage

```go
type PlatformInputMessage struct {
    Platform                 string
    ScopeID                  string
    UserID                   string
    MessageID                string
    ReplyToMessageID          string
    Text                     string
    Attachments              []Attachment
    Mentions                 []Mention
    Raw                      any
}
```

### PlatformOutputMessage

```go
type PlatformOutputMessage struct {
    Platform  string
    ScopeID   string
    UserID    string
    SessionID string
    Text      string
    Metadata  map[string]any
}
```

## Security

### Authorizer

```go
type Authorizer interface {
    Can(ctx context.Context, actor Actor, action Action, resource Resource) (bool, error)
}
```

### ConfirmationService

```go
type ConfirmationService interface {
    Require(ctx context.Context, req ConfirmationRequest) (*Confirmation, error)
    ConfirmOnce(ctx context.Context, confirmationID string, actor Actor) error
    ConfirmSession(ctx context.Context, confirmationID string, actor Actor) error
    Reject(ctx context.Context, confirmationID string, actor Actor, reason string) error
    CancelSession(ctx context.Context, sessionID string, actor Actor) error
}
```

### Action

```go
type Action string

const (
    ActionUseTool       Action = "tool.use"
    ActionDiscoverTool  Action = "tool.discover"
    ActionManageSession Action = "session.manage"
    ActionWriteMemory   Action = "memory.write"
    ActionManageConfig  Action = "config.manage"
    ActionConfirmRisk   Action = "risk.confirm"
)
```

## Hook

### HookManager

```go
type HookManager interface {
    BeforeReceive(ctx context.Context, msg *InputMessage) error
    AfterReceive(ctx context.Context, msg *InputMessage) error
    BeforeLLM(ctx context.Context, req *ChatRequest) error
    AfterLLM(ctx context.Context, resp *ChatResponse) error
    BeforeToolCall(ctx context.Context, call *ToolCallRequest) error
    AfterToolCall(ctx context.Context, result *ToolResult) error
    BeforeSend(ctx context.Context, msg *OutputMessage) error
    AfterSend(ctx context.Context, msg *OutputMessage) error
    OnError(ctx context.Context, err error)
    OnPlatformConnected(ctx context.Context, adapter PlatformAdapter) error
}
```

Hook 用于在关键流程前后执行扩展逻辑。权限、风险等级和危险确认由 Security Layer 作为硬约束统一处理，Hook 可用于复用实现流程。

## Config

### ConfigProvider

```go
type ConfigProvider interface {
    Load(ctx context.Context) (*Config, error)
    Watch(ctx context.Context, onChange func(*Config)) error
}
```

### Config

```go
type Config struct {
    ConfigFiles ConfigFilesConfig
    Models      ModelConfig
    Session     SessionConfig
    Context     ContextConfig
    Platform    PlatformConfig
    Storage     StorageConfig
    Security    SecurityConfig
    Runtime     RuntimeConfig
}
```

### SessionConfig

```go
type SessionConfig struct {
    Cleanup SessionCleanupConfig
}
```

### ConfigFilesConfig

```go
type ConfigFilesConfig struct {
    Providers string
}
```

### SessionCleanupConfig

```go
type SessionCleanupConfig struct {
    Enabled       bool
    RetentionDays int
    RunOnStartup  bool
}
```

### ContextConfig

```go
type ContextConfig struct {
    CompactEnabled      bool
    CompactTriggerRatio float64
    CompactTargetRatio  float64
    CompactProvider     string
    CompactModel        string
}
```

### ModelMetadataConfig

```go
type ModelMetadataConfig struct {
    DefaultContextWindow int
    ContextWindows       map[string]int
}
```

配置文件职责：

- 主配置入口为 `config/app.toml`。
- `app.toml` 保存应用级静态配置，包括 `[config_files]`、`[storage]`、`[runtime]`、`[context]` 和 `[soul]`，不保存当前模型选择。
- `providers.toml` 保存模型供应商相关配置，包括 `[global_default]`、`[providers.*]` 和模型 context window 元信息，不保存当前选用模型。
- `state.toml` 保存运行态状态，包括 `[session] default_mode`、`[mode_models]` 和 `[naming_model]`。
- 配置内相对路径基于主配置文件所在目录解析。
- 默认配置入口为 `config/app.toml`，Provider 配置通过主配置引用。

## 接口实现优先级

首批优先实现：

1. `LLM`
2. `Storage`
3. `SessionService`
4. `Agent`
5. `PlatformAdapter` 的 CLI 实现
6. `ToolRegistry`

后续 Milestone 按能力引入：

- `ContextLoader`
- `ContextCompressor`
- `Authorizer`
- `ConfirmationService`
- `HookManager`
