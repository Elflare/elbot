// Agent 对外的回调、请求与结果契约。
package agent

import (
	"context"
	"encoding/json"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/tool"
)

// Points is one provider's callbacks. Nil callbacks preserve the original value
// or do nothing. Independent Agent requests may call this group concurrently.
// Providers report their execution failures; Agent handles returned business
// errors at the existing call sites without repeating provider logs.
type Points struct {
	// HasRoute is queried before media preparation; CancelRoute handles /cancel.
	HasRoute    func(context.Context, Facts, Message) bool
	CancelRoute func(context.Context, Facts, Message) bool
	// Includes continuation routing and ordinary rules. Their stop-propagation
	// remains internal to the provider.
	PlatformMessageReceived func(context.Context, Facts, Message) (PlatformMessageReceivedResult, events.LogResult, error)
	AgentInputPrepared      func(context.Context, Facts, Message) ([]llm.MessageSegment, events.LogResult, error)
	LLMTurnPrepared         func(context.Context, Facts, LLMInput) (LLMInputResult, events.LogResult, error)
	LLMRequestPrepared      func(context.Context, Facts, LLMInput) (LLMInputResult, events.LogResult, error)
	LLMResponseReceived     func(context.Context, Facts, LLMResponse) (LLMResponseResult, events.LogResult, error)
	ToolCallPrepared        func(context.Context, Facts, llm.ToolCallRequest) (string, events.LogResult, error)
	ToolCallCompleted       func(context.Context, Facts, ToolResult) ([]llm.MessageSegment, events.LogResult, error)
	AgentOutputPrepared     func(context.Context, Facts, string) (string, events.LogResult, error)
	AgentTurnOutputPrepared func(context.Context, Facts, string) (string, events.LogResult, error)
	PlatformMessageSent     func(context.Context, Facts, string)
	ErrorOccurred           func(context.Context, Facts, LLMResponse, error)
}

// Facts contains Agent facts, not a Hook protocol envelope. Conversation,
// execution and original platform message facts remain available through their
// owning packages' context accessors. Providers must honor read-only snapshots.
type Facts struct {
	Session       Session           // Session snapshot; empty before binding.
	Actor         contextinfo.Actor // Source identity without defaults for source-free work.
	Woken         bool              // Entry wakeup result; never recomputed after edits.
	ReadOnlyCalls bool              // Reject changes to returned call IDs, names and arguments.
}

type Session struct {
	ID, Mode, Title, Status string // Detached session identity and display facts.
}

type Message struct {
	ID, Role     string               // Persisted identity and role when available.
	PlatformText string               // Original platform text, unaffected by edits.
	IntentText   string               // User text with the entry wakeup prefix removed.
	Segments     []llm.MessageSegment // Editable message bound to this stage.
}

type PlatformMessageReceivedResult struct {
	Segments []llm.MessageSegment // Message after continuation and ordinary rules.
	Consume  bool                 // Stop Agent command/model processing after sending Outputs.
	Outputs  []delivery.Output    // Additional output sent before the consume decision.
}

type LLMInput struct {
	Message         Message          // Initial user input for turn; only new pending for request.
	Provider, Model string           // Selected model facts.
	Messages        []llm.LLMMessage // Detached read-only working context.
	Tools           []llm.ToolSchema // Definitions governed by the existing provider edit policy.
}

type LLMInputResult struct {
	Segments []llm.MessageSegment // Only the message bound in LLMInput may be edited.
	Tools    []llm.ToolSchema     // Returned definitions remain subject to Agent availability.
}

type LLMResponse struct {
	Provider, Model  string                // Selected model facts.
	Text, SourceText string                // Display text and original model text.
	Usage            *llm.Usage            // Model usage snapshot.
	ToolCalls        []llm.ToolCallRequest // Immutable when Facts.ReadOnlyCalls is true.
	ElapsedMS        int64                 // Duration of the completed or failed model call.
}

