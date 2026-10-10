package toolrun

import (
	"context"
	"elbot/internal/events"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	sandboxctx "elbot/internal/sandbox"
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
func (foregroundOnlyTestTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, string, error) {
	{
		result := &tool.Result{Content: "ok"}
		return result, result.Content, nil
	}
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
	ctx := sandboxctx.WithSandboxContext(context.Background(), sandboxctx.SandboxContext{Dir: t.TempDir(), Background: true, BackgroundKind: sandboxctx.BackgroundKindCron})
	view := Context{Mode: storage.SessionModeWork, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}}
	names, err := manager.ToolNames(ctx, view)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == "foreground_only" {
			t.Fatalf("foreground-only tool leaked into background names: %#v", names)
		}
	}
	schemas, err := manager.Schemas(ctx, view, []CachedTool{{Name: "cached_foreground", Source: SourceKindELwisp, ForegroundOnly: true, Schema: llm.ToolSchema{Name: "cached_foreground"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range schemas {
		if schema.Name == "foreground_only" || schema.Name == "cached_foreground" {
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
	view := Context{Mode: storage.SessionModeWork, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}}
	cached := func(name string) CachedTool {
		return CachedTool{Name: name, Source: SourceKindNative, Schema: llm.ToolSchema{Name: name}}
	}
	alpha := cached("alpha")
	beta := cached("beta")
	batch := NormalizeCachedTools([]CachedTool{beta, alpha})
	incremental := MergeCachedTools(MergeCachedTools(nil, []CachedTool{beta}), []CachedTool{alpha})
	reverseIncremental := MergeCachedTools(MergeCachedTools(nil, []CachedTool{alpha}), []CachedTool{beta})
	metadata, err := json.Marshal(map[string]any{"tool_cache": incremental})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeState(string(metadata))
	if err != nil {
		t.Fatal(err)
	}

	for name, tools := range map[string][]CachedTool{
		"batch":               batch,
		"incremental":         incremental,
		"reverse incremental": reverseIncremental,
		"restored":            restored.ToolCache,
	} {
		t.Run(name, func(t *testing.T) {
			schemas, err := manager.Schemas(context.Background(), view, tools)
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(schemas))
			for _, schema := range schemas {
				names = append(names, schema.Name)
			}
			if got := strings.Join(names, ","); got != "discover_tool,alpha,beta" {
				t.Fatalf("schema order = %q", got)
			}
		})
	}
}

func TestPathSchemasStayStableAcrossBackgroundTakeover(t *testing.T) {
	manager := NewManager(tool.NewRegistry(), security.DefaultPolicy())
	var cached []CachedTool
	for _, name := range []string{"shell", "read_file", "edit_file", "send_file"} {
		cached = append(cached, CachedTool{Name: name, Source: SourceKindNative, Schema: llm.ToolSchema{Name: name, Description: "stable definition", Parameters: map[string]any{"type": "object"}}})
	}
	ctx := sandboxctx.WithSandboxContext(t.Context(), sandboxctx.SandboxContext{Background: true, Dir: t.TempDir()})
	background, err := manager.Schemas(ctx, Context{Mode: storage.SessionModeBackground, DisableBaseTools: true}, cached)
	if err != nil {
		t.Fatal(err)
	}
	foreground, err := manager.Schemas(t.Context(), Context{Mode: storage.SessionModeWork}, cached)
	if err != nil {
		t.Fatal(err)
	}
	if len(background) != len(cached) || !reflect.DeepEqual(background, foreground) {
		t.Fatalf("schemas changed on takeover: background=%+v foreground=%+v", background, foreground)
	}
}
func TestForegroundOnlyToolResolveRejectedInBackground(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(foregroundOnlyTestTool{}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(registry, security.DefaultPolicy())
	ctx := sandboxctx.WithSandboxContext(context.Background(), sandboxctx.SandboxContext{Dir: t.TempDir(), Background: true, BackgroundKind: sandboxctx.BackgroundKindCron})
	resolved := manager.Resolve(ctx, "foreground_only", nil, storage.SessionModeBackground)
	if resolved.ResultStatus != events.ResultRejected || resolved.Error == nil {
		t.Fatalf("resolved = %#v", resolved)
	}
	cached := manager.Resolve(ctx, "cached_foreground", []CachedTool{{Name: "cached_foreground", Source: SourceKindELwisp, Endpoint: "http://127.0.0.1", ForegroundOnly: true}}, storage.SessionModeBackground)
	if cached.ResultStatus != events.ResultRejected || cached.Error == nil {
		t.Fatalf("cached resolved = %#v", cached)
	}
	var denied tool.PolicyDeniedError
	if !errors.As(resolved.Error, &denied) || !errors.As(cached.Error, &denied) {
		t.Fatalf("policy errors were flattened: %T / %T", resolved.Error, cached.Error)
	}
}

func TestResolveDistinguishesMissingToolFromMissingRemoteConfiguration(t *testing.T) {
	manager := NewManager(tool.NewRegistry(), nil)
	for _, tc := range []struct {
		name   string
		cached []CachedTool
	}{
		{"missing", nil},
		{"remote", []CachedTool{{Name: "remote", Source: SourceKindELwisp}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved := manager.Resolve(t.Context(), tc.name, tc.cached, storage.SessionModeWork)
			var denied tool.PolicyDeniedError
			if resolved.ResultStatus != events.ResultFailed || resolved.Error == nil || errors.As(resolved.Error, &denied) {
				t.Fatalf("resolution facts = %#v", resolved)
			}
			result := manager.Execute(t.Context(), llm.ToolCallRequest{Name: tc.name}, resolved, contextinfo.Actor{})
			if result.ResultStatus != resolved.ResultStatus || result.Error != resolved.Error || result.ExecutionStarted {
				t.Fatalf("execution lost resolution facts: %#v", result)
			}
		})
	}
}
