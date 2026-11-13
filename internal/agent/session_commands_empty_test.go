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
	request command.Request
	result  command.Result
	failure error
	outputs []delivery.Output
}
type commandCapturePlatform struct {
	PlatformCapabilities
	replies []capturedCommandReply
}

func (p *commandCapturePlatform) SendCommandResult(ctx context.Context, req command.Request, result command.Result, failure error) (delivery.Receipt, error) {
	outputs, err := platformshare.CommandResultOutputs(req, result, failure)
	if err != nil {
		return delivery.Receipt{}, err
	}
	p.replies = append(p.replies, capturedCommandReply{request: req, result: result, failure: failure, outputs: outputs})
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
					if reply.result.Status == operationresult.Success || reply.result.Message == "" || reply.failure != nil || len(reply.outputs) != 1 || reply.outputs[0].Text != reply.result.Message || reply.outputs[0].Text == "nil" {
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
				if len(reply.outputs) != 1 || reply.outputs[0].Text != reply.result.Message || reply.outputs[0].Text == "nil" {
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
	text, err := platformshare.FormatCommandResult(reply.request, reply.result)
	if err != nil {
		t.Fatal(err)
	}
	if reply.request.Name != "help" || reply.result.Kind != command.ResultKindHelp || !strings.Contains(text, "!!stop [request_id|number]") || !strings.Contains(text, "!!help <command>") || strings.Contains(text, "/help") {
		t.Fatalf("reply=%+v formatted=%q", reply, text)
	}
}
