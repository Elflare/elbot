package responses

import (
	"context"
	"fmt"

	"elbot/internal/session"
	"elbot/internal/storage"
)

func (c *Context) PrepareFork(ctx context.Context, source *storage.Session, message *storage.Message) (*session.PreparedMaterial, error) {
	latest, err := c.Repository.CurrentCheckpoint(ctx, source.ID)
	if err != nil {
		return nil, err
	}
	if latest == nil {
		return nil, fmt.Errorf("Responses fork 缺少完整原生 checkpoint")
	}
	cp, err := c.Repository.CheckpointForMessage(ctx, source.ID, message.ID)
	if err != nil {
		return nil, fmt.Errorf("指定回复没有完整原生 checkpoint: %w", err)
	}
	records, err := c.Repository.LoadNativeWindow(ctx, source, cp)
	if err != nil {
		return nil, err
	}
	w, err := c.Load(ctx, source, cp, records)
	if err != nil {
		return nil, err
	}
	calls, err := checkpointCalls(cp)
	if err != nil {
		return nil, err
	}
	continuation, err := closeBranchCalls(w, calls)
	if err != nil {
		return nil, err
	}
	if err := validateCallLinks(w.Items, false); err != nil {
		return nil, err
	}
	if err := c.Validate(ctx, w); err != nil {
		return nil, err
	}
	state, err := exchangeStorage(w.exchange)
	if err != nil {
		return nil, err
	}
	responseID := ""
	if state.Available {
		responseID = cp.ResponseID
	}
	seed, err := w.Seed(continuation, calls, responseID)
	if err != nil {
		return nil, err
	}
	return &session.PreparedMaterial{Seed: seed, ExpectedCheckpointID: latest.ID}, nil
}

func (c *Context) PrepareCopy(ctx context.Context, source *storage.Session) (*session.PreparedMaterial, error) {
	cp, err := c.Repository.CurrentCheckpoint(ctx, source.ID)
	if err != nil {
		return nil, err
	}
	pending, err := c.Repository.PendingInputs(ctx, source.ID)
	if err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		if cp == nil || cp.MessageID == "" {
			return nil, fmt.Errorf("后台副本需要完整提交的原生窗口")
		}
		snapshot, err := c.Repository.CheckpointForMessage(ctx, source.ID, cp.MessageID)
		if err != nil {
			return nil, err
		}
		calls, err := checkpointCalls(snapshot)
		if err != nil {
			return nil, err
		}
		ids := map[string]bool{}
		for _, call := range calls {
			if call.Status == "pending" {
				return nil, fmt.Errorf("后台副本不能包含未封存的工具批次")
			}
			ids[call.ResultInputID] = true
		}
		for _, input := range pending {
			if !ids[input.ID] {
				return nil, fmt.Errorf("后台副本需要完整提交的原生窗口")
			}
		}
		return c.PrepareFork(ctx, source, &storage.Message{ID: cp.MessageID})
	}
	records, err := c.Repository.LoadNativeWindow(ctx, source, cp)
	if err != nil {
		return nil, err
	}
	w, err := c.Load(ctx, source, cp, records)
	if err != nil {
		return nil, err
	}
	var calls []storage.NativeCall
	responseID, expected := "", ""
	if cp != nil {
		calls, err = checkpointCalls(cp)
		if err != nil {
			return nil, err
		}
		state, err := exchangeStorage(w.exchange)
		if err != nil {
			return nil, err
		}
		if state.Available {
			responseID = cp.ResponseID
		}
		expected = cp.ID
	}
	continuation, err := closeBranchCalls(w, calls)
	if err != nil {
		return nil, err
	}
	if err := validateCallLinks(w.Items, false); err != nil {
		return nil, err
	}
	if err := c.Validate(ctx, w); err != nil {
		return nil, err
	}
	seed, err := w.Seed(continuation, calls, responseID)
	if err != nil {
		return nil, err
	}
	return &session.PreparedMaterial{Seed: seed, ExpectedCheckpointID: expected}, nil
}
