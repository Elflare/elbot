package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"elbot/internal/agent/dialogue"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	api "elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type Compactor struct {
	Context  *Context
	Messages *dialogue.MessageStore
	View     dialogue.ExecutionView
	System   dialogue.SystemPromptManager
	Identity dialogue.Identity
}

func (c *Compactor) Prepare(ctx context.Context, row *storage.Session, reason string, selection modelmgr.Selection) (prepared *contextmgr.PreparedCompact, err error) {
	target, err := c.View.Providers.OriginFor(selection.Provider)
	if err != nil {
		return nil, err
	}
	source, _, err := session.Origin(row)
	if err != nil {
		return nil, err
	}
	if err := modelmgr.CanSwitch(source, target); err != nil {
		return nil, err
	}
	client, ok := selection.Client.(api.Compactor)
	if !ok {
		return nil, fmt.Errorf("当前 Responses 模型不支持原生压缩")
	}
	ctx, err = c.View.WithModel(ctx, selection)
	if err != nil {
		return nil, err
	}
	cp, err := c.Context.Repository.CurrentCheckpoint(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	w, err := c.Context.Load(ctx, row, cp)
	if err != nil {
		return nil, err
	}
	pending, err := c.Context.Repository.PendingInputs(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	for _, input := range pending {
		item, segments, err := decodeQueuedInput(input)
		if err != nil {
			return nil, err
		}
		if item.Type != "additional_tools" {
			w.Materials = append(w.Materials, material{ItemIndex: len(w.Items), Segments: segments})
		}
		w.Items = append(w.Items, item)
	}
	if cp != nil {
		calls, err := c.Context.Repository.Calls(ctx, cp.ExchangeID)
		if err != nil {
			return nil, err
		}
		if _, err := closeBranchCalls(w, calls); err != nil {
			return nil, err
		}
	}
	if err := validateCallLinks(w.Items, false); err != nil {
		return nil, err
	}
	items, release, err := c.Context.Resolve(ctx, w)
	if err != nil {
		return nil, err
	}
	defer release()
	instructions, err := c.System.Build(ctx, dialogue.SystemPromptRequest{Mode: row.Mode, Session: row, Scope: c.Identity.Scope(ctx)})
	if err != nil {
		return nil, err
	}
	request, err := client.PrepareCompact(api.CompactRequest{Model: selection.Model, Instructions: instructions, Input: items})
	if err != nil {
		return nil, err
	}
	checkpointID := ""
	if cp != nil {
		checkpointID = cp.ID
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	exchange := &storage.NativeExchange{SessionID: row.ID, APIType: string(target.APIType), Provider: target.Provider, BaseURL: target.BaseURL, Model: selection.Model, RequestID: facts.RequestID, RunID: facts.RunID, Attempt: facts.Attempt, PreviousCheckpointID: checkpointID, RequestJSON: string(request.JSON()), InputIDsJSON: "[]"}
	locked, leave, err := c.Messages.Gate.Enter(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	err = c.Context.Repository.CreateExchange(locked, exchange)
	leave()
	if err != nil {
		return nil, err
	}
	var result *api.CompactResult
	defer func() {
		status, raw, output, failure := "failed", "", "", ""
		if result != nil {
			raw = string(result.Raw)
			encoded, encodeErr := json.Marshal(result.Output)
			if encodeErr != nil {
				err = errors.Join(err, encodeErr)
			} else {
				output = string(encoded)
			}
		}
		if err == nil {
			status = "compacted"
		} else {
			failure = err.Error()
			if ctx.Err() != nil {
				status = "canceled"
			}
		}
		if saveErr := c.Context.Repository.FinishExchange(context.WithoutCancel(ctx), exchange.ID, status, raw, output, failure); saveErr != nil {
			err = errors.Join(err, saveErr)
		}
	}()
	result, err = client.CompactPrepared(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	nextWindow := &nativeWindow{Items: []api.Item{result.Compaction}, Origin: target, Checkpoint: cp, RetainedMediaIDs: w.mediaIDs()}
	if err := validateCallLinks(nextWindow.Items, false); err != nil {
		return nil, err
	}
	if err := c.Context.Validate(ctx, nextWindow); err != nil {
		return nil, err
	}
	seed, err := nextWindow.Seed(nil, nil, "")
	if err != nil {
		return nil, err
	}
	seed.ID = storage.NewID()
	state, err := contextmgr.DecodeState(row.Metadata)
	if err != nil {
		return nil, err
	}
	title, generation, base := contextmgr.NextCompactedTitle(row, state.Compact)
	compact := &contextmgr.CompactState{Pending: true, SeedID: seed.ID, SourceSessionID: row.ID, Provider: selection.Provider, Model: selection.Model, TriggerReason: reason, Generation: generation, BaseTitle: base}
	if usage := result.TokenUsage(); usage != nil {
		compact.SourceTokens, compact.SummaryTokens, compact.TotalTokens, compact.CacheHitTokens = usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.CacheHitTokens
	}
	return &contextmgr.PreparedCompact{Title: title, State: compact, Seed: seed, ExpectedCheckpointID: checkpointID}, nil
}