type LLMResponseResult struct {
	Text, SourceText string                // Display result and original model text.
	Usage            *llm.Usage            // Usage under the existing provider policy.
	ToolCalls        []llm.ToolCallRequest // Calls governed by Facts.ReadOnlyCalls.
	Outputs          []delivery.Output     // Extra outputs retaining their existing delivery timing.
}

type ToolResult struct {
	Call     llm.ToolCallRequest  // Executed call; identity and arguments are facts here.
	Risk     string               // Execution risk classification.
	Segments []llm.MessageSegment // Editable result, including non-text segments.
	Err      error                // Actual execution error, independent of result display.
}

type StatusChangedEvent struct {
	Snapshot     runtimestatus.Snapshot    // Committed execution state, independent of presentation.
	Version      uint64                    // Monotonic publication order within the recorder, not a binding version.
	Execution    contextinfo.Execution     // Identity of the execution that published the state, including terminal states.
	Conversation *contextinfo.Conversation // Original presentation destination; nil when no conversation exists.
	Binding      *BindingSnapshot          // Activation associated with this state; nil when unbound.
}

// BindingSnapshot describes an activation without exposing its internal handle.
type BindingSnapshot struct {
	Scope     session.Scope // Conversation and actor scope of the activation.
	SessionID string        // Persisted session selected by this activation.
	Version   uint64        // Service-local version assigned when this activation was established.
}

// BindingChangedEvent contains independently copied facts for one subscriber.
// At least one of Old and New is present.
type BindingChangedEvent struct {
	Version uint64           // Service-local committed change version; unbinding also advances it.
	Old     *BindingSnapshot // Previous activation; nil when none existed.
	New     *BindingSnapshot // New activation; nil when the scope becomes unbound.
}

// BackgroundRequest 描述后台执行输入；调用方在 context 中提供已准备的非空工作目录范围与后台来源。
// 工具声明仅初始化新会话，续跑沿用会话中已提交的状态。
type BackgroundRequest struct {
	Kind             string                       // 后台任务来源。
	Name             string                       // 来源内的任务标识。
	Title            string                       // 新会话标题。
	Platform         string                       // 任务所属平台。
	Actor            contextinfo.Actor            // 执行身份。
	ScopeID          string                       // 任务作用域。
	SessionID        string                       // 续跑会话；空值创建新会话。
	ModelProvider    string                       // 本次模型提供方覆盖。
	Model            string                       // 本次模型名称覆盖。
	Prompt           string                       // 本次输入文本。
	PromptSegments   []llm.MessageSegment         // 本次输入的有序多模态片段。
	NeedsElyphRules  bool                         // 输入材料是否要求 ELyph 通用规则。
	ToolListNames    []string                     // 初始化时选择的工具或技能。
	AllowedToolNames []string                     // 本地根工具限制；nil 不额外限制，空切片禁止。
	ELwispTools      []tool.ELwispToolDeclaration // 已授权的远端工具声明。
	Metadata         map[string]string            // 写入任务会话的来源元数据。
}

// BackgroundResult 只返回执行事实；报告解析和重试策略归 Background。
type BackgroundResult struct {
	RunID     string // 本次执行标识。
	SessionID string // 实际使用的会话。
	MessageID string // 最终持久化回复标识。
	Text      string // 最终回复正文。
	TakenOver bool   // 执行是否已由前台接管。
	Outcome   string // 执行终态。
}

// BackgroundCopyRequest 复制后台历史，不激活目标前台会话。
type BackgroundCopyRequest struct {
	SourceSessionID string                     // 被复制的后台会话。
	Scope           session.Scope              // 目标会话作用域。
	Kind            string                     // 目标后台来源。
	Name            string                     // 来源内的任务标识。
	Title           string                     // 目标标题。
	Metadata        map[string]json.RawMessage // 目标来源元数据，保留各字段的 JSON 值类型。
}

// ErrForegroundSession 表示后台复制的来源已不再允许后台操作。
var ErrForegroundSession = session.ErrForegroundSession
