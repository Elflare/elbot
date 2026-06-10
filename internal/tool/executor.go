package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"elbot/internal/llm"
	"elbot/internal/security"
)

type Executor struct {
	Registry *Registry
	Actor    security.Actor
	Policy   *security.Policy
}

type ExecutionResult struct {
	Call    llm.ToolCallRequest
	Message llm.LLMMessage
	Result  *Result
	Err     error
}

func (e Executor) Execute(ctx context.Context, call llm.ToolCallRequest) ExecutionResult {
	message := llm.LLMMessage{
		Role:       llm.RoleTool,
		Name:       call.Name,
		ToolCallID: call.ID,
	}
	if e.Registry == nil {
		return executionError(call, message, fmt.Errorf("tool registry is not configured"))
	}
	tool, ok := e.Registry.Get(call.Name)
	if !ok {
		return executionError(call, message, fmt.Errorf("tool %q not found", call.Name))
	}
	assessment, err := AssessRisk(ctx, tool, CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
	if err != nil {
		return executionError(call, message, fmt.Errorf("assess risk: %w", err))
	}
	policy := e.Policy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	actor := e.Actor
	if actor.Role == "" {
		actor = security.Actor{Role: security.RoleUser}
	}
	info := tool.Info()
	if info.SuperadminOnly && actor.Role != security.RoleSuperadmin {
		return executionError(call, message, fmt.Errorf("tool %q requires superadmin role", call.Name))
	}
	if !policy.CanUseTool(actor, assessment.Level) {
		return executionError(call, message, fmt.Errorf("risk %s is above your allowed tool level", assessment.Level))
	}
	result, err := tool.Call(ctx, CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
	if err != nil {
		return executionError(call, message, err)
	}
	// 只把 Content/Segments 回灌给 LLM；Result.Data 留给 Agent 内部结构化消费。
	// 避免 discover schema、内部状态或大对象污染上下文。
	message.Segments = result.LLMSegments()
	return ExecutionResult{Call: call, Message: message, Result: result}
}

func executionError(call llm.ToolCallRequest, message llm.LLMMessage, err error) ExecutionResult {
	content := fmt.Sprintf("tool call %s failed: %v", call.Name, err)
	message.Segments = llm.TextSegments(content)
	return ExecutionResult{Call: call, Message: message, Err: err}
}
