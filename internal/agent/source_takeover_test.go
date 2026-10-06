package agent

import (
	"context"
	"elbot/internal/llm/chatcompletions"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/tool"
)

func TestLocalCLITakeoverDeliversFinalOutput(t *testing.T) {
	p := &fakePlatform{}
	started, release := make(chan struct{}), make(chan struct{})
	f := &fakeLLM{chunks: [][]chatcompletions.Chunk{
		{{ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "slow-call", Name: "slow", Args: `{}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "foreground final"}},
	}}

	registry := tool.NewRegistry()
	_ = registry.Register(slowTool{started: started, release: release})
	_ = registry.Register(tool.NewDiscoverTool(registry))
	a := newTestAgent(t, p, f, "model", config.ProviderConfig{}, newTestStore(t), func(cfg *testAgentOptions) {
		cfg.ToolRegistry = registry
	})
	a.output.dispatcher.RegisterPlatformSender("qq", p)
	done := startTakeoverTest(a)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("tool did not start")
	}
	ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: "cli", ScopeID: "local", ConversationKind: contextinfo.ConversationUnknown, ConversationID: "local"},
		Identity: contextinfo.Identity{ActorID: "cli:local", PlatformUserID: "local"},
	})
	err := a.HandleMessage(ctx, "/resume "+f.chatRequests()[0].SessionID)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	result := awaitTakeover(t, done)
	if !result.TakenOver || !strings.Contains(p.out.String(), "foreground final") {
		t.Fatalf("local CLI takeover result=%#v output=%s", result, p.out.String())
	}
}
