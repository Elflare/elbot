package responses

import (
	"context"
	agentshare "elbot/internal/agent/share"

	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/storage"
)

type nativeCallResult struct {
	Exchange         *storage.NativeExchange
	Response         *api.Response
	Items            []api.Item
	ReasoningContent string
	OutputStarted    bool
	FactsSaved       bool
	AllowedTools     []string
}

// inputReference identifies one ordered input in an immutable record. Synthetic
// items (branch closure/compaction trigger) are frozen inline; never media data.
type inputReference struct {
	Kind  string          `json:"kind"`
	ID    string          `json:"id,omitempty"`
	Index int             `json:"index,omitempty"`
	Item  json.RawMessage `json:"item,omitempty"`
}

func requestAudit(parameters json.RawMessage, refs []inputReference) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(parameters, &fields); err != nil {
		return "", err
	}
	if refs == nil {
		refs = []inputReference{}
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		return "", err
	}
	fields["input_refs"] = raw
	encoded, err := json.Marshal(fields)
	return string(encoded), err
}

// A part keeps opaque provider fields intact and buffers only the active text field.
type streamPart struct {
	fields map[string]json.RawMessage
	key    string
	text   strings.Builder
}

func (p *streamPart) flush() {
	if p.key != "" {
		p.fields[p.key], _ = json.Marshal(p.text.String())
		p.key = ""
		p.text.Reset()
	}
}

type streamItem struct {
	snapshot *api.Item // Last complete item event; a new snapshot replaces all buffered parts.
	header   map[string]json.RawMessage
	parts    map[int]*streamPart
}

func (m *streamItem) update(event api.Event) error {
	header := m.header
	if header == nil {
		if m.snapshot != nil {
			if err := json.Unmarshal(m.snapshot.Raw, &header); err != nil {
				return fmt.Errorf("responses stream item: %w", err)
			}
		} else {
			header = map[string]json.RawMessage{"type": json.RawMessage(`"message"`), "role": json.RawMessage(`"assistant"`)}
			if event.ItemID != "" {
				header["id"], _ = json.Marshal(event.ItemID)
			}
		}
		m.parts = map[int]*streamPart{}
		var existing []map[string]json.RawMessage
		if raw := header["content"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &existing); err != nil {
				return fmt.Errorf("responses stream content: %w", err)
			}
		}
		for i, fields := range existing {
			m.parts[i] = &streamPart{fields: fields}
		}
	}
	p := m.parts[event.ContentIndex]
	if p == nil {
		p = &streamPart{fields: map[string]json.RawMessage{}}
	}
	switch event.Type {
	case "response.content_part.added", "response.content_part.done":
		p.flush()
		fields := maps.Clone(p.fields)
		var data struct {
			Part json.RawMessage `json:"part"`
		}
		if err := json.Unmarshal(event.Raw, &data); err != nil {
			return fmt.Errorf("responses stream part event: %w", err)
		}
		if err := json.Unmarshal(data.Part, &fields); err != nil {
			return fmt.Errorf("responses stream part event: %w", err)
		}
		raw, _ := json.Marshal(fields)
		var parsed api.OutputContent
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return fmt.Errorf("responses stream partial item: %w", err)
		}
		p.fields = fields
	default:
		key, typ, done := "text", `"output_text"`, event.Text
		if event.Type == "response.refusal.delta" || event.Type == "response.refusal.done" {
			key, typ, done = "refusal", `"refusal"`, event.Refusal
		}
		if p.key != key {
			p.flush()
			var prefix string
			if raw := p.fields[key]; len(raw) > 0 {
				if err := json.Unmarshal(raw, &prefix); err != nil {
					return fmt.Errorf("responses stream %s: %w", key, err)
				}
			}
			if p.fields == nil {
				p.fields = map[string]json.RawMessage{}
			}
			p.key = key
			p.fields["type"] = json.RawMessage(typ)
			p.text.WriteString(prefix)
		}
		if strings.HasSuffix(event.Type, ".done") {
			p.text.Reset()
			p.text.WriteString(done)
		} else {
			p.text.WriteString(event.Delta)
		}
	}
	m.header, m.parts[event.ContentIndex] = header, p
	return nil
}

