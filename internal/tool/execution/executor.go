// 执行本次已批准的工具对象，转换结果，不重复查询或授权。
package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"elbot/internal/llm"
)

type executionResult struct {
	// Message is the model-facing result; partial tool segments survive failures.
	Message llm.LLMMessage
	// Result is the original tool result, including partial results on error; nil means none.
	Result *toolshare.Result
	// Summary is the tool's independent audit text; business decisions never inspect it.
	Summary string
	// Error preserves the original execution or preparation failure.
	Error error
	// ExecutionStarted 记录进入 PreparedCall.Call，准备阶段失败不算开始执行。
	ExecutionStarted bool
}

func (m *Manager) executePrepared(ctx context.Context, call llm.ToolCallRequest, resolved resolvedTool, prepared *toolshare.PreparedCall, onStarted func()) executionResult {
	message := llm.LLMMessage{
		Role:       llm.RoleTool,
		Name:       call.Name,
		ToolCallID: call.ID,
	}
	if resolved.Error != nil {
		return executionError(call, message, resolved.Error)
	}
	if err := ctx.Err(); err != nil {
		return executionError(call, message, err)
	}
	request := toolshare.CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)}
	if !prepared.Matches(request) {
		err := fmt.Errorf("tool call changed after preparation")
		record := newRunRecord(ctx, "", "tool_prepare", err)
		record.Level = slog.LevelError
		m.Log(ctx, record)
		return executionError(call, message, err)
	}
	if onStarted != nil {
		onStarted()
	}
	result, logContent, err := prepared.Call(ctx)
	if err != nil {
		message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s failed: %v", call.Name, err))
	}
	// 只把 Content/Segments 回灌给 LLM；Result.Data 留给 Agent 内部结构化消费。
	// 避免 discover schema、内部状态或大对象污染上下文。
	if result != nil {
		if resolved.Native != nil {
			result.NeedsElyphRules = result.NeedsElyphRules || resolved.Native.Definition.NeedsElyphRules
		}
		if result.DiscoveryText != nil {
			// Hook 只处理业务详情；摘要与公共规则由协调器在 Hook 后组装。
			message.Segments = llm.TextSegments(result.DiscoveryText.Details)
		} else {
			message.Segments = result.LLMSegments()
		}
	}
	return executionResult{Message: message, Result: result, Summary: logContent, Error: err, ExecutionStarted: true}
}

func executionError(call llm.ToolCallRequest, message llm.LLMMessage, err error) executionResult {
	reason := "failed"
	var denied toolshare.PolicyDeniedError
	if errors.As(err, &denied) {
		reason = "rejected"
	}
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if cause == context.Canceled {
			reason = "canceled"
			break
		}
	}
	content := fmt.Sprintf("tool call %s %s: %v", call.Name, reason, err)
	message.Segments = llm.TextSegments(content)
	return executionResult{Message: message, Error: err, Summary: content}
}
