package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/modelmgr"

	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// ModelContext supplies the prospective main-dialogue target to tool views.
// Execution still resolves and checks its fixed selection at admission.
func (c *ExecutionCoordinator) ModelContext(ctx context.Context, row *storage.Session) (context.Context, error) {
	selected := c.SelectionForTurn(ctx, row)
	return c.view.WithModel(ctx, selected)
}

func (c *ExecutionCoordinator) runAttempt(ctx context.Context, session *storage.Session, text string, out agentshare.Output) (*storage.Session, turn.Input, error) {
	ctx, release, err := c.enterTurn(ctx, session)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, agentshare.ErrSessionBindingChanged) && !errors.Is(err, errExecutionConflict) {
			agentshare.ReportFailure(ctx, session.ID, err, out.Notify)
		}
		return session, turn.Input{}, err
	}
	selection := c.SelectionForTurn(ctx, session)
	source, _, err := sessionpkg.Origin(session)
	if err != nil {
		release()
		agentshare.ReportFailure(ctx, session.ID, err, out.Notify)
		return session, turn.Input{}, err
	}
	err = c.dialogue.CheckSelection(selection)
	if err == nil {
		err = modelmgr.CanSwitch(source, selection.Origin)
	}
	if err != nil {
		release()
		agentshare.ReportFailure(ctx, session.ID, err, out.Notify)
		return session, turn.Input{}, err
	}
	ctx, err = c.view.WithModel(ctx, selection)
	if err != nil {
		release()
		agentshare.ReportFailure(ctx, session.ID, err, out.Notify)
		return session, turn.Input{}, err
	}
	latest, err := c.sessions.RegisterOrigin(ctx, session.ID, selection.Origin)
	if err == nil {
		*session = *latest
	}
	release()
	if err != nil {
		if ctx.Err() == nil {
			agentshare.ReportFailure(ctx, session.ID, fmt.Errorf("登记对话来源: %w", err), out.Notify)
		}
		return session, turn.Input{}, err
	}
	ctx, session, err = c.compactBeforeTurn(ctx, session, text, out, selection)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, agentshare.ErrSessionBindingChanged) {
			agentshare.ReportFailure(ctx, session.ID, fmt.Errorf("轮前压缩: %w", err), out.Notify)
		}
		return session, turn.Input{}, err
	}
	ctx, release, err = c.enterTurn(ctx, session)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, agentshare.ErrSessionBindingChanged) && !errors.Is(err, errExecutionConflict) {
			agentshare.ReportFailure(ctx, session.ID, err, out.Notify)
		}
		return session, turn.Input{}, err
	}
	attempt := storage.NewID()
	ctx = turn.WithAttempt(ctx, attempt)
	execution := turn.ExecutionFromContext(ctx)
	started := c.turns.StartExecution(session.ID, agentshare.InboundTurnInput(ctx, text), execution, attempt)
	if !started {
		release()
		if c.turns.Execution(session.ID) == execution {
			return session, turn.Input{}, nil
		}
		return session, turn.Input{}, errExecutionConflict
	}
	// Restore durable usage before cancellation can publish the first snapshot.
	if _, err := c.status.Usage(session); err != nil {
		c.turns.FinishRequest(session.ID, attempt)
		release()
		return session, turn.Input{}, err
	}
	// Keep the request registered through terminal status and Turn cleanup,
	// including the interval after CompleteLLMInput removes the Turn.
	reqInfo, reqCtx, done := c.requests.Start(ctx, request.StartRequest{SessionID: session.ID, Kind: request.KindTurn, Label: "chat", Timeout: c.responseTimeout})
	release()
	stopObserver := sync.OnceFunc(c.observeRequestStop(reqCtx, session.ID, attempt))
	defer func() {
		stopObserver()
		c.turns.FinishRequest(session.ID, attempt)
		done()
	}()
	result, pending := c.runTurn(ctx, reqCtx, reqInfo.ID, session, text, out, selection)
	stopObserver()
	if err := c.finishAttempt(ctx, session, out, result); err != nil {
		return session, turn.Input{}, err
	}
	return session, pending, nil
}

