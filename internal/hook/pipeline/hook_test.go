// 验证 Hook 匹配、流水线执行与事件语义，共享定义相关用例待拆分。

package pipeline

import (
	"bytes"
	"context"
	"elbot/internal/hook/share"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/signal"
)

func TestBlockPolicyMatchesPlatformGroupAndUser(t *testing.T) {
	policy, err := share.NewBlockPolicy(
		[]string{"telegram"},
		[]string{"qqonebot:123", "telegram:-100456"},
		[]string{"qqonebot:42"},
	)
	if err != nil {
		t.Fatalf("NewBlockPolicy: %v", err)
	}
	tests := []struct {
		name  string
		event share.Event
		want  bool
	}{
		{name: "platform", event: share.Event{Platform: share.PlatformContext{Name: "telegram"}}, want: true},
		{name: "group", event: share.Event{Platform: share.PlatformContext{Name: "qqonebot", ScopeID: "group:123"}}, want: true},
		{name: "supergroup", event: share.Event{Platform: share.PlatformContext{Name: "telegram", ScopeID: "supergroup:-100456"}}, want: true},
		{name: "user", event: share.Event{Platform: share.PlatformContext{Name: "qqonebot"}, Actor: share.ActorContext{UserID: "42"}}, want: true},
		{name: "private scope is not group", event: share.Event{Platform: share.PlatformContext{Name: "qqonebot", ScopeID: "private:123"}}},
		{name: "other", event: share.Event{Platform: share.PlatformContext{Name: "qqonebot", ScopeID: "group:999"}, Actor: share.ActorContext{UserID: "7"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := policy.Blocks(tt.event); got != tt.want {
				t.Fatalf("Blocks() = %v, want %v", got, tt.want)
			}
		})
	}
	if _, err := share.NewBlockPolicy(nil, []string{"missing-platform"}, nil); err == nil {
		t.Fatal("expected malformed blocked_group to fail")
	}
}

func TestManagerSkipsBlockedPluginAndContinues(t *testing.T) {
	manager := newTestManager()

	policy, err := share.NewBlockPolicy(nil, []string{"qqonebot:123"}, nil)
	if err != nil {
		t.Fatalf("NewBlockPolicy: %v", err)
	}
	runs := []string{}
	for _, reg := range []share.Registration{
		{PluginID: "blocked", Name: "blocked", Block: policy},
		{PluginID: "other", Name: "other"},
	} {
		reg.Point = share.PointPlatformMessageReceived
		reg.Match = share.Always()
		name := reg.Name
		reg.Handler = share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
			runs = append(runs, name)
			return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
		})
		manager.Register(reg)
	}
	if _, _, err := manager.Run(context.Background(), share.Event{Point: share.PointPlatformMessageReceived, Platform: share.PlatformContext{Name: "qqonebot", ScopeID: "group:123"}}, CallOptions{Woken: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(runs) != 1 || runs[0] != "other" {
		t.Fatalf("runs = %#v, want other only", runs)
	}
	if _, err := manager.Notify(context.Background(), share.Event{Point: share.PointPlatformMessageReceived, Platform: share.PlatformContext{Name: "qqonebot", ScopeID: "group:123"}}, CallOptions{Woken: true}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(runs) != 2 || runs[1] != "other" {
		t.Fatalf("runs after Notify = %#v, want other twice", runs)
	}
}

func TestPreparedErrorEventExposesMessage(t *testing.T) {
	event, _, err := newTestManager().Run(context.Background(), share.Event{
		Point: share.PointErrorOccurred,
		Error: errors.New("boom"),
	}, CallOptions{Woken: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := share.EventErrorMessage(event); got != "boom" {
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
	event := share.Event{Point: share.PointErrorOccurred, Error: errors.New("hook failed")}
	if !share.Contains("error.message", "failed").Matches(event) {
		t.Fatal("expected error.message match")
	}
}

func TestMatchMessageIntentText(t *testing.T) {
	event := share.Event{Point: share.PointPlatformMessageReceived, Message: share.MessagePayload{
		Segments:   llm.TextSegments("芙莉丝 咩"),
		IntentText: "咩",
	}}
	if !share.FullMatch("message.intent_text", "咩").Matches(event) {
		t.Fatal("expected message.intent_text match")
	}
	if share.FullMatch("message.text", "咩").Matches(event) {
		t.Fatal("message.text should still include the original text")
	}
}

func TestManagerRunsByPriorityAndRegistrationOrder(t *testing.T) {
	manager := newTestManager()

	var got []string
	manager.Register(share.Registration{Point: share.PointAgentInputPrepared, Priority: 10, Name: "late", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		got = append(got, "late")
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})})
	manager.Register(share.Registration{Point: share.PointAgentInputPrepared, Priority: 0, Name: "first", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		got = append(got, "first")
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})})
	manager.Register(share.Registration{Point: share.PointAgentInputPrepared, Priority: 0, Name: "second", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		got = append(got, "second")
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})})

	if _, _, err := manager.Run(context.Background(), share.Event{Point: share.PointAgentInputPrepared}, CallOptions{Woken: true}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	want := []string{"first", "second", "late"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("run order = %v, want %v", got, want)
	}
}

