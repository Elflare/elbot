package agent

import (
	"context"
	"testing"

	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func connectTestForeground(t *testing.T, sessions *session.Service, adopt func(context.Context, *storage.Session, *session.Binding)) {
	t.Helper()
	c, err := sessions.ForegroundActivated().Observe(func(ctx context.Context, event session.ForegroundActivatedEvent) error {
		adopt(ctx, &event.Session, event.Binding)
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Disconnect)
}
