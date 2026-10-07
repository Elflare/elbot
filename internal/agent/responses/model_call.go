package responses

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"elbot/internal/agent/dialogue"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/storage"
)

func (s *turnState) call(ctx context.Context, pending *dialogue.PendingUserMessage, stream delivery.MessageStream) (final dialogue.CallOutput, err error) {
	client, ok := s.selection.Client.(api.Streamer)
	if !ok {
		return final, fmt.Errorf("provider %q does not provide Responses streaming", s.selection.Provider)
	}
	ctx = hook.WithReadOnlyCalls(ctx)
	observation := s.route.Calls.Begin(s.session.ID, s.selection)
	defer func() { observation.Publish(ctx, err) }()
	projection, err := s.route.Calls.PrepareProjection(ctx, s.session, s.selection, s.projection, s.tools, pending, inputCommitter{state: s})
	if err != nil {
		return final, err
	}
	s.projection, s.tools = projection.Messages, projection.Tools
	inputs, err := s.route.Repository.PendingInputs(ctx, s.session.ID)
	if err != nil {
		return final, err
	}
	inputs, err = s.queueForegroundNotice(ctx, inputs)
	if err != nil {
		return final, err
	}
	inputs, err = s.queueToolDefinitions(ctx, inputs)
	if err != nil {
		return final, err
	}
	sort.SliceStable(inputs, func(i, j int) bool { return inputOrder(inputs[i]) < inputOrder(inputs[j]) })
	items, cleanup, err := s.resolveInputs(ctx, inputs)
	if err != nil {
		return final, err
	}
	defer cleanup()
	var consumed []string
	for _, input := range inputs {
		consumed = append(consumed, input.ID)
	}
	instructions, err := s.instructions(ctx)
	if err != nil {
		return final, err
	}
	store, err := client.StoreForModel(s.selection.Model)
	if err != nil {
		return final, err
	}
	request := api.Request{Model: s.selection.Model, Instructions: instructions, Input: items, AllowedTools: s.allowedToolNames(), Store: &store, Include: []string{"reasoning.encrypted_content"}}
	origin, err := s.route.View.Providers.OriginFor(s.selection.Provider)
	if err != nil {
		return final, err
	}
	baseURL := origin.BaseURL
	if s.checkpoint != nil {
		request.PreviousResponseID = s.checkpoint.ResponseID
		exchange, err := s.route.Repository.GetExchange(ctx, s.checkpoint.ExchangeID)
		if err != nil {
			return final, err
		}
		baseURL = exchange.BaseURL
		state, err := exchangeStorage(exchange)
		if err != nil {
			return final, err
		}
		if !state.Available {
			if store && state.Requested {
				_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
					Category: globalevents.LogRuntime,
					Level:    slog.LevelWarn,
					Name:     "responses_switching_to_stateless_replay",
					Module:   "agent",
					Summary:  "Responses switching to stateless replay",
					Fields: []slog.Attr{
						slog.Any("provider", s.selection.Provider),
						slog.Any("model", s.selection.Model),
						slog.Any("session_id", s.session.ID),
						slog.Any("response_id", s.checkpoint.ResponseID),
						slog.Any("reason", "upstream returned store:false"),
					},
				})
			}
			store = false
		}
	} else if s.seed != nil {
		request.PreviousResponseID, baseURL = s.seed.ResponseID, s.seed.BaseURL
		if store && request.PreviousResponseID != "" && baseURL == origin.BaseURL {
			prefix, release, err := s.route.Context.resolveContinuation(ctx, s.seed)
			if err != nil {
				return final, err
			}
			defer release()
			request.Input = append(prefix, items...)
		}
	}
	replay := !store && (s.checkpoint != nil || s.seed != nil) || request.PreviousResponseID == "" && s.seed != nil || request.PreviousResponseID != "" && baseURL != origin.BaseURL
	if replay {
		var release func()
		request, release, err = s.replayRequest(ctx, request, items, origin)
		if err != nil {
			return final, err
		}
		defer release()
	}
	started := time.Now()
	result, err := s.nativeCall(ctx, client, request, consumed, stream)
	if err != nil && request.PreviousResponseID != "" && !result.OutputStarted && result.FactsSaved && api.PreviousResponseUnavailable(err) {
		var release func()
		request, release, err = s.replayRequest(ctx, request, items, origin)
		if err != nil {
			return final, err
		}
		defer release()
		result, err = s.nativeCall(ctx, client, request, consumed, stream)
	}
	if err != nil {
		observation.Event.ProviderError = true
		if ctx.Err() == nil {
			s.route.Calls.NotifyError(ctx, hook.Event{Point: hook.PointLLMResponseReceived, Session: hook.SessionContext{ID: s.session.ID}, LLM: hook.LLMPayload{Provider: s.selection.Provider, Model: s.selection.Model}}, err)
		}
		return final, err
	}
	var calls []llm.ToolCallRequest
	seen := map[string]bool{}
	for _, item := range result.Items {
		if item.Type != "function_call" {
			continue
		}
		if item.CallID == "" || item.Name == "" || seen[item.CallID] {
			return final, fmt.Errorf("invalid or duplicate Responses function call")
		}
		if !slices.Contains(result.AllowedTools, item.Name) {
			return final, fmt.Errorf("Responses returned unavailable function %q", item.Name)
		}
		seen[item.CallID] = true
		calls = append(calls, llm.ToolCallRequest{ID: item.CallID, Name: item.Name, Arguments: item.Arguments})
	}
	if s.session.Mode == storage.SessionModeChat && len(calls) > 0 {
		return final, fmt.Errorf("unexpected function call with tools disabled")
	}
	text := outputText(result.Items)
	s.sourceText = text
	elapsed := time.Since(started).Milliseconds()
	observation.Event.ElapsedMS = elapsed
	final, err = s.route.Calls.CompleteCall(ctx, s.session, s.selection, text, result.Response.TokenUsage(), calls, elapsed)
	if err != nil {
		return final, err
	}
	observation.Event.OutputReady = true
	observation.Event.Text, observation.Event.SourceText, observation.Event.ToolCallCount, observation.Event.Usage = final.Text, text, len(calls), final.Usage
	s.exchange, s.response, s.consumed, s.calls = result.Exchange, result.Response, consumed, calls
	return final, nil
}

