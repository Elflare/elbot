package execution

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/events"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

func TestCallToolPreservesDirectCallResult(t *testing.T) {
	catalog := registry.NewRegistry(security.DefaultPermission())
	failure := errors.New("partial failure")
	if err := catalog.Register(partialResultTool{err: failure}); err != nil {
		t.Fatal(err)
	}
	m := newTestManager(catalog, nil)
	result, log, err := m.CallTool(t.Context(), share.CallRequest{Name: "segments", Arguments: []byte("{}")})
	if err != failure || result == nil || string(result.Data) != `{"partial":true}` || log == "" {
		t.Fatalf("result=%+v log=%q err=%v", result, log, err)
	}
	if _, _, err := m.CallTool(t.Context(), share.CallRequest{Name: "missing"}); err == nil {
		t.Fatal("missing tool accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := m.CallTool(ctx, share.CallRequest{Name: "segments"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestRemoteResolutionUsesModelName(t *testing.T) {
	m := newTestManager(registry.NewRegistry(security.DefaultPermission()), nil)
	items := []share.DiscoveredTool{
		{Name: "status", CanonicalName: "first_status", Source: share.SourceKindELwisp, Endpoint: "http://first.invalid", EventKey: "first"},
		{Name: "status", CanonicalName: "second_status", Source: share.SourceKindELwisp, Endpoint: "http://second.invalid", EventKey: "second"},
	}
	for _, item := range items {
		got := m.resolve(t.Context(), item.CanonicalName, items, storage.SessionModeBackground)
		if got.ResultStatus != events.ResultSucceeded || got.Discovered.Endpoint != item.Endpoint || got.Discovered.EventKey != item.EventKey {
			t.Fatalf("resolved=%+v", got)
		}
	}
	got := m.resolve(t.Context(), "status", items, storage.SessionModeBackground)
	if got.ResultStatus != events.ResultRejected {
		t.Fatalf("ambiguous original name accepted: %+v", got)
	}
}
