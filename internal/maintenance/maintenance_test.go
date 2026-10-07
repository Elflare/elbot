package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/config"
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
	svc := NewService(nil, store, config.MaintenanceCleanupConfig{Enabled: true, RetentionDays: 1})
	svc.Sessions = live
	if err := svc.RunSessionCleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if !binding.Valid() {
		t.Fatal("busy current was deleted")
	}
	busy = false
	if err := svc.RunSessionCleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if binding.Valid() {
		t.Fatal("idle cleanup did not invalidate live binding")
	}
}
