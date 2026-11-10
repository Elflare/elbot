package agent

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	platformshare "elbot/internal/platform/share"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/testutil"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type capturedCommandReply struct {
	request command.Request
	result  *command.Result
	failure *command.Error
}
type commandCapturePlatform struct {
	PlatformCapabilities
	replies []capturedCommandReply
}

func (p *commandCapturePlatform) SendCommandResult(ctx context.Context, req command.Request, result *command.Result, failure *command.Error) (delivery.Receipt, error) {
	p.replies = append(p.replies, capturedCommandReply{req, result, failure})
	return p.PlatformCapabilities.SendCommandResult(ctx, req, result, failure)
}

func TestSessionCommandsWithoutCurrentConversation(t *testing.T) {
	for _, state := range []string{"fresh", "after-new"} {
		t.Run(state, func(t *testing.T) {
			deps := isolatedModuleDeps(t)
			platform := &commandCapturePlatform{PlatformCapabilities: deps.Platform}
			deps.Platform = platform
			cfg := isolatedAgentConfig()
			cfg.Commands.Prefixes = []string{"-", "/"}
			m, err := NewSupervisor(t.Context(), cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close(context.Background()) })
			notices := []string{}
			connection, err := events.NotificationRequested.Observe(func(_ context.Context, n events.Notification) {
				if n.Text != "barrier" {
					notices = append(notices, n.Text)
				}
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = connection.Close(context.Background()) })
			ctx := testutil.WithCLIConversation(t.Context())
			scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}, Platform: "cli", ChatID: "local", ChatType: "private"}
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
					prior := len(platform.replies)
					err := m.HandleMessage(ctx, MessageRequest{Text: text})
					var failure *command.Error
					if !errors.As(err, &failure) || failure.Kind != command.ErrorRejected || !errors.Is(err, session.ErrNoCurrentSession) {
						t.Fatalf("error=%v", err)
					}
					if len(platform.replies) != prior+1 || platform.replies[prior].failure != failure {
						t.Fatalf("replies=%+v", platform.replies)
					}
					if _, binding, _ := m.sessions.CurrentBound(ctx, scope); binding != nil {
						t.Fatal("created a conversation")
					}
					after, err := deps.Store.Sessions().List(ctx, storage.ListSessionsRequest{IncludeAllPlatforms: true, Limit: 100})
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("sessions changed: before=%+v after=%+v err=%v", before, after, err)
					}
				})
			}
			for _, text := range []string{"-pin missing-session", "-fork missing-message"} {
				err := m.HandleMessage(ctx, MessageRequest{Text: text})
				var failure *command.Error
				if text == "-fork missing-message" {
					if !errors.As(err, &failure) || failure.Code != "no_current_session" || !errors.Is(err, session.ErrNoCurrentSession) {
						t.Fatalf("error=%v", err)
					}
					continue
				}
				if !errors.As(err, &failure) || failure.Code != command.CodeNotFound || !errors.Is(err, storage.ErrNotFound) {
					t.Fatalf("error=%v", err)
				}
			}
			if _, err := events.NotificationRequested.Emit(ctx, events.Notification{Text: "barrier"}); err != nil {
				t.Fatal(err)
			}
			if len(notices) != 0 {
				t.Fatalf("command replies leaked into notifications: %v", notices)
			}
		})
	}
}

func TestCommandTextWiringUsesConfigurationFirstPrefix(t *testing.T) {
	deps := isolatedModuleDeps(t)
	platform := &commandCapturePlatform{PlatformCapabilities: deps.Platform}
	deps.Platform = platform
	cfg := isolatedAgentConfig()
	cfg.Commands.Prefixes = []string{"!", "!!"}
	m, err := NewSupervisor(t.Context(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	if err := m.HandleMessage(testutil.WithCLIConversation(t.Context()), MessageRequest{Text: "!!help"}); err != nil {
		t.Fatal(err)
	}
	reply := platform.replies[0]
	text := platformshare.FormatCommandResult(reply.request, reply.result)
	if reply.request.Name != "help" || reply.result.Kind != command.ResultKindHelp || !strings.Contains(text, "!!stop [request_id|number]") || !strings.Contains(text, "!!help <command>") || strings.Contains(text, "/help") {
		t.Fatalf("reply=%+v formatted=%q", reply, text)
	}
}
