// 定义批次执行的输入输出及调用方协作契约。
package share

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
	"time"
)

type ELwispToolDeclaration struct {
	// CanonicalName 是适配方生成的模型侧唯一名称，不属于外部声明协议字段。
	CanonicalName string `json:"-"`
	// EventKey 是远程调用需传回的事件关联标识，不参与预载授权判断。
	EventKey       string         `json:"-"`
	Name           string         `json:"name"`
	Description    string         `json:"description,omitempty"`
	Schema         map[string]any `json:"schema,omitempty"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	Endpoint       string         `json:"endpoint,omitempty"`
	ForegroundOnly bool           `json:"foreground_only,omitempty"`
}

type ExecutionResult struct {
	// Call is the request supplied to this execution entry point.
	Call llm.ToolCallRequest
	// Message is the model-facing result; partial tool segments survive failures.
	Message llm.LLMMessage
	// Result is the original tool result, including partial results on error; nil means none.
	Result *Result
	// LogResult.Content carries the independent tool log body. Before call entry,
	// the execution source declares ResultStatus and the reason; after entry,
	// the runner uses ResultStatus for the invocation log outcome.
	LogResult events.LogResult
	// Error is the original execution or preparation error; nil does not declare success.
	Error error
	// ResultStatus is declared at the execution source, including pre-execution rejection.
	ResultStatus events.ResultStatus
	// ExecutionStarted records entry into Tool.Call or PreparedCall.Call, even if it fails.
	ExecutionStarted bool
}

// PolicyDeniedError identifies an expected authorization or confirmation refusal.
// Its text remains the user-facing reason; observers use the explicit source result.
type PolicyDeniedError string

func (e PolicyDeniedError) Error() string { return string(e) }

type Context struct {
	Mode             string
	Session          *storage.Session
	Scope            session.Scope
	Actor            contextinfo.Actor
	DisableBaseTools bool
}

type RunnerDeps interface {
	RefreshExecution(context.Context, *storage.Session) (context.Context, error)
	// Hook sources publish their own records; Runner consumes only the business return.
	PrepareToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, events.LogResult, error)
	ShouldSendPreview(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, assistantText string) bool
	ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment RiskAssessment, detail string) (ConfirmResult, error)
	// StartToolRequest returns its source registration result independently of
	// the business context, start time, cleanup function and original error.
	StartToolRequest(ctx context.Context, sessionID, toolName string) (context.Context, time.Time, func(), events.LogResult, error)
	PrepareToolContext(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) context.Context
	// CompleteToolCall also exposes the declared Hook outcome separately from segments.
	CompleteToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, risk string, segments []llm.MessageSegment, callErr error) ([]llm.MessageSegment, events.LogResult, error)
	// SendPreview takes one complete notice per tool call, including multiline
	// bodies. Pass batch notices separately so each receives one display prefix.
	SendPreview(ctx context.Context, text ...string)
	// SendOutputs returns the source logging result and body, including partial
	// delivery facts, separately from its original business error.
	SendOutputs(ctx context.Context, outputs []delivery.Output) (events.LogResult, error)
	AuditToolDenied(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk RiskLevel, reason string)
	AddToolUse(sessionID, toolName string)
	ToolResultMessage(sessionID string, message llm.LLMMessage) storage.Message
	ToolCallMessage(sessionID, content, rawText string, calls []llm.ToolCallRequest) storage.Message
}

type ConfirmResult struct {
	Allowed bool
	Extra   string
	Message llm.LLMMessage
	Stopped bool
}

// ToolCommitter is synchronous persistence owned by the consuming dialogue.
// Begin reserves slots; Result persists one outcome; Flush publishes the projection.
// Each operation returns its source log result separately from the business error;
// phases without an actual write declare skipped instead of inventing a save.
type ToolCommitter interface {
	Begin(context.Context, *storage.Message) (events.LogResult, error)
	Prepared(context.Context, int, llm.ToolCallRequest) (events.LogResult, error)
	Result(context.Context, int, llm.ToolCallRequest, llm.LLMMessage, *storage.ToolResultCommit) (events.LogResult, error)
	Flush(context.Context) (events.LogResult, error)
}

type RunRequest struct {
	Committer        ToolCommitter
	Session          *storage.Session
	Calls            []llm.ToolCallRequest
	AssistantText    string
	AssistantRawText string
	DiscoveredTools  []DiscoveredTool
	Actor            contextinfo.Actor
}

type RunResult struct {
	Err               error
	Messages          []llm.LLMMessage
	PreparedCalls     []llm.ToolCallRequest
	ConfirmationExtra string
	Stopped           bool
}
