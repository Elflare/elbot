package object

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/signal"
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

func TestReadReturnsOriginalErrorAndLogsCleanup(t *testing.T) {
	logs := captureMediaLogs(t)
	m, _, backend := newTestManager(t)
	item := importTestMedia(t, m, "errors")
	readErr, closeErr, releaseErr := errors.New("read"), errors.New("close"), errors.New("release")
	backend.open = func(context.Context, string) (io.ReadCloser, error) {
		return &testReader{Reader: errorReader{readErr}, closeErr: closeErr}, nil
	}
	m.references = testReferences{ReferenceRepository: m.references, remove: func(context.Context, storage.MediaReference) error { return releaseErr }}
	_, _, err := m.Read(t.Context(), item.ID)
	if err != readErr {
		t.Fatalf("read error=%v", err)
	}
	requireMediaLog(t, logs, closeErr.Error())
	requireMediaLog(t, logs, releaseErr.Error())
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
	if err := m.Cleanup(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Info(t.Context(), item.ID); err != nil {
		t.Fatal("held object deleted", err)
	}
	if err := held.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.options.Now = func() time.Time { return time.Now().Add(30 * time.Minute) }
	if err := m.Cleanup(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Info(t.Context(), item.ID); err != nil {
		t.Fatal("grace period ignored", err)
	}
	m.options.Now = func() time.Time { return time.Now().Add(2 * OrphanGrace) }
	if err := m.Cleanup(t.Context(), nil); err != nil {
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

func captureMediaLogs(t *testing.T) <-chan events.LogRecord {
	t.Helper()
	logs := make(chan events.LogRecord, 128)
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.Module == "media" {
			logs <- record
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	return logs
}
func requireMediaLog(t *testing.T, logs <-chan events.LogRecord, detail string) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case record := <-logs:
			if strings.Contains(record.Content, detail) {
				return
			}
		case <-timer.C:
			t.Fatalf("missing media log: %s", detail)
		}
	}
}
