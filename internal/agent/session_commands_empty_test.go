package agent

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/testutil"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

func TestSessionCommandsWithoutCurrentConversation(t *testing.T) {
	for _, state := range []string{"fresh", "after-new"} {
		t.Run(state, func(t *testing.T) {
			deps := isolatedModuleDeps(t)
			cfg := isolatedAgentConfig()
			cfg.Commands.Prefixes = []string{"-", "/"}
			m, err := NewSupervisor(t.Context(), cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close(context.Background()) })
			notices := make(chan events.Notification, 32)
			connection, err := events.NotificationRequested.Observe(func(_ context.Context, notice events.Notification) error {
				notices <- notice
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = connection.Close(context.Background()) })
			ctx := testutil.WithCLIConversation(t.Context())
			scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
			if state == "after-new" {
				if _, err := m.sessions.GetOrCreateCurrent(ctx, scope, "previous conversation"); err != nil {
					t.Fatal(err)
				}
				if err := m.HandleMessage(ctx, MessageRequest{Text: "-new"}); err != nil {
					t.Fatal(err)
				}
				awaitNotification(t, notices)
			}
			before, err := deps.Store.Sessions().List(ctx, storage.ListSessionsRequest{IncludeAllPlatforms: true, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"-pin", "-unpin", "-archive", "-archive --confirm", "-unarchive", "-rename title", "-messages", "-compact", "-rollback", "-rollback 1", "-stop", "/pin", "/compact"} {
				t.Run(text, func(t *testing.T) {
					if err := m.HandleMessage(ctx, MessageRequest{Text: text}); err != nil {
						t.Fatalf("command returned an error: %v", err)
					}
					notice := awaitNotification(t, notices)
					if notice.Text != "还未开始对话" || notice.Level != slog.LevelInfo {
						t.Fatalf("expected a normal empty-conversation notice, got %+v", notice)
					}
					if _, binding, _ := m.sessions.CurrentBound(ctx, scope); binding != nil {
						t.Fatal("command created a current conversation")
					}
					after, err := deps.Store.Sessions().List(ctx, storage.ListSessionsRequest{IncludeAllPlatforms: true, Limit: 100})
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("command changed sessions: before=%+v after=%+v err=%v", before, after, err)
					}
				})
			}
			for _, text := range []string{"-pin missing-session", "-fork missing-message"} {
				t.Run(text, func(t *testing.T) {
					if err := m.HandleMessage(ctx, MessageRequest{Text: text}); !errors.Is(err, storage.ErrNotFound) {
						t.Fatalf("explicit missing target error = %v", err)
					}
					notice := awaitNotification(t, notices)
					if notice.Level != slog.LevelWarn {
						t.Fatalf("expected target failure, got %+v", notice)
					}
				})
			}
		})
	}
}

func TestCommandTextWiringUsesConfigurationFirstPrefix(t *testing.T) {
	deps := isolatedModuleDeps(t)
	cfg := isolatedAgentConfig()
	cfg.Commands.Prefixes = []string{"!", "!!"}
	m, err := NewSupervisor(t.Context(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	notices := make(chan events.Notification, 32)
	connection, err := events.NotificationRequested.Observe(func(_ context.Context, notice events.Notification) error {
		notices <- notice
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	ctx := testutil.WithCLIConversation(t.Context())
	if err := m.HandleMessage(ctx, MessageRequest{Text: "!!help"}); err != nil {
		t.Fatal(err)
	}
	notice := awaitNotification(t, notices)
	if !strings.Contains(notice.Text, "!stop [request_id|number]") || strings.Contains(notice.Text, "!!") || strings.Contains(notice.Text, "/help") {
		t.Fatalf("notice=%+v", notice)
	}
}

func awaitNotification(t *testing.T, notices <-chan events.Notification) events.Notification {
	t.Helper()
	select {
	case notice := <-notices:
		return notice
	case <-time.After(5 * time.Second):
		t.Fatal("notification was not delivered")
		return events.Notification{}
	}
}
