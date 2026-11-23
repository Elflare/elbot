package execution

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type coagentOutput struct {
	backgroundTurnOutput
	reply func(context.Context, string) (delivery.Receipt, error)
}

type coagentOutputKey struct{}

func (o coagentOutput) StartStream(context.Context) delivery.MessageStream {
	return delivery.StreamFunc(func(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
		if event.Type == delivery.StreamSend {
			return o.reply(ctx, event.Text)
		}
		return delivery.Receipt{}, nil
	})
}

// QueueBoardInput admits a signed board entry to one member and runs idle sessions independently.
func (c *ExecutionCoordinator) QueueBoardInput(ctx context.Context, boardID int64, member storage.CoagentMember, text string, main bool, reply func(context.Context, string) (delivery.Receipt, error)) error {
	scope := c.deps.Identity.Scope(ctx)
	locked, release, err := c.deps.Sessions.EnterActivation(ctx, scope, member.SessionID)
	if err != nil {
		return err
	}
	defer release()
	current, err := c.deps.Sessions.CurrentBound(locked, scope)
	if err != nil {
		return err
	}
	if current.Status != operationresult.Success {
		return fmt.Errorf("成员 %s 的会话已切换", member.Name)
	}
	if member.SessionID != current.Data.Session.ID {
		member.SessionID = current.Data.Session.ID
		board, err := c.deps.Sessions.ReadCoagentBoard(locked, current.Data.Binding, math.MaxInt64)
		if err != nil {
			return err
		}
		if board.Data.ID != boardID || !slices.Contains(board.Data.Members, member) {
			return fmt.Errorf("成员 %s 的会话已切换", member.Name)
		}
	}
	if snapshot := c.deps.Turns.Snapshot(member.SessionID); snapshot.Phase == turn.PhaseCompact {
		// Register the wait before releasing admission so /stopall also cancels this delivery.
		_, wait, done, err := c.deps.Requests.Start(ctx, request.StartRequest{SessionID: member.SessionID, Kind: request.KindTool, Label: "coagent_board"})
		if err != nil {
			return err
		}
		go func() {
			defer done()
			if err := c.waitForAttempt(wait, snapshot.AttemptDone); err == nil {
				if err := c.QueueBoardInput(wait, boardID, member, text, main, reply); err != nil {
					slog.WarnContext(wait, "压缩后投递协作留言失败", "session_id", member.SessionID, "error", err)
				}
			}
		}()
		return nil
	}
	ctx = session.WithBinding(ctx, current.Data.Binding)
	input := turn.Input{Text: text, PlatformText: text, BoardNotice: true}
	admitted, err := c.admitReport(locked, current.Data.Session, input)
	if err != nil {
		return err
	}
	if admitted.Status != operationresult.Success {
		return fmt.Errorf("投递留言给 %s：%s", member.Name, admitted.Message)
	}
	if admitted.Data == nil {
		return nil
	}
	runCtx, finish, accepted := c.attemptWaits.begin(ctx)
	if !accepted {
		c.deps.Turns.ReleaseReservation(member.SessionID, admitted.Data)
		return context.Canceled
	}
	var out agentshare.Output = coagentOutput{reply: reply}
	if main {
		out = foregroundTurnOutput{ExecutionSender: c.deps.Output}
	} else {
		runCtx = context.WithValue(runCtx, coagentOutputKey{}, out)
	}
	go func() {
		defer finish()
		defer c.deps.Turns.ReleaseReservation(member.SessionID, admitted.Data)
		runCtx = agentshare.WithInboundTurnInput(turn.WithExecution(runCtx, admitted.Data), input)
		_, result, err := c.Run(runCtx, current.Data.Session, text, out)
		if err != nil || result.Status != operationresult.Success {
			slog.WarnContext(runCtx, "协作留言处理结束", "session_id", member.SessionID, "status", result.Status, "message", result.Message, "error", err)
		}
	}()
	return nil
}
