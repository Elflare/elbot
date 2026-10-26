package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/events"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestCleanupSandboxDeletesOldFilesAndEmptyDirs(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "cron", "old")
	newDir := filepath.Join(root, "elnis", "new")

	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(oldDir, "old.txt")
	newFile := filepath.Join(newDir, "new.txt")
	if err := os.WriteFile(oldFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().AddDate(0, 0, -10)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	deleted, err := cleanupSandbox(context.Background(), root, time.Now().AddDate(0, 0, -7))

	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("old file still exists or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old dir still exists or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("sandbox root missing: %v", err)
	}
	if _, err := os.Stat(newFile); err != nil {

		t.Fatalf("new file missing: %v", err)
	}
}

func TestCleanupUsesInjectedLiveSessionService(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	live := session.NewService(store)
	scope := session.Scope{ActorID: "u", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	row, err := live.Create(ctx, scope, session.CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, binding, err := live.CurrentBound(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Sessions().Mutate(ctx, row.ID, func(row *storage.Session) error { row.UpdatedAt = time.Now().AddDate(0, 0, -10); return nil })
	if err != nil {
		t.Fatal(err)
	}
	busy := true
	live.SetActivitySource(func() []string {
		if busy {
			return []string{row.ID}
		}
		return nil
	})
	svc := NewServiceWithConfig(nil, nil, &config.Config{Maintenance: config.MaintenanceConfig{SessionCleanup: config.MaintenanceCleanupConfig{Enabled: true, RetentionDays: 1}}}, live.CleanupExpired)
	if logResult, err := svc.RunSessionCleanup(ctx); err != nil || logResult.ResultStatus != events.ResultSucceeded || !strings.Contains(logResult.Content, "deleted=0") {
		t.Fatalf("cleanup=%#v error=%v", logResult, err)
	}
	if !binding.Valid() {
		t.Fatal("busy current was deleted")
	}
	busy = false
	if logResult, err := svc.RunSessionCleanup(ctx); err != nil || logResult.ResultStatus != events.ResultSucceeded || !strings.Contains(logResult.Content, "deleted=1") {
		t.Fatalf("cleanup=%#v error=%v", logResult, err)
	}
	if binding.Valid() {
		t.Fatal("idle cleanup did not invalidate live binding")
	}
}

func TestSandboxCleanupReturnsSourceResult(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		ctx       context.Context
		retention int
		want      events.ResultStatus
		err       error
	}{
		{"canceled", func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }(), 1, events.ResultCanceled, context.Canceled},
		{"deadline", func() context.Context {
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			return ctx
		}(), 1, events.ResultFailed, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{sandboxRoot: root, sandboxCleanup: config.MaintenanceCleanupConfig{RetentionDays: tc.retention}}
			logResult, err := svc.RunSandboxCleanup(tc.ctx)
			if logResult.ResultStatus != tc.want || !errors.Is(err, tc.err) || logResult.Content == "" {
				t.Fatalf("source result=%#v error=%v", logResult, err)
			}
		})
	}
}
