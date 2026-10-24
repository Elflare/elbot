package execution

import (
	"context"
	agenthooks "elbot/internal/agent/hooks"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"testing"
)

func TestHookCallbackReceivesPublicFactsWithoutInventingSource(t *testing.T) {
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local"})
	for _, tc := range []struct {
		name       string
		ctx        context.Context
		wantActor  string
		wantSource contextinfo.Source
	}{
		{"absent", t.Context(), "", contextinfo.Source{}},
		{"platform only", contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "telegram"}}), "", contextinfo.Source{Platform: "telegram"}},
		{"explicit actor", contextinfo.WithActor(t.Context(), contextinfo.Actor{ID: "trusted"}), "trusted", contextinfo.Source{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{Identity: identity})
			called := false
			_, err := bridge.Register(agenthooks.Points{AgentOutputPrepared: func(ctx context.Context, facts agenthooks.Facts, text string) (string, events.LogResult, error) {
				called = true
				conversation, _ := contextinfo.ConversationFromContext(ctx)
				if facts.Actor.ID != tc.wantActor || conversation.Source != tc.wantSource {
					t.Fatalf("facts=%+v conversation=%+v", facts, conversation)
				}
				return text, events.LogResult{}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := bridge.AgentOutputPrepared(tc.ctx, "text"); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("callback not invoked")
			}
			if err := bridge.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
