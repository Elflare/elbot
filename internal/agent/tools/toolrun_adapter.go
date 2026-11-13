package tools

import (
	"context"
	"elbot/internal/operationresult"
	"elbot/internal/tool"
	"encoding/json"
	"log/slog"
	"strings"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/security"
	sessionstate "elbot/internal/session"
	"elbot/internal/storage"

	"elbot/internal/turn"
	"elbot/internal/workspace"
)

type toolRunDeps struct {
	status agentshare.ExecutionStatus
	hooks  interface {
		ToolCallPrepared(context.Context, agentshare.Session, llm.ToolCallRequest) (string, error)
		ToolCallCompleted(context.Context, agentshare.Session, agentshare.ToolResult) ([]llm.MessageSegment, error)
	}
	requests interface {
		ListBySession(string) []request.Request
		Start(context.Context, request.StartRequest) (request.Request, context.Context, func(), error)
	}
	turns interface {
		Snapshot(string) turn.Snapshot
		AddToolUse(string, string, string)
	}
	identity ToolIdentity
	runtime  *ToolRuntimeState
	sessions interface {
		CurrentBound(context.Context, sessionstate.Scope) (operationresult.Result[sessionstate.CurrentBoundData], error)
		EnterBinding(context.Context, *sessionstate.Binding) (context.Context, func(), error)
		EnterSessions(context.Context, ...string) (context.Context, func(), error)
	}
	NewWorkspaceStore func(string) workspace.WorkspaceStore
	confirmations     ToolConfirmations
	view              interface {
		RefreshExecution(context.Context, *storage.Session) (context.Context, error)
	}
	output  agentshare.Output
	attempt string
}

func (d toolRunDeps) ForTurn(out agentshare.Output, attempt string) tool.RunnerDeps {
	d.output, d.attempt = out, attempt
	return d
}

func (d toolRunDeps) PrepareToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, error) {
	arguments, err := d.hooks.ToolCallPrepared(ctx,
		agentshare.Session{ID: session.ID, Mode: session.Mode, Title: session.Title, Status: session.Status}, call)
	if err != nil {
		return call, err
	}
	call.Arguments = arguments
	return call, nil
}

func (d toolRunDeps) CompleteToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, risk string, result operationresult.Result[[]llm.MessageSegment], callErr error) ([]llm.MessageSegment, error) {
	if callErr == nil && result.Status == operationresult.Success {
		result.Data = append([]llm.MessageSegment(nil), result.Data...)
	}
	updated, err := d.hooks.ToolCallCompleted(ctx,
		agentshare.Session{ID: session.ID, Mode: session.Mode, Title: session.Title, Status: session.Status},
		agentshare.ToolResult{Call: call, Risk: risk, Result: result, Err: callErr})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (d toolRunDeps) StartToolRequest(ctx context.Context, sessionID, toolName string) (context.Context, func(), error) {
	toolReq, toolCtx, done, err := d.requests.Start(ctx, request.StartRequest{ParentID: contextinfo.RootRequestIDFromContext(ctx), SessionID: sessionID, Kind: request.KindTool, Label: toolName})
	if err != nil {
		return nil, nil, err
	}
	d.status.Record(ctx, runtimestatus.Snapshot{
		SessionID:      sessionID,
		Phase:          runtimestatus.PhaseTool,
		RequestID:      toolReq.ID,
		Kind:           request.KindTool,
		Label:          toolName,
		ToolName:       toolName,
		StageStartedAt: toolReq.StartedAt,
	})
	return toolCtx, done, nil
}

func (d toolRunDeps) PrepareToolContext(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) context.Context {
	ctx = agentshare.FileRollbackContext(ctx, d.runtime.fileRollback, d.sessions, d.turns, d.requests, d.identity, session)
	if agentshare.IsBackgroundSession(session) {
		return ctx
	}
	return workspace.WithWorkspaceStore(ctx, d.NewWorkspaceStore(session.ID))
}

func (d toolRunDeps) ShouldSendPreview(ctx context.Context, assistantText string) bool {
	return d.identity.IsCLI(ctx) || strings.TrimSpace(assistantText) == ""
}

func (d toolRunDeps) ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment tool.RiskAssessment, detail []tool.ConfirmationDetailBlock) (operationresult.Result[tool.ConfirmResult], error) {
	return d.confirmations.AwaitToolConfirmation(ctx, sessionID, call, assessment, detail)
}

func (d toolRunDeps) SendPreview(ctx context.Context, text ...string) {
	d.output.SendPreview(ctx, text...)
}

