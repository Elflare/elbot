package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/llm"
)

func TestNoopManagerRunPreparesEvent(t *testing.T) {
	event, err := NoopManager{}.Run(context.Background(), Event{Point: PointAgentInputPrepared})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if event.Time.IsZero() {
		t.Fatal("expected Time to be populated")
	}
	if event.Metadata == nil {
		t.Fatal("expected Metadata to be populated")
	}
}

func TestBlockPolicyMatchesPlatformGroupAndUser(t *testing.T) {
	policy, err := NewBlockPolicy(
		[]string{"telegram"},
		[]string{"qqonebot:123", "telegram:-100456"},
		[]string{"qqonebot:42"},
	)
	if err != nil {
		t.Fatalf("NewBlockPolicy: %v", err)
	}
	tests := []struct {
		name  string
		event Event
		want  bool
	}{
		{name: "platform", event: Event{Platform: PlatformContext{Name: "telegram"}}, want: true},
		{name: "group", event: Event{Platform: PlatformContext{Name: "qqonebot", ScopeID: "group:123"}}, want: true},
		{name: "supergroup", event: Event{Platform: PlatformContext{Name: "telegram", ScopeID: "supergroup:-100456"}}, want: true},
		{name: "user", event: Event{Platform: PlatformContext{Name: "qqonebot"}, Actor: ActorContext{UserID: "42"}}, want: true},
		{name: "private scope is not group", event: Event{Platform: PlatformContext{Name: "qqonebot", ScopeID: "private:123"}}},
		{name: "other", event: Event{Platform: PlatformContext{Name: "qqonebot", ScopeID: "group:999"}, Actor: ActorContext{UserID: "7"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := policy.Blocks(tt.event); got != tt.want {
				t.Fatalf("Blocks() = %v, want %v", got, tt.want)
			}
		})
	}
	if _, err := NewBlockPolicy(nil, []string{"missing-platform"}, nil); err == nil {
		t.Fatal("expected malformed blocked_group to fail")
	}
}

func TestManagerSkipsBlockedPluginAndContinues(t *testing.T) {
	manager := NewManager()
	policy, err := NewBlockPolicy(nil, []string{"qqonebot:123"}, nil)
	if err != nil {
		t.Fatalf("NewBlockPolicy: %v", err)
	}
	runs := []string{}
	for _, reg := range []Registration{
		{PluginID: "blocked", Name: "blocked", Block: policy},
		{PluginID: "other", Name: "other"},
	} {
		reg.Point = PointPlatformMessageReceived
		reg.Match = Always()
		name := reg.Name
		reg.Handler = HandlerFunc(func(_ context.Context, event Event) (Event, error) {
			runs = append(runs, name)
			return event, nil
		})
		if err := manager.Register(reg); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}
	if _, err := manager.Run(context.Background(), Event{Point: PointPlatformMessageReceived, Platform: PlatformContext{Name: "qqonebot", ScopeID: "group:123"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(runs) != 1 || runs[0] != "other" {
		t.Fatalf("runs = %#v, want other only", runs)
	}
	if err := manager.Notify(context.Background(), Event{Point: PointPlatformMessageReceived, Platform: PlatformContext{Name: "qqonebot", ScopeID: "group:123"}}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(runs) != 2 || runs[1] != "other" {
		t.Fatalf("runs after Notify = %#v, want other twice", runs)
	}
}

func TestPreparedErrorEventExposesMessage(t *testing.T) {
	event, err := NoopManager{}.Run(context.Background(), Event{
		Point: PointErrorOccurred,
		Error: errors.New("boom"),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := EventErrorMessage(event); got != "boom" {
		t.Fatalf("error message = %q, want boom", got)
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if !strings.Contains(string(data), `"error":{"message":"boom"}`) {
		t.Fatalf("event json = %s", data)
	}
}

func TestMatchErrorMessage(t *testing.T) {
	event := Event{Point: PointErrorOccurred, Error: errors.New("hook failed")}
	if !Contains("error.message", "failed").Matches(event) {
		t.Fatal("expected error.message match")
	}
}

func TestMatchMessageIntentText(t *testing.T) {
	event := Event{Point: PointPlatformMessageReceived, Message: MessagePayload{
		Segments:   llm.TextSegments("芙莉丝 咩"),
		IntentText: "咩",
	}}
	if !FullMatch("message.intent_text", "咩").Matches(event) {
		t.Fatal("expected message.intent_text match")
	}
	if FullMatch("message.text", "咩").Matches(event) {
		t.Fatal("message.text should still include the original text")
	}
}

func TestManagerRunsByPriorityAndRegistrationOrder(t *testing.T) {
	manager := NewManager()
	var got []string
	manager.Register(Registration{Point: PointAgentInputPrepared, Priority: 10, Name: "late", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		got = append(got, "late")
		return event, nil
	})})
	manager.Register(Registration{Point: PointAgentInputPrepared, Priority: 0, Name: "first", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		got = append(got, "first")
		return event, nil
	})})
	manager.Register(Registration{Point: PointAgentInputPrepared, Priority: 0, Name: "second", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		got = append(got, "second")
		return event, nil
	})})

	if _, err := manager.Run(context.Background(), Event{Point: PointAgentInputPrepared}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	want := []string{"first", "second", "late"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("run order = %v, want %v", got, want)
	}
}

func TestManagerPassesUpdatedEventToNextHandler(t *testing.T) {
	manager := NewManager()
	manager.Register(Registration{Point: PointLLMResponseReceived, Priority: 0, Name: "clean", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		event.LLM.Text = "cleaned"
		return event, nil
	})})
	manager.Register(Registration{Point: PointLLMResponseReceived, Priority: 1, Name: "append", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		event.LLM.Text += " response"
		return event, nil
	})})

	event, err := manager.Run(context.Background(), Event{Point: PointLLMResponseReceived, LLM: LLMPayload{Text: "raw"}})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if event.LLM.Text != "cleaned response" {
		t.Fatalf("Text = %q, want %q", event.LLM.Text, "cleaned response")
	}
}

