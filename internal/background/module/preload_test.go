package module

import (
	"context"
	"errors"
	"reflect"
	"testing"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool/preload"
	"elbot/internal/tool/registry"
	toolshare "elbot/internal/tool/share"
	"elbot/internal/tool/state"
)

type backgroundPreloadTestTool struct {
	name string
	err  error
}

func (t backgroundPreloadTestTool) Name() string { return t.name }
func (t backgroundPreloadTestTool) Info() toolshare.Info {
	return toolshare.Info{Name: t.name, Risk: toolshare.RiskLow}
}
func (t backgroundPreloadTestTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: t.name, Parameters: map[string]any{"type": "object"}}
}
func (t backgroundPreloadTestTool) DiscoveryContent(context.Context) (string, bool, error) {
	return "local instructions", false, t.err
}
func (value backgroundPreloadTestTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: value.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		return &toolshare.Result{}, "", nil
	}), nil
}

func TestBackgroundPreloadCommitsDeclarationsAndContinuationKeepsSet(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "preparation failure"}[fail], func(t *testing.T) {
			store, err := sqlite.New(t.Context(), ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			sessions := session.NewService(store)
			t.Cleanup(func() { _ = sessions.Close(context.Background()) })
			permission := security.DefaultPermission()
			registry := registry.NewRegistry(permission)
			tool := backgroundPreloadTestTool{name: "local"}
			if fail {
				tool.err = errors.New("cannot prepare content")
			}
			if err := registry.Register(tool); err != nil {
				t.Fatal(err)
			}
			preloader := preload.NewPreloadService(preload.PreloadOptions{Registry: registry, Tags: config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{
				"group": {Tools: []string{"local"}},
			}}})
			stateService := state.NewStateService(store)
			loadState := func(id string) (toolshare.State, error) {
				row, err := store.Sessions().Get(t.Context(), id)
				if err != nil {
					return toolshare.State{}, err
				}
				return state.DecodeState(row.Metadata)
			}
			runner := NewBackgroundRunner(BackgroundRunnerDeps{
				Sessions: sessions, SessionRows: store.Sessions(),
				Identity:  agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local", Permission: permission}),
				Execution: &backgroundExecutionStub{},
				Preloader: preloader, ToolState: stateService, SandboxRoot: t.TempDir(),
			})
			req := background.RunRequest{Kind: background.KindElnis, Name: "declarations", Prompt: "run", ToolListNames: []string{"group"}, ELwispTools: []toolshare.ELwispToolDeclaration{
				{Name: "remote", CanonicalName: "origin_remote", EventKey: "event", Endpoint: "https://example.invalid", Schema: map[string]any{"type": "object"}},
			}}
			result, err := runner.RunBackground(t.Context(), req)
			if fail {
				if !errors.Is(err, tool.err) {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				stored, loadErr := loadState(result.SessionID)
				if loadErr != nil || len(stored.DiscoveredTools) != 0 || len(stored.ToolTags) != 0 {
					t.Fatalf("partial state=%+v err=%v", stored, loadErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := loadState(result.SessionID)
			if err != nil || len(before.DiscoveredTools) != 2 || len(before.ToolTags) != 1 || before.ToolTags[0] != "group" {
				t.Fatalf("state=%+v err=%v", before, err)
			}
			if before.DiscoveredTools[1].EventKey != "event" || before.DiscoveredTools[1].Schema.Name != "origin_remote" {
				t.Fatalf("remote=%+v", before.DiscoveredTools[1])
			}
			req.SessionID = result.SessionID
			req.ToolListNames = []string{"different"}
			req.ELwispTools = []toolshare.ELwispToolDeclaration{{Name: "new", CanonicalName: "origin_new"}}
			if _, err := runner.RunBackground(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			after, err := loadState(result.SessionID)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("continuation changed state: before=%+v after=%+v err=%v", before, after, err)
			}
		})
	}
}
