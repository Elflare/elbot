package qqonebot

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/events"
	"elbot/internal/signal"
)

func TestContextFailureUsesRegisteredPlatformName(t *testing.T) {
	logs := make(chan events.LogRecord, 1)
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.EventName == "platform_context_failed" {
			logs <- record
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	(&Adapter{name: "injected-onebot"}).logContextFailure(t.Context(), errors.New("test context failure"))
	select {
	case record := <-logs:
		if record.Module != "injected-onebot" || record.Module != (&Adapter{name: "injected-onebot"}).Name() || record.ResultStatus != events.ResultFailed {
			t.Fatalf("record=%+v", record)
		}
	default:
		t.Fatal("failure log not emitted")
	}
}
