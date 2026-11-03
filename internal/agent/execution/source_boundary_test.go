package execution

import (
	"context"
	agenthooks "elbot/internal/agent/hooks"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/security"
	"testing"
)

func TestHookCallbackReceivesPublicFactsWithoutInventingSource(t *testing.T) {
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: "cli", Permission: security.DefaultPermission()})
	for _, tc := range []struct {
		name       string
		ctx        context.Context
		wantActor  string
		wantSource contextinfo.Source
	}{
		{"absent", t.Context(), "", contextinfo.Source{}},
		{"platform only", contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "telegram"}}), "", contextinfo.Source{Platform: "telegram"}},
		{"association only", contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli"}, Identity: contextinfo.Identity{}}), "", contextinfo.Source{Platform: "cli"}},
		{"explicit actor", contextinfo.WithActor(t.Context(), contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{Platform: "cli", ID: "trusted"}}), "trusted", contextinfo.Source{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridge := agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{Identity: identity})
			called := false
			_, err := bridge.Register(agenthooks.Points{AgentOutputPrepared: func(ctx context.Context, facts agenthooks.Facts, text string) (string, error) {
				called = true
				conversation, _ := contextinfo.ConversationFromContext(ctx)
				if facts.Actor.ID != tc.wantActor || conversation.Source != tc.wantSource {
					t.Fatalf("facts=%+v conversation=%+v", facts, conversation)
				}
				return text, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bridge.AgentOutputPrepared(tc.ctx, "text"); err != nil {
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
