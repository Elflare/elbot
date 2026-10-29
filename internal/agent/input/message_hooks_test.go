package input

import (
	"context"
	"log/slog"
	"reflect"
	"testing"

	"elbot/internal/agent/hooks"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/security"
	"elbot/internal/testutil/mediatest"
)

type entryProbe struct {
	order []string
	text  string
}

func (p *entryProbe) HandleInput(_ context.Context, text string) error {
	p.order = append(p.order, "input")
	p.text = text
	return nil
}
func (p *entryProbe) Handle(_ context.Context, text string) (bool, error) {
	p.order = append(p.order, "command")
	return false, nil
}
func (p *entryProbe) IsCommand(string) bool { return false }
func (p *entryProbe) SendChat(context.Context, string) error {
	p.order = append(p.order, "chat")
	return nil
}
func (p *entryProbe) SendOutputs(context.Context, []delivery.Output) (events.LogResult, error) {
	p.order = append(p.order, "outputs")
	return events.LogResult{}, nil
}
func (p *entryProbe) Notify(context.Context, slog.Level, string) {
	p.order = append(p.order, "failure")
}

func hookMessageEntry(t *testing.T, p *entryProbe, points hooks.Points) *MessageHandler {
	t.Helper()
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local", Permission: security.DefaultPermission()})
	bridge := hooks.NewHookBridge(hooks.HookBridgeDeps{Identity: identity})
	if _, err := bridge.Register(points); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close(context.Background()) })
	return NewMessageHandler(MessageHandlerDeps{Identity: identity, Hooks: bridge, Commands: p, Input: p, Output: p})
}

func TestInboundCallbackOutputPrecedesConsumeAndDispatch(t *testing.T) {
	for _, consume := range []bool{false, true} {
		t.Run(map[bool]string{false: "continue", true: "consume"}[consume], func(t *testing.T) {
			p := &entryProbe{}
			h := hookMessageEntry(t, p, hooks.Points{
				HasRoute: func(context.Context, hooks.Facts, agentshare.Message) bool {
					p.order = append(p.order, "route?")
					return false
				},
				PlatformMessageReceived: func(_ context.Context, facts hooks.Facts, message agentshare.Message) (agentshare.PlatformMessageReceivedResult, events.LogResult, error) {
					p.order = append(p.order, "received")
					if !facts.Woken {
						t.Fatal("source-free input should be awake")
					}
					return agentshare.PlatformMessageReceivedResult{Segments: llm.TextSegments("edited"), Consume: consume, Outputs: []delivery.Output{delivery.Text("extra")}}, events.LogResult{}, nil
				},
			})
			if err := h.HandleMessage(t.Context(), "original"); err != nil {
				t.Fatal(err)
			}
			want := []string{"route?", "received", "outputs"}
			if !consume {
				want = append(want, "command", "input")
			}
			if !reflect.DeepEqual(p.order, want) {
				t.Fatalf("order=%v want=%v", p.order, want)
			}
			if !consume && p.text != "edited" {
				t.Fatal(p.text)
			}
		})
	}
}

func TestWaitingInputPreparesMediaWithoutRecomputingWakeup(t *testing.T) {
	p := &entryProbe{}
	resolver := &inboundMediaResolver{}
	h := hookMessageEntry(t, p, hooks.Points{
		HasRoute: func(_ context.Context, facts hooks.Facts, _ agentshare.Message) bool {
			if facts.Woken || resolver.calls != 0 {
				t.Fatal("route probe occurred after media or changed wakeup")
			}
			return true
		},
		PlatformMessageReceived: func(_ context.Context, facts hooks.Facts, message agentshare.Message) (agentshare.PlatformMessageReceivedResult, events.LogResult, error) {
			if facts.Woken || resolver.calls != 1 || len(message.Segments) != 1 || !media.ValidID(message.Segments[0].MediaID) {
				t.Fatalf("facts=%+v message=%+v calls=%d", facts, message, resolver.calls)
			}
			return agentshare.PlatformMessageReceivedResult{Segments: llm.TextSegments("wake edited")}, events.LogResult{}, nil
		},
	})
	store := newTestStore(t)
	root := t.TempDir()
	h.media = mediatest.New(t, media.MediaCenterDeps{Store: store}, root)
	ctx := agentshare.WithMessageContext(contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "telegram", ConversationKind: contextinfo.ConversationGroup}}), agentshare.MessageContext{

		TriggerKeywords: []string{"wake"}, ResolveMedia: resolver.ResolveMedia,
		Segments: []agentshare.MessageSegment{{Type: "image", Media: media.PlatformMedia{Type: "image", PlatformFileID: "image"}}},
	})
	if err := h.HandleMessage(ctx, "original"); err != nil {
		t.Fatal(err)
	}
	if len(p.order) != 0 {
		t.Fatalf("rewritten wakeup unexpectedly entered Agent: %v", p.order)
	}
}

func TestCancelRouteBeforeAndAfterMessageRewrite(t *testing.T) {
	for _, rawCancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "rewritten", true: "raw"}[rawCancel], func(t *testing.T) {
			p := &entryProbe{}
			h := hookMessageEntry(t, p, hooks.Points{
				CancelRoute: func(_ context.Context, _ hooks.Facts, message agentshare.Message) bool {
					p.order = append(p.order, "cancel")
					if llm.SegmentsTextOnly(message.Segments) != "/cancel" {
						t.Fatal(message)
					}
					return true
				},
				PlatformMessageReceived: func(_ context.Context, _ hooks.Facts, _ agentshare.Message) (agentshare.PlatformMessageReceivedResult, events.LogResult, error) {
					p.order = append(p.order, "received")
					return agentshare.PlatformMessageReceivedResult{Segments: llm.TextSegments("/cancel")}, events.LogResult{}, nil
				},
			})
			text := "original"
			want := []string{"received", "cancel", "chat"}
			if rawCancel {
				text = "/cancel"
				want = []string{"cancel", "chat"}
			}
			if err := h.HandleMessage(t.Context(), text); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(p.order, want) {
				t.Fatalf("order=%v want=%v", p.order, want)
			}
		})
	}
}
