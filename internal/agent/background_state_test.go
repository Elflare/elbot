package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

func backgroundCachedTool(name string, source toolrun.SourceKind) toolrun.CachedTool {
	return toolrun.CachedTool{Name: name, Source: source, CanonicalName: name,
		Endpoint: "http://example.invalid/tool", Schema: llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{Name: name}}}
}

func TestBackgroundStateFreezesInitialTools(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "declared", true: "empty"}[empty], func(t *testing.T) {
			ctx := context.Background()
			store := newTestStore(t)
			f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: "first"}}, {{DeltaContent: "second"}}}}
			a := newTestAgent(t, &fakePlatform{}, f, "model", config.ProviderConfig{}, store)
			a.SetSandboxRoot(t.TempDir())
			registry := tool.NewRegistry()
			_ = registry.Register(agentWrapperTool{name: "native_old"})
			_ = registry.Register(agentDetailTool{name: "doc", source: tool.SourceSkillAgent, detail: "SHOULD_NOT_BE_PRELOADED"})
			a.SetToolRuntime(registry, nil)
			req := background.RunRequest{Kind: background.KindElnis, Name: "freeze", Platform: "cli",
				Actor: security.Actor{ID: "cli:local", Role: security.RoleSuperadmin}, Prompt: "run"}
			if !empty {
				req.CachedTools = []toolrun.CachedTool{backgroundCachedTool("external_old", toolrun.SourceKindELwisp), backgroundCachedTool("native_old", toolrun.SourceKindNative)}
			}
			first, err := a.RunBackground(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			before, err := a.toolState.Snapshot(ctx, first.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			repo := &toolStateFaultRepo{SessionRepository: store.Sessions(), failure: errors.New("retry must not write tools")}
			setTestToolState(a, toolrun.NewStateService(toolStateFaultStore{Store: store, repo: repo}))
			req.SessionID = first.SessionID
			req.ToolListNames = []string{"native_old", "doc"}
			req.CachedTools = []toolrun.CachedTool{backgroundCachedTool("external_new", toolrun.SourceKindELwisp)}
			if _, err := a.RunBackground(ctx, req); err != nil {
				t.Fatal(err)
			}
			after, err := a.toolState.Snapshot(ctx, first.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if repo.writes != 0 || !reflect.DeepEqual(before, after) {
				t.Fatalf("retry changed tools: writes=%d before=%+v after=%+v", repo.writes, before, after)
			}
			requests := f.chatRequests()
			if len(requests) != 2 {
				t.Fatalf("requests=%d", len(requests))
			}
			want := "external_old,native_old"
			if empty {
				want = ""
			}
			if got := toolNames(requests[1].Tools); got != want {
				t.Fatalf("tools=%q want=%q", got, want)
			}
			for _, message := range requests[1].Messages {
				if strings.Contains(llm.SegmentsContentText(message.Segments), "SHOULD_NOT_BE_PRELOADED") {
					t.Fatal("retry loaded additional skill")
				}
			}
		})
	}
}

func TestBackgroundStateFailureDoesNotPrewriteCache(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	f := &fakeLLM{}
	a := newTestAgent(t, &fakePlatform{}, f, "model", config.ProviderConfig{}, store)
	a.SetSandboxRoot(t.TempDir())
	failure := errors.New("background tool state commit rejected")
	repo := &toolStateFaultRepo{SessionRepository: store.Sessions(), failure: failure}
	setTestToolState(a, toolrun.NewStateService(toolStateFaultStore{Store: store, repo: repo}))
	result, err := a.RunBackground(ctx, background.RunRequest{Kind: background.KindElnis, Name: "failure", Platform: "cli",
		Actor: security.Actor{ID: "cli:local", Role: security.RoleSuperadmin}, Prompt: "run",
		CachedTools: []toolrun.CachedTool{backgroundCachedTool("new", toolrun.SourceKindELwisp)}})
	if !errors.Is(err, failure) || repo.writes != 1 {
		t.Fatalf("err=%v commits=%d", err, repo.writes)
	}
	after, err := toolrun.NewStateService(store).Snapshot(ctx, result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := toolrun.DecodeState("")
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("failed commit changed state: %+v", after)
	}
	if len(f.chatRequests()) != 0 {
		t.Fatal("failed preload started LLM execution")
	}
}
