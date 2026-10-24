package execution

import toolshare "elbot/internal/tool/share"

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
func (foregroundOnlyTestTool) Info() toolshare.Info {
	return toolshare.NewBuilder("foreground_only").ForegroundOnly().Risk(toolshare.RiskLow).BuildInfo()
}
func (foregroundOnlyTestTool) Schema() llm.ToolSchema {
	return toolshare.NewBuilder("foreground_only").ForegroundOnly().BuildSchema()
}
func (foregroundOnlyTestTool) Call(ctx context.Context, req toolshare.CallRequest) (*toolshare.Result, string, error) {
	{
		result := &toolshare.Result{Content: "ok"}
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
	manager := NewManager(registry, security.DefaultPermission())
	ctx := sandboxctx.WithSandboxContext(context.Background(), sandboxctx.SandboxContext{Dir: t.TempDir(), Background: true, BackgroundKind: sandboxctx.BackgroundKindCron})
	view := toolshare.Context{Mode: storage.SessionModeWork, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}}
	names, err := manager.ToolNames(ctx, view)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == "foreground_only" {
			t.Fatalf("foreground-only tool leaked into background names: %#v", names)
		}
	}
	schemas, err := manager.Schemas(ctx, view, []toolshare.DiscoveredTool{{Name: "cached_foreground", Source: toolshare.SourceKindELwisp, ForegroundOnly: true, Schema: llm.ToolSchema{Name: "cached_foreground"}}})
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
	manager := NewManager(registry, security.DefaultPermission())
	view := toolshare.Context{Mode: storage.SessionModeWork, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}}
	cached := func(name string) toolshare.DiscoveredTool {
		return toolshare.DiscoveredTool{Name: name, Source: toolshare.SourceKindNative, Schema: llm.ToolSchema{Name: name}}
	}
	alpha := cached("alpha")
	beta := cached("beta")
	batch := NormalizeCachedTools([]toolshare.DiscoveredTool{beta, alpha})
	incremental := MergeCachedTools(MergeCachedTools(nil, []toolshare.DiscoveredTool{beta}), []toolshare.DiscoveredTool{alpha})
	reverseIncremental := MergeCachedTools(MergeCachedTools(nil, []toolshare.DiscoveredTool{alpha}), []toolshare.DiscoveredTool{beta})
	metadata, err := json.Marshal(map[string]any{"tool_cache": incremental})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeState(string(metadata))
	if err != nil {
		t.Fatal(err)
	}

	for name, tools := range map[string][]toolshare.DiscoveredTool{
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
	manager := NewManager(tool.NewRegistry(), security.DefaultPermission())
	var cached []toolshare.DiscoveredTool
	for _, name := range []string{"shell", "read_file", "edit_file", "send_file"} {
		cached = append(cached, toolshare.DiscoveredTool{Name: name, Source: toolshare.SourceKindNative, Schema: llm.ToolSchema{Name: name, Description: "stable definition", Parameters: map[string]any{"type": "object"}}})
	}
	ctx := sandboxctx.WithSandboxContext(t.Context(), sandboxctx.SandboxContext{Background: true, Dir: t.TempDir()})
	background, err := manager.Schemas(ctx, toolshare.Context{Mode: storage.SessionModeBackground, DisableBaseTools: true}, cached)
	if err != nil {
		t.Fatal(err)
	}
	foreground, err := manager.Schemas(t.Context(), toolshare.Context{Mode: storage.SessionModeWork}, cached)
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
	manager := NewManager(registry, security.DefaultPermission())
	ctx := sandboxctx.WithSandboxContext(context.Background(), sandboxctx.SandboxContext{Dir: t.TempDir(), Background: true, BackgroundKind: sandboxctx.BackgroundKindCron})
	resolved := manager.Resolve(ctx, "foreground_only", nil, storage.SessionModeBackground)
	if resolved.ResultStatus != events.ResultRejected || resolved.Error == nil {
		t.Fatalf("resolved = %#v", resolved)
	}
	cached := manager.Resolve(ctx, "cached_foreground", []toolshare.DiscoveredTool{{Name: "cached_foreground", Source: toolshare.SourceKindELwisp, Endpoint: "http://127.0.0.1", ForegroundOnly: true}}, storage.SessionModeBackground)
	if cached.ResultStatus != events.ResultRejected || cached.Error == nil {
		t.Fatalf("cached resolved = %#v", cached)
	}
	var denied toolshare.PolicyDeniedError
	if !errors.As(resolved.Error, &denied) || !errors.As(cached.Error, &denied) {
		t.Fatalf("permission errors were flattened: %T / %T", resolved.Error, cached.Error)
	}
}

func TestResolveDistinguishesMissingToolFromMissingRemoteConfiguration(t *testing.T) {
	manager := NewManager(tool.NewRegistry(), nil)
	for _, tc := range []struct {
		name   string
		cached []toolshare.DiscoveredTool
	}{
		{"missing", nil},
		{"remote", []toolshare.DiscoveredTool{{Name: "remote", Source: toolshare.SourceKindELwisp}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved := manager.Resolve(t.Context(), tc.name, tc.cached, storage.SessionModeWork)
			var denied toolshare.PolicyDeniedError
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
