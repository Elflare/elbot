package agent

import (
	"strings"
	"testing"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/security"
	"elbot/internal/tool"
)

func TestBackgroundTaskAndSkillsShareSessionRules(t *testing.T) {
	for _, kind := range []background.Kind{background.KindCron, background.KindElnis} {
		t.Run(string(kind), func(t *testing.T) {
			registry := tool.NewRegistry()
			for _, name := range []string{"alpha", "beta"} {
				_ = registry.Register(agentDetailTool{name: name, source: tool.SourceSkillAgent, detail: "#skill " + name + " - task", format: "elyph"})
			}
			f := &fakeLLM{replies: []string{"done", "done", "done"}}
			a := newTestAgent(t, &fakePlatform{}, f, "model", config.ProviderConfig{}, newTestStore(t), func(cfg *testAgentOptions) { cfg.ToolRegistry = registry; cfg.SandboxRoot = t.TempDir() })
			req := background.RunRequest{Kind: kind, Name: "rules", Platform: "cli", Actor: contextinfo.Actor{ID: "cli:local", Role: contextinfo.RoleSuperadmin}, Prompt: "#task main - work", NeedsElyphRules: true, ToolListNames: []string{"alpha", "beta"}}
			first, err := a.RunBackground(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			req.SessionID = first.SessionID
			if _, err := a.RunBackground(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			req.SessionID = ""
			third, err := a.RunBackground(t.Context(), req)
			if err != nil || third.SessionID == first.SessionID {
				t.Fatalf("new session: %+v %v", third, err)
			}
			requests := f.chatRequests()
			if len(requests) != 3 {
				t.Fatalf("requests=%d", len(requests))
			}
			for i, request := range requests {
				text := llm.SegmentsContentText(request.Messages[len(request.Messages)-1].Segments)
				want := 1
				if i == 1 {
					want = 0
				}
				if strings.Count(text, "ELyph 规则：") != want || !strings.Contains(text, "#task main") {
					t.Fatalf("request %d: %s", i, text)
				}
				if i != 1 && (!strings.Contains(text, "#skill alpha") || !strings.Contains(text, "#skill beta")) {
					t.Fatalf("lost preloaded skills: %s", text)
				}
			}
		})
	}
}

func TestSkillDirectiveAndDiscoveryShareRules(t *testing.T) {
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(agentDetailTool{name: "alpha", source: tool.SourceSkillAgent, detail: "#skill alpha - first", format: "elyph"})
	_ = registry.Register(agentDetailTool{name: "beta", source: tool.SourceSkillAgent, detail: "#skill beta - second", format: "elyph"})
	f := &fakeLLM{chunks: [][]chatcompletions.Chunk{
		{{ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "discover", Name: "discover_tool", Args: `{"name":"beta"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "done"}},
	}}
	a := newTestAgent(t, &fakePlatform{}, f, "model", config.ProviderConfig{}, newTestStore(t), func(cfg *testAgentOptions) {
		cfg.ToolRegistry = registry
		cfg.SecurityPolicy = security.NewPolicy("low", "critical", nil)
	})
	if err := a.HandleMessage(t.Context(), "do this @skill:alpha"); err != nil {
		t.Fatal(err)
	}
	requests := f.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	if strings.Count(chatRequestText(requests[1]), "ELyph 规则：") != 1 {
		t.Fatalf("rules duplicated or missing: %s", chatRequestText(requests[1]))
	}
	last := llm.SegmentsContentText(requests[1].Messages[len(requests[1].Messages)-1].Segments)
	if strings.Contains(last, "ELyph 规则：") || !strings.Contains(last, "#skill beta") {
		t.Fatalf("discovery: %s", last)
	}
}
