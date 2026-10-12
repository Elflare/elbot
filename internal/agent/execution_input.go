package agent

import (
	"context"
	"errors"

	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (c *ExecutionCoordinator) ResumeAppend(ctx context.Context, row *storage.Session, text string) error {
	locked, release, err := c.sessions.EnterActivation(ctx, c.identity.Scope(ctx), row.ID)
	if err != nil {
		return err
	}
	locked, err = captureSessionBinding(locked, c.sessions, c.identity, row)
	if err != nil {
		release()
		return err
	}
	switch {
	case turn.IsConfirm(text):
		merged, execution, ok := c.turns.ResumeAppend(row.ID)
		ctx = turn.WithExecution(locked, execution)
		if !ok || (merged.Text == "" && len(merged.Segments) == 0) {
			release()
			return nil
		}
		ctx = withInboundTurnInput(ctx, merged)
		release()
		return c.Run(ctx, row, merged.Text, foregroundTurnOutput{sender: c.output})
	case turn.IsCancel(text):
		c.turns.CancelAppend(row.ID)
		release()
		c.output.SendChat(ctx, "已取消追加，本轮处理已停止。")
		return nil
	default:
		c.turns.AppendPendingInput(row.ID, inboundTurnInput(ctx, text))
		release()
		return nil
	}
}

// InputDisposition directs prepared input to execution or risk confirmation.
// It does not transfer ownership of the active Turn or its confirmation state.
type InputDisposition uint8

const (
	inputHandled InputDisposition = iota
	inputRiskConfirmation
)

// AcceptInput revalidates the prepared input and dispatches under its original binding.
// Risk responses return to the entrypoint for ConfirmationCoordinator to consume.
func (c *ExecutionCoordinator) AcceptInput(ctx context.Context, session *storage.Session, text string) (InputDisposition, error) {
	for {
		locked, release, err := c.EnterInput(ctx, session)
		if err != nil {
			return inputHandled, err
		}
		ctx = locked
		snapshot := c.turns.Snapshot(session.ID)
		switch snapshot.Phase {
		case turn.PhaseAwaitRiskConfirm:
			release()
			return inputRiskConfirmation, nil
		case turn.PhaseAwaitAppendConfirm:
			release()
			return inputHandled, c.ResumeAppend(ctx, session, text)
		case turn.PhaseLLM:
			waitCtx, finishWait, accepted := c.appendWaits.begin(ctx)
			if !accepted {
				release()
				return inputHandled, context.Canceled
			}
			if !c.turns.InterruptLLMInput(session.ID, inboundTurnInput(ctx, text)) {
				finishWait()
				release()
				return inputHandled, nil
			}
			wait := c.turns.AppendWait(session.ID)
			c.requests.CancelSession(session.ID)
			release()
			timeout := c.waitPolicy.WaitTimeout(ctx)
			c.output.SendChat(ctx, appendConfirmPromptText(timeout))
			go func() {
				defer finishWait()
				if wait.Wait(waitCtx, timeout) {
					c.output.SendChat(waitCtx, "追加确认已过期，待追加内容已丢弃，本轮处理已停止。")
				}
			}()
			return inputHandled, nil
		case turn.PhaseTool:
			c.turns.AppendPendingInput(session.ID, inboundTurnInput(ctx, text))
			release()
			c.output.SendChat(ctx, "已追加，将在当前流程下一次模型调用时带上。发送 /stop 可打断当前流程。")
			return inputHandled, nil
		default:
			release()
			err = c.Run(ctx, session, text, foregroundTurnOutput{sender: c.output})
			// A competing execution won after the idle snapshot. Revalidate the
			// original binding and dispatch the prepared input by its latest phase.
			if !errors.Is(err, errExecutionConflict) {
				return inputHandled, err
			}
		}
	}
}
