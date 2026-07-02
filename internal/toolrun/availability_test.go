package toolrun

import (
	"context"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type foregroundOnlyTestTool struct{}

func (foregroundOnlyTestTool) Name() string { return "foreground_only" }
func (foregroundOnlyTestTool) Info() tool.Info {
	return tool.NewBuilder("foreground_only").ForegroundOnly().Risk(tool.RiskLow).BuildInfo()
}
func (foregroundOnlyTestTool) Schema() llm.ToolSchema {
	return tool.NewBuilder("foreground_only").ForegroundOnly().BuildSchema()
}
func (foregroundOnlyTestTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	return &tool.Result{Content: "ok"}, nil
}

func TestForegroundOnlyToolHiddenInBackgroundSchemasAndNames(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(tool.NewDiscoverTool(registry)); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(foregroundOnlyTestTool{}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(registry, security.DefaultPolicy())
	ctx := tool.WithSandboxContext(context.Background(), tool.SandboxContext{Dir: t.TempDir(), Background: true, BackgroundKind: tool.BackgroundKindCron})
	view := Context{Mode: storage.SessionModeWork, Actor: security.Actor{Role: security.RoleSuperadmin}}
	names, err := manager.ToolNames(ctx, view)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == "foreground_only" {
			t.Fatalf("foreground-only tool leaked into background names: %#v", names)
		}
	}
	schemas, err := manager.Schemas(ctx, view, []CachedTool{{Name: "cached_foreground", Source: SourceKindELwisp, ForegroundOnly: true, Schema: llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{Name: "cached_foreground"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range schemas {
		if schema.Function.Name == "foreground_only" || schema.Function.Name == "cached_foreground" {
			t.Fatalf("foreground-only tool leaked into background schemas: %#v", schemas)
		}
	}
}

func TestForegroundOnlyToolResolveRejectedInBackground(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(foregroundOnlyTestTool{}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(registry, security.DefaultPolicy())
	ctx := tool.WithSandboxContext(context.Background(), tool.SandboxContext{Dir: t.TempDir(), Background: true, BackgroundKind: tool.BackgroundKindCron})
	resolved := manager.Resolve(ctx, "foreground_only", nil)
	if resolved.Available || resolved.Reason == "" {
		t.Fatalf("resolved = %#v", resolved)
	}
	cached := manager.Resolve(ctx, "cached_foreground", []CachedTool{{Name: "cached_foreground", Source: SourceKindELwisp, Endpoint: "http://127.0.0.1", ForegroundOnly: true}})
	if cached.Available || cached.Reason == "" {
		t.Fatalf("cached resolved = %#v", cached)
	}
}
