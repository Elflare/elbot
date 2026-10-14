package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/security"
)

type Executor struct {
	Registry *Registry
	Actor    contextinfo.Actor
	Policy   *security.Policy
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
	// the runner declares the invocation log status, independent of business errors.
	LogResult events.LogResult
	// Error is the original execution or preparation error; nil does not declare success.
	Error error
	// ResultStatus is declared at the execution source, including pre-execution rejection.
	ResultStatus events.ResultStatus
	// ExecutionStarted becomes true only when Tool.Call is entered, even if it fails.
	ExecutionStarted bool
}

// PolicyDeniedError identifies an expected authorization or confirmation refusal.
// Its text remains the user-facing reason; observers use the explicit source result.
type PolicyDeniedError string

func (e PolicyDeniedError) Error() string { return string(e) }

func (e Executor) Execute(ctx context.Context, call llm.ToolCallRequest) ExecutionResult {
	message := llm.LLMMessage{
		Role:       llm.RoleTool,
		Name:       call.Name,
		ToolCallID: call.ID,
	}
	tool, ok := e.Registry.Get(call.Name)
	if !ok {
		return executionError(call, message, fmt.Errorf("tool %q not found", call.Name), events.ResultFailed)
	}
	if reason := UnavailableReason(ctx, tool.Info()); reason != "" {
		return executionError(call, message, PolicyDeniedError(reason), events.ResultRejected)
	}
	assessment, err := AssessRisk(ctx, tool, CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
	if err != nil {
		resultStatus := events.ResultFailed
		if errors.Is(err, context.Canceled) {
			resultStatus = events.ResultCanceled
		}
		return executionError(call, message, err, resultStatus)
	}
	policy := e.Policy
	actor := e.Actor
	info := tool.Info()
	if info.SuperadminOnly && actor.Role != contextinfo.RoleSuperadmin {
		return executionError(call, message, PolicyDeniedError(fmt.Sprintf("tool %q requires superadmin role", call.Name)), events.ResultRejected)
	}
	if !policy.CanUseTool(actor, assessment.Level, info.OwnerScoped) {
		return executionError(call, message, PolicyDeniedError(fmt.Sprintf("risk %s is above your allowed tool level", assessment.Level)), events.ResultRejected)
	}
	result, logContent, err := tool.Call(ctx, CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
	resultStatus := events.ResultSucceeded
	if err != nil {
		resultStatus = events.ResultFailed
		if errors.Is(err, context.Canceled) {
			resultStatus = events.ResultCanceled
		}
		message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s failed: %v", call.Name, err))
	}
	// 只把 Content/Segments 回灌给 LLM；Result.Data 留给 Agent 内部结构化消费。
	// 避免 discover schema、内部状态或大对象污染上下文。
	if result != nil {
		message.Segments = result.LLMSegments()
	}
	return ExecutionResult{Call: call, Message: message, Result: result, LogResult: events.LogResult{Content: logContent}, Error: err, ResultStatus: resultStatus, ExecutionStarted: true}
}

func executionError(call llm.ToolCallRequest, message llm.LLMMessage, err error, resultStatus events.ResultStatus) ExecutionResult {
	content := fmt.Sprintf("tool call %s failed: %v", call.Name, err)
	message.Segments = llm.TextSegments(content)
	return ExecutionResult{Call: call, Message: message, Error: err, ResultStatus: resultStatus, LogResult: events.LogResult{ResultStatus: resultStatus, Content: TruncateLogContent(content, 256)}}
}
