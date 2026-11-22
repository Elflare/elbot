package tool

import (
	"slices"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/storage"
)

func TestWeavePreloadReflectsFullCommittedState(t *testing.T) {
	m, store := newLifecycleTool(t, t.TempDir())
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	actor := contextinfo.Actor{Role: contextinfo.RoleSuperadmin, ActorIdentity: contextinfo.ActorIdentity{Platform: "cli", ID: "owner"}}
	ctx := contextinfo.WithActor(t.Context(), actor)
	row := storage.Session{ID: storage.NewID(), Owner: actor.ActorIdentity, Platform: "cli", ChatType: "private", ChatID: "local", Mode: storage.SessionModeWork}
	if err := store.Sessions().Create(ctx, &row); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		names []string
		want  string
	}{
		{[]string{"shell"}, "tool_weave 可调用：workspace"},
		{[]string{"tool_weave"}, "tool_weave 可调用：workspace"},
		{[]string{"read_file"}, "tool_weave 可调用：read_file、rollback_file、workspace"},
		{[]string{"web_extract"}, "tool_weave 可调用：read_file、rollback_file、web_extract、web_search、workspace"},
	} {
		prepared, err := m.PrepareTools(ctx, step.names)
		if err != nil {
			t.Fatal(err)
		}
		committed, err := m.Commit(ctx, row.ID, prepared.Update)
		if err != nil || committed.WeaveDescription != step.want {
			t.Fatalf("preload %v: %q, %v", step.names, committed.WeaveDescription, err)
		}
	}
	prepared, err := m.PrepareTools(ctx, []string{"files"})
	if err != nil || !slices.ContainsFunc(prepared.Update.Tools, func(item DiscoveredTool) bool { return item.Name == "tool_weave" }) {
		t.Fatalf("files tag did not preload tool_weave: %v", err)
	}
}
