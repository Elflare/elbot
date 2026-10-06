package responses

import (
	"context"
	"encoding/json"
	"fmt"

	api "elbot/internal/llm/responses"
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
	w, err := c.Load(ctx, source, cp)
	if err != nil {
		return nil, err
	}
	exchange, err := c.Repository.GetExchange(ctx, cp.ExchangeID)
	if err != nil {
		return nil, err
	}
	var output []api.Item
	if err := json.Unmarshal([]byte(exchange.ItemsJSON), &output); err != nil {
		return nil, err
	}
	calls, err := checkpointCalls(cp, output)
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
	seed, err := w.Seed(continuation, calls, cp.ResponseID)
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
	w, err := c.Load(ctx, source, cp)
	if err != nil {
		return nil, err
	}
	pending, err := c.Repository.PendingInputs(ctx, source.ID)
	if err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		return nil, fmt.Errorf("后台副本需要完整提交的原生窗口")
	}
	var calls []storage.NativeCall
	responseID, expected := "", ""
	if cp != nil {
		exchange, err := c.Repository.GetExchange(ctx, cp.ExchangeID)
		if err != nil {
			return nil, err
		}
		var output []api.Item
		if err := json.Unmarshal([]byte(exchange.ItemsJSON), &output); err != nil {
			return nil, err
		}
		calls, err = checkpointCalls(cp, output)
		if err != nil {
			return nil, err
		}
		responseID, expected = cp.ResponseID, cp.ID
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
