package logging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"elbot/internal/events"
	"elbot/internal/signal"
)

func testManager(t *testing.T, level string) *Manager {
	t.Helper()
	m, err := NewManager(level, filepath.Join(t.TempDir(), "sessions.db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return m
}

func emitRecord(t *testing.T, record events.LogRecord) {
	t.Helper()
	if err := events.EmitLog(context.Background(), record); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalRecordsUseIndependentLevelsAndReader(t *testing.T) {
	for _, level := range []string{"warn", "error"} {
		t.Run(level, func(t *testing.T) {
			m := testManager(t, level)
			at := time.Now().Add(-time.Minute).Truncate(time.Second)
			for _, category := range logCategories {
				emitRecord(t, events.LogRecord{At: at, Category: category, Level: slog.LevelInfo, Name: "llm_usage", Module: "model", Summary: "usage", Detail: "upstream detail", Fields: []slog.Attr{slog.Int("input_tokens", 17), slog.String("session_id", "session")}})
			}
			m.Audit().Info("legacy audit")
			m.Elnis().Info("legacy elnis")
			emitRecord(t, events.LogRecord{Category: events.LogRuntime, Level: slog.LevelError, Name: "failure", Detail: "runtime detail"})
			if err := m.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, category := range logCategories {
				prefix := string(category)
				if category == events.LogRuntime {
					prefix = "elbot"
				}
				entries, err := (Reader{Dir: m.LogDir()}).Query(context.Background(), LogQuery{Prefix: prefix, Limit: 10})
				if err != nil {
					t.Fatal(err)
				}
				if category == events.LogRuntime {
					if len(entries) != 1 || entries[0].Fields["event"] != "failure" || entries[0].Fields["detail"] != "" {
						t.Fatal(entries)
					}
				} else {
					if len(entries) != 2 {
						t.Fatal(entries)
					}
					var found bool
					for _, entry := range entries {
						if entry.Fields["event"] == "llm_usage" {
							found = true
							if !entry.Time.Equal(at) || entry.Fields["input_tokens"] != "17" || entry.Fields["detail"] != "upstream detail" || entry.Fields["session_id"] != "session" {
								t.Fatal(entry)
							}
						}
					}
					if !found {
						t.Fatal(entries)
					}
				}
			}
		})
	}
}

func TestGlobalRecordsSnapshotBeforeQueueConsumption(t *testing.T) {
	m := testManager(t, "debug")
	started, release := make(chan struct{}), make(chan struct{})
	if err := m.sinks[events.LogRuntime].queue.Submit(context.Background(), signal.Task{Shutdown: signal.Drain, Run: func(context.Context) error { close(started); <-release; return nil }}); err != nil {
		t.Fatal(err)
	}
	<-started
	values := map[string]any{"nested": []any{"original"}}
	emitRecord(t, events.LogRecord{Category: events.LogRuntime, Summary: "snapshot", Detail: "debug detail", Fields: []slog.Attr{slog.Any("payload", values)}})
	values["nested"].([]any)[0] = "changed"
	close(release)
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := (Reader{Dir: m.LogDir()}).Query(context.Background(), LogQuery{Prefix: "elbot"})
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	if !strings.Contains(entries[0].Fields["payload"], "original") || strings.Contains(entries[0].Raw, "changed") || entries[0].Fields["detail"] != "debug detail" {
		t.Fatal(entries)
	}
}

type controlledWriter struct {
	write  func([]byte) (int, error)
	closed atomic.Bool
}

func (w *controlledWriter) Write(p []byte) (int, error) {
	if w.write != nil {
		return w.write(p)
	}
	return len(p), nil
}
func (w *controlledWriter) Close() error { w.closed.Store(true); return nil }

func TestManagerRejectsDuplicatesAndRollsBackFailedInitialization(t *testing.T) {
	first := &controlledWriter{}
	failure := errors.New("open audit failed")
	_, err := newManager("info", t.TempDir(), 30, func(prefix string) (io.WriteCloser, error) {
		if prefix == "audit" {
			return nil, failure
		}
		return first, nil
	})
	if !errors.Is(err, failure) || !first.closed.Load() {
		t.Fatalf("closed=%t err=%v", first.closed.Load(), err)
	}
	m := testManager(t, "info")
	original := events.LogSubmitted
	called := false
	if _, err := newManager("info", t.TempDir(), 30, func(string) (io.WriteCloser, error) { called = true; return nil, failure }); err == nil || called {
		t.Fatalf("duplicate opened files: %t err=%v", called, err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := testManager(t, "info")
	if original != events.LogSubmitted {
		t.Fatal("replaced global signal")
	}
	emitRecord(t, events.LogRecord{Category: events.LogAudit, Summary: "one consumer"})
	if err := next.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := (Reader{Dir: next.LogDir()}).Query(context.Background(), LogQuery{Prefix: "audit"})
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func captureStderr(t *testing.T) func() string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = file
	t.Cleanup(func() { os.Stderr = old; _ = file.Close() })
	return func() string {
		data, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
}

func TestGlobalWriteFailureReportsToStderrAndContinues(t *testing.T) {
	readStderr := captureStderr(t)
	var writes atomic.Int32
	m, err := newManager("error", t.TempDir(), 30, func(string) (io.WriteCloser, error) {
		return &controlledWriter{write: func(p []byte) (int, error) {
			if writes.Add(1) == 1 {
				return 0, errors.New("disk write failed")
			}
			return len(p), nil
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	var publications atomic.Int32
	connection, err := events.LogSubmitted.Connect(func(context.Context, events.LogRecord) error { publications.Add(1); return nil }, signal.ConnectOptions{Shutdown: signal.CancelPending})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	for range 2 {
		emitRecord(t, events.LogRecord{Category: events.LogAudit, Summary: "business fact"})
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 2 || publications.Load() != 2 {
		t.Fatalf("writes=%d publications=%d", writes.Load(), publications.Load())
	}
	if got := readStderr(); !strings.Contains(got, "disk write failed") || strings.Count(got, "signal task failed") != 1 {
		t.Fatal(got)
	}
}

func TestManagerBackpressureCancellationDrainAndTimeout(t *testing.T) {
	readStderr := captureStderr(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	writer := &controlledWriter{write: func(p []byte) (int, error) { once.Do(func() { close(started); <-release }); return len(p), nil }}
	m, err := newManager("info", t.TempDir(), 30, func(prefix string) (io.WriteCloser, error) {
		if prefix == "audit" {
			return writer, nil
		}
		return &controlledWriter{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); _ = m.Close(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := events.EmitLog(ctx, events.LogRecord{Category: events.LogAudit}); err != nil {
		t.Fatal(err)
	}
	<-started
	for range 256 {
		emitRecord(t, events.LogRecord{Category: events.LogAudit})
	}
	blocked := make(chan error, 1)
	go func() { blocked <- events.EmitLog(ctx, events.LogRecord{Category: events.LogAudit}) }()
	select {
	case err := <-blocked:
		t.Fatalf("request cancellation escaped backpressure: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	m.BeginClose()
	select {
	case err := <-blocked:
		if !errors.Is(err, signal.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("BeginClose did not wake producer")
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if err := m.Close(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if writer.closed.Load() {
		t.Fatal("closed writer in active callback")
	}
	if _, err := NewManager("info", filepath.Join(t.TempDir(), "db"), 30); err == nil {
		t.Fatal("timed-out manager released ownership")
	}
	if !strings.Contains(readStderr(), "signal drain incomplete") {
		t.Fatal(readStderr())
	}
	releaseOnce.Do(func() { close(release) })
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !writer.closed.Load() {
		t.Fatal("writer not closed after worker exited")
	}
}

func TestGlobalRecordsDrainInCategoryOrder(t *testing.T) {
	m := testManager(t, "info")
	for i := range 100 {
		emitRecord(t, events.LogRecord{Category: events.LogAudit, Name: "order", Fields: []slog.Attr{slog.Int("index", i)}})
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := (Reader{Dir: m.LogDir()}).Query(context.Background(), LogQuery{Prefix: "audit", Limit: 100})
	if err != nil || len(entries) != 100 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	for i, entry := range entries {
		if entry.Fields["index"] != fmt.Sprint(99-i) {
			t.Fatal(entry)
		}
	}
}

func TestRecordRedactionUTF8AndSerializedLimit(t *testing.T) {
	m := testManager(t, "debug")
	attrs := []slog.Attr{
		slog.Any("raw", json.RawMessage(`{"api_key":"raw-secret"}`)),
		slog.String("session_id", "keep-session"), slog.Int("input_tokens", 42), slog.String("token_name", "business-name"),
		slog.Group("nested", slog.Any("payload", map[string]any{"Authorization": "nested-secret", "items": []any{map[string]any{"api_key": "array-secret"}}, "text": "Bearer inline-secret", "image_url": "data:image/png;base64,media-secret"})),
	}
	emitRecord(t, events.LogRecord{Category: events.LogAudit, Name: "failure", Module: "model", Summary: strings.Repeat("中文", 200), Detail: `{"credentials":{"cookie":"cookie-secret"},"message":"` + strings.Repeat("中", 5000) + `"}`, Fields: attrs})
	for i := range 100 {
		attrs = append(attrs, slog.String(fmt.Sprintf("large_%d", i), strings.Repeat("\"中", 6000)))
	}
	emitRecord(t, events.LogRecord{Category: events.LogAudit, Name: "bounded", Module: "model", Detail: strings.Repeat("文", 9000), Fields: attrs})
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := (Reader{Dir: m.LogDir()}).Query(context.Background(), LogQuery{Prefix: "audit", Limit: 10})
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	for _, entry := range entries {
		if !utf8.ValidString(entry.Raw) || len(entry.Raw)+1 > maxRecordBytes || utf8.RuneCountInString(entry.Message) > maxSummaryRunes || len(entry.Fields["detail"]) > maxDetailBytes {
			t.Fatalf("record bounds violated: %d", len(entry.Raw))
		}
		for _, secret := range []string{"nested-secret", "array-secret", "inline-secret", "media-secret", "cookie-secret", "raw-secret"} {
			if strings.Contains(entry.Raw, secret) {
				t.Fatalf("leaked %s", secret)
			}
		}
		if entry.Fields["session_id"] != "keep-session" || entry.Fields["module"] != "model" {
			t.Fatal(entry.Fields)
		}
		if entry.Fields["event"] == "failure" && (entry.Fields["token_name"] != "business-name" || entry.Fields["input_tokens"] != "42" || !strings.Contains(entry.Fields["detail"], truncatedMarker)) {
			t.Fatal(entry.Fields)
		}
		if entry.Fields["event"] == "bounded" && entry.Fields["truncated"] != "true" {
			t.Fatal("missing truncation marker")
		}
	}
}