func (s *turnState) replayRequest(ctx context.Context, request api.Request, inputs []api.Item, target llm.Origin) (api.Request, func(), error) {
	if s.route.Context == nil {
		return request, func() {}, fmt.Errorf("Responses 原生材料能力未配置")
	}
	w, err := s.route.Context.Load(ctx, s.session, s.checkpoint)
	if err != nil {
		return request, func() {}, err
	}
	if err := checkWindowTarget(w, target); err != nil {
		return request, func() {}, err
	}
	prefix, release, err := s.route.Context.Resolve(ctx, w)
	if err != nil {
		return request, release, err
	}
	request.PreviousResponseID = ""
	request.Input = append(prefix, inputs...)
	if err := validateCallLinks(request.Input, false); err != nil {
		release()
		return request, func() {}, err
	}
	return request, release, nil
}

func outputItems(response *api.Response, completed map[int]api.Item) []api.Item {
	items := make(map[int]api.Item, len(completed))
	if response != nil {
		for index, item := range response.Output {
			items[index] = item
		}
	}
	for index, item := range completed {
		items[index] = item
	}
	indices := make([]int, 0, len(items))
	for index := range items {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	result := make([]api.Item, 0, len(indices))
	for _, index := range indices {
		result = append(result, items[index])
	}
	return result
}

func outputText(items []api.Item) string {
	var text, refusal strings.Builder
	for _, item := range items {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			switch part.Type {
			case "output_text":
				text.WriteString(part.Text)
			case "refusal":
				refusal.WriteString(part.Refusal)
			}
		}
	}
	if text.Len() > 0 {
		return text.String()
	}
	return refusal.String()
}
