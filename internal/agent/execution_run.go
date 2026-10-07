package agent

import (
	"context"
	"errors"
	"log/slog"

	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"
	"elbot/internal/modelmgr"
	notificationrules "elbot/internal/notification/rules"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// modelContext supplies the prospective main-dialogue target to tool views.
// Execution still resolves and checks its fixed selection at admission.
func (c *executionCoordinator) modelContext(ctx context.Context, row *storage.Session) (context.Context, error) {
	selected := modelmgr.SelectionForTurn(ctx, c.models, row)
	return c.view.WithModel(ctx, selected)
}

func (c *executionCoordinator) runAttempt(ctx context.Context, session *storage.Session, text string, out dialogue.Output) (*storage.Session, turn.Input, error) {
	ctx, release, err := c.enterTurn(ctx, session)
	if err != nil {
		return session, turn.Input{}, err
	}
	selection := modelmgr.SelectionForTurn(ctx, c.models, session)
	if err := c.dialogue.CheckSelection(selection); err != nil {
		release()
		return session, turn.Input{}, err
	}
	if err := c.view.CheckSelection(session, selection); err != nil {
		release()
		return session, turn.Input{}, err
	}
	ctx, err = c.view.WithModel(ctx, selection)
	if err != nil {
		release()
		return session, turn.Input{}, err
	}
	origin, err := c.dialogue.Routes.OriginFor(selection.Provider)
	if err == nil {
		var latest *storage.Session
		latest, err = c.sessions.RegisterOrigin(ctx, session.ID, origin)
		if err == nil {
			*session = *latest
		}
	}
	release()
	if err != nil {
		return session, turn.Input{}, err
	}
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

func (c *executionCoordinator) finishAttempt(ctx context.Context, session *storage.Session, out dialogue.Output, result dialogue.TurnResult) error {
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

func (c *executionCoordinator) runTurn(ctx context.Context, session *storage.Session, text string, out dialogue.Output, selection modelmgr.Selection) (dialogue.TurnResult, turn.Input) {
	prepared, err := c.dialogue.PrepareTurn(ctx, dialogue.TurnInput{Session: session, Text: text, Input: inboundTurnInput(ctx, text), ReplyToPlatformMessageID: inboundReplyMessageID(ctx), Selection: selection})
	if err != nil {
		return dialogue.TurnResult{Outcome: dialogue.FailedOutcome(err), Err: err}, turn.Input{}
	}
	locked, releaseRequest, err := c.sessions.EnterSessions(ctx, session.ID)
	if err != nil {
		return dialogue.TurnResult{Outcome: dialogue.FailedOutcome(err), Err: err}, turn.Input{}
	}
	if !c.turns.MatchesAttempt(session.ID, turn.AttemptFromContext(ctx)) {
		releaseRequest()
		return dialogue.TurnResult{Outcome: dialogue.Canceled, Err: context.Canceled}, turn.Input{}
	}
	reqCtxInfo, reqCtx, done, err := c.requests.Start(locked, request.StartRequest{SessionID: session.ID, Kind: request.KindTurn, Label: "chat", Timeout: c.responseTimeout})
	releaseRequest()
	if err != nil {
		return dialogue.TurnResult{Outcome: dialogue.FailedOutcome(err), Err: err}, turn.Input{}
	}
	defer done()

	result := c.dialogue.RunTurn(ctx, reqCtx, dialogue.TurnInput{Session: session, Text: text, Selection: selection, RequestID: reqCtxInfo.ID, Prepared: prepared}, out)
	if result.QuietCancellation {
		c.handleTurnContextDone(ctx, session.ID, result.Err, out)
		return result, turn.Input{}
	}
	if result.Outcome != dialogue.Completed {
		return result, turn.Input{}
	}
	pending, err := c.finishCompletedTurn(ctx, session, out, result)
	if err != nil {
		result.Outcome, result.Err = dialogue.FailedOutcome(err), err
	}
	return result, pending
}

func (c *executionCoordinator) finishCompletedTurn(ctx context.Context, session *storage.Session, out dialogue.Output, result dialogue.TurnResult) (turn.Input, error) {
	ctx = c.view.Context(ctx)
	selection, usage, turnStartedAt, committed := result.Selection, result.Usage, result.StartedAt, result.Committed
	if err := c.sessions.Touch(ctx, session); err != nil {
		agentevents.Emit(ctx, c.persistenceFailed, agentevents.PersistenceFailedEvent{EventMeta: agentevents.Meta(ctx, session.ID), Operation: "touch_session", Err: err})
		return turn.Input{}, err
	}
	c.recordUsage(session.ID, usage)
	doneStatus := runtimeDoneStatus(runtimestatus.Snapshot{SessionID: session.ID, Provider: selection.Provider, Model: selection.Model, Mode: session.Mode, TurnStartedAt: turnStartedAt, StageStartedAt: turnStartedAt, Usage: usage}, storage.Now())
	out.PublishRuntimeStatus(ctx, doneStatus)
	nextSelection := modelmgr.SelectionForTurn(ctx, c.models, session)
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

func (c *executionCoordinator) handleTurnContextDone(ctx context.Context, sessionID string, err error, out dialogue.Output) {
	if c.turns.Execution(sessionID) != turn.ExecutionFromContext(ctx) || (c.turns.Snapshot(sessionID).Phase != turn.PhaseAwaitAppendConfirm && c.turns.MatchesAttempt(sessionID, turn.AttemptFromContext(ctx))) {
		if e := turn.ExecutionFromContext(ctx); e != nil {
			e.Finish(err)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		agentevents.Emit(ctx, c.timedOut, agentevents.TurnTimedOutEvent{EventMeta: agentevents.Meta(ctx, sessionID), Err: err})
		out.SendNotice(ctx, slog.LevelWarn, notificationrules.TurnTimeout)
	}
}
