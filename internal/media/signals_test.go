package media

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type controlledReferences struct {
	storage.MediaReferenceRepository
	fail    atomic.Bool
	calls   atomic.Int32
	started chan struct{}
	unblock chan struct{}
}

func (r *controlledReferences) Remove(ctx context.Context, ref storage.MediaReference) error {
	n := r.calls.Add(1)
	if n == 1 && r.started != nil {
		close(r.started)
		<-r.unblock
	}
	if r.fail.Load() {
		return errors.New("reference store unavailable")
	}
	return r.MediaReferenceRepository.Remove(ctx, ref)
}

type usageStore struct {
	Store
	refs *controlledReferences
}

func (s usageStore) MediaReferences() storage.MediaReferenceRepository { return s.refs }

func newUsageCenter(t *testing.T) (*MediaCenter, *sqlite.Store, *controlledReferences) {
	t.Helper()
	store, err := sqlite.New(t.Context(), filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	refs := &controlledReferences{MediaReferenceRepository: store.MediaReferences()}
	cfg := config.Default()
	cfg.Sandbox.Root = filepath.Join(t.TempDir(), "sandbox")
	cfg.FileDelivery.Backend = "base64"
	m, err := NewMediaCenter(t.Context(), cfg, usageStore{Store: store, refs: refs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	return m, store, refs
}

func drainUsageEvents(t *testing.T, m *MediaCenter) {
	t.Helper()
	if _, err := m.usageFinished.Emit(t.Context(), UsageFinishedEvent{}); err != nil {
		t.Fatal(err)
	}
}

func TestUsageFailureRemainsOwnedAfterCanceledConsumerReturns(t *testing.T) {
	m, store, refs := newUsageCenter(t)
	item, err := m.ImportBytes(t.Context(), []byte("content"), Input{Name: "input"}, ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.Hold(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Hold(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.UsageID == b.UsageID || a.UsageID == "" {
		t.Fatal("usage IDs were shared")
	}
	refs.fail.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m.PublishUsageFinished(ctx, UsageFinishedEvent{UsageIDs: []UsageID{a.UsageID}})
	drainUsageEvents(t, m)
	if refs.calls.Load() != 1 {
		t.Fatal("consumer completion retried cleanup")
	}
	refs.fail.Store(false)
	if _, err := m.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	remaining, err := store.MediaReferences().ListMediaIDs(t.Context(), item.ID)
	if err != nil || len(remaining) != 1 {
		t.Fatalf("other usage lost: %v %v", remaining, err)
	}
	m.PublishUsageFinished(ctx, UsageFinishedEvent{UsageIDs: []UsageID{a.UsageID, a.UsageID}})
	drainUsageEvents(t, m)
	if refs.calls.Load() != 2 {
		t.Fatal("successful resource released twice")
	}
	reader, err := b.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "content" {
		t.Fatal(string(data), err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	m.PublishUsageFinished(ctx, UsageFinishedEvent{UsageIDs: []UsageID{b.UsageID}})
	drainUsageEvents(t, m)
	remaining, err = store.MediaReferences().ListMediaIDs(t.Context(), item.ID)
	if err != nil || len(remaining) != 0 {
		t.Fatal(remaining, err)
	}
}

func TestUsageQueueRejectionAndCloseTimeoutKeepResources(t *testing.T) {
	m, store, refs := newUsageCenter(t)
	refs.started, refs.unblock = make(chan struct{}), make(chan struct{})
	item, err := m.ImportBytes(t.Context(), []byte("body"), Input{}, ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.Hold(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Hold(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.PublishUsageFinished(t.Context(), UsageFinishedEvent{UsageIDs: []UsageID{a.UsageID}})
	select {
	case <-refs.started:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}
	t.Cleanup(func() {
		select {
		case <-refs.unblock:
		default:
			close(refs.unblock)
		}
	})
	for range 256 {
		if _, err := m.usageFinished.EmitAsync(t.Context(), UsageFinishedEvent{}); err != nil {
			t.Fatal(err)
		}
	}
	m.PublishUsageFinished(t.Context(), UsageFinishedEvent{UsageIDs: []UsageID{b.UsageID}})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := m.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close should wait: %v", err)
	}
	if _, err := m.Hold(t.Context(), item.ID); !errors.Is(err, signal.ErrClosed) {
		t.Fatalf("accepted after close: %v", err)
	}
	remaining, err := store.MediaReferences().ListMediaIDs(t.Context(), item.ID)
	if err != nil || len(remaining) != 2 {
		t.Fatal("released before cleanup exited", remaining, err)
	}
	close(refs.unblock)
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	remaining, err = store.MediaReferences().ListMediaIDs(t.Context(), item.ID)
	if err != nil || len(remaining) != 0 {
		t.Fatal("rejected end fact lost", remaining, err)
	}
}

func TestOutputFailureAndWorkspaceCleanupRemainInMedia(t *testing.T) {
	m, store, refs := newUsageCenter(t)
	item, err := m.ImportBytes(t.Context(), []byte("body"), Input{}, ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Media().Get(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(metadata.LocalPath); err != nil {
		t.Fatal(err)
	}
	refs.fail.Store(true)
	_, id, err := m.ResolveForOutput(t.Context(), item.ID)
	if err == nil || id != "" || refs.calls.Load() != 1 {
		t.Fatalf("failed prepare handed ownership out: %q %v", id, err)
	}
	refs.fail.Store(false)
	dir, workspaceID, err := m.Workspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "output"), []byte("body"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("active workspace removed", err)
	}
	m.PublishUsageFinished(t.Context(), UsageFinishedEvent{UsageIDs: []UsageID{workspaceID}})
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("workspace survived", err)
	}
	remaining, err := store.MediaReferences().ListMediaIDs(t.Context(), item.ID)
	if err != nil || len(remaining) != 0 {
		t.Fatal(remaining, err)
	}
}
