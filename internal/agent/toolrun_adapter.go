package agent

import (
	"context"
	"reflect"
	"strings"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	sessionstate "elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
	"elbot/internal/workspace"
)

type toolRunDeps struct {
	hooks         *hookBridge
	requests      *request.Manager
	turns         *turn.Manager
	identity      *identityResolver
	media         *media.Manager
	state         *toolrun.StateService
	runtime       *toolRuntimeState
	sessions      *sessionstate.Service
	store         storage.Store
	confirmations *confirmationCoordinator
	view          executionView
	output        turnOutput
	attempt       string
	completed     *signal.Signal[ToolCallCompletedEvent]
	denied        *signal.Signal[ToolDeniedEvent]
}

func (d toolRunDeps) forTurn(out turnOutput, attempt string) toolRunDeps {
	d.output, d.attempt = out, attempt
	return d
}

func (d toolRunDeps) PrepareToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, error) {
	event, err := d.hooks.Run(ctx, hook.Event{
		Point:   hook.PointToolCallPrepared,
		Session: hookSession(session),
		Tool:    hook.ToolPayload{ID: call.ID, Name: call.Name, Arguments: call.Arguments},
	})
	if err != nil {
		return call, err
	}
	call.Arguments = event.Tool.Arguments
	return call, nil
}

func (d toolRunDeps) CompleteToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, risk string, segments []llm.MessageSegment, callErr error) ([]llm.MessageSegment, error) {
	original := append([]llm.MessageSegment(nil), segments...)
	resultText := llm.SegmentsTextOnly(original)
	event, err := d.hooks.Run(ctx, hook.Event{
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
		return nil, err
	}
	if !reflect.DeepEqual(event.Message.Segments, original) {
		return materializeMedia(ctx, d.media, event.Message.Segments), nil
	}
	if event.Tool.Result != resultText {
		return materializeMedia(ctx, d.media, llm.SetSegmentText(original, event.Tool.Result)), nil
	}
	return materializeMedia(ctx, d.media, original), nil
}

func (d toolRunDeps) StartToolRequest(ctx context.Context, sessionID, toolName string) (context.Context, time.Time, func(), error) {
	toolReq, toolCtx, done, err := d.requests.Start(ctx, request.StartRequest{ParentID: turnRequestIDFromContext(ctx), SessionID: sessionID, Kind: request.KindTool, Label: toolName})
	if err != nil {
		return ctx, time.Time{}, func() {}, err
	}
	d.output.PublishRuntimeStatus(ctx, runtimestatus.Snapshot{SessionID: sessionID, Phase: runtimestatus.PhaseTool, RequestID: toolReq.ID, Kind: request.KindTool, Label: toolName, ToolName: toolName, StageStartedAt: toolReq.StartedAt})
	return toolCtx, toolReq.StartedAt, done, nil
}

func (d toolRunDeps) PrepareToolContext(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) context.Context {
	if session == nil {
		return ctx
	}
	state, err := toolrun.DecodeState(session.Metadata)
	if err == nil {
		ctx = tool.WithShownRuleCardFormats(ctx, state.ShownRuleCardFormats)
	}
	ctx = fileRollbackContext(ctx, d.runtime.fileRollback, d.sessions, d.turns, d.requests, d.identity, session)
	if isBackgroundSession(session) {
		return ctx
	}
	return workspace.WithWorkspaceStore(ctx, sessionstate.NewWorkspaceStore(d.sessions, d.store.Sessions(), session.ID))
}

func (d toolRunDeps) ShouldSendPreview(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, assistantText string) bool {
	return d.identity.IsCLI(ctx) || strings.TrimSpace(assistantText) == ""
}

func (d toolRunDeps) ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment tool.RiskAssessment, detail string) (toolrun.ConfirmResult, error) {
	return d.confirmations.AwaitToolConfirmation(ctx, sessionID, call, assessment, detail)
}

func (d toolRunDeps) ConfirmBackgroundTool(ctx context.Context, sessionID string, call llm.ToolCallRequest, resolved toolrun.ResolvedTool, assessment tool.RiskAssessment) (toolrun.ConfirmResult, bool) {
	message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID}
	allowed, handled := d.confirmations.confirmBackgroundSandboxShell(ctx, sessionID, call, assessment.Level, &message)
	if !handled {
		return toolrun.ConfirmResult{}, false
	}
	return toolrun.ConfirmResult{Allowed: allowed, Message: message}, true
}

func (d toolRunDeps) SendPreview(ctx context.Context, text string) {
	d.output.SendPreview(ctx, text)
}

func (d toolRunDeps) SendOutputs(ctx context.Context, outputs []delivery.Output) error {
	return d.output.SendOutputs(ctx, outputs)
}

func (d toolRunDeps) RecordToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk string, startedAt time.Time, result string, callErr error) {
	record := &storage.ToolCallRecord{
		SessionID:     sessionID,
		ToolCallID:    call.ID,
		ToolName:      call.Name,
		ActorID:       d.identity.Actor(ctx).ID,
		RiskLevel:     risk,
		Success:       callErr == nil,
		ResultPreview: previewLogText(result),
		StartedAt:     startedAt,
		FinishedAt:    storage.Now(),
	}
	if callErr != nil {
		record.Error = callErr.Error()
	}
	var recordErr error
	if d.store != nil && d.store.ToolCalls() != nil {
		recordErr = d.store.ToolCalls().Create(ctx, record)
	}
	emitFact(ctx, d.completed, ToolCallCompletedEvent{EventMeta: eventMeta(ctx, sessionID), Record: *record, Arguments: call.Arguments, RecordErr: recordErr})
}

func (d toolRunDeps) AuditToolDenied(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk tool.RiskLevel, reason string) {
	emitFact(ctx, d.denied, ToolDeniedEvent{EventMeta: eventMeta(ctx, sessionID), ActorID: d.identity.Actor(ctx).ID, Tool: call.Name, Risk: string(risk), Reason: reason})
}

func (d toolRunDeps) RememberDiscoveryResult(ctx context.Context, row *storage.Session, result *tool.Result) error {
	if row == nil {
		return nil
	}
	update, err := toolrun.DiscoveryStateUpdate(ctx, result, d.runtime.registry, d.identity.Actor(ctx), d.identity.policy)
	if err != nil {
		return err
	}
	_, err = commitToolState(ctx, d.state, row, update)
	return err
}

func (d toolRunDeps) AddToolUse(sessionID, toolName string) {
	d.turns.AddToolUse(sessionID, toolName, d.attempt)
}

func (d toolRunDeps) ToolResultMessage(sessionID string, message llm.LLMMessage) storage.Message {
	return toolResultStorageMessage(sessionID, message)
}

func (d toolRunDeps) ToolCallMessage(sessionID, content, rawText string, calls []llm.ToolCallRequest) storage.Message {
	return toolCallStorageMessage(sessionID, content, rawText, calls)
}

func (d toolRunDeps) PersistedToolMessage(message llm.LLMMessage) llm.LLMMessage {
	return persistedToolMessage(message)
}

func (d toolRunDeps) RefreshExecution(ctx context.Context, row *storage.Session) (context.Context, error) {
	return d.view.RefreshSession(ctx, row)
}
