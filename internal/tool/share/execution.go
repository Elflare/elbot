// 定义批次执行的输入输出及调用方协作契约。
package share

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
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

type Context struct {
	Mode  string
	Actor contextinfo.Actor
}

type RunnerDeps interface {
	RefreshExecution(context.Context, *storage.Session) (context.Context, error)
	// Hook sources publish their own records; Runner consumes only the business return.
	PrepareToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, error)
	ShouldSendPreview(ctx context.Context, assistantText string) bool
	ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment RiskAssessment, detail []ConfirmationDetailBlock) (operationresult.Result[ConfirmResult], error)
	// StartToolRequest registers the request and returns its context and cleanup.
	StartToolRequest(ctx context.Context, sessionID, toolName string) (context.Context, func(), error)
	PrepareToolContext(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) context.Context
	// CompleteToolCall applies the Hook to the result segments.
	CompleteToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, risk string, result operationresult.Result[[]llm.MessageSegment], callErr error) ([]llm.MessageSegment, error)
	// SendPreview takes one complete notice per tool call, including multiline
	// bodies. Pass batch notices separately so each receives one display prefix.
	SendPreview(ctx context.Context, text ...string)
	// SendOutputs delivers tool outputs and returns the original delivery error.
	SendOutputs(ctx context.Context, outputs []delivery.Output) (operationresult.Result[struct{}], error)
	PublishToolResult(context.Context, string, delivery.ToolResult)
	AuditToolDenied(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk RiskLevel, reason string)
	AddToolUse(sessionID, toolName string)
	ToolResultMessage(sessionID string, message llm.LLMMessage) storage.Message
	ToolCallMessage(sessionID, content, rawText, reasoningContent string, calls []llm.ToolCallRequest) storage.Message
}

type ConfirmResult struct {
	Extra string // 用户批准调用时补充的输入。
}

// ToolCommitter is synchronous persistence owned by the consuming dialogue.
// Begin reserves slots; Result persists one outcome; Flush publishes the projection.
// Each operation returns its original persistence fault;
// phases without an actual write return nil.
type ToolCommitter interface {
	Begin(context.Context, *storage.Message) error
	Prepared(context.Context, int, llm.ToolCallRequest) error
	Result(context.Context, int, llm.ToolCallRequest, llm.LLMMessage, *storage.ToolResultCommit) error
	Flush(context.Context) error
}

type RunRequest struct {
	Committer        ToolCommitter
	Session          *storage.Session
	Calls            []llm.ToolCallRequest
	AssistantText    string
	AssistantRawText string
	ReasoningContent string
	DiscoveredTools  []DiscoveredTool
	Actor            contextinfo.Actor
}

type RunResult struct {
	Err               error
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
