package execution

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
	"fmt"
)

// StartAsyncTool registers under the same binding gate as /stop. Its context
// survives a completed tool batch but is canceled by session stop or Agent close.
func (c *ExecutionCoordinator) StartAsyncTool(ctx context.Context, sid, name string) (tool.AsyncJob, error) {
	binding, _ := session.BindingFromContext(ctx)
	_, release, err := c.deps.Sessions.EnterBinding(ctx, binding)
	if err != nil {
		return tool.AsyncJob{}, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return tool.AsyncJob{}, err
	}
	parent, finish, ok := c.attemptWaits.begin(ctx)
	if !ok {
		return tool.AsyncJob{}, context.Canceled
	}
	req, job, done, err := c.deps.Requests.Start(parent, request.StartRequest{ParentID: contextinfo.RootRequestIDFromContext(ctx), SessionID: sid, Kind: request.KindTool, Label: name})
	if err != nil {
		finish()
		return tool.AsyncJob{}, err
	}
	return tool.AsyncJob{ID: req.ID, Context: job, Record: c.deps.RecordToolCall, Done: func() { done(); finish() }, Deliver: func(call llm.ToolCallRequest, msg llm.LLMMessage) error {
		return c.deliverAsyncTool(parent, sid, req.ID, done, call, msg)
	}}, nil
}
func (c *ExecutionCoordinator) deliverAsyncTool(ctx context.Context, sid, id string, retire func(), call llm.ToolCallRequest, msg llm.LLMMessage) error {
	binding, _ := session.BindingFromContext(ctx)
	locked, release, err := c.deps.Sessions.EnterBinding(ctx, binding)
	if err != nil {
		return err
	}
	// Resolve the outcome and retire the request under the same gate as both /stop forms.
	active, ok := c.deps.Requests.Get(id)
	if !ok || active.SessionStopped || ctx.Err() != nil {
		release()
		return nil
	}
	if active.Stopping {
		msg.Segments = llm.TextSegments("异步任务 " + id + " 被用户取消；不是新的执行。")
	}
	retire()
	row, err := c.deps.SessionRows.Get(locked, sid)
	if err != nil {
		release()
		return err
	}
	head := agentshare.ToolCallStorageMessage(sid, "", "", []llm.ToolCallRequest{call}, "")
	result := agentshare.ToolResultStorageMessage(sid, msg)
	in := turn.Input{Reports: []*storage.Message{&head, &result}}
	admitted, err := c.AdmitDeferredInput(locked, row, in)
	release()
	if err != nil {
		return err
	}
	if admitted.Status != operationresult.Success {
		return fmt.Errorf("异步结果投递: %s", admitted.Message)
	}
	if admitted.Data == nil {
		return nil
	}
	defer c.deps.Turns.ReleaseReservation(sid, admitted.Data)
	_, r, err := c.RunDeferredInput(ctx, row, in, admitted.Data)
	if err == nil && r.Status != operationresult.Success {
		return fmt.Errorf("异步结果续跑: %s", r.Message)
	}
	return err
}

// AdmitDeferredInput runs under the caller's session binding lock. A nil execution
// means the active turn owns the pending input; otherwise the caller owns a reservation.
func (c *ExecutionCoordinator) AdmitDeferredInput(ctx context.Context, row *storage.Session, in turn.Input) (result operationresult.Result[*turn.Execution], err error) {
	if row.ArchivedAt != nil {
		result.Status, result.Message = operationresult.Rejected, "会话已归档"
		return
	}
	for {
		snapshot := c.deps.Turns.Snapshot(row.ID)
		e := snapshot.Execution
		idle := snapshot.Phase == turn.PhaseIdle
		if idle {
			e, err = c.deps.Turns.NewExecution(ctx)
			if err != nil {
				return
			}
		} else if e == nil || snapshot.Phase == turn.PhaseStopping || snapshot.Phase == turn.PhaseCompact {
			result.Status, result.Message = operationresult.Busy, "会话忙"
			return
		}
		for _, batch := range in.Answers {
			batch.Bind(e.Done())
		}
		if idle {
			if !c.deps.Turns.ReserveExecution(row.ID, in, e) {
				continue
			}
			result.Data = e
		} else if !c.deps.Turns.AppendPendingInput(row.ID, in) {
			continue
		}
		result.Status = operationresult.Success
		return
	}
}

func (c *ExecutionCoordinator) RunDeferredInput(ctx context.Context, row *storage.Session, input turn.Input, execution *turn.Execution) (context.Context, operationresult.Result[struct{}], error) {
	select {
	case <-execution.Done():
		return ctx, operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "提交已取消"}, nil
	default:
	}
	ctx = agentshare.WithInboundTurnInput(turn.WithExecution(ctx, execution), input)
	return c.Run(ctx, row, input.Text, foregroundTurnOutput{ExecutionSender: c.deps.Output})
}
