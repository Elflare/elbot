// 定义批次执行的输入输出及调用方协作契约。
package share

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/storage"
	"time"
)

type ELwispToolDeclaration struct {
	Risk RiskLevel `json:"-"` // 接线方固定提供 low，不接受远端协议输入。
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

// PolicyDeniedError identifies an expected authorization or confirmation refusal.
// Its text remains the user-facing reason; observers use the explicit source result.
type PolicyDeniedError string

func (e PolicyDeniedError) Error() string { return string(e) }

type Context struct {
	Mode             string
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

// CallPolicy 是根部从执行事实裁剪的工具策略，子包不解释任务种类。
type CallPolicy struct {
	DisableForegroundTools bool            // 当前调用不开放仅前台使用的工具。
	ExplicitChat           bool            // 历史查询使用显式聊天地址。
	AutoConfirmShell       bool            // 受限任务 Shell 自动确认，仍拒绝最高风险。
	OutputTarget           delivery.Target // 提供方预先确定的工具输出目标。
}
type callPolicyKey struct{}

func WithCallPolicy(ctx context.Context, policy CallPolicy) context.Context {
	return context.WithValue(ctx, callPolicyKey{}, policy)
}
func PolicyFromContext(ctx context.Context) CallPolicy {
	policy, _ := ctx.Value(callPolicyKey{}).(CallPolicy)
	return policy
}