func TestMatchMessagePlatformTextAndReplyFields(t *testing.T) {
	event := Event{
		Message: MessagePayload{
			PlatformText: "撤回",
			Segments:     llm.TextSegments("[引用]：通知\n\n撤回"),
			Reply: &MessageReplyPayload{
				MessageID:   "notice-1",
				SenderID:    "bot",
				Text:        "通知",
				DisplayText: "通知",
			},
		},
	}
	match := Match{Conditions: []Condition{
		{Field: "message.platform_text", Op: MatchFull, Value: "撤回"},
		{Field: "message.reply.message_id", Op: MatchExists},
		{Field: "message.reply.sender_id", Op: MatchFull, Value: "bot"},
		{Field: "message.reply.text", Op: MatchFull, Value: "通知"},
		{Field: "message.reply.display_text", Op: MatchContains, Value: "通知"},
	}}
	if result := match.MatchEvent(event); !result.OK {
		t.Fatal("match did not include message platform/reply fields")
	}
}

func TestManagerStopsOnRunError(t *testing.T) {
	manager := NewManager()
	boom := errors.New("boom")
	called := false
	manager.Register(Registration{Point: PointAgentInputPrepared, Priority: 0, Name: "boom", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		event.Message.Segments = llm.ReplaceSegmentText(event.Message.Segments, regexp.MustCompile(".*"), "changed", false)
		return event, boom
	})})
	manager.Register(Registration{Point: PointAgentInputPrepared, Priority: 1, Name: "later", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		called = true
		return event, nil
	})})

	event, err := manager.Run(context.Background(), Event{Point: PointAgentInputPrepared, Message: MessagePayload{Segments: llm.TextSegments("original")}})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
	if !strings.Contains(err.Error(), "hook boom") {
		t.Fatalf("error = %q, want hook name", err.Error())
	}
	if llm.SegmentsTextOnly(event.Message.Segments) != "original" {
		t.Fatalf("event text = %q, want original because failed update is not committed", llm.SegmentsTextOnly(event.Message.Segments))
	}
	if called {
		t.Fatal("expected later handler not to run")
	}
}

