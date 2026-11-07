package execution

import (
	"context"
	"elbot/internal/delivery"
	"errors"

	agentshare "elbot/internal/agent/share"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (c *ExecutionCoordinator) ResumeAppend(ctx context.Context, sessionID string) (context.Context, error) {
	defer c.sessionIdle(ctx, sessionID)
	for {
		locked, release, err := c.sessions.EnterActivation(ctx, c.identity.Scope(ctx), sessionID)
		if err != nil {
			return ctx, err
		}
		row, err := c.sessionRows.Get(locked, sessionID)
		if err != nil {
			release()
			return ctx, err
		}
		locked, err = agentshare.CaptureSessionBinding(locked, c.sessions, c.identity, row)
		if err != nil {
			release()
			return ctx, err
		}
		binding, _ := sessionpkg.BindingFromContext(locked)
		if err := c.sessions.RecordActivity(locked, binding); err != nil {
			release()
			return ctx, err
		}
		snapshot := c.turns.Snapshot(row.ID)
		if snapshot.Phase != turn.PhaseAwaitAppendConfirm {
			release()
			return ctx, agentshare.ErrConfirmationExpired
		}
		if snapshot.AttemptDone != nil {
			release()
			if err := c.waitForAttempt(ctx, snapshot.AttemptDone); err != nil {
				return ctx, err
			}
			continue
		}
		merged, execution, ok := c.turns.ResumeAppend(row.ID)
		if !ok || (merged.Text == "" && len(merged.Segments) == 0) {
			release()
			return ctx, agentshare.ErrConfirmationExpired
		}
		ctx = turn.WithExecution(locked, execution)
		ctx = agentshare.WithInboundTurnInput(ctx, merged)
		release()
		return c.Run(ctx, row, merged.Text, foregroundTurnOutput{ExecutionSender: c.output})
	}
}

// waitForAttempt joins an old attempt outside admission locks. The input and
// application lifetimes can both abandon this wait without replaying the input.
func (c *ExecutionCoordinator) waitForAttempt(ctx context.Context, done <-chan struct{}) error {
	waitCtx, finish, accepted := c.attemptWaits.begin(ctx)
	if !accepted {
		return context.Canceled
	}
	defer finish()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-waitCtx.Done():
		return waitCtx.Err()
	case <-done:
		if err := ctx.Err(); err != nil {
			return err
		}
		return waitCtx.Err()
	}
}

// AcceptInput revalidates the prepared input and dispatches under its original binding.
// Risk responses return to the entrypoint for ConfirmationCoordinator to consume.
func (c *ExecutionCoordinator) AcceptInput(ctx context.Context, session *storage.Session, text string) (context.Context, agentshare.InputDisposition, error) {
	for {
		locked, release, err := c.EnterInput(ctx, session)
		if err != nil {
			return ctx, agentshare.InputHandled, err
		}
		ctx = locked
		snapshot := c.turns.Snapshot(session.ID)
		if snapshot.Phase != turn.PhaseStopping && snapshot.Phase != turn.PhaseAwaitRiskConfirm && snapshot.Phase != turn.PhaseAwaitAppendConfirm {
			binding, _ := sessionpkg.BindingFromContext(ctx)
			if err := c.sessions.RecordActivity(ctx, binding); err != nil {
				release()
				return ctx, agentshare.InputHandled, err
			}
		}
		switch snapshot.Phase {
		case turn.PhaseStopping:
			release()
			if err := c.waitForAttempt(ctx, snapshot.AttemptDone); err != nil {
				return ctx, agentshare.InputHandled, err
			}
			continue
		case turn.PhaseAwaitRiskConfirm:
			release()
			return ctx, agentshare.InputRiskConfirmation, nil
		case turn.PhaseAwaitAppendConfirm:
			c.turns.AppendPendingInput(session.ID, agentshare.InboundTurnInput(ctx, text))
			release()
			return ctx, agentshare.InputHandled, nil
		case turn.PhaseLLM:
			if !c.turns.BeginAppend(session.ID, snapshot.Attempt, agentshare.InboundTurnInput(ctx, text)) {
				release()
				continue
			}
			c.requests.CancelSession(session.ID)
			release()
			presentation := c.appendConfirmation
			_, err := c.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "stopped · append?\ny=send · " + presentation.Actions[1].Command + "=drop+stop", Actions: presentation.Actions})
			return ctx, agentshare.InputHandled, err
		case turn.PhaseTool:
			c.turns.AppendPendingInput(session.ID, agentshare.InboundTurnInput(ctx, text))
			release()
			_, err := c.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "queued → next LLM call"})
			return ctx, agentshare.InputHandled, err
		default:
			release()
			ctx, err = c.Run(ctx, session, text, foregroundTurnOutput{ExecutionSender: c.output})
			// A competing execution won after the idle snapshot. Revalidate the
			// original binding and dispatch the prepared input by its latest phase.
			if !errors.Is(err, errExecutionConflict) {
				return ctx, agentshare.InputHandled, err
			}
		}
	}
}
