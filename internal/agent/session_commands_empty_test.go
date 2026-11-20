package agent

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/operationresult"
	platformshare "elbot/internal/platform/share"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/testutil"
	"reflect"
	"strings"
	"testing"
)

type capturedCommandReply struct {
	result  command.Result
	failure error
	outputs []delivery.Output
}
type commandCapturePlatform struct {
	PlatformCapabilities
	replies []capturedCommandReply
}

func (p *commandCapturePlatform) SendCommandResult(ctx context.Context, result command.Result, failure error) (delivery.Receipt, error) {
	outputs := platformshare.CommandResultOutputs(result, failure)
	p.replies = append(p.replies, capturedCommandReply{result: result, failure: failure, outputs: outputs})
	return p.PlatformCapabilities.SendCommandResult(ctx, result, failure)
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
				if result, err := m.sessions.GetOrCreateCurrent(ctx, scope, "previous conversation"); err != nil || result.Status != operationresult.Success {
					t.Fatalf("create previous session: %+v %v", result, err)
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
					if err != nil || len(platform.replies) != prior+1 {
						t.Fatalf("error=%v", err)
					}
					reply := platform.replies[prior]
					if reply.result.Status == operationresult.Success || delivery.ContentBlocksText(reply.result.Blocks) == "" || reply.failure != nil || len(reply.outputs) != 1 || delivery.FallbackText(reply.outputs[0]) != delivery.ContentBlocksText(reply.result.Blocks) || delivery.FallbackText(reply.outputs[0]) == "nil" {
						t.Fatalf("replies=%+v", platform.replies)
					}
					current, _ := m.sessions.CurrentBound(ctx, scope)
					if current.Status != operationresult.NoCurrentSession {
						t.Fatal("created a conversation")
					}
					after, err := deps.Store.Sessions().List(ctx, storage.ListSessionsRequest{IncludeAllPlatforms: true, Limit: 100})
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("sessions changed: before=%+v after=%+v err=%v", before, after, err)
					}
				})
			}
			for _, text := range []string{"-pin missing-session", "-fork missing-message"} {
				prior := len(platform.replies)
				err := m.HandleMessage(ctx, MessageRequest{Text: text})
				if err != nil || len(platform.replies) != prior+1 {
					t.Fatalf("error=%v replies=%+v", err, platform.replies)
				}
				reply := platform.replies[prior]
				if len(reply.outputs) != 1 || delivery.FallbackText(reply.outputs[0]) != delivery.ContentBlocksText(reply.result.Blocks) || delivery.FallbackText(reply.outputs[0]) == "nil" {
					t.Fatalf("unexpected command output: %+v", reply.outputs)
				}
				if text == "-fork missing-message" {
					if reply.result.Status != operationresult.NoCurrentSession {
						t.Fatalf("result=%+v", reply.result)
					}
					continue
				}
				if reply.result.Status != operationresult.NotFound {
					t.Fatalf("result=%+v", reply.result)
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

func TestResumeCommandOnlySwitchesCallingView(t *testing.T) {
	deps := isolatedModuleDeps(t)
	platform := &commandCapturePlatform{PlatformCapabilities: deps.Platform}
	deps.Platform = platform
	cfg := isolatedAgentConfig()
	cfg.Commands.Prefixes = []string{"/"}
	m, err := NewSupervisor(t.Context(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	base := testutil.WithCLIConversation(t.Context())
	info, _ := contextinfo.ConversationFromContext(base)
	a := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}, Platform: "cli", ChatType: contextinfo.ChatPrivate, ChatID: "local", ViewID: "A"}
	b := a
	b.ViewID = "B"
	history, err := m.sessions.Create(base, a, session.CreateRequest{Title: "history"})
	if err != nil || history.Status != operationresult.Success {
		t.Fatal(history, err)
	}
	running, err := m.sessions.Create(base, a, session.CreateRequest{Title: "current A"})
	if err != nil || running.Status != operationresult.Success {
		t.Fatal(running, err)
	}
	before, err := m.sessions.CurrentBound(base, a)
	if err != nil || before.Status != operationresult.Success {
		t.Fatal(before, err)
	}
	info.ViewID = "B"
	ctx := contextinfo.WithConversation(base, info)
	for _, id := range []string{running.Data.ID, history.Data.ID} {
		prior := len(platform.replies)
		if err := m.HandleMessage(ctx, MessageRequest{Text: "/resume " + id}); err != nil {
			t.Fatal(err)
		}
		if len(platform.replies) != prior+1 || platform.replies[prior].result.Status != operationresult.Success {
			t.Fatalf("/resume %s: %+v", id, platform.replies)
		}
		left, err := m.sessions.CurrentBound(base, a)
		if err != nil || left.Status != operationresult.Success || left.Data.Binding != before.Data.Binding || !left.Data.Binding.Valid() {
			t.Fatalf("A changed: %+v %v", left, err)
		}
		right, err := m.sessions.GetOrCreateCurrent(ctx, b, "next input")
		if err != nil || right.Status != operationresult.Success || right.Data.ID != id {
			t.Fatalf("B current: %+v %v", right, err)
		}
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
	text := platformshare.FormatCommandResult(reply.result)
	if reply.result.Kind != command.ResultKindHelp || !strings.Contains(text, "!!stop [request_id|number]") || !strings.Contains(text, "!!help <command>") || strings.Contains(text, "/help") {
		t.Fatalf("reply=%+v formatted=%q", reply, text)
	}
}
