package execution

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/operationresult"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/modelmgr"

	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// ModelContext supplies the prospective main-dialogue target to tool views.
// Execution still resolves and checks its fixed selection at admission.
func (c *ExecutionCoordinator) ModelContext(ctx context.Context, row *storage.Session) (context.Context, error) {
	selected := c.deps.SelectionForTurn(ctx, row)
	if selected.Client == nil {
		return ctx, fmt.Errorf("model provider %q not found", selected.Provider)
	}
	return c.deps.View.WithModel(ctx, selected), nil
}

func (c *ExecutionCoordinator) DefaultModelContext(ctx context.Context) (context.Context, error) {
	return c.ModelContext(ctx, nil)
}

func (c *ExecutionCoordinator) runAttempt(ctx context.Context, session *storage.Session, text string, out agentshare.Output) (context.Context, *storage.Session, operationresult.Result[turn.Input], error) {
	ctx, admitted, release, err := c.enterTurn(ctx, session)
	if err != nil {
		return ctx, session, operationresult.Result[turn.Input]{}, err
	}
	if admitted.Status != operationresult.Success {
		return ctx, session, operationresult.Result[turn.Input]{Status: admitted.Status, Message: admitted.Message}, nil
	}
	selection := c.deps.SelectionForTurn(ctx, session)
	validation, err := func() (operationresult.Result[struct{}], error) {
		defer release()
		source, _, err := sessionpkg.Origin(session)
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		checked, err := c.deps.Dialogue.CheckSelection(selection)
		if err != nil || checked.Status != operationresult.Success {
			return checked, err
		}
		compatible, err := modelmgr.CanSwitch(source, selection.Origin)
		if err != nil || compatible.Status != operationresult.Success {
			return compatible, err
		}
		ctx = c.deps.View.WithModel(ctx, selection)
		registered, err := c.deps.Sessions.RegisterOrigin(ctx, session.ID, selection.Origin)
		if err == nil && registered.Status == operationresult.Success {
			*session = *registered.Data
		}
		return operationresult.Result[struct{}]{Status: registered.Status, Message: registered.Message}, err
	}()
	if err != nil {
		return ctx, session, operationresult.Result[turn.Input]{}, err
	}
	if validation.Status != operationresult.Success {
		return ctx, session, operationresult.Result[turn.Input]{Status: validation.Status, Message: validation.Message}, nil
	}
	ctx, compacted, err := c.compactBeforeTurn(ctx, session, text, out, selection)
	if err != nil {
		return ctx, session, operationresult.Result[turn.Input]{}, err
	}
	if compacted.Status != operationresult.Success {
		return ctx, session, operationresult.Result[turn.Input]{Status: compacted.Status, Message: compacted.Message}, nil
	}
	session = compacted.Data
	ctx, admitted, release, err = c.enterTurn(ctx, session)
	if err != nil {
		return ctx, session, operationresult.Result[turn.Input]{}, err
	}
	if admitted.Status != operationresult.Success {
		return ctx, session, operationresult.Result[turn.Input]{Status: admitted.Status, Message: admitted.Message}, nil
	}
	attempt, err := c.deps.Turns.AllocateAttempt(ctx)
	if err != nil {
		release()
		return ctx, session, operationresult.Result[turn.Input]{}, err
	}
	ctx = turn.WithAttempt(ctx, attempt)
	execution := turn.ExecutionFromContext(ctx)
	if !c.deps.Turns.StartAttempt(session.ID, agentshare.InboundTurnInput(ctx, text), execution, attempt) {
		release()
		if c.deps.Turns.Snapshot(session.ID).Execution == execution {
			return ctx, session, operationresult.Result[turn.Input]{Status: operationresult.Success}, nil
		}
		return ctx, session, operationresult.Result[turn.Input]{Status: operationresult.Busy, Message: "当前会话已有其他执行，输入尚未开始处理"}, nil
	}
	// Restore durable usage before cancellation can publish the first snapshot.
	var reqInfo request.Request
	var reqCtx context.Context
	var done func()
	if _, err = c.deps.Status.Usage(session); err == nil {
		reqInfo, reqCtx, done, err = c.deps.Requests.Start(ctx, request.StartRequest{SessionID: session.ID, Kind: request.KindTurn, Label: "chat", Timeout: c.deps.ResponseTimeout})
	}
	if err != nil {
		c.deps.Turns.FinishAttempt(session.ID, attempt)
		release()
		return ctx, session, operationresult.Result[turn.Input]{}, err
	}
	release()
	stopObserver := sync.OnceFunc(c.observeRequestStop(reqCtx, session.ID, attempt))
	defer func() { stopObserver(); c.deps.Turns.FinishAttempt(session.ID, attempt); done() }()
	reqCtx, result, pending, runErr := c.runTurn(ctx, reqCtx, reqInfo.ID, session, text, out, selection)
	stopObserver()
	facts, _ := contextinfo.ExecutionFromContext(reqCtx)
	ctx = c.deps.View.Context(contextinfo.WithExecution(ctx, facts))
	if model, ok := contextinfo.ModelFromContext(reqCtx); ok {
		ctx = contextinfo.WithModel(ctx, model)
	}
	if runErr != nil {
		if err := c.finishAttempt(ctx, session, runErr); err != nil {
			return ctx, session, operationresult.Result[turn.Input]{}, err
		}
		return ctx, session, operationresult.Result[turn.Input]{Status: operationresult.Success}, nil
	}
	if result.Status != operationresult.Success {
		c.deps.Turns.StopAttempt(session.ID, attempt)
		c.deps.Status.Record(ctx, runtimeDoneStatus(c.deps.Status.Snapshot(session.ID)))
		return ctx, session, operationresult.Result[turn.Input]{Status: result.Status, Message: result.Message}, nil
	}
	if err := c.finishAttempt(ctx, session, nil); err != nil {
		return ctx, session, operationresult.Result[turn.Input]{}, err
	}
	return ctx, session, operationresult.Result[turn.Input]{Status: operationresult.Success, Data: pending}, nil
}