func TestManagerObserverWrapsMatchedHookContext(t *testing.T) {
	type contextKey struct{}
	manager := NewManager()
	observed := false
	done := false
	if err := manager.Register(Registration{
		Point:    PointAgentInputPrepared,
		Priority: 7,
		Name:     "observed",
		Match:    Always(),
		Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
			if got, _ := ctx.Value(contextKey{}).(string); got != "wrapped" {
				t.Fatalf("observer context value = %q, want wrapped", got)
			}
			return event, nil
		}),
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	manager.SetObserver(func(ctx context.Context, event Event, info ObserverInfo) (context.Context, func()) {
		observed = true
		if info.Name != "observed" || info.Point != PointAgentInputPrepared || info.Priority != 7 || info.Mode != "run" {
			t.Fatalf("observer info = %#v", info)
		}
		return context.WithValue(ctx, contextKey{}, "wrapped"), func() { done = true }
	})

	if _, err := manager.Run(context.Background(), Event{Point: PointAgentInputPrepared}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !observed || !done {
		t.Fatalf("observer observed=%v done=%v, want both true", observed, done)
	}
}

func TestManagerObserverSkipsUnmatchedAndWakeupSkippedHooks(t *testing.T) {
	manager := NewManager()
	observed := 0
	called := 0
	if err := manager.Register(Registration{
		Point:  PointLLMResponseReceived,
		Name:   "unmatched",
		Match:  Contains("llm.text", "[["),
		Wakeup: WakeupAny,
		Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
			called++
			return event, nil
		}),
	}); err != nil {
		t.Fatalf("Register unmatched: %v", err)
	}
	if err := manager.Register(Registration{
		Point: PointLLMResponseReceived,
		Name:  "wakeup-skipped",
		Match: Always(),
		Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
			called++
			return event, nil
		}),
	}); err != nil {
		t.Fatalf("Register wakeup-skipped: %v", err)
	}
	manager.SetWakeupFunc(func(context.Context, Event) bool { return false })
	manager.SetObserver(func(ctx context.Context, event Event, info ObserverInfo) (context.Context, func()) {
		observed++
		return ctx, func() {}
	})

	if _, err := manager.Run(context.Background(), Event{Point: PointLLMResponseReceived, LLM: LLMPayload{Text: "plain"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if observed != 0 || called != 0 {
		t.Fatalf("observed=%d called=%d, want both 0", observed, called)
	}
}

func TestManagerLogsCanceledHookAsInfo(t *testing.T) {
	var buf bytes.Buffer
	manager := NewManager()
	manager.SetLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err := manager.Register(Registration{
		Point: PointAgentInputPrepared,
		Name:  "cancel",
		Match: Always(),
		Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
			return event, context.Canceled
		}),
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err := manager.Run(context.Background(), Event{Point: PointAgentInputPrepared})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context canceled", err)
	}
	logs := buf.String()
	if !strings.Contains(logs, "level=INFO") || !strings.Contains(logs, "hook canceled") {
		t.Fatalf("logs missing info cancellation:\n%s", logs)
	}
	if strings.Contains(logs, "hook error") || strings.Contains(logs, "level=WARN") {
		t.Fatalf("canceled hook should not log warning/error:\n%s", logs)
	}
}

func TestNotifyRunsAllHandlersAndJoinsErrors(t *testing.T) {
	manager := NewManager()
	first := errors.New("first")
	second := errors.New("second")
	calls := 0
	manager.Register(Registration{Point: PointErrorOccurred, Priority: 0, Name: "first", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		calls++
		return event, first
	})})
	manager.Register(Registration{Point: PointErrorOccurred, Priority: 1, Name: "second", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		calls++
		return event, second
	})})

	err := manager.Notify(context.Background(), Event{Point: PointErrorOccurred})
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("joined error = %v, want both errors", err)
	}
	if !strings.Contains(err.Error(), "hook first") || !strings.Contains(err.Error(), "hook second") {
		t.Fatalf("joined error = %q, want hook names", err.Error())
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestManagerLogsNamedHook(t *testing.T) {
	var buf bytes.Buffer
	manager := NewManager()
	manager.SetLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	manager.Register(Registration{Point: PointLLMResponseReceived, Priority: 5, Name: "test.logger", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		event.LLM.Text = "after"
		return event, nil
	})})

	if _, err := manager.Run(context.Background(), Event{Point: PointLLMResponseReceived, LLM: LLMPayload{Text: "before", SourceText: "raw"}}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	logs := buf.String()
	for _, want := range []string{"hook triggered", "test.logger", "before_text=before", "after_text=after", "source_text=raw"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q:\n%s", want, logs)
		}
	}
}

func TestManagerSkipsUnmatchedHookAndLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	manager := NewManager()
	manager.SetLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	called := false
	if err := manager.Register(Registration{
		Point:    PointLLMResponseReceived,
		Priority: 0,
		Name:     "test.unmatched",
		Match:    Contains("llm.text", "[["),
		Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
			called = true
			return event, nil
		}),
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := manager.Run(context.Background(), Event{Point: PointLLMResponseReceived, LLM: LLMPayload{Text: "plain text"}}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if called {
		t.Fatal("unmatched hook should not run")
	}
	if logs := buf.String(); strings.Contains(logs, "hook triggered") || strings.Contains(logs, "hook event") {
		t.Fatalf("unmatched hook should not be logged:\n%s", logs)
	}
}

func TestManagerRejectsRegistrationWithoutExplicitMatch(t *testing.T) {
	manager := NewManager()
	err := manager.Register(Registration{Point: PointAgentInputPrepared, Name: "test.no-match", Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		return event, nil
	})})
	if err == nil {
		t.Fatal("expected registration without match to fail")
	}
}

