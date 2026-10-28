package object

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"elbot/internal/storage"
)

type testReferences struct {
	ReferenceRepository
	remove func(context.Context, storage.MediaReference) error
}

func (r testReferences) Remove(ctx context.Context, ref storage.MediaReference) error {
	return r.remove(ctx, ref)
}

type testReader struct {
	io.Reader
	closes   int
	closeErr error
}

func (r *testReader) Close() error { r.closes++; return r.closeErr }

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func TestHeldMediaAndReaderCloseRetry(t *testing.T) {
	m, store, backend := newTestManager(t)
	item := importTestMedia(t, m, "held")
	failure := errors.New("release failure")
	calls := 0
	m.references = testReferences{ReferenceRepository: m.references, remove: func(ctx context.Context, ref storage.MediaReference) error {
		calls++
		if ctx.Err() != nil {
			t.Error("cleanup context canceled")
		}
		if calls == 1 {
			return failure
		}
		return store.MediaReferences().Remove(ctx, ref)
	}}
	closeErr := errors.New("reader close failure")
	input := &testReader{Reader: strings.NewReader("held"), closeErr: closeErr}
	backend.open = func(context.Context, string) (io.ReadCloser, error) { return input, nil }
	reader, _, err := m.Open(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); !errors.Is(err, failure) || !errors.Is(err, closeErr) {
		t.Fatalf("first close = %v", err)
	}
	if refs, _ := store.MediaReferences().ListMediaIDs(t.Context(), item.ID); len(refs) != 1 {
		t.Fatal("failed release discarded hold")
	}
	if err := reader.Close(); errors.Is(err, failure) || !errors.Is(err, closeErr) {
		t.Fatalf("retry = %v", err)
	}
	_ = reader.Close()
	if calls != 2 || input.closes != 1 {
		t.Fatalf("release calls %d reader closes %d", calls, input.closes)
	}
	if refs, _ := store.MediaReferences().ListMediaIDs(t.Context(), item.ID); len(refs) != 0 {
		t.Fatal("hold leaked")
	}
}

func TestOpenFailureReturnsRetryableCleanup(t *testing.T) {
	m, store, backend := newTestManager(t)
	item := importTestMedia(t, m, "failure")
	openErr, releaseErr := errors.New("open"), errors.New("release")
	backend.open = func(context.Context, string) (io.ReadCloser, error) { return nil, openErr }
	m.references = testReferences{ReferenceRepository: m.references, remove: func(context.Context, storage.MediaReference) error { return releaseErr }}
	reader, _, err := m.Open(t.Context(), item.ID)
	if reader == nil || !errors.Is(err, openErr) || !errors.Is(err, releaseErr) {
		t.Fatalf("open result = %v %v", reader, err)
	}
	m.references = store.MediaReferences()
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if refs, _ := store.MediaReferences().ListMediaIDs(t.Context(), item.ID); len(refs) != 0 {
		t.Fatal("retry did not release")
	}
}

func TestReadPreservesReadCloseAndReleaseErrors(t *testing.T) {
	m, _, backend := newTestManager(t)
	item := importTestMedia(t, m, "errors")
	readErr, closeErr, releaseErr := errors.New("read"), errors.New("close"), errors.New("release")
	backend.open = func(context.Context, string) (io.ReadCloser, error) {
		return &testReader{Reader: errorReader{readErr}, closeErr: closeErr}, nil
	}
	m.references = testReferences{ReferenceRepository: m.references, remove: func(context.Context, storage.MediaReference) error { return releaseErr }}
	_, _, err := m.Read(t.Context(), item.ID)
	for _, want := range []error{readErr, closeErr, releaseErr} {
		if !errors.Is(err, want) {
			t.Fatalf("lost %v: %v", want, err)
		}
	}
}

func TestHoldProtectsUntilLastRelease(t *testing.T) {
	m, store, _ := newTestManager(t)
	item := importTestMedia(t, m, "protected")
	held, err := m.Hold(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		reader, err := held.Open(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if refs, _ := store.MediaReferences().ListMediaIDs(t.Context(), item.ID); len(refs) != 1 {
		t.Fatalf("Open added references: %#v", refs)
	}
	m.options.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	if _, err := m.Cleanup(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Info(t.Context(), item.ID); err != nil {
		t.Fatal("held object deleted", err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	m.options.Now = func() time.Time { return time.Now().Add(30 * time.Minute) }
	if _, err := m.Cleanup(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Info(t.Context(), item.ID); err != nil {
		t.Fatal("grace period ignored", err)
	}
	m.options.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	if _, err := m.Cleanup(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Info(t.Context(), item.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("orphan remains: %v", err)
	}
	if _, err := held.Open(t.Context()); err == nil {
		t.Fatal("released hold can read")
	}
	if _, err := held.SignedURL(t.Context()); err == nil {
		t.Fatal("released hold can sign")
	}
}
