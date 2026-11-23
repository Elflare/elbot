package execution

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
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
	return tool.AsyncJob{ID: req.ID, Context: job, Done: func() { done(); finish() }, Deliver: func(call llm.ToolCallRequest, outcome delivery.ToolResult) error {
		return c.deliverAsyncTool(parent, sid, req.ID, done, call, outcome)
	}}, nil
}
func (c *ExecutionCoordinator) deliverAsyncTool(ctx context.Context, sid, id string, retire func(), call llm.ToolCallRequest, outcome delivery.ToolResult) error {
	binding, _ := session.BindingFromContext(ctx)
	var locked context.Context
	var release func()
	var err error
	info, _ := contextinfo.ConversationFromContext(ctx)
	for {
		locked, release, err = c.deps.Sessions.EnterBinding(ctx, binding)
		if err != nil {
			return err
		}
		// 在 /stop 的同一准入锁下处理已完成的异步结果。私聊工具结果也不能进入公开板轮。
		active, ok := c.deps.Requests.Get(id)
		if !ok || active.SessionStopped || ctx.Err() != nil {
			release()
			return nil
		}
		if info.CoagentMainSessionID != "" {
			e := c.deps.Turns.Snapshot(sid).Execution
			if e != nil && e.CoagentBoardOutput {
				release()
				if err := c.waitForAttempt(ctx, e.Done()); err != nil {
					return err
				}
				continue
			}
		}
		if active.Stopping {
			outcome.Success = false
			outcome.Segments = llm.TextSegments("异步任务 " + id + " 被用户取消；不是新的执行。")
		}
		break
	}
	retire()
	row, err := c.deps.SessionRows.Get(locked, sid)
	if err != nil {
		release()
		return err
	}
	head := agentshare.ToolCallStorageMessage(sid, "", "", []llm.ToolCallRequest{call}, "")
	result := agentshare.ToolResultStorageMessage(sid, llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: outcome.Segments})
	in := turn.Input{Reports: []*storage.Message{&head, &result}}
	admitted, err := c.admitReport(locked, row, in)
	release()
	if err != nil {
		return err
	}
	if admitted.Status != operationresult.Success {
		return fmt.Errorf("异步结果投递: %s", admitted.Message)
	}
	var out agentshare.Output = foregroundTurnOutput{ExecutionSender: c.deps.Output}
	if memberOutput, ok := ctx.Value(coagentOutputKey{}).(agentshare.Output); ok {
		out = memberOutput
	}
	stream := out.StartStream(ctx)
	_, _ = stream.Send(ctx, delivery.StreamEvent{Type: delivery.StreamToolCall, ToolCallID: call.ID, ToolCall: &delivery.ToolCall{Name: call.Name, Arguments: call.Arguments}})
	_, _ = stream.Send(ctx, delivery.StreamEvent{Type: delivery.StreamToolResult, ToolCallID: call.ID, ToolResult: &outcome})
	if admitted.Data == nil {
		return nil
	}
	defer c.deps.Turns.ReleaseReservation(sid, admitted.Data)
	ctx = agentshare.WithInboundTurnInput(turn.WithExecution(ctx, admitted.Data), in)
	_, r, err := c.Run(ctx, row, "", out)
	if err == nil && r.Status != operationresult.Success {
		return fmt.Errorf("异步结果续跑: %s", r.Message)
	}
	return err
}

// admitReport runs under the caller's session binding lock. A nil execution
// means the active turn owns the pending input; otherwise the caller owns a reservation.
func (c *ExecutionCoordinator) admitReport(ctx context.Context, row *storage.Session, in turn.Input) (result operationresult.Result[*turn.Execution], err error) {
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
		if idle {
			_, publicAsync := ctx.Value(coagentOutputKey{}).(agentshare.Output)
			e.CoagentBoardOutput = row.TaskType == "coagent" && (in.BoardNotice || publicAsync)
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
