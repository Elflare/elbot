package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/processenv"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool/builtin/weave"
	"elbot/internal/tool/builtin/web"
	"elbot/internal/tool/discovery"
	"elbot/internal/tool/preload"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
	"elbot/internal/workspace"
)

type weaveDeps struct {
	runnerTestDeps
	prompts []string
}

func (d *weaveDeps) ConfirmToolCall(ctx context.Context, s string, call llm.ToolCallRequest, risk share.RiskAssessment, details []share.ConfirmationDetailBlock) (operationresult.Result[share.ConfirmResult], error) {
	d.prompts = append(d.prompts, call.Name)
	result, err := d.runnerTestDeps.ConfirmToolCall(ctx, s, call, risk, details)
	result.Data.Extra = call.ID
	return result, err
}

func TestWeaveAuditsToolRemovedAfterDiscovery(t *testing.T) {
	permission := security.DefaultPermission()
	manager := newTestManager(registry.NewRegistry(permission), permission)
	var audit events.LogRecord
	manager.Log = func(_ context.Context, record events.LogRecord) { audit = record }
	session := &storage.Session{ID: "work", Mode: storage.SessionModeWork}
	items := []share.DiscoveredTool{{Name: "removed", Source: share.SourceKindNative}}
	call := llm.ToolCallRequest{ID: "outer/1", Name: "removed", Arguments: "{}"}
	got, err := manager.callWeave(t.Context(), &weaveDeps{}, session, items, []string{"removed"}, call, contextinfo.Actor{})
	if err != nil || got.Status != operationresult.Unavailable || audit.EventName != "tool_call_completed" || audit.ResultStatus != events.ResultRejected {
		t.Fatalf("removed tool: result=%+v audit=%+v err=%v", got, audit, err)
	}
}

func TestWeaveDiscoveryAndExecution(t *testing.T) {
	permission := security.DefaultPermission()
	catalog := registry.NewRegistry(permission)
	env := processenv.Environment{}
	outer := weave.Definition(env)
	original, _ := json.Marshal(outer.Schema())
	edit := (fakeTool{name: "edit_file", risk: share.RiskHigh}).Definition()
	edit.Definition.Weavable = true
	if err := registerTestTools(catalog, outer, web.NewWebExtractTool(env).Definition(), web.NewWebSearchTool(env).Definition(), edit, discovery.NewDiscoverTool(discovery.NewResolver(catalog))); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(catalog, permission)
	actor := contextinfo.Actor{Role: contextinfo.RoleSuperadmin}
	ctx := contextinfo.WithActor(workspace.WithPathScope(t.Context(), workspace.PathScope{Dir: t.TempDir()}), actor)
	session := &storage.Session{ID: "work", Mode: storage.SessionModeWork}
	run := func(name, arguments string, deps *weaveDeps) string {
		t.Helper()
		state, err := manager.DecodeState(session.Metadata)
		if err != nil {
			t.Fatal(err)
		}
		sink := &testToolCommitter{}
		result := manager.Run(ctx, deps, share.RunRequest{Session: session, Actor: actor, Committer: sink, DiscoveredTools: state.DiscoveredTools, Calls: []llm.ToolCallRequest{{ID: "outer", Name: name, Arguments: arguments}}})
		if result.Err != nil || len(sink.results) != 1 || len(deps.toolCalls) != 1 {
			t.Fatalf("result=%+v messages=%+v hooks=%d calls=%v", result, sink.results, deps.completed, deps.toolCalls)
		}
		return sink.results[0].Content
	}
	if text := run("discover_tool", `{"name":"tool_weave"}`, &weaveDeps{}); !strings.Contains(text, "tool_weave 可调用：发现对应工具后显示") {
		t.Fatal(text)
	}
	if text := run("discover_tool", `{"names":["web_extract","edit_file"]}`, &weaveDeps{}); !strings.Contains(text, "tool_weave 可调用：edit_file、web_extract、web_search") {
		t.Fatal(text)
	}
	state, err := manager.DecodeState(session.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := manager.bindWeave(ctx, &weaveDeps{}, *session, state.DiscoveredTools, "forged", actor)
	if err != nil {
		t.Fatal(err)
	}
	// Even a hand-written IPC request cannot enter a callable registry fallback.
	if denied, err := share.WeaveFromContext(bound).Call(ctx, "discover_tool", json.RawMessage("{}")); err != nil || denied.Status != operationresult.Forbidden {
		t.Fatalf("undiscovered invocation: %+v %v", denied, err)
	}
	user := contextinfo.WithActor(ctx, contextinfo.Actor{Role: contextinfo.RoleUser})
	if edit, err := share.WeaveFromContext(bound).Call(user, "edit_file", json.RawMessage("{}")); err != nil || edit.Status != operationresult.Success || edit.Data != "edit_file" {
		t.Fatalf("edit within approved weave: %+v %v", edit, err)
	}
	for _, tool := range state.DiscoveredTools {
		actual, _ := json.Marshal(tool.Schema)
		if tool.Name == "tool_weave" && !bytes.Equal(original, actual) {
			t.Fatal("discovery changed schema")
		}
	}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "<html><body><p>deprecated fixture</p></body></html>")
	}))
	defer server.Close()
	code := fmt.Sprintf(`import asyncio
pages = await asyncio.gather(*(web_extract(url=%q + path, method="direct", proxy="disabled") for path in ("/one", "/two")))
edits = await asyncio.gather(edit_file(), edit_file())
print(*edits, sum("deprecated" in p for p in pages))
`, server.URL)
	args, _ := json.Marshal(map[string]any{"code": code, "mode": "system"})
	rejected := &weaveDeps{runnerTestDeps: runnerTestDeps{reject: true}}
	if text := run("tool_weave", string(args), rejected); text != "rejected" || hits.Load() != 0 || rejected.completed != 0 {
		t.Fatalf("rejected weave: %q hits=%d completed=%d", text, hits.Load(), rejected.completed)
	}
	deps := &weaveDeps{}
	if text := run("tool_weave", string(args), deps); strings.TrimSpace(text) != "edit_file edit_file 2" || hits.Load() != 2 || deps.completed != 1 {
		t.Fatalf("final=%q hits=%d", text, hits.Load())
	}
	if !slices.Equal(deps.prompts, []string{"tool_weave"}) {
		t.Fatalf("weave prompts: %v", deps.prompts)
	}
	deps = &weaveDeps{}
	result := manager.Run(ctx, deps, share.RunRequest{Session: session, Actor: actor, Committer: &testToolCommitter{}, DiscoveredTools: state.DiscoveredTools, Calls: []llm.ToolCallRequest{
		{ID: "first", Name: "edit_file", Arguments: "{}"},
		{ID: "second", Name: "edit_file", Arguments: "{}"},
	}})
	if result.Err != nil || result.ConfirmationExtra != "first\n\nsecond" || !slices.Equal(deps.prompts, []string{"edit_file", "edit_file"}) {
		t.Fatalf("direct edits: result=%+v prompts=%v", result, deps.prompts)
	}
}

