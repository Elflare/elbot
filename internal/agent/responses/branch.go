package responses

import (
	"context"
	"elbot/internal/operationresult"
	"errors"

	"elbot/internal/session"
	"elbot/internal/storage"
)

func (c *Context) PrepareFork(ctx context.Context, source *storage.Session, message *storage.Message) (operationresult.Result[*session.PreparedMaterial], error) {
	latest, err := c.Repository.CurrentCheckpoint(ctx, source.ID)
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	if latest == nil {
		return operationresult.Result[*session.PreparedMaterial]{Status: operationresult.InvalidTarget, Message: "Responses fork 缺少完整原生 checkpoint"}, nil
	}
	cp, err := c.Repository.CheckpointForMessage(ctx, source.ID, message.ID)
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[*session.PreparedMaterial]{Status: operationresult.InvalidTarget, Message: "指定回复没有完整原生 checkpoint"}, nil
	}
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	records, err := c.Repository.LoadNativeWindow(ctx, source, cp)
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	loaded, err := c.Load(ctx, source, cp, records)
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	if loaded.Status != operationresult.Success {
		return operationresult.Result[*session.PreparedMaterial]{Status: loaded.Status, Message: loaded.Message}, nil
	}
	w := loaded.Data
	calls, err := checkpointCalls(cp)
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	continuation, err := closeBranchCalls(w, calls)
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	if err := validateCallLinks(w.Items, false); err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	if err := c.Validate(ctx, w); err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	state, err := exchangeStorage(w.exchange)
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	responseID := ""
	if state.Available {
		responseID = cp.ResponseID
	}
	seed, err := w.Seed(continuation, calls, responseID)
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	return operationresult.Result[*session.PreparedMaterial]{Status: operationresult.Success, Data: &session.PreparedMaterial{Seed: seed, ExpectedCheckpointID: latest.ID}}, nil
}

func (c *Context) PrepareCopy(ctx context.Context, source *storage.Session) (operationresult.Result[*session.PreparedMaterial], error) {
	cp, err := c.Repository.CurrentCheckpoint(ctx, source.ID)
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	pending, err := c.Repository.PendingInputs(ctx, source.ID)
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	if len(pending) > 0 {
		if cp == nil || cp.MessageID == "" {
			return operationresult.Result[*session.PreparedMaterial]{Status: operationresult.Conflict, Message: "后台副本需要完整提交的原生窗口"}, nil
		}
		snapshot, err := c.Repository.CheckpointForMessage(ctx, source.ID, cp.MessageID)
		if err != nil {
			return operationresult.Result[*session.PreparedMaterial]{}, err
		}
		calls, err := checkpointCalls(snapshot)
		if err != nil {
			return operationresult.Result[*session.PreparedMaterial]{}, err
		}
		ids := map[string]bool{}
		for _, call := range calls {
			if call.Status == "pending" {
				return operationresult.Result[*session.PreparedMaterial]{Status: operationresult.Conflict, Message: "后台副本不能包含未封存的工具批次"}, nil
			}
			ids[call.ResultInputID] = true
		}
		for _, input := range pending {
			if !ids[input.ID] {
				return operationresult.Result[*session.PreparedMaterial]{Status: operationresult.Conflict, Message: "后台副本需要完整提交的原生窗口"}, nil
			}
		}
		return c.PrepareFork(ctx, source, &storage.Message{ID: cp.MessageID})
	}
	records, err := c.Repository.LoadNativeWindow(ctx, source, cp)
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	loaded, err := c.Load(ctx, source, cp, records)
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	if loaded.Status != operationresult.Success {
		return operationresult.Result[*session.PreparedMaterial]{Status: loaded.Status, Message: loaded.Message}, nil
	}
	w := loaded.Data
	var calls []storage.NativeCall
	responseID, expected := "", ""
	if cp != nil {
		calls, err = checkpointCalls(cp)
		if err != nil {
			return operationresult.Result[*session.PreparedMaterial]{}, err
		}
		state, err := exchangeStorage(w.exchange)
		if err != nil {
			return operationresult.Result[*session.PreparedMaterial]{}, err
		}
		if state.Available {
			responseID = cp.ResponseID
		}
		expected = cp.ID
	}
	continuation, err := closeBranchCalls(w, calls)
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	if err := validateCallLinks(w.Items, false); err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	if err := c.Validate(ctx, w); err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	seed, err := w.Seed(continuation, calls, responseID)
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	return operationresult.Result[*session.PreparedMaterial]{Status: operationresult.Success, Data: &session.PreparedMaterial{Seed: seed, ExpectedCheckpointID: expected}}, nil
}
