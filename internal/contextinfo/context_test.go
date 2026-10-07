package contextinfo

import (
	"context"
	"sync"
	"testing"
)

func TestPerMessageSnapshots(t *testing.T) {
	var wg sync.WaitGroup
	for _, platform := range []string{"telegram", "qqonebot"} {
		for _, user := range []string{"first", "second"} {
			wg.Go(func() {
				info := Conversation{Source: Source{Platform: platform, ScopeID: "group:1"}, Identity: Identity{PlatformUserID: user}}
				ctx := WithConversation(context.Background(), info)
				info.Identity.PlatformUserID = "changed"
				child, cancel := context.WithCancel(ctx)
				cancel()
				got, ok := ConversationFromContext(context.WithoutCancel(child))
				if !ok || got.Source.Platform != platform || got.Identity.PlatformUserID != user {
					t.Errorf("snapshot = %+v, %v", got, ok)
				}
			})
		}
	}
	wg.Wait()
}

func TestDomainsCanBeMissingAndClearedIndependently(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	ctx := WithConversation(base, Conversation{Source: Source{Platform: "qq"}})
	ctx = WithActor(ctx, Actor{ID: "qq:1", Role: RoleUser})
	ctx = WithExecution(ctx, Execution{SessionID: "session", RunID: "run"})
	ctx = WithModel(ctx, Model{Provider: "fixed", Model: "m", APIType: "chat"})
	cancel()
	cleared := WithoutConversation(WithoutActor(WithoutExecution(WithoutModel(ctx))))
	if _, ok := ConversationFromContext(cleared); ok {
		t.Fatal("inherited conversation survived explicit removal")
	}
	if _, ok := ActorFromContext(cleared); ok {
		t.Fatal("inherited actor survived explicit removal")
	}
	if _, ok := ExecutionFromContext(cleared); ok {
		t.Fatal("inherited execution survived explicit removal")
	}
	if _, ok := ModelFromContext(cleared); ok {
		t.Fatal("inherited model survived explicit removal")
	}
	if cleared.Err() != context.Canceled {
		t.Fatal("fact replacement lost cancellation")
	}
	if actor, ok := ActorFromContext(ctx); !ok || actor.ID != "qq:1" {
		t.Fatal("removal changed the parent snapshot")
	}
	conversationOnly := WithoutActor(ctx)
	if model, ok := ModelFromContext(conversationOnly); !ok || model.Provider != "fixed" {
		t.Fatal("clearing one domain changed another")
	}
	if _, ok := ModelFromContext(context.Background()); ok {
		t.Fatal("missing model was guessed")
	}
}
