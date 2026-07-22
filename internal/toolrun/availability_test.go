package toolrun

import (
	"context"
	"strings"
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

func TestSchemasKeepStableOrderAcrossBatchAndIncrementalDiscovery(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(tool.NewDiscoverTool(registry)); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(registry, security.DefaultPolicy())
	view := Context{Mode: storage.SessionModeWork, Actor: security.Actor{Role: security.RoleSuperadmin}}
	cached := func(name string) CachedTool {
		return CachedTool{Name: name, Source: SourceKindNative, Schema: llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{Name: name}}}
	}
	alpha := cached("alpha")
	beta := cached("beta")
	batch := NormalizeCachedTools([]CachedTool{beta, alpha})
	incremental := MergeCachedTools(MergeCachedTools(nil, []CachedTool{beta}), []CachedTool{alpha})
	reverseIncremental := MergeCachedTools(MergeCachedTools(nil, []CachedTool{alpha}), []CachedTool{beta})
	restored := DecodeCache(EncodeCache(Cache{Tools: incremental})).Tools

	for name, tools := range map[string][]CachedTool{
		"batch":               batch,
		"incremental":         incremental,
		"reverse incremental": reverseIncremental,
		"restored":            restored,
	} {
		t.Run(name, func(t *testing.T) {
			schemas, err := manager.Schemas(context.Background(), view, tools)
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(schemas))
			for _, schema := range schemas {
				names = append(names, schema.Function.Name)
			}
			if got := strings.Join(names, ","); got != "discover_tool,alpha,beta" {
				t.Fatalf("schema order = %q", got)
			}
		})
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
