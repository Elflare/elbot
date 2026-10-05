package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
)

type backgroundBoundaryProbe struct {
	preparedArgumentTool
	assessed, preflight *bool
}

func (p backgroundBoundaryProbe) AssessRisk(context.Context, tool.CallRequest) (tool.RiskAssessment, error) {
	*p.assessed = true
	return tool.RiskAssessment{Level: tool.RiskLow}, nil
}

func (p backgroundBoundaryProbe) PreflightConfirmation(context.Context, tool.CallRequest) error {
	*p.preflight = true
	return nil
}

func TestBackgroundRejectsUndeclaredNativeTool(t *testing.T) {
	ctx := context.Background()
	var executed string
	var assessed, preflight bool
	candidate := backgroundBoundaryProbe{preparedArgumentTool: preparedArgumentTool{arguments: &executed}, assessed: &assessed, preflight: &preflight}
	registry := tool.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "forced", Name: candidate.Name(), Args: "{}"}}}},
		{{DeltaContent: "finished"}},
	}}
	a := newTestAgent(t, &fakePlatform{}, f, "model", config.ProviderConfig{}, newTestStore(t))
	a.SetSandboxRoot(t.TempDir())
	a.SetToolRuntime(registry, nil)
	result, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindElnis, Name: "restricted", Platform: "cli", Actor: security.Actor{ID: "cli:local", Role: security.RoleSuperadmin}, Prompt: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if executed != "" || assessed || preflight {
		t.Fatalf("undeclared tool reached execution/preflight/risk: %q %v %v", executed, preflight, assessed)
	}
	row, err := a.store.Sessions().Get(ctx, result.SessionID)
	if err != nil || row.Mode != "background" {
		t.Fatalf("mode=%v err=%v", row, err)
	}
}

func TestBackgroundRelativeFilesUseElwispSandbox(t *testing.T) {
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "write", Name: "shell", Args: `{"cmd":"printf sandbox-ok > result.txt"}`}}}},
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "read", Name: "read_file", Args: `{"path":"result.txt"}`}}}},
		{{DeltaContent: "done"}},
	}}
	a := newTestAgent(t, &fakePlatform{}, f, "model", config.ProviderConfig{}, newTestStore(t))
	root := t.TempDir()
	a.SetSandboxRoot(root)
	a.SetToolConfig(config.ToolsConfig{MaxRoundsPerTurn: 3})
	dir := filepath.Join(root, "elnis", "watcher")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("DO_NOT_INJECT_BACKGROUND_AGENTS"), 0644); err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	_ = registry.Register(builtin.NewShellTool())
	_ = registry.Register(builtin.NewReadFileTool())
	_ = registry.Register(builtin.NewWorkspaceTool())
	_ = registry.Register(tool.NewDiscoverTool(registry))
	a.SetToolRuntime(registry, nil)
	if _, err := a.RunBackground(context.Background(), background.RunRequest{Kind: background.KindElnis, Name: "files", Platform: "cli", Actor: security.Actor{ID: "cli:local", Role: security.RoleSuperadmin}, SandboxSubdir: "elnis/watcher", Prompt: "write and read", ToolListNames: []string{"shell", "read_file", "workspace", "discover_tool"}}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "result.txt"))
	if err != nil || string(content) != "sandbox-ok" {
		t.Fatalf("sandbox file=%q err=%v", content, err)
	}
	read := false
	for _, request := range f.chatRequests() {
		for _, schema := range request.Tools {
			if schema.Function.Name == "workspace" || schema.Function.Name == "discover_tool" {
				t.Fatalf("forbidden schema: %s", schema.Function.Name)
			}
		}
		for _, message := range request.Messages {
			text := llm.SegmentsContentText(message.Segments)
			if strings.Contains(text, "DO_NOT_INJECT_BACKGROUND_AGENTS") {
				t.Fatal("injected background AGENTS.md")
			}
			if message.Role == llm.RoleTool && message.Name == "read_file" && strings.Contains(text, "sandbox-ok") {
				read = true
			}
		}
	}
	if !read {
		t.Fatal("read_file did not use the Elwisp sandbox")
	}
}

