package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type trackingDeleteBackend struct {
	Backend
	name  string
	calls *[]string
	fail  bool
}

func TestCleanupWithEmptyNativeSeedDeletesOrphans(t *testing.T) {
	m := newConcurrentTestManager(t)
	seed := &storage.NativeSeed{ID: "empty-root", APIType: "response", Provider: "p", ItemsJSON: `[]`, MaterialsJSON: `[]`, ContinuationJSON: `[]`, CallsJSON: `[]`}
	row := &storage.Session{ID: "empty-session", Metadata: `{"llm_origin":{"protocol":"response","provider":"p"}}`}
	if err := m.Store.Sessions().CreateMaterial(t.Context(), storage.SessionMaterialCreate{Session: row, Seed: seed}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ImportBytes(t.Context(), []byte("orphan"), Input{}); err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	result, err := m.Cleanup(t.Context())
	if err != nil || result.ResultStatus != events.ResultSucceeded || !strings.Contains(result.Content, "claimed=1 deleted=1 skipped=0 failed=0 canceled=0 not_started=0") {
		t.Fatalf("empty seed blocked cleanup: %#v, %v", result, err)
	}
	result, err = m.Cleanup(t.Context())
	if err != nil || !strings.Contains(result.Content, "claimed=0 deleted=0 skipped=0 failed=0 canceled=0 not_started=0") {
		t.Fatalf("empty cleanup: %#v, %v", result, err)
	}
}

func TestCleanupSummaryCountsAndBoundsDetails(t *testing.T) {
	m := newConcurrentTestManager(t)
	data := seedCleanupMedia(t, m, 60)
	failedIDs := map[string]bool{}
	for id := range data {
		if len(failedIDs) < 2 {
			failedIDs[id] = true
		}
	}
	failure := errors.New("remote error\n\nretry later")
	m.Remote = &concurrentTestBackend{remove: func(_ context.Context, item *storage.Media) error {
		if failedIDs[item.ID] {
			return failure
		}
		return nil
	}}
	result, err := m.Cleanup(t.Context())
	if !errors.Is(err, failure) || result.ResultStatus != events.ResultFailed || !strings.Contains(result.Content, "claimed=60 deleted=58 skipped=0 failed=2 canceled=0 not_started=0") {
		t.Fatalf("partial cleanup: %#v, %v", result, err)
	}
	if len(strings.Split(result.Content, "\n")) > 50 || strings.Contains(result.Content, "\n\n") || !strings.Contains(result.Content, "truncated=true omitted_details=") {
		t.Fatalf("unbounded or incomplete details: %q", result.Content)
	}
	for id := range data {
		_, err := m.Store.Media().Get(t.Context(), id)
		if failedIDs[id] {
			if err != nil || !strings.Contains(result.Content, "media_id="+id+" result=failed") || !strings.Contains(result.Content, "stage=remote_delete") {
				t.Fatalf("failed media detail lost: %s, %v, %q", id, err, result.Content)
			}
		} else if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("deleted media survived: %s, %v", id, err)
		}
	}
}

func TestCleanupCountsUnavailableClaimAsSkipped(t *testing.T) {
	m := newConcurrentTestManager(t)
	item, err := m.ImportBytes(t.Context(), []byte("disappearing"), Input{})
	if err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	unlock, err := m.objects.acquire(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan events.LogResult, 1)
	go func() { result, _ := m.Cleanup(t.Context()); results <- result }()
	waitObjectUsers(t, &m.objects, item.ID, 2)
	if err := m.Store.Media().FinishDelete(t.Context(), item.ID); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	result := awaitMediaTest(t, results)
	if result.ResultStatus != events.ResultSucceeded || !strings.Contains(result.Content, "claimed=1 deleted=0 skipped=1 failed=0 canceled=0 not_started=0") {
		t.Fatalf("unavailable claim counts = %#v", result)
	}
}

