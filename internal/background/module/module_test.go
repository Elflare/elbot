package module

import (
	"context"
	"testing"
	"time"

	"elbot/internal/agent"
	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
	"elbot/internal/toolrun"
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
func backgroundDeps(t *testing.T) ModuleDeps {
	t.Helper()
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessions := session.NewService(store)
	t.Cleanup(func() { _ = sessions.Close(context.Background()) })
	identity, err := agent.NewIdentityResolver(agent.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local", Policy: security.DefaultPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	return ModuleDeps{Store: store, Sessions: sessions, Identity: identity, Execution: &backgroundExecutionStub{}, Registry: registry, Preloader: toolrun.NewPreloadService(toolrun.PreloadOptions{Registry: registry}), ToolState: toolrun.NewStateService(store), SandboxRoot: t.TempDir(), Maintenance: config.Default().Maintenance}
}
func TestBackgroundSharesDialogueStateAndReleasesOnlyItsTools(t *testing.T) {
	deps := backgroundDeps(t)
	sentinel := builtin.NewWorkspaceTool()
	if err := deps.Registry.Register(sentinel); err != nil {
		t.Fatal(err)
	}
	m, err := NewModule(t.Context(), deps)
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
	for _, name := range []string{"cron", "cron_query", "cron_write"} {
		if _, ok := deps.Registry.Get(name); ok {
			t.Fatal("owned registration leaked", name)
		}
	}
	if got, ok := deps.Registry.Get(sentinel.Name()); !ok || got != sentinel {
		t.Fatal("borrowed registration removed")
	}
	select {
	case <-deps.Sessions.Done():
		t.Fatal("borrowed session service closed")
	default:
	}
}
func TestBackgroundToolConflictIsAtomicAndDoesNotReleaseForeignRegistration(t *testing.T) {
	deps := backgroundDeps(t)
	foreignValue := builtin.NewCronTools(nil)[0].(builtin.CronTool)
	foreign := &foreignValue
	if err := deps.Registry.Register(foreign); err != nil {
		t.Fatal(err)
	}
	m, err := NewModule(t.Context(), deps)
	if err == nil || m == nil {
		t.Fatal(m, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got, ok := deps.Registry.Get("cron"); !ok || got != foreign {
		t.Fatal("foreign registration was removed")
	}
	if _, ok := deps.Registry.Get("cron_query"); ok {
		t.Fatal("failed batch partially registered tools")
	}
}