func TestManagerPassesUpdatedEventToNextHandler(t *testing.T) {
	manager := newTestManager()

	manager.Register(share.Registration{Point: share.PointLLMResponseReceived, Priority: 0, Name: "clean", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		event.LLM.Text = "cleaned"
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})})
	manager.Register(share.Registration{Point: share.PointLLMResponseReceived, Priority: 1, Name: "append", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		event.LLM.Text += " response"
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})})

	event, _, err := manager.Run(context.Background(), share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: "raw"}}, CallOptions{Woken: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if event.LLM.Text != "cleaned response" {
		t.Fatalf("Text = %q, want %q", event.LLM.Text, "cleaned response")
	}
}

func TestMatchMessagePlatformTextAndReplyFields(t *testing.T) {
	event := share.Event{
		Message: share.MessagePayload{
			PlatformText: "撤回",
			Segments:     llm.TextSegments("[引用]：通知\n\n撤回"),
			Reply: &share.MessageReplyPayload{
				MessageID:   "notice-1",
				SenderID:    "bot",
				Text:        "通知",
				DisplayText: "通知",
			},
		},
	}
	match := share.Match{Conditions: []share.Condition{
		{Field: "message.platform_text", Op: share.MatchFull, Value: "撤回"},
		{Field: "message.reply.message_id", Op: share.MatchExists},
		{Field: "message.reply.sender_id", Op: share.MatchFull, Value: "bot"},
		{Field: "message.reply.text", Op: share.MatchFull, Value: "通知"},
		{Field: "message.reply.display_text", Op: share.MatchContains, Value: "通知"},
	}}
	if result := match.MatchEvent(event); !result.OK {
		t.Fatal("match did not include message platform/reply fields")
	}
}

func TestManagerStopsOnRunError(t *testing.T) {
	manager := newTestManager()

	boom := errors.New("boom")
	called := false
	manager.Register(share.Registration{Point: share.PointAgentInputPrepared, Priority: 0, Name: "boom", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		event.Message.Segments = llm.ReplaceSegmentText(event.Message.Segments, regexp.MustCompile(".*"), "changed", false)
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "test source failed"}, boom
	})})
	manager.Register(share.Registration{Point: share.PointAgentInputPrepared, Priority: 1, Name: "later", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		called = true
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})})

	event, _, err := manager.Run(context.Background(), share.Event{Point: share.PointAgentInputPrepared, Message: share.MessagePayload{Segments: llm.TextSegments("original")}}, CallOptions{Woken: true})
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
	manager := newTestManager()

	observed := false
	done := false
	manager.Register(share.Registration{
		Point:    share.PointAgentInputPrepared,
		Priority: 7,
		Name:     "observed",
		Match:    share.Always(),
		Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
			if got, _ := ctx.Value(contextKey{}).(string); got != "wrapped" {
				t.Fatalf("observer context value = %q, want wrapped", got)
			}
			return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
		}),
	})
	options := CallOptions{Woken: true, Observe: func(ctx context.Context, name string) (context.Context, func()) {
		observed = true
		if name != "observed" {
			t.Fatalf("observer name = %q", name)
		}
		return context.WithValue(ctx, contextKey{}, "wrapped"), func() { done = true }
	}}

	if _, _, err := manager.Run(context.Background(), share.Event{Point: share.PointAgentInputPrepared}, options); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !observed || !done {
		t.Fatalf("observer observed=%v done=%v, want both true", observed, done)
	}
}

func TestManagerObserverSkipsUnmatchedAndWakeupSkippedHooks(t *testing.T) {
	manager := newTestManager()

	observed := 0
	called := 0
	manager.Register(share.Registration{
		Point:  share.PointLLMResponseReceived,
		Name:   "unmatched",
		Match:  share.Contains("llm.text", "[["),
		Wakeup: share.WakeupAny,
		Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
			called++
			return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
		}),
	})
	manager.Register(share.Registration{
		Point: share.PointLLMResponseReceived,
		Name:  "wakeup-skipped",
		Match: share.Always(),
		Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
			called++
			return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
		}),
	})
	options := CallOptions{Woken: false, Observe: func(ctx context.Context, name string) (context.Context, func()) {
		observed++
		return ctx, func() {}
	}}

	if _, _, err := manager.Run(context.Background(), share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: "plain"}}, options); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if observed != 0 || called != 0 {
		t.Fatalf("observed=%d called=%d, want both 0", observed, called)
	}
}