func (c *ExecutionCoordinator) finishAttempt(ctx context.Context, session *storage.Session, runErr error) error {
	attempt := turn.AttemptFromContext(ctx)
	execution := turn.ExecutionFromContext(ctx)
	current := c.deps.Turns.Snapshot(session.ID)
	if runErr != nil {
		if current.Attempt != attempt || current.Phase == turn.PhaseAwaitAppendConfirm {
			return nil
		}
		execution.Finish(runErr)
		status := c.deps.Status.Snapshot(session.ID)
		status.Phase = runtimestatus.PhaseError
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			status = runtimeDoneStatus(status)
		}
		status.FinishedAt = storage.Now()
		status.Error = runErr.Error()
		c.deps.Status.Record(ctx, status)
		c.deps.Turns.StopAttempt(session.ID, attempt)
		return runErr
	}
	status := c.deps.Status.Snapshot(session.ID)
	if status.Running() && (current.Phase == turn.PhaseIdle || current.Attempt == attempt) {
		c.deps.Status.Record(ctx, runtimeDoneStatus(status))
	}
	return nil
}

func inboundReplyMessageID(ctx context.Context) string {
	msg, ok := agentshare.MessageContextFrom(ctx)
	if !ok {
		return ""
	}
	if id := strings.TrimSpace(msg.Reply.MessageID); id != "" {
		return id
	}
	info, _ := contextinfo.ConversationFromContext(ctx)
	return strings.TrimSpace(info.ReplyToMessageID)
}

