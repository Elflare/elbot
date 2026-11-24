package tool

import (
	"slices"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/storage"
	"elbot/internal/testutil"
)

func TestDraftDependencies(t *testing.T) {
	m, _ := newLifecycleTool(t, t.TempDir())
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	names := []string{"tool_weave", "shell", "edit_file"}
	for _, name := range names {
		prepared, err := m.PrepareTools(testutil.ForegroundContext(ctx), []string{name})
		if err != nil || !slices.ContainsFunc(prepared.Update.Tools, func(item DiscoveredTool) bool { return item.Name == "draft" }) {
			t.Errorf("%s did not bring in draft: %v", name, err)
		}
	}
	bg, err := m.PrepareBackground(testutil.TaskContext(ctx, "cron"), BackgroundPreloadRequest{SessionID: "background", Names: names})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"shell": true, "edit_file": true, "draft": false, "tool_weave": false} {
		if got := slices.ContainsFunc(bg.Update.Tools, func(item DiscoveredTool) bool { return item.Name == name }); got != want {
			t.Errorf("background %s: got %t, want %t", name, got, want)
		}
	}
}

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
		{[]string{"shell"}, "tool_weave 可调用：draft、workspace"},
		{[]string{"tool_weave"}, "tool_weave 可调用：draft、workspace"},
		{[]string{"read_file"}, "tool_weave 可调用：draft、read_file、rollback_file、workspace"},
		{[]string{"web_extract"}, "tool_weave 可调用：draft、read_file、rollback_file、web_extract、web_search、workspace"},
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
