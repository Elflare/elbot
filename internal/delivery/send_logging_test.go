package delivery

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/signal"
)

func TestSendLoggingKeepsSourceResultAndPartialReceipt(t *testing.T) {
	logs := make(chan events.LogRecord, 4)
	c, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) error {
		if r.Module == "delivery" {
			logs <- r
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()
	want := errors.New("partial send failed")
	sender := &independentChatSender{fakeSender: fakeSender{err: want}}
	facts := contextinfo.Execution{SessionID: "session", RunID: "run", Attempt: "attempt", RequestID: "request", RootRequestID: "root"}
	ctx := contextinfo.WithExecution(t.Context(), facts)
	out := Text("notice")
	out.Delivery = DeliveryInfo{HookName: " notify ", HookPoint: " point ", HookMode: " run "}
	receipt, err := NewManager(sender).SendTo(ctx, Target{Platform: "test"}, []Output{out})
	if !errors.Is(err, want) || len(receipt.PlatformMessageIDs) != 1 || len(logs) != 1 {
		t.Fatal(receipt, err, len(logs))
	}
	record := <-logs
	if record.Level != slog.LevelWarn || record.ResultStatus != events.ResultFailed {
		t.Fatalf("failure record = %#v", record)
	}
	for _, content := range []string{want.Error(), `hook="notify"`, `hook_point="point"`, `hook_mode="run"`} {
		if !strings.Contains(record.Content, content) {
			t.Fatalf("log %q missing %q", record.Content, content)
		}
	}
	identity := events.LogIdentity{SessionID: &facts.SessionID, RunID: &facts.RunID, Attempt: &facts.Attempt, RequestID: &facts.RequestID, RootRequestID: &facts.RootRequestID}
	if !reflect.DeepEqual(record.Identity, identity) {
		t.Fatalf("identity = %#v, want %#v", record.Identity, identity)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	LogSendResult(ctx, Target{Platform: "test"}, Text("notice"), want)
	if len(logs) != 1 {
		t.Fatal("shutdown discarded an already known send failure")
	}
	<-logs
	LogSendResult(t.Context(), Target{}, Text("notice"), context.Canceled)
	if len(logs) != 1 {
		t.Fatal("cancellation log missing")
	}
	record = <-logs
	if record.Level != slog.LevelInfo || record.ResultStatus != events.ResultCanceled || record.Identity.SessionID == nil || *record.Identity.SessionID != "" {
		t.Fatalf("cancellation record = %#v", record)
	}
	LogSendResult(t.Context(), Target{}, Text("notice"), nil)
	if len(logs) != 0 {
		t.Fatal("successful send emitted a failure log")
	}
}
