package agent

import (
	"context"
	"errors"
	"log/slog"

	"elbot/internal/modelmgr"
	notificationrules "elbot/internal/notification/rules"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (c *executionCoordinator) runAttempt(ctx context.Context, session *storage.Session, text string, out turnOutput) (*storage.Session, turn.Input, error) {
	ctx, release, err := c.enterTurn(ctx, session)
	if err != nil {
		return session, turn.Input{}, err
	}
	release()
	selection := modelSelectionForTurn(ctx, c.models, session)
	ctx, session, err = c.compactBeforeTurn(ctx, session, text, out, selection)
	if err != nil {
		return session, turn.Input{}, err
	}
	ctx, release, err = c.enterTurn(ctx, session)
	if err != nil {
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
		return session, turn.Input{}, sessionpkg.ErrSessionBusy
	}
	defer c.turns.FinishRequest(session.ID, attempt)

	result, pending := c.runTurn(ctx, session, text, out, selection)
	if err := c.finishAttempt(ctx, session, out, result); err != nil {
		return session, turn.Input{}, err
	}
	return session, pending, nil
}

func (c *executionCoordinator) finishAttempt(ctx context.Context, session *storage.Session, out turnOutput, result chatTurnResult) error {
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
		out.PublishRuntimeStatus(ctx, status)
		c.turns.StopSession(session.ID, attempt)
		return err
	}
	status := c.status.Snapshot(session.ID)
	if status.Running() && (c.turns.Snapshot(session.ID).Phase == turn.PhaseIdle || c.turns.MatchesAttempt(session.ID, attempt)) {
		out.PublishRuntimeStatus(ctx, runtimeDoneStatus(status, storage.Now()))
	}
	return nil
}

func (c *executionCoordinator) runTurn(ctx context.Context, session *storage.Session, text string, out turnOutput, selection modelmgr.Selection) (chatTurnResult, turn.Input) {
	prepared, err := c.chat.prepareTurn(ctx, session, text)
	if err != nil {
		return chatTurnResult{Outcome: failedChatOutcome(err), Err: err}, turn.Input{}
	}
	locked, releaseRequest, err := c.sessions.EnterSessions(ctx, session.ID)
	if err != nil {
		return chatTurnResult{Outcome: failedChatOutcome(err), Err: err}, turn.Input{}
	}
	if !c.turns.MatchesAttempt(session.ID, turn.AttemptFromContext(ctx)) {
		releaseRequest()
		return chatTurnResult{Outcome: chatTurnCanceled, Err: context.Canceled}, turn.Input{}
	}
	reqCtxInfo, reqCtx, done, err := c.requests.Start(locked, request.StartRequest{SessionID: session.ID, Kind: request.KindTurn, Label: "chat", Timeout: c.responseTimeout})
	releaseRequest()
	if err != nil {
		return chatTurnResult{Outcome: failedChatOutcome(err), Err: err}, turn.Input{}
	}
	defer done()
	reqCtx = withTurnRequestID(reqCtx, reqCtxInfo.ID)

	result := c.chat.RunTurn(ctx, reqCtx, chatTurnInput{Session: session, Text: text, Selection: selection, RequestID: reqCtxInfo.ID, Prepared: prepared}, out)
	if result.QuietCancellation {
		c.handleTurnContextDone(ctx, session.ID, result.Err, out)
		return result, turn.Input{}
	}
	if result.Outcome != chatTurnCompleted {
		return result, turn.Input{}
	}
	pending, err := c.finishCompletedTurn(ctx, session, out, result)
	if err != nil {
		result.Outcome, result.Err = failedChatOutcome(err), err
	}
	return result, pending
}

func (c *executionCoordinator) finishCompletedTurn(ctx context.Context, session *storage.Session, out turnOutput, result chatTurnResult) (turn.Input, error) {
	ctx = c.view.Context(ctx)
	selection, usage, turnStartedAt, committed := result.Selection, result.Usage, result.StartedAt, result.Committed
	if err := c.sessions.Touch(ctx, session); err != nil {
		emitFact(ctx, c.persistenceFailed, PersistenceFailedEvent{EventMeta: eventMeta(ctx, session.ID), Operation: "touch_session", Err: err})
		return turn.Input{}, err
	}
	c.recordUsage(session.ID, usage)
	doneStatus := runtimeDoneStatus(runtimestatus.Snapshot{SessionID: session.ID, Provider: selection.Provider, Model: selection.Model, Mode: session.Mode, TurnStartedAt: turnStartedAt, StageStartedAt: turnStartedAt, Usage: usage}, storage.Now())
	out.PublishRuntimeStatus(ctx, doneStatus)
	nextSelection := modelSelectionForTurn(ctx, c.models, session)
	if c.shouldCompact(ctx, session, nextSelection) {
		_, _ = out.SendAssistant(ctx, "compact status: will compact before next request")
	}
	pending, completed := c.turns.CompleteLLMInput(session.ID, turn.AttemptFromContext(ctx))
	if !completed {
		return turn.Input{}, nil
	}
	if execution := turn.ExecutionFromContext(ctx); execution != nil {
		execution.SetResult(session.ID, committed.MessageID, committed.RawText)
	}
	c.sessions.MaybeScheduleNaming(ctx, session.ID)
	return pending, nil
}

func (c *executionCoordinator) handleTurnContextDone(ctx context.Context, sessionID string, err error, out turnOutput) {
	if c.turns.Execution(sessionID) != turn.ExecutionFromContext(ctx) || (c.turns.Snapshot(sessionID).Phase != turn.PhaseAwaitAppendConfirm && c.turns.MatchesAttempt(sessionID, turn.AttemptFromContext(ctx))) {
		if e := turn.ExecutionFromContext(ctx); e != nil {
			e.Finish(err)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		emitFact(ctx, c.timedOut, TurnTimedOutEvent{EventMeta: eventMeta(ctx, sessionID), Err: err})
		out.SendNotice(ctx, slog.LevelWarn, notificationrules.TurnTimeout)
	}
}
