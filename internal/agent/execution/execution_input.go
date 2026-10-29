package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentshare "elbot/internal/agent/share"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (c *ExecutionCoordinator) ResumeAppend(ctx context.Context, row *storage.Session, text string) error {
	defer c.sessionIdle(ctx, row.ID)
	for {
		locked, release, err := c.sessions.EnterActivation(ctx, c.identity.Scope(ctx), row.ID)
		if err != nil {
			return err
		}
		locked, err = agentshare.CaptureSessionBinding(locked, c.sessions, c.identity, row)
		if err != nil {
			release()
			return err
		}
		binding, _ := sessionpkg.BindingFromContext(locked)
		if err := c.sessions.RecordActivity(locked, binding); err != nil {
			release()
			return err
		}
		switch {
		case agentshare.IsAppendConfirm(text):
			snapshot := c.turns.Snapshot(row.ID)
			if snapshot.Phase != turn.PhaseAwaitAppendConfirm {
				release()
				return nil
			}
			if snapshot.AttemptDone != nil {
				release()
				if err := c.waitForAttempt(ctx, snapshot.AttemptDone); err != nil {
					return err
				}
				continue
			}
			merged, execution, ok := c.turns.ResumeAppend(row.ID)
			ctx = turn.WithExecution(locked, execution)
			if !ok || (merged.Text == "" && len(merged.Segments) == 0) {
				release()
				return nil
			}
			ctx = agentshare.WithInboundTurnInput(ctx, merged)
			release()
			return c.Run(ctx, row, merged.Text, foregroundTurnOutput{sender: c.output})
		case agentshare.IsAppendCancel(text):
			c.turns.CancelAppend(row.ID)
			release()
			return c.output.SendChat(ctx, "已取消追加，正在结束本轮处理。")
		default:
			c.turns.AppendPendingInput(row.ID, agentshare.InboundTurnInput(ctx, text))
			release()
			return nil
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
		return errors.Join(ctx.Err(), waitCtx.Err())
	}
}

// AcceptInput revalidates the prepared input and dispatches under its original binding.
// Risk responses return to the entrypoint for ConfirmationCoordinator to consume.
func (c *ExecutionCoordinator) AcceptInput(ctx context.Context, session *storage.Session, text string) (agentshare.InputDisposition, error) {
	for {
		locked, release, err := c.EnterInput(ctx, session)
		if err != nil {
			return agentshare.InputHandled, err
		}
		ctx = locked
		snapshot := c.turns.Snapshot(session.ID)
		if snapshot.Phase != turn.PhaseStopping && snapshot.Phase != turn.PhaseAwaitRiskConfirm && snapshot.Phase != turn.PhaseAwaitAppendConfirm {
			binding, _ := sessionpkg.BindingFromContext(ctx)
			if err := c.sessions.RecordActivity(ctx, binding); err != nil {
				release()
				return agentshare.InputHandled, err
			}
		}
		switch snapshot.Phase {
		case turn.PhaseStopping:
			release()
			if err := c.waitForAttempt(ctx, snapshot.AttemptDone); err != nil {
				return agentshare.InputHandled, err
			}
			continue
		case turn.PhaseAwaitRiskConfirm:
			release()
			return agentshare.InputRiskConfirmation, nil
		case turn.PhaseAwaitAppendConfirm:
			release()
			return agentshare.InputHandled, c.ResumeAppend(ctx, session, text)
		case turn.PhaseLLM:
			waitCtx, finishWait, accepted := c.appendWaits.begin(ctx)
			if !accepted {
				release()
				return agentshare.InputHandled, context.Canceled
			}
			wait := c.turns.BeginAppend(session.ID, snapshot.Attempt, agentshare.InboundTurnInput(ctx, text))
			if wait == nil {
				finishWait()
				release()
				return agentshare.InputHandled, nil
			}
			c.requests.CancelSession(session.ID)
			release()
			timeout := c.waitPolicy.WaitTimeout(ctx)
			err := c.output.SendChat(ctx, appendConfirmPromptText(timeout))
			go func() {
				defer finishWait()
				defer c.sessionIdle(waitCtx, session.ID)
				if wait.Wait(waitCtx, timeout) {
					_ = c.output.SendChat(waitCtx, "追加确认已过期，待追加内容已丢弃，本轮处理已停止。")
				}
			}()
			return agentshare.InputHandled, err
		case turn.PhaseTool:
			c.turns.AppendPendingInput(session.ID, agentshare.InboundTurnInput(ctx, text))
			release()
			return agentshare.InputHandled, c.output.SendChat(ctx, "已追加，将在当前流程下一次模型调用时带上。发送 /stop 可打断当前流程。")
		default:
			release()
			err = c.Run(ctx, session, text, foregroundTurnOutput{sender: c.output})
			// A competing execution won after the idle snapshot. Revalidate the
			// original binding and dispatch the prepared input by its latest phase.
			if !errors.Is(err, errExecutionConflict) {
				return agentshare.InputHandled, err
			}
		}
	}
}

func appendConfirmPromptText(timeout time.Duration) string {
	text := "已停止当前处理。是否追加这条消息并重新发送？\n发送 $ / 是 / y / yes 确认；发送 取消 / 否 / n / no 放弃。\n也可以继续发送内容，发送完后再确认。"
	if timeout > 0 {
		text += fmt.Sprintf("\n超过 %s 没有继续发送内容或确认，将自动放弃。", agentshare.ConfirmationWaitDurationText(timeout))
	}
	return text
}