func TestCleanupMissingSeedReferenceIncludesOwnerAndMedia(t *testing.T) {
	m := newConcurrentTestManager(t)
	item, err := m.ImportBytes(t.Context(), []byte("referenced"), Input{})
	if err != nil {
		t.Fatal(err)
	}
	seed := &storage.NativeSeed{ID: "root", APIType: "response", Provider: "p", ItemsJSON: `[]`, MaterialsJSON: `[]`, ContinuationJSON: `[]`, CallsJSON: `[]`, MediaIDs: []string{item.ID}}
	row := &storage.Session{ID: "child", Metadata: `{"llm_origin":{"protocol":"response","provider":"p"}}`}
	if err := m.Store.Sessions().CreateMaterial(t.Context(), storage.SessionMaterialCreate{Session: row, Seed: seed}); err != nil {
		t.Fatal(err)
	}
	if err := m.Store.MediaReferences().Remove(t.Context(), storage.MediaReference{MediaID: item.ID, OwnerType: "native_seed", OwnerID: seed.ID, Purpose: "content"}); err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	result, err := m.Cleanup(t.Context())
	if err == nil || result.ResultStatus != events.ResultFailed {
		t.Fatalf("missing reference cleanup = %#v, %v", result, err)
	}
	for _, value := range []string{"stage=reference_check", "owner_type=native_seed", "owner_id=root", "session_id=child", "media_id=" + item.ID} {
		if !strings.Contains(result.Content, value) {
			t.Fatalf("missing %q in %q", value, result.Content)
		}
	}
	if strings.Contains(result.Content, "claimed=") {
		t.Fatalf("reported a claim before validation passed: %q", result.Content)
	}
	if _, err := m.Store.Media().Get(t.Context(), item.ID); err != nil {
		t.Fatalf("referenced media was deleted: %v", err)
	}
}

func (b *trackingDeleteBackend) Remove(ctx context.Context, item *storage.Media) error {
	*b.calls = append(*b.calls, b.name)
	if b.fail {
		return fmt.Errorf("temporary %s backend error", b.name)
	}
	if b.Backend == nil {
		return nil
	}
	return b.Backend.Remove(ctx, item)
}

