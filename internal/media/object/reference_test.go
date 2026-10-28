package object

import (
	"context"
	"errors"
	"io"
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
	if err := held.Close(t.Context()); err != nil {
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
