package naming

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type namingDiagnosticError struct{}

func (namingDiagnosticError) Error() string {
	return "upstream failed: gateway detail"
}

func captureNamingDiagnostics(t *testing.T) *os.File {
	t.Helper()
	if err := signal.FlushDiagnostics(t.Context()); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	previousStderr, previousLogger := os.Stderr, slog.Default()
	os.Stderr = file
	slog.SetDefault(slog.New(slog.NewTextHandler(file, nil)))
	t.Cleanup(func() {
		if err := signal.FlushDiagnostics(context.Background()); err != nil {
			t.Error(err)
		}
		os.Stderr = previousStderr
		slog.SetDefault(previousLogger)
		_ = file.Close()
	})
	return file
}

func TestNamingLogFailureDoesNotChangeBusinessResult(t *testing.T) {
	file := captureNamingDiagnostics(t)
	connection, err := events.LogSubmitted.Observe(func(context.Context, events.LogRecord) error { return errors.New("log observer unavailable") }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	f := newFixture(t, 1, func(context.Context, llm.TextRequest) (llm.TextResult, error) {
		return llm.TextResult{Text: "saved title"}, nil
	})
	id := f.row(t, storage.Message{ID: storage.NewID(), Role: storage.RoleUser, Content: "user"})
	event := f.attempt(t, id)
	if event.ResultStatus != events.ResultSucceeded || event.Title == nil || *event.Title != "saved title" {
		t.Fatalf("log failure changed result: %+v", event)
	}
	row, err := f.store.Sessions().Get(t.Context(), id)
	if err != nil || row.Title != "saved title" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	if err := signal.FlushDiagnostics(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("signal must not diagnose business observer failures: %s", data)
	}
}

func TestNamingFailureContentSurvivesRuntimeLevel(t *testing.T) {
	for _, level := range []string{"info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			center, err := logging.NewManager(level, filepath.Join(t.TempDir(), "sessions.db"), 30)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = center.Close(context.Background()) })
			service, err := newTestService(Config{}, ServiceDeps{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.Close(context.Background()) })
			at := time.Now().Add(-time.Minute).Truncate(time.Second)
			sessionID, requestID := "source", "naming-request"
			ctx := contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "wrong", RequestID: "wrong"})
			service.notifyNamingCompleted(ctx, NamingCompletedEvent{
				Identity:    events.LogIdentity{SessionID: &sessionID, RequestID: &requestID},
				TriggeredAt: at, CompletedAt: time.Now(), Provider: "provider", Model: "model",
				ResultStatus: events.ResultFailed, GenerationResultStatus: events.ResultFailed,
				GenerationError: fmt.Errorf("wrapped: %w", namingDiagnosticError{}),
			})
			if err := center.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, prefix := range []string{"audit", "elbot"} {
				paths, err := filepath.Glob(filepath.Join(center.LogDir(), prefix+"-*.log"))
				if err != nil {
					t.Fatal(err)
				}
				if prefix == "elbot" && level == "error" {
					for _, path := range paths {
						data, err := os.ReadFile(path)
						if err != nil || len(strings.TrimSpace(string(data))) != 0 {
							t.Fatalf("WARN passed ERROR filter: %s %v", data, err)
						}
					}
					continue
				}
				if len(paths) != 1 {
					t.Fatalf("paths=%v err=%v", paths, err)
				}
				data, err := os.ReadFile(paths[0])
				if err != nil {
					t.Fatal(err)
				}
				var entry struct {
					Time                                      time.Time
					Level, SessionID, RequestID, ResultStatus string
					Content                                   json.RawMessage
				}
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(data, &raw); err != nil {
					t.Fatal(err)
				}
				for key, target := range map[string]any{"time": &entry.Time, "level": &entry.Level, "content": &entry.Content, "session_id": &entry.SessionID, "request_id": &entry.RequestID, "result_status": &entry.ResultStatus} {
					if err := json.Unmarshal(raw[key], target); err != nil {
						t.Fatal(err)
					}
				}
				if entry.Level != "WARN" || !entry.Time.After(at) || entry.SessionID != sessionID || entry.RequestID != requestID || entry.ResultStatus != "failed" {
					t.Fatalf("lost facts: %+v", entry)
				}
				if !strings.Contains(string(entry.Content), "wrapped: "+(namingDiagnosticError{}).Error()) {
					t.Fatalf("source diagnostic changed: %s", data)
				}
			}
		})
	}
}

func TestNamingOwnsSynchronousLogProjection(t *testing.T) {
	var records []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) error { records = append(records, r); return nil }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	service, err := newTestService(Config{}, ServiceDeps{})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(123, 0)
	sessionID, title := "source", "title"
	ctx := contextinfo.WithExecution(context.Background(), contextinfo.Execution{SessionID: "wrong", RequestID: "request"})
	service.notifyNamingCompleted(ctx, NamingCompletedEvent{Identity: events.LogIdentity{SessionID: &sessionID}, TriggeredAt: at, Title: &title, ResultStatus: events.ResultSucceeded})
	if len(records) != 1 || records[0].EventName != "session_naming_completed" || records[0].Identity.SessionID == nil || *records[0].Identity.SessionID != "source" || records[0].Identity.RequestID != nil {
		t.Fatal(records)
	}
	sessionID = "changed"
	if *records[0].Identity.SessionID != "source" {
		t.Fatal("identity aliased source")
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
