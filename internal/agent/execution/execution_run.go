package execution

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
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

func (c *ExecutionCoordinator) runAttempt(ctx context.Context, session *storage.Session, text string, out agentshare.Output) (context.Context, *storage.Session, turn.Input, error) {
	ctx, release, err := c.enterTurn(ctx, session)
	if err != nil {
		return ctx, session, turn.Input{}, err
	}
	selection := c.SelectionForTurn(ctx, session)
	source, _, err := sessionpkg.Origin(session)
	if err != nil {
		release()
		return ctx, session, turn.Input{}, err
	}
	err = c.dialogue.CheckSelection(selection)
	if err == nil {
		err = modelmgr.CanSwitch(source, selection.Origin)
	}
	if err != nil {
		release()
		return ctx, session, turn.Input{}, err
	}
	ctx, err = c.view.WithModel(ctx, selection)
	if err != nil {
		release()
		return ctx, session, turn.Input{}, err
	}
	latest, err := c.sessions.RegisterOrigin(ctx, session.ID, selection.Origin)
	if err == nil {
		*session = *latest
	}
	release()
	if err != nil {
		return ctx, session, turn.Input{}, err
	}
	ctx, session, err = c.compactBeforeTurn(ctx, session, text, out, selection)
	if err != nil {
		return ctx, session, turn.Input{}, err
	}
	ctx, release, err = c.enterTurn(ctx, session)
	if err != nil {
		return ctx, session, turn.Input{}, err
	}
	attempt, err := c.turns.AllocateAttempt(ctx)
	if err != nil {
		release()
		return ctx, session, turn.Input{}, err
	}
	ctx = turn.WithAttempt(ctx, attempt)
	execution := turn.ExecutionFromContext(ctx)
	started := c.turns.StartAttempt(session.ID, agentshare.InboundTurnInput(ctx, text), execution, attempt)
	if !started {
		release()
		if c.turns.Snapshot(session.ID).Execution == execution {
			return ctx, session, turn.Input{}, nil
		}
		return ctx, session, turn.Input{}, errExecutionConflict
	}
	// Restore durable usage before cancellation can publish the first snapshot.
	if _, err := c.status.Usage(session); err != nil {
		c.turns.FinishAttempt(session.ID, attempt)
		release()
		return ctx, session, turn.Input{}, err
	}
	// Keep the request registered through terminal status and Turn cleanup,
	// including the interval after CompleteTurn removes the Turn.
	reqInfo, reqCtx, done, err := c.requests.Start(ctx, request.StartRequest{SessionID: session.ID, Kind: request.KindTurn, Label: "chat", Timeout: c.responseTimeout})
	if err != nil {
		c.turns.FinishAttempt(session.ID, attempt)
		release()
		return ctx, session, turn.Input{}, err
	}
	release()
	stopObserver := sync.OnceFunc(c.observeRequestStop(reqCtx, session.ID, attempt))
	defer func() {
		stopObserver()
		c.turns.FinishAttempt(session.ID, attempt)
		done()
	}()
	reqCtx, result, pending := c.runTurn(ctx, reqCtx, reqInfo.ID, session, text, out, selection)
	stopObserver()
	// Request lifetime ends below; return its facts on the execution lifetime.
	facts, _ := contextinfo.ExecutionFromContext(reqCtx)
	ctx = c.view.Context(contextinfo.WithExecution(ctx, facts))
	if model, ok := contextinfo.ModelFromContext(reqCtx); ok {
		ctx = contextinfo.WithModel(ctx, model)
	}
	if err := c.finishAttempt(ctx, session, result); err != nil {
		return ctx, session, turn.Input{}, err
	}
	return ctx, session, pending, nil
}

func (c *ExecutionCoordinator) finishAttempt(ctx context.Context, session *storage.Session, result agentshare.TurnResult) error {
	attempt := turn.AttemptFromContext(ctx)
	execution := turn.ExecutionFromContext(ctx)
	current := c.turns.Snapshot(session.ID)
	if err := result.Err; err != nil {
		if current.Attempt != attempt || current.Phase == turn.PhaseAwaitAppendConfirm {
			return nil
		}
		execution.Finish(err)
		status := c.status.Snapshot(session.ID)
		status.Phase = runtimestatus.PhaseError
		if result.Outcome == agentshare.Canceled {
			status = runtimeDoneStatus(status, storage.Now())
		}
		status.FinishedAt = storage.Now()
		status.Error = err.Error()
		c.status.Record(ctx, status)
		c.turns.StopAttempt(session.ID, attempt)
		return err
	}
	status := c.status.Snapshot(session.ID)
	if status.Running() && (current.Phase == turn.PhaseIdle || current.Attempt == attempt) {
		c.status.Record(ctx, runtimeDoneStatus(status, storage.Now()))
	}
	return nil
}

func (c *ExecutionCoordinator) runTurn(ctx, reqCtx context.Context, requestID string, session *storage.Session, text string, out agentshare.Output, selection modelmgr.Selection) (context.Context, agentshare.TurnResult, turn.Input) {
	prepared, err := c.dialogue.PrepareTurn(ctx, agentshare.TurnInput{Session: session, Text: text, Input: agentshare.InboundTurnInput(ctx, text), ReplyToPlatformMessageID: agentshare.InboundReplyMessageID(ctx), Selection: selection})
	if err != nil {
		result := agentshare.TurnResult{Outcome: agentshare.Failed, Err: err}
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			result.Outcome = agentshare.Canceled
		}
		return reqCtx, result, turn.Input{}
	}
	current := c.turns.Snapshot(session.ID)
	if current.Attempt != turn.AttemptFromContext(ctx) || current.Phase == turn.PhaseStopping {
		return reqCtx, agentshare.TurnResult{Outcome: agentshare.Canceled, Err: context.Canceled, QuietCancellation: true}, turn.Input{}
	}
	reqCtx, result := c.dialogue.RunTurn(ctx, reqCtx, agentshare.TurnInput{Session: session, Text: text, Selection: selection, RequestID: requestID, Prepared: prepared}, out)
	if result.QuietCancellation {
		c.handleTurnContextDone(ctx, session.ID, reqCtx.Err())
		return reqCtx, result, turn.Input{}
	}
	if result.Outcome != agentshare.Completed {
		return reqCtx, result, turn.Input{}
	}
	pending, err := c.finishCompletedTurn(ctx, session, out, result)
	if err != nil {
		result.Outcome, result.Err = agentshare.Failed, err
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			result.Outcome = agentshare.Canceled
		}
	}
	return reqCtx, result, pending
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
		c.turns.StopAttempt(sessionID, attempt)
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
		if _, err := out.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "compact → next request"}); err != nil {
			record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelWarn, EventName: "compact_notice_failed", Module: "agent", Content: err.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err)}
			if record.ResultStatus == events.ResultCanceled {
				record.Level = slog.LevelInfo
			}
			events.AsyncLog(ctx, record)
		}
	}
	pending, completed := c.turns.CompleteTurn(session.ID, turn.AttemptFromContext(ctx))
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

func (c *ExecutionCoordinator) handleTurnContextDone(ctx context.Context, sessionID string, err error) {
	current := c.turns.Snapshot(sessionID)
	if current.Execution != turn.ExecutionFromContext(ctx) || (current.Phase != turn.PhaseAwaitAppendConfirm && current.Attempt == turn.AttemptFromContext(ctx)) {
		if e := turn.ExecutionFromContext(ctx); e != nil {
			e.Finish(err)
		}
	}
}
