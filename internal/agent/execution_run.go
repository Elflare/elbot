package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/modelmgr"
	notificationrules "elbot/internal/notification/rules"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// ModelContext supplies the prospective main-dialogue target to tool views.
// Execution still resolves and checks its fixed selection at admission.
func (c *ExecutionCoordinator) ModelContext(ctx context.Context, row *storage.Session) (context.Context, error) {
	selected := modelmgr.SelectionForTurn(ctx, c.models, row)
	return c.view.WithModel(ctx, selected)
}

func (c *ExecutionCoordinator) runAttempt(ctx context.Context, session *storage.Session, text string, out dialogue.Output) (*storage.Session, turn.Input, error) {
	ctx, release, err := c.enterTurn(ctx, session)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, errSessionBindingChanged) && !errors.Is(err, errExecutionConflict) {
			dialogue.ReportFailure(ctx, session.ID, err, out.Notify)
		}
		return session, turn.Input{}, err
	}
	selection := modelmgr.SelectionForTurn(ctx, c.models, session)
	source, _, err := sessionpkg.Origin(session)
	if err != nil {
		release()
		dialogue.ReportFailure(ctx, session.ID, err, out.Notify)
		return session, turn.Input{}, err
	}
	err = c.dialogue.CheckSelection(selection)
	if err == nil {
		err = modelmgr.CanSwitch(source, selection.Origin)
	}
	if err != nil {
		release()
		dialogue.ReportFailure(ctx, session.ID, err, out.Notify)
		return session, turn.Input{}, err
	}
	ctx, err = c.view.WithModel(ctx, selection)
	if err != nil {
		release()
		dialogue.ReportFailure(ctx, session.ID, err, out.Notify)
		return session, turn.Input{}, err
	}
	latest, err := c.sessions.RegisterOrigin(ctx, session.ID, selection.Origin)
	if err == nil {
		*session = *latest
	}
	release()
	if err != nil {
		if ctx.Err() == nil {
			dialogue.ReportFailure(ctx, session.ID, fmt.Errorf("登记对话来源: %w", err), out.Notify)
		}
		return session, turn.Input{}, err
	}
	ctx, session, err = c.compactBeforeTurn(ctx, session, text, out, selection)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, errSessionBindingChanged) {
			dialogue.ReportFailure(ctx, session.ID, fmt.Errorf("轮前压缩: %w", err), out.Notify)
		}
		return session, turn.Input{}, err
	}
	ctx, release, err = c.enterTurn(ctx, session)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, errSessionBindingChanged) && !errors.Is(err, errExecutionConflict) {
			dialogue.ReportFailure(ctx, session.ID, err, out.Notify)
		}
		return session, turn.Input{}, err
	}
	attempt := storage.NewID()
	ctx = turn.WithAttempt(ctx, attempt)
	execution := turn.ExecutionFromContext(ctx)
	started := c.turns.StartExecution(session.ID, inboundTurnInput(ctx, text), execution, attempt)
	release()
	if !started {
		if c.turns.Execution(session.ID) == execution {
			return session, turn.Input{}, nil
		}
		return session, turn.Input{}, errExecutionConflict
	}
	defer c.turns.FinishRequest(session.ID, attempt)

	result, pending := c.runTurn(ctx, session, text, out, selection)
	if err := c.finishAttempt(ctx, session, out, result); err != nil {
		return session, turn.Input{}, err
	}
	return session, pending, nil
}

func (c *ExecutionCoordinator) finishAttempt(ctx context.Context, session *storage.Session, out dialogue.Output, result dialogue.TurnResult) error {
	attempt := turn.AttemptFromContext(ctx)
	execution := turn.ExecutionFromContext(ctx)
	if err := result.Err; err != nil && !result.QuietCancellation {
		if !c.turns.MatchesAttempt(session.ID, attempt) || c.turns.Snapshot(session.ID).Phase == turn.PhaseAwaitAppendConfirm {
			return nil
		}
		execution.Finish(err)
		status := c.status.Snapshot(session.ID)
		status.Phase = runtimestatus.PhaseError
		status.FinishedAt = storage.Now()
		status.Error = err.Error()
		c.status.Record(ctx, status)
		c.turns.StopSession(session.ID, attempt)
		return err
	}
	status := c.status.Snapshot(session.ID)
	if status.Running() && (c.turns.Snapshot(session.ID).Phase == turn.PhaseIdle || c.turns.MatchesAttempt(session.ID, attempt)) {
		c.status.Record(ctx, runtimeDoneStatus(status, storage.Now()))
	}
	return nil
}

