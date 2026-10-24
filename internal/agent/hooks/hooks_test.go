package hooks

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/platform"
)

func TestSingleProviderDetachAndReplace(t *testing.T) {
	h := NewHookBridge(HookBridgeDeps{})
	defer h.Close(context.Background())
	if got, result, err := h.AgentOutputPrepared(t.Context(), "original"); err != nil || got != "original" || result.ResultStatus != events.ResultSkipped {
		t.Fatalf("unregistered=%q %+v %v", got, result, err)
	}
	points := Points{AgentOutputPrepared: func(_ context.Context, _ Facts, text string) (string, events.LogResult, error) {
		return text + "/first", events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}}
	detach, err := h.Register(points)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Register(Points{}); err == nil {
		t.Fatal("second provider accepted")
	}
	points.AgentOutputPrepared = nil // Registration owns a copy of the callback table.
	if got, _, err := h.AgentOutputPrepared(t.Context(), "original"); err != nil || got != "original/first" {
		t.Fatal(got, err)
	}
	detach()
	detach()
	if _, err := h.Register(Points{AgentOutputPrepared: func(_ context.Context, _ Facts, text string) (string, events.LogResult, error) {
		return text + "/second", events.LogResult{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	detach() // An old handle cannot detach a replacement.
	if got, _, err := h.AgentOutputPrepared(t.Context(), "original"); err != nil || got != "original/second" {
		t.Fatal(got, err)
	}
}

func TestPartialProviderPreservesUnregisteredValues(t *testing.T) {
	h := NewHookBridge(HookBridgeDeps{})
	defer h.Close(context.Background())
	if _, err := h.Register(Points{}); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	segments := llm.TextSegments("original")
	message := Message{Segments: segments}
	session := Session{ID: "session"}
	if got, _, err := h.PlatformMessageReceived(ctx, message); err != nil || got.Consume || len(got.Outputs) != 0 || !reflect.DeepEqual(got.Segments, segments) {
		t.Fatal(got, err)
	}
	if got, _, err := h.AgentInputPrepared(ctx, session, message); err != nil || !reflect.DeepEqual(got, segments) {
		t.Fatal(got, err)
	}
	input := LLMInput{Message: message, Tools: []llm.ToolSchema{{Name: "tool"}}}
	for _, run := range []func(context.Context, Session, LLMInput) (LLMInputResult, events.LogResult, error){h.LLMTurnPrepared, h.LLMRequestPrepared} {
		got, _, err := run(ctx, session, input)
		if err != nil || !reflect.DeepEqual(got.Segments, segments) || !reflect.DeepEqual(got.Tools, input.Tools) {
			t.Fatal(got, err)
		}
	}
	response := LLMResponse{Text: "display", SourceText: "raw", ToolCalls: []llm.ToolCallRequest{{ID: "call", Name: "tool", Arguments: "{}"}}}
	got, _, err := h.LLMResponseReceived(ctx, session, response)
	if err != nil || got.Text != "display" || got.SourceText != "raw" || !reflect.DeepEqual(got.ToolCalls, response.ToolCalls) {
		t.Fatal(got, err)
	}
	if args, _, err := h.ToolCallPrepared(ctx, session, response.ToolCalls[0]); err != nil || args != "{}" {
		t.Fatal(args, err)
	}
	if got, _, err := h.ToolCallCompleted(ctx, session, ToolResult{Segments: segments}); err != nil || !reflect.DeepEqual(got, segments) {
		t.Fatal(got, err)
	}
	if got, _, err := h.AgentTurnOutputPrepared(ctx, "display"); err != nil || got != "display" {
		t.Fatal(got, err)
	}
	if h.HasRoute(ctx, message) || h.CancelRoute(ctx, message) {
		t.Fatal("absent route handled input")
	}
	h.PlatformMessageSent(ctx, "display")
	h.ErrorOccurred(ctx, session, response, errors.New("source error"))
}

func TestCallbackFactsPreserveWakeupAndCallPolicy(t *testing.T) {
	h := NewHookBridge(HookBridgeDeps{})
	defer h.Close(context.Background())
	var seen []Facts
	_, err := h.Register(Points{ToolCallPrepared: func(_ context.Context, facts Facts, call llm.ToolCallRequest) (string, events.LogResult, error) {
		seen = append(seen, facts)
		return call.Arguments, events.LogResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	session := Session{ID: "s", Mode: "work"}
	for _, ctx := range []context.Context{t.Context(), WithReadOnlyCalls(WithWakeup(t.Context(), false))} {
		if _, _, err := h.ToolCallPrepared(ctx, session, llm.ToolCallRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || !seen[0].Woken || seen[0].ReadOnlyCalls || seen[1].Woken || !seen[1].ReadOnlyCalls || seen[1].Session != session {
		t.Fatalf("facts=%+v", seen)
	}
}

func TestMessageFactsKeepOriginalTextAndPreparedIntent(t *testing.T) {
	h := NewHookBridge(HookBridgeDeps{})
	defer h.Close(context.Background())
	ctx := platform.WithMessageContext(t.Context(), platform.MessageContext{RawText: "wake original", TriggerKeywords: []string{"wake"}})
	var seen []Message
	_, err := h.Register(Points{PlatformMessageReceived: func(_ context.Context, _ Facts, message Message) (PlatformMessageReceivedResult, events.LogResult, error) {
		seen = append(seen, message)
		return PlatformMessageReceivedResult{Segments: message.Segments}, events.LogResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range []string{"", "explicit original"} {
		_, _, err := h.PlatformMessageReceived(ctx, Message{Role: string(llm.RoleUser), PlatformText: original, Segments: llm.TextSegments("wake edited")})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || seen[0].PlatformText != "wake original" || seen[1].PlatformText != "explicit original" || seen[0].IntentText != "edited" || seen[1].IntentText != "edited" {
		t.Fatalf("message facts=%+v", seen)
	}
}

func TestDetachKeepsInFlightCallbackAndCloseWaitsForExit(t *testing.T) {
	h := NewHookBridge(HookBridgeDeps{})
	entered, release := make(chan struct{}), make(chan struct{})
	exited := make(chan error, 1)
	detach, err := h.Register(Points{AgentOutputPrepared: func(_ context.Context, _ Facts, text string) (string, events.LogResult, error) {
		close(entered)
		<-release
		return text, events.LogResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _, err := h.AgentOutputPrepared(t.Context(), "in flight"); exited <- err }()
	<-entered
	detach()
	if _, _, err := h.AgentOutputPrepared(t.Context(), "after detach"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := h.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close=%v", err)
	}
	select {
	case <-h.Done():
		t.Fatal("completed before callback exit")
	default:
	}
	if _, err := h.Register(Points{}); err == nil {
		t.Fatal("closed bridge accepted registration")
	}
	close(release)
	if err := <-exited; err != nil {
		t.Fatal(err)
	}
	if err := h.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.Done():
	default:
		t.Fatal("callback exited without completing Done")
	}
}
