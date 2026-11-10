package responses

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/media"
	"elbot/internal/storage"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
)

func (s *turnState) call(ctx context.Context, pending *agentshare.PendingUserMessage, stream delivery.MessageStream) (final agentshare.CallOutput, err error) {
	client := s.selection.Client.(api.Streamer)
	ctx = agentshare.WithReadOnlyCalls(ctx)
	var request api.Request
	var inputs []storage.NativeInput
	var consumed []string
	var usageID media.UsageID
	var result nativeCallResult
	var calls []llm.ToolCallRequest
	origin := s.selection.Origin
	defer func() { s.route.Preparer.FinishMaterials(ctx, usageID) }()
	prepare := func(ctx context.Context, projection agentshare.CallInput) error {
		s.projection, s.tools = projection.Messages, projection.Tools
		var err error
		inputs, err = s.route.Repository.PendingInputs(ctx, s.session.ID)
		if err != nil {
			return err
		}
		inputs, err = s.queueForegroundNotice(ctx, inputs)
		if err != nil {
			return err
		}
		inputs, err = s.queueToolDefinitions(ctx, inputs)
		if err != nil {
			return err
		}
		sort.SliceStable(inputs, func(i, j int) bool { return inputOrder(inputs[i]) < inputOrder(inputs[j]) })
		w := &nativeWindow{}
		s.requestReferences = nil
		for _, input := range inputs {
			consumed = append(consumed, input.ID)
			s.requestReferences = append(s.requestReferences, inputReference{Kind: "input", ID: input.ID})
		}
		instructions, err := s.instructions(ctx)
		if err != nil {
			return err
		}
		store, err := client.StoreForModel(s.selection.Model)
		if err != nil {
			return err
		}
		request = api.Request{
			Model:        s.selection.Model,
			Instructions: instructions,
			AllowedTools: s.allowedToolNames(),
			Store:        &store,
			Include:      []string{"reasoning.encrypted_content"},
		}
		baseURL := origin.BaseURL
		if s.checkpoint != nil {
			request.PreviousResponseID = s.checkpoint.ResponseID
			exchange := s.exchange
			if s.window != nil {
				exchange = s.window.exchange
			}
			baseURL = exchange.BaseURL
			state, err := exchangeStorage(exchange)
			if err != nil {
				return err
			}
			if !state.Available {
				if store && state.Requested {
					record := globalevents.LogRecord{
						Category:     globalevents.LogCategoryLog,
						Level:        slog.LevelWarn,
						EventName:    "responses_switching_to_stateless_replay",
						Module:       "agent",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("Responses switching to stateless replay\nprovider=%v\nmodel=%v\nresponse_id=%v\nreason=%v", s.selection.Provider, s.selection.Model, s.checkpoint.ResponseID, "upstream returned store:false"),
						ContentLimit: 4096,
					}
					record.Identity = globalevents.LogIdentityFromContext(ctx)
					record.Identity.SessionID = s.session.ID
					globalevents.Log(ctx, record)
				}
				store = false
			}
		} else if s.seed != nil {
			request.PreviousResponseID, baseURL = s.seed.ResponseID, s.seed.BaseURL
			if store && request.PreviousResponseID != "" && baseURL == origin.BaseURL {
				w, err = s.route.Context.resolveContinuation(s.seed)
				if err != nil {
					return err
				}
				var refs []inputReference
				for i := range w.Items {
					refs = append(refs, inputReference{Kind: "continuation", ID: s.seed.ID, Index: i})
				}
				s.requestReferences = append(refs, s.requestReferences...)
			}
		}
		replay := !store && (s.checkpoint != nil || s.seed != nil) || request.PreviousResponseID == "" && s.seed != nil || request.PreviousResponseID != "" && baseURL != origin.BaseURL
		if replay {
			request, usageID, err = s.replayRequest(ctx, request, inputs, consumed, origin)
		} else {
			request.Input, usageID, err = s.resolveInputs(ctx, inputs, w)
		}
		return err
	}
	attempt := func(ctx context.Context, observation agentshare.CallObservation) (agentshare.CallOutput, error) {
		calls = nil
		var callErr error
		result, callErr = s.nativeCall(ctx, client, request, consumed, stream, observation)
		final := agentshare.CallOutput{Text: outputText(result.Items), SourceText: outputText(result.Items), ReasoningContent: result.ReasoningContent}
		if result.Response != nil {
			final.Usage = result.Response.TokenUsage()
		}
		if callErr != nil {
			// Partial output is retained as a fact, never as executable tool calls.
			var failure *llm.CallError
			if request.PreviousResponseID == "" || result.OutputStarted || !result.FactsSaved || !errors.As(callErr, &failure) || failure.Kind != llm.ErrorPreviousResponseUnavailable {
				return final, callErr
			}
			s.route.Preparer.FinishMaterials(ctx, usageID)
			var err error
			request, usageID, err = s.replayRequest(ctx, request, inputs, consumed, origin)
			if err != nil {
				return final, err
			}
			// replayRequest clears PreviousResponseID; recovery can happen only once.
			return final, &llm.CallError{Kind: failure.Kind, Retryable: true, Cause: failure.Cause}
		}
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
		final.ToolCalls = calls
		return final, nil
	}
	final, err = s.route.Calls.Call(ctx, s.session, s.selection, s.projection, s.tools, pending, inputCommitter{state: s}, stream, prepare, attempt)
	s.sourceText = outputText(result.Items)
	if err != nil {
		return final, err
	}
	s.exchange, s.response, s.consumed, s.calls = result.Exchange, result.Response, consumed, calls
	return final, nil
}

