package agent

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"

	"elbot/internal/contextinfo"
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
			var notices []events.Notification
			connection, err := events.NotificationRequested.Observe(func(_ context.Context, notice events.Notification) error {
				notices = append(notices, notice)
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = connection.Close(context.Background()) })
			ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{ID: "cli:local", Role: contextinfo.RoleSuperadmin})
			scope := session.Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
			if state == "after-new" {
				if _, err := m.sessions.GetOrCreateCurrent(ctx, scope, "previous conversation"); err != nil {
					t.Fatal(err)
				}
				if err := m.HandleMessage(ctx, MessageRequest{Text: "-new"}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := deps.Store.Sessions().List(ctx, storage.ListSessionsRequest{IncludeAllPlatforms: true, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"-pin", "-unpin", "-archive", "-archive --confirm", "-unarchive", "-rename title", "-messages", "-compact", "-rollback", "-rollback 1", "-stop", "/pin", "/compact"} {
				t.Run(text, func(t *testing.T) {
					notices = nil
					if err := m.HandleMessage(ctx, MessageRequest{Text: text}); err != nil {
						t.Fatalf("command returned an error: %v", err)
					}
					if len(notices) != 1 || notices[0].Text != "还未开始对话" || notices[0].Level != slog.LevelInfo {
						t.Fatalf("expected a normal empty-conversation notice, got %+v", notices)
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
					notices = nil
					if err := m.HandleMessage(ctx, MessageRequest{Text: text}); !errors.Is(err, storage.ErrNotFound) {
						t.Fatalf("explicit missing target error = %v", err)
					}
					if len(notices) != 1 || notices[0].Level != slog.LevelError {
						t.Fatalf("expected target failure, got %+v", notices)
					}
				})
			}
		})
	}
}
