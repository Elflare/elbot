package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"elbot/internal/chatinfo"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/storage"
)

func TestFinalLLMTakeoverUsesForegroundHookAndReceipt(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "buffered"}[buffered], func(t *testing.T) {
			block := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(block.release) }) })
			f := &fakeLLM{chatBlocks: []fakeLLMBlock{block}, replies: []string{"foreground final"}}
			a := newTestAgent(t, &fakePlatform{}, f, "model", config.ProviderConfig{}, newTestStore(t))
			manager := hook.NewManager()
			var outputEvent hook.Event
			if err := manager.Register(hook.Registration{Point: hook.PointAgentTurnOutputPrepared, Name: "capture", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, error) {
				outputEvent = e
				return e, nil
			})}); err != nil {
				t.Fatal(err)
			}
			a.setTestHookManager(manager)
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
			msg.Sender = mediaSendFunc(func([]delivery.Output) (delivery.Receipt, error) {
				return delivery.Receipt{PlatformMessageIDs: []string{"sent"}, SentMessages: []delivery.SentMessage{{Platform: "qq", ScopeID: "private:1", PlatformMessageID: "sent"}}}, nil
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
			mapped, err := a.store.Messages().FindByPlatformMessage(ctx, "qq", "private:1", "sent")
			if err != nil || mapped.ID != result.MessageID {
				t.Errorf("foreground mapping=%#v/%v", mapped, err)
			}
			if _, err := a.store.Messages().FindByPlatformMessage(ctx, "qq", "cron:takeover", "sent"); !errors.Is(err, storage.ErrNotFound) {
				t.Errorf("background mapping remained: %v", err)
			}
		})
	}
}

func TestConnectedHooksKeepAbsentChatIdentity(t *testing.T) {
	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	var events []hook.Event
	for _, point := range []hook.Point{hook.PointPlatformConnected, hook.PointErrorOccurred} {
		if err := manager.Register(hook.Registration{Point: point, Name: string(point), Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, error) {
			events = append(events, e)
			if e.Point == hook.PointPlatformConnected {
				return e, errors.New("connection hook failed")
			}
			return e, nil
		})}); err != nil {
			t.Fatal(err)
		}
	}
	a.setTestHookManager(manager)
	a.NotifyPlatformConnected(context.Background(), "telegram")
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
	a := &Agent{platform: &fakePlatform{}, actorID: "cli:local", scopeID: "local"}
	ctx := chatinfo.WithInfo(context.Background(), chatinfo.Info{Source: chatinfo.Source{Platform: "cli", ScopeID: "local"}, Identity: chatinfo.Identity{PlatformUserID: "local"}, PlatformMessageID: "incoming", ReplyToMessageID: "replied"})
	e := a.fillHookContext(ctx, hook.Event{})
	if e.Platform.PlatformMessageID != "incoming" || e.Platform.ReplyToMessageID != "replied" || e.Actor.ID != "cli:local" {
		t.Fatalf("event=%#v", e)
	}
}

func TestHookKeepsExplicitFieldsAndDoesNotFillPartialSource(t *testing.T) {
	a := &Agent{platform: &fakePlatform{}, actorID: "cli:local", scopeID: "local"}
	ctx := chatinfo.WithInfo(context.Background(), chatinfo.Info{Source: chatinfo.Source{Platform: "telegram"}})
	e := a.fillHookContext(ctx, hook.Event{})
	if e.Platform.Name != "telegram" || e.Platform.ScopeID != "" || e.Platform.UserID != "" || e.Actor != (hook.ActorContext{}) {
		t.Fatalf("platform-only Info invented identity: %#v", e)
	}
	actor := security.Actor{ID: "qq:1", Platform: "qq", PlatformUserID: "1", Role: security.RoleUser}
	ctx = security.WithActor(ctx, actor)
	explicit := hook.Event{Platform: hook.PlatformContext{Name: "explicit", ScopeID: "explicit-scope", UserID: "explicit-user", PlatformMessageID: "explicit-message", ReplyToMessageID: "explicit-reply"}, Actor: hook.ActorContext{ID: "explicit-actor", Role: "explicit-role", DisplayName: "explicit-name"}}
	e = a.fillHookContext(ctx, explicit)
	if e.Platform != explicit.Platform || e.Actor != explicit.Actor {
		t.Fatalf("explicit fields overwritten: %#v", e)
	}
	e = a.fillHookContext(security.WithActor(context.Background(), actor), hook.Event{})
	if e.Actor.ID != actor.ID || e.Actor.Role != string(actor.Role) || e.Platform.ScopeID != "" {
		t.Fatalf("explicit security actor lost: %#v", e)
	}
}
