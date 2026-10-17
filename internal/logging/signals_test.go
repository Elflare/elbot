package logging

import (
	"bytes"
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
	"testing/synctest"
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

func readJSONRecords(t *testing.T, dir, prefix string) []map[string]json.RawMessage {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, prefix+"-*.log"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]json.RawMessage
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 {
			continue
		}
		if data[len(data)-1] != '\n' {
			t.Fatalf("missing final newline: %s", path)
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if len(line)+1 > maxRecordBytes {
				t.Fatalf("oversized line: %d", len(line)+1)
			}
			var entry map[string]json.RawMessage
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			entries = append(entries, entry)
		}
	}
	return entries
}

func TestGlobalRecordsUseIndependentLevelsAndContent(t *testing.T) {
	for _, configuredLevel := range []string{"debug", "info", "warn", "error"} {
		t.Run(configuredLevel, func(t *testing.T) {
			readStderr := captureStderr(t)
			m := testManager(t, configuredLevel)
			facts := []struct {
				name   string
				level  slog.Level
				status events.ResultStatus
			}{
				{"debug", slog.LevelDebug, events.ResultSucceeded},
				{"rejection", slog.LevelInfo, events.ResultRejected},
				{"attempt", slog.LevelWarn, events.ResultFailed},
				{"cancellation", slog.LevelError, events.ResultCanceled},
			}
			for _, category := range logCategories {
				for _, fact := range facts {
					emitRecord(t, events.LogRecord{Category: category, Level: fact.level, EventName: fact.name, Module: "test",
						Type: events.ContentJSON, Content: `{"reason":"diagnostic evidence","api_key":"secret-value","level":"forged","result_status":"forged"}`, ResultStatus: fact.status})
				}
			}
			if configuredLevel != "debug" {
				emitRecord(t, events.LogRecord{Category: events.LogRuntime, Level: slog.LevelDebug, Type: events.ContentJSON, Content: "\"" + strings.Repeat("x", maxRecordBytes) + "\""})
			}
			if err := m.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := readStderr(); got != "" {
				t.Fatalf("filtered record reached formatter: %s", got)
			}
			for _, category := range logCategories {
				prefix := category.String()
				if category == events.LogRuntime {
					prefix = "elbot"
				}
				entries := readJSONRecords(t, m.LogDir(), prefix)
				var expected []int
				for i, fact := range facts {
					if category != events.LogRuntime || fact.level >= parseLevel(configuredLevel) {
						expected = append(expected, i)
					}
				}
				if len(entries) != len(expected) {
					t.Fatalf("%s: %d entries, want %d", prefix, len(entries), len(expected))
				}
				for i, entry := range entries {
					fact := facts[expected[i]]
					if string(entry["event"]) != fmt.Sprintf("%q", fact.name) || string(entry["level"]) != fmt.Sprintf("%q", fact.level.String()) ||
						string(entry["result_status"]) != fmt.Sprintf("%q", fact.status.String()) || string(entry["category"]) != fmt.Sprintf("%q", category.String()) {
						t.Fatalf("business facts changed: %s", entry)
					}
					if string(entry["content"]) != `{"reason":"diagnostic evidence","api_key":"secret-value","level":"forged","result_status":"forged"}` {
						t.Fatalf("content differs by configured level: %s", entry)
					}
				}
			}
		})
	}
}

func TestGlobalRecordsSnapshotBeforeQueueConsumption(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := testManager(t, "debug")
		started, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
		if err := m.sinks[events.LogRuntime].queue.Submit(context.Background(), signal.Task{Shutdown: signal.Drain, Run: func(context.Context) error { close(started); <-release; return nil }}); err != nil {
			t.Fatal(err)
		}
		<-started
		value := "source-session"
		before := time.Now()
		emitRecord(t, events.LogRecord{Category: events.LogRuntime, Content: "original content", Identity: events.LogIdentity{SessionID: &value}})
		after := time.Now()
		value = "changed"
		// RFC3339 has second precision. Consume only after the next second so a
		// timestamp incorrectly taken by the worker cannot pass the assertion.
		time.Sleep(time.Until(after.Truncate(time.Second).Add(time.Second)))
		releaseOnce.Do(func() { close(release) })
		if err := m.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		entries := readJSONRecords(t, m.LogDir(), "elbot")
		if len(entries) != 1 {
			t.Fatal(entries)
		}
		var timestamp string
		if err := json.Unmarshal(entries[0]["time"], &timestamp); err != nil {
			t.Fatal(err)
		}
		at, err := time.Parse(time.RFC3339, timestamp)
		if err != nil {
			t.Fatal(err)
		}
		if at.Before(before.Truncate(time.Second)) || at.After(after) {
			t.Fatalf("not reception time: %s outside %s..%s", at, before, after)
		}
		if string(entries[0]["session_id"]) != "\"source-session\"" || string(entries[0]["content"]) != "\"original content\"" {
			t.Fatal(entries)
		}
	})
}

