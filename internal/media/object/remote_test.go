package object

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"elbot/internal/media/share"
	"elbot/internal/storage"
)

func TestConcurrentSigningSharesReplicaAndRefresh(t *testing.T) {
	m, store, _ := newTestManager(t)
	remote := &testBackend{}
	m.remote = remote
	now := time.Now().Truncate(time.Second)
	m.options.Now = func() time.Time { return now }
	item := importTestMedia(t, m, "upload")
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			held, err := m.Hold(t.Context(), item.ID)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() {
				if err := held.Close(); err != nil {
					t.Error(err)
				}
			}()
			if _, err := held.SignedURL(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	if remote.puts != 1 || remote.signs != 1 {
		t.Fatalf("puts=%d signs=%d", remote.puts, remote.signs)
	}
	held, err := m.Hold(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	cached, _ := store.Media().GetSignedURL(t.Context(), item.ID)
	now = cached.ExpiresAt.Add(-signedURLRefreshMargin - time.Nanosecond)
	if _, err := held.SignedURL(t.Context()); err != nil {
		t.Fatal(err)
	}
	if remote.signs != 1 {
		t.Fatal("refreshed too early")
	}
	now = cached.ExpiresAt.Add(-signedURLRefreshMargin)
	if _, err := held.SignedURL(t.Context()); err != nil {
		t.Fatal(err)
	}
	if remote.puts != 1 || remote.signs != 2 {
		t.Fatal("refresh failed or uploaded again")
	}
	cached, _ = store.Media().GetSignedURL(t.Context(), item.ID)
	failure := errors.New("sign failed")
	remote.sign = func(context.Context, string, time.Duration) (string, error) { return "", failure }
	now = cached.ExpiresAt
	if _, err := held.SignedURL(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("sign failure = %v", err)
	}
	unchanged, _ := store.Media().GetSignedURL(t.Context(), item.ID)
	if *unchanged != *cached {
		t.Fatal("failed refresh overwrote cache")
	}
}

func TestS3PrimaryUsesStoredKey(t *testing.T) {
	m, _, local := newTestManager(t)
	remote := &testBackend{}
	m.remote = remote
	m.options.Backend = "s3"
	item := importTestMedia(t, m, "remote primary")
	if local.puts != 0 || remote.puts != 1 {
		t.Fatal("wrong import backend")
	}
	m.options.Backend = "local"
	data, _, err := m.Read(t.Context(), item.ID)
	if err != nil || string(data) != "remote primary" {
		t.Fatalf("stored backend ignored: %q %v", data, err)
	}
	held, err := m.Hold(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if _, err := held.SignedURL(t.Context()); err != nil {
		t.Fatal(err)
	}
	if remote.puts != 1 {
		t.Fatal("remote primary reuploaded")
	}
}

func TestReplicaPersistenceFailureRollsBackAndPreservesErrors(t *testing.T) {
	m, _, local := newTestManager(t)
	item := importTestMedia(t, m, "replica")
	remote := &testBackend{}
	m.remote = remote
	held, err := m.Hold(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	persistErr, closeErr, removeErr := errors.New("persist"), errors.New("close"), errors.New("remove")
	reader, _ := local.Open(t.Context(), item.ID)
	local.open = func(context.Context, string) (io.ReadCloser, error) {
		return &testReader{Reader: reader, closeErr: closeErr}, nil
	}
	m.media = testRepository{MediaRepository: m.media, upsert: func(context.Context, *storage.Media) error { return persistErr }}
	remote.remove = func(ctx context.Context, key string) error {
		if ctx.Err() != nil || key != item.ID {
			t.Errorf("rollback context/key = %v %q", ctx.Err(), key)
		}
		return removeErr
	}
	_, err = held.SignedURL(t.Context())
	for _, want := range []error{persistErr, closeErr, removeErr} {
		if !errors.Is(err, want) {
			t.Fatalf("lost %v: %v", want, err)
		}
	}
	record, _ := m.media.Get(t.Context(), item.ID)
	if record.ObjectKey != "" {
		t.Fatal("failed persistence changed record")
	}
}

func TestSigningHoldsReferenceUntilUploadFinishes(t *testing.T) {
	m, store, _ := newTestManager(t)
	item := importTestMedia(t, m, "blocked upload")
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	remote := &testBackend{}
	remote.put = func(ctx context.Context, id string, r io.Reader, size int64, mime string) (string, error) {
		close(entered)
		select {
		case <-release:
			return "", errors.New("released")
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	m.remote = remote
	held, err := m.Hold(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := held.SignedURL(ctx); done <- err }()
	awaitTest(t, entered)
	// 配置不在并发调用中改写；直接用仓储检查未来认领仍受持有保护。
	items, err := store.Media().ClaimOrphans(t.Context(), time.Now().Add(2*OrphanGrace))
	if err != nil || len(items) != 0 {
		t.Fatalf("held upload claimed: %#v %v", items, err)
	}
	cancel()
	if err := awaitTest(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("upload cancellation = %v", err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if refs, _ := store.MediaReferences().ListMediaIDs(t.Context(), item.ID); len(refs) != 0 {
		t.Fatal("upload reference leaked")
	}
}

func TestConcurrentDistinctImportsDoNotBlock(t *testing.T) {
	m, _, _ := newTestManager(t)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	m.local = &testBackend{put: func(ctx context.Context, id string, _ io.Reader, _ int64, _ string) (string, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return id, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 2)
	for _, data := range []string{"one", "two"} {
		go func() { _, err := m.ImportBytes(ctx, []byte(data), share.Input{}, share.ImportLimits{}); done <- err }()
	}
	awaitTest(t, entered)
	awaitTest(t, entered)
	cancel()
	awaitTest(t, done)
	awaitTest(t, done)
}
