package dialogue

import (
	"context"
	"errors"

	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type Runner struct {
	Routes   LoopResolver
	Preparer *Preparer
	Messages *MessageStore
	Replies  *ReplyCommitter
	Turns    *turn.Manager
	View     ExecutionView
}
type PreparedTurn struct{ loop PreparedLoop }

func (r *Runner) PrepareTurn(ctx context.Context, in TurnInput) (*PreparedTurn, error) {
	route, err := r.Routes.LoopFor(in.Selection.Protocol)
	if err != nil {
		return nil, err
	}
	materials, err := r.Preparer.LoadMaterials(ctx, in)
	if err != nil {
		return nil, err
	}
	prepared, err := route.PrepareTurn(ctx, materials)
	if err != nil {
		return nil, err
	}
	return &PreparedTurn{loop: prepared}, nil
}

func (r *Runner) RunTurn(ctx, requestCtx context.Context, in TurnInput, out Output) (result TurnResult) {
	result.StartedAt = storage.Now()
	result.Selection = in.Selection
	defer func() {
		if result.Outcome == Completed {
			return
		}
		execution := r.Turns.Execution(in.Session.ID)
		if execution != nil && execution == turn.ExecutionFromContext(ctx) && r.Turns.Snapshot(in.Session.ID).Phase == turn.PhaseAwaitAppendConfirm {
			result.Outcome = Paused
		} else if execution != nil && !r.Turns.MatchesAttempt(in.Session.ID, turn.AttemptFromContext(ctx)) {
			result.Outcome = Superseded
		}
	}()
	loopIn := LoopInput{Session: in.Session, Text: in.Text, Selection: in.Selection, RequestID: in.RequestID, StartedAt: result.StartedAt}
	prepared := in.Prepared.loop
	user, err := prepared.PrepareInput(ctx, requestCtx, loopIn, out)
	if err == nil {
		err = requestCtx.Err()
	}
	if err == nil {
		err = r.Messages.Append(requestCtx, user, "append_user_message")
	}
	if err != nil {
		result.Outcome, result.Err = FailedOutcome(err), err
		result.QuietCancellation = requestCtx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
		return result
	}
	loop := prepared.RunLoop(ctx, requestCtx, loopIn, out)
	result.Outcome, result.Err, result.QuietCancellation = loop.Outcome, loop.Err, loop.QuietCancellation
	result.Usage, result.Selection = loop.Usage, loop.Selection
	if loop.Outcome != Completed {
		return result
	}
	requestCtx, err = r.View.RefreshSession(requestCtx, in.Session)
	if err != nil {
		result.Outcome, result.Err = FailedOutcome(err), err
		return result
	}
	ctx = r.View.Context(ctx)
	out.PublishRuntimeStatus(ctx, runtimestatus.Snapshot{SessionID: in.Session.ID, Phase: runtimestatus.PhaseSending,
		Provider: result.Selection.Provider, Model: result.Selection.Model, Mode: in.Session.Mode, RequestID: in.RequestID,
		Kind: request.KindTurn, Label: "chat", TurnStartedAt: result.StartedAt, StageStartedAt: storage.Now()})
	result.Committed, err = r.Replies.Commit(ctx, requestCtx, loop.Commit, out)
	if err != nil {
		result.Outcome, result.Err = FailedOutcome(err), err
	}
	return result
}