func (c *ExecutionCoordinator) finishAttempt(ctx context.Context, session *storage.Session, out agentshare.Output, result agentshare.TurnResult) error {
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

func (c *ExecutionCoordinator) runTurn(ctx, reqCtx context.Context, requestID string, session *storage.Session, text string, out agentshare.Output, selection modelmgr.Selection) (agentshare.TurnResult, turn.Input) {
	prepared, err := c.dialogue.PrepareTurn(ctx, agentshare.TurnInput{Session: session, Text: text, Input: agentshare.InboundTurnInput(ctx, text), ReplyToPlatformMessageID: agentshare.InboundReplyMessageID(ctx), Selection: selection})
	if err != nil {
		result := agentshare.TurnResult{Outcome: agentshare.Failed, Err: err}
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			result.Outcome = agentshare.Canceled
		} else {
			agentshare.ReportFailure(ctx, session.ID, err, out.Notify)
		}
		return result, turn.Input{}
	}
	if !c.turns.MatchesAttempt(session.ID, turn.AttemptFromContext(ctx)) || c.turns.Snapshot(session.ID).Phase == turn.PhaseStopping {
		return agentshare.TurnResult{Outcome: agentshare.Canceled, Err: context.Canceled, QuietCancellation: true}, turn.Input{}
	}
	result := c.dialogue.RunTurn(ctx, reqCtx, agentshare.TurnInput{Session: session, Text: text, Selection: selection, RequestID: requestID, Prepared: prepared}, out)
	if result.QuietCancellation {
		c.handleTurnContextDone(ctx, session.ID, reqCtx.Err(), out)
		return result, turn.Input{}
	}
	if result.Outcome != agentshare.Completed {
		return result, turn.Input{}
	}
	pending, err := c.finishCompletedTurn(ctx, session, out, result)
	if err != nil {
		result.Outcome, result.Err = agentshare.Failed, err
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			result.Outcome = agentshare.Canceled
		} else {
			agentshare.ReportFailure(reqCtx, session.ID, err, out.Notify)
		}
	}
	return result, pending
}

// observeRequestStop publishes cancellation without retiring the attempt. The
// execution owner still finishes status and Turn after all synchronous work joins.
func (c *ExecutionCoordinator) observeRequestStop(ctx context.Context, sessionID, attempt string) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		if c.turns.Snapshot(sessionID).Phase == turn.PhaseAwaitAppendConfirm {
			return
		}
		c.turns.StopSession(sessionID, attempt)
		status := c.status.Snapshot(sessionID)
		status.SessionID, status.Phase = sessionID, runtimestatus.PhaseStopping
		c.status.Record(ctx, status)
	})
	return func() {
		if !stop() {
			<-done
		}
	}
}

func (c *ExecutionCoordinator) finishCompletedTurn(ctx context.Context, session *storage.Session, out agentshare.Output, result agentshare.TurnResult) (turn.Input, error) {
	ctx = c.view.Context(ctx)
	selection, turnStartedAt, committed := result.Selection, result.StartedAt, result.Committed
	usage, err := c.status.Usage(session)
	if err != nil {
		return turn.Input{}, err
	}
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
	nextSelection := c.SelectionForTurn(ctx, session)
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

func (c *ExecutionCoordinator) handleTurnContextDone(ctx context.Context, sessionID string, err error, out agentshare.Output) {
	if c.turns.Execution(sessionID) != turn.ExecutionFromContext(ctx) || (c.turns.Snapshot(sessionID).Phase != turn.PhaseAwaitAppendConfirm && c.turns.MatchesAttempt(sessionID, turn.AttemptFromContext(ctx))) {
		if e := turn.ExecutionFromContext(ctx); e != nil {
			e.Finish(err)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		facts.SessionID = sessionID
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
		agentshare.EmitAgentLog(ctx, facts, record)
		content, _ := json.Marshal(map[string]string{"error": fmt.Sprintf("%.2048v", err)})
		record.Category, record.Type, record.Content = events.LogAudit, events.ContentJSON, string(content)
		agentshare.EmitAgentLog(ctx, facts, record)
		out.Notify(context.WithoutCancel(ctx), slog.LevelWarn, c.TurnTimeout)
	}
}
