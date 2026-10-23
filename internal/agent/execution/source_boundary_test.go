package execution

import (
	"context"
	"errors"

	"testing"

	agenthooks "elbot/internal/agent/hooks"
	agentshare "elbot/internal/agent/share"

	"elbot/internal/contextinfo"

	globalevents "elbot/internal/events"
	"elbot/internal/hook"
)

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
	_, _, _ = bridge.Run(context.Background(), hook.Event{Point: hook.PointPlatformConnected, Platform: hook.PlatformContext{Name: "telegram"}})
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
	bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{Identity: agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local"})})
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
	bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{Identity: agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local"})})
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