func TestManagerLogsCanceledHookAsInfo(t *testing.T) {
	var buf bytes.Buffer
	manager := newTestManager()

	captureLogs(t, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	manager.Register(share.Registration{
		Point: share.PointAgentInputPrepared,
		Name:  "cancel",
		Match: share.Always(),
		Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
			return event, globalevents.LogResult{ResultStatus: globalevents.ResultCanceled, Content: "test source canceled"}, context.Canceled
		}),
	})

	_, _, err := manager.Run(context.Background(), share.Event{Point: share.PointAgentInputPrepared}, CallOptions{Woken: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context canceled", err)
	}
	logs := buf.String()
	if !strings.Contains(logs, "level=INFO") || !strings.Contains(logs, "event=hook_canceled") {
		t.Fatalf("logs missing info cancellation:\n%s", logs)
	}
}

func TestNotifyRunsAllHandlersAndJoinsErrors(t *testing.T) {
	manager := newTestManager()

	first := errors.New("first")
	second := errors.New("second")
	calls := 0
	manager.Register(share.Registration{Point: share.PointErrorOccurred, Priority: 0, Name: "first", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		calls++
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "test source failed"}, first
	})})
	manager.Register(share.Registration{Point: share.PointErrorOccurred, Priority: 1, Name: "second", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		calls++
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "test source failed"}, second
	})})

	_, err := manager.Notify(context.Background(), share.Event{Point: share.PointErrorOccurred}, CallOptions{Woken: true})
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
	manager := newTestManager()
	var record globalevents.LogRecord
	manager.Log = func(_ context.Context, r globalevents.LogRecord) { record = r }
	manager.Register(share.Registration{Point: share.PointLLMResponseReceived, Priority: 5, PluginID: "test.plugin", Name: "test.logger", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		event.LLM.Text = "after"
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})})

	if _, _, err := manager.Run(context.Background(), share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: strings.Repeat("雪", 1025), SourceText: strings.Repeat("雪", 1025), Provider: "test-provider", Model: "test-model"}}, CallOptions{Woken: true}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(record.Content), &content); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"detail": "test source completed", "hook": "test.logger", "plugin": "test.plugin",
		"provider": "test-provider", "model": "test-model", "before_text": strings.Repeat("雪", 1023) + "…", "after_text": "after", "source_text": "同before_text",
	} {
		if content[key] != want {
			t.Fatalf("field %s=%v want=%s", key, content[key], want)
		}
	}
}

func TestManagerOmitsInapplicableLogFields(t *testing.T) {
	manager := newTestManager()

	var records []globalevents.LogRecord
	connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, r globalevents.LogRecord) error {
		if r.EventName == "hook_triggered" {
			records = append(records, r)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
	manager.Register(share.Registration{Point: share.PointAgentTurnOutputPrepared, Name: "clear_text", Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		event.Message.Segments = nil
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "action=delete"}, nil
	})})
	if _, _, err := manager.Run(t.Context(), share.Event{Point: share.PointAgentTurnOutputPrepared, Message: share.MessagePayload{Segments: llm.TextSegments("before")}}, CallOptions{Woken: true}); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatal(records)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(records[0].Content), &content); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"plugin", "provider", "model", "tool", "source_text"} {
		if _, ok := content[field]; ok {
			t.Fatalf("inapplicable field %s: %s", field, records[0].Content)
		}
	}
	if content["before_text"] != "before" || content["after_text"] != "" {
		t.Fatalf("text deletion lost: %s", records[0].Content)
	}
}

func TestManagerSkipsUnmatchedHookAndLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	manager := newTestManager()

	captureLogs(t, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	called := false
	manager.Register(share.Registration{
		Point:    share.PointLLMResponseReceived,
		Priority: 0,
		Name:     "test.unmatched",
		Match:    share.Contains("llm.text", "[["),
		Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
			called = true
			return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
		}),
	})

	if _, _, err := manager.Run(context.Background(), share.Event{Point: share.PointLLMResponseReceived, LLM: share.LLMPayload{Text: "plain text"}}, CallOptions{Woken: true}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if called {
		t.Fatal("unmatched hook should not run")
	}
	if logs := buf.String(); logs != "" {
		t.Fatalf("unmatched hook should not be logged:\n%s", logs)
	}
}

