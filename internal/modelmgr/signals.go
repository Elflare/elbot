package modelmgr

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/signal"
	"fmt"
	"log/slog"
	"time"
)

// ModelRetryingEvent uses the complete operation's context across attempts.
type ModelRetryingEvent struct {
	// CallContext retains the complete operation's lifetime for transient UI
	// notifications. The fact itself must still be logged after cancellation.
	CallContext context.Context
	// Identity is captured by the publishing source from this provider call;
	// nil fields are absent, and published pointers are read-only snapshots.
	Identity events.LogIdentity
	// Provider identifies the configured provider that owns the retrying client.
	Provider string
	// Retry retains the common retry loop's attempt result and original error.
	Retry llm.RetryEvent
}

// ObserveModelRetrying 只交付订阅连接；发布与关闭信号归模型总管。
func (s *ModelManager) ObserveModelRetrying(handler func(context.Context, ModelRetryingEvent), options signal.ConnectOptions) (*signal.Connection, error) {
	return s.retrying.Observe(handler, options)
}

func (s *ModelManager) connectRetrySignals() error {
	connection, err := s.retrying.Observe(logModelRetry, signal.ConnectOptions{Shutdown: signal.Drain})
	if err != nil {
		return err
	}
	s.connections = append(s.connections, connection)
	connection, err = s.retrying.Observe(func(_ context.Context, e ModelRetryingEvent) {
		ctx := e.CallContext
		if ctx.Err() != nil {
			return
		}
		text := modelRetryText(e.Provider, e.Retry)
		if text == "" {
			return
		}
		events.Notify(ctx, events.NewNotification(ctx, slog.LevelWarn, text))

	}, signal.ConnectOptions{Shutdown: signal.CancelPending})
	if err != nil {
		return err
	}
	s.connections = append(s.connections, connection)
	return nil
}

func modelRetryText(provider string, event llm.RetryEvent) string {
	if event.Err == nil {
		return ""
	}
	if provider != "" {
		return fmt.Sprintf("LLM retry %d/%d · %s · %s 后: %v", event.Attempt, event.MaxRetries, provider, event.Delay.Round(time.Millisecond), event.Err)
	}
	return fmt.Sprintf("LLM retry %d/%d · %s 后: %v", event.Attempt, event.MaxRetries, event.Delay.Round(time.Millisecond), event.Err)
}
