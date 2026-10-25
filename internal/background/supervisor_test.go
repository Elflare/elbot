package background

import (
	"context"
	"testing"
	"time"

	agentshare "elbot/internal/agent/share"
	background "elbot/internal/background/share"
	"elbot/internal/config"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool/preload"
	"elbot/internal/tool/registry"
	toolstate "elbot/internal/tool/state"
	"elbot/internal/turn"
)

type backgroundExecutionStub struct{ sessionID string }

func (s *backgroundExecutionStub) ModelContext(ctx context.Context, _ *storage.Session) (context.Context, error) {
	return ctx, nil
}

func (s *backgroundExecutionStub) RunBackground(_ context.Context, row *storage.Session, _ string) turn.Result {
	s.sessionID = row.ID
	return turn.Result{SessionID: row.ID, Text: "borrowed execution", Outcome: "completed"}
}

func backgroundDeps(t *testing.T) SupervisorDeps {
	t.Helper()
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessions := session.NewService(store)
	t.Cleanup(func() { _ = sessions.Close(context.Background()) })
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local", Permission: security.DefaultPermission()})
	registry := registry.NewRegistry(security.DefaultPermission())
	return SupervisorDeps{Store: store, Sessions: sessions, Identity: identity, Execution: &backgroundExecutionStub{}, Preloader: preload.NewPreloadService(preload.PreloadOptions{Registry: registry}), ToolState: toolstate.NewStateService(store)}
}

func TestBackgroundSharesDialogueStateAndDoesNotCloseBorrowedSessions(t *testing.T) {
	deps := backgroundDeps(t)
	cfg := config.Default()
	cfg.Sandbox.Root = t.TempDir()
	m, err := NewSupervisor(t.Context(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer m.Close(ctx)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if m.startupDone != nil || m.elnisDone != nil {
		t.Fatal("disabled workers started")
	}
	result, err := m.RunBackground(t.Context(), background.RunRequest{Kind: background.KindCron, Name: "borrowed", Prompt: "run"})
	if err != nil || result.Text != "borrowed execution" {
		t.Fatal(result, err)
	}
	if result.SessionID != deps.Execution.(*backgroundExecutionStub).sessionID {
		t.Fatal("execution replaced")
	}
	row, err := deps.Store.Sessions().Get(t.Context(), result.SessionID)
	if err != nil || row == nil {
		t.Fatal("task did not use the borrowed Session store", row, err)
	}
	for range 2 {
		if err := m.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-deps.Sessions.Done():
		t.Fatal("borrowed session service closed")
	default:
	}
}
