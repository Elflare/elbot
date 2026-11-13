package execution

import (
	"elbot/internal/operationresult"
	"errors"
	"testing"

	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

func TestCallToolPreservesDirectCallResult(t *testing.T) {
	catalog := registry.NewRegistry(security.DefaultPermission())
	failure := errors.New("partial failure")
	if err := registerTestTools(catalog, (loggingTool{runnerHighRiskTool: runnerHighRiskTool{name: "segments"}, err: failure}).Definition()); err != nil {
		t.Fatal(err)
	}
	m := newTestManager(catalog, nil)
	result, log, err := m.CallTool(t.Context(), share.CallRequest{Name: "segments", Arguments: []byte("{}")})
	if err != failure || result.Data != nil || log == "" {
		t.Fatalf("result=%+v log=%q err=%v", result, log, err)
	}
	if result, log, err := m.CallTool(t.Context(), share.CallRequest{Name: "missing"}); err != nil || result.Status != operationresult.NotFound || result.Message != `tool "missing" not found` || log != "" {
		t.Fatalf("missing tool: result=%+v log=%q err=%v", result, log, err)
	}
}

func TestRemoteResolutionUsesModelName(t *testing.T) {
	m := newTestManager(registry.NewRegistry(security.DefaultPermission()), nil)
	items := []share.DiscoveredTool{
		{Name: "status", CanonicalName: "first_status", Source: share.SourceKindELwisp, Endpoint: "http://first.invalid", EventKey: "first"},
		{Name: "status", CanonicalName: "second_status", Source: share.SourceKindELwisp, Endpoint: "http://second.invalid", EventKey: "second"},
	}
	for _, item := range items {
		got, err := m.resolve(t.Context(), item.CanonicalName, items, storage.SessionModeBackground)
		if err != nil || got.Status != operationresult.Success || got.Data.Discovered.Endpoint != item.Endpoint || got.Data.Discovered.EventKey != item.EventKey {
			t.Fatalf("resolved=%+v", got)
		}
	}
	got, err := m.resolve(t.Context(), "status", items, storage.SessionModeBackground)
	if err != nil || got.Status != operationresult.Forbidden {
		t.Fatalf("ambiguous original name accepted: %+v", got)
	}
}
