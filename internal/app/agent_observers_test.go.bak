package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	agentevents "elbot/internal/agent/events"
	globalevents "elbot/internal/events"
	"elbot/internal/signal"
)

func observerSignals() agentevents.Signals {
	return agentevents.Signals{
		ModelCallCompleted: signal.New[agentevents.ModelCallCompletedEvent, signal.Empty]("model"),
		StatusChanged:      signal.New[agentevents.StatusChangedEvent, signal.Empty]("status"),
		VisionFallbackUsed: signal.New[agentevents.VisionFallbackUsedEvent, signal.Empty]("vision"),
		HookFailed:         signal.New[agentevents.HookFailedEvent, signal.Empty]("hook"),
	}
}

type recordHandler struct {
	records chan slog.Record
	fail    bool
}

func (h recordHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordHandler) Handle(_ context.Context, r slog.Record) error {
	h.records <- r.Clone()
	if h.fail {
		return errors.New("disk write failed")
	}
	return nil
}
func (h recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordHandler) WithGroup(string) slog.Handler      { return h }
func recordAttrs(record slog.Record) map[string]any {
	attrs := map[string]any{}
	record.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.Any(); return true })
	return attrs
}
func awaitRecord(t *testing.T, records <-chan slog.Record) slog.Record {
	t.Helper()
	select {
	case record := <-records:
		return record
	case <-time.After(time.Second):
		t.Fatal("missing log record")
		return slog.Record{}
	}
}

func TestProductionAssemblyObservesConversationExactlyOnce(t *testing.T) {
	req, _, _ := runtimeAssemblyFixture(t)
	records := make(chan slog.Record, 512)
	logger := slog.New(recordHandler{records: records})
	captureLogs(t, logger)
	runtime, err := (defaultWiringFactory{}).Build(context.Background(), req)
	t.Cleanup(func() { closeAssembledRuntime(t, runtime) })
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Dialogue.Handler().HandleMessage(context.Background(), "observe this"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Lifecycle.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for len(records) > 0 {
		counts[recordAttrs(<-records)["event"].(string)]++
	}
	for _, message := range []string{"user_message", "assistant_message", "reply_delivered"} {
		if counts[message] != 1 {
			t.Fatalf("%s: %d records", message, counts[message])
		}
	}
}

func captureLogs(t *testing.T, logger *slog.Logger) {
	t.Helper()
	connection, err := globalevents.LogSubmitted.Observe(func(ctx context.Context, record globalevents.LogRecord) error {
		if !logger.Enabled(ctx, record.Level) {
			return nil
		}
		out := slog.NewRecord(time.Now(), record.Level, record.Content, 0)
		out.AddAttrs(slog.String("event", record.EventName), slog.String("module", record.Module))
		return logger.Handler().Handle(ctx, out)
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Disconnect)
}
