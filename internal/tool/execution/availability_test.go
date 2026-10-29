package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool/discovery"
	toolregistry "elbot/internal/tool/registry"
	"elbot/internal/tool/state"
	"elbot/internal/workspace"
)

type foregroundOnlyTestTool struct{}

func (foregroundOnlyTestTool) Name() string { return "foreground_only" }
func (foregroundOnlyTestTool) Info() toolshare.Info {
	return toolshare.NewBuilder("foreground_only").ForegroundOnly().Risk(toolshare.RiskLow).BuildInfo()
}
func (foregroundOnlyTestTool) Schema() llm.ToolSchema {
	return toolshare.NewBuilder("foreground_only").ForegroundOnly().BuildSchema()
}
func (value foregroundOnlyTestTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: value.Info().Risk}, "", func(ctx context.Context) (*toolshare.Result, string, error) {
		{
			result := &toolshare.Result{Content: "ok"}
			return result, result.Content, nil
		}
	}), nil
}

func TestForegroundOnlyToolHiddenInBackgroundSchemas(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registry.Register(discovery.NewDiscoverTool(discovery.NewResolver(registry))); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(foregroundOnlyTestTool{}); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.DefaultPermission())
	ctx := workspace.WithPathScope(contextinfo.WithBackgroundKind(context.Background(), contextinfo.BackgroundKindCron), workspace.PathScope{Dir: t.TempDir(), Restricted: true})
	view := toolshare.Context{Mode: storage.SessionModeWork, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}}
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
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registry.Register(discovery.NewDiscoverTool(discovery.NewResolver(registry))); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.DefaultPermission())
	view := toolshare.Context{Mode: storage.SessionModeWork, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}}
	discovered := func(name string) toolshare.DiscoveredTool {
		return toolshare.DiscoveredTool{Name: name, Source: toolshare.SourceKindNative, Schema: llm.ToolSchema{Name: name}}
	}
	alpha := discovered("alpha")
	beta := discovered("beta")
	prepare := func(raw string, tools ...toolshare.DiscoveredTool) (string, []toolshare.DiscoveredTool) {
		t.Helper()
		patch, err := state.PrepareUpdate(raw, toolshare.StateUpdate{Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
		metadata, err := patch.Encode()
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := state.DecodeState(metadata)
		if err != nil {
			t.Fatal(err)
		}
		return metadata, snapshot.DiscoveredTools
	}
	_, batch := prepare("", beta, alpha)
	first, _ := prepare("", beta)
	metadata, incremental := prepare(first, alpha)
	first, _ = prepare("", alpha)
	_, reverseIncremental := prepare(first, beta)
	restored, err := state.DecodeState(metadata)
	if err != nil {
		t.Fatal(err)
	}

	for name, tools := range map[string][]toolshare.DiscoveredTool{
		"batch":               batch,
		"incremental":         incremental,
		"reverse incremental": reverseIncremental,
		"restored":            restored.DiscoveredTools,
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
	manager := newTestManager(toolregistry.NewRegistry(security.DefaultPermission()), security.DefaultPermission())
	var discovered []toolshare.DiscoveredTool
	for _, name := range []string{"shell", "read_file", "edit_file", "send_file"} {
		discovered = append(discovered, toolshare.DiscoveredTool{Name: name, Source: toolshare.SourceKindNative, Schema: llm.ToolSchema{Name: name, Description: "stable definition", Parameters: map[string]any{"type": "object"}}})
	}
	ctx := workspace.WithPathScope(contextinfo.WithBackgroundKind(t.Context(), ""), workspace.PathScope{Dir: t.TempDir(), Restricted: true})
	background, err := manager.Schemas(ctx, toolshare.Context{Mode: storage.SessionModeBackground, DisableBaseTools: true}, discovered)
	if err != nil {
		t.Fatal(err)
	}
	foreground, err := manager.Schemas(t.Context(), toolshare.Context{Mode: storage.SessionModeWork}, discovered)
	if err != nil {
		t.Fatal(err)
	}
	if len(background) != len(discovered) || !reflect.DeepEqual(background, foreground) {
		t.Fatalf("schemas changed on takeover: background=%+v foreground=%+v", background, foreground)
	}
}
func TestForegroundOnlyToolResolveRejectedInBackground(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registry.Register(foregroundOnlyTestTool{}); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.DefaultPermission())
	ctx := workspace.WithPathScope(contextinfo.WithBackgroundKind(context.Background(), contextinfo.BackgroundKindCron), workspace.PathScope{Dir: t.TempDir(), Restricted: true})
	resolved := manager.resolve(ctx, "foreground_only", nil, storage.SessionModeBackground)
	if resolved.ResultStatus != events.ResultRejected || resolved.Error == nil {
		t.Fatalf("resolved = %#v", resolved)
	}
	discovered := manager.resolve(ctx, "cached_foreground", []toolshare.DiscoveredTool{{Name: "cached_foreground", Source: toolshare.SourceKindELwisp, Endpoint: "http://127.0.0.1", ForegroundOnly: true}}, storage.SessionModeBackground)
	if discovered.ResultStatus != events.ResultRejected || discovered.Error == nil {
		t.Fatalf("discovered resolved = %#v", discovered)
	}
	var denied toolshare.PolicyDeniedError
	if !errors.As(resolved.Error, &denied) || !errors.As(discovered.Error, &denied) {
		t.Fatalf("permission errors were flattened: %T / %T", resolved.Error, discovered.Error)
	}
}

func TestResolveDistinguishesMissingToolFromMissingRemoteConfiguration(t *testing.T) {
	manager := newTestManager(toolregistry.NewRegistry(security.DefaultPermission()), nil)
	for _, tc := range []struct {
		name       string
		discovered []toolshare.DiscoveredTool
	}{
		{"missing", nil},
		{"remote", []toolshare.DiscoveredTool{{Name: "remote", Source: toolshare.SourceKindELwisp}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved := manager.resolve(t.Context(), tc.name, tc.discovered, storage.SessionModeWork)
			var denied toolshare.PolicyDeniedError
			if resolved.ResultStatus != events.ResultFailed || resolved.Error == nil || errors.As(resolved.Error, &denied) {
				t.Fatalf("resolution facts = %#v", resolved)
			}
			result := manager.executePrepared(t.Context(), llm.ToolCallRequest{Name: tc.name}, resolved, nil, nil)
			if result.ResultStatus != resolved.ResultStatus || result.Error != resolved.Error || result.ExecutionStarted {
				t.Fatalf("execution lost resolution facts: %#v", result)
			}
		})
	}
}

func TestSchemasAreDetachedForPreparedHook(t *testing.T) {
	item := toolshare.DiscoveredTool{Name: "tool", Source: toolshare.SourceKindNative, Schema: llm.ToolSchema{Name: "tool", Parameters: map[string]any{"properties": map[string]any{"path": map[string]any{"type": "string"}}}}}
	manager := newTestManager(toolregistry.NewRegistry(security.DefaultPermission()), security.DefaultPermission())
	schemas, err := manager.Schemas(context.Background(), toolshare.Context{Mode: storage.SessionModeWork, DisableBaseTools: true}, []toolshare.DiscoveredTool{item})
	if err != nil {
		t.Fatal(err)
	}
	schemas[0].Parameters["properties"].(map[string]any)["path"].(map[string]any)["type"] = "number"
	next, err := manager.Schemas(context.Background(), toolshare.Context{Mode: storage.SessionModeWork, DisableBaseTools: true}, []toolshare.DiscoveredTool{item})
	if err != nil {
		t.Fatal(err)
	}
	if next[0].Parameters["properties"].(map[string]any)["path"].(map[string]any)["type"] != "string" {
		t.Fatal("prepared hook polluted next request")
	}
}
