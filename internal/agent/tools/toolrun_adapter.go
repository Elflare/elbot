package tools

import (
	"context"
	"elbot/internal/tool"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

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
		ToolCallPrepared(context.Context, agentshare.Session, llm.ToolCallRequest) (string, events.LogResult, error)
		ToolCallCompleted(context.Context, agentshare.Session, agentshare.ToolResult) ([]llm.MessageSegment, events.LogResult, error)
	}
	requests interface {
		ListBySession(string) []request.Request
		Start(context.Context, request.StartRequest) (request.Request, context.Context, func())
	}
	turns interface {
		Snapshot(string) turn.Snapshot
		AddToolUse(string, string, string)
	}
	identity ToolIdentity
	runtime  *ToolRuntimeState
	sessions interface {
		CurrentBound(context.Context, sessionstate.Scope) (*storage.Session, *sessionstate.Binding, error)
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

func (d toolRunDeps) PrepareToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, events.LogResult, error) {
	arguments, logResult, err := d.hooks.ToolCallPrepared(ctx,
		agentshare.Session{ID: session.ID, Mode: session.Mode, Title: session.Title, Status: session.Status}, call)
	if err != nil {
		return call, logResult, err
	}
	call.Arguments = arguments
	return call, logResult, nil
}

func (d toolRunDeps) CompleteToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, risk string, segments []llm.MessageSegment, callErr error) ([]llm.MessageSegment, events.LogResult, error) {
	original := append([]llm.MessageSegment(nil), segments...)
	updated, logResult, err := d.hooks.ToolCallCompleted(ctx,
		agentshare.Session{ID: session.ID, Mode: session.Mode, Title: session.Title, Status: session.Status},
		agentshare.ToolResult{Call: call, Risk: risk, Segments: original, Err: callErr})
	if err != nil {
		return nil, logResult, err
	}
	return updated, logResult, nil
}

func (d toolRunDeps) StartToolRequest(ctx context.Context, sessionID, toolName string) (context.Context, time.Time, func(), events.LogResult, error) {
	toolReq, toolCtx, done := d.requests.Start(ctx, request.StartRequest{ParentID: contextinfo.RootRequestIDFromContext(ctx), SessionID: sessionID, Kind: request.KindTool, Label: toolName})
	d.status.Record(ctx, runtimestatus.Snapshot{
		SessionID:      sessionID,
		Phase:          runtimestatus.PhaseTool,
		RequestID:      toolReq.ID,
		Kind:           request.KindTool,
		Label:          toolName,
		ToolName:       toolName,
		StageStartedAt: toolReq.StartedAt,
	})
	return toolCtx, toolReq.StartedAt, done, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "tool request started"}, nil
}

func (d toolRunDeps) PrepareToolContext(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) context.Context {
	if session == nil {
		return ctx
	}
	ctx = agentshare.FileRollbackContext(ctx, d.runtime.fileRollback, d.sessions, d.turns, d.requests, d.identity, session)
	if agentshare.IsBackgroundSession(session) {
		return ctx
	}
	return workspace.WithWorkspaceStore(ctx, d.NewWorkspaceStore(session.ID))
}

func (d toolRunDeps) ShouldSendPreview(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, assistantText string) bool {
	return d.identity.IsCLI(ctx) || strings.TrimSpace(assistantText) == ""
}

func (d toolRunDeps) ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment tool.RiskAssessment, detail string) (tool.ConfirmResult, error) {
	return d.confirmations.AwaitToolConfirmation(ctx, sessionID, call, assessment, detail)
}

func (d toolRunDeps) SendPreview(ctx context.Context, text ...string) {
	d.output.SendPreview(ctx, text...)
}

func (d toolRunDeps) SendOutputs(ctx context.Context, outputs []delivery.Output) (events.LogResult, error) {
	return d.output.SendOutputs(ctx, outputs)
}

func (d toolRunDeps) AuditToolDenied(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk security.RiskLevel, reason string) {
	content := map[string]string{"actor_id": d.identity.Actor(ctx).ID, "tool": call.Name, "risk": string(risk), "reason": reason}
	for key, value := range content {
		runes := []rune(value)
		if len(runes) > 2048 {
			content[key] = string(runes[:2047]) + "…"
		}
	}
	data, _ := json.Marshal(content)
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.SessionID = sessionID
	agentshare.EmitAgentLog(ctx, facts, events.LogRecord{
		Category:     events.LogAudit,
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

func (d toolRunDeps) ToolCallMessage(sessionID, content, rawText string, calls []llm.ToolCallRequest) storage.Message {
	return agentshare.ToolCallStorageMessage(sessionID, content, rawText, calls)
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
	AwaitToolConfirmation(context.Context, string, llm.ToolCallRequest, tool.RiskAssessment, string) (tool.ConfirmResult, error)
}

// ToolRunDeps declares the application bridge consumed by ToolRun. Per-turn
// output and attempt remain private copies created by ForTurn.
type ToolRunDeps struct {
	Status agentshare.ExecutionStatus
	Hooks  interface {
		ToolCallPrepared(context.Context, agentshare.Session, llm.ToolCallRequest) (string, events.LogResult, error)
		ToolCallCompleted(context.Context, agentshare.Session, agentshare.ToolResult) ([]llm.MessageSegment, events.LogResult, error)
	}
	Requests interface {
		ListBySession(string) []request.Request
		Start(context.Context, request.StartRequest) (request.Request, context.Context, func())
	}
	Turns interface {
		Snapshot(string) turn.Snapshot
		AddToolUse(string, string, string)
	}
	Identity ToolIdentity
	Runtime  *ToolRuntimeState
	Sessions interface {
		CurrentBound(context.Context, sessionstate.Scope) (*storage.Session, *sessionstate.Binding, error)
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
