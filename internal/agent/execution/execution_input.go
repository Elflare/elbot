package execution

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"errors"
	"fmt"
	"log/slog"
	"time"

	agentshare "elbot/internal/agent/share"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (c *ExecutionCoordinator) ResumeAppend(ctx context.Context, row *storage.Session, text string) (context.Context, error) {
	defer c.sessionIdle(ctx, row.ID)
	for {
		locked, release, err := c.sessions.EnterActivation(ctx, c.identity.Scope(ctx), row.ID)
		if err != nil {
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
		switch {
		case agentshare.IsAppendConfirm(text):
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
			ctx = turn.WithExecution(locked, execution)
			if !ok || (merged.Text == "" && len(merged.Segments) == 0) {
				release()
				return ctx, agentshare.ErrConfirmationExpired
			}
			ctx = agentshare.WithInboundTurnInput(ctx, merged)
			release()
			return c.Run(ctx, row, merged.Text, foregroundTurnOutput{ExecutionSender: c.output})
		case agentshare.IsAppendCancel(text):
			if c.turns.Snapshot(row.ID).Phase != turn.PhaseAwaitAppendConfirm {
				release()
				return ctx, agentshare.ErrConfirmationExpired
			}
			c.turns.CancelAppend(row.ID)
			release()
			return ctx, nil
		default:
			c.turns.AppendPendingInput(row.ID, agentshare.InboundTurnInput(ctx, text))
			release()
			return ctx, nil
		}
	}
}

// waitForAttempt joins an old attempt outside admission locks. The input and
// application lifetimes can both abandon this wait without replaying the input.
func (c *ExecutionCoordinator) waitForAttempt(ctx context.Context, done <-chan struct{}) error {
	waitCtx, finish, accepted := c.appendWaits.begin(ctx)
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
			release()
			ctx, err := c.ResumeAppend(ctx, session, text)
			if err == nil && agentshare.IsAppendCancel(text) {
				_, err = c.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "append canceled · stopping"})
			}
			return ctx, agentshare.InputHandled, err
		case turn.PhaseLLM:
			waitCtx, finishWait, accepted := c.appendWaits.begin(ctx)
			if !accepted {
				release()
				return ctx, agentshare.InputHandled, context.Canceled
			}
			wait := c.turns.BeginAppend(session.ID, snapshot.Attempt, agentshare.InboundTurnInput(ctx, text))
			if wait == nil {
				finishWait()
				release()
				return ctx, agentshare.InputHandled, nil
			}
			c.requests.CancelSession(session.ID)
			release()
			timeout := c.waitPolicy.WaitTimeout(ctx)
			presentation := c.appendConfirmation
			_, err := c.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: appendConfirmPromptText(timeout), Actions: presentation.Actions})
			go func() {
				defer finishWait()
				defer c.sessionIdle(waitCtx, session.ID)
				if wait.Wait(waitCtx, timeout) {
					if _, err := c.output.Send(waitCtx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "append timeout → dropped · stopped"}); err != nil {
						record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelWarn, EventName: "append_timeout_notice_failed", Module: "agent", Content: err.Error(), Identity: events.LogIdentityFromContext(waitCtx), ResultStatus: events.LogStatus(err)}
						if record.ResultStatus == events.ResultCanceled {
							record.Level = slog.LevelInfo
						}
						events.AsyncLog(waitCtx, record)
					}
				}
			}()
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

func appendConfirmPromptText(timeout time.Duration) string {
	text := "stopped · append?\ny=send · n=drop+stop"
	if timeout > 0 {
		text += fmt.Sprintf("\nidle=%s → drop+stop", agentshare.ConfirmationWaitDurationText(timeout))
	}
	return text
}
