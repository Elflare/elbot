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
					if len(entries) != 1 {
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
	failure := &mutableDiagnosticError{message: "original error", detail: "original diagnostic"}
	values := map[string]any{"nested": []any{"original"}}
	emitRecord(t, events.LogRecord{Category: events.LogRuntime, Summary: "snapshot", Detail: "debug detail", Error: failure, ResultStatus: events.ResultFailed, Fields: []slog.Attr{slog.Any("payload", values)}})
	values["nested"].([]any)[0] = "changed"
	failure.message, failure.detail = "changed error", "changed diagnostic"
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
	if !strings.Contains(entries[0].Fields["error"], "original diagnostic") || !strings.Contains(entries[0].Fields["error"], "original error") || entries[0].Fields["result_status"] != "failed" {
		t.Fatalf("lost error snapshot: %+v", entries)
	}
}

type mutableDiagnosticError struct{ message, detail string }

func (e *mutableDiagnosticError) Error() string { return e.message }
func (e *mutableDiagnosticError) LogDiagnostic() events.LogDiagnostic {
	return events.LogDiagnostic{Kind: "upstream", Detail: e.detail}
}

func TestGlobalResultAndErrorFieldsPreserveSeverityAndDetailPolicy(t *testing.T) {
	for _, configuredLevel := range []string{"debug", "info", "warn", "error"} {
		t.Run(configuredLevel, func(t *testing.T) {
			manager := testManager(t, configuredLevel)
			failure := &mutableDiagnosticError{message: "call failed", detail: `{"description":"diagnostic evidence","api_key":"secret-value"}`}
			for _, category := range logCategories {
				for _, fact := range []struct {
					name   string
					level  slog.Level
					status events.ResultStatus
				}{
					{"attempt", slog.LevelWarn, events.ResultFailed},
					{"cancellation", slog.LevelError, events.ResultCanceled},
					{"rejection", slog.LevelInfo, events.ResultRejected},
				} {
					emitRecord(t, events.LogRecord{Category: category, Name: fact.name, Level: fact.level, ResultStatus: fact.status, Error: failure,
						Fields: []slog.Attr{slog.String("result_status", "forged"), slog.Group("", slog.String("error", "forged"), slog.String("result_status", "forged"))}})
				}
			}
			// Unmigrated producers keep their existing fields until step 2.
			emitRecord(t, events.LogRecord{Category: events.LogAudit, Name: "existing_error", Fields: []slog.Attr{slog.String("error", "existing failure")}})
			// No declared result must remain absent, even with a conflicting free field.
			emitRecord(t, events.LogRecord{Category: events.LogAudit, Name: "progress", Fields: []slog.Attr{slog.String("result_status", "succeeded")}})
			if err := manager.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, prefix := range []string{"elbot", "audit", "elnis"} {
				entries, err := (Reader{Dir: manager.LogDir()}).Query(context.Background(), LogQuery{Prefix: prefix, Limit: 20})
				if err != nil {
					t.Fatal(err)
				}
				wantCount := 3
				if prefix == "audit" {
					wantCount += 2
				}
				if prefix == "elbot" && configuredLevel == "warn" {
					wantCount = 2
				}
				if prefix == "elbot" && configuredLevel == "error" {
					wantCount = 1
				}
				if len(entries) != wantCount {
					t.Fatalf("%s entries=%+v", prefix, entries)
				}
				for _, entry := range entries {
					name := entry.Fields["event"]
					if name == "existing_error" {
						if entry.Fields["error"] != "existing failure" {
							t.Fatal(entry)
						}
						continue
					}
					if name == "progress" {
						if _, present := entry.Fields["result_status"]; present {
							t.Fatal(entry)
						}
						if _, present := entry.Fields["error"]; present {
							t.Fatal(entry)
						}
						continue
					}
					wantLevel := map[string]string{"attempt": "WARN", "cancellation": "ERROR", "rejection": "INFO"}[name]
					wantStatus := map[string]string{"attempt": "failed", "cancellation": "canceled", "rejection": "rejected"}[name]
					if entry.Level != wantLevel || entry.Fields["result_status"] != wantStatus || !strings.Contains(entry.Fields["error"], "call failed") || strings.Contains(entry.Raw, "forged") || strings.Contains(entry.Raw, "secret-value") {
						t.Fatalf("facts overwritten or secret leaked: %+v", entry)
					}
					wantDetails := prefix != "elbot" || configuredLevel == "debug"
					if strings.Contains(entry.Fields["error"], "diagnostic evidence") != wantDetails {
						t.Fatalf("detail policy: %+v", entry)
					}
				}
			}
		})
	}
}

func TestResultErrorFieldsSurviveRecordLimits(t *testing.T) {
	manager := testManager(t, "info")
	failure := &mutableDiagnosticError{message: strings.Repeat("失败", 10000), detail: `{"token":"secret-value","text":"` + strings.Repeat("详情", 10000) + `"}`}
	fields := make([]slog.Attr, 40)
	for i := range fields {
		fields[i] = slog.String(fmt.Sprintf("payload_%d", i), strings.Repeat("x", 8000))
	}
	emitRecord(t, events.LogRecord{Category: events.LogAudit, Level: slog.LevelError, Name: "large_failure", ResultStatus: events.ResultFailed, Error: failure, Fields: fields})
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := (Reader{Dir: manager.LogDir()}).Query(context.Background(), LogQuery{Prefix: "audit", Fields: map[string]string{"event": "large_failure", "result_status": "failed"}})
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%+v error=%v", entries, err)
	}
	entry := entries[0]
	if len(entry.Raw) > maxRecordBytes || len(entry.Fields["error"]) > maxDetailBytes || !utf8.ValidString(entry.Raw) || !strings.Contains(entry.Fields["error"], truncatedMarker) || entry.Fields["truncated"] != "true" || strings.Contains(entry.Raw, "secret-value") {
		t.Fatalf("limits failed: errorBytes=%d recordBytes=%d fields=%v", len(entry.Fields["error"]), len(entry.Raw), entry.Fields)
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
