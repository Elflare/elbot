package agent

import (
	"context"
	"encoding/json"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

func newWorkspaceTestSession(t *testing.T, ctx context.Context, store storage.Store, metadata string) *storage.Session {
	t.Helper()
	session := &storage.Session{
		OwnerID:         "cli:local",
		Platform:        "cli",
		PlatformScopeID: "local",
		Mode:            storage.SessionModeWork,
		Status:          storage.SessionStatusActive,
		Title:           "test",
		Metadata:        metadata,
	}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	return session
}

func TestSessionWorkspaceStorePersistsWithoutDroppingMetadata(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	session := newWorkspaceTestSession(t, ctx, store, `{"unknown":"keep","tool_tags":["agent"]}`)
	agent := &Agent{store: store}
	workspaceStore := sessionWorkspaceStore{agent: agent, session: session}
	if err := workspaceStore.SetWorkspaceDir(ctx, "C:/work/project"); err != nil {
		t.Fatal(err)
	}
	latest, err := store.Sessions().Get(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	if metadata.WorkspaceDir != "C:/work/project" {
		t.Fatalf("workspace dir = %q", metadata.WorkspaceDir)
	}
	if len(metadata.ToolTags) != 1 || metadata.ToolTags[0] != "agent" {
		t.Fatalf("tool tags = %#v", metadata.ToolTags)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(latest.Metadata), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["unknown"] != "keep" {
		t.Fatalf("unknown metadata dropped: %s", latest.Metadata)
	}
	got, err := workspaceStore.GetWorkspaceDir(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "C:/work/project" {
		t.Fatalf("GetWorkspaceDir = %q", got)
	}
}

func TestSessionWorkspaceStoreClear(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	session := newWorkspaceTestSession(t, ctx, store, `{"workspace_dir":"C:/work/project","unknown":"keep"}`)
	agent := &Agent{store: store}
	workspaceStore := sessionWorkspaceStore{agent: agent, session: session}
	if err := workspaceStore.ClearWorkspaceDir(ctx); err != nil {
		t.Fatal(err)
	}
	latest, err := store.Sessions().Get(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	if metadata.WorkspaceDir != "" {
		t.Fatalf("workspace dir = %q", metadata.WorkspaceDir)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(latest.Metadata), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["unknown"] != "keep" {
		t.Fatalf("unknown metadata dropped: %s", latest.Metadata)
	}
}

func TestSessionWorkspaceStoreIsSessionScoped(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	first := newWorkspaceTestSession(t, ctx, store, "")
	second := newWorkspaceTestSession(t, ctx, store, "")
	agent := &Agent{store: store}
	firstStore := sessionWorkspaceStore{agent: agent, session: first}
	secondStore := sessionWorkspaceStore{agent: agent, session: second}
	if err := firstStore.SetWorkspaceDir(ctx, "C:/first"); err != nil {
		t.Fatal(err)
	}
	if err := secondStore.SetWorkspaceDir(ctx, "C:/second"); err != nil {
		t.Fatal(err)
	}
	gotFirst, err := firstStore.GetWorkspaceDir(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotSecond, err := secondStore.GetWorkspaceDir(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotFirst != "C:/first" || gotSecond != "C:/second" {
		t.Fatalf("session workspace not isolated: first=%q second=%q", gotFirst, gotSecond)
	}
}

func TestAgentToolRunDepsInjectsWorkspaceStoreForForegroundOnly(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	foreground := newWorkspaceTestSession(t, ctx, store, "")
	background := newWorkspaceTestSession(t, ctx, store, `{"background_kind":"cron"}`)
	deps := agentToolRunDeps{agent: &Agent{store: store}}
	withStore := deps.PrepareToolContext(ctx, foreground, llm.ToolCallRequest{Name: "read_file"})
	if _, ok := tool.WorkspaceStoreFromContext(withStore); !ok {
		t.Fatal("expected foreground workspace store")
	}
	backgroundCtx := deps.PrepareToolContext(ctx, background, llm.ToolCallRequest{Name: "shell"})
	if _, ok := tool.WorkspaceStoreFromContext(backgroundCtx); ok {
		t.Fatal("background tool must not get foreground workspace store")
	}
}