func (s *turnState) replayRequest(ctx context.Context, request api.Request, inputs []storage.NativeInput, consumed []string, target llm.Origin) (api.Request, media.UsageID, error) {
	if s.window == nil {
		records, err := s.route.Repository.LoadNativeWindow(ctx, s.session, s.checkpoint)
		if err != nil {
			return request, "", err
		}
		s.window, err = s.route.Context.Load(ctx, s.session, s.checkpoint, records)
		if err != nil {
			return request, "", err
		}
	}
	// resolveInputs appends pending inputs; preserve the cached committed prefix.
	w := *s.window
	w.Items = append([]api.Item(nil), w.Items...)
	w.Materials = append([]material(nil), w.Materials...)
	if err := checkWindowTarget(&w, target); err != nil {
		return request, "", err
	}
	request.PreviousResponseID = ""
	// A retry replaces the actual-input manifest while the consumed-input list
	// remains the same. Replayed prefixes never become newly consumed inputs.
	refs := append([]inputReference(nil), w.References...)
	for _, id := range consumed {
		refs = append(refs, inputReference{Kind: "input", ID: id})
	}
	s.requestReferences = refs
	var usageID media.UsageID
	var err error
	request.Input, usageID, err = s.resolveInputs(ctx, inputs, &w)
	if err != nil {
		return request, usageID, err
	}
	if err := validateCallLinks(request.Input, false); err != nil {
		return request, usageID, err
	}
	return request, usageID, nil
}

func outputItems(response *api.Response, completed map[int]api.Item) []api.Item {
	items := make(map[int]api.Item, len(completed))
	if response != nil {
		for index, item := range response.Output {
			items[index] = item
		}
	}
	for index, item := range completed {
		if response != nil && response.Status == "completed" && index >= 0 && index < len(response.Output) {
			// Terminal fields replace partial fields, while item.done metadata
			// omitted from the terminal response remains an observed native fact.
			var content, data map[string]json.RawMessage
			if json.Unmarshal(item.Raw, &content) == nil && content != nil && json.Unmarshal(response.Output[index].Raw, &data) == nil && data != nil {
				for key, value := range data {
					content[key] = value
				}
				raw, err := json.Marshal(content)
				if err == nil {
					if parsed, err := api.ParseItem(raw); err == nil {
						item = parsed
					}
				}
			}
		}
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
	var text strings.Builder
	for _, item := range items {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			switch part.Type {
			case "output_text":
				text.WriteString(part.Text)
			case "refusal":
				text.WriteString(part.Refusal)
			}
		}
	}
	return text.String()
}