func TestManagerPassesRegexMatchContext(t *testing.T) {
	manager := newTestManager()

	var got share.MatchContext
	manager.Register(share.Registration{
		Point: share.PointAgentInputPrepared,
		Name:  "test.regex",
		Match: share.Regex("message.text", `^mute (?P<target>\S+) (?P<minutes>\d+)$`),
		Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
			got, _ = event.Metadata["match"].(share.MatchContext)
			return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
		}),
	})

	if _, _, err := manager.Run(context.Background(), share.Event{Point: share.PointAgentInputPrepared, Message: share.MessagePayload{Segments: llm.TextSegments("mute alice 10")}}, CallOptions{Woken: true}); err != nil {
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
	manager := newTestManager()

	woken := false
	calls := []string{}
	manager.Register(share.Registration{Point: share.PointPlatformMessageReceived, Name: "default", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		calls = append(calls, "default")
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})})
	manager.Register(share.Registration{Point: share.PointPlatformMessageReceived, Name: "any", Match: share.Always(), Wakeup: share.WakeupAny, Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		calls = append(calls, "any")
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})})
	manager.Register(share.Registration{Point: share.PointPlatformMessageReceived, Name: "forbidden", Match: share.Always(), Wakeup: share.WakeupForbidden, Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		calls = append(calls, "forbidden")
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})})

	if _, _, err := manager.Run(context.Background(), share.Event{Point: share.PointPlatformMessageReceived}, CallOptions{Woken: woken}); err != nil {
		t.Fatalf("Run unwoken message: %v", err)
	}
	woken = true
	if _, _, err := manager.Run(context.Background(), share.Event{Point: share.PointPlatformMessageReceived}, CallOptions{Woken: woken}); err != nil {
		t.Fatalf("Run woken message: %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"any", "forbidden", "default", "any"}) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestManagerStopsPropagation(t *testing.T) {
	manager := newTestManager()

	calls := 0
	manager.Register(share.Registration{Point: share.PointAgentInputPrepared, Name: "first", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		calls++
		event.Control.StopPropagation = true
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})})
	manager.Register(share.Registration{Point: share.PointAgentInputPrepared, Priority: 1, Name: "second", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		calls++
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})})

	got, _, err := manager.Run(context.Background(), share.Event{Point: share.PointAgentInputPrepared}, CallOptions{Woken: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if calls != 1 || !got.Control.StopPropagation {
		t.Fatalf("calls = %d, control = %#v", calls, got.Control)
	}
}
func TestManagerMarksHookOutputs(t *testing.T) {
	manager := newTestManager()
	output := delivery.WithDeliveryTiming(delivery.Text("connected"), delivery.DeliveryAfterAssistant)

	manager.Register(share.Registration{Point: share.PointPlatformConnected, Priority: 0, Name: "notify.connected", Match: share.Always(), Handler: share.HandlerFunc(func(ctx context.Context, event share.Event, _ bool) (share.Event, globalevents.LogResult, error) {
		event.Outputs = append(event.Outputs, output)
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
	})})

	event, _, err := manager.Run(context.Background(), share.Event{Point: share.PointPlatformConnected}, CallOptions{Woken: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(event.Outputs) != 1 {
		t.Fatalf("outputs = %d, want 1", len(event.Outputs))
	}
	info := event.Outputs[0].Delivery
	if info != (delivery.DeliveryInfo{Timing: delivery.DeliveryAfterAssistant, HookName: "notify.connected", HookPoint: string(share.PointPlatformConnected), HookMode: "run"}) {
		t.Fatalf("delivery info = %#v", info)
	}
	if output.Delivery != (delivery.DeliveryInfo{Timing: delivery.DeliveryAfterAssistant}) {
		t.Fatalf("marking changed original output: %#v", output.Delivery)
	}
}

func TestMarkHookOutputsPreservesExistingSource(t *testing.T) {
	info := delivery.DeliveryInfo{Timing: delivery.DeliveryAfterAssistant, HookName: "inner", HookPoint: "inner.point", HookMode: "run"}
	event := share.Event{Point: share.PointPlatformConnected, Outputs: []delivery.Output{{Kind: delivery.KindText, Text: "hello", Delivery: info}}}
	got := event
	markHookOutputs(got, 0, registration{name: "outer"}, "notify")
	markHookOutputs(got, 2, registration{name: "outer"}, "notify")
	if got.Outputs[0].Delivery != info {
		t.Fatalf("source changed: %#v, want %#v", got.Outputs[0].Delivery, info)
	}
}

// captureLogs observes the public signal; production never receives this logger.
func captureLogs(t *testing.T, logger *slog.Logger) {
	t.Helper()
	connection, err := globalevents.LogSubmitted.Observe(func(ctx context.Context, record globalevents.LogRecord) error {
		if !logger.Enabled(ctx, record.Level) {
			return nil
		}
		out := slog.NewRecord(time.Now(), record.Level, record.Content, 0)
		out.AddAttrs(slog.String("event", record.EventName), slog.String("module", record.Module), slog.String("result_status", record.ResultStatus.String()))
		return logger.Handler().Handle(ctx, out)
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
}

func newTestManager() *Manager {
	return NewManager(func(ctx context.Context, record globalevents.LogRecord) {
		_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
	}, nil)
}