func TestManagerPassesRegexMatchContext(t *testing.T) {
	manager := NewManager()
	var got MatchContext
	if err := manager.Register(Registration{
		Point: PointAgentInputPrepared,
		Name:  "test.regex",
		Match: Regex("message.text", `^mute (?P<target>\S+) (?P<minutes>\d+)$`),
		Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
			got, _ = event.Metadata["match"].(MatchContext)
			return event, nil
		}),
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := manager.Run(context.Background(), Event{Point: PointAgentInputPrepared, Message: MessagePayload{Segments: llm.TextSegments("mute alice 10")}}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(got.Regex) != 1 {
		t.Fatalf("regex matches = %#v", got.Regex)
	}
	match := got.Regex[0]
	if match.Text != "mute alice 10" || match.Named["target"] != "alice" || match.Named["minutes"] != "10" {
		t.Fatalf("match = %#v", match)
	}
}

func TestManagerFiltersByWakeupPolicy(t *testing.T) {
	manager := NewManager()
	woken := false
	manager.SetWakeupFunc(func(context.Context, Event) bool { return woken })
	calls := []string{}
	if err := manager.Register(Registration{Point: PointPlatformMessageReceived, Name: "default", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		calls = append(calls, "default")
		return event, nil
	})}); err != nil {
		t.Fatalf("Register default: %v", err)
	}
	if err := manager.Register(Registration{Point: PointPlatformMessageReceived, Name: "any", Match: Always(), Wakeup: WakeupAny, Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		calls = append(calls, "any")
		return event, nil
	})}); err != nil {
		t.Fatalf("Register any: %v", err)
	}
	if err := manager.Register(Registration{Point: PointPlatformMessageReceived, Name: "forbidden", Match: Always(), Wakeup: WakeupForbidden, Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		calls = append(calls, "forbidden")
		return event, nil
	})}); err != nil {
		t.Fatalf("Register forbidden: %v", err)
	}

	if _, err := manager.Run(context.Background(), Event{Point: PointPlatformMessageReceived}); err != nil {
		t.Fatalf("Run unwoken message: %v", err)
	}
	woken = true
	if _, err := manager.Run(context.Background(), Event{Point: PointPlatformMessageReceived}); err != nil {
		t.Fatalf("Run woken message: %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"any", "forbidden", "default", "any"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestManagerRejectsUnsupportedWakeupPolicy(t *testing.T) {
	manager := NewManager()
	err := manager.Register(Registration{Point: PointPlatformMessageReceived, Name: "invalid", Match: Always(), Wakeup: "sometimes", Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		return event, nil
	})})
	if err == nil || !strings.Contains(err.Error(), `unsupported wakeup policy "sometimes"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestManagerRunsWakeupRequiredHookWhenWakeupUnknown(t *testing.T) {
	manager := NewManager()
	called := false
	if err := manager.Register(Registration{Point: PointPlatformConnected, Name: "connected", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		called = true
		return event, nil
	})}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := manager.Run(context.Background(), Event{Point: PointPlatformConnected}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !called {
		t.Fatal("expected hook to run when no wakeup function is configured")
	}
}

func TestManagerStopsPropagation(t *testing.T) {
	manager := NewManager()
	calls := 0
	if err := manager.Register(Registration{Point: PointAgentInputPrepared, Name: "first", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		calls++
		event.Control.StopPropagation = true
		return event, nil
	})}); err != nil {
		t.Fatalf("Register first: %v", err)
	}
	if err := manager.Register(Registration{Point: PointAgentInputPrepared, Priority: 1, Name: "second", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		calls++
		return event, nil
	})}); err != nil {
		t.Fatalf("Register second: %v", err)
	}

	got, err := manager.Run(context.Background(), Event{Point: PointAgentInputPrepared})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if calls != 1 || !got.Control.StopPropagation {
		t.Fatalf("calls = %d, control = %#v", calls, got.Control)
	}
}
func TestManagerMarksHookOutputs(t *testing.T) {
	manager := NewManager()
	if err := manager.Register(Registration{Point: PointPlatformConnected, Priority: 0, Name: "notify.connected", Match: Always(), Handler: HandlerFunc(func(ctx context.Context, event Event) (Event, error) {
		event.Outputs = append(event.Outputs, delivery.Text("connected"))
		return event, nil
	})}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	event, err := manager.Run(context.Background(), Event{Point: PointPlatformConnected})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(event.Outputs) != 1 {
		t.Fatalf("outputs = %d, want 1", len(event.Outputs))
	}
	meta := event.Outputs[0].Meta
	if meta[delivery.MetaHookName] != "notify.connected" || meta[delivery.MetaHookPoint] != string(PointPlatformConnected) || meta[delivery.MetaHookMode] != "run" {
		t.Fatalf("output meta = %#v", meta)
	}
}
