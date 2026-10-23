package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/logging"
)

func TestProductionGlobalLogsKeepUsageAndBoundBodies(t *testing.T) {
	for _, level := range []string{"info", "debug", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			req, _, model := runtimeAssemblyFixtureWithLogLevel(t, level)
			model.chunks = [][]chatcompletions.Chunk{{{
				DeltaContent: "assembled answer",
				Usage: &llm.Usage{
					PromptTokens:     func() *int64 { n := int64(10); return &n }(),
					CompletionTokens: func() *int64 { n := int64(5); return &n }(),
					TotalTokens:      func() *int64 { n := int64(15); return &n }(),
				},
			}}}
			runtime, err := (defaultWiringFactory{}).Build(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeAssembledRuntime(t, runtime) })
			body := strings.Repeat("正文", 300) + "end-marker"
			if err := runtime.Dialogue.Handler().HandleMessage(context.Background(), body); err != nil {
				t.Fatal(err)
			}
			if _, err := events.NotificationRequested.Emit(context.Background(), events.Notification{Target: events.NotificationTarget{Platform: "service"}, Text: "background notice", Level: slog.LevelWarn}); err != nil {
				t.Fatal(err)
			}
			manager := hook.NewManager()
			manager.SetWakeupFunc(func(context.Context, hook.Event) bool { return true })
			manager.SetObserver(func(ctx context.Context, _ hook.Event, _ hook.ObserverInfo) (context.Context, func()) {
				return ctx, func() {}
			})
			manager.Register(hook.Registration{Point: hook.PointErrorOccurred, Name: "audit-hook", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, events.LogResult, error) {
				return event, events.LogResult{ResultStatus: events.ResultFailed, Content: "test source failed"}, errors.New("hook failure")
			})})
			_, _ = manager.Notify(context.Background(), hook.Event{Point: hook.PointErrorOccurred})
			profiler := newStartupProfiler(time.Now())
			profiler.SetEnabled(true)
			profiler.Mark("test-stage")
			profiler.Flush()
			if err := req.Foundation.Logs.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			reader := logging.Reader{Dir: req.Foundation.Logs.LogDir()}
			audit, err := reader.Query(context.Background(), logging.LogQuery{Prefix: "audit", Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, entry := range audit.Entries {
				if entry.EventName == "llm_usage" {
					found = true
					var content map[string]json.RawMessage
					if err := json.Unmarshal([]byte(entry.Content), &content); err != nil {
						t.Fatal(err)
					}
					if string(content["total_tokens"]) != "15" {
						t.Fatal("lost usage fields")
					}
				}
			}
			if !found {
				t.Fatal("runtime level suppressed llm_usage")
			}
			entries, err := reader.Query(context.Background(), logging.LogQuery{Prefix: "elbot", Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, entry := range entries.Entries {
				counts[entry.EventName]++
				if entry.EventName == "hook_error" && entry.Level != "ERROR" {
					t.Fatalf("final Hook failure severity: %+v", entry)
				}
				if entry.EventName == "user_message" {
					if !strings.Contains(entry.Content, "end-marker") {
						t.Fatalf("body policy for %s: %s", level, entry.Raw)
					}

				}
			}
			if level == "info" || level == "debug" {
				if counts["user_message"] != 1 || counts["assistant_message"] != 1 {
					t.Fatal(counts)
				}
			} else if counts["user_message"] != 0 {
				t.Fatal(counts)
			}
			if counts["hook_error"] != 1 || (level != "error" && counts["runtime_notification"] != 1) {
				t.Fatal(counts)
			}
			if (counts["startup_stage"] == 1) != (level == "debug") {
				t.Fatal(counts)
			}
		})
	}
}
