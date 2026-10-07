package toolrun

import (
	"context"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type availabilityTestTool struct {
	name  string
	types []llm.APIType
	calls *int
}

func (t availabilityTestTool) Name() string { return t.name }
func (t availabilityTestTool) Info() tool.Info {
	return tool.NewBuilder(t.name).APITypes(t.types...).Tags("pictures").BuildInfo()
}
func (t availabilityTestTool) Schema() llm.ToolSchema { return tool.NewBuilder(t.name).BuildSchema() }
func (t availabilityTestTool) Call(context.Context, tool.CallRequest) (*tool.Result, error) {
	if t.calls != nil {
		*t.calls++
	}
	return &tool.Result{Content: "image"}, nil
}

func TestAPITypeAvailabilityAcrossDiscoveryCacheAndExecution(t *testing.T) {
	registry := tool.NewRegistry()
	calls := 0
	image := availabilityTestTool{name: "image", types: []llm.APIType{llm.APITypeResponse}, calls: &calls}
	if err := registry.Register(image); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(tool.NewDiscoverTool(registry)); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(registry, security.DefaultPolicy())
	preloader := NewPreloadService(PreloadOptions{Registry: registry})
	actor := contextinfo.Actor{Role: contextinfo.RoleSuperadmin}
	for _, apiType := range []string{"", "chat", "response", "other"} {
		t.Run(apiType, func(t *testing.T) {
			ctx := contextinfo.WithActor(t.Context(), actor)
			if apiType != "" {
				ctx = contextinfo.WithModel(ctx, contextinfo.Model{APIType: apiType})
			}
			want := apiType == "response"
			names, err := manager.ToolNames(ctx, Context{Mode: storage.SessionModeWork, Actor: actor})
			if err != nil || (len(names) == 1) != want {
				t.Fatalf("names=%v err=%v", names, err)
			}
			prepared, err := preloader.PrepareTools(ctx, []string{"image"})
			if err != nil || len(prepared.Update.Tools) != boolInt(want) {
				t.Fatalf("preload=%+v err=%v", prepared, err)
			}
			cache := prepared.Update.Tools
			for _, mode := range []string{storage.SessionModeWork, storage.SessionModeBackground} {
				schemas, err := manager.Schemas(ctx, Context{Mode: mode, DisableBaseTools: true}, cache)
				if err != nil || len(schemas) != boolInt(want) {
					t.Fatalf("%s schemas=%v err=%v", mode, schemas, err)
				}
				if got := manager.Resolve(ctx, "image", cache, mode); got.Available != want {
					t.Fatalf("resolved=%+v", got)
				}
			}
			// Authorization expands the full tag even before API facts are known.
			roots := preloader.BackgroundSelections(ctx, []string{"pictures"})
			if len(roots) != 1 || len(roots[0].Names) != 1 || roots[0].Names[0] != "image" {
				t.Fatalf("roots=%+v", roots)
			}
			executed := (tool.Executor{Registry: registry, Actor: actor}).Execute(ctx, llm.ToolCallRequest{Name: "image", Arguments: `{}`})
			if (executed.Err == nil) != want {
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
