package modelmgr

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/signal"
)

type retryClient struct {
	testClient
	notify func(context.Context, llm.RetryEvent)
}

func TestRetryNotificationOwnedByModelManager(t *testing.T) {
	notices := make(chan events.Notification, 2)
	connection, err := events.NotificationRequested.Observe(func(_ context.Context, n events.Notification) error {
		notices <- n
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	service := newTestService(t, testOptions())
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli", ChatID: "original"}})
	event := ModelRetryingEvent{CallContext: ctx, Provider: "p", Retry: llm.RetryEvent{Attempt: 1, MaxRetries: 2, Delay: time.Second, Err: fmt.Errorf("busy")}}
	if err := service.retrying.Publish(context.WithoutCancel(ctx), event); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-notices:
		if n.Origin == nil || n.Origin.Source.ChatID != "original" || n.Level != slog.LevelWarn || n.Text != "LLM retry 1/2 · p · 1s 后: busy" {
			t.Fatalf("notice=%+v", n)
		}
	case <-time.After(time.Second):
		t.Fatal("model manager did not publish notification without Agent")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	event.CallContext = canceled
	if err := service.retrying.Publish(context.WithoutCancel(ctx), event); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 0 {
		t.Fatal("canceled call published notification")
	}
}

func (c *retryClient) SetRetryNotifier(fn func(context.Context, llm.RetryEvent)) { c.notify = fn }

func TestRetryLogsWithoutNotificationSubscriber(t *testing.T) {
	received := make(chan events.LogRecord, 2)
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		received <- record
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	opts := testOptions()
	client := &retryClient{}
	opts.Clients["p"] = client
	service, err := buildTestManager(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	ctx, cancel := context.WithCancel(contextinfo.WithExecution(context.Background(), contextinfo.Execution{RequestID: "retry-request"}))
	cancel()
	client.notify(ctx, llm.RetryEvent{ResultStatus: events.ResultFailed})
	records := []events.LogRecord{<-received}
	if len(records) != 1 || records[0].EventName != "model_retry" || records[0].Level != slog.LevelWarn || records[0].ResultStatus != events.ResultFailed {
		t.Fatalf("retry facts: %+v", records)
	}
	if records[0].Identity.RequestID == nil || *records[0].Identity.RequestID != "retry-request" {
		t.Fatal("retry lost actual call identity")
	}
	client.notify(ctx, llm.RetryEvent{Err: fmt.Errorf("wrapped: %w", context.Canceled), ResultStatus: events.ResultCanceled})
	records = append(records, <-received)
	if len(records) != 2 || records[1].Level != slog.LevelInfo {
		t.Fatal("wrapped cancellation treated as retry failure")
	}
	_ = service.Close(context.Background())
	client.notify(ctx, llm.RetryEvent{})
	if len(received) != 0 {
		t.Fatal("retry subscription survived close")
	}
}
