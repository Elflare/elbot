package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
	"elbot/internal/workspace"
)

type weaveDeps struct {
	runnerTestDeps
	wait bool
}

func (d *weaveDeps) ConfirmToolCall(ctx context.Context, s string, call llm.ToolCallRequest, risk share.RiskAssessment, details []share.ConfirmationDetailBlock) (operationresult.Result[share.ConfirmResult], error) {
	if call.Name == "restricted" {
		if d.wait {
			<-ctx.Done()
			return operationresult.Result[share.ConfirmResult]{}, ctx.Err()
		}
		return operationresult.Result[share.ConfirmResult]{Status: operationresult.Rejected, Message: "denied"}, nil
	}
	return d.runnerTestDeps.ConfirmToolCall(ctx, s, call, risk, details)
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
	restricted := (fakeTool{name: "restricted", risk: share.RiskHigh}).Definition()
	restricted.Definition.Weavable = true
	if err := registerTestTools(catalog, outer, web.NewWebExtractTool(env).Definition(), web.NewWebSearchTool(env).Definition(), restricted, discovery.NewDiscoverTool(discovery.NewResolver(catalog))); err != nil {
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
		if result.Err != nil || len(sink.results) != 1 || deps.completed != 1 || len(deps.toolCalls) != 1 {
			t.Fatalf("result=%+v messages=%+v hooks=%d calls=%v", result, sink.results, deps.completed, deps.toolCalls)
		}
		return sink.results[0].Content
	}
	if text := run("discover_tool", `{"name":"tool_weave"}`, &weaveDeps{}); !strings.Contains(text, "tool_weave 可调用：暂无") {
		t.Fatal(text)
	}
	if text := run("discover_tool", `{"names":["web_extract","restricted"]}`, &weaveDeps{}); !strings.Contains(text, "tool_weave 可调用：restricted、web_extract、web_search") {
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
try:
    await restricted()
except RuntimeError as exc:
    print(str(exc), sum("deprecated" in p for p in pages))
`, server.URL)
	args, _ := json.Marshal(map[string]any{"code": code, "mode": "system"})
	if text := run("tool_weave", string(args), &weaveDeps{}); strings.TrimSpace(text) != "denied 2" || hits.Load() != 2 {
		t.Fatalf("final=%q hits=%d", text, hits.Load())
	}
	text := run("tool_weave", `{"code":"await restricted()","mode":"system","timeout":1}`, &weaveDeps{wait: true})
	if !strings.Contains(text, "deadline exceeded") {
		t.Fatal(text)
	}
}
