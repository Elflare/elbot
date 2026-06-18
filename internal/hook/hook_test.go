package hook

import (
	"bytes"
	"context"
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

	if _, err := manager.Run(context.Background(), Event{Point: PointLLMResponseReceived, LLM: LLMPayload{Text: "before", RawText: "raw"}}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	logs := buf.String()
	for _, want := range []string{"hook triggered", "hook event", "test.logger", "before_text=before", "after_text=after", "raw_text=raw"} {
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
