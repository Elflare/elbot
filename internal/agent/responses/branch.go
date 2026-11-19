package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/operationresult"
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
	expected := ""
	if cp != nil {
		expected = cp.ID
		if len(pending) > 0 && cp.MessageID != "" {
			// Tool results are frozen at the message snapshot without advancing the live cursor.
			cp, err = c.Repository.CheckpointForMessage(ctx, source.ID, cp.MessageID)
			if err != nil {
				return operationresult.Result[*session.PreparedMaterial]{}, err
			}
		}
	}
	records, err := c.Repository.LoadNativeWindow(ctx, source, cp)
	if err != nil {
		return operationresult.Result[*session.PreparedMaterial]{}, err
	}
	var w *nativeWindow
	if cp == nil && records.Seed == nil && len(pending) > 0 {
		checked, err := checkInputFormat(source)
		if err != nil {
			return operationresult.Result[*session.PreparedMaterial]{}, err
		}
		if checked.Status != operationresult.Success {
			return operationresult.Result[*session.PreparedMaterial]{Status: checked.Status, Message: checked.Message}, nil
		}
		origin, known, err := session.Origin(source)
		if err != nil {
			return operationresult.Result[*session.PreparedMaterial]{}, err
		}
		if !known || origin.APIType != llm.APITypeResponse || origin.Provider == "" {
			return operationresult.Result[*session.PreparedMaterial]{}, fmt.Errorf("Responses 原生归属不完整")
		}
		// A stopped first request has durable input but no committed response yet.
		w = &nativeWindow{Origin: origin}
	} else {
		loaded, err := c.Load(ctx, source, cp, records)
		if err != nil {
			return operationresult.Result[*session.PreparedMaterial]{}, err
		}
		if loaded.Status != operationresult.Success {
			return operationresult.Result[*session.PreparedMaterial]{Status: loaded.Status, Message: loaded.Message}, nil
		}
		w = loaded.Data
	}
	var calls []storage.NativeCall
	var continuation []api.Item
	responseID := ""
	if cp != nil {
		calls, err = checkpointCalls(cp)
		if err != nil {
			return operationresult.Result[*session.PreparedMaterial]{}, err
		}
		for _, call := range calls {
			if call.Status == "pending" {
				return operationresult.Result[*session.PreparedMaterial]{Status: operationresult.Conflict, Message: "会话副本不能包含未封存的工具批次"}, nil
			}
		}
		state, err := exchangeStorage(w.exchange)
		if err != nil {
			return operationresult.Result[*session.PreparedMaterial]{}, err
		}
		if state.Available {
			responseID = cp.ResponseID
		}
		continuation, err = closeBranchCalls(w, calls)
		if err != nil {
			return operationresult.Result[*session.PreparedMaterial]{}, err
		}
	} else if records.Seed != nil {
		loaded, err := c.resolveContinuation(records.Seed, w.Items)
		if err != nil {
			return operationresult.Result[*session.PreparedMaterial]{}, err
		}
		continuation = loaded.Items
		responseID = records.Seed.ResponseID
		if err := json.Unmarshal([]byte(records.Seed.CallsJSON), &calls); err != nil {
			return operationresult.Result[*session.PreparedMaterial]{}, err
		}
	}
	ids := map[string]bool{}
	for _, call := range calls {
		if call.ResultInputID != "" {
			ids[call.ResultInputID] = true
		}
	}
	// Match model-call input order; completed results already loaded above stay single.
	sort.SliceStable(pending, func(i, j int) bool { return inputOrder(pending[i]) < inputOrder(pending[j]) })
	for _, input := range pending {
		if ids[input.ID] {
			continue
		}
		item, err := w.appendInput(input)
		if err != nil {
			return operationresult.Result[*session.PreparedMaterial]{}, err
		}
		continuation = append(continuation, item)
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
