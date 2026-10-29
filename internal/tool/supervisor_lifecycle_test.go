package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/security"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func newLifecycleTool(t *testing.T, root string) (*Supervisor, storage.Store) {
	t.Helper()
	store, err := sqlite.New(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Default()
	cfg.ConfigPath = filepath.Join(root, "app.toml")
	cfg.Sandbox.Root = root
	m, err := NewSupervisor(t.Context(), cfg, SupervisorDeps{Store: store, Permission: security.DefaultPermission()})
	if m != nil {
		t.Cleanup(func() {
			if err := m.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	return m, store
}

func writeLifecycleSkill(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "skills", "agent", "startup_skill")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: startup_skill\ndescription: Startup guidance\n---\nUse the startup instructions."), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorInitialSkillPreloadUsesRealStartup(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreground", true: "background"}[background], func(t *testing.T) {
			root := t.TempDir()
			writeLifecycleSkill(t, root)
			m, _ := newLifecycleTool(t, root)
			if err := m.Start(); err != nil {
				t.Fatal(err)
			}
			ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{ID: "owner", Role: contextinfo.RoleSuperadmin})
			var names []string
			var content string
			if background {
				ctx = contextinfo.WithBackgroundKind(ctx, "cron")
				prepared, err := m.PrepareBackground(ctx, BackgroundPreloadRequest{SessionID: "startup", Names: []string{"startup_skill"}})
				if err != nil {
					t.Fatal(err)
				}
				names, content = prepared.Skills, prepared.SkillPrompt
			} else {
				prepared, err := m.PrepareSkills(ctx, "startup", []string{"startup_skill"})
				if err != nil {
					t.Fatal(err)
				}
				names, content = prepared.Names, prepared.Content
			}
			if !slices.Equal(names, []string{"startup_skill"}) || !strings.Contains(content, "Use the startup instructions.") {
				t.Fatalf("names=%v content=%q", names, content)
			}
			if err := m.Remove(ctx, "startup_skill"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "skills", "agent", "startup_skill")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if _, ok, err := m.ToolInfo(ctx, "startup_skill"); err != nil || ok {
				t.Fatalf("removed skill: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestSupervisorLoadingErrorRequiresExplicitReload(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "skills"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	m, _ := newLifecycleTool(t, root)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	_, failure := m.List(t.Context())
	if failure == nil {
		t.Fatal("invalid skill directory was accepted")
	}
	if _, _, err := m.ToolInfo(t.Context(), "startup_skill"); !errors.Is(err, failure) {
		t.Fatalf("ToolInfo error=%v", err)
	}
	if _, _, err := m.ToolSchema(t.Context(), "startup_skill"); !errors.Is(err, failure) {
		t.Fatalf("ToolSchema error=%v", err)
	}
	if err := os.Remove(filepath.Join(root, "skills")); err != nil {
		t.Fatal(err)
	}
	writeLifecycleSkill(t, root)
	if _, err := m.List(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("query retried failed scan: %v", err)
	}
	if err := m.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	info, ok, err := m.ToolInfo(t.Context(), "startup_skill")
	if err != nil || !ok || info.Name != "startup_skill" {
		t.Fatalf("reload result=%+v ok=%v err=%v", info, ok, err)
	}
}

func TestSupervisorCloseWaitsForCronNotificationAndBorrowsStore(t *testing.T) {
	m, store := newLifecycleTool(t, t.TempDir())
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	connection, err := m.ObserveCronChanged(func(event CronChangedEvent) error {
		_, err := store.CronJobs().GetByName(context.Background(), event.Name)
		close(entered)
		<-release
		return err
	}, signal.ConnectOptions{Shutdown: signal.Drain})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{ID: "owner", Platform: "cli", Role: contextinfo.RoleSuperadmin})
	written := make(chan error, 1)
	go func() {
		_, _, err := m.CallTool(ctx, CallRequest{Name: "cron_write", Arguments: json.RawMessage(`{"operation":"create","name":"startup","schedule_mode":"cron","cron_expr":"* * * * *","trigger_mode":"direct","message":"hello"}`)})
		written <- err
	}()
	<-entered
	m.BeginClose()
	if _, err := m.List(t.Context()); err != nil {
		t.Fatalf("BeginClose removed capabilities: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	closeErr := m.Close(canceled)
	select {
	case <-m.Done():
		t.Error("Close completed before notification returned")
	default:
	}
	close(release)
	if !errors.Is(closeErr, context.Canceled) {
		t.Fatalf("Close=%v", closeErr)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CronJobs().GetByName(t.Context(), "user.cron.startup"); err != nil {
		t.Fatalf("borrowed store was closed: %v", err)
	}
}

func TestSupervisorCloseBeforeStart(t *testing.T) {
	m, _ := newLifecycleTool(t, t.TempDir())
	if _, err := m.List(t.Context()); err == nil {
		t.Fatal("unstarted catalog reported ready")
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err == nil {
		t.Fatal("closed Tool restarted")
	}
}
