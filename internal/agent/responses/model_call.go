package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"elbot/internal/agent/dialogue"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
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
	// Close outstanding function outputs before appending the next user input.
	// Relative order within each group remains the order of local submission.
	sort.SliceStable(inputs, func(i, j int) bool { return inputs[i].CallID != "" && inputs[j].CallID == "" })
	messages := make([]llm.LLMMessage, 0, len(inputs))
	for _, input := range inputs {
		segments, err := inputSegments(input)
		if err != nil {
			return final, err
		}
		messages = append(messages, llm.LLMMessage{Role: llm.RoleUser, Segments: segments})
	}
	resolved := messages
	cleanup := func() {}
	if s.route.Calls.Media != nil {
		resolved, cleanup, err = s.route.Calls.Media.AcquireForLLM(ctx, messages)
		if err != nil {
			return final, err
		}
	}
	defer cleanup()
	var items []api.Item
	var consumed []string
	for i, input := range inputs {
		item, err := nativeItem(input.CallID, resolved[i].Segments)
		if err != nil {
			return final, err
		}
		items = append(items, item)
		consumed = append(consumed, input.ID)
	}
	instructions, err := s.instructions(ctx)
	if err != nil {
		return final, err
	}
	store := true
	request := api.Request{Model: s.selection.Model, Instructions: instructions, Input: items, Tools: api.FunctionTools(s.tools), Store: &store}
	if s.checkpoint != nil {
		request.PreviousResponseID = s.checkpoint.ResponseID
	}
	prepared, err := client.PrepareRequest(request)
	if err != nil {
		return final, err
	}
	origin, err := s.route.View.Providers.OriginFor(s.selection.Provider)
	if err != nil {
		return final, err
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	exchange := &storage.NativeExchange{SessionID: s.session.ID, Protocol: string(origin.Protocol), Provider: origin.Provider, BaseURL: origin.BaseURL, Model: s.selection.Model, RequestID: facts.RequestID, RunID: facts.RunID, Attempt: facts.Attempt, PreviousCheckpointID: s.checkpointID(), RequestJSON: string(prepared.JSON())}
	locked, release, err := s.route.Messages.Gate.Enter(ctx, s.session.ID)
	if err != nil {
		return final, err
	}
	err = s.route.Repository.CreateExchange(locked, exchange)
	release()
	if err != nil {
		return final, fmt.Errorf("save native request: %w", err)
	}
	started := time.Now()
	status := "failed"
	var response *api.Response
	completedItems := map[int]api.Item{}
	defer func() {
		raw := ""
		if response != nil {
			raw = string(response.Raw)
		}
		done := outputItems(response, completedItems)
		encoded, encodeErr := json.Marshal(done)
		if encodeErr != nil {
			err = errors.Join(err, encodeErr)
			return
		}
		failure := ""
		if err != nil {
			failure = err.Error()
		}
		// Audit facts survive cancellation; they cannot advance the checkpoint.
		if saveErr := s.route.Repository.FinishExchange(context.WithoutCancel(s.ctx), exchange.ID, status, raw, string(encoded), failure); saveErr != nil {
			err = errors.Join(err, fmt.Errorf("save native response: %w", saveErr))
		}
		if err == nil {
			s.exchange, s.response, s.consumed = exchange, response, consumed
		}
	}()
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := client.StreamPrepared(callCtx, prepared)
	if err != nil {
		if ctx.Err() != nil {
			status = "canceled"
			return final, ctx.Err()
		}
		observation.Event.ProviderError = true
		return final, err
	}
	reasoningOpen := false
	defer func() {
		if reasoningOpen {
			s.output.SendReasoning(ctx, "[/thinking]\n\n")
		}
	}()
	for event := range events {
		if event.Response != nil {
			response = event.Response
		}
		if event.Type == "response.output_item.done" && event.Item != nil {
			completedItems[event.OutputIndex] = *event.Item
		}
		if event.Error != nil {
			status = event.Type
			if ctx.Err() != nil {
				status = "canceled"
				return final, ctx.Err()
			}
			observation.Event.ProviderError = true
			s.route.Calls.NotifyError(ctx, hook.Event{Point: hook.PointLLMResponseReceived, Session: hook.SessionContext{ID: s.session.ID}, LLM: hook.LLMPayload{Provider: s.selection.Provider, Model: s.selection.Model}}, event.Error)
			return final, event.Error
		}
		switch event.Type {
		case "response.output_text.delta", "response.refusal.delta":
			if stream != nil && event.Delta != "" {
				if err := stream.Append(ctx, event.Delta); err != nil {
					return final, err
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
		return final, err
	}
	if status != "completed" || response == nil || response.Status != "completed" || response.ID == "" {
		observation.Event.ProviderError = true
		return final, fmt.Errorf("responses stream: %w", io.ErrUnexpectedEOF)
	}
	var calls []llm.ToolCallRequest
	seen := map[string]bool{}
	items = outputItems(response, completedItems)
	for _, item := range items {
		if item.Type != "function_call" {
			continue
		}
		if item.CallID == "" || item.Name == "" || seen[item.CallID] {
			return final, fmt.Errorf("invalid or duplicate Responses function call")
		}
		seen[item.CallID] = true
		calls = append(calls, llm.ToolCallRequest{ID: item.CallID, Name: item.Name, Arguments: item.Arguments})
	}
	if s.session.Mode == storage.SessionModeChat && len(calls) > 0 {
		return final, fmt.Errorf("unexpected function call with tools disabled")
	}
	text := outputText(items)
	s.sourceText = text
	elapsed := time.Since(started).Milliseconds()
	observation.Event.ElapsedMS = elapsed
	final, err = s.route.Calls.CompleteCall(ctx, s.session, s.selection, text, response.TokenUsage(), calls, elapsed)
	if err != nil {
		return final, err
	}
	observation.Event.OutputReady = true
	observation.Event.Text, observation.Event.SourceText, observation.Event.ToolCallCount, observation.Event.Usage = final.Text, text, len(calls), final.Usage
	s.calls = calls
	return final, nil
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
