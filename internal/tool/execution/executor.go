// 执行单次原生工具调用，处理风险评估与执行校验。
package execution

import toolshare "elbot/internal/tool/share"

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
	Registry   *Registry
	Actor      contextinfo.Actor
	Permission *security.Permission
	// OnStarted runs after authorization, immediately before the tool body.
	OnStarted func()
}

func (e Executor) Execute(ctx context.Context, call llm.ToolCallRequest) toolshare.ExecutionResult {
	return e.execute(ctx, call, nil)
}

func (e Executor) ExecutePrepared(ctx context.Context, call llm.ToolCallRequest, prepared *PreparedCall) toolshare.ExecutionResult {
	return e.execute(ctx, call, prepared)
}

func (e Executor) execute(ctx context.Context, call llm.ToolCallRequest, prepared *PreparedCall) toolshare.ExecutionResult {
	message := llm.LLMMessage{
		Role:       llm.RoleTool,
		Name:       call.Name,
		ToolCallID: call.ID,
	}
	tool, ok := e.Registry.Get(call.Name)
	if !ok {
		return executionError(call, message, fmt.Errorf("tool %q not found", call.Name), events.ResultFailed)
	}
	request := toolshare.CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)}
	if prepared != nil {
		if !prepared.matches(request) {
			return executionError(call, message, fmt.Errorf("tool call changed after preparation"), events.ResultFailed)
		}
		tool = prepared.tool
	}
	if reason := UnavailableReason(ctx, tool.Info()); reason != "" {
		return executionError(call, message, toolshare.PolicyDeniedError(reason), events.ResultRejected)
	}
	assessment, err := AssessRisk(ctx, tool, toolshare.CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
	if err != nil {
		resultStatus := events.ResultFailed
		if errors.Is(err, context.Canceled) {
			resultStatus = events.ResultCanceled
		}
		return executionError(call, message, err, resultStatus)
	}
	permission := e.Permission
	actor := e.Actor
	info := tool.Info()
	if info.SuperadminOnly && actor.Role != contextinfo.RoleSuperadmin {
		return executionError(call, message, toolshare.PolicyDeniedError(fmt.Sprintf("tool %q requires superadmin role", call.Name)), events.ResultRejected)
	}
	if !permission.CanUseTool(actor, assessment.Level, info.OwnerScoped) {
		return executionError(call, message, toolshare.PolicyDeniedError(fmt.Sprintf("risk %s is above your allowed tool level", assessment.Level)), events.ResultRejected)
	}
	var result *toolshare.Result
	var logContent string
	if e.OnStarted != nil {
		e.OnStarted()
	}
	if prepared != nil {
		result, logContent, err = prepared.Call(ctx)
	} else {
		result, logContent, err = tool.Call(ctx, request)
	}
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
	return toolshare.ExecutionResult{Call: call, Message: message, Result: result, LogResult: events.LogResult{Content: logContent}, Error: err, ResultStatus: resultStatus, ExecutionStarted: true}
}

func executionError(call llm.ToolCallRequest, message llm.LLMMessage, err error, resultStatus events.ResultStatus) toolshare.ExecutionResult {
	content := fmt.Sprintf("tool call %s failed: %v", call.Name, err)
	message.Segments = llm.TextSegments(content)
	return toolshare.ExecutionResult{Call: call, Message: message, Error: err, ResultStatus: resultStatus, LogResult: events.LogResult{ResultStatus: resultStatus, Content: toolshare.TruncateLogContent(content, 1024)}}
}
