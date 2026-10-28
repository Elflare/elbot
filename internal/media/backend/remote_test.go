package backend

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/media/share"
)

type remoteStub struct {
	put     func(context.Context, string, io.Reader, int64, string) (string, error)
	open    func(context.Context, string) (io.ReadCloser, error)
	remove  func(context.Context, string) error
	presign func(context.Context, string, time.Duration) (string, error)
}

func (s *remoteStub) Put(ctx context.Context, id string, input io.Reader, size int64, contentType string) (string, error) {
	return s.put(ctx, id, input, size, contentType)
}
func (s *remoteStub) Open(ctx context.Context, location string) (io.ReadCloser, error) {
	return s.open(ctx, location)
}
func (s *remoteStub) Remove(ctx context.Context, location string) error {
	return s.remove(ctx, location)
}
func (s *remoteStub) PresignGet(ctx context.Context, location string, expiry time.Duration) (string, error) {
	return s.presign(ctx, location, expiry)
}

func TestLazyBackendForwardsOperations(t *testing.T) {
	ctx := t.Context()
	input := strings.NewReader("input")
	reader := io.NopCloser(strings.NewReader("output"))
	failure := errors.New("operation failed")
	calls := 0
	remote := &remoteStub{
		put: func(got context.Context, id string, r io.Reader, size int64, mime string) (string, error) {
			if got != ctx || id != "id" || r != input || size != 5 || mime != "text/plain" {
				t.Error("Put arguments changed")
			}
			return "written", failure
		},
		open: func(got context.Context, location string) (io.ReadCloser, error) {
			if got != ctx || location != "selected" {
				t.Error("Open arguments changed")
			}
			return reader, failure
		},
		remove: func(got context.Context, location string) error {
			if got != ctx || location != "selected" {
				t.Error("Remove arguments changed")
			}
			return failure
		},
		presign: func(got context.Context, location string, expiry time.Duration) (string, error) {
			if got != ctx || location != "selected" || expiry != 7*time.Minute {
				t.Error("PresignGet arguments changed")
			}
			return "signed", failure
		},
	}
	b := NewLazyBackend(func(got context.Context) (share.RemoteBackend, error) {
		calls++
		if got != ctx {
			t.Error("factory context changed")
		}
		return remote, nil
	})
	if calls != 0 {
		t.Fatal("eager initialization")
	}
	if got, err := b.Put(ctx, "id", input, 5, "text/plain"); got != "written" || err != failure {
		t.Fatalf("Put = %q, %v", got, err)
	}
	if got, err := b.Open(ctx, "selected"); got != reader || err != failure {
		t.Fatalf("Open = %v, %v", got, err)
	}
	if err := b.Remove(ctx, "selected"); err != failure {
		t.Fatalf("Remove = %v", err)
	}
	if got, err := b.PresignGet(ctx, "selected", 7*time.Minute); got != "signed" || err != failure {
		t.Fatalf("PresignGet = %q, %v", got, err)
	}
	if calls != 1 {
		t.Fatalf("factory calls = %d", calls)
	}
}

func TestLazyBackendRetriesFailedConstruction(t *testing.T) {
	failure := errors.New("construction failed")
	calls := 0
	remote := &remoteStub{remove: func(context.Context, string) error { return nil }}
	b := NewLazyBackend(func(context.Context) (share.RemoteBackend, error) {
		calls++
		if calls == 1 {
			return remote, failure
		}
		return remote, nil
	})
	if err := b.Remove(t.Context(), "key"); !errors.Is(err, failure) {
		t.Fatalf("first call = %v", err)
	}
	for range 2 {
		if err := b.Remove(t.Context(), "key"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("factory calls = %d", calls)
	}
}

func TestLazyBackendConcurrentCallsShareConstruction(t *testing.T) {
	var builds, operations atomic.Int32
	remote := &remoteStub{remove: func(context.Context, string) error { operations.Add(1); return nil }}
	b := NewLazyBackend(func(context.Context) (share.RemoteBackend, error) { builds.Add(1); return remote, nil })
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			<-start
			if err := b.Remove(t.Context(), "key"); err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	if builds.Load() != 1 || operations.Load() != 32 {
		t.Fatalf("builds=%d operations=%d", builds.Load(), operations.Load())
	}
}

func TestLazyBackendCanceledCallDoesNotInitialize(t *testing.T) {
	b := NewLazyBackend(func(context.Context) (share.RemoteBackend, error) {
		t.Error("factory called for canceled request")
		return nil, errors.New("unexpected construction")
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := b.Remove(ctx, "key"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Remove = %v", err)
	}
}

func TestLazyBackendOperationsRunOutsideInitializationLock(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	b := NewLazyBackend(func(context.Context) (share.RemoteBackend, error) {
		return &remoteStub{remove: func(context.Context, string) error { entered <- struct{}{}; <-release; return nil }}, nil
	})
	var wg sync.WaitGroup
	defer wg.Wait()
	defer close(release)
	for range 2 {
		wg.Go(func() {
			if err := b.Remove(t.Context(), "key"); err != nil {
				t.Error(err)
			}
		})
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("backend operation holds initialization lock")
		}
	}
}
