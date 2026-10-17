package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/platform"
	"elbot/internal/storage"
)

func TestFinalLLMTakeoverUsesForegroundHookAndReceipt(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "buffered"}[buffered], func(t *testing.T) {
			block := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(block.release) }) })
			f := &fakeLLM{chatBlocks: []fakeLLMBlock{block}, replies: []string{"foreground final"}}

			manager := newTestHookManager()

			var outputEvent hook.Event
			manager.Register(hook.Registration{Point: hook.PointAgentTurnOutputPrepared, Name: "capture", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, globalevents.LogResult, error) {
				outputEvent = e
				return e, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
			})})
			a := newTestAgent(t, &fakePlatform{}, f, "model", config.ProviderConfig{}, newTestStore(t), func(cfg *testAgentOptions) {
				cfg.HookManager = manager
			})
			connectFixtureNotifications(t, "qq", a.output.dispatcher)
			done := startTakeoverTest(a)
			select {
			case <-block.started:
			case <-time.After(3 * time.Second):
				t.Fatal("LLM did not start")
			}
			id := f.chatRequests()[0].SessionID
			ctx := takeoverPrivateContext()
			msg, _ := platform.MessageContextFrom(ctx)
			msg.BufferAssistantOutput = buffered
			msg.Sender = mediaSendFunc(func([]delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
				return delivery.Receipt{PlatformMessageIDs: []string{"sent"}, SentMessages: []delivery.SentMessage{{Platform: "qq", ScopeID: "private:1", PlatformMessageID: "sent"}}}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
			})
			ctx = platform.WithMessageContext(ctx, msg)
			if err := a.HandleMessage(ctx, "/resume "+id); err != nil {
				t.Fatal(err)
			}
			release.Do(func() { close(block.release) })
			result := awaitTakeover(t, done)
			if !result.TakenOver || len(f.chatRequests()) != 1 {
				t.Fatalf("result=%#v requests=%d", result, len(f.chatRequests()))
			}
			if outputEvent.Platform.ScopeID != "private:1" || outputEvent.Actor.ID != "qq:1" {
				t.Errorf("output hook retained background identity: %#v / %#v", outputEvent.Platform, outputEvent.Actor)
			}
			mapped, err := a.message.messages.FindByPlatformMessage(ctx, "qq", "private:1", "sent")
			if err != nil || mapped.ID != result.MessageID {
				t.Errorf("foreground mapping=%#v/%v", mapped, err)
			}
			if _, err := a.message.messages.FindByPlatformMessage(ctx, "qq", "cron:takeover", "sent"); !errors.Is(err, storage.ErrNotFound) {
				t.Errorf("background mapping remained: %v", err)
			}
		})
	}
}

func TestConnectedHooksKeepAbsentChatIdentity(t *testing.T) {
	manager := newTestHookManager()

	var events []hook.Event
	for _, point := range []hook.Point{hook.PointPlatformConnected, hook.PointErrorOccurred} {
		manager.Register(hook.Registration{Point: point, Name: string(point), Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, globalevents.LogResult, error) {
			events = append(events, e)
			if e.Point == hook.PointPlatformConnected {
				return e, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "test source failed"}, errors.New("connection hook failed")
			}
			return e, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "test source completed"}, nil
		})})
	}
	bridge := newTestHookBridge(t, &fakePlatform{}, manager)
	bridge.handlePlatformConnected(context.Background(), "telegram")
	if len(events) != 2 {
		t.Fatalf("events=%#v", events)
	}
	for _, e := range events {
		if e.Platform.Name != "telegram" || e.Platform.ScopeID != "" || e.Platform.UserID != "" || e.Actor != (hook.ActorContext{}) || e.Platform.PlatformMessageID != "" || e.Platform.ReplyToMessageID != "" {
			t.Errorf("invented chat source for %s: %#v / %#v", e.Point, e.Platform, e.Actor)
		}
	}
}

func TestHookReadsPublicInfoWithoutPlatformExtension(t *testing.T) {
	bridge := &HookBridge{identity: &IdentityResolver{platformName: "cli", actorID: "cli:local", scopeID: "local"}}
	ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{
		Source:            contextinfo.Source{Platform: "cli", ScopeID: "local"},
		Identity:          contextinfo.Identity{PlatformUserID: "local"},
		PlatformMessageID: "incoming",
		ReplyToMessageID:  "replied",
	})
	e := bridge.FillContext(ctx, hook.Event{})
	if e.Platform.PlatformMessageID != "incoming" || e.Platform.ReplyToMessageID != "replied" || e.Actor.ID != "cli:local" {
		t.Fatalf("event=%#v", e)
	}
}

func TestHookKeepsExplicitFieldsAndDoesNotFillPartialSource(t *testing.T) {
	bridge := &HookBridge{identity: &IdentityResolver{platformName: "cli", actorID: "cli:local", scopeID: "local"}}
	ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "telegram"}})
	e := bridge.FillContext(ctx, hook.Event{})
	if e.Platform.Name != "telegram" || e.Platform.ScopeID != "" || e.Platform.UserID != "" || e.Actor != (hook.ActorContext{}) {
		t.Fatalf("platform-only Info invented identity: %#v", e)
	}
	actor := contextinfo.Actor{ID: "qq:1", Platform: "qq", PlatformUserID: "1", Role: contextinfo.RoleUser}
	ctx = contextinfo.WithActor(ctx, actor)
	explicit := hook.Event{
		Platform: hook.PlatformContext{Name: "explicit", ScopeID: "explicit-scope", UserID: "explicit-user", PlatformMessageID: "explicit-message", ReplyToMessageID: "explicit-reply"},
		Actor:    hook.ActorContext{ID: "explicit-actor", Role: "explicit-role", DisplayName: "explicit-name"},
	}
	e = bridge.FillContext(ctx, explicit)
	if e.Platform != explicit.Platform || e.Actor != explicit.Actor {
		t.Fatalf("explicit fields overwritten: %#v", e)
	}
	e = bridge.FillContext(contextinfo.WithActor(context.Background(), actor), hook.Event{})
	if e.Actor.ID != actor.ID || e.Actor.Role != string(actor.Role) || e.Platform.ScopeID != "" {
		t.Fatalf("explicit security actor lost: %#v", e)
	}
}
