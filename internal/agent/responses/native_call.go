package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"elbot/internal/agent/dialogue"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	api "elbot/internal/llm/responses"
	"elbot/internal/storage"
)

type nativeCallResult struct {
	Exchange      *storage.NativeExchange
	Response      *api.Response
	Items         []api.Item
	OutputStarted bool
	FactsSaved    bool
	AllowedTools  []string
}

// Each actual HTTP attempt gets its own immutable request and terminal facts.
// Local hooks/inputs are prepared by the caller once, outside recovery retries.
func (s *turnState) nativeCall(ctx context.Context, client api.Streamer, request api.Request, consumed []string, stream delivery.MessageStream, observation *dialogue.CallObservation) (result nativeCallResult, err error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			observation.LogRecord.ResultStatus, observation.LogRecord.Level = globalevents.ResultCanceled, slog.LevelInfo
		}
		return result, err
	}
	if err := s.route.View.CheckSelection(s.session, s.selection); err != nil {
		return result, err
	}
	prepared, err := client.PrepareRequest(request)
	if err != nil {
		return result, err
	}
	result.AllowedTools = prepared.AllowedTools()
	origin, err := s.route.View.Providers.OriginFor(s.selection.Provider)
	if err != nil {
		return result, err
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	ids, err := json.Marshal(consumed)
	if err != nil {
		return result, err
	}
	exchange := &storage.NativeExchange{SessionID: s.session.ID, APIType: string(origin.APIType), Provider: origin.Provider, BaseURL: origin.BaseURL, Model: s.selection.Model, RequestID: facts.RequestID, RunID: facts.RunID, Attempt: facts.Attempt, PreviousCheckpointID: s.checkpointID(), RequestJSON: string(prepared.JSON()), InputIDsJSON: string(ids)}
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
		observation.Event.Text, observation.Event.SourceText = outputText(result.Items), outputText(result.Items)
		observation.Event.OutputReady = len(result.Items) > 0
		if result.Response != nil {
			observation.Event.Usage = result.Response.TokenUsage()
		}
		if err != nil {
			observation.LogRecord.Content += fmt.Sprintf("\ncall_error=%v", err)
			var failure *api.APIError
			if errors.As(err, &failure) {
				observation.LogRecord.Content += "\nupstream_detail=" + failure.Detail
			}
		}
		encoded, encodeErr := json.Marshal(result.Items)
		if encodeErr != nil {
			observation.LogRecord.Content += fmt.Sprintf("\npersistence_error=%v", encodeErr)
			observation.LogRecord.ResultStatus, observation.LogRecord.Level = globalevents.ResultFailed, slog.LevelError
			err = errors.Join(err, encodeErr)
			return
		}
		failure := ""
		if err != nil {
			failure = err.Error()
		}
		if saveErr := s.route.Repository.FinishExchange(context.WithoutCancel(s.ctx), exchange.ID, status, raw, string(encoded), failure); saveErr != nil {
			observation.LogRecord.Content += fmt.Sprintf("\npersistence_error=%v", saveErr)
			observation.LogRecord.ResultStatus, observation.LogRecord.Level = globalevents.ResultFailed, slog.LevelError
			err = errors.Join(err, fmt.Errorf("save native response: %w", saveErr))
		} else {
			result.FactsSaved = true
		}
	}()
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	observation.LogRecord.EventName = "llm_usage"
	events, err := client.StreamPrepared(callCtx, prepared)
	if err != nil {
		observation.Event.ProviderError = true
		if errors.Is(err, context.Canceled) {
			observation.LogRecord.ResultStatus, observation.LogRecord.Level = globalevents.ResultCanceled, slog.LevelInfo
		}
		if ctx.Err() != nil {
			status = "canceled"
			return result, errors.Join(err, ctx.Err())
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
			observation.Event.ProviderError = true
			if errors.Is(event.Error, context.Canceled) {
				observation.LogRecord.ResultStatus, observation.LogRecord.Level = globalevents.ResultCanceled, slog.LevelInfo
			}
			if event.Type != "" {
				status = event.Type
			}
			if ctx.Err() != nil {
				status = "canceled"
				return result, errors.Join(event.Error, ctx.Err())
			}
			return result, event.Error
		}
		switch event.Type {
		case "response.output_text.delta", "response.refusal.delta":
			if stream != nil && event.Delta != "" {
				if err := stream.Append(ctx, event.Delta); err != nil {
					observation.LogRecord.Content += fmt.Sprintf("\nstream_error=%v", err)
					if errors.Is(err, context.Canceled) {
						observation.LogRecord.ResultStatus, observation.LogRecord.Level = globalevents.ResultCanceled, slog.LevelInfo
					}
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
		if errors.Is(err, context.Canceled) {
			observation.LogRecord.ResultStatus, observation.LogRecord.Level = globalevents.ResultCanceled, slog.LevelInfo
		}
		status = "canceled"
		return result, err
	}
	if status != "completed" || result.Response == nil || result.Response.Status != "completed" || result.Response.ID == "" {
		return result, fmt.Errorf("responses stream: %w", io.ErrUnexpectedEOF)
	}
	observation.LogRecord.ResultStatus, observation.LogRecord.Level = globalevents.ResultSucceeded, slog.LevelInfo
	return result, nil
}
