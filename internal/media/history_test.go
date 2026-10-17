package media

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil"
)

type brokenHistory struct{ storage.ChatHistoryRepository }

func (brokenHistory) GetByPlatformMessage(context.Context, string, string, string) (*storage.ChatMessage, error) {
	return nil, errors.New("history offline")
}

func TestHistoryReconciliationAfterInterruptedDeletion(t *testing.T) {
	ctx := context.Background()
	store := testutil.NewSQLiteStore(t)
	history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	root := t.TempDir()
	m := NewManager(store, root, &LocalBackend{Root: root})
	m.History = history.Repository()
	msg := &storage.ChatMessage{Platform: "p", PlatformScopeID: "s", PlatformMessageID: "1", SenderID: "u", CreatedAt: time.Now().Add(-24 * time.Hour)}
	if err := m.History.Append(ctx, msg); err != nil {
		t.Fatal(err)
	}
	item, err := m.ImportBytes(ctx, []byte("media"), Input{Name: "image.png"})
	if err != nil {
		t.Fatal(err)
	}
	association := storage.HistoryMedia{HistoryID: msg.ID, Platform: "p", ScopeID: "s", MessageID: "1", MediaIndex: 1, Kind: "image", MediaID: item.ID}
	if err := store.Media().SaveHistory(ctx, association); err != nil {
		t.Fatal(err)
	}
	if err := m.ReconcileHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if refs, _ := store.MediaReferences().ListMediaIDs(ctx, item.ID); len(refs) != 1 {
		t.Fatal(refs)
	}
	m.History = brokenHistory{}
	if _, err := m.Cleanup(ctx); err == nil {
		t.Fatal("history failure ignored")
	}
	if refs, _ := store.MediaReferences().ListMediaIDs(ctx, item.ID); len(refs) != 1 {
		t.Fatal("offline history released reference")
	}
	m.History = history.Repository()
	// Simulate process interruption after the history transaction but before main DB cleanup.
	if _, err := m.History.DeleteBefore(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if refs, _ := store.MediaReferences().ListMediaIDs(ctx, item.ID); len(refs) != 1 {
		t.Fatal("expected interrupted reference")
	}
	if err := m.ReconcileHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if refs, _ := store.MediaReferences().ListMediaIDs(ctx, item.ID); len(refs) != 0 {
		t.Fatal(refs)
	}
	if _, err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Metadata(ctx, item.ID); err != nil {
		t.Fatal("grace not preserved", err)
	}
	// Reusing a platform message ID must not revive associations belonging to the deleted history row.
	if err := store.Media().SaveHistory(ctx, association); err != nil {
		t.Fatal(err)
	}
	msg.ID = ""
	if err := m.History.Append(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if err := m.ReconcileHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if refs, _ := store.MediaReferences().ListMediaIDs(ctx, item.ID); len(refs) != 0 {
		t.Fatal("new row inherited old reference", refs)
	}
}

func TestHistoryReconciliationHandlesAttachmentsAcrossPages(t *testing.T) {
	for _, name := range []string{"present", "missing", "replacement", "offline"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			store := testutil.NewSQLiteStore(t)
			history, err := sqlite.NewChatHistory(ctx, ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = history.Close() })
			root := t.TempDir()
			manager := NewManager(store, root, &LocalBackend{Root: root})
			manager.History = history.Repository()
			message := &storage.ChatMessage{Platform: "p", PlatformScopeID: "s", PlatformMessageID: "m", CreatedAt: time.Now().Add(-time.Hour)}
			if err := manager.History.Append(ctx, message); err != nil {
				t.Fatal(err)
			}
			item, err := manager.ImportBytes(ctx, []byte("shared attachments"), Input{Name: "image.png"})
			if err != nil {
				t.Fatal(err)
			}
			for index := 1; index <= 105; index++ {
				if err := store.Media().SaveHistory(ctx, storage.HistoryMedia{HistoryID: message.ID, Platform: "p", ScopeID: "s", MessageID: "m", MediaIndex: index, Kind: "image", MediaID: item.ID}); err != nil {
					t.Fatal(err)
				}
			}
			if name == "missing" || name == "replacement" {
				if _, err := manager.History.DeleteBefore(ctx, time.Now()); err != nil {
					t.Fatal(err)
				}
				if name == "replacement" {
					message.ID = ""
					if err := manager.History.Append(ctx, message); err != nil {
						t.Fatal(err)
					}
				}
			}
			if name == "offline" {
				manager.History = brokenHistory{}
			}
			err = manager.ReconcileHistory(ctx)
			if (err != nil) != (name == "offline") {
				t.Fatalf("reconcile error=%v", err)
			}
			want := 0
			if name == "present" || name == "offline" {
				want = 105
			}
			refs, err := store.MediaReferences().ListMediaIDs(ctx, item.ID)
			if err != nil || len(refs) != want {
				t.Fatalf("remaining owners=%d want=%d error=%v", len(refs), want, err)
			}
		})
	}
}
