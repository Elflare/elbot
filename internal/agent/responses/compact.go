package responses

import (
	agentshare "elbot/internal/agent/share"
	"elbot/internal/operationresult"

	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type Compactor struct {
	Context *Context
	Gate    interface {
		Enter(context.Context, string) (context.Context, func(), error)
	}
	View interface {
		WithModel(context.Context, modelmgr.Selection) (context.Context, error)
	}
	System interface {
		Build(context.Context, agentshare.SystemPromptRequest) (string, error)
	}
	Identity interface {
		Scope(context.Context) session.Scope
	}
}

func (c *Compactor) Prepare(ctx context.Context, row *storage.Session, reason string, selection modelmgr.Selection) (prepared operationresult.Result[*contextmgr.PreparedCompact], err error) {
	target := selection.Origin
	source, _, err := session.Origin(row)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	compatible, err := modelmgr.CanSwitch(source, target)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	if compatible.Status != operationresult.Success {
		return operationresult.Result[*contextmgr.PreparedCompact]{Status: compatible.Status, Message: compatible.Message}, nil
	}
	client, ok := selection.Client.(api.Compactor)
	if !ok {
		return operationresult.Result[*contextmgr.PreparedCompact]{Status: operationresult.Incompatible, Message: "当前 Responses 模型不支持原生压缩"}, nil
	}
	ctx, err = c.View.WithModel(ctx, selection)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	cp, err := c.Context.Repository.CurrentCheckpoint(ctx, row.ID)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	records, err := c.Context.Repository.LoadNativeWindow(ctx, row, cp)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	loaded, err := c.Context.Load(ctx, row, cp, records)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	if loaded.Status != operationresult.Success {
		return operationresult.Result[*contextmgr.PreparedCompact]{Status: loaded.Status, Message: loaded.Message}, nil
	}
	w := loaded.Data
	pending, err := c.Context.Repository.PendingInputs(ctx, row.ID)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	for _, input := range pending {
		item, segments, err := decodeQueuedInput(input)
		if err != nil {
			return operationresult.Result[*contextmgr.PreparedCompact]{}, err
		}
		if item.Type != "additional_tools" {
			w.Materials = append(w.Materials, material{Index: len(w.Items), MessageID: input.MessageID, Segments: segments})
		}
		w.Items = append(w.Items, item)
		w.References = append(w.References, inputReference{Kind: "input", ID: input.ID})
	}
	if cp != nil {
		calls, err := c.Context.Repository.Calls(ctx, cp.ExchangeID)
		if err != nil {
			return operationresult.Result[*contextmgr.PreparedCompact]{}, err
		}
		if _, err := closeBranchCalls(w, calls); err != nil {
			return operationresult.Result[*contextmgr.PreparedCompact]{}, err
		}
	}
	if err := validateCallLinks(w.Items, false); err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	items, usageID, err := c.Context.Resolve(ctx, w)
	defer c.Context.Preparer.FinishMaterials(ctx, usageID)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	if items.Status != operationresult.Success {
		return operationresult.Result[*contextmgr.PreparedCompact]{Status: items.Status, Message: items.Message}, nil
	}
	instructions, err := c.System.Build(ctx, agentshare.SystemPromptRequest{Mode: row.Mode, Session: row, Scope: c.Identity.Scope(ctx)})
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	request, err := client.PrepareCompact(api.CompactRequest{Model: selection.Model, Instructions: instructions, Input: items.Data})
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	checkpointID := ""
	if cp != nil {
		checkpointID = cp.ID
	}
	refs := append(append([]inputReference(nil), w.References...), inputReference{Kind: "synthetic", Item: json.RawMessage(`{"type":"compaction_trigger"}`)})
	audit, err := requestAudit(request.ParametersJSON(), refs)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	result, err := llm.Retry(ctx, selection.Client, func(ctx context.Context, request api.PreparedCompactRequest) (*api.CompactResult, error) {
		return c.compactOnce(ctx, row, selection, client, request, checkpointID, audit)
	}, request)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	nextWindow := &nativeWindow{Items: []api.Item{result.Compaction}, Origin: target, Checkpoint: cp, RetainedMediaIDs: w.mediaIDs()}
	if err := validateCallLinks(nextWindow.Items, false); err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	if err := c.Context.Validate(ctx, nextWindow); err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	seed, err := nextWindow.Seed(nil, nil, "")
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	state, err := contextmgr.DecodeState(row.Metadata)
	if err != nil {
		return operationresult.Result[*contextmgr.PreparedCompact]{}, err
	}
	title, generation, base := contextmgr.NextCompactedTitle(row, state.Compact)
	compact := &contextmgr.CompactState{
		SourceSessionID: row.ID,
		Provider:        selection.Provider,
		Model:           selection.Model,
		TriggerReason:   reason,
		Generation:      generation,
		BaseTitle:       base,
	}
	if usage := result.TokenUsage(); usage != nil {
		compact.SourceTokens, compact.SummaryTokens, compact.TotalTokens, compact.CacheHitTokens = usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.CacheHitTokens
	}
	return operationresult.Result[*contextmgr.PreparedCompact]{Status: operationresult.Success, Data: &contextmgr.PreparedCompact{Title: title, State: compact, Seed: seed, ExpectedCheckpointID: checkpointID}}, nil
}

func (c *Compactor) compactOnce(ctx context.Context, row *storage.Session, selection modelmgr.Selection, client api.Compactor, request api.PreparedCompactRequest, checkpointID, audit string) (result *api.CompactResult, err error) {
	target := selection.Origin
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	exchange := &storage.NativeExchange{
		SessionID:            row.ID,
		APIType:              string(target.APIType),
		Provider:             target.Provider,
		BaseURL:              target.BaseURL,
		Model:                selection.Model,
		RequestID:            facts.RequestID,
		RunID:                facts.RunID,
		Attempt:              facts.Attempt,
		PreviousCheckpointID: checkpointID,
		RequestJSON:          audit,
		InputIDsJSON:         "[]",
	}
	locked, leave, err := c.Gate.Enter(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	err = c.Context.Repository.CreateExchange(locked, exchange)
	leave()
	if err != nil {
		return nil, err
	}
	result, err = client.CompactPrepared(ctx, request)
	archiveErr := func() error {
		status, raw, output, failure := "failed", "", "", ""
		if result != nil {
			raw = string(result.Raw)
			encoded, encodeErr := json.Marshal(result.Output)
			if encodeErr != nil {
				return fmt.Errorf("encode compact response: %w", encodeErr)
			}
			output = string(encoded)
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
			return fmt.Errorf("save compact response: %w", saveErr)
		}

		return nil
	}()
	if archiveErr != nil {
		if err == nil {
			return result, archiveErr
		}
		globalevents.AsyncLog(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryLog, Level: slog.LevelError, EventName: "compact_archive_failed", Module: "agent", Content: archiveErr.Error(), Identity: globalevents.LogIdentityFromContext(ctx), ResultStatus: globalevents.ResultFailed})
	}
	return result, err
}
