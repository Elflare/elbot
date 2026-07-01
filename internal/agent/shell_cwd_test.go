package agent

import (
	"context"
	"encoding/json"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

func newShellCWDTestSession(t *testing.T, ctx context.Context, store storage.Store, metadata string) *storage.Session {
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

func TestSessionShellCWDStorePersistsWithoutDroppingMetadata(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	session := newShellCWDTestSession(t, ctx, store, `{"unknown":"keep","tool_tags":["agent"]}`)
	agent := &Agent{store: store}
	cwdStore := sessionShellCWDStore{agent: agent, session: session}
	if err := cwdStore.SetShellCWD(ctx, "C:/work/project"); err != nil {
		t.Fatal(err)
	}
	latest, err := store.Sessions().Get(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	if metadata.ShellCWD != "C:/work/project" {
		t.Fatalf("shell cwd = %q", metadata.ShellCWD)
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
	got, err := cwdStore.GetShellCWD(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "C:/work/project" {
		t.Fatalf("GetShellCWD = %q", got)
	}
}

func TestSessionShellCWDStoreIsSessionScoped(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	first := newShellCWDTestSession(t, ctx, store, "")
	second := newShellCWDTestSession(t, ctx, store, "")
	agent := &Agent{store: store}
	firstStore := sessionShellCWDStore{agent: agent, session: first}
	secondStore := sessionShellCWDStore{agent: agent, session: second}
	if err := firstStore.SetShellCWD(ctx, "C:/first"); err != nil {
		t.Fatal(err)
	}
	if err := secondStore.SetShellCWD(ctx, "C:/second"); err != nil {
		t.Fatal(err)
	}
	gotFirst, err := firstStore.GetShellCWD(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotSecond, err := secondStore.GetShellCWD(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotFirst != "C:/first" || gotSecond != "C:/second" {
		t.Fatalf("session cwd not isolated: first=%q second=%q", gotFirst, gotSecond)
	}
}

func TestAgentToolRunDepsInjectsShellCWDStoreForForegroundShellOnly(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	foreground := newShellCWDTestSession(t, ctx, store, "")
	background := newShellCWDTestSession(t, ctx, store, `{"background_kind":"cron"}`)
	deps := agentToolRunDeps{agent: &Agent{store: store}}
	withStore := deps.PrepareToolContext(ctx, foreground, llm.ToolCallRequest{Name: "shell"})
	if _, ok := tool.ShellCWDStoreFromContext(withStore); !ok {
		t.Fatal("expected foreground shell cwd store")
	}
	backgroundCtx := deps.PrepareToolContext(ctx, background, llm.ToolCallRequest{Name: "shell"})
	if _, ok := tool.ShellCWDStoreFromContext(backgroundCtx); ok {
		t.Fatal("background shell must not get foreground cwd store")
	}
	nonShellCtx := deps.PrepareToolContext(ctx, foreground, llm.ToolCallRequest{Name: "read_file"})
	if _, ok := tool.ShellCWDStoreFromContext(nonShellCtx); ok {
		t.Fatal("non-shell tool must not get shell cwd store")
	}
}