func (c *ExecutionCoordinator) runTurn(ctx, reqCtx context.Context, requestID string, session *storage.Session, text string, out agentshare.Output, selection modelmgr.Selection) (context.Context, operationresult.Result[agentshare.TurnResult], turn.Input, error) {
	input := agentshare.InboundTurnInput(ctx, text)
	prepared, err := c.deps.Dialogue.PrepareTurn(ctx, agentshare.TurnInput{Session: session, Text: text, Input: input, ReplyToPlatformMessageID: inboundReplyMessageID(ctx), Selection: selection})
	if err != nil {
		return reqCtx, operationresult.Result[agentshare.TurnResult]{}, turn.Input{}, err
	}
	current := c.deps.Turns.Snapshot(session.ID)
	if current.Attempt != turn.AttemptFromContext(ctx) || current.Phase == turn.PhaseStopping {
		return reqCtx, operationresult.Result[agentshare.TurnResult]{}, turn.Input{}, context.Canceled
	}
	reqCtx, result, err := c.deps.Dialogue.RunTurn(ctx, reqCtx, agentshare.TurnInput{Session: session, Text: text, Selection: selection, Input: input, RequestID: requestID, Prepared: prepared}, out)
	if err != nil {
		if reqCtx.Err() != nil && errors.Is(err, reqCtx.Err()) {
			c.handleTurnContextDone(ctx, session.ID, reqCtx.Err())
		}
		return reqCtx, operationresult.Result[agentshare.TurnResult]{}, turn.Input{}, err
	}
	if result.Status != operationresult.Success {
		return reqCtx, result, turn.Input{}, nil
	}
	data := result.Data
	if data.Outcome != agentshare.Completed {
		return reqCtx, result, turn.Input{}, nil
	}
	pending, err := c.finishCompletedTurn(ctx, session, out, data)
	if err != nil {
		return reqCtx, operationresult.Result[agentshare.TurnResult]{}, turn.Input{}, err
	}
	return reqCtx, result, pending, nil
}

func (c *ExecutionCoordinator) observeRequestStop(ctx context.Context, sessionID, attempt string) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		if c.deps.Turns.Snapshot(sessionID).Phase == turn.PhaseAwaitAppendConfirm {
			return
		}
		c.deps.Turns.StopAttempt(sessionID, attempt)
		status := c.deps.Status.Snapshot(sessionID)
		status.SessionID, status.Phase = sessionID, runtimestatus.PhaseStopping
		c.deps.Status.Record(ctx, status)
	})
	return func() {
		if !stop() {
			<-done
		}
	}
}

func (c *ExecutionCoordinator) finishCompletedTurn(ctx context.Context, session *storage.Session, out agentshare.Output, result agentshare.TurnResult) (turn.Input, error) {
	ctx = c.deps.View.Context(ctx)
	selection, turnStartedAt, committed := result.Selection, result.StartedAt, result.Committed
	usage, err := c.deps.Status.Usage(session)
	if err != nil {
		return turn.Input{}, err
	}
	if err := c.deps.Contexts.RecordTurn(ctx, session.ID, usage); err != nil {
		return turn.Input{}, fmt.Errorf("保存会话活动时间和用量: %w", err)
	}
	doneStatus := runtimeDoneStatus(runtimestatus.Snapshot{
		SessionID:      session.ID,
		Provider:       selection.Provider,
		Model:          selection.Model,
		Mode:           session.Mode,
		TurnStartedAt:  turnStartedAt,
		StageStartedAt: turnStartedAt,
		Usage:          usage,
	})
	c.deps.Status.Record(ctx, doneStatus)
	nextSelection := c.deps.SelectionForTurn(ctx, session)
	if c.shouldCompact(ctx, session, nextSelection) {
		if _, err := out.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "compact → next request"}); err != nil {
			record := events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "compact_notice_failed", Module: "agent", Content: err.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err)}
			if record.ResultStatus == events.ResultCanceled {
				record.Level = slog.LevelInfo
			}
			events.AsyncLog(ctx, record)
		}
	}
	pending, completed := c.deps.Turns.CompleteTurn(session.ID, turn.AttemptFromContext(ctx))
	if !completed {
		return turn.Input{}, nil
	}
	if execution := turn.ExecutionFromContext(ctx); execution != nil {
		execution.SetResult(session.ID, committed.MessageID, committed.RawText)
	}
	ctx = c.deps.View.WithModel(ctx, selection)
	c.deps.Sessions.MaybeScheduleNaming(ctx, session.ID)
	return pending, nil
}

func (c *ExecutionCoordinator) handleTurnContextDone(ctx context.Context, sessionID string, err error) {
	current := c.deps.Turns.Snapshot(sessionID)
	if current.Execution != turn.ExecutionFromContext(ctx) || (current.Phase != turn.PhaseAwaitAppendConfirm && current.Attempt == turn.AttemptFromContext(ctx)) {
		if e := turn.ExecutionFromContext(ctx); e != nil {
			e.Finish(err)
		}
	}
}
