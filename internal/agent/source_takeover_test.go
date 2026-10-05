package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"elbot/internal/chatinfo"
	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/tool"
)

func TestLocalCLITakeoverDeliversFinalOutput(t *testing.T) {
	p := &fakePlatform{}
	started, release := make(chan struct{}), make(chan struct{})
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "slow-call", Name: "slow", Args: `{}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "foreground final"}},
	}}
	a := newTestAgent(t, p, f, "model", config.ProviderConfig{}, newTestStore(t))
	a.output.dispatcher.RegisterPlatformSender("qq", p)
	registry := tool.NewRegistry()
	_ = registry.Register(slowTool{started: started, release: release})
	_ = registry.Register(tool.NewDiscoverTool(registry))
	a.SetToolRuntime(registry, nil)
	done := startTakeoverTest(a)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("tool did not start")
	}
	ctx := chatinfo.WithInfo(context.Background(), chatinfo.Info{
		Source:   chatinfo.Source{Platform: "cli", ScopeID: "local", ConversationKind: chatinfo.ConversationUnknown, ConversationID: "local"},
		Identity: chatinfo.Identity{ActorID: "cli:local", PlatformUserID: "local"},
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
