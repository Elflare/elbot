package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool/discovery"
	"elbot/internal/tool/preload"
	toolregistry "elbot/internal/tool/registry"
)

type availabilityTestTool struct {
	name  string
	types []llm.APIType
	calls *int
}

func (t availabilityTestTool) Name() string { return t.name }
func (t availabilityTestTool) Info() toolshare.Info {
	return toolshare.NewBuilder(t.name).APITypes(t.types...).Tags("pictures").BuildInfo()
}
func (t availabilityTestTool) Schema() llm.ToolSchema {
	return toolshare.NewBuilder(t.name).BuildSchema()
}
func (t availabilityTestTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		if t.calls != nil {
			*t.calls++
		}
		{
			result := &toolshare.Result{Content: "image"}
			return result, result.Content, nil
		}
	}), nil
}

func TestAPITypeAvailabilityAcrossDiscoveryCacheAndExecution(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	calls := 0
	image := availabilityTestTool{name: "image", types: []llm.APIType{llm.APITypeResponse}, calls: &calls}
	if err := registry.Register(image); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(discovery.NewDiscoverTool(discovery.NewResolver(registry))); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.DefaultPermission())
	preloader := preload.NewPreloadService(preload.PreloadOptions{Registry: registry, Resolver: discovery.NewResolver(registry), NativeDiscoveredToolsFromDiscovery: discovery.NativeDiscoveredToolsFromDiscovery, LoadDiscoveryContent: discovery.LoadDiscoveryContent, BackgroundDiscoveredTools: toolregistry.BackgroundDiscoveredTools})
	actor := contextinfo.Actor{Role: contextinfo.RoleSuperadmin}
	for _, apiType := range []string{"", "chat", "response", "other"} {
		t.Run(apiType, func(t *testing.T) {
			ctx := contextinfo.WithActor(t.Context(), actor)
			if apiType != "" {
				ctx = contextinfo.WithModel(ctx, contextinfo.Model{APIType: apiType})
			}
			want := apiType == "response"
			prepared, err := preloader.PrepareTools(ctx, []string{"image"})
			if err != nil || len(prepared.Update.Tools) != boolInt(want) {
				t.Fatalf("preload=%+v err=%v", prepared, err)
			}
			background, err := preloader.PrepareBackground(contextinfo.WithBackgroundKind(ctx, ""), toolshare.BackgroundPreloadRequest{SessionID: "session", Names: []string{"image"}, AllowedToolNames: []string{"image"}})
			if err != nil || len(background.Update.Tools) != boolInt(want) {
				t.Fatalf("background preload=%+v err=%v", background, err)
			}
			cache := prepared.Update.Tools
			for _, mode := range []string{storage.SessionModeWork, storage.SessionModeBackground} {
				schemas, err := manager.Schemas(ctx, toolshare.Context{Mode: mode, DisableBaseTools: true}, cache)
				if err != nil || len(schemas) != boolInt(want) {
					t.Fatalf("%s schemas=%v err=%v", mode, schemas, err)
				}
				if got := manager.resolve(ctx, "image", cache, mode); (got.ResultStatus == events.ResultSucceeded) != want {
					t.Fatalf("resolved=%+v", got)
				}
			}
			// Authorization expands the full tag even before API facts are known.
			roots, err := preloader.BackgroundSelections(ctx, []string{"pictures"})
			if err != nil {
				t.Fatal(err)
			}
			if len(roots) != 1 || len(roots[0].Names) != 1 || roots[0].Names[0] != "image" {
				t.Fatalf("roots=%+v", roots)
			}
			call := llm.ToolCallRequest{Name: "image", Arguments: `{}`}
			resolved := manager.resolve(ctx, "image", nil, storage.SessionModeWork)
			var preparedCall *toolshare.PreparedCall
			if want {
				preparedCall, err = manager.prepareCall(ctx, resolved, call)
				if err != nil {
					t.Fatal(err)
				}
			}
			executed := manager.executePrepared(ctx, call, resolved, preparedCall, nil)
			if (executed.Error == nil) != want {
				t.Fatalf("execute=%+v", executed)
			}
		})
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times", calls)
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