func (c *ExecutionCoordinator) runTurn(ctx context.Context, session *storage.Session, text string, out dialogue.Output, selection modelmgr.Selection) (dialogue.TurnResult, turn.Input) {
	prepared, err := c.dialogue.PrepareTurn(ctx, dialogue.TurnInput{Session: session, Text: text, Input: inboundTurnInput(ctx, text), ReplyToPlatformMessageID: inboundReplyMessageID(ctx), Selection: selection})
	if err != nil {
		result := dialogue.TurnResult{Outcome: dialogue.Failed, Err: err}
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			result.Outcome = dialogue.Canceled
		} else {
			dialogue.ReportFailure(ctx, session.ID, err, out.Notify)
		}
		return result, turn.Input{}
	}
	locked, releaseRequest, err := c.sessions.EnterSessions(ctx, session.ID)
	if err != nil {
		result := dialogue.TurnResult{Outcome: dialogue.Failed, Err: err}
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			result.Outcome = dialogue.Canceled
		} else {
			dialogue.ReportFailure(ctx, session.ID, err, out.Notify)
		}
		return result, turn.Input{}
	}
	if !c.turns.MatchesAttempt(session.ID, turn.AttemptFromContext(ctx)) {
		releaseRequest()
		return dialogue.TurnResult{Outcome: dialogue.Canceled, Err: context.Canceled}, turn.Input{}
	}
	reqCtxInfo, reqCtx, done := c.requests.Start(locked, request.StartRequest{SessionID: session.ID, Kind: request.KindTurn, Label: "chat", Timeout: c.responseTimeout})
	releaseRequest()
	defer done()

	result := c.dialogue.RunTurn(ctx, reqCtx, dialogue.TurnInput{Session: session, Text: text, Selection: selection, RequestID: reqCtxInfo.ID, Prepared: prepared}, out)
	if result.QuietCancellation {
		c.handleTurnContextDone(ctx, session.ID, reqCtx.Err(), out)
		return result, turn.Input{}
	}
	if result.Outcome != dialogue.Completed {
		return result, turn.Input{}
	}
	pending, err := c.finishCompletedTurn(ctx, session, out, result)
	if err != nil {
		result.Outcome, result.Err = dialogue.Failed, err
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			result.Outcome = dialogue.Canceled
		} else {
			dialogue.ReportFailure(reqCtx, session.ID, err, out.Notify)
		}
	}
	return result, pending
}

func (c *ExecutionCoordinator) finishCompletedTurn(ctx context.Context, session *storage.Session, out dialogue.Output, result dialogue.TurnResult) (turn.Input, error) {
	ctx = c.view.Context(ctx)
	selection, usage, turnStartedAt, committed := result.Selection, result.Usage, result.StartedAt, result.Committed
	if err := c.contexts.RecordTurn(ctx, session.ID, usage); err != nil {
		return turn.Input{}, fmt.Errorf("保存会话活动时间和用量: %w", err)
	}
	doneStatus := runtimeDoneStatus(runtimestatus.Snapshot{
		SessionID:      session.ID,
		Provider:       selection.Provider,
		Model:          selection.Model,
		Mode:           session.Mode,
		TurnStartedAt:  turnStartedAt,
		StageStartedAt: turnStartedAt,
		Usage:          usage,
	}, storage.Now())
	c.status.Record(ctx, doneStatus)
	nextSelection := modelmgr.SelectionForTurn(ctx, c.models, session)
	if c.shouldCompact(ctx, session, nextSelection) {
		_, _, _ = out.SendAssistant(ctx, "compact status: will compact before next request")
	}
	pending, completed := c.turns.CompleteLLMInput(session.ID, turn.AttemptFromContext(ctx))
	if !completed {
		return turn.Input{}, nil
	}
	if execution := turn.ExecutionFromContext(ctx); execution != nil {
		execution.SetResult(session.ID, committed.MessageID, committed.RawText)
	}
	ctx = contextinfo.WithModel(ctx, contextinfo.Model{Provider: selection.Provider, Model: selection.Model, APIType: string(selection.Origin.APIType)})
	c.sessions.MaybeScheduleNaming(ctx, session.ID)
	return pending, nil
}

func (c *ExecutionCoordinator) handleTurnContextDone(ctx context.Context, sessionID string, err error, out dialogue.Output) {
	if c.turns.Execution(sessionID) != turn.ExecutionFromContext(ctx) || (c.turns.Snapshot(sessionID).Phase != turn.PhaseAwaitAppendConfirm && c.turns.MatchesAttempt(sessionID, turn.AttemptFromContext(ctx))) {
		if e := turn.ExecutionFromContext(ctx); e != nil {
			e.Finish(err)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		record := events.LogRecord{
			Category:     events.LogRuntime,
			Level:        slog.LevelError,
			EventName:    "turn_response_timeout",
			Module:       "agent",
			Type:         events.ContentText,
			Content:      fmt.Sprintf("turn response timeout: %v", err),
			ContentLimit: 4096,
			Identity:     events.LogIdentity{},
			ResultStatus: events.ResultFailed,
		}
		emitAgentLog(ctx, agentevents.Meta(ctx, sessionID), record)
		content, _ := json.Marshal(map[string]string{"error": fmt.Sprintf("%.2048v", err)})
		record.Category, record.Type, record.Content = events.LogAudit, events.ContentJSON, string(content)
		emitAgentLog(ctx, agentevents.Meta(ctx, sessionID), record)
		out.Notify(context.WithoutCancel(ctx), slog.LevelWarn, notificationrules.TurnTimeout)
	}
}
