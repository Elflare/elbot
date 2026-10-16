package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/security"
	sessionstate "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
	"elbot/internal/workspace"
)

type toolRunDeps struct {
	status        ExecutionStatus
	hooks         dialogue.Hooks
	requests      *request.Manager
	turns         *turn.Manager
	identity      ToolIdentity
	state         *toolrun.StateService
	runtime       *ToolRuntimeState
	sessions      *sessionstate.Service
	sessionRows   storage.SessionRepository
	toolCalls     storage.ToolCallRepository
	confirmations ToolConfirmations
	view          dialogue.ExecutionView
	output        dialogue.Output
	attempt       string
}

func (d toolRunDeps) ForTurn(out dialogue.Output, attempt string) toolrun.RunnerDeps {
	d.output, d.attempt = out, attempt
	return d
}

func (d toolRunDeps) PrepareToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, events.LogResult, error) {
	event, logResult, err := d.hooks.Run(ctx, hook.Event{
		Point:   hook.PointToolCallPrepared,
		Session: hookSession(session),
		Tool:    hook.ToolPayload{ID: call.ID, Name: call.Name, Arguments: call.Arguments},
	})
	if err != nil {
		return call, logResult, err
	}
	call.Arguments = event.Tool.Arguments
	return call, logResult, nil
}

func (d toolRunDeps) CompleteToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, risk string, segments []llm.MessageSegment, callErr error) ([]llm.MessageSegment, events.LogResult, error) {
	original := append([]llm.MessageSegment(nil), segments...)
	resultText := llm.SegmentsTextOnly(original)
	event, logResult, err := d.hooks.Run(ctx, hook.Event{
		Point:   hook.PointToolCallCompleted,
		Session: hookSession(session),
		Message: hook.MessagePayload{Role: string(llm.RoleTool), Segments: original},
		Tool: hook.ToolPayload{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
			Risk:      risk,
			Result:    resultText,
			Error:     callErr,
		},
	})
	if err != nil {
		return nil, logResult, err
	}
	if !reflect.DeepEqual(event.Message.Segments, original) {
		return event.Message.Segments, logResult, nil
	}
	if event.Tool.Result != resultText {
		return llm.SetSegmentText(original, event.Tool.Result), logResult, nil
	}
	return original, logResult, nil
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
	ctx = fileRollbackContext(ctx, d.runtime.fileRollback, d.sessions, d.turns, d.requests, d.identity, session)
	if isBackgroundSession(session) {
		return ctx
	}
	return workspace.WithWorkspaceStore(ctx, sessionstate.NewWorkspaceStore(d.sessions, d.sessionRows, session.ID))
}

func (d toolRunDeps) ShouldSendPreview(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, assistantText string) bool {
	return d.identity.IsCLI(ctx) || strings.TrimSpace(assistantText) == ""
}

func (d toolRunDeps) ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment tool.RiskAssessment, detail string) (toolrun.ConfirmResult, error) {
	return d.confirmations.AwaitToolConfirmation(ctx, sessionID, call, assessment, detail)
}

func (d toolRunDeps) ConfirmBackgroundTool(ctx context.Context, sessionID string, call llm.ToolCallRequest, resolved toolrun.ResolvedTool, assessment tool.RiskAssessment) (toolrun.ConfirmResult, bool) {
	message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID}
	allowed, handled := d.confirmations.ConfirmBackgroundSandboxShell(ctx, sessionID, call, assessment.Level, &message)
	if !handled {
		return toolrun.ConfirmResult{}, false
	}
	return toolrun.ConfirmResult{Allowed: allowed, Message: message}, true
}

func (d toolRunDeps) SendPreview(ctx context.Context, text ...string) {
	d.output.SendPreview(ctx, text...)
}

func (d toolRunDeps) SendOutputs(ctx context.Context, outputs []delivery.Output) (events.LogResult, error) {
	return d.output.SendOutputs(ctx, outputs)
}