func (m *streamItem) item() (api.Item, error) {
	parts := make([]map[string]json.RawMessage, 0, len(m.parts))
	for _, i := range slices.Sorted(maps.Keys(m.parts)) {
		p := m.parts[i]
		p.flush()
		parts = append(parts, p.fields)
	}
	m.header["content"], _ = json.Marshal(parts)
	raw, _ := json.Marshal(m.header)
	return api.ParseItem(raw)
}

// Each actual HTTP attempt gets its own immutable request and terminal facts.
// Local hooks/inputs are prepared by the caller once, outside recovery retries.
func (s *turnState) nativeCall(ctx context.Context, client api.Streamer, request api.Request, consumed []string, stream delivery.MessageStream, observation agentshare.CallObservation) (result nativeCallResult, err error) {
	prepared, err := client.PrepareRequest(request)
	if err != nil {
		return result, err
	}
	result.AllowedTools = prepared.AllowedTools()
	audit, err := requestAudit(prepared.ParametersJSON(), s.requestReferences)
	if err != nil {
		return result, err
	}
	origin := s.Selection.Origin
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	ids, _ := json.Marshal(consumed)
	exchange := &storage.NativeExchange{
		SessionID:            s.Session.ID,
		APIType:              string(origin.APIType),
		Provider:             origin.Provider,
		BaseURL:              origin.BaseURL,
		Model:                s.Selection.Model,
		RequestID:            facts.RequestID,
		RunID:                facts.RunID,
		Attempt:              facts.Attempt,
		PreviousCheckpointID: s.checkpointID(),
		RequestJSON:          audit,
		InputIDsJSON:         string(ids),
	}
	locked, release, err := s.route.Gate.Enter(ctx, s.Session.ID)
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
	// Keep only observed output indices; complete item events replace their buffered deltas.
	observed := map[int]*streamItem{}
	result, err = func() (nativeCallResult, error) {
		callCtx, cancel := context.WithCancel(ctx)
		observation.LogRecord().EventName = "llm_usage"
		observation.LogRecord().ResultStatus = globalevents.ResultCanceled
		events, err := client.StreamPrepared(callCtx, prepared)
		defer func() {
			cancel()
			if events != nil {
				for range events {
				}
			}
		}()
		if err != nil {
			if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
				observation.LogRecord().ResultStatus = globalevents.ResultCanceled
				status = "canceled"
				return result, ctx.Err()
			}
			observation.Event().ProviderError, observation.Event().Err = true, err
			observation.LogRecord().ResultStatus = globalevents.ResultFailed
			return result, err
		}
		failStream := func(cause error) (nativeCallResult, error) {
			failure := llm.MapStreamError(cause)
			observation.Event().ProviderError, observation.Event().Err = true, failure
			observation.LogRecord().ResultStatus = globalevents.ResultFailed
			return result, failure
		}
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
			if (event.Type == "response.output_item.added" || event.Type == "response.output_item.done") && event.Item != nil {
				observed[event.OutputIndex] = &streamItem{snapshot: event.Item}
			}
			if event.Error != nil {
				if event.Type != "" {
					status = event.Type
				}
				if ctx.Err() != nil && errors.Is(event.Error, ctx.Err()) {
					observation.LogRecord().ResultStatus = globalevents.ResultCanceled
					status = "canceled"
					return result, ctx.Err()
				}
				observation.Event().ProviderError, observation.Event().Err = true, event.Error
				observation.LogRecord().ResultStatus = globalevents.ResultFailed
				return result, event.Error
			}
			switch event.Type {
			case "response.content_part.added", "response.content_part.done", "response.output_text.done", "response.refusal.done", "response.output_text.delta", "response.refusal.delta":
				if event.OutputIndex < 0 || event.ContentIndex < 0 {
					return failStream(fmt.Errorf("responses stream: negative output or content index"))
				}
				m := observed[event.OutputIndex]
				if m == nil {
					m = &streamItem{}
					observed[event.OutputIndex] = m
				}
				if err := m.update(event); err != nil {
					return failStream(err)
				}
				result.OutputStarted = true
			}
			switch event.Type {
			case "response.output_text.delta", "response.refusal.delta":
				if event.Delta != "" {
					if _, err := stream.Send(ctx, delivery.StreamEvent{Type: delivery.StreamTextDelta, Text: event.Delta}); err != nil {
						observation.Event().Err = err
						observation.LogRecord().ResultStatus = globalevents.LogStatus(err)
						return result, err
					}
				}
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				if event.Delta != "" {
					result.ReasoningContent += event.Delta
					_, _ = stream.Send(ctx, delivery.StreamEvent{Type: delivery.StreamReasoningDelta, Text: event.Delta})
				}
			case "response.reasoning_summary_text.done", "response.reasoning_text.done":
				_, _ = stream.Send(ctx, delivery.StreamEvent{Type: delivery.StreamReasoningDone})
			case "response.completed":
				status = "completed"
				_, err = stream.Send(ctx, delivery.StreamEvent{Type: delivery.StreamFinish})
				observation.LogRecord().ResultStatus = globalevents.ResultSucceeded
				if err != nil {
					observation.Event().Err = err
					observation.LogRecord().ResultStatus = globalevents.LogStatus(err)
				}
				return result, err
			}
		}
		if err := ctx.Err(); err != nil {
			observation.LogRecord().ResultStatus = globalevents.ResultCanceled
			status = "canceled"
			return result, err
		}
		return failStream(fmt.Errorf("response stream closed before response.completed: %w", io.ErrUnexpectedEOF))
	}()
	completed := make(map[int]api.Item, len(observed))
	for index, m := range observed {
		if m.snapshot != nil {
			completed[index] = *m.snapshot
		}
		if m.header == nil {
			continue
		}
		item, buildErr := m.item()
		if buildErr != nil {
			if err == nil {
				err = llm.MapStreamError(fmt.Errorf("responses stream partial item: %w", buildErr))
				status = "failed"
				observation.Event().ProviderError, observation.Event().Err = true, err
				observation.LogRecord().ResultStatus = globalevents.ResultFailed
			}
			continue
		}
		completed[index] = item
	}
	result.Items = outputItems(result.Response, completed)
	observation.Event().Text, observation.Event().SourceText = outputText(result.Items), outputText(result.Items)
	observation.Event().OutputReady = len(result.Items) > 0
	if result.Response != nil {
		observation.Event().Usage = result.Response.TokenUsage()
	}
	observation.Event().ToolCallCount = 0
	for _, item := range result.Items {
		if item.Type == "function_call" {
			observation.Event().ToolCallCount++
		}
	}
	raw := ""
	if result.Response != nil {
		raw = string(result.Response.Raw)
	}
	archiveErr := func() error {
		encoded, encodeErr := json.Marshal(result.Items)
		if encodeErr != nil {
			return fmt.Errorf("encode native response: %w", encodeErr)
		}
		failure := ""
		if err != nil {
			failure = err.Error()
		}
		saveErr := s.route.Repository.FinishExchange(context.WithoutCancel(s.Ctx), exchange.ID, status, raw, string(encoded), failure)
		if saveErr != nil {
			return fmt.Errorf("save native response: %w", saveErr)
		} else {
			exchange.Status, exchange.ResponseJSON, exchange.ItemsJSON, exchange.Error = status, raw, string(encoded), failure
			result.FactsSaved = true
		}

		return nil
	}()
	if archiveErr != nil {
		if err == nil {
			return result, archiveErr
		}
		globalevents.AsyncLog(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryLog, Level: slog.LevelError, EventName: "native_archive_failed", Module: "agent", Content: archiveErr.Error(), Identity: globalevents.LogIdentityFromContext(ctx), ResultStatus: globalevents.ResultFailed})
	}
	return result, err
}
