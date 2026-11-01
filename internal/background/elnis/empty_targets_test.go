package elnis

import (
	"context"
	"slices"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/events"
	"elbot/internal/signal"
)

func TestLLMRejectsEmptyEffectiveTargetsBeforeEnqueue(t *testing.T) {
	for _, mode := range []string{"disabled", "all_without_platforms", "unconfigured"} {
		t.Run(mode, func(t *testing.T) {
			runner := &fakeBackgroundRunner{}
			service, cleanup := newTestServiceWithRunner(t, runner, nil)
			defer cleanup()
			req := testRequest(ModeLLM)
			if mode == "disabled" {
				policy := service.cfg.Elwisps["watcher"]
				policy.DisabledTargets = []config.ElnisTargetConfig{{Platform: "cli"}}
				service.cfg.Elwisps["watcher"] = policy
			} else if mode == "all_without_platforms" {
				req.Targets = []Target{{Platform: "all"}}
				service.enabledPlatforms = nil
			} else {
				req.Targets = []Target{{Platform: "telegram"}}
			}
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.EventName == "elnis.rejected" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			queued := 0
			service.SetLLMEnqueuer(func(context.Context, QueuedLLMEvent) error { queued++; return nil })
			resp, err := service.Handle(t.Context(), "secret", req)
			if err == nil || resp.Accepted || queued != 0 || len(runner.requests) != 0 {
				t.Fatalf("response=%+v err=%v queued=%d runner=%d", resp, err, queued, len(runner.requests))
			}
			if len(records) != 1 || records[0].ResultStatus != events.ResultRejected || !strings.Contains(records[0].Content, err.Error()) {
				t.Fatalf("logs=%+v err=%v", records, err)
			}
		})
	}
}

func TestLLMForwardsOnlyEffectiveTargetPlatforms(t *testing.T) {
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":false}`}
	service, cleanup := newTestServiceWithRunner(t, runner, nil)
	defer cleanup()
	policy := service.cfg.Elwisps["watcher"]
	policy.DisabledTargets = []config.ElnisTargetConfig{{Platform: "qqonebot"}}
	service.cfg.Elwisps["watcher"] = policy
	var queued QueuedLLMEvent
	service.SetLLMEnqueuer(func(_ context.Context, event QueuedLLMEvent) error { queued = event; return nil })
	req := testRequest(ModeLLM)
	req.Targets = []Target{{Platform: "all"}, {Platform: "telegram"}}
	if _, err := service.Handle(t.Context(), "secret", req); err != nil {
		t.Fatal(err)
	}
	if err := service.RunLLMEvent(t.Context(), queued.Event, queued.EventID); err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 || !slices.Equal(runner.requests[0].TargetPlatforms, []string{"cli"}) {
		t.Fatalf("requests=%+v", runner.requests)
	}
}
