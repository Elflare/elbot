package modelmgr

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/signal"
)

type retryClient struct {
	testClient
	notify func(context.Context, llm.RetryEvent)
}

func (c *retryClient) SetRetryNotifier(fn func(context.Context, llm.RetryEvent)) { c.notify = fn }

func TestRetryLogsWithoutNotificationSubscriber(t *testing.T) {
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Connect(func(_ context.Context, record events.LogRecord) error {
		records = append(records, record)
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	opts := testOptions()
	client := &retryClient{}
	opts.Clients["p"] = client
	service, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	ctx, cancel := context.WithCancel(contextinfo.WithExecution(context.Background(), contextinfo.Execution{RequestID: "retry-request"}))
	cancel()
	client.notify(ctx, llm.RetryEvent{})
	if len(records) != 1 || records[0].Name != "model_retry" || records[0].Level != slog.LevelWarn {
		t.Fatalf("retry facts: %+v", records)
	}
	found := false
	for _, field := range records[0].Fields {
		if field.Key == "request_id" && field.Value.String() == "retry-request" {
			found = true
		}
	}
	if !found {
		t.Fatal("retry lost actual call identity")
	}
	client.notify(ctx, llm.RetryEvent{Err: fmt.Errorf("wrapped: %w", context.Canceled)})
	if len(records) != 2 || records[1].Level != slog.LevelInfo {
		t.Fatal("wrapped cancellation treated as retry failure")
	}
	_ = service.Close()
	client.notify(ctx, llm.RetryEvent{})
	if len(records) != 2 {
		t.Fatal("retry subscription survived close")
	}
}