func TestWeaveDescriptionPermission(t *testing.T) {
	child := share.DiscoveredTool{Name: "web_search", Source: share.SourceKindNative}
	outer := share.DiscoveredTool{Name: "tool_weave", Source: share.SourceKindNative}
	user := contextinfo.Actor{Role: contextinfo.RoleUser}
	admin := contextinfo.Actor{Role: contextinfo.RoleSuperadmin}
	for _, tc := range []struct {
		name    string
		actor   contextinfo.Actor
		maxRisk security.RiskLevel
		items   []share.DiscoveredTool
		want    string
	}{
		{"denied with child", user, security.RiskLow, []share.DiscoveredTool{child}, ""},
		{"denied with cached weave", user, security.RiskLow, []share.DiscoveredTool{outer}, ""},
		{"allowed user", user, security.RiskHigh, []share.DiscoveredTool{child}, "tool_weave 可调用：web_search"},
		{"superadmin", admin, security.RiskLow, []share.DiscoveredTool{child}, "tool_weave 可调用：web_search"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			permission := security.DefaultPermission()
			permission.UserMaxToolRisk = tc.maxRisk
			catalog := registry.NewRegistry(permission)
			env := processenv.Environment{}
			if err := registerTestTools(catalog, weave.Definition(env), web.NewWebSearchTool(env).Definition()); err != nil {
				t.Fatal(err)
			}
			got, err := newTestManager(catalog, permission).WeaveDescription(contextinfo.WithActor(t.Context(), tc.actor), tc.items)
			if err != nil || got != tc.want {
				t.Fatalf("description = %q, err = %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestBackgroundRejectsWeave(t *testing.T) {
	permission := security.DefaultPermission()
	catalog := registry.NewRegistry(permission)
	outer := weave.Definition(processenv.Environment{})
	if err := registerTestTools(catalog, outer); err != nil {
		t.Fatal(err)
	}
	actor := contextinfo.Actor{Role: contextinfo.RoleSuperadmin}
	ctx := share.WithCallPolicy(contextinfo.WithActor(t.Context(), actor), share.CallPolicy{DisableForegroundTools: true})
	preloader := preload.NewPreloadService(preload.PreloadOptions{Registry: catalog, Resolver: discovery.NewResolver(catalog), NativeDiscoveredToolsFromDiscovery: discovery.NativeDiscoveredToolsFromDiscovery, BackgroundDiscoveredTools: registry.BackgroundDiscoveredTools})
	prepared, err := preloader.PrepareBackground(ctx, share.BackgroundPreloadRequest{Names: []string{"tool_weave"}})
	if err != nil || len(prepared.Update.Tools) != 0 {
		t.Fatalf("background preload: %+v err=%v", prepared, err)
	}
	// A previously discovered declaration still resolves against the current definition.
	cached := []share.DiscoveredTool{{Name: "tool_weave", Source: share.SourceKindNative, Schema: outer.Schema()}}
	manager := newTestManager(catalog, permission)
	resolved, err := manager.resolve(ctx, "tool_weave", cached, storage.SessionModeBackground)
	if err != nil || resolved.Status != operationresult.Forbidden {
		t.Fatalf("background tool resolution: %+v err=%v", resolved, err)
	}
	if description, err := manager.WeaveDescription(ctx, cached); err != nil || description != "" {
		t.Fatalf("background weave description=%q err=%v", description, err)
	}
}