func TestCleanupUsesStoredLocationsAcrossBackendChanges(t *testing.T) {
	t.Run("s3 to hybrid removes remote object", func(t *testing.T) {
		ctx := context.Background()
		store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		calls := []string{}
		root := t.TempDir()
		local := &trackingDeleteBackend{Backend: &LocalBackend{Root: root}, name: "local", calls: &calls}
		remote := &trackingDeleteBackend{name: "remote", calls: &calls}
		m := NewManager(store, root, local)
		m.Remote = remote
		item := &storage.Media{
			ID:        IDPrefix + strings.Repeat("a", 64),
			Name:      "remote.bin",
			MIMEType:  "application/octet-stream",
			Size:      1,
			Backend:   "s3",
			ObjectKey: "media/remote",
		}
		if err := store.Media().Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
		m.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
		if _, err := m.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(calls, []string{"remote"}) {
			t.Fatalf("remove calls = %v", calls)
		}
		if _, err := store.Media().Get(ctx, item.ID); err != storage.ErrNotFound {
			t.Fatalf("record survives: %v", err)
		}
	})

	t.Run("hybrid to s3 retries remote before local", func(t *testing.T) {
		ctx := context.Background()
		store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		calls := []string{}
		root := t.TempDir()
		local := &trackingDeleteBackend{Backend: &LocalBackend{Root: root}, name: "local", calls: &calls}
		remote := &trackingDeleteBackend{name: "remote", calls: &calls, fail: true}
		m := NewManager(store, root, local)
		item, err := m.ImportBytes(ctx, []byte("dual"), Input{Name: "dual.bin"})
		if err != nil {
			t.Fatal(err)
		}
		item.ObjectKey = "media/dual"
		if err := store.Media().Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
		m.Backend = remote
		m.Remote = remote
		m.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
		if _, err := m.Cleanup(ctx); err == nil {
			t.Fatal("expected remote delete failure")
		}
		if !reflect.DeepEqual(calls, []string{"remote"}) {
			t.Fatalf("remove calls before retry = %v", calls)
		}
		if _, err := os.Stat(item.LocalPath); err != nil {
			t.Fatalf("local copy removed before remote: %v", err)
		}
		marked, err := store.Media().Get(ctx, item.ID)
		if err != nil || !marked.Deleting {
			t.Fatalf("retry metadata = %#v, %v", marked, err)
		}

		remote.fail = false
		if _, err := m.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(calls, []string{"remote", "remote", "local"}) {
			t.Fatalf("remove calls after retry = %v", calls)
		}
		if _, err := os.Stat(item.LocalPath); !os.IsNotExist(err) {
			t.Fatalf("local copy survives: %v", err)
		}
		if _, err := store.Media().Get(ctx, item.ID); err != storage.ErrNotFound {
			t.Fatalf("record survives: %v", err)
		}
	})

	t.Run("local failure after remote deletion retries idempotently", func(t *testing.T) {
		ctx := context.Background()
		store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		calls := []string{}
		root := t.TempDir()
		local := &trackingDeleteBackend{Backend: &LocalBackend{Root: root}, name: "local", calls: &calls, fail: true}
		remote := &trackingDeleteBackend{name: "remote", calls: &calls}
		m := NewManager(store, root, local)
		m.Remote = remote
		item, err := m.ImportBytes(ctx, []byte("partial delete"), Input{Name: "partial.bin"})
		if err != nil {
			t.Fatal(err)
		}
		item.ObjectKey = "media/partial"
		if err := store.Media().Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
		m.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
		if _, err := m.Cleanup(ctx); err == nil {
			t.Fatal("expected local delete failure")
		}
		if !reflect.DeepEqual(calls, []string{"remote", "local"}) {
			t.Fatalf("first remove calls = %v", calls)
		}
		if _, err := os.Stat(item.LocalPath); err != nil {
			t.Fatalf("local copy missing after failed deletion: %v", err)
		}
		if marked, err := store.Media().Get(ctx, item.ID); err != nil || !marked.Deleting {
			t.Fatalf("retry metadata = %#v, %v", marked, err)
		}

		local.fail = false
		if _, err := m.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(calls, []string{"remote", "local", "remote", "local"}) {
			t.Fatalf("retry remove calls = %v", calls)
		}
		if _, err := store.Media().Get(ctx, item.ID); err != storage.ErrNotFound {
			t.Fatalf("record survives: %v", err)
		}
	})

	t.Run("missing remote preserves copies and retry metadata", func(t *testing.T) {
		ctx := context.Background()
		store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		calls := []string{}
		root := t.TempDir()
		local := &trackingDeleteBackend{Backend: &LocalBackend{Root: root}, name: "local", calls: &calls}
		m := NewManager(store, root, local)
		item, err := m.ImportBytes(ctx, []byte("dual without remote"), Input{Name: "dual.bin"})
		if err != nil {
			t.Fatal(err)
		}
		item.ObjectKey = "media/dual"
		if err := store.Media().Upsert(ctx, item); err != nil {
			t.Fatal(err)
		}
		m.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
		if _, err := m.Cleanup(ctx); err == nil {
			t.Fatal("expected unavailable remote backend")
		}
		if len(calls) != 0 {
			t.Fatalf("removed physical copies without remote backend: %v", calls)
		}
		if _, err := os.Stat(item.LocalPath); err != nil {
			t.Fatalf("local copy missing: %v", err)
		}
		marked, err := store.Media().Get(ctx, item.ID)
		if err != nil || !marked.Deleting {
			t.Fatalf("retry metadata = %#v, %v", marked, err)
		}
	})
}

func TestOpenProtectsMediaUntilReaderCloses(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	m := NewManager(store, root, &LocalBackend{Root: root})
	item, err := m.ImportBytes(ctx, []byte("reading"), Input{})
	if err != nil {
		t.Fatal(err)
	}
	reader, _, err := m.Open(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	if _, err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Media().Get(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Media().Get(ctx, item.ID); err != storage.ErrNotFound {
		t.Fatal(err)
	}
}
