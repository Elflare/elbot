package state

import (
	"elbot/internal/security"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
	"testing"
)

func TestRemoteRegistrationCommitsOnlyItsSessionAndEvent(t *testing.T) {
	store, row := stateTestStore(t, `{"workspace":"keep"}`)
	service := NewStateService(store.Sessions(), registry.BackgroundDiscoveredTools)
	catalog := registry.NewRegistry(security.DefaultPermission())
	register := func(event, name string) share.StateUpdate {
		t.Helper()
		var definitions []share.ToolDefinition
		if name != "" {
			definitions = []share.ToolDefinition{{Metadata: share.Info{Name: name, Description: "remote", Risk: share.RiskLow}, Parameters: map[string]any{"type": "object", "properties": map[string]any{}}, Remote: &share.ELwispToolDeclaration{Name: name, CanonicalName: name, EventKey: event, Endpoint: "https://example.invalid"}}}
		}
		update, err := catalog.RegisterTools(t.Context(), share.SourceELwisp, share.ToolRegistrationScope{SessionID: row.ID, EventKey: event}, definitions)
		if err != nil {
			t.Fatal(err)
		}
		return update
	}
	first := register("first", "old")
	unchanged, _ := store.Sessions().Get(t.Context(), row.ID)
	if unchanged.Metadata != row.Metadata || len(catalog.List()) != 0 {
		t.Fatal("registration published before commit")
	}
	if _, _, err := service.Commit(t.Context(), "another", first); err == nil {
		t.Fatal("wrong session accepted")
	}
	for _, update := range []share.StateUpdate{first, register("second", "keep"), register("first", "new")} {
		if _, _, err := service.Commit(t.Context(), row.ID, update); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := stateTestSnapshot(t.Context(), store, row.ID)
	if err != nil || len(loaded.DiscoveredTools) != 2 || loaded.DiscoveredTools[0].Name != "keep" || loaded.DiscoveredTools[1].Name != "new" || loaded.DiscoveredTools[1].TimeoutSeconds != 10 || loaded.DiscoveredTools[1].Risk != share.RiskLow {
		t.Fatalf("restored=%+v err=%v", loaded, err)
	}
	before, _ := store.Sessions().Get(t.Context(), row.ID)
	if _, _, err := service.Commit(t.Context(), row.ID, register("first", "keep")); err == nil {
		t.Fatal("cross-event collision accepted")
	}
	after, _ := store.Sessions().Get(t.Context(), row.ID)
	if after.Metadata != before.Metadata {
		t.Fatal("rejected patch changed persisted state")
	}
	for _, event := range []string{"first", "second"} {
		if _, _, err := service.Commit(t.Context(), row.ID, register(event, "")); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err = stateTestSnapshot(t.Context(), store, row.ID)
	if err != nil || len(loaded.DiscoveredTools) != 0 {
		t.Fatal(loaded, err)
	}
}
