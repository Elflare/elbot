// 执行本次已批准的工具对象，转换结果，不重复查询或授权。
package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"elbot/internal/events"
	"elbot/internal/llm"
)

type executionResult struct {
	// Call is the request supplied to this execution entry point.
	Call llm.ToolCallRequest
	// Message is the model-facing result; partial tool segments survive failures.
	Message llm.LLMMessage
	// Result is the original tool result, including partial results on error; nil means none.
	Result *toolshare.Result
	// LogResult.Content carries the independent tool log body. Before call entry,
	// the execution source declares ResultStatus and the reason; after entry,
	// the runner uses ResultStatus for the invocation log outcome.
	LogResult events.LogResult
	// Error is the original execution or preparation error; nil does not declare success.
	Error error
	// ResultStatus is declared at the execution source, including pre-execution rejection.
	ResultStatus events.ResultStatus
	// ExecutionStarted 记录进入 PreparedCall.Call，准备阶段失败不算开始执行。
	ExecutionStarted bool
}

func (m *Manager) executePrepared(ctx context.Context, call llm.ToolCallRequest, resolved resolvedTool, prepared *toolshare.PreparedCall, onStarted func()) executionResult {
	message := llm.LLMMessage{
		Role:       llm.RoleTool,
		Name:       call.Name,
		ToolCallID: call.ID,
	}
	if resolved.ResultStatus != events.ResultSucceeded {
		return executionError(call, message, resolved.Error, resolved.ResultStatus)
	}
	if err := ctx.Err(); err != nil {
		return executionError(call, message, err, events.ResultCanceled)
	}
	request := toolshare.CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)}
	if !prepared.Matches(request) {
		return executionError(call, message, fmt.Errorf("tool call changed after preparation"), events.ResultFailed)
	}
	if onStarted != nil {
		onStarted()
	}
	result, logContent, err := prepared.Call(ctx)
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
	return executionResult{Call: call, Message: message, Result: result, LogResult: events.LogResult{Content: logContent}, Error: err, ResultStatus: resultStatus, ExecutionStarted: true}
}

func executionError(call llm.ToolCallRequest, message llm.LLMMessage, err error, resultStatus events.ResultStatus) executionResult {
	content := fmt.Sprintf("tool call %s failed: %v", call.Name, err)
	message.Segments = llm.TextSegments(content)
	return executionResult{Call: call, Message: message, Error: err, ResultStatus: resultStatus, LogResult: events.LogResult{ResultStatus: resultStatus, Content: toolshare.TruncateLogContent(content, 1024)}}
}
