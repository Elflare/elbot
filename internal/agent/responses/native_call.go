package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	api "elbot/internal/llm/responses"
	"elbot/internal/storage"
)

type nativeCallResult struct {
	Exchange      *storage.NativeExchange
	Response      *api.Response
	Items         []api.Item
	OutputStarted bool
	FactsSaved    bool
}

// Each actual HTTP attempt gets its own immutable request and terminal facts.
// Local hooks/inputs are prepared by the caller once, outside recovery retries.
func (s *turnState) nativeCall(ctx context.Context, client api.Streamer, request api.Request, consumed []string, stream delivery.MessageStream) (result nativeCallResult, err error) {
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := s.route.View.CheckSelection(s.session, s.selection); err != nil {
		return result, err
	}
	prepared, err := client.PrepareRequest(request)
	if err != nil {
		return result, err
	}
	origin, err := s.route.View.Providers.OriginFor(s.selection.Provider)
	if err != nil {
		return result, err
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	ids, err := json.Marshal(consumed)
	if err != nil {
		return result, err
	}
	exchange := &storage.NativeExchange{SessionID: s.session.ID, Protocol: string(origin.Protocol), Provider: origin.Provider, BaseURL: origin.BaseURL, Model: s.selection.Model, RequestID: facts.RequestID, RunID: facts.RunID, Attempt: facts.Attempt, PreviousCheckpointID: s.checkpointID(), RequestJSON: string(prepared.JSON()), InputIDsJSON: string(ids)}
	locked, release, err := s.route.Messages.Gate.Enter(ctx, s.session.ID)
	if err != nil {
		return result, err
	}
	err = s.route.Repository.CreateExchange(locked, exchange)
	release()
	if err != nil {
		return result, fmt.Errorf("save native request: %w", err)
	}
	result.Exchange = exchange
	status := "failed"
	completed := map[int]api.Item{}
	defer func() {
		raw := ""
		if result.Response != nil {
			raw = string(result.Response.Raw)
		}
		result.Items = outputItems(result.Response, completed)
		encoded, encodeErr := json.Marshal(result.Items)
		if encodeErr != nil {
			err = errors.Join(err, encodeErr)
			return
		}
		failure := ""
		if err != nil {
			failure = err.Error()
		}
		if saveErr := s.route.Repository.FinishExchange(context.WithoutCancel(s.ctx), exchange.ID, status, raw, string(encoded), failure); saveErr != nil {
			err = errors.Join(err, fmt.Errorf("save native response: %w", saveErr))
		} else {
			result.FactsSaved = true
		}
	}()
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := client.StreamPrepared(callCtx, prepared)
	if err != nil {
		if ctx.Err() != nil {
			status = "canceled"
			return result, ctx.Err()
		}
		return result, err
	}
	reasoningOpen := false
	defer func() {
		if reasoningOpen {
			s.output.SendReasoning(ctx, "[/thinking]\n\n")
		}
	}()
	for event := range events {
		if event.Response != nil {
			result.Response = event.Response
		}
		if event.Item != nil || event.Delta != "" {
			result.OutputStarted = true
		}
		if event.Response != nil && len(event.Response.Output) > 0 {
			result.OutputStarted = true
		}
		if event.Type == "response.output_item.done" && event.Item != nil {
			completed[event.OutputIndex] = *event.Item
		}
		if event.Error != nil {
			if event.Type != "" {
				status = event.Type
			}
			if ctx.Err() != nil {
				status = "canceled"
				return result, ctx.Err()
			}
			return result, event.Error
		}
		switch event.Type {
		case "response.output_text.delta", "response.refusal.delta":
			if stream != nil && event.Delta != "" {
				if err := stream.Append(ctx, event.Delta); err != nil {
					return result, err
				}
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if s.route.Calls.Identity.IsCLI(ctx) && event.Delta != "" {
				if !reasoningOpen {
					s.output.SendReasoning(ctx, "[thinking] ")
					reasoningOpen = true
				}
				s.output.SendReasoning(ctx, event.Delta)
			}
		case "response.completed":
			status = "completed"
		}
	}
	if err := ctx.Err(); err != nil {
		status = "canceled"
		return result, err
	}
	if status != "completed" || result.Response == nil || result.Response.Status != "completed" || result.Response.ID == "" {
		return result, fmt.Errorf("responses stream: %w", io.ErrUnexpectedEOF)
	}
	return result, nil
}