func (d toolRunDeps) SendOutputs(ctx context.Context, outputs []delivery.Output) (operationresult.Result[struct{}], error) {
	target, err := delivery.ValidateOutputsTarget(outputs)
	if err != nil {
		return operationresult.Result[struct{}]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	sent, err := d.output.SendTo(ctx, target, outputs)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	return operationresult.Result[struct{}]{Status: sent.Status, Message: sent.Message}, nil
}
func (d toolRunDeps) PublishToolResult(ctx context.Context, toolCallID string, result delivery.ToolResult) {
	_, _ = d.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamToolResult, ToolCallID: toolCallID, ToolResult: &result})
}

func (d toolRunDeps) AuditToolDenied(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk security.RiskLevel, reason string) {
	content := map[string]any{"actor": d.identity.Actor(ctx)}
	for key, value := range map[string]string{"tool": call.Name, "risk": string(risk), "reason": reason} {
		runes := []rune(value)
		if len(runes) > 2048 {
			value = string(runes[:2047]) + "…"
		}
		content[key] = value
	}
	data, _ := json.Marshal(content)
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.SessionID = sessionID
	agentshare.EmitAgentLog(ctx, facts, events.LogRecord{
		Category:     events.LogCategoryAudit,
		Level:        slog.LevelInfo,
		EventName:    "permission_denied",
		Module:       "agent",
		Type:         events.ContentJSON,
		Content:      string(data),
		ContentLimit: 0,
		Identity:     events.LogIdentity{},
		ResultStatus: events.ResultRejected,
	})
}

func (d toolRunDeps) AddToolUse(sessionID, toolName string) {
	d.turns.AddToolUse(sessionID, toolName, d.attempt)
}

func (d toolRunDeps) ToolResultMessage(sessionID string, message llm.LLMMessage) storage.Message {
	return agentshare.ToolResultStorageMessage(sessionID, message)
}

func (d toolRunDeps) ToolCallMessage(sessionID, content, rawText, reasoningContent string, calls []llm.ToolCallRequest) storage.Message {
	return agentshare.ToolCallStorageMessage(sessionID, content, rawText, calls, reasoningContent)
}

func (d toolRunDeps) RefreshExecution(ctx context.Context, row *storage.Session) (context.Context, error) {
	return d.view.RefreshExecution(ctx, row)
}

type ToolIdentity interface {
	Scope(context.Context) sessionstate.Scope
	Actor(context.Context) contextinfo.Actor
	IsCLI(context.Context) bool
}

type ToolConfirmations interface {
	AwaitToolConfirmation(context.Context, string, llm.ToolCallRequest, tool.RiskAssessment, []tool.ConfirmationDetailBlock) (operationresult.Result[tool.ConfirmResult], error)
}

// ToolRunDeps declares the application bridge consumed by ToolRun. Per-turn
// output and attempt remain private copies created by ForTurn.
type ToolRunDeps struct {
	Status agentshare.ExecutionStatus
	Hooks  interface {
		ToolCallPrepared(context.Context, agentshare.Session, llm.ToolCallRequest) (string, error)
		ToolCallCompleted(context.Context, agentshare.Session, agentshare.ToolResult) ([]llm.MessageSegment, error)
	}
	Requests interface {
		ListBySession(string) []request.Request
		Start(context.Context, request.StartRequest) (request.Request, context.Context, func(), error)
	}
	Turns interface {
		Snapshot(string) turn.Snapshot
		AddToolUse(string, string, string)
	}
	Identity ToolIdentity
	Runtime  *ToolRuntimeState
	Sessions interface {
		CurrentBound(context.Context, sessionstate.Scope) (operationresult.Result[sessionstate.CurrentBoundData], error)
		EnterBinding(context.Context, *sessionstate.Binding) (context.Context, func(), error)
		EnterSessions(context.Context, ...string) (context.Context, func(), error)
	}
	NewWorkspaceStore func(string) workspace.WorkspaceStore
	Confirmations     ToolConfirmations
	View              interface {
		RefreshExecution(context.Context, *storage.Session) (context.Context, error)
	}
}

func NewToolRunDeps(deps ToolRunDeps) agentshare.ToolDepsFactory {
	return &toolRunDeps{
		status:            deps.Status,
		hooks:             deps.Hooks,
		requests:          deps.Requests,
		turns:             deps.Turns,
		identity:          deps.Identity,
		runtime:           deps.Runtime,
		sessions:          deps.Sessions,
		NewWorkspaceStore: deps.NewWorkspaceStore,
		confirmations:     deps.Confirmations,
		view:              deps.View,
	}
}