func TestBackgroundHooksCannotExpandTools(t *testing.T) {
	for _, point := range []hook.Point{hook.PointLLMTurnPrepared, hook.PointLLMRequestPrepared, hook.PointLLMResponseReceived, hook.PointToolCallPrepared} {
		t.Run(string(point), func(t *testing.T) {
			var executed string
			candidate := preparedArgumentTool{arguments: &executed}
			registry := tool.NewRegistry()
			_ = registry.Register(candidate)
			_ = registry.Register(agentWrapperTool{name: "allowed"})
			callName := candidate.Name()
			if point == hook.PointToolCallPrepared {
				callName = "allowed"
			}
			f := &fakeLLM{chunks: [][]llm.StreamChunk{
				{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call", Name: callName, Args: "{}"}}}},
				{{DeltaContent: "done"}},
			}}
			a := newTestAgent(t, &fakePlatform{}, f, "model", config.ProviderConfig{}, newTestStore(t))
			a.SetSandboxRoot(t.TempDir())
			a.SetToolRuntime(registry, nil)
			manager := hook.NewManager()
			responseInjected := false
			if err := manager.Register(hook.Registration{Point: point, Name: "expand", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, error) {
				switch point {
				case hook.PointToolCallPrepared:
					event.Tool.Name = candidate.Name()
				case hook.PointLLMResponseReceived:
					if !responseInjected {
						event.LLM.ToolCalls = []llm.ToolCallRequest{{ID: "hook-call", Name: candidate.Name(), Arguments: "{}"}}
						responseInjected = true
					}
				default:
					event.LLM.Tools = []llm.ToolSchema{candidate.Schema()}
				}
				return event, nil
			})}); err != nil {
				t.Fatal(err)
			}
			a.setTestHookManager(manager)
			if _, err := a.RunBackground(context.Background(), background.RunRequest{Kind: background.KindElnis, Name: "hooks", Platform: "cli", Actor: security.Actor{ID: "cli:local", Role: security.RoleSuperadmin}, Prompt: "run", ToolListNames: []string{"allowed"}}); err != nil {
				t.Fatal(err)
			}
			if executed != "" {
				t.Fatalf("hook expanded authority: %s", executed)
			}
			for _, request := range f.chatRequests() {
				if strings.Contains(toolNames(request.Tools), candidate.Name()) {
					t.Fatal("hook injected undeclared schema")
				}
			}
		})
	}
}

func TestBackgroundTagPromptRequiresExplicitSelection(t *testing.T) {
	for _, selector := range []string{"alpha", "worker", ""} {
		t.Run(selector, func(t *testing.T) {
			ctx := context.Background()
			f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: "first"}}, {{DeltaContent: "second"}}}}
			a := newTestAgent(t, &fakePlatform{}, f, "model", config.ProviderConfig{}, newTestStore(t))
			a.SetSandboxRoot(t.TempDir())
			registry := tool.NewRegistry()
			_ = registry.Register(agentWrapperTool{name: "alpha"})
			_ = registry.Register(tool.NewDiscoverTool(registry))
			a.SetToolRuntime(registry, nil)
			a.SetToolTagConfig("", config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{"worker": {Tools: []string{"alpha"}, Prompt: "EXPLICIT_TAG_PROMPT"}}})
			req := background.RunRequest{Kind: background.KindElnis, Name: "tag", Platform: "cli", Actor: security.Actor{ID: "cli:local", Role: security.RoleSuperadmin}, Prompt: "@tool:worker @skill:doc", ToolListNames: []string{selector}}
			first, err := a.RunBackground(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			req.SessionID, req.ToolListNames = first.SessionID, nil
			if _, err := a.RunBackground(ctx, req); err != nil {
				t.Fatal(err)
			}
			for _, request := range f.chatRequests() {
				system := llm.SegmentsContentText(request.Messages[0].Segments)
				if strings.Contains(system, "EXPLICIT_TAG_PROMPT") != (selector == "worker") {
					t.Fatalf("tag prompt selector=%q: %s", selector, system)
				}
				want := "alpha"
				if selector == "" {
					want = ""
				}
				if got := toolNames(request.Tools); got != want {
					t.Fatalf("tools=%q want=%q", got, want)
				}
			}
		})
	}
}
