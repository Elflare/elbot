package agent

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
)

func TestViewImageCompletionAndBackgroundUseSelectedAPI(t *testing.T) {
	registry := tool.NewRegistry()
	_ = registry.Register(builtin.NewViewImageTool(nil, nil))
	f := newNativeFixture(t, func(_ int, request nativeTestRequest, w http.ResponseWriter) {
		if !strings.Contains(inputJSON(request), `"name":"view_image"`) {
			t.Error("Responses background override did not preload view_image")
		}
		emitNative(w, "done", "completed", nativeText("done"))
	}, func(opts *testAgentOptions) {
		addNativeOtherModels(t, opts, "chat")
		opts.ToolRegistry = registry
		opts.SandboxRoot = t.TempDir()
	})
	if got := completeTest(f.agent.CompletionService(), "@tool:view_image"); len(got) != 0 {
		t.Fatalf("Chat completion exposed view_image: %v", got)
	}
	_, err := f.agent.RunBackground(t.Context(), background.RunRequest{
		Kind: background.KindCron, Name: "view", Platform: "cli",
		Actor:  contextinfo.Actor{ID: "cli:local", Role: contextinfo.RoleSuperadmin},
		Prompt: "run", ModelProvider: "native", Model: "m", ToolListNames: []string{"view_image"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.captured()) != 1 {
		t.Fatalf("requests=%d", len(f.captured()))
	}
	if err := f.agent.HandleMessage(t.Context(), "/model native/m"); err != nil {
		t.Fatal(err)
	}
	if got := completeTest(f.agent.CompletionService(), "@tool:view_image"); len(got) != 1 {
		t.Fatalf("Responses completion=%v", got)
	}
}

func TestChatHooksCannotEnableResponsesTool(t *testing.T) {
	called := false
	candidate := apiRestrictedProbe{nativeTool{name: "response_only", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
		called = true
		return &tool.Result{Content: "unexpected"}, nil
	}}}
	registry := tool.NewRegistry()
	_ = registry.Register(candidate)
	hooks := hook.NewManager()
	for _, point := range []hook.Point{hook.PointLLMTurnPrepared, hook.PointLLMRequestPrepared} {
		if err := hooks.Register(hook.Registration{Point: point, Name: "inject", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, error) {
			event.LLM.Tools = append(event.LLM.Tools, candidate.Schema())
			return event, nil
		})}); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeLLM{chunks: [][]chatcompletions.Chunk{
		{{ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "forced", Name: candidate.Name(), Args: "{}"}}}},
		{{DeltaContent: "done"}},
	}}
	a := newTestAgent(t, &fakePlatform{}, f, "model", config.ProviderConfig{}, newTestStore(t), func(opts *testAgentOptions) {
		opts.ToolRegistry, opts.HookManager = registry, hooks
	})
	if err := a.HandleMessage(t.Context(), "run"); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("Chat executed Responses-only tool")
	}
	for _, request := range f.chatRequests() {
		if strings.Contains(toolNames(request.Tools), candidate.Name()) {
			t.Fatal("hook injected unavailable schema")
		}
	}
}

type apiRestrictedProbe struct{ nativeTool }

func (p apiRestrictedProbe) Info() tool.Info {
	info := p.nativeTool.Info()
	info.APITypes = []llm.APIType{llm.APITypeResponse}
	return info
}
