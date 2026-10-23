package execution

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	agentinput "elbot/internal/agent/input"
	"elbot/internal/command"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/notification"
	"elbot/internal/session"
	"elbot/internal/signal"
)

func commandNotificationFixture(t *testing.T, receive func(context.Context, events.Notification) error) (*agentFixture, *fakePlatform) {
	t.Helper()
	p := &fakePlatform{}
	a := newTestAgent(t, p, &fakeLLM{models: []string{"alpha"}}, "alpha", config.ProviderConfig{}, newTestStore(t))
	// Replace the fixture's text-only adapter with a receiver that distinguishes
	// notifications from business Chat delivery, retaining the real manager.
	hub, _ := fixtureNotificationHubs.Load(t)
	if err := hub.(*fixtureNotificationHub).manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	m, err := notification.New("cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	s, err := m.AddPlatform("cli")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Connect(func(ctx context.Context, n events.Notification) (events.NotificationReceipt, error) {
		return events.NotificationReceipt{}, receive(ctx, n)
	}, signal.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	return a, p
}

func TestCommandResultsUseNotifications(t *testing.T) {
	for _, text := range []string{"/models", "/log", "/longnotice"} {
		t.Run(text, func(t *testing.T) {
			var notices []events.Notification
			a, p := commandNotificationFixture(t, func(_ context.Context, n events.Notification) error {
				notices = append(notices, n)
				return nil
			})
			long := strings.Repeat("model information\n", 400)
			if err := a.commandDeps.Router.Register(command.NewFunc(command.Info{Name: "longnotice"}, func(context.Context, command.Request) (*command.Result, error) {
				return &command.Result{Content: long}, nil
			})); err != nil {
				t.Fatal(err)
			}
			origin := contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli", ScopeID: "local", ConversationID: "local"}}
			ctx := contextinfo.WithConversation(context.Background(), origin)
			if err := a.HandleMessage(ctx, text); err != nil {
				t.Fatal(err)
			}
			if len(notices) != 1 || notices[0].Text == "" || notices[0].Level != slog.LevelInfo {
				t.Fatalf("notices = %#v", notices)
			}
			if notices[0].Origin == nil || notices[0].Origin.Source != origin.Source {
				t.Fatalf("origin = %#v", notices[0].Origin)
			}
			if text == "/longnotice" && notices[0].Text != long {
				t.Fatal("long command result changed")
			}
			if got := p.out.String(); got != "" {
				t.Fatalf("command result sent as Chat: %q", got)
			}
		})
	}
}

func TestCommandArgumentErrorsUseNotifications(t *testing.T) {
	for _, text := range []string{"/log --warn", "/log --limit", "/audit --event", "/elwisp --limit", "/usage --days", "/fork --bad", "/resume --bad"} {
		t.Run(text, func(t *testing.T) {
			var notices []events.Notification
			a, p := commandNotificationFixture(t, func(ctx context.Context, n events.Notification) error {
				if _, ok := session.BindingFromContext(ctx); ok {
					t.Error("error retained session binding")
				}
				notices = append(notices, n)
				return nil
			})
			origin := contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli", ScopeID: "local", ConversationID: "local"}}
			err := a.HandleMessage(contextinfo.WithConversation(t.Context(), origin), text)
			if err == nil {
				t.Fatal("expected command error")
			}
			if len(notices) != 1 || notices[0].Level != slog.LevelError || !strings.Contains(notices[0].Text, err.Error()) || !strings.HasPrefix(notices[0].Text, "命令 "+strings.Fields(text)[0]+" 执行失败：") {
				t.Fatalf("err=%v notices=%+v", err, notices)
			}
			if notices[0].Origin == nil || notices[0].Origin.Source != origin.Source {
				t.Fatalf("origin=%+v", notices[0].Origin)
			}
			if p.out.String() != "" {
				t.Fatal("error sent as Chat")
			}
		})
	}
}

func TestCommandFailurePreservesNotificationError(t *testing.T) {
	want := errors.New("command failed")
	deliveryErr := errors.New("notification failed")
	a, _ := commandNotificationFixture(t, func(context.Context, events.Notification) error { return deliveryErr })
	e := a.commands.(*agentinput.CommandExecutor)
	if err := a.commandDeps.Router.Register(command.NewFunc(command.Info{Name: "failure"}, func(context.Context, command.Request) (*command.Result, error) { return nil, want })); err != nil {
		t.Fatal(err)
	}
	_, err := e.Handle(contextinfo.WithActor(t.Context(), a.identity.Actor(t.Context())), "/failure")
	if !errors.Is(err, want) || !errors.Is(err, deliveryErr) {
		t.Fatalf("err=%v", err)
	}
}

func TestCommandNotificationSurvivesSessionSwitch(t *testing.T) {
	var notices []events.Notification
	a, p := commandNotificationFixture(t, func(ctx context.Context, n events.Notification) error {
		if _, ok := session.BindingFromContext(ctx); ok {
			t.Error("command result retained a session binding")
		}
		notices = append(notices, n)
		return nil
	})
	ctx := context.Background()
	scope := a.Scope(ctx)
	first, err := a.execution.sessions.Create(ctx, scope, session.CreateRequest{Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"/new", "/resume " + first.ID} {
		if _, err := a.execution.sessions.Create(ctx, scope, session.CreateRequest{Title: "current"}); err != nil {
			t.Fatal(err)
		}
		_, binding, err := a.execution.sessions.CurrentBound(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		before := len(notices)
		if err := a.HandleMessage(ctx, text); err != nil {
			t.Fatal(err)
		}
		if binding.Valid() || len(notices) != before+1 {
			t.Fatalf("%s: binding valid=%v notices=%d", text, binding.Valid(), len(notices))
		}
	}
	if got := p.out.String(); got != "" {
		t.Fatalf("session command sent as Chat: %q", got)
	}
}

type notificationContinuationFunc func(context.Context, command.Continuation) error

func (f notificationContinuationFunc) ContinueCommandInput(ctx context.Context, c command.Continuation) error {
	return f(ctx, c)
}

func TestCommandNotificationPrecedesContinuation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			var sent atomic.Bool
			wantErr := errors.New("notification delivery failed")
			a, _ := commandNotificationFixture(t, func(context.Context, events.Notification) error {
				if fail {
					return wantErr
				}
				sent.Store(true)
				return nil
			})
			deps := a.commandDeps
			continued := false
			deps.Input = notificationContinuationFunc(func(context.Context, command.Continuation) error {
				continued = true
				if !sent.Load() {
					t.Error("continuation started before notification delivery")
				}
				return nil
			})
			if err := a.commandDeps.Router.Register(command.NewFunc(command.Info{Name: "continuetest"}, func(context.Context, command.Request) (*command.Result, error) {
				return &command.Result{Content: "ready", Continuation: &command.Continuation{Text: "next"}}, nil
			})); err != nil {
				t.Fatal(err)
			}
			e := agentinput.NewCommandExecutor(deps)
			handled, err := e.Handle(contextinfo.WithActor(context.Background(), a.identity.Actor(context.Background())), "/continuetest")
			if !handled || continued == fail || (fail && !errors.Is(err, wantErr)) || (!fail && err != nil) {
				t.Fatalf("handled=%v continued=%v err=%v", handled, continued, err)
			}
		})
	}
}
