package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/operationresult"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
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
func (value foregroundOnlyTestTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (operationresult.Result[*toolshare.PreparedCall], error) {
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: value.Info().Risk}, nil, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {

		result := &toolshare.Result{Content: "ok"}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, result.Content, nil

	})}, nil
}

func TestForegroundOnlyToolHiddenInBackgroundSchemas(t *testing.T) {
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	if err := registerTestTools(registry, discovery.NewDiscoverTool(discovery.NewResolver(registry))); err != nil {
		t.Fatal(err)
	}
	if err := registerTestTools(registry, (foregroundOnlyTestTool{}).Definition()); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.DefaultPermission())
	ctx := workspace.WithPathScope(toolshare.WithCallPolicy(context.Background(), toolshare.CallPolicy{DisableForegroundTools: true}), workspace.PathScope{Dir: t.TempDir(), Restricted: true})
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
	if err := registerTestTools(registry, discovery.NewDiscoverTool(discovery.NewResolver(registry))); err != nil {
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
	ctx := workspace.WithPathScope(toolshare.WithCallPolicy(t.Context(), toolshare.CallPolicy{DisableForegroundTools: true}), workspace.PathScope{Dir: t.TempDir(), Restricted: true})
	background, err := manager.Schemas(ctx, toolshare.Context{Mode: storage.SessionModeBackground}, discovered)
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
	if err := registerTestTools(registry, (foregroundOnlyTestTool{}).Definition()); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(registry, security.DefaultPermission())
	ctx := workspace.WithPathScope(toolshare.WithCallPolicy(context.Background(), toolshare.CallPolicy{DisableForegroundTools: true}), workspace.PathScope{Dir: t.TempDir(), Restricted: true})
	resolved, err := manager.resolve(ctx, "foreground_only", nil, storage.SessionModeBackground)
	if err != nil || resolved.Status != operationresult.Forbidden {
		t.Fatalf("resolved = %#v", resolved)
	}
	discovered, err := manager.resolve(ctx, "cached_foreground", []toolshare.DiscoveredTool{{Name: "cached_foreground", Source: toolshare.SourceKindELwisp, Endpoint: "http://127.0.0.1", ForegroundOnly: true}}, storage.SessionModeBackground)
	if err != nil || discovered.Status != operationresult.Forbidden {
		t.Fatalf("discovered resolved = %#v", discovered)
	}
}

func TestSchemasAreDetachedForPreparedHook(t *testing.T) {
	item := toolshare.DiscoveredTool{Name: "tool", Source: toolshare.SourceKindNative, Schema: llm.ToolSchema{Name: "tool", Parameters: map[string]any{"properties": map[string]any{"path": map[string]any{"type": "string"}}}}}
	manager := newTestManager(toolregistry.NewRegistry(security.DefaultPermission()), security.DefaultPermission())
	schemas, err := manager.Schemas(context.Background(), toolshare.Context{Mode: storage.SessionModeWork}, []toolshare.DiscoveredTool{item})
	if err != nil {
		t.Fatal(err)
	}
	schemas[0].Parameters["properties"].(map[string]any)["path"].(map[string]any)["type"] = "number"
	next, err := manager.Schemas(context.Background(), toolshare.Context{Mode: storage.SessionModeWork}, []toolshare.DiscoveredTool{item})
	if err != nil {
		t.Fatal(err)
	}
	if next[0].Parameters["properties"].(map[string]any)["path"].(map[string]any)["type"] != "string" {
		t.Fatal("prepared hook polluted next request")
	}
}
