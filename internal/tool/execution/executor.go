// 执行本次已批准的工具对象，转换结果，不重复查询或授权。
package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"elbot/internal/llm"
	"elbot/internal/operationresult"
)

type executionResult struct {
	// Message is the model-facing result, using Message on rejection and err.Error() on faults.
	Message llm.LLMMessage
	// Result carries the business outcome; Data is available only on success.
	Result operationresult.Result[*toolshare.Result]
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
	if err := ctx.Err(); err != nil {
		return executionError(call, err)
	}
	request := toolshare.CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)}
	if !prepared.Matches(request) {
		err := fmt.Errorf("tool call changed after preparation")
		record := newRunRecord(ctx, "", "tool_prepare", err)
		record.Level = slog.LevelError
		m.Log(ctx, record)
		return executionError(call, err)
	}
	if onStarted != nil {
		onStarted()
	}
	result, logContent, err := prepared.Call(ctx)
	if err != nil {
		message.Segments = llm.TextSegments(err.Error())
		return executionResult{Message: message, Summary: logContent, Error: err, ExecutionStarted: true}
	}
	if result.Status != operationresult.Success {
		message.Segments = llm.TextSegments(result.Message)
		return executionResult{Message: message, Result: result, Summary: logContent, ExecutionStarted: true}
	}
	toolResult := result.Data
	// 只把 Content/Segments 回灌给 LLM；Result.Data 留给 Agent 内部结构化消费。
	// 避免 discover schema、内部状态或大对象污染上下文。
	if toolResult != nil {
		if resolved.Native != nil {
			toolResult.NeedsElyphRules = toolResult.NeedsElyphRules || resolved.Native.Definition.NeedsElyphRules
		}
		if toolResult.DiscoveryText != nil {
			// Hook 只处理业务详情；摘要与公共规则由协调器在 Hook 后组装。
			message.Segments = llm.TextSegments(toolResult.DiscoveryText.Details)
		} else {
			message.Segments = toolResult.LLMSegments()
		}
	}
	return executionResult{Message: message, Result: result, Summary: logContent, ExecutionStarted: true}
}

func executionError(call llm.ToolCallRequest, err error) executionResult {
	content := err.Error()
	return executionResult{Message: toolMessage(call.Name, call.ID, content), Error: err, Summary: content}
}
