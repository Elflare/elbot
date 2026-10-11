package delivery

import (
	"context"
	"errors"
	"testing"

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
	source := events.LogResult{ResultStatus: events.ResultFailed, Content: "one recipient sent, one failed"}
	sender := &independentChatSender{fakeSender: fakeSender{err: want}, logResult: source}
	receipt, result, err := NewManager(sender).SendTo(t.Context(), Target{Platform: "test"}, []Output{Text("notice")})
	if !errors.Is(err, want) || len(receipt.PlatformMessageIDs) != 1 || result != source || len(logs) != 1 {
		t.Fatal(receipt, result, err, len(logs))
	}
	<-logs
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	LogSendResult(ctx, Target{Platform: "test"}, Text("notice"), source)
	if len(logs) != 1 {
		t.Fatal("shutdown discarded an already known send failure")
	}
}