func TestOversizedRecordsReportAndContinue(t *testing.T) {
	readStderr := captureStderr(t)
	m := testManager(t, "error")
	emitRecord(t, events.LogRecord{Category: events.LogAudit, Type: events.ContentJSON, Content: "\"" + strings.Repeat("x", maxRecordBytes) + "\""})
	emitRecord(t, events.LogRecord{Category: events.LogAudit, Content: "following record"})
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := readJSONRecords(t, m.LogDir(), "audit")
	if len(entries) != 1 || string(entries[0]["content"]) != "\"following record\"" {
		t.Fatal(entries)
	}
	if got := readStderr(); !strings.Contains(got, "exceeds 65536 bytes") || strings.Count(got, "signal task failed") != 1 {
		t.Fatal(got)
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
	emitRecord(t, events.LogRecord{Category: events.LogAudit, Content: "one consumer"})
	if err := next.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := readJSONRecords(t, next.LogDir(), "audit")
	if len(entries) != 1 {
		t.Fatalf("entries=%v", entries)
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
		if err := signal.FlushDiagnostics(t.Context()); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
}

func TestGlobalWriteFailureReportsToStderrAndContinues(t *testing.T) {
	for _, failure := range []error{errors.New("disk write failed"), io.ErrShortWrite} {
		t.Run(failure.Error(), func(t *testing.T) {
			readStderr := captureStderr(t)
			var writes atomic.Int32
			m, err := newManager("error", t.TempDir(), 30, func(string) (io.WriteCloser, error) {
				return &controlledWriter{write: func(p []byte) (int, error) {
					if writes.Add(1) == 1 {
						if failure == io.ErrShortWrite {
							return len(p) - 1, nil
						}
						return 0, failure
					}
					return len(p), nil
				}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close(context.Background()) })
			var publications atomic.Int32
			connection, err := events.LogSubmitted.Observe(func(context.Context, events.LogRecord) error { publications.Add(1); return nil }, signal.ConnectOptions{Shutdown: signal.CancelPending})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			for range 2 {
				emitRecord(t, events.LogRecord{Category: events.LogAudit, Content: "business fact"})
			}
			if err := m.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if writes.Load() != 2 || publications.Load() != 2 {
				t.Fatalf("writes=%d publications=%d", writes.Load(), publications.Load())
			}
			if got := readStderr(); !strings.Contains(got, failure.Error()) || strings.Count(got, "signal task failed") != 1 {
				t.Fatal(got)
			}
		})
	}
}

func TestManagerReportsInvalidCategoryValue(t *testing.T) {
	m := testManager(t, "info")
	for _, category := range []events.LogCategory{0, 255} {
		// Invalid records are rejected at the publication boundary, before the
		// manager receives them; consumers do not repeat contract validation.
		err := events.EmitLog(context.Background(), events.LogRecord{Category: category})
		want := fmt.Sprintf("invalid log category: %d", category)
		if err == nil || err.Error() != want {
			t.Fatalf("category %d: got %v, want %s", category, err, want)
		}
	}
	emitRecord(t, events.LogRecord{Category: events.LogRuntime, EventName: "after_invalid", Content: "valid"})
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if records := readJSONRecords(t, m.LogDir(), "elbot"); len(records) != 1 || string(records[0]["event"]) != `"after_invalid"` {
		t.Fatalf("invalid records entered the consumer or blocked later records: %#v", records)
	}
}

func TestManagerRecordsTimeBeforeCapacityWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output bytes.Buffer
		started, release := make(chan struct{}), make(chan struct{})
		var writeOnce, releaseOnce sync.Once
		m, err := newManager("info", t.TempDir(), 30, func(prefix string) (io.WriteCloser, error) {
			if prefix != "audit" {
				return &controlledWriter{}, nil
			}
			return &controlledWriter{write: func(p []byte) (int, error) {
				writeOnce.Do(func() { close(started); <-release })
				return output.Write(p)
			}}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			releaseOnce.Do(func() { close(release) })
			if err := m.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
		emitRecord(t, events.LogRecord{Category: events.LogAudit, EventName: "active"})
		<-started
		for range 256 {
			emitRecord(t, events.LogRecord{Category: events.LogAudit, EventName: "queued"})
		}
		receivedAt := time.Now()
		done := make(chan error, 1)
		go func() {
			done <- events.EmitLog(context.Background(), events.LogRecord{Category: events.LogAudit, EventName: "waiting"})
		}()
		// Wait until the publisher is blocked inside the full queue. The fake
		// clock then advances without relying on real scheduler timing.
		synctest.Wait()
		time.Sleep(250 * time.Millisecond)
		select {
		case err := <-done:
			t.Fatalf("full queue did not block publication: %v", err)
		default:
		}
		releaseOnce.Do(func() { close(release) })
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if err := m.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSuffix(output.Bytes(), []byte("\n")), []byte("\n"))
		if len(lines) != 258 {
			t.Fatalf("got %d records, want 258", len(lines))
		}
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(lines[len(lines)-1], &entry); err != nil {
			t.Fatal(err)
		}
		if string(entry["event"]) != `"waiting"` || string(entry["time"]) != fmt.Sprintf("%q", receivedAt.Format(time.RFC3339)) {
			t.Fatalf("record timestamp changed while waiting for capacity: %s", lines[len(lines)-1])
		}
	})
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
		emitRecord(t, events.LogRecord{Category: events.LogAudit, EventName: "order", Type: events.ContentJSON, Content: fmt.Sprintf(`{"index":%d}`, i)})
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := readJSONRecords(t, m.LogDir(), "audit")
	if len(entries) != 100 {
		t.Fatalf("entries=%d", len(entries))
	}
	for i, entry := range entries {
		if string(entry["content"]) != fmt.Sprintf(`{"index":%d}`, i) {
			t.Fatal(entry)
		}
	}
}

func TestRecordPreservesContentUTF8AndSerializedLimit(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 34, 56, 0, time.FixedZone("test", 8*60*60))
	t.Run("text limits", func(t *testing.T) {
		for _, limit := range []int{0, 1, 2, 256, 512} {
			record := events.LogRecord{Category: events.LogRuntime, Content: strings.Repeat("中🙂", 300), ContentLimit: limit}
			data, err := formatRecord(context.Background(), record, at)
			if err != nil {
				t.Fatal(err)
			}
			var entry map[string]json.RawMessage
			if err := json.Unmarshal(data, &entry); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := json.Unmarshal(entry["content"], &got); err != nil {
				t.Fatal(err)
			}
			want := limit
			if want == 0 {
				want = defaultContentLimit
			}
			if !utf8.ValidString(got) || utf8.RuneCountInString(got) != want || !strings.HasSuffix(got, "…") {
				t.Fatalf("limit %d: %q", limit, got)
			}
			if string(entry["time"]) != "\"2026-10-08T12:34:56+08:00\"" || len(entry) != 7 {
				t.Fatalf("unexpected fields: %s", data)
			}
		}
	})
	t.Run("text stays text", func(t *testing.T) {
		for _, text := range []string{"", "中🙂", "line\n\"quoted\"", " { \"count\" : 9007199254740993 } "} {
			data, err := formatRecord(context.Background(), events.LogRecord{Category: events.LogRuntime, Content: text}, at)
			if err != nil {
				t.Fatal(err)
			}
			var entry map[string]json.RawMessage
			if err := json.Unmarshal(data, &entry); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := json.Unmarshal(entry["content"], &got); err != nil {
				t.Fatal(err)
			}
			if got != text || strings.Count(string(data), "\n") != 1 {
				t.Fatalf("%q became %s", text, data)
			}
		}
	})
	t.Run("text is only limited", func(t *testing.T) {
		data, err := formatRecord(context.Background(), events.LogRecord{Category: events.LogRuntime, Content: "reason: token=" + strings.Repeat("secret", 1000)}, at)
		if err != nil {
			t.Fatal(err)
		}
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(data, &entry); err != nil {
			t.Fatal(err)
		}
		var content string
		if err := json.Unmarshal(entry["content"], &content); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(content, "reason: token=secret") || utf8.RuneCountInString(content) != defaultContentLimit || !strings.HasSuffix(content, "…") {
			t.Fatalf("%s", data)
		}
	})
	t.Run("JSON types and precision", func(t *testing.T) {
		for _, text := range []string{"null", "true", "false", "9007199254740993123456789", "0.12345678901234567890123456789", "1e1000", "\"中🙂\"", "[1,\"two\",null]", "{\"count\":9007199254740993}", strings.Repeat("[", 64) + "9007199254740993" + strings.Repeat("]", 64)} {
			data, err := formatRecord(context.Background(), events.LogRecord{Category: events.LogAudit, Type: events.ContentJSON, Content: text, ContentLimit: 1}, at)
			if err != nil {
				t.Fatal(err)
			}
			var entry map[string]json.RawMessage
			if err := json.Unmarshal(data, &entry); err != nil {
				t.Fatal(err)
			}
			if string(entry["content"]) != text || string(entry["type"]) != "\"json\"" {
				t.Fatalf("%s became %s", text, data)
			}
		}
	})
	t.Run("JSON source order and public fields", func(t *testing.T) {
		text := `{"nested":[{"api_key":"nested-secret","authorization":"header-secret","cookie":"cookie-secret","image_data":"media-secret","text":"Bearer inline-secret","url":"data:image/png;base64,url-secret"}],"prompt_tokens":9007199254740993,"token_name":"business-name","event":"inner","level":"ERROR","result_status":"failed"}`
		values := []string{"session", "", "2", "request", "root"}
		record := events.LogRecord{Category: events.LogAudit, EventName: "outer", Module: "test", Type: events.ContentJSON, Content: text, ContentLimit: 1,
			ResultStatus: events.ResultCanceled, Identity: events.LogIdentity{SessionID: &values[0], RunID: &values[1], Attempt: &values[2], RequestID: &values[3], RootRequestID: &values[4]}}
		data, err := formatRecord(context.Background(), record, at)
		if err != nil {
			t.Fatal(err)
		}
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(data, &entry); err != nil {
			t.Fatal(err)
		}
		if string(entry["content"]) != text {
			t.Fatalf("source content or order changed: %s", data)
		}
		for key, want := range map[string]string{"event": "\"outer\"", "level": "\"INFO\"", "module": "\"test\"", "result_status": "\"canceled\"", "session_id": "\"session\"", "run_id": "\"\"", "attempt": "\"2\"", "request_id": "\"request\"", "root_request_id": "\"root\""} {
			if string(entry[key]) != want {
				t.Fatalf("%s: %s", key, data)
			}
		}
		if len(entry) != 13 {
			t.Fatalf("unexpected outer fields: %s", data)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry["content"], &fields); err != nil {
			t.Fatal(err)
		}
		if string(fields["prompt_tokens"]) != "9007199254740993" || string(fields["token_name"]) != "\"business-name\"" || string(fields["event"]) != "\"inner\"" {
			t.Fatalf("business data changed: %s", data)
		}
	})
	t.Run("serialized boundary", func(t *testing.T) {
		for _, value := range []events.LogContentType{events.ContentText, events.ContentJSON} {
			record := events.LogRecord{Category: events.LogAudit, Type: value, ContentLimit: maxRecordBytes}
			if value == events.ContentJSON {
				record.Content = "\"\""
			}
			data, err := formatRecord(context.Background(), record, at)
			if err != nil {
				t.Fatal(err)
			}
			text := strings.Repeat("x", maxRecordBytes-len(data))
			record.Content = text
			if value == events.ContentJSON {
				record.Content = "\"" + text + "\""
			}
			data, err = formatRecord(context.Background(), record, at)
			if err != nil || len(data) != maxRecordBytes || data[len(data)-1] != '\n' || !json.Valid(data) {
				t.Fatalf("boundary: len=%d err=%v", len(data), err)
			}
			record.Content = "x" + text
			if value == events.ContentJSON {
				record.Content = "\"" + record.Content + "\""
			}
			data, err = formatRecord(context.Background(), record, at)
			if err == nil || data != nil {
				t.Fatalf("oversize record accepted: len=%d err=%v", len(data), err)
			}
		}
	})
}
