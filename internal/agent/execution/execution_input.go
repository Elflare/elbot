package execution

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	"elbot/internal/request"

	agentshare "elbot/internal/agent/share"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (c *ExecutionCoordinator) ResumeAppend(ctx context.Context, sessionID string) (context.Context, operationresult.Result[struct{}], error) {
	defer c.sessionIdle(ctx, sessionID)
	for {
		locked, release, err := c.deps.Sessions.EnterActivation(ctx, c.deps.Identity.Scope(ctx), sessionID)
		if err != nil {
			return ctx, operationresult.Result[struct{}]{}, err
		}
		locked, captured, err := agentshare.CaptureSessionBinding(locked, c.deps.Sessions, c.deps.Identity, sessionID)
		if err != nil || captured.Status != operationresult.Success {
			release()
			return ctx, operationresult.Result[struct{}]{Status: captured.Status, Message: captured.Message}, err
		}
		row := captured.Data
		binding, _ := sessionpkg.BindingFromContext(locked)
		if err := c.deps.Sessions.RecordActivity(locked, binding); err != nil {
			release()
			return ctx, operationresult.Result[struct{}]{}, err
		}
		snapshot := c.deps.Turns.Snapshot(row.ID)
		if snapshot.Phase != turn.PhaseAwaitAppendConfirm {
			release()
			return ctx, operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "追加确认已失效"}, nil
		}
		if snapshot.AttemptDone != nil {
			release()
			if err := c.waitForAttempt(ctx, snapshot.AttemptDone); err != nil {
				return ctx, operationresult.Result[struct{}]{}, err
			}
			continue
		}
		merged, execution, ok := c.deps.Turns.ResumeAppend(row.ID)
		if !ok || (merged.Text == "" && len(merged.Segments) == 0 && len(merged.Reports) == 0) {
			release()
			return ctx, operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "追加确认已失效"}, nil
		}
		ctx = turn.WithExecution(locked, execution)
		ctx = agentshare.WithInboundTurnInput(ctx, merged)
		release()
		return c.Run(ctx, row, merged.Text, foregroundTurnOutput{ExecutionSender: c.deps.Output})
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
func (c *ExecutionCoordinator) AcceptInput(ctx context.Context, session *storage.Session, text string) (context.Context, operationresult.Result[agentshare.InputDisposition], error) {
	// 私聊不能进入会把回复发回主窗口的 Execution。等待的输入本身也归 /stop 管理。
	var wait context.Context
	var retire func()
	defer func() {
		if retire != nil {
			retire()
		}
	}()
	for {
		locked, admitted, release, err := c.EnterInput(ctx, session)
		if err != nil {
			return ctx, operationresult.Result[agentshare.InputDisposition]{}, err
		}
		if admitted.Status != operationresult.Success {
			return ctx, operationresult.Result[agentshare.InputDisposition]{Status: admitted.Status, Message: admitted.Message}, nil
		}
		ctx = locked
		snapshot := c.deps.Turns.Snapshot(session.ID)
		if wait != nil && wait.Err() != nil {
			release()
			return ctx, operationresult.Result[agentshare.InputDisposition]{}, wait.Err()
		}
		if session.TaskType == "coagent" && snapshot.Execution != nil && snapshot.Execution.CoagentBoardOutput {
			if wait == nil {
				_, wait, retire, err = c.deps.Requests.Start(ctx, request.StartRequest{SessionID: session.ID, Kind: request.KindTurn, Label: "coagent_private_input"})
				if err != nil {
					release()
					return ctx, operationresult.Result[agentshare.InputDisposition]{}, err
				}
			}
			release()
			if err := c.waitForAttempt(wait, snapshot.Execution.Done()); err != nil {
				return ctx, operationresult.Result[agentshare.InputDisposition]{}, err
			}
			continue
		}
		if snapshot.ReservationDone != nil {
			release()
			if err := c.waitForAttempt(ctx, snapshot.ReservationDone); err != nil {
				return ctx, operationresult.Result[agentshare.InputDisposition]{}, err
			}
			continue
		}
		if snapshot.Phase != turn.PhaseStopping && snapshot.Phase != turn.PhaseAwaitRiskConfirm && snapshot.Phase != turn.PhaseAwaitAppendConfirm {
			binding, _ := sessionpkg.BindingFromContext(ctx)
			if err := c.deps.Sessions.RecordActivity(ctx, binding); err != nil {
				release()
				return ctx, operationresult.Result[agentshare.InputDisposition]{}, err
			}
		}
		switch snapshot.Phase {
		case turn.PhaseStopping:
			release()
			if err := c.waitForAttempt(ctx, snapshot.AttemptDone); err != nil {
				return ctx, operationresult.Result[agentshare.InputDisposition]{}, err
			}
			continue
		case turn.PhaseAwaitRiskConfirm:
			release()
			return ctx, operationresult.Result[agentshare.InputDisposition]{Status: operationresult.Success, Data: agentshare.InputRiskConfirmation}, nil
		case turn.PhaseAwaitAppendConfirm:
			c.deps.Turns.AppendPendingInput(session.ID, agentshare.InboundTurnInput(ctx, text))
			release()
			return ctx, operationresult.Result[agentshare.InputDisposition]{Status: operationresult.Success, Data: agentshare.InputHandled}, nil
		case turn.PhaseLLM, turn.PhaseTool:
			input := agentshare.InboundTurnInput(ctx, text)
			if c.deps.Turns.AppendPendingInput(session.ID, input) {
				release()
				_, err := c.deps.Output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "queued → next LLM call"})
				return ctx, operationresult.Result[agentshare.InputDisposition]{Status: operationresult.Success, Data: agentshare.InputHandled}, err
			}
			if !c.deps.Turns.BeginAppend(session.ID, snapshot.Attempt, input) {
				release()
				continue
			}
			// Supersede this turn, not accepted process jobs owned by the session.
			for _, req := range c.deps.Requests.ListBySession(session.ID) {
				if req.Kind == request.KindTurn {
					c.deps.Requests.Cancel(req.ID)
				}
			}
			release()
			_, err := c.deps.Output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "stopped · append?", Actions: c.deps.AppendConfirmation.Actions})
			if err != nil {
				return ctx, operationresult.Result[agentshare.InputDisposition]{}, err
			}
			return ctx, operationresult.Result[agentshare.InputDisposition]{Status: operationresult.Success, Data: agentshare.InputHandled}, nil
		default:
			if session.TaskType == "coagent" {
				// 在已复核名册的同一准入内预约，退组、/stop 或下一份公开投递不能抢占这个空档。
				e, err := c.deps.Turns.NewExecution(ctx)
				if err != nil {
					release()
					return ctx, operationresult.Result[agentshare.InputDisposition]{}, err
				}
				if !c.deps.Turns.ReserveExecution(session.ID, agentshare.InboundTurnInput(ctx, text), e) {
					release()
					continue
				}
				ctx = turn.WithExecution(ctx, e)
				defer c.deps.Turns.ReleaseReservation(session.ID, e)
				if retire != nil {
					retire()
					retire = nil
					wait = nil
				}
			}
			release()
			var result operationresult.Result[struct{}]
			ctx, result, err = c.Run(ctx, session, text, foregroundTurnOutput{ExecutionSender: c.deps.Output})
			// A competing execution won after the idle snapshot. Revalidate the
			// original binding and dispatch the prepared input by its latest phase.
			if err != nil {
				return ctx, operationresult.Result[agentshare.InputDisposition]{}, err
			}
			if result.Status == operationresult.Busy {
				current := c.deps.Turns.Snapshot(session.ID)
				if current.Execution != nil && current.Execution != turn.ExecutionFromContext(ctx) {
					continue
				}
			}
			if result.Status != operationresult.Success {
				return ctx, operationresult.Result[agentshare.InputDisposition]{Status: result.Status, Message: result.Message}, nil
			}
			return ctx, operationresult.Result[agentshare.InputDisposition]{Status: operationresult.Success, Data: agentshare.InputHandled}, nil
		}
	}
}
