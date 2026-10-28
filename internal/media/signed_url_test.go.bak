package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestPresignRefreshBoundary(t *testing.T) {
	for _, remaining := range []time.Duration{5*time.Minute + time.Second, 5 * time.Minute, 5*time.Minute - time.Second, -time.Second} {
		t.Run(remaining.String(), func(t *testing.T) {
			m, id, counts := newSignedURLFixture(t)
			now := time.Now().Truncate(time.Second).Add(700 * time.Millisecond)
			m.Now = func() time.Time { return now }
			old := &storage.MediaSignedURL{MediaID: id, URL: "https://media.example/old", ExpiresAt: now.Add(remaining)}
			if err := m.Store.Media().UpsertSignedURL(t.Context(), old); err != nil {
				t.Fatal(err)
			}
			// Time spent signing must not extend the stored deadline.
			remote := m.Remote.(*concurrentTestBackend)
			sign := remote.presign
			remote.presign = func(ctx context.Context, item *storage.Media, expiry time.Duration) (string, error) {
				now = now.Add(20 * time.Second)
				return sign(ctx, item, expiry)
			}
			started := now
			got, err := acquireTestURL(t.Context(), m, id)
			if err != nil {
				t.Fatal(err)
			}
			cached, err := m.Store.Media().GetSignedURL(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if remaining > 5*time.Minute {
				if got != old.URL || counts.signs != 0 || !cached.ExpiresAt.Equal(old.ExpiresAt) {
					t.Fatalf("cache hit changed URL or deadline: %+v signs=%d", cached, counts.signs)
				}
			} else if got == old.URL || cached.URL != got || counts.signs != 1 || !cached.ExpiresAt.Equal(started.Truncate(time.Second).Add(time.Hour)) {
				t.Fatalf("refresh result: %+v signs=%d", cached, counts.signs)
			}
			if counts.puts != 0 {
				t.Fatal("existing object uploaded again")
			}
		})
	}
}

func TestPresignSurvivesDatabaseReopen(t *testing.T) {
	ctx := t.Context()
	path, root := filepath.Join(t.TempDir(), "db"), t.TempDir()
	store, err := sqlite.New(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m := NewManager(store, root, &LocalBackend{Root: root})
	m.FileDelivery.Backend = "s3"
	item, err := m.ImportBytes(ctx, []byte("survive restart"), Input{})
	if err != nil {
		t.Fatal(err)
	}
	m.Remote = &concurrentTestBackend{Backend: &LocalBackend{Root: t.TempDir()}}
	first, err := acquireTestURL(ctx, m, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.New(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	m = NewManager(reopened, root, &LocalBackend{Root: root})
	m.FileDelivery.Backend = "s3"
	m.Remote = &concurrentTestBackend{
		put: func(context.Context, string, io.Reader, int64, string) (string, error) {
			return "", errors.New("restart must not upload")
		},
		presign: func(context.Context, *storage.Media, time.Duration) (string, error) {
			return "", errors.New("restart must not sign")
		},
	}
	if got, err := acquireTestURL(ctx, m, item.ID); err != nil || got != first {
		t.Fatalf("restart URL equal=%v err=%v", got == first, err)
	}
}

func TestPresignConcurrentRefresh(t *testing.T) {
	m, id, counts := newSignedURLFixture(t)
	if err := m.Store.Media().UpsertSignedURL(t.Context(), &storage.MediaSignedURL{MediaID: id, URL: "old", ExpiresAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	started, gate := make(chan struct{}), make(chan struct{})
	remote := m.Remote.(*concurrentTestBackend)
	sign := remote.presign
	remote.presign = func(ctx context.Context, item *storage.Media, expiry time.Duration) (string, error) {
		close(started)
		select {
		case <-gate:
			return sign(ctx, item, expiry)
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	urls, errs := make(chan string, 2), make(chan error, 2)
	call := func() {
		url, err := acquireTestURL(ctx, m, id)
		urls <- url
		errs <- err
	}
	go call()
	awaitMediaTest(t, started)
	go call()
	waitObjectUsers(t, &m.objects, id, 2)
	close(gate)
	for range 2 {
		if err := awaitMediaTest(t, errs); err != nil {
			t.Fatal(err)
		}
	}
	if a, b := <-urls, <-urls; a != b || counts.signs != 1 || counts.puts != 0 {
		t.Fatalf("URLs equal=%v uploads=%d signs=%d", a == b, counts.puts, counts.signs)
	}
}

type signedURLFaultRepository struct {
	storage.MediaRepository
	readErr, writeErr error
}

func (r *signedURLFaultRepository) GetSignedURL(ctx context.Context, id string) (*storage.MediaSignedURL, error) {
	if r.readErr != nil {
		return nil, r.readErr
	}
	return r.MediaRepository.GetSignedURL(ctx, id)
}

func (r *signedURLFaultRepository) UpsertSignedURL(ctx context.Context, record *storage.MediaSignedURL) error {
	if r.writeErr != nil {
		return r.writeErr
	}
	return r.MediaRepository.UpsertSignedURL(ctx, record)
}

type signedURLFaultStore struct {
	storage.Store
	repository storage.MediaRepository
}

func (s *signedURLFaultStore) Media() storage.MediaRepository { return s.repository }

func TestPresignFailuresKeepCacheAndReleaseHold(t *testing.T) {
	for _, stage := range []string{"read", "sign", "write", "cancel"} {
		t.Run(stage, func(t *testing.T) {
			m, id, _ := newSignedURLFixture(t)
			old := &storage.MediaSignedURL{MediaID: id, URL: "https://media.example/old", ExpiresAt: time.Now().Truncate(time.Second)}
			repository := m.Store.Media()
			if err := repository.UpsertSignedURL(t.Context(), old); err != nil {
				t.Fatal(err)
			}
			fault := &signedURLFaultRepository{MediaRepository: repository}
			m.Store = &signedURLFaultStore{Store: m.Store, repository: fault}
			failure := errors.New(stage + " failed")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			remote := m.Remote.(*concurrentTestBackend)
			sign := remote.presign
			switch stage {
			case "read":
				fault.readErr = failure
			case "write":
				fault.writeErr = failure
			case "sign", "cancel":
				if stage == "cancel" {
					failure = context.Canceled
				}
				remote.presign = func(context.Context, *storage.Media, time.Duration) (string, error) {
					if stage == "cancel" {
						cancel()
					}
					return "", failure
				}
			}
			if url, err := acquireTestURL(ctx, m, id); !errors.Is(err, failure) || url != "" {
				t.Fatalf("failure returned URL=%q err=%v", url, err)
			}
			cached, err := repository.GetSignedURL(t.Context(), id)
			if err != nil ||
				cached.MediaID != old.MediaID ||
				cached.URL != old.URL ||
				!cached.ExpiresAt.Equal(old.ExpiresAt) {
				t.Fatalf("failure changed old cache: %+v %v", cached, err)
			}
			refs, err := m.Store.MediaReferences().ListMediaIDs(t.Context(), id)
			if err != nil || len(refs) != 0 {
				t.Fatalf("hold leaked: %+v %v", refs, err)
			}
			fault.readErr, fault.writeErr, remote.presign = nil, nil, sign
			retryCtx, retryCancel := context.WithTimeout(t.Context(), time.Second)
			defer retryCancel()
			if _, err := acquireTestURL(retryCtx, m, id); err != nil {
				t.Fatalf("retry failed or object lock leaked: %v", err)
			}
		})
	}
}

func TestCleanupReportsRemoteInitializationFailure(t *testing.T) {
	m := newConcurrentTestManager(t)
	seedCleanupMedia(t, m, 1)
	failure := errors.New("test credentials unavailable")
	m.remoteFactory = func(context.Context) (Backend, error) { return nil, failure }
	result, err := m.Cleanup(t.Context())
	if !errors.Is(err, failure) || result.ResultStatus != events.ResultFailed {
		t.Fatalf("cleanup lost initialization failure: %+v %v", result, err)
	}
}

type signedURLCounts struct{ puts, signs int }

func newSignedURLFixture(t *testing.T) (*Manager, string, *signedURLCounts) {
	t.Helper()
	m := newConcurrentTestManager(t)
	m.FileDelivery.Backend = "s3"
	item, err := m.ImportBytes(t.Context(), []byte("existing remote media"), Input{})
	if err != nil {
		t.Fatal(err)
	}
	item.ObjectKey = "media/object"
	if err := m.Store.Media().Upsert(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	counts := &signedURLCounts{}
	m.Remote = &concurrentTestBackend{
		put: func(context.Context, string, io.Reader, int64, string) (string, error) {
			counts.puts++
			return item.ObjectKey, nil
		},
		presign: func(context.Context, *storage.Media, time.Duration) (string, error) {
			counts.signs++
			return fmt.Sprintf("https://media.example/object?signature=%d", counts.signs), nil
		},
	}
	return m, item.ID, counts
}

func acquireTestURL(ctx context.Context, m *Manager, id string) (string, error) {
	messages, release, err := m.AcquireForLLM(ctx, []llm.LLMMessage{{Role: llm.RoleUser, Segments: []llm.MessageSegment{{Type: llm.SegmentFile, MediaID: id}}}})
	defer release()
	if err != nil {
		return "", err
	}
	return messages[0].Segments[0].URL, nil
}