func (d toolRunDeps) RecordToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk string, startedAt time.Time, result string, callErr error) (events.LogResult, error) {
	record := &storage.ToolCallRecord{
		SessionID:  sessionID,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		ActorID:    d.identity.Actor(ctx).ID,
		RiskLevel:  risk,
		Success:    callErr == nil,
		StartedAt:  startedAt,
		FinishedAt: storage.Now(),
	}
	if callErr != nil {
		record.Error = callErr.Error()
	}
	logResult := events.LogResult{ResultStatus: events.ResultSucceeded, Content: "tool call record saved"}
	recordErr := d.toolCalls.Create(ctx, record)
	if recordErr != nil {
		logResult.ResultStatus = events.ResultFailed
		logResult.Content = fmt.Sprintf("record_error=%v", recordErr)
		if errors.Is(recordErr, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
	}

	return logResult, recordErr
}

func (d toolRunDeps) AuditToolDenied(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk tool.RiskLevel, reason string) {
	content := map[string]string{"actor_id": d.identity.Actor(ctx).ID, "tool": call.Name, "risk": string(risk), "reason": reason}
	for key, value := range content {
		runes := []rune(value)
		if len(runes) > 2048 {
			content[key] = string(runes[:2047]) + "…"
		}
	}
	data, _ := json.Marshal(content)
	emitAgentLog(ctx, agentevents.Meta(ctx, sessionID), events.LogRecord{
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

func (d toolRunDeps) RememberDiscoveryResult(ctx context.Context, row *storage.Session, result *tool.Result) (events.LogResult, error) {
	if row == nil {
		return events.LogResult{ResultStatus: events.ResultSkipped, Content: "session is absent"}, nil
	}
	update, err := toolrun.DiscoveryStateUpdate(ctx, result, d.runtime.registry, d.identity.Actor(ctx), d.identity.Policy())
	if err != nil {
		return events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	_, logResult, err := commitToolState(ctx, d.state, row, update)
	return logResult, err
}

func (d toolRunDeps) AddToolUse(sessionID, toolName string) {
	d.turns.AddToolUse(sessionID, toolName, d.attempt)
}

func (d toolRunDeps) ToolResultMessage(sessionID string, message llm.LLMMessage) storage.Message {
	return dialogue.ToolResultStorageMessage(sessionID, message)
}

func (d toolRunDeps) ToolCallMessage(sessionID, content, rawText string, calls []llm.ToolCallRequest) storage.Message {
	return dialogue.ToolCallStorageMessage(sessionID, content, rawText, calls)
}

func (d toolRunDeps) RefreshExecution(ctx context.Context, row *storage.Session) (context.Context, error) {
	return d.view.RefreshExecution(ctx, row)
}

type ToolIdentity interface {
	dialogue.Identity
	Policy() *security.Policy
}

type ToolConfirmations interface {
	AwaitToolConfirmation(context.Context, string, llm.ToolCallRequest, tool.RiskAssessment, string) (toolrun.ConfirmResult, error)
	ConfirmBackgroundSandboxShell(context.Context, string, llm.ToolCallRequest, tool.RiskLevel, *llm.LLMMessage) (bool, bool)
}

// ToolRunDeps declares the application bridge consumed by ToolRun. Per-turn
// output and attempt remain private copies created by ForTurn.
type ToolRunDeps struct {
	Status        ExecutionStatus
	Hooks         dialogue.Hooks
	Requests      *request.Manager
	Turns         *turn.Manager
	Identity      ToolIdentity
	State         *toolrun.StateService
	Runtime       *ToolRuntimeState
	Sessions      *sessionstate.Service
	SessionRows   storage.SessionRepository
	ToolCalls     storage.ToolCallRepository
	Confirmations ToolConfirmations
	View          dialogue.ExecutionView
}

func NewToolRunDeps(deps ToolRunDeps) dialogue.ToolDepsFactory {
	return &toolRunDeps{
		status:        deps.Status,
		hooks:         deps.Hooks,
		requests:      deps.Requests,
		turns:         deps.Turns,
		identity:      deps.Identity,
		state:         deps.State,
		runtime:       deps.Runtime,
		sessions:      deps.Sessions,
		sessionRows:   deps.SessionRows,
		toolCalls:     deps.ToolCalls,
		confirmations: deps.Confirmations,
		view:          deps.View,
	}
}
